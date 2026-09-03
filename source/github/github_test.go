package github

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

func TestNewValidatesRepository(t *testing.T) {
	for _, repository := range []string{
		"",
		"owner",
		"owner/",
		"/repo",
		"owner/repo/extra",
		"a/b/c",
	} {
		t.Run(repository, func(t *testing.T) {
			if _, err := New(repository); err == nil {
				t.Errorf("New(%q) returned nil error, want invalid repository error", repository)
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	source, err := New("owner/repo")
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if source.serverURL != defaultServerURL {
		t.Errorf("serverURL = %q, want %q", source.serverURL, defaultServerURL)
	}

	if source.apiURL != defaultAPIURL {
		t.Errorf("apiURL = %q, want %q", source.apiURL, defaultAPIURL)
	}

	if source.client == nil {
		t.Error("client is nil, want a default client")
	}
}

func TestNewEnterpriseAPIDerivation(t *testing.T) {
	source, err := New("owner/repo", WithServerURL("https://ghe.example.com/"))
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if source.serverURL != "https://ghe.example.com" {
		t.Errorf("serverURL = %q, want trailing slash trimmed", source.serverURL)
	}

	if source.apiURL != "https://ghe.example.com/api/v3" {
		t.Errorf("apiURL = %q, want Enterprise API derived from server URL", source.apiURL)
	}
}

func TestNewExplicitAPIURLWinsOverDerivation(t *testing.T) {
	source, err := New("owner/repo",
		WithServerURL("https://ghe.example.com"),
		WithAPIURL("https://api.example.com"),
	)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if source.apiURL != "https://api.example.com" {
		t.Errorf("apiURL = %q, want explicit API URL", source.apiURL)
	}
}

func TestNewOptionOrderIndependence(t *testing.T) {
	first, err := New("owner/repo",
		WithServerURL("https://ghe.example.com"),
		WithAPIURL("https://api.example.com"),
	)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	second, err := New("owner/repo",
		WithAPIURL("https://api.example.com"),
		WithServerURL("https://ghe.example.com"),
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
	source, err := New("owner/repo", WithAPIURL("https://api-proxy.example.com"))
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

func TestNewRejectsInvalidOptions(t *testing.T) {
	tests := map[string]Option{
		"empty server URL": WithServerURL("   "),
		"empty API URL":    WithAPIURL(""),
		"bad scheme":       WithServerURL("ftp://example.com"),
		"missing host":     WithAPIURL("not a url"),
	}

	for name, opt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := New("owner/repo", opt); err == nil {
				t.Errorf("New returned nil error for %s", name)
			}
		})
	}
}

func TestNewWithHTTPClient(t *testing.T) {
	client := &http.Client{Timeout: 5 * time.Second}

	source, err := New("owner/repo", WithHTTPClient(client))
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if source.client != client {
		t.Error("injected client was not used")
	}
}

// recordingServer captures requests and replies with scripted releases.
type recordingServer struct {
	t         *testing.T
	server    *httptest.Server
	headers   []http.Header
	queries   []string
	responses [][]releaseDTO
}

func newRecordingServer(t *testing.T, pages ...[]releaseDTO) *recordingServer {
	rec := &recordingServer{t: t, responses: pages}

	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.headers = append(rec.headers, r.Header.Clone())
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
		Body:        "Body of " + tag,
		HTMLURL:     "https://github.com/owner/repo/releases/tag/" + tag,
		PublishedAt: time.Now().Add(-age),
	}
}

func newTestSource(rec *recordingServer, opts ...Option) *Source {
	options := append([]Option{
		WithServerURL(rec.server.URL),
		WithAPIURL(rec.server.URL),
	}, opts...)

	source, err := New("owner/repo", options...)
	if err != nil {
		rec.t.Fatalf("New returned error: %v", err)
	}

	return source
}

func TestFetchSendsGitHubHeadersAndQuery(t *testing.T) {
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

	if got := rec.headers[0].Get(apiVersionKey); got != apiVersion {
		t.Errorf("%s header = %q, want %q", apiVersionKey, got, apiVersion)
	}

	query := rec.queries[0]
	if !strings.Contains(query, "per_page=10") {
		t.Errorf("query %q does not request per_page=10", query)
	}

	if !strings.Contains(query, "page=1") {
		t.Errorf("query %q does not request page=1", query)
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

func TestFetchDropsDrafts(t *testing.T) {
	draft := releaseDTOFor("v2.0.0-rc", time.Hour)
	draft.Draft = true

	rec := newRecordingServer(t, []releaseDTO{draft, releaseDTOFor("v1.0.0", 2*time.Hour)})
	source := newTestSource(rec)

	feed, err := source.Fetch(context.Background(), updates.FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if len(feed.Releases) != 1 || feed.Releases[0].Version != "v1.0.0" {
		t.Errorf("drafts must never appear in the feed: %v", feed.Releases)
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

func TestFetchPaginatesUntilLimitSatisfied(t *testing.T) {
	// Two pages: the first is full of drafts, so pagination must continue
	// until the eligible limit is satisfied.
	draft := releaseDTOFor("v2.0.0", time.Hour)
	draft.Draft = true

	rec := newRecordingServer(t,
		[]releaseDTO{draft, draft, draft},
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
		t.Fatalf("made %d requests, want 2 (drafts must not satisfy the limit)", len(rec.queries))
	}

	if !strings.Contains(rec.queries[1], "per_page=2") {
		t.Errorf("second request query %q does not request the remaining need (2)", rec.queries[1])
	}
}

func TestFetchMapsAssets(t *testing.T) {
	release := releaseDTOFor("v1.0.0", time.Hour)
	release.Assets = []assetDTO{{
		Name:               "app-linux-amd64.tar.gz",
		BrowserDownloadURL: "https://github.com/owner/repo/releases/download/v1.0.0/app-linux-amd64.tar.gz",
		Size:               12345,
		ContentType:        "application/gzip",
	}}

	rec := newRecordingServer(t, []releaseDTO{release})
	source := newTestSource(rec)

	feed, err := source.Fetch(context.Background(), updates.FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if len(feed.Releases) != 1 || len(feed.Releases[0].Assets) != 1 {
		t.Fatalf("assets were not mapped: %+v", feed.Releases)
	}

	asset := feed.Releases[0].Assets[0]
	if asset.Name != "app-linux-amd64.tar.gz" || asset.Size != 12345 || asset.ContentType != "application/gzip" {
		t.Errorf("asset = %+v, unexpected mapping", asset)
	}

	if asset.URL != "https://github.com/owner/repo/releases/download/v1.0.0/app-linux-amd64.tar.gz" {
		t.Errorf("asset URL = %q, unexpected value", asset.URL)
	}
}

func TestFetchFeedURLsComeFromServerNotAPI(t *testing.T) {
	// The API points at the test server (simulating an API proxy); the
	// human-facing feed URLs must stay on the default GitHub server.
	rec := newRecordingServer(t, []releaseDTO{releaseDTOFor("v1.0.0", time.Hour)})

	source, err := New("owner/repo", WithAPIURL(rec.server.URL))
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	feed, err := source.Fetch(context.Background(), updates.FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if feed.RepositoryURL != "https://github.com/owner/repo" {
		t.Errorf("RepositoryURL = %q, want the server URL, not the API URL", feed.RepositoryURL)
	}

	if feed.ReleasesURL != "https://github.com/owner/repo/releases" {
		t.Errorf("ReleasesURL = %q, want the server URL, not the API URL", feed.ReleasesURL)
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

	got := feed.Releases[0]
	if got.Title != release.Name || got.Body != release.Body || got.URL != release.HTMLURL {
		t.Errorf("release = %+v, fields were not preserved", got)
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
}

func TestFetchNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)

		_, _ = fmt.Fprint(w, `{"message":"Bad credentials"}`)
	}))
	defer srv.Close()

	source, err := New("owner/repo", WithAPIURL(srv.URL))
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

	source, err := New("owner/repo", WithAPIURL(srv.URL))
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

	source, err := New("owner/repo", WithAPIURL(srv.URL))
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

	source, err := New("owner/repo", WithAPIURL(srv.URL))
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
