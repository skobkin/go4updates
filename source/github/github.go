// Package github provides a release Source backed by the GitHub releases API.
//
// The public github.com deployment is the default endpoint; GitHub
// Enterprise-like hosts are supported through the server and API URL options.
// Authentication and transport customization belong to the caller-provided
// HTTP client.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/internal/fetch"
)

const (
	defaultServerURL = "https://github.com"
	defaultAPIURL    = "https://api.github.com"

	// gheAPIPath is the releases API path used by GitHub Enterprise-like hosts.
	gheAPIPath = "/api/v3"

	// maxPerPage is the maximum number of releases the API returns per page.
	maxPerPage = 100

	acceptHeader  = "application/vnd.github+json"
	apiVersion    = "2022-11-28"
	apiVersionKey = "X-GitHub-Api-Version"
)

// Source fetches releases for a single GitHub repository.
type Source struct {
	client    *http.Client
	serverURL string
	apiURL    string
	owner     string
	repo      string
}

type options struct {
	client    *http.Client
	serverURL string
	serverSet bool
	apiURL    string
	apiSet    bool
}

// Option configures a Source.
type Option func(*options) error

// WithHTTPClient sets the HTTP client used for API requests. When unset, a
// client with a conservative default timeout is used.
func WithHTTPClient(client *http.Client) Option {
	return func(o *options) error {
		o.client = client

		return nil
	}
}

// WithServerURL sets the human-facing server base URL, for example a GitHub
// Enterprise deployment. When set without an explicit API URL, the matching
// Enterprise API path is derived from it.
func WithServerURL(rawURL string) Option {
	return func(o *options) error {
		normalized, err := normalizeBaseURL(rawURL)
		if err != nil {
			return fmt.Errorf("invalid server URL: %w", err)
		}

		o.serverURL = normalized
		o.serverSet = true

		return nil
	}
}

// WithAPIURL sets the API base URL, overriding any derived default. The API
// URL never changes the human-facing server URL: feed and release links stay
// on the server even when requests are routed through an API proxy.
func WithAPIURL(rawURL string) Option {
	return func(o *options) error {
		normalized, err := normalizeBaseURL(rawURL)
		if err != nil {
			return fmt.Errorf("invalid API URL: %w", err)
		}

		o.apiURL = normalized
		o.apiSet = true

		return nil
	}
}

// New creates a Source for repository in "owner/repository" form.
func New(repository string, opts ...Option) (*Source, error) {
	owner, repo, err := parseRepository(repository)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}

	o := &options{}

	for _, opt := range opts {
		if opt == nil {
			continue
		}

		if err := opt(o); err != nil {
			return nil, fmt.Errorf("github: %w", err)
		}
	}

	// Server and API URLs are resolved once after all options are applied,
	// so the final endpoints do not depend on option order.
	server := defaultServerURL
	if o.serverSet {
		server = o.serverURL
	}

	api := defaultAPIURL

	switch {
	case o.apiSet:
		api = o.apiURL

	case o.serverSet:
		api = server + gheAPIPath
	}

	client := o.client
	if client == nil {
		client = fetch.DefaultClient()
	}

	return &Source{
		client:    client,
		serverURL: server,
		apiURL:    api,
		owner:     owner,
		repo:      repo,
	}, nil
}

// Fetch implements updates.Source.
//
// Releases are requested newest first and re-sorted defensively before the
// fetch options are applied, so the returned feed is always newest-first
// regardless of provider ordering. Drafts are dropped during conversion and
// never appear in the feed.
func (s *Source) Fetch(ctx context.Context, opts updates.FetchOptions) (updates.Feed, error) {
	limit, err := fetch.ResolveLimit(opts.Limit)
	if err != nil {
		return updates.Feed{}, fmt.Errorf("github: %w", err)
	}

	releases, err := fetch.Paginate(ctx, limit, maxPerPage, s.fetchPage, convertRelease, opts.IncludePrereleases)
	if err != nil {
		return updates.Feed{}, err
	}

	return updates.Feed{
		RepositoryURL: s.repositoryURL(),
		ReleasesURL:   s.releasesURL(),
		Releases:      releases,
	}, nil
}

// fetchPage fetches one page of raw provider releases.
func (s *Source) fetchPage(ctx context.Context, page, perPage int) ([]releaseDTO, bool, error) {
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/releases?per_page=%d&page=%d",
		s.apiURL,
		url.PathEscape(s.owner),
		url.PathEscape(s.repo),
		perPage,
		page,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, fmt.Errorf("github: building request: %w", err)
	}

	req.Header.Set("Accept", acceptHeader)
	req.Header.Set(apiVersionKey, apiVersion)

	var releases []releaseDTO
	if err := fetch.DoJSON(ctx, s.client, req, &releases); err != nil {
		return nil, false, fmt.Errorf("github: fetching releases page %d: %w", page, err)
	}

	return releases, len(releases) < perPage, nil
}

// repositoryURL returns the human-facing repository page.
func (s *Source) repositoryURL() string {
	return fmt.Sprintf("%s/%s/%s", s.serverURL, s.owner, s.repo)
}

// releasesURL returns the human-facing releases page.
func (s *Source) releasesURL() string {
	return s.repositoryURL() + "/releases"
}

// normalizeBaseURL validates and normalizes a base URL option value.
func normalizeBaseURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", errors.New("URL must not be empty")
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parsing URL: %w", err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("URL scheme must be http or https, got %q", parsed.Scheme)
	}

	if parsed.Host == "" {
		return "", errors.New("URL host must not be empty")
	}

	return strings.TrimRight(rawURL, "/"), nil
}

// parseRepository splits an "owner/repository" identifier.
func parseRepository(repository string) (string, string, error) {
	repository = strings.TrimSpace(repository)

	owner, repo, found := strings.Cut(repository, "/")
	if !found || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", fmt.Errorf("invalid repository %q: expected \"owner/repository\"", repository)
	}

	return owner, repo, nil
}

type assetDTO struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	ContentType        string `json:"content_type"`
}

type releaseDTO struct {
	TagName     string     `json:"tag_name"`
	Name        string     `json:"name"`
	Body        string     `json:"body"`
	HTMLURL     string     `json:"html_url"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	PublishedAt time.Time  `json:"published_at"`
	Assets      []assetDTO `json:"assets"`
}

// convertRelease maps a provider release to the common model. The boolean
// result reports eligibility: drafts are dropped here because the common
// model intentionally has no draft state.
func convertRelease(dto releaseDTO) (updates.Release, bool) {
	if dto.Draft {
		return updates.Release{}, false
	}

	release := updates.Release{
		Version:     dto.TagName,
		Title:       dto.Name,
		Body:        dto.Body,
		URL:         dto.HTMLURL,
		PublishedAt: dto.PublishedAt,
		Prerelease:  dto.Prerelease,
	}

	for _, asset := range dto.Assets {
		release.Assets = append(release.Assets, updates.Asset{
			Name:        asset.Name,
			URL:         asset.BrowserDownloadURL,
			Size:        asset.Size,
			ContentType: asset.ContentType,
		})
	}

	return release, true
}
