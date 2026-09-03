package watch

import (
	"context"
	"errors"
	"testing"
	"time"

	updates "github.com/skobkin/go4updates"
)

// mustStream subscribes to the events of w and unsubscribes when the test
// ends.
func mustStream(t *testing.T, w *Watcher) <-chan Event {
	t.Helper()

	events, unsubscribe := w.Subscribe()
	t.Cleanup(unsubscribe)

	return events
}

func TestCoalescingKeepsPendingEventsOfOtherTargets(t *testing.T) {
	// Three targets publish in a controlled order: the event of the plug
	// target is the oldest one, so the dispatcher takes it for delivery and
	// the events of the other two targets stay pending while nobody reads.
	plug := newGatedSource(testFeed("v0.9.0"))
	other := newGatedSource(testFeed("v1.5.0"))
	busy := newGatedSource(testFeed("v2.5.0"))

	w, factory := newTestWatcher(t, nil, Options{Interval: time.Hour})

	if err := w.Add("plug", updates.Target{CurrentVersion: "0.8.0", Source: plug}); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	if err := w.Add("other", updates.Target{CurrentVersion: "1.0.0", Source: other}, WithInterval(2*time.Hour)); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	if err := w.Add("busy", updates.Target{CurrentVersion: "2.0.0", Source: busy}, WithInterval(10*time.Millisecond)); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	events := mustStream(t, w)

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	plug.release()
	waitFor(t, 2*time.Second, func() bool { return publishedEvents(w) == 1 })

	other.release()
	waitFor(t, 2*time.Second, func() bool { return publishedEvents(w) == 2 })

	busy.release()
	waitFor(t, 2*time.Second, func() bool { return publishedEvents(w) == 3 })

	// The event of plug is being delivered, the other two are pending.
	sub := activeSubscription(t, w)

	waitFor(t, 2*time.Second, func() bool {
		return pendingCount(sub) == 2 && pendingSeq(sub, "other") == 1 && pendingSeq(sub, "busy") == 2
	})

	// Four more attempts of the busy target must collapse into its single
	// pending event and must not touch the pending event of other. The
	// attempts are driven one interval at a time: a target waits its
	// interval after a completed attempt instead of consuming queued ticks
	// back to back.
	seq := uint64(2)
	for range 4 {
		factory.at(10 * time.Millisecond).tick()
		seq++

		waitFor(t, 2*time.Second, func() bool { return pendingSeq(sub, "busy") == seq })
	}

	if got := pendingSeq(sub, "other"); got != 1 {
		t.Errorf("other pending sequence = %d, want 1: the busy target must not evict it", got)
	}

	if got := pendingCount(sub); got != 2 {
		t.Errorf("pending events = %d, want 2 (one per target)", got)
	}

	// Delivery follows the occurrence order: the event that was already in
	// delivery, then the pending events oldest first. The intermediate
	// states of the busy target are gone.
	delivered := make([]Event, 0, 3)
	finished := make(chan struct{})

	go func() {
		defer close(finished)

		for event := range events {
			delivered = append(delivered, event)

			if len(delivered) == 3 {
				return
			}
		}
	}()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("did not receive the pending events")
	}

	wantIDs := []string{"plug", "other", "busy"}
	wantSequences := []uint64{0, 1, 6}

	for i, want := range wantIDs {
		if delivered[i].ID != want || delivered[i].seq != wantSequences[i] {
			t.Errorf("event %d = %q (seq %d), want %q (seq %d)", i, delivered[i].ID, delivered[i].seq, want, wantSequences[i])
		}
	}

	stopWatcher(t, ctx, cancel, done)
}

func TestSlowConsumerDoesNotBlockAnything(t *testing.T) {
	source := &stubSource{feed: testFeed("v1.1.0")}

	w, factory := newTestWatcher(t, nil, Options{})

	if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}, WithInterval(5*time.Millisecond)); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	// Nobody ever reads this stream.
	events, unsubscribe := w.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	sub := activeSubscription(t, w)

	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() >= 1 })

	if got := pendingCount(sub); got > 1 {
		t.Errorf("pending events = %d, want at most 1: coalescing must bound the subscription", got)
	}

	// Checks keep happening while nobody reads.
	factory.at(5 * time.Millisecond).tick()
	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() >= 2 })

	factory.at(5 * time.Millisecond).tick()
	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() >= 3 })

	if got := pendingCount(sub); got > 1 {
		t.Errorf("pending events = %d after three attempts, want at most 1", got)
	}

	shutdownStarted := time.Now()

	stopWatcher(t, ctx, cancel, done)

	if elapsed := time.Since(shutdownStarted); elapsed > 2*time.Second {
		t.Errorf("shutdown took %s while a consumer was silent", elapsed)
	}

	unsubscribeStarted := time.Now()

	unsubscribe()
	unsubscribe()

	if elapsed := time.Since(unsubscribeStarted); elapsed > time.Second {
		t.Errorf("unsubscribe took %s while a consumer was silent", elapsed)
	}

	// The dispatcher closed the stream, so a consumer that starts after the
	// shutdown only observes the end of the stream.
	closed := make(chan struct{})

	go func() {
		defer close(closed)

		for range events {
		}
	}()

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("event stream was not closed after shutdown")
	}
}

func TestDispatcherSendIsInterruptibleByStop(t *testing.T) {
	sub := newSubscription()

	sub.publish(Event{ID: "application", State: State{}, seq: 1})

	finished := make(chan struct{})

	go func() {
		defer close(finished)

		sub.dispatch()
	}()

	// The dispatcher consumed its wake signal and took the only pending
	// event for delivery, so it is blocked sending to a consumer that never
	// reads.
	waitFor(t, 2*time.Second, func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()

		return len(sub.wake) == 0 && len(sub.pending) == 0
	})

	select {
	case <-finished:
		t.Fatal("dispatcher exited before the subscription was stopped")

	case <-time.After(50 * time.Millisecond):
	}

	sub.end()
	sub.end()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher did not exit when the subscription was stopped")
	}

	if _, ok := <-sub.out; ok {
		t.Error("event stream is open after the dispatcher exited, want it closed")
	}
}

func TestUnsubscribeIsIdempotent(t *testing.T) {
	source := &stubSource{feed: testFeed("v1.1.0")}

	w, _ := newTestWatcher(t, nil, Options{})

	if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	events, unsubscribe := w.Subscribe()

	unsubscribe()
	unsubscribe()

	collector := collectEvents(t, events)

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() >= 1 })

	stopWatcher(t, ctx, cancel, done)

	if got := collector.count(); got != 0 {
		t.Errorf("received %d events after unsubscribing, want 0", got)
	}
}

func TestEventsArePublishedAfterFailures(t *testing.T) {
	source := &stubSource{feed: testFeed("v1.1.0")}

	w, factory := newTestWatcher(t, nil, Options{})

	if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}, WithInterval(10*time.Millisecond)); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	collector := collectEvents(t, mustStream(t, w))

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	waitFor(t, 2*time.Second, func() bool { return collector.count() >= 1 })

	first := collector.event(0)
	if first.ID != "application" || first.State.LastError != nil || first.State.Result == nil {
		t.Errorf("first event = %+v, want a successful state", first)
	}

	fetchErr := errors.New("provider unavailable")
	source.fail(fetchErr)

	factory.at(10 * time.Millisecond).tick()

	waitFor(t, 2*time.Second, func() bool { return collector.count() >= 2 })

	second := collector.event(1)
	if !errors.Is(second.State.LastError, fetchErr) {
		t.Errorf("second event LastError = %v, want a wrapped %v", second.State.LastError, fetchErr)
	}

	if second.State.Result == nil || second.State.Result.Latest == nil || second.State.Result.Latest.Version != "v1.1.0" {
		t.Errorf("second event result = %+v, want the preserved result", second.State.Result)
	}

	stopWatcher(t, ctx, cancel, done)
}

func TestEventIsADeepCopy(t *testing.T) {
	source := &stubSource{feed: testFeed("v1.1.0", "v1.0.0")}

	w, _ := newTestWatcher(t, nil, Options{})

	if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	events := mustStream(t, w)

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	var received Event

	select {
	case received = <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("did not receive the event of the immediate check")
	}

	received.State.LastError = errors.New("mutated")
	received.State.Result.Feed.RepositoryURL = "https://mutated.example.org"
	received.State.Result.Feed.Releases[0].Version = "mutated"
	received.State.Result.Feed.Releases[0].Assets[0].Name = "mutated"
	received.State.Result.Feed.Releases = append(received.State.Result.Feed.Releases, updates.Release{Version: "appended"})

	stopWatcher(t, ctx, cancel, done)

	assertIntact(t, mustState(t, w, "application"))
}

func TestSubscribersReceiveIndependentCopies(t *testing.T) {
	source := &stubSource{feed: testFeed("v1.1.0", "v1.0.0")}

	w, _ := newTestWatcher(t, nil, Options{})

	if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	first := mustStream(t, w)
	second := mustStream(t, w)

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	var firstEvent, secondEvent Event

	select {
	case firstEvent = <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("the first subscriber did not receive the event")
	}

	select {
	case secondEvent = <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("the second subscriber did not receive the event")
	}

	firstEvent.State.Result.Feed.Releases[0].Version = "mutated"

	stopWatcher(t, ctx, cancel, done)

	if secondEvent.State.Result == nil || secondEvent.State.Result.Feed.Releases[0].Version != "v1.1.0" {
		t.Errorf("event of the second subscriber = %+v, want an untouched copy", secondEvent.State.Result)
	}

	assertIntact(t, mustState(t, w, "application"))
}

func TestSubscribeAfterFinishReturnsClosedStream(t *testing.T) {
	source := &stubSource{feed: testFeed("v1.1.0")}

	w, _ := newTestWatcher(t, nil, Options{})

	if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	stopWatcher(t, ctx, cancel, startWatcher(t, w, ctx))

	events, unsubscribe := w.Subscribe()
	defer unsubscribe()

	select {
	case _, ok := <-events:
		if ok {
			t.Error("received an event from a finished watcher")
		}

	case <-time.After(5 * time.Second):
		t.Fatal("event stream of a finished watcher was not closed")
	}
}
