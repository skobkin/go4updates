package fetch

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	updates "github.com/skobkin/go4updates"
)

// pageSpec describes one provider page of raw releases.
type pageSpec struct {
	versions []string
	pre      []bool
	ages     []time.Duration
}

// pageServer emulates a provider with fixed pages and records pagination
// requests. Pages contain raw items: drafts are included here and dropped
// only by the conversion function, mirroring the adapter boundary.
type pageServer struct {
	pages []pageSpec
	calls []int

	base   time.Time
	failOn int
}

const draftVersion = "draft"

func newPageServer(pages ...pageSpec) *pageServer {
	return &pageServer{pages: pages, base: time.Now()}
}

func (p *pageServer) fetchPage(ctx context.Context, page, perPage int) ([]updates.Release, bool, error) {
	p.calls = append(p.calls, perPage)

	if p.failOn == page {
		return nil, false, errors.New("provider failure")
	}

	if page > len(p.pages) {
		return nil, true, nil
	}

	spec := p.pages[page-1]

	items := make([]updates.Release, 0, len(spec.versions))
	for i, version := range spec.versions {
		age := time.Hour
		if i < len(spec.ages) {
			age = spec.ages[i]
		}

		items = append(items, updates.Release{
			Version:     version,
			PublishedAt: p.base.Add(-age),
			Prerelease:  i < len(spec.pre) && spec.pre[i],
		})
	}

	complete := len(items) < perPage

	return items, complete, nil
}

// convertDroppingDrafts drops draft items the way provider adapters do.
func convertDroppingDrafts(item updates.Release) (updates.Release, bool) {
	if item.Version == draftVersion {
		return updates.Release{}, false
	}

	return item, true
}

// keepAll converts every item as-is.
func keepAll(item updates.Release) (updates.Release, bool) {
	return item, true
}

func TestPaginateSingleShortPage(t *testing.T) {
	server := newPageServer(pageSpec{
		versions: []string{"v1.2.0", "v1.1.0"},
	})

	releases, err := Paginate(t.Context(), 5, 100, server.fetchPage, keepAll, false)
	if err != nil {
		t.Fatalf("Paginate returned error: %v", err)
	}

	if len(releases) != 2 {
		t.Fatalf("len(releases) = %d, want 2", len(releases))
	}

	if len(server.calls) != 1 {
		t.Fatalf("made %d requests, want 1 (short page must stop pagination)", len(server.calls))
	}
}

func TestPaginateContinuesAfterFullPage(t *testing.T) {
	server := newPageServer(
		pageSpec{versions: []string{"v1.4.0", "v1.3.0"}, ages: []time.Duration{time.Hour, 2 * time.Hour}},
		pageSpec{versions: []string{"v1.2.0"}},
	)

	// perPageMax of 2 makes the first page full.
	releases, err := Paginate(t.Context(), 3, 2, server.fetchPage, keepAll, false)
	if err != nil {
		t.Fatalf("Paginate returned error: %v", err)
	}

	if len(releases) != 3 {
		t.Fatalf("len(releases) = %d, want 3", len(releases))
	}

	if len(server.calls) != 2 {
		t.Fatalf("made %d requests, want 2 (a full page must continue pagination)", len(server.calls))
	}
}

func TestPaginateStopsAtLimit(t *testing.T) {
	server := newPageServer(pageSpec{
		versions: []string{"v1.5.0", "v1.4.0", "v1.3.0", "v1.2.0", "v1.1.0"},
		ages:     []time.Duration{time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour, 5 * time.Hour},
	})

	releases, err := Paginate(t.Context(), 2, 100, server.fetchPage, keepAll, false)
	if err != nil {
		t.Fatalf("Paginate returned error: %v", err)
	}

	if len(releases) != 2 {
		t.Fatalf("len(releases) = %d, want 2", len(releases))
	}

	if releases[0].Version != "v1.5.0" || releases[1].Version != "v1.4.0" {
		t.Errorf("releases = %v, want the two newest", versions(releases))
	}

	if len(server.calls) != 1 {
		t.Fatalf("made %d requests, want 1 (limit satisfied on first page)", len(server.calls))
	}
}

func TestPaginateKeepsPageSizeStable(t *testing.T) {
	// A full first page that loses items to filtering must not shrink the
	// page size of the next request: numbered pagination locates items
	// through page number and page size together, so a shrinking size would
	// re-fetch items the first page already returned and hide older
	// releases behind the duplicates.
	server := newPageServer(
		pageSpec{versions: []string{"v1.4.0", draftVersion, "v1.3.0"}, ages: []time.Duration{time.Hour, 2 * time.Hour, 3 * time.Hour}},
		pageSpec{versions: []string{"v1.2.0", "v1.1.0"}, ages: []time.Duration{4 * time.Hour, 5 * time.Hour}},
	)

	releases, err := Paginate(t.Context(), 4, 3, server.fetchPage, convertDroppingDrafts, false)
	if err != nil {
		t.Fatalf("Paginate returned error: %v", err)
	}

	if len(server.calls) != 2 {
		t.Fatalf("made %d requests, want 2", len(server.calls))
	}

	if server.calls[0] != 3 || server.calls[1] != 3 {
		t.Errorf("page sizes = %v, want the constant min(limit, maxPerPage) = 3 on every page", server.calls)
	}

	want := []string{"v1.4.0", "v1.3.0", "v1.2.0", "v1.1.0"}
	if got := versions(releases); !slices.Equal(got, want) {
		t.Errorf("releases = %v, want %v without duplicates", got, want)
	}
}

func TestPaginatePrereleaseFiltering(t *testing.T) {
	server := newPageServer(pageSpec{
		versions: []string{"v1.3.0-rc.1", "v1.2.0"},
		pre:      []bool{true, false},
		ages:     []time.Duration{time.Hour, 2 * time.Hour},
	})

	excluded, err := Paginate(t.Context(), 10, 100, server.fetchPage, keepAll, false)
	if err != nil {
		t.Fatalf("Paginate returned error: %v", err)
	}

	if len(excluded) != 1 || excluded[0].Version != "v1.2.0" {
		t.Errorf("prereleases must be excluded by default: got %v", versions(excluded))
	}

	included, err := Paginate(t.Context(), 10, 100, server.fetchPage, keepAll, true)
	if err != nil {
		t.Fatalf("Paginate returned error: %v", err)
	}

	if len(included) != 2 {
		t.Errorf("prereleases must be included on request: got %v", versions(included))
	}
}

func TestPaginateSortsDefensively(t *testing.T) {
	// Provider returned oldest first, violating the newest-first page order
	// assumption. The defensive sort must still produce a newest-first feed.
	server := newPageServer(pageSpec{
		versions: []string{"v1.1.0", "v1.2.0", "v1.3.0"},
		ages:     []time.Duration{3 * time.Hour, 2 * time.Hour, time.Hour},
	})

	releases, err := Paginate(t.Context(), 10, 100, server.fetchPage, keepAll, false)
	if err != nil {
		t.Fatalf("Paginate returned error: %v", err)
	}

	want := []string{"v1.3.0", "v1.2.0", "v1.1.0"}
	for i, version := range want {
		if releases[i].Version != version {
			t.Fatalf("release %d = %q, want %q", i, releases[i].Version, version)
		}
	}
}

func TestPaginateErrorPropagation(t *testing.T) {
	server := newPageServer(pageSpec{versions: []string{"v1.0.0"}})
	server.failOn = 2

	// First page is full (perPageMax 1), so a second page is requested,
	// which fails.
	_, err := Paginate(t.Context(), 10, 1, server.fetchPage, keepAll, false)
	if err == nil {
		t.Fatal("Paginate returned nil error, want provider failure")
	}
}

func TestPaginateEmptyFeed(t *testing.T) {
	server := newPageServer(pageSpec{})

	releases, err := Paginate(t.Context(), 5, 100, server.fetchPage, keepAll, false)
	if err != nil {
		t.Fatalf("Paginate returned error: %v", err)
	}

	if len(releases) != 0 {
		t.Errorf("len(releases) = %d, want 0", len(releases))
	}
}

func versions(releases []updates.Release) []string {
	out := make([]string, len(releases))
	for i, release := range releases {
		out[i] = release.Version
	}

	return out
}
