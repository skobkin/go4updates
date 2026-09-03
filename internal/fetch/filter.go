package fetch

import (
	"fmt"
	"slices"

	updates "github.com/skobkin/go4updates"
)

// ResolveLimit normalizes FetchOptions.Limit into a concrete release limit.
// Zero selects updates.DefaultReleaseLimit; negative values are rejected.
func ResolveLimit(limit int) (int, error) {
	switch {
	case limit < 0:
		return 0, fmt.Errorf("go4updates: invalid release limit %d: must not be negative", limit)

	case limit == 0:
		return updates.DefaultReleaseLimit, nil

	default:
		return limit, nil
	}
}

// SortNewestFirst orders releases newest first by publication timestamp.
// The sort is stable, so releases with identical timestamps keep their
// relative order.
func SortNewestFirst(releases []updates.Release) {
	slices.SortStableFunc(releases, func(a, b updates.Release) int {
		return b.PublishedAt.Compare(a.PublishedAt)
	})
}
