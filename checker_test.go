package updates

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// stubSource returns a canned feed or error and counts Fetch calls.
type stubSource struct {
	feed  Feed
	err   error
	calls atomic.Int32
}

func (s *stubSource) Fetch(ctx context.Context, opts FetchOptions) (Feed, error) {
	s.calls.Add(1)

	if s.err != nil {
		return Feed{}, s.err
	}

	return s.feed, nil
}

func testRelease(version string, age time.Duration) Release {
	return Release{
		Version:     version,
		PublishedAt: time.Now().Add(-age),
	}
}

func waitForGoroutineCount(t *testing.T, baseline int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)

	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("goroutine count did not settle: baseline %d, current %d", baseline, runtime.NumGoroutine())
}

func TestCheckUpdateAvailable(t *testing.T) {
	source := &stubSource{feed: Feed{
		RepositoryURL: "https://example.org/owner/repo",
		Releases: []Release{
			testRelease("v1.3.0", time.Hour),
			testRelease("v1.2.0", 2*time.Hour),
		},
	}}

	result, err := NewChecker(CheckerOptions{}).Check(context.Background(), Target{
		CurrentVersion: "1.2.0",
		Source:         source,
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}

	if result.Status != StatusUpdateAvailable {
		t.Errorf("Status = %v, want %v", result.Status, StatusUpdateAvailable)
	}

	if result.Latest == nil || result.Latest.Version != "v1.3.0" {
		t.Errorf("Latest = %+v, want version v1.3.0", result.Latest)
	}

	if result.CurrentVersion != "1.2.0" {
		t.Errorf("CurrentVersion = %q, want %q", result.CurrentVersion, "1.2.0")
	}

	if result.Feed.RepositoryURL != "https://example.org/owner/repo" {
		t.Errorf("Feed.RepositoryURL = %q, unexpected value", result.Feed.RepositoryURL)
	}

	if result.CheckedAt.IsZero() {
		t.Error("CheckedAt is zero, want a timestamp")
	}
}

func TestCheckUpToDate(t *testing.T) {
	source := &stubSource{feed: Feed{
		Releases: []Release{testRelease("v1.3.0", time.Hour)},
	}}

	result, err := NewChecker(CheckerOptions{}).Check(context.Background(), Target{
		CurrentVersion: "v1.3.0",
		Source:         source,
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}

	if result.Status != StatusUpToDate {
		t.Errorf("Status = %v, want %v", result.Status, StatusUpToDate)
	}
}

func TestCheckEmptyFeedIsNotAnError(t *testing.T) {
	source := &stubSource{feed: Feed{RepositoryURL: "https://example.org/owner/repo"}}

	result, err := NewChecker(CheckerOptions{}).Check(context.Background(), Target{
		CurrentVersion: "1.0.0",
		Source:         source,
	})
	if err != nil {
		t.Fatalf("Check returned error for empty feed: %v", err)
	}

	if result.Latest != nil {
		t.Errorf("Latest = %+v, want nil", result.Latest)
	}

	if result.Status != StatusUnknown {
		t.Errorf("Status = %v, want %v", result.Status, StatusUnknown)
	}

	if result.Feed.RepositoryURL != "https://example.org/owner/repo" {
		t.Errorf("Feed was not preserved on empty feed: %+v", result.Feed)
	}

	if result.CheckedAt.IsZero() {
		t.Error("CheckedAt is zero, want a timestamp")
	}
}

func TestCheckNilSource(t *testing.T) {
	_, err := NewChecker(CheckerOptions{}).Check(context.Background(), Target{CurrentVersion: "1.0.0"})
	if !errors.Is(err, ErrNilSource) {
		t.Fatalf("Check error = %v, want ErrNilSource", err)
	}
}

func TestCheckFetchErrorIsWrapped(t *testing.T) {
	fetchErr := errors.New("provider unavailable")
	source := &stubSource{err: fetchErr}

	result, err := NewChecker(CheckerOptions{}).Check(context.Background(), Target{
		CurrentVersion: "1.0.0",
		Source:         source,
	})
	if !errors.Is(err, fetchErr) {
		t.Fatalf("Check error = %v, want wrapped fetchErr", err)
	}

	if result.Latest != nil || result.Status != StatusUnknown || !result.CheckedAt.IsZero() {
		t.Errorf("Result = %+v, want an empty result on error", result)
	}
}

func TestCheckComparatorPrecedence(t *testing.T) {
	targetComparator := ComparatorFunc(func(current, latest string) Status {
		return StatusUpdateAvailable
	})

	checkerComparator := ComparatorFunc(func(current, latest string) Status {
		return StatusUpToDate
	})

	feed := Feed{Releases: []Release{testRelease("v1.0.0", time.Hour)}}

	// Target comparator wins over the checker comparator.
	checker := NewChecker(CheckerOptions{Comparator: checkerComparator})

	result, err := checker.Check(context.Background(), Target{
		CurrentVersion: "0.1.0",
		Source:         &stubSource{feed: feed},
		Comparator:     targetComparator,
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}

	if result.Status != StatusUpdateAvailable {
		t.Errorf("target comparator did not take precedence: status = %v", result.Status)
	}

	// Checker comparator wins over the default.
	result, err = checker.Check(context.Background(), Target{
		CurrentVersion: "0.1.0",
		Source:         &stubSource{feed: feed},
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}

	if result.Status != StatusUpToDate {
		t.Errorf("checker comparator did not take precedence: status = %v", result.Status)
	}

	// Default SemVer policy applies when nothing is configured.
	plain := &Checker{}

	result, err = plain.Check(context.Background(), Target{
		CurrentVersion: "0.1.0",
		Source:         &stubSource{feed: feed},
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}

	if result.Status != StatusUpdateAvailable {
		t.Errorf("default comparator did not apply: status = %v", result.Status)
	}

	// The default comparator reports unknown for uncomparable versions.
	result, err = plain.Check(context.Background(), Target{
		CurrentVersion: "dev",
		Source:         &stubSource{feed: feed},
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}

	if result.Status != StatusUnknown {
		t.Errorf("status = %v, want %v for uncomparable versions", result.Status, StatusUnknown)
	}
}

func TestZeroValueCheckerIsUsable(t *testing.T) {
	var checker Checker

	result, err := checker.Check(context.Background(), Target{
		CurrentVersion: "1.0.0",
		Source: &stubSource{feed: Feed{
			Releases: []Release{testRelease("v2.0.0", time.Hour)},
		}},
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}

	if result.Status != StatusUpdateAvailable {
		t.Errorf("Status = %v, want %v", result.Status, StatusUpdateAvailable)
	}
}

func TestCheckAllPreservesOrderAndCount(t *testing.T) {
	checker := NewChecker(CheckerOptions{})

	targets := []Target{
		{CurrentVersion: "1.0.0", Source: &stubSource{feed: Feed{Releases: []Release{testRelease("v1.1.0", time.Hour)}}}},
		{CurrentVersion: "2.0.0", Source: &stubSource{feed: Feed{Releases: []Release{testRelease("v2.0.0", time.Hour)}}}},
		{CurrentVersion: "3.0.0", Source: &stubSource{feed: Feed{Releases: []Release{testRelease("v3.1.0", time.Hour)}}}},
	}

	outcomes := checker.CheckAll(context.Background(), targets)
	if len(outcomes) != len(targets) {
		t.Fatalf("len(outcomes) = %d, want %d", len(outcomes), len(targets))
	}

	wantStatuses := []Status{StatusUpdateAvailable, StatusUpToDate, StatusUpdateAvailable}
	for i, outcome := range outcomes {
		if outcome.Err != nil {
			t.Fatalf("outcome %d returned error: %v", i, outcome.Err)
		}

		if outcome.Result.Status != wantStatuses[i] {
			t.Errorf("outcome %d status = %v, want %v", i, outcome.Result.Status, wantStatuses[i])
		}

		if outcome.Result.CurrentVersion != targets[i].CurrentVersion {
			t.Errorf("outcome %d current version = %q, want %q (order not preserved)",
				i, outcome.Result.CurrentVersion, targets[i].CurrentVersion)
		}
	}
}

func TestCheckAllPartialFailure(t *testing.T) {
	checker := NewChecker(CheckerOptions{})

	failing := &stubSource{err: errors.New("boom")}

	targets := []Target{
		{CurrentVersion: "1.0.0", Source: &stubSource{feed: Feed{Releases: []Release{testRelease("v1.1.0", time.Hour)}}}},
		{CurrentVersion: "2.0.0", Source: failing},
		{CurrentVersion: "3.0.0", Source: &stubSource{feed: Feed{Releases: []Release{testRelease("v3.1.0", time.Hour)}}}},
	}

	outcomes := checker.CheckAll(context.Background(), targets)

	if outcomes[0].Err != nil || outcomes[2].Err != nil {
		t.Fatalf("successful targets must not fail: %v, %v", outcomes[0].Err, outcomes[2].Err)
	}

	if outcomes[0].Result.Status != StatusUpdateAvailable {
		t.Errorf("first outcome status = %v, want %v", outcomes[0].Result.Status, StatusUpdateAvailable)
	}

	if !errors.Is(outcomes[1].Err, failing.err) {
		t.Errorf("second outcome error = %v, want wrapped %v", outcomes[1].Err, failing.err)
	}

	if outcomes[1].Result.Latest != nil || outcomes[1].Result.Status != StatusUnknown {
		t.Errorf("failed outcome result = %+v, want an empty result", outcomes[1].Result)
	}
}

func TestCheckAllEmptyTargets(t *testing.T) {
	outcomes := NewChecker(CheckerOptions{}).CheckAll(context.Background(), nil)
	if len(outcomes) != 0 {
		t.Errorf("len(outcomes) = %d, want 0", len(outcomes))
	}
}

func TestCheckAllCancellationFailsChecks(t *testing.T) {
	checker := NewChecker(CheckerOptions{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	blocked := make(chan struct{})
	defer close(blocked)

	source := &cancelingSource{ctx: ctx, blocked: blocked}

	outcomes := checker.CheckAll(ctx, []Target{
		{CurrentVersion: "1.0.0", Source: source},
		{CurrentVersion: "2.0.0", Source: source},
	})

	for i, outcome := range outcomes {
		if !errors.Is(outcome.Err, context.Canceled) {
			t.Errorf("outcome %d error = %v, want context.Canceled", i, outcome.Err)
		}
	}
}

// cancelingSource reports context cancellation the way network sources do.
type cancelingSource struct {
	ctx     context.Context
	blocked chan struct{}
}

func (s *cancelingSource) Fetch(ctx context.Context, opts FetchOptions) (Feed, error) {
	if err := ctx.Err(); err != nil {
		return Feed{}, err
	}

	<-s.blocked

	return Feed{}, nil
}

func TestCheckAllDoesNotLeakGoroutines(t *testing.T) {
	baseline := runtime.NumGoroutine()

	checker := NewChecker(CheckerOptions{})

	source := &stubSource{feed: Feed{Releases: []Release{testRelease("v1.0.0", time.Hour)}}}

	targets := make([]Target, 20)
	for i := range targets {
		targets[i] = Target{CurrentVersion: "0.9.0", Source: source}
	}

	_ = checker.CheckAll(context.Background(), targets)

	waitForGoroutineCount(t, baseline)
}
