// Package watch periodically checks a fixed set of update targets and keeps
// their last known state.
//
// A Watcher is created with New, filled with Add, and then started once with
// Run. Run blocks until its context is done; the package never starts
// background work on its own.
//
//	w := watch.New(nil, watch.Options{Interval: time.Hour})
//
//	if err := w.Add("application", updates.Target{
//		CurrentVersion: "1.2.3",
//		Source:         source,
//	}); err != nil {
//		return err
//	}
//
//	events, unsubscribe := w.Subscribe()
//	defer unsubscribe()
//
//	go func() {
//		for event := range events {
//			fmt.Printf("%s: %v\n", event.ID, event.State.LastError)
//		}
//	}()
//
//	if err := w.Run(ctx); err != nil {
//		return err
//	}
//
// Every target is checked once immediately when the watcher starts and then
// again one interval after each completed attempt, so a check that outlasts
// its interval is never followed by an immediate re-check. Each attempt is
// bounded by the configured timeout. The
// state of a target survives failed attempts: a failed check keeps the last
// successful updates.Result and refreshes only the attempt timestamp and the
// error.
//
// Subscriptions are non-blocking by construction. Events are coalesced per
// target, so the newest event of a target supersedes only its own older
// pending events, and the memory a subscription needs stays bounded by the
// number of registered targets. A consumer that stops reading therefore only
// delays its own delivery; it can never block the workers, other subscribers,
// or the watcher shutdown. Because of that, delivery of every pending event
// is not promised: ending a subscription or the watcher may drop events that
// were not delivered yet.
//
// The package performs no logging: failures surface through State.LastError
// and through events.
package watch

import (
	"context"
	"fmt"
	"sync"
	"time"

	updates "github.com/skobkin/go4updates"
)

const (
	// defaultInterval is used when neither the watcher nor the target
	// carries a positive interval.
	defaultInterval = 12 * time.Hour

	// defaultTimeout is used when no positive timeout is configured.
	defaultTimeout = 15 * time.Second
)

// Options configures a Watcher.
//
// Values are resolved when they are used, not when the watcher is created, so
// the zero value selects the package defaults.
type Options struct {
	// Interval is the time a target waits after a completed check before it
	// is checked again, starting with the immediate first check. A
	// non-positive value selects a 12 hour interval.
	Interval time.Duration

	// Timeout bounds a single check attempt of one target. A non-positive
	// value selects a 15 second timeout.
	Timeout time.Duration
}

// AddOption customizes the registration of a single target.
type AddOption func(*targetOptions)

// targetOptions holds the resolved settings of a single registration.
type targetOptions struct {
	interval time.Duration
}

// WithInterval overrides the watcher-wide check interval for the target it is
// applied to. A non-positive interval falls back to the watcher-wide
// interval.
func WithInterval(interval time.Duration) AddOption {
	return func(o *targetOptions) {
		o.interval = interval
	}
}

// watchedTarget is the resolved registration of a single target.
type watchedTarget struct {
	// id is the registration id of the target.
	id string

	// target is the target as it was registered.
	target updates.Target

	// interval is the per-target interval override, zero when unset.
	interval time.Duration
}

// Watcher periodically checks a fixed set of update targets and keeps their
// last known state.
//
// A Watcher is single use: it is configured with Add, started once with Run,
// and finishes when the Run context is done. Restart semantics are not
// supported. The last known state stays readable after the watcher has
// finished.
//
// All methods are safe for concurrent use.
type Watcher struct {
	checker *updates.Checker
	options Options

	// newTicker creates the ticker a target worker schedules itself with.
	// It is a seam for tests; production code always uses newRealTicker.
	newTicker func(time.Duration) ticker

	mu       sync.RWMutex
	targets  map[string]watchedTarget
	states   map[string]State
	subs     map[*subscription]struct{}
	started  bool
	finished bool
	seq      uint64
}

// ticker is the timer a target worker spaces its checks with. The watcher
// re-arms it after every completed attempt, so the wait before the next check
// is always a full interval instead of the leftover phase of a periodic
// schedule.
type ticker interface {
	// C returns the stream carrying one signal per scheduled interval.
	C() <-chan time.Time

	// Reset re-arms the timer to signal once after d, dropping a signal
	// that was pending.
	Reset(d time.Duration)

	// Stop releases the timer.
	Stop()
}

// realTicker adapts time.Timer to the ticker interface.
type realTicker struct {
	timer *time.Timer
}

// newRealTicker creates a time.Timer based ticker. The duration is always
// positive: the watcher resolves intervals at use time.
func newRealTicker(d time.Duration) ticker {
	return &realTicker{timer: time.NewTimer(d)}
}

// C returns the stream carrying one signal per scheduled interval.
func (t *realTicker) C() <-chan time.Time {
	return t.timer.C
}

// Reset re-arms the timer to signal once after d, dropping a signal that was
// pending.
func (t *realTicker) Reset(d time.Duration) {
	t.timer.Reset(d)
}

// Stop releases the timer.
func (t *realTicker) Stop() {
	t.timer.Stop()
}

// New creates a Watcher that checks targets with checker.
//
// A nil checker selects a zero-value updates.Checker, which compares versions
// with the default SemVer policy. Options with non-positive values select a
// 12 hour interval and a 15 second per-attempt timeout.
func New(checker *updates.Checker, opts Options) *Watcher {
	if checker == nil {
		checker = &updates.Checker{}
	}

	return &Watcher{
		checker:   checker,
		options:   opts,
		newTicker: newRealTicker,
		targets:   make(map[string]watchedTarget),
		states:    make(map[string]State),
		subs:      make(map[*subscription]struct{}),
	}
}

// Add registers a target under id.
//
// The id is mandatory and unique: an empty id returns ErrEmptyID and an id
// that is already registered returns ErrDuplicateID. Registration is only
// possible before the watcher starts; afterwards it returns ErrWatcherStarted.
//
// A target without a Source is accepted but fails every attempt, which
// surfaces as its State.LastError and as an event.
func (w *Watcher) Add(id string, target updates.Target, opts ...AddOption) error {
	if id == "" {
		return ErrEmptyID
	}

	var resolved targetOptions
	for _, opt := range opts {
		if opt == nil {
			continue
		}

		opt(&resolved)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.started {
		return ErrWatcherStarted
	}

	if _, exists := w.targets[id]; exists {
		return ErrDuplicateID
	}

	w.targets[id] = watchedTarget{id: id, target: target, interval: resolved.interval}

	return nil
}

// Run starts the watcher and blocks until ctx is done.
//
// Every target performs an immediate check when Run starts and is checked
// again one interval after each completed attempt. Each attempt runs under a
// timeout derived from Options.Timeout and from ctx. Run returns nil when ctx
// ends, whether through cancellation or through a deadline: the context ending
// is the normal way for a watcher to stop, not a failure.
//
// Run is single use. A second call returns ErrWatcherStarted, as does Add
// after Run. The lifecycle of a watcher is configured -> running -> finished.
func (w *Watcher) Run(ctx context.Context) error {
	w.mu.Lock()

	if w.started {
		w.mu.Unlock()

		return ErrWatcherStarted
	}

	w.started = true

	targets := make([]watchedTarget, 0, len(w.targets))
	for _, target := range w.targets {
		targets = append(targets, target)
	}

	w.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	for _, target := range targets {
		wg.Add(1)

		go func() {
			defer wg.Done()

			w.watchTarget(runCtx, target)
		}()
	}

	<-ctx.Done()

	cancel()
	wg.Wait()

	w.stopSubscriptions()

	return nil
}

// watchTarget checks a single target immediately and then again one interval
// after each completed attempt until ctx is done.
func (w *Watcher) watchTarget(ctx context.Context, target watchedTarget) {
	scheduler := w.newTicker(w.intervalFor(target))
	defer scheduler.Stop()

	if ctx.Err() == nil {
		w.attempt(ctx, target)
	}

	for {
		// The scheduler is re-armed after every completed attempt: the wait
		// before the next check is always a full interval, never the
		// leftover phase of a periodic schedule, and a signal that fired
		// while the attempt was running is dropped.
		scheduler.Reset(w.intervalFor(target))

		select {
		case <-ctx.Done():
			return

		case <-scheduler.C():
			w.attempt(ctx, target)
		}
	}
}

// attempt performs a single check of a target and records its outcome.
//
// A failed attempt keeps the last successful result of the target: only the
// attempt timestamp and the error are refreshed. An event is published after
// every attempt, including failed ones. Publishing happens outside the
// watcher lock and never blocks.
func (w *Watcher) attempt(ctx context.Context, target watchedTarget) {
	attemptCtx, cancel := context.WithTimeout(ctx, w.timeout())
	defer cancel()

	result, err := w.checker.Check(attemptCtx, target.target)

	w.mu.Lock()

	state := State{LastAttemptAt: time.Now()}

	if err != nil {
		state.LastError = fmt.Errorf("go4updates: watch: checking target %q: %w", target.id, err)

		if previous := w.states[target.id]; previous.Result != nil {
			state.Result = previous.Result
		}
	} else {
		state.Result = &result
	}

	stored := cloneState(state)
	w.states[target.id] = stored

	event := Event{ID: target.id, State: stored, seq: w.seq}
	w.seq++

	subscribers := make([]*subscription, 0, len(w.subs))
	for sub := range w.subs {
		subscribers = append(subscribers, sub)
	}

	w.mu.Unlock()

	for _, sub := range subscribers {
		sub.publish(event)
	}
}

// intervalFor resolves the check interval of a target at use time. The
// per-target override wins over the watcher-wide interval, which wins over
// the package default.
func (w *Watcher) intervalFor(target watchedTarget) time.Duration {
	if target.interval > 0 {
		return target.interval
	}

	if w.options.Interval > 0 {
		return w.options.Interval
	}

	return defaultInterval
}

// timeout resolves the per-attempt timeout at use time.
func (w *Watcher) timeout() time.Duration {
	if w.options.Timeout > 0 {
		return w.options.Timeout
	}

	return defaultTimeout
}
