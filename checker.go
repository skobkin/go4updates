package updates

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrNilSource is returned by Checker.Check when the target has no Source.
var ErrNilSource = errors.New("go4updates: target has no source")

// CheckerOptions configures a Checker.
type CheckerOptions struct {
	// Comparator overrides the default version comparison policy for all
	// checks performed with the checker. Individual targets may still
	// override it through Target.Comparator.
	Comparator Comparator
}

// Checker performs one-shot release checks against targets.
//
// A Checker is immutable after construction and safe for concurrent use.
// It performs no scheduling, caching, or background work of its own: a check
// runs for exactly as long as the caller keeps the call running.
//
// The zero value is usable and behaves like a checker built with an empty
// CheckerOptions.
type Checker struct {
	comparator Comparator
}

// NewChecker creates a Checker. A nil Comparator in opts selects the default
// SemVerComparator policy.
func NewChecker(opts CheckerOptions) *Checker {
	return &Checker{comparator: opts.Comparator}
}

// Check fetches the release feed for target and compares the target's
// current version with the newest release.
//
// The comparison policy is Target.Comparator when set, otherwise the
// checker's configured comparator, otherwise SemVerComparator.
//
// A successfully fetched but empty feed is not an error: the returned Result
// has a nil Latest and StatusUnknown. Errors report retrieval, parsing,
// authentication, or protocol failures.
func (c *Checker) Check(ctx context.Context, target Target) (Result, error) {
	if target.Source == nil {
		return Result{}, ErrNilSource
	}

	feed, err := target.Source.Fetch(ctx, target.Fetch)
	if err != nil {
		return Result{}, fmt.Errorf("go4updates: fetching releases: %w", err)
	}

	result := Result{
		CurrentVersion: target.CurrentVersion,
		Feed:           feed,
		Status:         StatusUnknown,
		CheckedAt:      time.Now(),
	}

	if len(feed.Releases) == 0 {
		return result, nil
	}

	latest := feed.Releases[0]
	result.Latest = &latest
	result.Status = c.comparatorFor(target).Compare(target.CurrentVersion, latest.Version)

	return result, nil
}

// CheckAll checks every target and returns one Outcome per target, in input
// order. Targets are checked concurrently and independently: a failure of
// one target does not affect the results of the others.
//
// Concurrency is unbounded; callers with very large target lists should
// batch them. Cancellation of ctx is observed by the individual checks.
func (c *Checker) CheckAll(ctx context.Context, targets []Target) []Outcome {
	outcomes := make([]Outcome, len(targets))

	var wg sync.WaitGroup

	for i, target := range targets {
		wg.Go(func() {
			result, err := c.Check(ctx, target)
			outcomes[i] = Outcome{Result: result, Err: err}
		})
	}

	wg.Wait()

	return outcomes
}

// comparatorFor resolves the comparison policy for a single target.
func (c *Checker) comparatorFor(target Target) Comparator {
	if target.Comparator != nil {
		return target.Comparator
	}

	if c.comparator != nil {
		return c.comparator
	}

	return SemVerComparator{}
}
