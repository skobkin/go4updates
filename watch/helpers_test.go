package watch

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	updates "github.com/skobkin/go4updates"
)

// stubSource returns a canned feed or error and counts Fetch calls. It is
// safe for concurrent use so tests can reconfigure it while a watcher runs.
type stubSource struct {
	mu    sync.Mutex
	feed  updates.Feed
	err   error
	calls atomic.Int32
}

func (s *stubSource) Fetch(ctx context.Context, opts updates.FetchOptions) (updates.Feed, error) {
	s.calls.Add(1)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.err != nil {
		return updates.Feed{}, s.err
	}

	return s.feed, nil
}

// fail makes subsequent calls return err.
func (s *stubSource) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.err = err
}

// callsSoFar reports the number of Fetch calls so far.
func (s *stubSource) callsSoFar() int {
	return int(s.calls.Load())
}

// gatedSource blocks every Fetch until it is released and then serves a
// canned feed. Tests use it to control the order in which targets publish
// their events.
type gatedSource struct {
	once  sync.Once
	gate  chan struct{}
	feed  updates.Feed
	calls atomic.Int32
}

func newGatedSource(feed updates.Feed) *gatedSource {
	return &gatedSource{gate: make(chan struct{}), feed: feed}
}

func (s *gatedSource) Fetch(ctx context.Context, opts updates.FetchOptions) (updates.Feed, error) {
	s.calls.Add(1)

	select {
	case <-s.gate:
		return s.feed, nil

	case <-ctx.Done():
		return updates.Feed{}, ctx.Err()
	}
}

// release lets the blocked Fetch return.
func (s *gatedSource) release() {
	s.once.Do(func() { close(s.gate) })
}

// callsSoFar reports the number of Fetch calls so far.
func (s *gatedSource) callsSoFar() int {
	return int(s.calls.Load())
}

// publishedEvents reports how many events the watcher has published so far.
func publishedEvents(w *Watcher) uint64 {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.seq
}

// testFeed builds a feed with one asset per release, so deep-copy behavior
// can be observed along the whole Result -> Feed -> Releases -> Assets chain.
func testFeed(versions ...string) updates.Feed {
	releases := make([]updates.Release, 0, len(versions))

	for i, version := range versions {
		releases = append(releases, updates.Release{
			Version: version,
			URL:     "https://example.org/owner/repository/releases/" + version,
			Assets: []updates.Asset{
				{
					Name: "application-linux-amd64.tar.gz",
					URL:  "https://example.org/download/" + version + ".tar.gz",
					Size: int64(1024 * (i + 1)),
				},
			},
		})
	}

	return updates.Feed{
		RepositoryURL: "https://example.org/owner/repository",
		ReleasesURL:   "https://example.org/owner/repository/releases",
		Releases:      releases,
	}
}

// fakeTicker is a manually driven replacement for the timer the watcher
// schedules itself with.
type fakeTicker struct {
	c      chan time.Time
	stop   sync.Once
	resets atomic.Int32
}

func (t *fakeTicker) C() <-chan time.Time {
	return t.c
}

// Reset re-arms the timer and drops a tick that is still buffered, mirroring
// the re-arm semantics of the real timer.
func (t *fakeTicker) Reset(time.Duration) {
	t.resets.Add(1)

	for {
		select {
		case <-t.c:

		default:
			return
		}
	}
}

// resetCount reports how often the watcher re-armed the timer.
func (t *fakeTicker) resetCount() int {
	return int(t.resets.Load())
}

func (t *fakeTicker) Stop() {
	t.stop.Do(func() { close(t.c) })
}

// tick delivers a single tick. Ticks that nobody consumed yet are buffered,
// so driving a timer never depends on worker scheduling.
func (t *fakeTicker) tick() {
	select {
	case t.c <- time.Now():
	default:
	}
}

// tickerFactory creates manually driven timers and indexes them by the
// interval they were created for. Tests give every target a distinct interval
// so that each fake timer belongs to exactly one target.
type tickerFactory struct {
	mu      sync.Mutex
	tickers map[time.Duration]*fakeTicker
}

func newTickerFactory() *tickerFactory {
	return &tickerFactory{tickers: make(map[time.Duration]*fakeTicker)}
}

func (f *tickerFactory) create(d time.Duration) ticker {
	f.mu.Lock()
	defer f.mu.Unlock()

	created := &fakeTicker{c: make(chan time.Time, 64)}
	f.tickers[d] = created

	return created
}

// at returns the fake timer that was created for d.
func (f *tickerFactory) at(d time.Duration) *fakeTicker {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.tickers[d]
}

// newTestWatcher creates a watcher whose targets are scheduled by manually
// driven fake timers.
func newTestWatcher(t *testing.T, checker *updates.Checker, opts Options) (*Watcher, *tickerFactory) {
	t.Helper()

	factory := newTickerFactory()

	w := New(checker, opts)
	w.newTicker = factory.create

	return w, factory
}

// startWatcher runs w in the background and returns the channel carrying its
// result.
func startWatcher(t *testing.T, w *Watcher, ctx context.Context) <-chan error {
	t.Helper()

	done := make(chan error, 1)

	go func() {
		done <- w.Run(ctx)
	}()

	return done
}

// stopWatcher cancels ctx and waits for Run to return, which must happen
// well within the deadline even when a subscriber stopped reading.
func stopWatcher(t *testing.T, ctx context.Context, cancel context.CancelFunc, done <-chan error) {
	t.Helper()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}

	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// waitFor polls cond until it holds or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatalf("condition not met within %s", timeout)
}

// waitForGoroutineCount waits until the process is back at the baseline
// goroutine count.
func waitForGoroutineCount(t *testing.T, baseline int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)

	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("goroutine count did not settle: baseline %d, current %d", baseline, runtime.NumGoroutine())
}

// eventCollector consumes an event stream in the background until it is
// closed.
type eventCollector struct {
	mu     sync.Mutex
	events []Event
}

// collectEvents starts consuming events for the lifetime of the test.
func collectEvents(t *testing.T, events <-chan Event) *eventCollector {
	t.Helper()

	collector := &eventCollector{}

	finished := make(chan struct{})

	go func() {
		defer close(finished)

		for event := range events {
			collector.mu.Lock()
			collector.events = append(collector.events, event)
			collector.mu.Unlock()
		}
	}()

	t.Cleanup(func() {
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("event consumer did not finish")
		}
	})

	return collector
}

// count reports the number of events received so far.
func (c *eventCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.events)
}

// event returns the event received at position i, or a zero event.
func (c *eventCollector) event(i int) Event {
	c.mu.Lock()
	defer c.mu.Unlock()

	if i >= len(c.events) {
		return Event{}
	}

	return c.events[i]
}

// activeSubscription returns the only subscription of a watcher.
func activeSubscription(t *testing.T, w *Watcher) *subscription {
	t.Helper()

	w.mu.RLock()
	defer w.mu.RUnlock()

	for sub := range w.subs {
		return sub
	}

	t.Fatal("watcher has no active subscription")

	return nil
}

// pendingCount reports how many events a subscription still holds.
func pendingCount(sub *subscription) int {
	sub.mu.Lock()
	defer sub.mu.Unlock()

	return len(sub.pending)
}

// pendingSeq reports the sequence number of the pending event of a target.
func pendingSeq(sub *subscription, id string) uint64 {
	sub.mu.Lock()
	defer sub.mu.Unlock()

	return sub.pending[id].seq
}
