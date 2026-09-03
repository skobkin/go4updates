package watch

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"

	updates "github.com/skobkin/go4updates"
)

func TestRunChecksTargetImmediately(t *testing.T) {
	source := &stubSource{feed: testFeed("v1.1.0")}

	w, _ := newTestWatcher(t, nil, Options{})

	if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() >= 1 })

	state, ok := w.State("application")
	if !ok {
		t.Fatal("State reported no state after the immediate check")
	}

	if state.Result == nil || state.Result.Latest == nil || state.Result.Latest.Version != "v1.1.0" {
		t.Fatalf("State.Result = %+v, want the fetched release", state.Result)
	}

	if state.Result.Status != updates.StatusUpdateAvailable {
		t.Errorf("Status = %v, want %v", state.Result.Status, updates.StatusUpdateAvailable)
	}

	if state.LastError != nil {
		t.Errorf("LastError = %v, want nil", state.LastError)
	}

	if state.LastAttemptAt.IsZero() {
		t.Error("LastAttemptAt is zero, want a timestamp")
	}

	stopWatcher(t, ctx, cancel, done)
}

func TestRunReturnsNilOnCancellation(t *testing.T) {
	source := &stubSource{feed: testFeed("v1.1.0")}

	w, _ := newTestWatcher(t, nil, Options{})

	if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() >= 1 })

	stopWatcher(t, ctx, cancel, done)
}

func TestRunWithCanceledContextReturnsNil(t *testing.T) {
	source := &stubSource{feed: testFeed("v1.1.0")}

	w, _ := newTestWatcher(t, nil, Options{})

	if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stopWatcher(t, ctx, cancel, startWatcher(t, w, ctx))

	if states := w.States(); len(states) != 0 {
		t.Errorf("States = %d entries, want none: a canceled context must not trigger a check", len(states))
	}
}

func TestRunWithoutTargetsBlocksUntilCancellation(t *testing.T) {
	w, _ := newTestWatcher(t, nil, Options{})

	ctx, cancel := context.WithCancel(context.Background())

	stopWatcher(t, ctx, cancel, startWatcher(t, w, ctx))
}

func TestPerTargetIntervalsAreIndependent(t *testing.T) {
	fast := &stubSource{feed: testFeed("v1.0.1")}
	slow := &stubSource{feed: testFeed("v2.0.1")}

	w, factory := newTestWatcher(t, nil, Options{Interval: time.Hour})

	if err := w.Add("fast", updates.Target{CurrentVersion: "1.0.0", Source: fast}, WithInterval(10*time.Millisecond)); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	if err := w.Add("slow", updates.Target{CurrentVersion: "2.0.0", Source: slow}); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	waitFor(t, 2*time.Second, func() bool { return fast.callsSoFar() == 1 && slow.callsSoFar() == 1 })

	factory.at(10 * time.Millisecond).tick()
	waitFor(t, 2*time.Second, func() bool { return fast.callsSoFar() == 2 })

	if slow.callsSoFar() != 1 {
		t.Errorf("slow target was checked %d times, want 1: the fast interval must not leak", slow.callsSoFar())
	}

	factory.at(time.Hour).tick()
	waitFor(t, 2*time.Second, func() bool { return slow.callsSoFar() == 2 })

	if fast.callsSoFar() != 2 {
		t.Errorf("fast target was checked %d times, want 2: the slow interval must not leak", fast.callsSoFar())
	}

	stopWatcher(t, ctx, cancel, done)
}

func TestWatcherWaitsIntervalAfterSlowAttempt(t *testing.T) {
	// An attempt that outlasts its interval must not turn into an immediate
	// re-check: the tick that fired while it was running is dropped and the
	// source is approached again only one full interval later.
	source := newGatedSource(testFeed("v1.1.0"))

	w, factory := newTestWatcher(t, nil, Options{})

	if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}, WithInterval(2*time.Hour)); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	events, unsubscribe := w.Subscribe()
	defer unsubscribe()

	collector := collectEvents(t, events)

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	// The immediate check blocks in the source.
	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() == 1 })

	// The interval elapses while the attempt is still running.
	factory.at(2 * time.Hour).tick()

	source.release()

	waitFor(t, 2*time.Second, func() bool { return collector.count() >= 1 })

	if calls := source.callsSoFar(); calls != 1 {
		t.Fatalf("slow source was approached %d times right after its attempt, want 1", calls)
	}

	// The next check waits for the next interval.
	factory.at(2 * time.Hour).tick()
	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() == 2 })

	stopWatcher(t, ctx, cancel, done)
}

func TestWatcherRearmsSchedulerAfterEveryAttempt(t *testing.T) {
	// A purely periodic schedule keeps its phase from watcher start, so an
	// attempt finishing shortly before the next scheduled tick would be
	// followed by a nearly immediate re-check. The scheduler is therefore
	// re-armed once per completed attempt instead of ticking periodically.
	source := &stubSource{feed: testFeed("v1.1.0")}

	w, factory := newTestWatcher(t, nil, Options{})

	if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}, WithInterval(time.Hour)); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	// The immediate check re-arms the scheduler when it completes, before
	// any interval has elapsed.
	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() == 1 })
	waitFor(t, 2*time.Second, func() bool { return factory.at(time.Hour).resetCount() >= 1 })

	scheduler := factory.at(time.Hour)

	// The next check happens only after the re-armed interval.
	scheduler.tick()
	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() == 2 })
	waitFor(t, 2*time.Second, func() bool { return scheduler.resetCount() >= 2 })

	stopWatcher(t, ctx, cancel, done)
}

func TestFailedAttemptPreservesPreviousResult(t *testing.T) {
	source := &stubSource{feed: testFeed("v1.1.0")}

	w, factory := newTestWatcher(t, nil, Options{})

	if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}, WithInterval(10*time.Millisecond)); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() == 1 })

	fetchErr := errors.New("provider unavailable")
	source.fail(fetchErr)

	factory.at(10 * time.Millisecond).tick()
	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() == 2 })

	state, ok := w.State("application")
	if !ok {
		t.Fatal("State reported no state after the failed attempt")
	}

	if !errors.Is(state.LastError, fetchErr) {
		t.Errorf("LastError = %v, want a wrapped %v", state.LastError, fetchErr)
	}

	if state.Result == nil {
		t.Fatal("Result = nil, want the preserved result of the last successful check")
	}

	if state.Result.Latest == nil || state.Result.Latest.Version != "v1.1.0" {
		t.Errorf("Result.Latest = %+v, want the preserved v1.1.0 release", state.Result.Latest)
	}

	if state.LastAttemptAt.Before(state.Result.CheckedAt) {
		t.Errorf("LastAttemptAt %v is older than the preserved CheckedAt %v", state.LastAttemptAt, state.Result.CheckedAt)
	}

	stopWatcher(t, ctx, cancel, done)
}

func TestAddRejectsEmptyID(t *testing.T) {
	w := New(nil, Options{})

	if err := w.Add("", updates.Target{}); !errors.Is(err, ErrEmptyID) {
		t.Fatalf("Add error = %v, want ErrEmptyID", err)
	}
}

func TestAddRejectsDuplicateID(t *testing.T) {
	w := New(nil, Options{})

	if err := w.Add("application", updates.Target{}); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	if err := w.Add("application", updates.Target{}); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("Add error = %v, want ErrDuplicateID", err)
	}
}

func TestAddAfterStartIsRejected(t *testing.T) {
	source := &stubSource{feed: testFeed("v1.1.0")}

	w, _ := newTestWatcher(t, nil, Options{})

	if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() >= 1 })

	if err := w.Add("other", updates.Target{CurrentVersion: "2.0.0", Source: source}); !errors.Is(err, ErrWatcherStarted) {
		t.Errorf("Add error = %v, want ErrWatcherStarted", err)
	}

	stopWatcher(t, ctx, cancel, done)

	if err := w.Add("other", updates.Target{CurrentVersion: "2.0.0", Source: source}); !errors.Is(err, ErrWatcherStarted) {
		t.Errorf("Add after finish error = %v, want ErrWatcherStarted", err)
	}
}

func TestSecondRunIsRejected(t *testing.T) {
	source := &stubSource{feed: testFeed("v1.1.0")}

	w, _ := newTestWatcher(t, nil, Options{})

	if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() >= 1 })

	// A concurrent second Run must fail immediately instead of starting a
	// second set of workers.
	if err := w.Run(ctx); !errors.Is(err, ErrWatcherStarted) {
		t.Errorf("concurrent Run error = %v, want ErrWatcherStarted", err)
	}

	stopWatcher(t, ctx, cancel, done)

	if err := w.Run(ctx); !errors.Is(err, ErrWatcherStarted) {
		t.Errorf("Run after finish error = %v, want ErrWatcherStarted", err)
	}
}

func TestDefaultsResolveAtUseTime(t *testing.T) {
	w := New(nil, Options{})

	if got := w.timeout(); got != defaultTimeout {
		t.Errorf("timeout = %s, want %s", got, defaultTimeout)
	}

	if got := w.intervalFor(watchedTarget{id: "application"}); got != defaultInterval {
		t.Errorf("interval = %s, want %s", got, defaultInterval)
	}
}

func TestIntervalResolutionPrecedence(t *testing.T) {
	w := New(nil, Options{Interval: time.Hour, Timeout: 2 * time.Second})

	if got := w.timeout(); got != 2*time.Second {
		t.Errorf("timeout = %s, want %s", got, 2*time.Second)
	}

	overridden := watchedTarget{id: "application", interval: 5 * time.Minute}
	if got := w.intervalFor(overridden); got != 5*time.Minute {
		t.Errorf("interval = %s, want %s", got, 5*time.Minute)
	}

	// A non-positive override falls back to the watcher-wide interval.
	fallback := watchedTarget{id: "application", interval: -time.Minute}
	if got := w.intervalFor(fallback); got != time.Hour {
		t.Errorf("interval = %s, want %s", got, time.Hour)
	}
}

func TestRunDoesNotLeakGoroutines(t *testing.T) {
	baseline := runtime.NumGoroutine()

	source := &stubSource{feed: testFeed("v1.1.0")}

	w, _ := newTestWatcher(t, nil, Options{})

	for i := range 5 {
		err := w.Add(fmt.Sprintf("target-%d", i), updates.Target{CurrentVersion: "1.0.0", Source: source})
		if err != nil {
			t.Fatalf("Add returned error: %v", err)
		}
	}

	events, unsubscribe := w.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() >= 5 })

	consumerFinished := make(chan struct{})

	go func() {
		defer close(consumerFinished)

		for range events {
		}
	}()

	stopWatcher(t, ctx, cancel, done)
	unsubscribe()

	select {
	case <-consumerFinished:
	case <-time.After(5 * time.Second):
		t.Fatal("event stream was not closed after shutdown")
	}

	waitForGoroutineCount(t, baseline)
}

func TestShutdownClosesEveryEventStreamExactlyOnce(t *testing.T) {
	for i := range 20 {
		source := &stubSource{feed: testFeed("v1.1.0")}

		w, _ := newTestWatcher(t, nil, Options{})

		if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}); err != nil {
			t.Fatalf("iteration %d: Add returned error: %v", i, err)
		}

		events, unsubscribe := w.Subscribe()

		consumerFinished := make(chan struct{})

		go func() {
			defer close(consumerFinished)

			for range events {
			}
		}()

		ctx, cancel := context.WithCancel(context.Background())
		done := startWatcher(t, w, ctx)

		waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() >= 1 })

		cancel()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("iteration %d: Run returned error: %v", i, err)
			}

		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: Run did not return after cancellation", i)
		}

		// Repeated unsubscribes and a consumer that is still ranging over
		// the stream must not panic on a double close or on a send to a
		// closed channel.
		unsubscribe()
		unsubscribe()

		select {
		case <-consumerFinished:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: event stream was not closed after shutdown", i)
		}
	}
}
