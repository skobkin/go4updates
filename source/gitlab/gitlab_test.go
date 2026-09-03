package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/internal/fetch"
)

func TestNewValidatesProjectPath(t *testing.T) {
	for _, project := range []string{
		"",
		"   ",
		"project",
		"group/",
		"/project",
		"group/subgroup/project/",
		"group//project",
		"group/sub group/project",
	} {
		t.Run(project, func(t *testing.T) {
			if _, err := New(project); err == nil {
				t.Errorf("New(%q) returned nil error, want an invalid project path error", project)
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	source, err := New("group/project")
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if source.serverURL != defaultServerURL {
		t.Errorf("serverURL = %q, want %q", source.serverURL, defaultServerURL)
	}

	if source.apiURL != defaultServerURL+apiPath {
		t.Errorf("apiURL = %q, want %q", source.apiURL, defaultServerURL+apiPath)
	}

	if source.client == nil {
		t.Error("client is nil, want a default client")
	}
}

func TestNewServerURLOptionDerivesAPI(t *testing.T) {
	source, err := New("group/project", WithServerURL("https://git.example.com/"))
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if source.serverURL != "https://git.example.com" {
		t.Errorf("serverURL = %q, want trailing slash trimmed", source.serverURL)
	}

	if source.apiURL != "https://git.example.com"+apiPath {
		t.Errorf("apiURL = %q, want API derived from the server URL", source.apiURL)
	}
}

func TestNewExplicitAPIURLWinsOverDerivation(t *testing.T) {
	source, err := New("group/project",
		WithServerURL("https://git.example.com"),
		WithAPIURL("https://api.example.com"),
	)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if source.apiURL != "https://api.example.com" {
		t.Errorf("apiURL = %q, want explicit API URL", source.apiURL)
	}

	if source.serverURL != "https://git.example.com" {
		t.Errorf("serverURL = %q, want the server URL untouched by the API URL", source.serverURL)
	}
}

func TestNewOptionOrderIndependence(t *testing.T) {
	first, err := New("group/project",
		WithServerURL("https://git.example.com"),
		WithAPIURL("https://api.example.com"),
	)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	second, err := New("group/project",
		WithAPIURL("https://api.example.com"),
		WithServerURL("https://git.example.com"),
	)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if first.apiURL != second.apiURL || first.serverURL != second.serverURL {
		t.Errorf("option order changed resolution: (%q, %q) vs (%q, %q)",
			first.serverURL, first.apiURL, second.serverURL, second.apiURL)
	}
}

func TestNewAPIURLDoesNotChangeServer(t *testing.T) {
	source, err := New("group/project", WithAPIURL("https://api-proxy.example.com"))
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if source.serverURL != defaultServerURL {
		t.Errorf("serverURL = %q, want unchanged default (API URL must not become the server URL)", source.serverURL)
	}

	if source.apiURL != "https://api-proxy.example.com" {
		t.Errorf("apiURL = %q, want the explicit API URL", source.apiURL)
	}
}

func TestNewWithHTTPClient(t *testing.T) {
	client := &http.Client{Timeout: 5 * time.Second}

	source, err := New("group/project", WithHTTPClient(client))
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if source.client != client {
		t.Error("injected client was not used")
	}
}

func TestNewRejectsInvalidURLs(t *testing.T) {
	tests := map[string][]Option{
		"empty option server URL":      {WithServerURL("   ")},
		"empty option API URL":         {WithAPIURL("")},
		"option server URL bad scheme": {WithServerURL("ftp://example.com")},
		"option API URL without host":  {WithAPIURL("https://")},
	}

	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := New("group/project", opts...); err == nil {
				t.Errorf("New returned nil error for %s", name)
			}
		})
	}
}

// recordingServer captures requests and replies with scripted releases.
type recordingServer struct {
	t         *testing.T
	server    *httptest.Server
	headers   []http.Header
	paths     []string
	queries   []string
	responses [][]releaseDTO
}

func newRecordingServer(t *testing.T, pages ...[]releaseDTO) *recordingServer {
	rec := &recordingServer{t: t, responses: pages}

	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.headers = append(rec.headers, r.Header.Clone())
		rec.paths = append(rec.paths, r.URL.EscapedPath())
		rec.queries = append(rec.queries, r.URL.RawQuery)

		page := 0
		if parsed, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil {
			page = parsed - 1
		}

		w.Header().Set("Content-Type", "application/json")

		if page < 0 || page >= len(rec.responses) {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		if err := json.NewEncoder(w).Encode(rec.responses[page]); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	}))

	t.Cleanup(rec.server.Close)

	return rec
}

func releaseDTOFor(tag string, age time.Duration) releaseDTO {
	return releaseDTO{
		TagName:     tag,
		Name:        "Release " + tag,
		Description: "Body of " + tag,
		ReleasedAt:  time.Now().Add(-age),
	}
}

func newTestSource(rec *recordingServer, opts ...Option) *Source {
	options := append([]Option{
		WithServerURL(rec.server.URL),
		WithAPIURL(rec.server.URL),
	}, opts...)

	source, err := New("group/project", options...)
	if err != nil {
		rec.t.Fatalf("New returned error: %v", err)
	}

	return source
}

func TestFetchEncodesProjectPath(t *testing.T) {
	tests := map[string]struct {
		project  string
		wantPath string
	}{
		"single namespace": {"group/project", "/api/v4/projects/group%2Fproject/releases"},
		"with subgroups":   {"group/subgroup/project", "/api/v4/projects/group%2Fsubgroup%2Fproject/releases"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := newRecordingServer(t, []releaseDTO{releaseDTOFor("v1.0.0", time.Hour)})

			// Only the server URL is given, so the API path of the default
			// endpoint shape is exercised together with the path encoding.
			source, err := New(tt.project, WithServerURL(rec.server.URL))
			if err != nil {
				t.Fatalf("New returned error: %v", err)
			}

			if _, err := source.Fetch(context.Background(), updates.FetchOptions{Limit: 10}); err != nil {
				t.Fatalf("Fetch returned error: %v", err)
			}

			if len(rec.paths) != 1 {
				t.Fatalf("made %d requests, want 1", len(rec.paths))
			}

			// Slashes inside the project path must stay escaped on the wire: a
			// decoded slash would address nested API paths, not one project.
			if rec.paths[0] != tt.wantPath {
				t.Errorf("request path = %q, want %q", rec.paths[0], tt.wantPath)
			}

			if !strings.Contains(rec.paths[0], "%2F") {
				t.Errorf("request path %q does not keep the project path escaped", rec.paths[0])
			}
		})
	}
}

func TestFetchSendsHeadersAndQuery(t *testing.T) {
	rec := newRecordingServer(t, []releaseDTO{releaseDTOFor("v1.0.0", time.Hour)})
	source := newTestSource(rec)

	_, err := source.Fetch(context.Background(), updates.FetchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if len(rec.headers) != 1 {
		t.Fatalf("made %d requests, want 1", len(rec.headers))
	}

	if got := rec.headers[0].Get("Accept"); got != acceptHeader {
		t.Errorf("Accept header = %q, want %q", got, acceptHeader)
	}

	if got, want := rec.queries[0], "per_page=10&page=1"; got != want {
		t.Errorf("query = %q, want %q", got, want)
	}
}

func TestFetchSortsNewestFirst(t *testing.T) {
	// Provider returned oldest first; the feed must still be newest first.
	rec := newRecordingServer(t, []releaseDTO{
		releaseDTOFor("v1.1.0", 3*time.Hour),
		releaseDTOFor("v1.2.0", 2*time.Hour),
		releaseDTOFor("v1.3.0", time.Hour),
	})
	source := newTestSource(rec)

	feed, err := source.Fetch(context.Background(), updates.FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	want := []string{"v1.3.0", "v1.2.0", "v1.1.0"}
	for i, tag := range want {
		if feed.Releases[i].Version != tag {
			t.Fatalf("release %d = %q, want %q", i, feed.Releases[i].Version, tag)
		}
	}
}

func TestFetchDropsUpcomingReleases(t *testing.T) {
	// An upcoming release is planned for the future and must never be treated
	// as the newest published version.
	upcoming := releaseDTOFor("v2.0.0", 0)
	upcoming.ReleasedAt = time.Now().Add(time.Hour)
	upcoming.Upcoming = true

	// upcoming_release is the attribute GitLab documents; both spellings are
	// honored.
	upcomingDocumented := releaseDTOFor("v2.1.0", 0)
	upcomingDocumented.ReleasedAt = time.Now().Add(2 * time.Hour)
	upcomingDocumented.UpcomingRelease = true

	rec := newRecordingServer(t, []releaseDTO{upcoming, upcomingDocumented, releaseDTOFor("v1.0.0", 2*time.Hour)})
	source := newTestSource(rec)

	feed, err := source.Fetch(context.Background(), updates.FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if len(feed.Releases) != 1 || feed.Releases[0].Version != "v1.0.0" {
		t.Errorf("upcoming releases must never appear in the feed: %v", feed.Releases)
	}
}

func TestFetchPrereleaseFiltering(t *testing.T) {
	prerelease := releaseDTOFor("v1.3.0-rc.1", time.Hour)
	prerelease.Prerelease = true

	rec := newRecordingServer(t, []releaseDTO{prerelease, releaseDTOFor("v1.2.0", 2*time.Hour)})
	source := newTestSource(rec)

	feed, err := source.Fetch(context.Background(), updates.FetchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if len(feed.Releases) != 1 || feed.Releases[0].Version != "v1.2.0" {
		t.Errorf("prereleases must be excluded by default: %v", feed.Releases)
	}

	// A short page already stopped pagination, so reset the recorder for the
	// include-prereleases scenario.
	rec2 := newRecordingServer(t, []releaseDTO{prerelease, releaseDTOFor("v1.2.0", 2*time.Hour)})
	source2 := newTestSource(rec2)

	feed, err = source2.Fetch(context.Background(), updates.FetchOptions{IncludePrereleases: true})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if len(feed.Releases) != 2 || feed.Releases[0].Version != "v1.3.0-rc.1" {
		t.Errorf("prereleases must be included on request: %v", feed.Releases)
	}
}

func TestFetchClassifiesSemverPrereleaseTags(t *testing.T) {
	// The GitLab release API has no pre-release flag, so the state must be
	// derived from the tag: otherwise a candidate such as v2.0.0-rc.1 would
	// survive the default pre-release filter and be reported as the newest
	// release. A leading "v" is optional.
	rec := newRecordingServer(t, []releaseDTO{
		releaseDTOFor("v2.0.0-rc.1", time.Hour),
		releaseDTOFor("1.9.0-beta.2", 2*time.Hour),
		releaseDTOFor("v1.8.0", 3*time.Hour),
	})
	source := newTestSource(rec)

	feed, err := source.Fetch(context.Background(), updates.FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if len(feed.Releases) != 1 || feed.Releases[0].Version != "v1.8.0" {
		t.Fatalf("semver pre-release tags must be excluded by default: %v", feed.Releases)
	}

	if feed.Releases[0].Prerelease {
		t.Error("stable release must not be marked as a pre-release")
	}

	rec2 := newRecordingServer(t, []releaseDTO{
		releaseDTOFor("v2.0.0-rc.1", time.Hour),
		releaseDTOFor("1.9.0-beta.2", 2*time.Hour),
		releaseDTOFor("v1.8.0", 3*time.Hour),
	})
	source2 := newTestSource(rec2)

	feed, err = source2.Fetch(context.Background(), updates.FetchOptions{IncludePrereleases: true})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if len(feed.Releases) != 3 {
		t.Fatalf("len(releases) = %d, want 3 with IncludePrereleases", len(feed.Releases))
	}

	for _, release := range feed.Releases {
		if release.Version == "v1.8.0" {
			if release.Prerelease {
				t.Error("stable release must not be marked as a pre-release")
			}

			continue
		}

		if !release.Prerelease {
			t.Errorf("release %q must be marked as a pre-release", release.Version)
		}
	}
}

func TestHasSemverPrerelease(t *testing.T) {
	tests := map[string]bool{
		"v2.0.0-rc.1":     true,
		"2.0.0-beta.2":    true,
		"v1.0.0-alpha":    true,
		"v1.0.0":          false,
		"1.2.3":           false,
		"v1.2":            false,
		"release":         false,
		"release-1.2.3":   false,
		"":                false,
		"   v1.0.0-rc.1 ": true,
	}

	for tag, want := range tests {
		if got := hasSemverPrerelease(tag); got != want {
			t.Errorf("hasSemverPrerelease(%q) = %v, want %v", tag, got, want)
		}
	}
}

func TestFetchPaginatesUntilLimitSatisfied(t *testing.T) {
	// Two pages: the first is full of upcoming releases, so pagination must
	// continue until the eligible limit is satisfied.
	upcoming := releaseDTOFor("v2.0.0", time.Hour)
	upcoming.Upcoming = true

	rec := newRecordingServer(t,
		[]releaseDTO{upcoming, upcoming, upcoming},
		[]releaseDTO{releaseDTOFor("v1.0.0", 3*time.Hour), releaseDTOFor("v0.9.0", 4*time.Hour)},
	)
	source := newTestSource(rec)

	feed, err := source.Fetch(context.Background(), updates.FetchOptions{Limit: 2})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if len(feed.Releases) != 2 {
		t.Fatalf("len(releases) = %d, want 2", len(feed.Releases))
	}

	if len(rec.queries) != 2 {
		t.Fatalf("made %d requests, want 2 (upcoming releases must not satisfy the limit)", len(rec.queries))
	}

	if !strings.Contains(rec.queries[1], "per_page=2") {
		t.Errorf("second request query %q does not request the remaining need (2)", rec.queries[1])
	}

	for _, release := range feed.Releases {
		if release.Version == "v2.0.0" {
			t.Errorf("upcoming release %q reached the feed", release.Version)
		}
	}
}

func TestFetchMapsAssetLinks(t *testing.T) {
	release := releaseDTOFor("v1.0.0", time.Hour)
	release.Assets = assetsDTO{Links: []assetLinkDTO{
		{
			Name:           "app-linux-amd64.tar.gz",
			URL:            "https://cdn.example.com/app-linux-amd64.tar.gz",
			DirectAssetURL: "https://git.example.com/group/project/-/releases/v1.0.0/downloads/app-linux-amd64.tar.gz",
		},
		{
			Name:           "app.exe",
			DirectAssetURL: "https://git.example.com/group/project/-/releases/v1.0.0/downloads/app.exe",
		},
	}}

	rec := newRecordingServer(t, []releaseDTO{release})
	source := newTestSource(rec)

	feed, err := source.Fetch(context.Background(), updates.FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if len(feed.Releases) != 1 || len(feed.Releases[0].Assets) != 2 {
		t.Fatalf("asset links were not mapped: %+v", feed.Releases)
	}

	asset := feed.Releases[0].Assets[0]
	if asset.Name != "app-linux-amd64.tar.gz" {
		t.Errorf("asset name = %q, unexpected value", asset.Name)
	}

	if asset.URL != "https://cdn.example.com/app-linux-amd64.tar.gz" {
		t.Errorf("asset URL = %q, want the entered link URL", asset.URL)
	}

	// GitLab reports neither size nor content type for asset links.
	if asset.Size != 0 || asset.ContentType != "" {
		t.Errorf("asset = %+v, want empty size and content type", asset)
	}

	// A link entered without a URL falls back to the derived direct asset URL.
	if got, want := feed.Releases[0].Assets[1].URL, "https://git.example.com/group/project/-/releases/v1.0.0/downloads/app.exe"; got != want {
		t.Errorf("fallback asset URL = %q, want %q", got, want)
	}
}

func TestFetchFeedURLsComeFromServerNotAPI(t *testing.T) {
	// The API points at the test server (simulating an API proxy); the
	// human-facing feed and release URLs must stay on the default server.
	rec := newRecordingServer(t, []releaseDTO{releaseDTOFor("v1.0.0", time.Hour)})

	source, err := New("group/project", WithAPIURL(rec.server.URL))
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	feed, err := source.Fetch(context.Background(), updates.FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if feed.RepositoryURL != "https://gitlab.com/group/project" {
		t.Errorf("RepositoryURL = %q, want the server URL, not the API URL", feed.RepositoryURL)
	}

	if feed.ReleasesURL != "https://gitlab.com/group/project/-/releases" {
		t.Errorf("ReleasesURL = %q, want the server URL, not the API URL", feed.ReleasesURL)
	}

	if want := "https://gitlab.com/group/project/-/releases/v1.0.0"; feed.Releases[0].URL != want {
		t.Errorf("release URL = %q, want a URL derived from the server URL, not the API URL", feed.Releases[0].URL)
	}
}

func TestFetchUsesProviderSelfLink(t *testing.T) {
	release := releaseDTOFor("v1.0.0", time.Hour)
	release.Links = releaseLinksDTO{Self: "https://git.example.com/group/project/-/releases/v1.0.0"}

	rec := newRecordingServer(t, []releaseDTO{release})
	source := newTestSource(rec)

	feed, err := source.Fetch(context.Background(), updates.FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if got, want := feed.Releases[0].URL, release.Links.Self; got != want {
		t.Errorf("release URL = %q, want the provider self link %q", got, want)
	}
}

func TestFetchDerivesReleaseURLWhenSelfLinkMissing(t *testing.T) {
	release := releaseDTOFor("v1.0.0", time.Hour)
	release.Links = releaseLinksDTO{}

	rec := newRecordingServer(t, []releaseDTO{release})
	source := newTestSource(rec)

	feed, err := source.Fetch(context.Background(), updates.FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	want := rec.server.URL + "/group/project/-/releases/v1.0.0"
	if got := feed.Releases[0].URL; got != want {
		t.Errorf("release URL = %q, want the derived %q", got, want)
	}
}

func TestReleaseURLEscapesTag(t *testing.T) {
	source, err := New("group/project")
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	want := "https://gitlab.com/group/project/-/releases/release%2Fv1"
	if got := source.releaseURL("release/v1"); got != want {
		t.Errorf("releaseURL = %q, want %q", got, want)
	}
}

func TestFetchFallsBackToCreatedAt(t *testing.T) {
	createdAt := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)

	release := releaseDTOFor("v1.0.0", time.Hour)
	release.ReleasedAt = time.Time{}
	release.CreatedAt = createdAt

	rec := newRecordingServer(t, []releaseDTO{release})
	source := newTestSource(rec)

	feed, err := source.Fetch(context.Background(), updates.FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if !feed.Releases[0].PublishedAt.Equal(createdAt) {
		t.Errorf("PublishedAt = %v, want the creation date %v", feed.Releases[0].PublishedAt, createdAt)
	}
}

func TestFetchPreservesReleaseFields(t *testing.T) {
	release := releaseDTOFor("v1.0.0", time.Hour)

	rec := newRecordingServer(t, []releaseDTO{release})
	source := newTestSource(rec)

	feed, err := source.Fetch(context.Background(), updates.FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	wantURL := rec.server.URL + "/group/project/-/releases/v1.0.0"
	got := feed.Releases[0]
	if got.Title != release.Name || got.Body != release.Description || got.Version != release.TagName || got.URL != wantURL {
		t.Errorf("release = %+v, fields were not preserved", got)
	}

	if got.PublishedAt.IsZero() {
		t.Error("PublishedAt is zero, want the release date")
	}
}

func TestFetchEmptyFeed(t *testing.T) {
	rec := newRecordingServer(t, []releaseDTO{})
	source := newTestSource(rec)

	feed, err := source.Fetch(context.Background(), updates.FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch returned error for empty feed: %v", err)
	}

	if len(feed.Releases) != 0 {
		t.Errorf("len(releases) = %d, want 0", len(feed.Releases))
	}

	if feed.RepositoryURL == "" || feed.ReleasesURL == "" {
		t.Errorf("feed URLs are empty: %+v", feed)
	}
}

func TestFetchNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)

		_, _ = fmt.Fprint(w, `{"message":"401 Unauthorized"}`)
	}))
	defer srv.Close()

	source, err := New("group/project", WithAPIURL(srv.URL))
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	_, err = source.Fetch(context.Background(), updates.FetchOptions{})

	var statusErr *fetch.StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error = %v, want *fetch.StatusError", err)
	}

	if statusErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status code = %d, want 401", statusErr.StatusCode)
	}
}

func TestFetchMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `<html>not json</html>`)
	}))
	defer srv.Close()

	source, err := New("group/project", WithAPIURL(srv.URL))
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if _, err := source.Fetch(context.Background(), updates.FetchOptions{}); err == nil {
		t.Fatal("Fetch returned nil error for malformed JSON")
	}
}

func TestFetchResponseTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, strings.Repeat("a", int(fetch.MaxResponseBytes)+10))
	}))
	defer srv.Close()

	source, err := New("group/project", WithAPIURL(srv.URL))
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	_, err = source.Fetch(context.Background(), updates.FetchOptions{})
	if !errors.Is(err, fetch.ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
}

func TestFetchContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)

		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	source, err := New("group/project", WithAPIURL(srv.URL))
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err = source.Fetch(ctx, updates.FetchOptions{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestFetchNegativeLimit(t *testing.T) {
	rec := newRecordingServer(t, []releaseDTO{})
	source := newTestSource(rec)

	if _, err := source.Fetch(context.Background(), updates.FetchOptions{Limit: -1}); err == nil {
		t.Fatal("Fetch returned nil error for a negative limit")
	}

	if len(rec.queries) != 0 {
		t.Errorf("made %d requests, want 0 (negative limit must fail before any request)", len(rec.queries))
	}
}
