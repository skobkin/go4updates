// Package gitlab provides a release Source backed by the GitLab releases API.
//
// The public gitlab.com deployment is the default endpoint; self-hosted GitLab
// instances are supported through the server and API URL options. Projects are
// addressed by their full path, which may include subgroups. Authentication and
// transport customization belong to the caller-provided HTTP client.
//
// GitLab reports neither size nor content type for release asset links, so the
// mapped assets carry only a name and a URL. GitLab also has no draft state: a
// release whose released_at lies in the future is reported as an upcoming
// release, and upcoming releases are dropped during conversion so they can
// never be mistaken for the newest published version. The release API carries
// no pre-release flag either, so a tag whose version has a semantic-version
// pre-release suffix, such as v2.0.0-rc.1, is classified as a pre-release from
// the tag itself.
package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/internal/fetch"
	"golang.org/x/mod/semver"
)

const (
	defaultServerURL = "https://gitlab.com"

	// apiPath is the API base path appended to the server URL when no explicit
	// API URL is configured.
	apiPath = "/api/v4"

	// maxPerPage is the maximum number of releases the API returns per page.
	maxPerPage = 100

	acceptHeader = "application/json"
)

// Source fetches releases for a single GitLab project.
type Source struct {
	client    *http.Client
	serverURL string
	apiURL    string
	project   string
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

// WithServerURL sets the human-facing server base URL, for example a
// self-hosted GitLab instance. When set without an explicit API URL, the
// matching API base is derived from it.
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

// New creates a Source for a project in "[group/]subgroup/project" form, the
// project path GitLab uses in its web UI. The default gitlab.com deployment is
// used unless WithServerURL selects a self-hosted instance.
func New(project string, opts ...Option) (*Source, error) {
	projectPath, err := parseProjectPath(project)
	if err != nil {
		return nil, fmt.Errorf("gitlab: %w", err)
	}

	o := &options{}

	for _, opt := range opts {
		if opt == nil {
			continue
		}

		if err := opt(o); err != nil {
			return nil, fmt.Errorf("gitlab: %w", err)
		}
	}

	// Server and API URLs are resolved once after all options are applied,
	// so the final endpoints do not depend on option order. The API default
	// is derived from the resolved server URL, while an explicit API URL
	// overrides it without ever changing the server URL.
	server := defaultServerURL
	if o.serverSet {
		server = o.serverURL
	}

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
		project:   projectPath,
	}, nil
}

// Fetch implements updates.Source.
//
// GitLab documents that the releases endpoint returns releases sorted by
// released_at in descending order, which is newest first. The accumulated
// result is re-sorted defensively before the fetch options are applied anyway,
// so the returned feed is always newest-first regardless of provider ordering.
// Upcoming releases are dropped during conversion and never appear in the feed.
func (s *Source) Fetch(ctx context.Context, opts updates.FetchOptions) (updates.Feed, error) {
	limit, err := fetch.ResolveLimit(opts.Limit)
	if err != nil {
		return updates.Feed{}, fmt.Errorf("gitlab: %w", err)
	}

	releases, err := fetch.Paginate(ctx, limit, maxPerPage, s.fetchPage, s.convertRelease, opts.IncludePrereleases)
	if err != nil {
		return updates.Feed{}, err
	}

	return updates.Feed{
		RepositoryURL: s.repositoryURL(),
		ReleasesURL:   s.releasesURL(),
		Releases:      releases,
	}, nil
}

// fetchPage fetches one page of raw provider releases. The project path is
// percent-encoded with %2F for its separators because GitLab expects the whole
// path to be a single URL path element, and the endpoint is assembled by string
// concatenation because url.JoinPath would decode the escaping again.
func (s *Source) fetchPage(ctx context.Context, page, perPage int) ([]releaseDTO, bool, error) {
	endpoint := fmt.Sprintf(
		"%s/projects/%s/releases?per_page=%d&page=%d",
		s.apiURL,
		url.PathEscape(s.project),
		perPage,
		page,
	)

	// #nosec G107 -- the URL is assembled from caller-configured server and
	// API URL options, never from untrusted input.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, fmt.Errorf("gitlab: building request: %w", err)
	}

	req.Header.Set("Accept", acceptHeader)

	var releases []releaseDTO
	if err := fetch.DoJSON(ctx, s.client, req, &releases); err != nil {
		return nil, false, fmt.Errorf("gitlab: fetching releases page %d: %w", page, err)
	}

	return releases, len(releases) < perPage, nil
}

// repositoryURL returns the human-facing project page.
func (s *Source) repositoryURL() string {
	return fmt.Sprintf("%s/%s", s.serverURL, s.project)
}

// releasesURL returns the human-facing release listing page.
func (s *Source) releasesURL() string {
	return s.repositoryURL() + "/-/releases"
}

// releaseURL returns the human-facing page of a single release tag. It is used
// as a fallback when the provider does not report a release URL of its own.
func (s *Source) releaseURL(tag string) string {
	return fmt.Sprintf("%s/-/releases/%s", s.repositoryURL(), url.PathEscape(tag))
}

// convertRelease maps a provider release to the common model. The boolean
// result reports eligibility: upcoming releases are dropped here because the
// common model intentionally has no draft or upcoming state, and a release
// that is only planned for the future must not be treated as published.
func (s *Source) convertRelease(dto releaseDTO) (updates.Release, bool) {
	if dto.upcoming() {
		return updates.Release{}, false
	}

	release := updates.Release{
		Version:     dto.TagName,
		Title:       dto.Name,
		Body:        dto.Description,
		URL:         s.releaseLink(dto),
		PublishedAt: dto.publishedAt(),
		Prerelease:  dto.isPrerelease(),
	}

	for _, link := range dto.Assets.Links {
		release.Assets = append(release.Assets, updates.Asset{
			Name: link.Name,
			URL:  link.assetURL(),
		})
	}

	return release, true
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

// parseProjectPath validates a project path in "[group/]subgroup/project"
// form, where at least one subgroup namespace is required, and returns the
// trimmed path.
func parseProjectPath(project string) (string, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return "", errors.New("project path must not be empty")
	}

	if strings.ContainsFunc(project, unicode.IsSpace) {
		return "", fmt.Errorf("invalid project path %q: must not contain whitespace", project)
	}

	segments := strings.Split(project, "/")
	if len(segments) < 2 || slices.Contains(segments, "") {
		return "", fmt.Errorf("invalid project path %q: expected \"[group/]project\"", project)
	}

	return project, nil
}

type assetLinkDTO struct {
	// Name is the label shown for the link on the release page.
	Name string `json:"name"`

	// URL is the link address as entered when the asset link was created.
	URL string `json:"url"`

	// DirectAssetURL is the canonical direct download address GitLab derives
	// from the optional direct_asset_path of the link.
	DirectAssetURL string `json:"direct_asset_url"`
}

// assetURL returns the download location of the asset link. The entered URL is
// authoritative; the derived direct asset URL is only used when it is missing.
func (a assetLinkDTO) assetURL() string {
	if a.URL != "" {
		return a.URL
	}

	return a.DirectAssetURL
}

type assetsDTO struct {
	Links []assetLinkDTO `json:"links"`
}

type releaseLinksDTO struct {
	// Self is the human-facing page of the release.
	Self string `json:"self"`
}

type releaseDTO struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	ReleasedAt  time.Time `json:"released_at"`
	CreatedAt   time.Time `json:"created_at"`

	// Upcoming is a tolerated alias of the documented upcoming_release
	// attribute, so both spellings mark a release planned for the future.
	Upcoming bool `json:"upcoming"`

	// UpcomingRelease is the attribute GitLab documents for releases whose
	// released_at lies in the future.
	UpcomingRelease bool `json:"upcoming_release"`

	// Prerelease is not among the documented GitLab release attributes, the
	// API has no pre-release flag today. It is decoded anyway so a pre-release
	// marker returned by a future or modified deployment still honors
	// FetchOptions.IncludePrereleases exactly like the other adapters do.
	Prerelease bool `json:"prerelease"`

	Links  releaseLinksDTO `json:"_links"`
	Assets assetsDTO       `json:"assets"`
}

// isPrerelease reports whether the release must be treated as a pre-release.
//
// Because the GitLab release API documents no pre-release flag, a tag whose
// version carries a semantic-version pre-release suffix, such as
// "v2.0.0-rc.1", is classified from the tag itself; without that, a candidate
// release would survive the default pre-release filter and be reported as the
// newest version. An explicit flag returned by a deployment that has one is
// still honored.
func (r releaseDTO) isPrerelease() bool {
	return r.Prerelease || hasSemverPrerelease(r.TagName)
}

// hasSemverPrerelease reports whether tag is a semantic version with a
// pre-release suffix. A leading "v" is optional, matching how the default
// comparator reads versions. Tags that are not semantic versions are never
// classified as pre-releases.
func hasSemverPrerelease(tag string) bool {
	version := strings.TrimSpace(tag)
	if version == "" {
		return false
	}

	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}

	return semver.Prerelease(version) != ""
}

// upcoming reports whether the release is only planned for the future.
func (r releaseDTO) upcoming() bool {
	return r.Upcoming || r.UpcomingRelease
}

// publishedAt returns the release date, falling back to the creation date for
// releases that carry no explicit release date.
func (r releaseDTO) publishedAt() time.Time {
	if !r.ReleasedAt.IsZero() {
		return r.ReleasedAt
	}

	return r.CreatedAt
}

// releaseLink returns the human-facing release page, deriving one from the
// server URL and the tag when the provider does not report it.
func (s *Source) releaseLink(dto releaseDTO) string {
	if dto.Links.Self != "" {
		return dto.Links.Self
	}

	return s.releaseURL(dto.TagName)
}
