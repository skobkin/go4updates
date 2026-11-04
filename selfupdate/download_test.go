package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	updates "github.com/skobkin/go4updates"
)

func TestDownloadRejectsInsecureURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "payload")
	}))
	t.Cleanup(srv.Close)

	d, err := NewDownloader()
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}

	var buf bytes.Buffer

	err = d.Download(t.Context(), updates.Asset{Name: "app", URL: srv.URL + "/app"}, &buf)
	if !errors.Is(err, ErrInsecureURL) {
		t.Fatalf("err = %v, want ErrInsecureURL", err)
	}

	if buf.Len() != 0 {
		t.Errorf("writer should stay empty on refusal, got %d bytes", buf.Len())
	}
}

func TestDownloadHTTPSWithCustomClient(t *testing.T) {
	payload := "0123456789abcdef"

	var redirected bool

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			redirected = true

			http.Redirect(w, r, "/final", http.StatusFound)

			return
		}

		_, _ = fmt.Fprint(w, payload)
	}))
	t.Cleanup(srv.Close)

	d, err := NewDownloader(WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}

	var buf bytes.Buffer

	err = d.Download(t.Context(), updates.Asset{Name: "app", URL: srv.URL + "/start"}, &buf)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	if !redirected {
		t.Error("expected the redirect hop to be taken")
	}

	if buf.String() != payload {
		t.Errorf("downloaded %q, want %q", buf.String(), payload)
	}
}

func TestDownloadRejectsHTTPSDowngradeRedirect(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "should never be reached")
	}))
	t.Cleanup(plain.Close)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/downgraded", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	d, err := NewDownloader(WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}

	var buf bytes.Buffer

	err = d.Download(t.Context(), updates.Asset{Name: "app", URL: srv.URL + "/app"}, &buf)
	if !errors.Is(err, ErrInsecureURL) {
		t.Fatalf("err = %v, want ErrInsecureURL for the downgrade hop", err)
	}

	if buf.Len() != 0 {
		t.Errorf("writer should stay empty on refusal, got %d bytes", buf.Len())
	}
}

func TestDownloadAllowHTTPOptIn(t *testing.T) {
	payload := "plain-ok"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, payload)
	}))
	t.Cleanup(srv.Close)

	d, err := NewDownloader(WithAllowHTTP())
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}

	var buf bytes.Buffer

	err = d.Download(t.Context(), updates.Asset{Name: "app", URL: srv.URL + "/app"}, &buf)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	if buf.String() != payload {
		t.Errorf("downloaded %q, want %q", buf.String(), payload)
	}
}

func TestDownloadStatusError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)

		_, _ = fmt.Fprint(w, "no such asset")
	}))
	t.Cleanup(srv.Close)

	d, err := NewDownloader(WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}

	var buf bytes.Buffer

	err = d.Download(t.Context(), updates.Asset{Name: "app", URL: srv.URL + "/missing"}, &buf)

	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %v (%T), want *StatusError", err, err)
	}

	if statusErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", statusErr.StatusCode)
	}

	if statusErr.Body != "no such asset" {
		t.Errorf("Body = %q, want %q", statusErr.Body, "no such asset")
	}

	if !strings.HasPrefix(statusErr.URL, "https://") {
		t.Errorf("URL = %q, want the redacted request URL", statusErr.URL)
	}
}

// Signed asset URLs carry their auth tokens in query parameters, and
// url.Redacted masks userinfo passwords only. Errors surfaced by the
// downloader — status errors, transport failures, redirect failures — must
// never carry the query string, while still wrapping the underlying error
// (including context cancellation) for errors.Is and errors.As.
func TestDownloadErrorsRedactQueryTokens(t *testing.T) {
	const token = "SUPERSECRET-SIGNED-TOKEN-42"

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	newDownloader := func(t *testing.T, transport http.RoundTripper) *Downloader {
		t.Helper()

		client := srv.Client()
		if transport != nil {
			client = &http.Client{Transport: transport}
		}

		d, err := NewDownloader(WithHTTPClient(client))
		if err != nil {
			t.Fatalf("NewDownloader: %v", err)
		}

		return d
	}

	assertSecretAbsent := func(t *testing.T, err error) {
		t.Helper()

		if err == nil {
			t.Fatal("expected an error")

			return
		}

		if strings.Contains(err.Error(), token) {
			t.Fatalf("error %q leaks the signed query token", err.Error())
		}
	}

	t.Run("non-2xx status error", func(t *testing.T) {
		d := newDownloader(t, nil)

		var buf bytes.Buffer

		err := d.Download(t.Context(), updates.Asset{Name: "app", URL: srv.URL + "/asset?token=" + token}, &buf)
		assertSecretAbsent(t, err)

		var statusErr *StatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("err = %v (%T), want *StatusError", err, err)
		}

		if !strings.HasSuffix(statusErr.URL, "/asset") {
			t.Errorf("StatusError.URL = %q, want the path kept for diagnosis", statusErr.URL)
		}
	})

	t.Run("transport failure", func(t *testing.T) {
		d := newDownloader(t, failingTransport{errors.New("connection reset by peer")})

		var buf bytes.Buffer

		err := d.Download(t.Context(), updates.Asset{Name: "app", URL: srv.URL + "/asset?token=" + token}, &buf)
		assertSecretAbsent(t, err)

		var ue *url.Error
		if !errors.As(err, &ue) {
			t.Fatalf("err = %v (%T), want a *url.Error for the transport failure", err, err)
		}

		if strings.Contains(ue.URL, token) {
			t.Errorf("sanitized url.Error still carries the token: %q", ue.URL)
		}
	})

	t.Run("redirect failure", func(t *testing.T) {
		redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// Nothing listens on port 1; the redirected hop fails after the
			// token-bearing target URL has been built by the client.
			http.Redirect(w, nil, "https://127.0.0.1:1/next?token="+token, http.StatusFound)
		}))
		t.Cleanup(redirector.Close)

		d := newDownloader(t, nil)

		var buf bytes.Buffer

		err := d.Download(t.Context(), updates.Asset{Name: "app", URL: redirector.URL + "/asset?token=" + token}, &buf)
		assertSecretAbsent(t, err)
	})

	t.Run("cancellation still wraps the context error", func(t *testing.T) {
		d := newDownloader(t, nil)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		var buf bytes.Buffer

		err := d.Download(ctx, updates.Asset{Name: "app", URL: srv.URL + "/asset?token=" + token}, &buf)
		assertSecretAbsent(t, err)

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want it to wrap context.Canceled", err)
		}
	})
}

// Malformed URLs must not leak the raw asset URL either: url.Parse embeds
// it verbatim — signed query tokens included — in its error message, and
// the parse-error wraps used to bypass the redaction every other error
// path applies.
func TestDownloadParseErrorsRedactQueryTokens(t *testing.T) {
	const tokenURL = "https://releases.example.com/app.zip?token=SECRET\n"

	d, err := NewDownloader()
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}

	var buf bytes.Buffer

	err = d.Download(t.Context(), updates.Asset{Name: "app", URL: tokenURL}, &buf)
	if err == nil {
		t.Fatal("Download with a malformed URL succeeded")
	}

	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("building-asset-request error leaked the raw URL: %v", err)
	}

	err = checkURLScheme(tokenURL, false)
	if err == nil {
		t.Fatal("checkURLScheme accepted a malformed URL")
	}

	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("parsing-asset-URL error leaked the raw URL: %v", err)
	}
}

// failingTransport is a RoundTripper whose every request fails, standing in
// for network-level errors the http.Client wraps in a *url.Error.
type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, f.err
}

func TestDownloadSizeBound(t *testing.T) {
	// maxBytes must stay small: the bounded reader is exercised with real
	// payloads, not with half a gigabyte.
	const limit = int64(64)

	newDownloader := func(t *testing.T, srv *httptest.Server) *Downloader {
		t.Helper()

		d, err := NewDownloader(WithHTTPClient(srv.Client()), WithMaxAssetBytes(limit))
		if err != nil {
			t.Fatalf("NewDownloader: %v", err)
		}

		return d
	}

	t.Run("declared size over the limit fails before reading", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "128")

			_, _ = w.Write(make([]byte, 128))
		}))
		t.Cleanup(srv.Close)

		d := newDownloader(t, srv)

		var buf bytes.Buffer

		err := d.Download(t.Context(), updates.Asset{Name: "app", URL: srv.URL}, &buf)
		if !errors.Is(err, ErrAssetTooLarge) {
			t.Fatalf("err = %v, want ErrAssetTooLarge", err)
		}

		if buf.Len() != 0 {
			t.Errorf("writer should stay empty, got %d bytes", buf.Len())
		}
	})

	// Note: a declared-smaller-but-delivered-larger body cannot be simulated
	// through net/http servers — the server truncates and aborts. The
	// streaming bound (limit enforced without a usable Content-Length) is
	// covered by the chunked transfer test below.

	t.Run("missing content length over the limit", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// Chunked transfer encoding: no Content-Length.
			flusher := w.(http.Flusher)

			for range limit + 10 {
				_, _ = w.Write([]byte("x"))

				flusher.Flush()
			}
		}))
		t.Cleanup(srv.Close)

		d := newDownloader(t, srv)

		var buf bytes.Buffer

		err := d.Download(t.Context(), updates.Asset{Name: "app", URL: srv.URL}, &buf)
		if !errors.Is(err, ErrAssetTooLarge) {
			t.Fatalf("err = %v, want ErrAssetTooLarge", err)
		}
	})

	t.Run("exact limit succeeds", func(t *testing.T) {
		payload := strings.Repeat("x", int(limit))

		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, payload)
		}))
		t.Cleanup(srv.Close)

		d, err := NewDownloader(WithHTTPClient(srv.Client()), WithMaxAssetBytes(limit))
		if err != nil {
			t.Fatalf("NewDownloader: %v", err)
		}

		var buf bytes.Buffer

		err = d.Download(t.Context(), updates.Asset{Name: "app", URL: srv.URL}, &buf)
		if err != nil {
			t.Fatalf("Download: %v", err)
		}

		if int64(buf.Len()) != limit {
			t.Errorf("downloaded %d bytes, want %d", buf.Len(), limit)
		}
	})
}

func TestDownloadCallerClientNotMutated(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "payload")
	}))
	t.Cleanup(srv.Close)

	caller := srv.Client()
	caller.Timeout = 30 * time.Second
	transportBefore := caller.Transport

	d, err := NewDownloader(WithHTTPClient(caller))
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}

	var buf bytes.Buffer

	if err := d.Download(t.Context(), updates.Asset{Name: "app", URL: srv.URL}, &buf); err != nil {
		t.Fatalf("Download: %v", err)
	}

	if caller.Timeout != 30*time.Second {
		t.Errorf("caller Timeout changed to %v", caller.Timeout)
	}

	if caller.Transport != transportBefore {
		t.Error("caller Transport was replaced")
	}
}

// Regression: with WithMaxAssetBytes(math.MaxInt64), the former
// io.LimitReader(max+1) bound overflowed to a negative N and Download
// returned success having written zero bytes.
func TestDownloadMaxInt64BoundDoesNotTruncate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "payload")
	}))
	t.Cleanup(srv.Close)

	d, err := NewDownloader(WithMaxAssetBytes(math.MaxInt64), WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}

	var buf bytes.Buffer

	if err := d.Download(t.Context(), updates.Asset{Name: "app", URL: srv.URL}, &buf); err != nil {
		t.Fatalf("Download: %v", err)
	}

	if buf.String() != "payload" {
		t.Errorf("downloaded %q, want payload", buf.String())
	}
}

func TestDownloadContextCancellation(t *testing.T) {
	// Deterministic: cancellation happens only after the request reached
	// the handler, so the test never depends on wall-clock scheduling —
	// the former pinned 100ms deadline could expire before the request
	// was dispatched on a slow machine.
	started := make(chan struct{})

	release := make(chan struct{})

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)

		// Hold the response open until the test is done with the server.
		<-release

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	d, err := NewDownloader(WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var buf bytes.Buffer

	errCh := make(chan error, 1)

	go func() {
		errCh <- d.Download(ctx, updates.Asset{Name: "app", URL: srv.URL}, &buf)
	}()

	<-started
	cancel()

	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestSchemeGuardRejectsHTTPWithoutRequest(t *testing.T) {
	base := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Error("guard must reject before delegating to the base transport")

		return nil, nil
	})

	guard := &schemeGuardTransport{base: base}

	req, err := http.NewRequest(http.MethodGet, "http://example.test/app", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	_, err = guard.RoundTrip(req)
	if !errors.Is(err, ErrInsecureURL) {
		t.Fatalf("err = %v, want ErrInsecureURL", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestWithMaxAssetBytesValidation(t *testing.T) {
	for _, n := range []int64{0, -1} {
		_, err := NewDownloader(WithMaxAssetBytes(n))
		if err == nil {
			t.Errorf("WithMaxAssetBytes(%d) should be rejected", n)
		}
	}
}
