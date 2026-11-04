package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	updates "github.com/skobkin/go4updates"
)

// verifyFixture wires a TLS server serving checksum assets with a release
// describing them, plus the plaintext payload of the tested asset.
type verifyFixture struct {
	srv     *httptest.Server
	release updates.Release
	payload string
}

func newVerifyFixture(t *testing.T, files map[string]string, assets []updates.Asset) *verifyFixture {
	t.Helper()

	mux := http.NewServeMux()

	for path, content := range files {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != path {
				http.NotFound(w, r)

				return
			}

			_, _ = fmt.Fprint(w, content)
		})
	}

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	for i := range assets {
		if assets[i].URL == "" {
			assets[i].URL = srv.URL + "/" + assets[i].Name
		}
	}

	return &verifyFixture{
		srv:     srv,
		release: updates.Release{Version: "v1.0.0", Assets: assets},
		payload: "binary payload",
	}
}

func (f *verifyFixture) asset() updates.Asset {
	for _, a := range f.release.Assets {
		if a.Name == assetName {
			return a
		}
	}

	return updates.Asset{}
}

const assetName = "app_1.0.0_linux_amd64.tar.gz"

func newVerifier(t *testing.T, f *verifyFixture) *ChecksumVerifier {
	t.Helper()

	v, err := NewChecksumVerifier(WithHTTPClient(f.srv.Client()))
	if err != nil {
		t.Fatalf("NewChecksumVerifier: %v", err)
	}

	return v
}

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))

	return hex.EncodeToString(sum[:])
}

func TestChecksumVerifierSources(t *testing.T) {
	digest := digestOf("binary payload")

	tests := []struct {
		name    string
		files   map[string]string
		assets  []string
		wantErr error
	}{
		{
			name:   "sha256 sibling",
			files:  map[string]string{"/" + assetName + ".sha256": digest},
			assets: []string{assetName, assetName + ".sha256"},
		},
		{
			name:   "sha256sum sibling",
			files:  map[string]string{"/" + assetName + ".sha256sum": digest},
			assets: []string{assetName, assetName + ".sha256sum"},
		},
		{
			name:   "sibling with filename",
			files:  map[string]string{"/" + assetName + ".sha256": digest + "  " + assetName},
			assets: []string{assetName, assetName + ".sha256"},
		},
		{
			name:   "sibling with binary-mode filename",
			files:  map[string]string{"/" + assetName + ".sha256": digest + " *" + assetName},
			assets: []string{assetName, assetName + ".sha256"},
		},
		{
			name: "checksums.txt manifest",
			files: map[string]string{"/checksums.txt": strings.Join([]string{
				digestOf("other-file") + "  other_1.0.0_linux_amd64.tar.gz",
				digest + "  " + assetName,
				digestOf("third-file") + "  third.zip",
				"",
			}, "\n")},
			assets: []string{assetName, "checksums.txt"},
		},
		{
			name:   "SHA256SUMS uppercase manifest",
			files:  map[string]string{"/SHA256SUMS": digest + "  " + assetName},
			assets: []string{assetName, "SHA256SUMS"},
		},
		{
			name:   "manifest with CRLF endings",
			files:  map[string]string{"/checksums.txt": digest + "  " + assetName + "\r\n"},
			assets: []string{assetName, "checksums.txt"},
		},
		{
			name:   "manifest with a single bare digest",
			files:  map[string]string{"/checksums.txt": digest + "\n"},
			assets: []string{assetName, "checksums.txt"},
		},
		{
			name:   "manifest with comments and blanks",
			files:  map[string]string{"/checksums.txt": "# sha256\n\n" + digest + "  " + assetName + "\n\n"},
			assets: []string{assetName, "checksums.txt"},
		},
		{
			name:    "uppercase digest is accepted",
			files:   map[string]string{"/" + assetName + ".sha256": strings.ToUpper(digest)},
			assets:  []string{assetName, assetName + ".sha256"},
			wantErr: nil,
		},
		{
			name:    "checksum mismatch",
			files:   map[string]string{"/" + assetName + ".sha256": digestOf("other payload")},
			assets:  []string{assetName, assetName + ".sha256"},
			wantErr: ErrChecksumMismatch,
		},
		{
			name:    "no checksum asset at all",
			files:   map[string]string{},
			assets:  []string{assetName, "app_1.0.0_linux_arm64.tar.gz"},
			wantErr: ErrNoChecksum,
		},
		{
			name:    "manifest does not list the asset",
			files:   map[string]string{"/checksums.txt": digestOf("x") + "  other.zip\n"},
			assets:  []string{assetName, "checksums.txt"},
			wantErr: ErrNoChecksum,
		},
		{
			name:  "signature sidecars of the manifest are not manifests",
			files: map[string]string{"/checksums.txt": digest + "  " + assetName},
			assets: []string{
				assetName,
				"checksums.txt",
				"checksums.txt.sig",
				"checksums.txt.asc",
				"checksums.txt.minisig",
				"checksums.txt.gpg",
				"checksums.txt.bundle",
				"checksums.txt.sigstore.json",
			},
		},
		{
			name:    "several manifests are ambiguous",
			files:   map[string]string{"/checksums.txt": digest + "  " + assetName, "/SHA256SUMS": digest + "  " + assetName},
			assets:  []string{assetName, "checksums.txt", "SHA256SUMS"},
			wantErr: ErrNoChecksum,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assets := make([]updates.Asset, 0, len(tt.assets))
			for _, a := range tt.assets {
				assets = append(assets, updates.Asset{Name: a})
			}

			f := newVerifyFixture(t, tt.files, assets)
			v := newVerifier(t, f)

			err := v.Verify(t.Context(), f.release, f.asset(), bytes.NewReader([]byte(f.payload)))

			switch {
			case tt.wantErr == nil && err != nil:
				t.Fatalf("Verify: unexpected error: %v", err)

			case tt.wantErr != nil && !errors.Is(err, tt.wantErr):
				t.Fatalf("Verify: err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestChecksumVerifierMalformedDigestFile(t *testing.T) {
	for _, bad := range []string{"abc123", digestOf("x") + "zz", "not hex at all\n"} {
		f := newVerifyFixture(t,
			map[string]string{"/" + assetName + ".sha256": bad},
			[]updates.Asset{{Name: assetName}, {Name: assetName + ".sha256"}})

		v := newVerifier(t, f)

		err := v.Verify(t.Context(), f.release, f.asset(), bytes.NewReader([]byte(f.payload)))
		if err == nil {
			t.Fatalf("Verify(%q): expected a malformed-checksum error", bad)
		}

		if errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("Verify(%q): malformed content must not surface as a mismatch", bad)
		}
	}
}

func TestChecksumVerifierStatusError(t *testing.T) {
	f := newVerifyFixture(t,
		map[string]string{},
		[]updates.Asset{{Name: assetName}, {Name: assetName + ".sha256"}})

	// The checksum URL 404s: the fixture registered no handler for it.
	v := newVerifier(t, f)

	err := v.Verify(t.Context(), f.release, f.asset(), bytes.NewReader([]byte(f.payload)))

	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %v (%T), want a wrapped *StatusError", err, err)
	}

	if statusErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", statusErr.StatusCode)
	}
}

func TestChecksumVerifierRejectsInsecureChecksumURL(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, digestOf("binary payload"))
	}))
	t.Cleanup(srv.Close)

	release := updates.Release{Version: "v1.0.0", Assets: []updates.Asset{
		{Name: assetName, URL: "https://example.test/" + assetName},
		{Name: assetName + ".sha256", URL: "http://example.test/" + assetName + ".sha256"},
	}}

	v, err := NewChecksumVerifier(WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("NewChecksumVerifier: %v", err)
	}

	err = v.Verify(t.Context(), release, release.Assets[0], bytes.NewReader([]byte("binary payload")))
	if !errors.Is(err, ErrInsecureURL) {
		t.Fatalf("err = %v, want ErrInsecureURL", err)
	}
}

func TestChecksumVerifierBound(t *testing.T) {
	oversized := strings.Repeat("a", int(maxChecksumBytes)+100)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, oversized)
	}))
	t.Cleanup(srv.Close)

	release := updates.Release{Version: "v1.0.0", Assets: []updates.Asset{
		{Name: assetName, URL: srv.URL + "/" + assetName},
		{Name: assetName + ".sha256", URL: srv.URL + "/" + assetName + ".sha256"},
	}}

	v, err := NewChecksumVerifier(WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("NewChecksumVerifier: %v", err)
	}

	err = v.Verify(t.Context(), release, release.Assets[0], bytes.NewReader([]byte("binary payload")))
	if !errors.Is(err, ErrAssetTooLarge) {
		t.Fatalf("err = %v, want ErrAssetTooLarge for the oversized checksum file", err)
	}
}

func TestChecksumVerifierIgnoresMaxAssetBytes(t *testing.T) {
	// WithMaxAssetBytes must not loosen the checksum bound.
	v, err := NewChecksumVerifier(WithMaxAssetBytes(1 << 30))
	if err != nil {
		t.Fatalf("NewChecksumVerifier: %v", err)
	}

	if v.downloader.maxBytes != maxChecksumBytes {
		t.Errorf("checksum bound = %d, want %d", v.downloader.maxBytes, maxChecksumBytes)
	}
}

// Regression: the file name recorded inside a checksum file or manifest was
// compared case-sensitively against the asset name, so a publisher writing a
// differently-cased file name into the checksum failed verification although
// the digest matched.
func TestChecksumFileNameCaseInsensitive(t *testing.T) {
	digest := digestOf("binary payload")

	t.Run("sibling digest file", func(t *testing.T) {
		f := newVerifyFixture(t, map[string]string{
			"/" + assetName + ".sha256": digest + "  APP_1.0.0_LINUX_AMD64.tar.gz\n",
		}, []updates.Asset{{Name: assetName}, {Name: assetName + ".sha256"}})

		err := newVerifier(t, f).Verify(t.Context(), f.release, f.asset(), strings.NewReader("binary payload"))
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
	})

	t.Run("manifest line", func(t *testing.T) {
		f := newVerifyFixture(t, map[string]string{
			"/checksums.txt": digestOf("other payload") + "  other_1.0.0_linux_amd64.tar.gz\n" +
				strings.ToUpper(digest) + "  App_1.0.0_LINUX_amd64.tar.gz\n",
		}, []updates.Asset{{Name: assetName}, {Name: "checksums.txt"}})

		err := newVerifier(t, f).Verify(t.Context(), f.release, f.asset(), strings.NewReader("binary payload"))
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
	})
}

// Regression: when both sibling names were published, the resolution
// returned whichever came first in the release asset list; the documented
// precedence is "<asset>.sha256" first, "<asset>.sha256sum" second.
func TestFindChecksumSourceSiblingPrecedence(t *testing.T) {
	asset := updates.Asset{Name: assetName}

	release := updates.Release{Assets: []updates.Asset{
		{Name: assetName + ".sha256sum"},
		{Name: assetName + ".sha256"},
	}}

	source, err := findChecksumSource(release, asset)
	if err != nil {
		t.Fatalf("findChecksumSource: %v", err)
	}

	if source.manifest {
		t.Error("the sibling was resolved as a manifest")
	}

	if want := assetName + ".sha256"; source.asset.Name != want {
		t.Errorf("selected %q, want %q per the documented precedence", source.asset.Name, want)
	}
}

// Hashing must observe cancellation between chunks instead of running a
// large asset to completion.
func TestHexDigestCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cancel()

	payload := strings.Repeat("a", 1<<20) // larger than the hashing chunk

	_, err := hexDigest(ctx, strings.NewReader(payload))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// A canceled context mid-hash surfaces the same way.
func TestHexDigestContextCancelMidStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &cancelingReader{r: strings.NewReader(strings.Repeat("a", 64<<10)), cancel: cancel}

	digest, err := hexDigest(ctx, src)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	if digest != "" {
		t.Fatalf("digest = %q on failure, want empty", digest)
	}
}
