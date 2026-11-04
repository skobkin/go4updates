package selfupdate

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/internal/fetch"
)

// DefaultMaxAssetBytes bounds a single asset download when no limit is
// configured with [WithMaxAssetBytes].
const DefaultMaxAssetBytes int64 = 256 << 20 // 256 MiB

// StatusError describes a non-2xx HTTP response from an asset or checksum
// download. The URL is redacted: any password it carried in userinfo never
// appears in the error, and the query string is dropped entirely — signed
// asset URLs commonly carry their auth tokens there.
type StatusError struct {
	// URL is the request URL that produced the response, with any password
	// redacted and the query string and fragment removed.
	URL string

	// StatusCode is the HTTP status code.
	StatusCode int

	// Status is the HTTP status line.
	Status string

	// Body holds up to 1 KiB of the response body for context.
	Body string
}

// Error implements error.
func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("go4updates/selfupdate: unexpected HTTP status %s for %s", e.Status, e.URL)
	}

	return fmt.Sprintf("go4updates/selfupdate: unexpected HTTP status %s for %s: %s", e.Status, e.URL, e.Body)
}

// statusErrorFrom converts a non-2xx response into the public StatusError,
// reusing the internal bounded-snippet construction.
func statusErrorFrom(redactedURL string, resp *http.Response) *StatusError {
	internal := fetch.NewStatusError(redactedURL, resp)

	return &StatusError{
		URL:        internal.URL,
		StatusCode: internal.StatusCode,
		Status:     internal.Status,
		Body:       internal.Body,
	}
}

// Downloader streams release asset bodies to an [io.Writer] under a hard
// size bound. URLs must be HTTPS unless [WithAllowHTTP] was set, and the
// guard holds for every redirect hop: a download can never silently
// downgrade from HTTPS to HTTP.
//
// The caller-supplied HTTP client is never mutated. The zero value is not
// usable directly; construct with [NewDownloader].
type Downloader struct {
	client    *http.Client
	maxBytes  int64
	allowHTTP bool
}

// NewDownloader creates a Downloader from opts. It understands
// [WithHTTPClient], [WithAllowHTTP], and [WithMaxAssetBytes].
func NewDownloader(opts ...Option) (*Downloader, error) {
	o, err := applyOptions("go4updates/selfupdate", opts)
	if err != nil {
		return nil, err
	}

	client := o.httpClient
	if client == nil {
		// No overall timeout on purpose: binaries can be large, the
		// transfer is bounded by the context and the size limit instead.
		client = &http.Client{}
	}

	maxBytes := o.maxAssetBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxAssetBytes
	}

	return &Downloader{
		client:    client,
		maxBytes:  maxBytes,
		allowHTTP: o.allowHTTP,
	}, nil
}

// Download streams the body at asset.URL into w. Non-2xx responses yield a
// [*StatusError] suitable for errors.As. The declared Content-Length is
// checked before the transfer and the streamed body is bounded by the
// configured size limit, so a lying or missing Content-Length cannot exceed
// it. On error the contents of w must be treated as invalid and discarded.
//
// The context bounds the whole transfer; cancellation surfaces as an error
// wrapping the context error.
func (d *Downloader) Download(ctx context.Context, asset updates.Asset, w io.Writer) error {
	return d.download(ctx, asset, w, nil)
}

// download implements Download, reporting streaming progress to onBytes
// when it is not nil. The first observation carries the declared
// Content-Length as the total (zero when the server does not declare one);
// the counting sink reports each copied chunk afterwards.
func (d *Downloader) download(ctx context.Context, asset updates.Asset, w io.Writer, onBytes progressFunc) error {
	if err := checkURLScheme(asset.URL, d.allowHTTP); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		// url.Parse failures embed the raw URL — signed query tokens
		// included — in their rendered message; the same redaction
		// invariant as the request and status errors below applies here.
		return fmt.Errorf("go4updates/selfupdate: building asset request: %w", fetch.SanitizeURLError(err))
	}

	resp, err := d.guardedClient().Do(req)
	if err != nil {
		// The http.Client renders the full request URL — signed query
		// tokens included — into url.Error messages; the query never
		// reaches the caller. The wrapped error chain is preserved.
		return fmt.Errorf("go4updates/selfupdate: requesting asset %q: %w", asset.Name, fetch.SanitizeURLError(err))
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The URL is redacted: errors surface in logs, and the asset URL
		// may carry basic-auth userinfo or signed query tokens.
		return statusErrorFrom(fetch.RedactURL(req.URL), resp)
	}

	if resp.ContentLength > d.maxBytes {
		return fmt.Errorf("%w: asset %q is %d bytes, limit is %d",
			ErrAssetTooLarge, asset.Name, resp.ContentLength, d.maxBytes)
	}

	// Report the declared total before the first byte is read, so
	// consumers learn whether the stage is determinate upfront. An unknown
	// total is not reported here: the first chunk observation carries it.
	// A chunked response reports a negative Content-Length; it means
	// unknown, never a negative total.
	total := resp.ContentLength

	if total < 0 {
		total = 0
	}

	if onBytes != nil && total > 0 {
		onBytes(0, total)
	}

	// The LimitReader sentinel (limit+1) would overflow for a limit of
	// math.MaxInt64, truncating the transfer to zero bytes; only apply it
	// when there is headroom, the streamed-byte check below covers the
	// rest.
	body := io.Reader(resp.Body)

	if d.maxBytes < math.MaxInt64 {
		body = io.LimitReader(body, d.maxBytes+1)
	}

	sink := w

	if onBytes != nil {
		sink = &progressWriter{w: w, onBytes: onBytes, total: total}
	}

	n, err := io.Copy(sink, body)
	if err != nil {
		return fmt.Errorf("go4updates/selfupdate: downloading asset %q: %w", asset.Name, err)
	}

	if n > d.maxBytes {
		return fmt.Errorf("%w: asset %q exceeds %d bytes", ErrAssetTooLarge, asset.Name, d.maxBytes)
	}

	return nil
}

// guardedClient returns a private copy of the configured client whose
// transport enforces the URL scheme policy on the initial request and on
// every redirect hop. The caller's client is never mutated.
func (d *Downloader) guardedClient() *http.Client {
	base := d.client
	if base == nil {
		base = &http.Client{}
	}

	c := *base
	c.Transport = &schemeGuardTransport{base: c.Transport, allowHTTP: d.allowHTTP}

	return &c
}

// checkURLScheme rejects non-HTTPS URLs before any request is made. Plain
// HTTP is accepted only when explicitly allowed.
func checkURLScheme(rawURL string, allowHTTP bool) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		// The parse error embeds the raw URL verbatim; sanitize it like
		// every other error that carries the asset URL.
		return fmt.Errorf("go4updates/selfupdate: parsing asset URL: %w", fetch.SanitizeURLError(err))
	}

	switch {
	case u.Scheme == "https":
		return nil

	case u.Scheme == "http" && allowHTTP:
		return nil

	default:
		return fmt.Errorf("%w: %s", ErrInsecureURL, fetch.RedactURL(u))
	}
}

// schemeGuardTransport enforces the HTTPS policy on every outgoing request,
// which covers redirect hops the http.Client follows on its own.
type schemeGuardTransport struct {
	// base is the wrapped transport; nil selects http.DefaultTransport so
	// the caller's proxy and TLS configuration stay in effect.
	base      http.RoundTripper
	allowHTTP bool
}

// RoundTrip implements http.RoundTripper.
func (t *schemeGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL != nil {
		scheme := req.URL.Scheme
		allowed := scheme == "https" || (t.allowHTTP && scheme == "http")

		if !allowed {
			return nil, fmt.Errorf("%w: %s", ErrInsecureURL, fetch.RedactURL(req.URL))
		}
	}

	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}

	return base.RoundTrip(req)
}
