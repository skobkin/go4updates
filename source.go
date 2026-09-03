package updates

import "context"

// Source fetches release feeds from a single repository hosted by a provider.
//
// Implementations must only retrieve and normalize data: they must not
// compare versions, cache, schedule work, or rewrite release Markdown.
// Fetch must honor context cancellation.
type Source interface {
	// Fetch returns the current release feed according to opts.
	Fetch(ctx context.Context, opts FetchOptions) (Feed, error)
}

// Target couples a Source with the current version of the checked component
// and the comparison/fetch policy for it.
//
// A Target intentionally carries no identity; identification of multiple
// targets belongs to the watch package.
type Target struct {
	// CurrentVersion is the version of the checked component.
	CurrentVersion string

	// Source fetches the release feed for the component.
	Source Source

	// Comparator optionally overrides the comparison policy for this target.
	// When nil, the checker's comparator applies, falling back to
	// SemVerComparator.
	Comparator Comparator

	// Fetch controls how many releases the source returns and whether
	// pre-releases are eligible.
	Fetch FetchOptions
}
