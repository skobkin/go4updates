package watch

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	updates "github.com/skobkin/go4updates"
)

// checkedWatcher returns a watcher that completed exactly one check of a
// target and has already stopped, so state can be asserted without races.
// It must be called inside a synctest bubble, like every test that runs a
// watcher.
func checkedWatcher(t *testing.T, feed updates.Feed) *Watcher {
	t.Helper()

	source := &stubSource{feed: feed}

	w := New(nil, Options{})

	if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := startWatcher(t, w, ctx)

	waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() == 1 })

	stopWatcher(t, ctx, cancel, done)

	if source.callsSoFar() != 1 {
		t.Fatalf("made %d checks, want 1", source.callsSoFar())
	}

	return w
}

// assertIntact verifies that a state still carries the untouched data of the
// test feed.
func assertIntact(t *testing.T, state State) {
	t.Helper()

	if state.Result == nil {
		t.Fatal("Result = nil, want the result of the successful check")
	}

	if state.LastError != nil {
		t.Errorf("LastError = %v, want nil", state.LastError)
	}

	if state.Result.Feed.RepositoryURL != "https://example.org/owner/repository" {
		t.Errorf("Feed.RepositoryURL = %q, unexpected value", state.Result.Feed.RepositoryURL)
	}

	if len(state.Result.Feed.Releases) != 2 {
		t.Fatalf("len(Feed.Releases) = %d, want 2", len(state.Result.Feed.Releases))
	}

	if state.Result.Feed.Releases[0].Version != "v1.1.0" || state.Result.Feed.Releases[1].Version != "v1.0.0" {
		t.Errorf("Feed.Releases = %v, want the original releases", versionsOf(state.Result.Feed.Releases))
	}

	if state.Result.Latest == nil || state.Result.Latest.Version != "v1.1.0" {
		t.Fatalf("Latest = %+v, want the v1.1.0 release", state.Result.Latest)
	}

	if len(state.Result.Latest.Assets) != 1 || state.Result.Latest.Assets[0].Name != "application-linux-amd64.tar.gz" {
		t.Errorf("Latest.Assets = %+v, want the original asset", state.Result.Latest.Assets)
	}
}

func versionsOf(releases []updates.Release) []string {
	out := make([]string, 0, len(releases))

	for _, release := range releases {
		out = append(out, release.Version)
	}

	return out
}

func TestStateOfUnknownTarget(t *testing.T) {
	w := New(nil, Options{})

	state, ok := w.State("missing")
	if ok {
		t.Error("State of an unknown target reported ok")
	}

	if state.Result != nil || !state.LastAttemptAt.IsZero() || state.LastError != nil {
		t.Errorf("State of an unknown target = %+v, want the zero state", state)
	}
}

func TestStateBeforeFirstAttemptIsAbsent(t *testing.T) {
	w := New(nil, Options{})

	if err := w.Add("application", updates.Target{}); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	if _, ok := w.State("application"); ok {
		t.Error("State reported a state before the first attempt completed")
	}
}

func TestStatesIsEmptyBeforeAnyCheck(t *testing.T) {
	w := New(nil, Options{})

	states := w.States()
	if states == nil {
		t.Fatal("States returned nil, want an empty map")
	}

	if len(states) != 0 {
		t.Errorf("States = %v, want an empty map", states)
	}
}

func TestStateReturnsDeepCopy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := checkedWatcher(t, testFeed("v1.1.0", "v1.0.0"))

		state, ok := w.State("application")
		if !ok {
			t.Fatal("State reported no state after a successful check")
		}

		state.LastError = errors.New("mutated")
		state.Result.Feed.RepositoryURL = "https://mutated.example.org"

		state.Result.Feed.Releases[0].Version = "mutated"
		state.Result.Feed.Releases[0].Assets[0].Name = "mutated"
		state.Result.Feed.Releases = append(state.Result.Feed.Releases, updates.Release{Version: "appended"})

		// Latest is a copy as well: mutating it must not reach the watcher or
		// the releases of the same state.
		if state.Result.Latest != nil {
			state.Result.Latest.Version = "mutated"
			state.Result.Latest.Assets = append(state.Result.Latest.Assets, updates.Asset{Name: "appended"})
		}

		assertIntact(t, mustState(t, w, "application"))
	})
}

func TestStatesReturnDeepCopies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := checkedWatcher(t, testFeed("v1.1.0", "v1.0.0"))

		states := w.States()
		if len(states) != 1 {
			t.Fatalf("len(States) = %d, want 1", len(states))
		}

		state := states["application"]
		state.Result.Feed.Releases[0].Version = "mutated"
		state.Result.Feed.Releases[0].Assets[0].Name = "mutated"
		state.Result.Feed.Releases = append(state.Result.Feed.Releases, updates.Release{Version: "appended"})
		state.LastError = errors.New("mutated")

		// The map itself must be a copy as well: adding to it must not reach
		// the watcher.
		states["extra"] = State{}

		assertIntact(t, mustState(t, w, "application"))

		if _, ok := w.State("extra"); ok {
			t.Error("State reported the entry that was added to a returned map")
		}
	})
}

func TestStateSurvivesFailureAndStaysReadableAfterFinish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := &stubSource{feed: testFeed("v1.1.0")}

		w := New(nil, Options{})

		if err := w.Add("application", updates.Target{CurrentVersion: "1.0.0", Source: source}, WithInterval(10*time.Millisecond)); err != nil {
			t.Fatalf("Add returned error: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := startWatcher(t, w, ctx)

		waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() == 1 })

		source.fail(errors.New("provider unavailable"))

		time.Sleep(10 * time.Millisecond)
		waitFor(t, 2*time.Second, func() bool { return source.callsSoFar() == 2 })

		stopWatcher(t, ctx, cancel, done)

		state := mustState(t, w, "application")

		if state.Result == nil || state.Result.Latest == nil || state.Result.Latest.Version != "v1.1.0" {
			t.Fatalf("Result = %+v, want the preserved v1.1.0 result", state.Result)
		}

		if state.LastError == nil {
			t.Error("LastError = nil, want the error of the failed attempt")
		}

		states := w.States()
		if len(states) != 1 {
			t.Errorf("len(States) = %d, want 1", len(states))
		}
	})
}

// mustState returns the state of a target or fails the test.
func mustState(t *testing.T, w *Watcher, id string) State {
	t.Helper()

	state, ok := w.State(id)
	if !ok {
		t.Fatalf("State(%q) reported no state", id)
	}

	return state
}
