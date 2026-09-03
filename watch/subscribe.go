package watch

import "sync"

// Event reports the state of one target after a completed check attempt.
//
// Events are published after every attempt, including failed ones.
type Event struct {
	// ID is the id of the target the event belongs to.
	ID string

	// State is the state of the target after the attempt.
	State State

	// seq is a per-watcher monotonic sequence number. It orders coalesced
	// events of different targets by their true occurrence order when
	// several of them are delivered together. It is managed by the watcher
	// and is not part of the public data model.
	seq uint64
}

// subscription is a single event stream subscriber.
//
// Events are coalesced per target: pending holds at most one event per target
// id, so a busy target supersedes only its own older events, never the
// pending events of other targets. Together with the single event that the
// dispatcher is currently delivering, the memory a subscription needs stays
// bounded by the number of registered targets.
//
// The dispatcher goroutine of a subscription owns out and is the only code
// that sends on it or closes it.
type subscription struct {
	mu sync.Mutex
	// pending holds the newest event of every target that has not been
	// taken for delivery yet.
	pending map[string]Event
	// stopped reports that the subscription ended and no longer accepts
	// events.
	stopped bool

	// wake signals the dispatcher that pending may hold events. It is
	// buffered, and publishers signal it without blocking.
	wake chan struct{}
	// stop ends the dispatcher. It is closed exactly once, by end.
	stop chan struct{}
	// out is the event stream handed to the consumer. It is closed by the
	// dispatcher when it exits.
	out chan Event
}

// newSubscription creates a subscription. The caller must run dispatch in its
// own goroutine.
func newSubscription() *subscription {
	return &subscription{
		pending: make(map[string]Event),
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		out:     make(chan Event),
	}
}

// publish stores the newest event of a target and wakes the dispatcher.
//
// It never blocks: a consumer that stopped reading cannot slow down the
// worker that publishes, and the event of one target never evicts the pending
// event of another target. Events of an ended subscription are dropped.
func (s *subscription) publish(event Event) {
	s.mu.Lock()

	if s.stopped {
		s.mu.Unlock()

		return
	}

	s.pending[event.ID] = cloneEvent(event)

	s.mu.Unlock()

	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// dispatch delivers pending events until the subscription ends, then closes
// the event channel.
//
// Pending events are delivered oldest first, ordered by their sequence
// numbers instead of by target id, so coalesced events of different targets
// keep their occurrence order. Every send is interruptible: a consumer that
// stopped reading cannot keep the dispatcher from exiting.
func (s *subscription) dispatch() {
	defer close(s.out)

	for {
		select {
		case <-s.wake:
		case <-s.stop:
			return
		}

		for {
			event, ok := s.take()
			if !ok {
				break
			}

			select {
			case s.out <- event:
			case <-s.stop:
				return
			}
		}
	}
}

// take removes and returns the oldest pending event, keeping the newest event
// of every other target pending. The boolean result reports that an event was
// pending.
func (s *subscription) take() (Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.pending) == 0 {
		return Event{}, false
	}

	var (
		oldestID  string
		oldestSeq uint64
		found     bool
	)

	for id, event := range s.pending {
		if !found || event.seq < oldestSeq {
			oldestID, oldestSeq, found = id, event.seq, true
		}
	}

	event := s.pending[oldestID]
	delete(s.pending, oldestID)

	return event, true
}

// end stops the subscription and drops its undelivered events.
//
// It is idempotent and safe to call concurrently with publish and dispatch.
// The dispatcher exits promptly, without waiting for a consumer, and closes
// the event channel.
func (s *subscription) end() {
	s.mu.Lock()

	if s.stopped {
		s.mu.Unlock()

		return
	}

	s.stopped = true
	stop := s.stop

	s.mu.Unlock()

	close(stop)
}

// Subscribe returns the event stream of the watcher and the function that
// ends the subscription.
//
// Events are published after every attempt of every target, including failed
// ones. The channel is unbuffered and is closed when the subscription ends,
// either through unsubscribe or when the watcher stops; ranging over it is
// the intended consumption pattern.
//
// Events are coalesced per target: while a consumer is not reading, the
// newest event of a target supersedes its older pending events, and the
// pending events of other targets are never touched. A subscription
// therefore holds at most one pending event per target, plus the single
// event that is being delivered at that moment. Because events are delivered
// oldest first, the last event a consumer receives for a target always
// carries the newest state of that target, but the event that was already in
// delivery may additionally report an older state of it.
//
// A slow consumer only delays its own delivery: the watcher keeps checking,
// other subscribers keep receiving, and both unsubscribe and watcher shutdown
// finish without waiting for it. Delivery of every pending event is
// therefore not promised; ending a subscription or the watcher may drop
// events that were not delivered yet.
//
// Calling unsubscribe more than once is safe. Every delivered event is a deep
// copy of the watcher state and is never shared with another subscriber.
func (w *Watcher) Subscribe() (<-chan Event, func()) {
	sub := newSubscription()

	w.mu.Lock()

	finished := w.finished
	if !finished {
		w.subs[sub] = struct{}{}
	}

	w.mu.Unlock()

	if finished {
		// The watcher has already finished: the stream ends immediately.
		sub.end()
	}

	go sub.dispatch()

	unsubscribe := func() {
		w.mu.Lock()
		delete(w.subs, sub)
		w.mu.Unlock()

		sub.end()
	}

	return sub.out, unsubscribe
}

// stopSubscriptions ends every subscription of the watcher.
//
// Dispatchers exit promptly instead of waiting for their consumers, so a
// consumer that stopped reading cannot delay the watcher shutdown.
func (w *Watcher) stopSubscriptions() {
	w.mu.Lock()

	w.finished = true

	subs := make([]*subscription, 0, len(w.subs))
	for sub := range w.subs {
		subs = append(subs, sub)
	}

	w.mu.Unlock()

	for _, sub := range subs {
		sub.end()
	}
}
