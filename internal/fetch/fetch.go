// Package fetch contains the HTTP and normalization plumbing shared by all
// provider adapters. It is internal: adapters use it to keep response
// handling, filtering, and ordering behavior identical across providers.
package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// MaxResponseBytes bounds every provider response body so a malicious
	// endpoint cannot stream an unbounded payload into the process.
	MaxResponseBytes int64 = 4 << 20 // 4 MiB

	// maxErrorSnippet bounds the body excerpt attached to status errors.
	maxErrorSnippet = 1024

	// defaultTimeout is the client timeout used when the caller supplies none.
	defaultTimeout = 15 * time.Second
)

// ErrResponseTooLarge reports a response body larger than MaxResponseBytes.
var ErrResponseTooLarge = errors.New("go4updates: response body exceeds size limit")

// StatusError describes a non-2xx HTTP response from a provider.
type StatusError struct {
	// URL is the request URL that produced the response, with any password
	// it carried redacted.
	URL string

	// StatusCode is the HTTP status code.
	StatusCode int

	// Status is the HTTP status line.
	Status string

	// Body holds up to maxErrorSnippet bytes of the response body.
	Body string
}

// Error implements error.
func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("go4updates: unexpected HTTP status %s for %s", e.Status, e.URL)
	}

	return fmt.Sprintf("go4updates: unexpected HTTP status %s for %s: %s", e.Status, e.URL, e.Body)
}

// DefaultClient returns a fresh HTTP client with a conservative default
// timeout. A new instance is created per call; the package keeps no global
// client.
func DefaultClient() *http.Client {
	return &http.Client{Timeout: defaultTimeout}
}

// DoJSON executes req with client, validates the response status, bounds the
// response body, and decodes it as JSON into dst.
//
// A nil client selects DefaultClient. Non-2xx responses yield a *StatusError
// suitable for errors.As. Body size is enforced through MaxResponseBytes,
// with ErrResponseTooLarge in the error chain when exceeded.
func DoJSON(ctx context.Context, client *http.Client, req *http.Request, dst any) error {
	if client == nil {
		client = DefaultClient()
	}

	resp, err := client.Do(req)
	if err != nil {
		// The URL may carry signed query tokens the http.Client renders
		// verbatim into url.Error messages; they never reach the caller.
		return fmt.Errorf("go4updates: performing request: %w", SanitizeURLError(err))
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Errors carry a redacted URL: callers may point an adapter at a
		// server URL with basic-auth userinfo, and the error surfaces in
		// logs, so neither the password nor any query token appears in it.
		return NewStatusError(RedactURL(req.URL), resp)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("go4updates: reading response body: %w", err)
	}

	if int64(len(data)) > MaxResponseBytes {
		return fmt.Errorf("go4updates: %w: response for %s exceeds %d bytes", ErrResponseTooLarge, RedactURL(req.URL), MaxResponseBytes)
	}

	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("go4updates: decoding response body: %w", err)
	}

	return nil
}

// RedactURL renders u for error messages: a userinfo password is masked by
// url.Redacted, and the query string and fragment are dropped entirely —
// signed URLs commonly carry their auth tokens in query parameters, where
// url.Redacted would leave them.
func RedactURL(u *url.URL) string {
	if u == nil {
		return ""
	}

	clone := *u
	clone.RawQuery = ""
	clone.ForceQuery = false
	clone.Fragment = ""
	clone.RawFragment = ""

	return clone.Redacted()
}

// RedactURLString applies the same redaction as RedactURL to a raw URL
// string, for call sites that never parsed the URL. An unparseable URL is
// cut at its query separator; a token in a fragment survives only in that
// unparseable corner case.
func RedactURLString(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return RedactURL(u)
	}

	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}

	return raw
}

// SanitizeURLError rewrites a *url.Error so its rendered message cannot
// carry the request URL's query string: net/http wraps transport and
// redirect failures in url.Error values with the full request URL, and
// redirect chains nest them one level deep. The wrapped error chain —
// including context cancellation — is preserved for errors.Is and
// errors.As; errors that carry no *url.Error pass through unchanged.
func SanitizeURLError(err error) error {
	var ue *url.Error

	if !errors.As(err, &ue) {
		return err
	}

	sanitized := *ue
	sanitized.URL = RedactURLString(ue.URL)
	sanitized.Err = SanitizeURLError(ue.Err)

	return &sanitized
}

// NewStatusError builds a StatusError from a non-2xx response, consuming a
// bounded body snippet for context. The URL is expected to be already
// redacted by the caller (see RedactURL). It is exported for use by other
// packages within the module, so status errors keep identical shape
// everywhere.
func NewStatusError(url string, resp *http.Response) *StatusError {
	snippet, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorSnippet))
	if err != nil {
		snippet = nil
	}

	return &StatusError{
		URL:        url,
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       strings.TrimSpace(string(snippet)),
	}
}
