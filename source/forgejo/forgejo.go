// Package forgejo provides a release Source backed by the Forgejo releases
// API, which is compatible with the Gitea releases API.
//
// Forgejo is self-hosted, so no default deployment exists: the server URL is a
// required constructor argument. The API base defaults to
// "<server>/api/v1" and can be overridden through the API URL option, for
// example when requests must go through an API proxy. Authentication and
// transport customization belong to the caller-provided HTTP client.
package forgejo

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
	// apiPath is the releases API base path derived from the server URL.
	apiPath = "/api/v1"

	// maxPerPage is the maximum number of releases the API returns per page.
	// Larger page sizes are clamped to this limit server-side, so requesting
	// more would silently fetch nothing extra.
	maxPerPage = 50

	acceptHeader = "application/json"
)

// Source fetches releases for a single Forgejo repository.
type Source struct {
	client    *http.Client
	serverURL string
	apiURL    string
	owner     string
	repo      string
}

type options struct {
	client *http.Client
	apiURL string
	apiSet bool
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

// WithAPIURL sets the API base URL, overriding the default derived from the
// server URL. The API URL never changes the human-facing server URL: feed and
// release links stay on the server even when requests are routed through an
// API proxy.
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

// New creates a Source for repository in "owner/repository" form hosted on
// serverURL. serverURL is required: Forgejo is self-hosted and the library has
// no default instance to fall back to.
func New(serverURL, repository string, opts ...Option) (*Source, error) {
	server, err := normalizeBaseURL(serverURL)
	if err != nil {
		return nil, fmt.Errorf("forgejo: %w", err)
	}

	owner, repo, err := parseRepository(repository)
	if err != nil {
		return nil, fmt.Errorf("forgejo: %w", err)
	}

	o := &options{}

	for _, opt := range opts {
		if opt == nil {
			continue
		}

		if err := opt(o); err != nil {
			return nil, fmt.Errorf("forgejo: %w", err)
		}
	}

	// The API URL is resolved once after all options are applied, so the final
	// endpoint does not depend on option order.
	api := server + apiPath
	if o.apiSet {
		api = o.apiURL
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
// Documented assumption: the provider returns releases newest-first, ordered
// by creation time; a defensive SortNewestFirst is applied regardless, so the
// feed is always newest-first even if the server orders differently. Drafts
// are dropped during conversion and never appear in the feed, and no
// server-side draft or pre-release filtering parameters are sent: eligibility
// is decided locally so it does not depend on server-side filter support.
func (s *Source) Fetch(ctx context.Context, opts updates.FetchOptions) (updates.Feed, error) {
	limit, err := fetch.ResolveLimit(opts.Limit)
	if err != nil {
		return updates.Feed{}, fmt.Errorf("forgejo: %w", err)
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
		"%s/repos/%s/%s/releases?limit=%d&page=%d",
		s.apiURL,
		url.PathEscape(s.owner),
		url.PathEscape(s.repo),
		perPage,
		page,
	)

	// #nosec G107 -- the URL is assembled from validated caller configuration
	// and escaped path segments, not from untrusted input.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, fmt.Errorf("forgejo: building request: %w", err)
	}

	req.Header.Set("Accept", acceptHeader)

	var releases []releaseDTO
	if err := fetch.DoJSON(ctx, s.client, req, &releases); err != nil {
		return nil, false, fmt.Errorf("forgejo: fetching releases page %d: %w", page, err)
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
	CreatedAt   time.Time  `json:"created_at"`
	Assets      []assetDTO `json:"assets"`
}

// convertRelease maps a provider release to the common model. The boolean
// result reports eligibility: drafts are dropped here because the common model
// intentionally has no draft state, so a draft can never reach a consumer.
func convertRelease(dto releaseDTO) (updates.Release, bool) {
	if dto.Draft {
		return updates.Release{}, false
	}

	publishedAt := dto.PublishedAt
	if publishedAt.IsZero() {
		// Releases created without a publication timestamp keep the release
		// creation time, so ordering still has a usable instant.
		publishedAt = dto.CreatedAt
	}

	release := updates.Release{
		Version:     dto.TagName,
		Title:       dto.Name,
		Body:        dto.Body,
		URL:         dto.HTMLURL,
		PublishedAt: publishedAt,
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
