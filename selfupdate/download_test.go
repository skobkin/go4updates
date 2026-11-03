package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestDownloadContextCancellation(t *testing.T) {
	release := make(chan struct{})

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	var buf bytes.Buffer

	err = d.Download(ctx, updates.Asset{Name: "app", URL: srv.URL}, &buf)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
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
