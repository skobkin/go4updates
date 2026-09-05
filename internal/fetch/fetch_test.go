package fetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	updates "github.com/skobkin/go4updates"
)

func TestDoJSONDecodesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		_, _ = fmt.Fprint(w, `{"value":42}`)
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	var dst struct {
		Value int
	}

	if err := DoJSON(t.Context(), srv.Client(), req, &dst); err != nil {
		t.Fatalf("DoJSON returned error: %v", err)
	}

	if dst.Value != 42 {
		t.Errorf("decoded value = %d, want 42", dst.Value)
	}
}

func TestDoJSONWorksWithNilClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	var dst []int

	if err := DoJSON(t.Context(), nil, req, &dst); err != nil {
		t.Fatalf("DoJSON with nil client returned error: %v", err)
	}
}

func TestDoJSONNon2xxReturnsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)

		_, _ = fmt.Fprint(w, `{"message":"Not Found"}`)
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	var dst []int

	err = DoJSON(t.Context(), srv.Client(), req, &dst)

	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error = %v, want *StatusError", err)
	}

	if statusErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", statusErr.StatusCode)
	}

	if !strings.Contains(statusErr.Body, "Not Found") {
		t.Errorf("Body = %q, want response snippet", statusErr.Body)
	}

	if !strings.Contains(statusErr.Error(), srv.URL) {
		t.Errorf("error message %q does not mention the request URL", statusErr.Error())
	}
}

func TestDoJSONRedactsURLCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing server URL: %v", err)
	}

	target.User = url.UserPassword("deploy", "hunter2")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	var dst []int

	err = DoJSON(t.Context(), srv.Client(), req, &dst)

	statusErr, ok := err.(*StatusError)
	if !ok {
		t.Fatalf("error = %v, want *StatusError", err)
	}

	if !strings.Contains(statusErr.URL, "xxxxx") {
		t.Errorf("StatusError.URL = %q, want the redacted password placeholder", statusErr.URL)
	}

	if strings.Contains(statusErr.URL, "hunter2") || strings.Contains(statusErr.Error(), "hunter2") {
		t.Errorf("error %q leaks the URL password", statusErr.Error())
	}
}

func TestDoJSONMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `this is not json`)
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	var dst []int

	if err := DoJSON(t.Context(), srv.Client(), req, &dst); err == nil {
		t.Fatal("DoJSON returned nil error for malformed JSON")
	}
}

func TestDoJSONResponseTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, strings.Repeat("a", int(MaxResponseBytes)+10))
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	var dst []int

	err = DoJSON(t.Context(), srv.Client(), req, &dst)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
}

func TestDoJSONContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request must not reach the server after cancellation")
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	var dst []int

	if err := DoJSON(ctx, srv.Client(), req, &dst); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestDefaultClientIsIndependent(t *testing.T) {
	first, second := DefaultClient(), DefaultClient()
	if first == second {
		t.Error("DefaultClient returned the same instance twice; instances must not be shared globally")
	}

	if first.Timeout != 15*time.Second {
		t.Errorf("DefaultClient timeout = %v, want 15s", first.Timeout)
	}
}

func TestResolveLimit(t *testing.T) {
	tests := []struct {
		name    string
		limit   int
		want    int
		wantErr bool
	}{
		{"zero selects default", 0, updates.DefaultReleaseLimit, false},
		{"positive kept", 25, 25, false},
		{"one kept", 1, 1, false},
		{"negative rejected", -1, 0, true},
		{"large negative rejected", -100, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveLimit(tt.limit)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolveLimit(%d) = %d, nil; want error", tt.limit, got)
				}

				return
			}

			if err != nil {
				t.Fatalf("ResolveLimit(%d) returned error: %v", tt.limit, err)
			}

			if got != tt.want {
				t.Errorf("ResolveLimit(%d) = %d, want %d", tt.limit, got, tt.want)
			}
		})
	}
}

func TestSortNewestFirst(t *testing.T) {
	base := time.Now()

	releases := []updates.Release{
		{Version: "old", PublishedAt: base.Add(-3 * time.Hour)},
		{Version: "newest", PublishedAt: base},
		{Version: "mid", PublishedAt: base.Add(-1 * time.Hour)},
		{Version: "older", PublishedAt: base.Add(-2 * time.Hour)},
	}

	SortNewestFirst(releases)

	want := []string{"newest", "mid", "older", "old"}
	for i, version := range want {
		if releases[i].Version != version {
			t.Fatalf("release %d = %q, want %q (order: %v)", i, releases[i].Version, version, versions(releases))
		}
	}
}

func TestSortNewestFirstStableOnEqualTimestamps(t *testing.T) {
	ts := time.Now()

	releases := []updates.Release{
		{Version: "first", PublishedAt: ts},
		{Version: "second", PublishedAt: ts},
		{Version: "third", PublishedAt: ts},
	}

	SortNewestFirst(releases)

	want := []string{"first", "second", "third"}
	for i, version := range want {
		if releases[i].Version != version {
			t.Fatalf("equal timestamps must keep relative order: release %d = %q, want %q", i, releases[i].Version, version)
		}
	}
}
