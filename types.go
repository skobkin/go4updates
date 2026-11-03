package updates

import "time"

// DefaultReleaseLimit is used when FetchOptions.Limit is zero.
const DefaultReleaseLimit = 10

// Asset describes a single file attached to a release. The updates package
// treats assets as metadata only and never downloads them; downloading,
// verification, and installation are opt-in through the selfupdate package.
type Asset struct {
	// Name is the file name as published by the provider.
	Name string

	// URL is the download location of the asset.
	URL string

	// Size is the asset size in bytes, when the provider reports it.
	Size int64

	// ContentType is the MIME type of the asset, when the provider reports it.
	ContentType string
}

// Release is a single upstream release.
//
// Body contains the raw upstream Markdown exactly as the provider returned
// it. Presentation-oriented transformations (linkification, changelog
// building) belong to the releasefmt package and must never be applied to
// stored releases.
type Release struct {
	// Version is the upstream version identifier, usually a git tag.
	Version string

	// Title is the human-readable release title, when the provider has one.
	Title string

	// Body is the raw upstream release Markdown.
	Body string

	// URL is the human-facing release page, when the provider supplies one.
	URL string

	// PublishedAt is the publication timestamp. It is the zero time when the
	// provider does not report one.
	PublishedAt time.Time

	// Prerelease reports whether the provider marks the release as a
	// pre-release.
	Prerelease bool

	// Assets lists attached release files as metadata; the selfupdate
	// package downloads them on demand.
	Assets []Asset
}

// Feed is a normalized release feed for a single repository.
//
// Releases are already filtered according to the requested FetchOptions:
// drafts are excluded, pre-releases are excluded unless they were requested,
// the newest release comes first, and at most the requested number of
// releases is present.
type Feed struct {
	// RepositoryURL is the human-facing repository page.
	RepositoryURL string

	// ReleasesURL is the human-facing releases listing page.
	ReleasesURL string

	// Releases holds the fetched releases, newest first.
	Releases []Release
}

// FetchOptions controls how many releases a Source returns and whether
// pre-releases are included.
type FetchOptions struct {
	// Limit is the maximum number of releases to return.
	// Zero means DefaultReleaseLimit; negative values are rejected with an
	// error by the bundled sources.
	Limit int

	// IncludePrereleases controls whether pre-releases may be returned.
	IncludePrereleases bool
}

// Status is the outcome of comparing a current version against the latest
// upstream release.
type Status uint8

const (
	// StatusUnknown means the versions could not be compared, for example
	// because the feed was empty or a version string is not valid.
	StatusUnknown Status = iota

	// StatusUpToDate means the current version is at least as new as the
	// latest upstream release.
	StatusUpToDate

	// StatusUpdateAvailable means a newer release exists upstream.
	StatusUpdateAvailable
)

// String implements fmt.Stringer. Values other than the two recognized
// statuses, including the zero StatusUnknown, render as "unknown".
func (s Status) String() string {
	switch s {
	case StatusUpToDate:
		return "up_to_date"

	case StatusUpdateAvailable:
		return "update_available"

	default:
		return "unknown"
	}
}

// Result is the outcome of a single check of one target.
//
// An empty successful feed is not an error: Latest is nil and Status is
// StatusUnknown while Feed and CheckedAt are still populated.
type Result struct {
	// CurrentVersion is the version that was checked, as given by the caller.
	CurrentVersion string

	// Feed is the release feed that was fetched for the check.
	Feed Feed

	// Latest points at the newest release, or is nil when the feed was empty.
	Latest *Release

	// Status is the comparison outcome.
	Status Status

	// CheckedAt is the time the check completed.
	CheckedAt time.Time
}

// Outcome pairs a Result with the error of a single target check inside
// Checker.CheckAll. Exactly one of Result/Err is meaningful per target.
type Outcome struct {
	// Result is the check result when the check succeeded.
	Result Result

	// Err is the failure reason when the check failed.
	Err error
}
