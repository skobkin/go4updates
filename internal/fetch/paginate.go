package fetch

import (
	"context"

	updates "github.com/skobkin/go4updates"
)

// PageFetcher fetches a single page of provider releases. page is 1-based;
// perPage is the number of raw items to request. The boolean result reports
// that the provider has no more data: the returned page was empty or shorter
// than the requested perPage.
type PageFetcher[T any] func(ctx context.Context, page, perPage int) (items []T, complete bool, err error)

// ItemConverter converts one raw provider item into the common release model.
// The boolean result reports whether the item is eligible: provider-only
// states with no common equivalent, such as drafts, are dropped here.
type ItemConverter[T any] func(item T) (updates.Release, bool)

// Paginate fetches provider release pages and accumulates eligible releases
// until limit releases have been collected or the provider runs out of data.
//
// The limit applies to eligible releases after conversion and pre-release
// filtering: pages are fetched until enough eligible releases have been
// accumulated, not simply until a raw page count is reached. Every page is
// requested with the same page size, the limit capped at maxPerPage, so no
// more data is fetched than the caller asked for while the size the provider
// uses to locate a page never changes.
//
// The caller's fetchPage must iterate provider pages in newest-first order;
// early stopping is only correct under that assumption. The accumulated
// result is sorted newest first as a defensive consistency step before being
// truncated to the limit.
func Paginate[T any](
	ctx context.Context,
	limit int,
	maxPerPage int,
	fetchPage PageFetcher[T],
	convert ItemConverter[T],
	includePrereleases bool,
) ([]updates.Release, error) {
	eligible := make([]updates.Release, 0, min(limit, 64))

	// The page size stays constant across pages: numbered pagination locates
	// a page through the page size together with the page number, so a size
	// that shrinks with the remaining need would shift the offsets of later
	// pages back onto items earlier pages already returned. The size is
	// capped by the limit, so the first request still asks for no more than
	// the caller wants.
	perPage := min(limit, maxPerPage)

	for page := 1; len(eligible) < limit; page++ {
		items, complete, err := fetchPage(ctx, page, perPage)
		if err != nil {
			return nil, err
		}

		for _, item := range items {
			release, keep := convert(item)
			if !keep {
				continue
			}

			if !includePrereleases && release.Prerelease {
				continue
			}

			eligible = append(eligible, release)

			if len(eligible) >= limit {
				break
			}
		}

		if complete {
			break
		}
	}

	SortNewestFirst(eligible)

	if len(eligible) > limit {
		eligible = eligible[:limit]
	}

	return eligible, nil
}
