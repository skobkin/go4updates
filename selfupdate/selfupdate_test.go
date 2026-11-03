package selfupdate

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	updates "github.com/skobkin/go4updates"
)

// updateFixture serves a release (zip payload + checksum) over TLS and
// points at a fake installed binary. Asset names use the running platform
// so the default matcher matches on any development machine.
type updateFixture struct {
	srv    *httptest.Server
	target string
	dir    string

	assetName string
}

const (
	fixtureAppName = "app"
	oldPayload     = "v1-binary"
	newPayload     = "v2-binary"
)

func fixtureAssetName() string {
	return fixtureAppName + "_2.0.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip"
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()

	dir := t.TempDir()

	// Build the release payload.
	var zipBuf bytes.Buffer

	zw := zip.NewWriter(&zipBuf)

	member, err := zw.Create(fixtureAppName)
	if err != nil {
		t.Fatalf("creating zip member: %v", err)
	}

	if _, err := io.WriteString(member, newPayload); err != nil {
		t.Fatalf("writing zip member: %v", err)
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}

	zipBytes := zipBuf.Bytes()

	sum := sha256.Sum256(zipBytes)
	digest := hex.EncodeToString(sum[:])

	assetName := fixtureAssetName()

	mux := http.NewServeMux()

	mux.HandleFunc("/"+assetName, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipBytes)
	})

	mux.HandleFunc("/"+assetName+".sha256", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, digest)
	})

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	target := filepath.Join(dir, fixtureAppName)

	if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
		t.Fatalf("writing fake target: %v", err)
	}

	return &updateFixture{srv: srv, target: target, dir: dir, assetName: assetName}
}

func (f *updateFixture) release() updates.Release {
	return updates.Release{
		Version: "v2.0.0",
		Assets: []updates.Asset{
			{Name: f.assetName, URL: f.srv.URL + "/" + f.assetName, Size: int64(len(newPayload))},
			{Name: f.assetName + ".sha256", URL: f.srv.URL + "/" + f.assetName + ".sha256"},
		},
	}
}

func (f *updateFixture) result() updates.Result {
	return updates.Result{
		CurrentVersion: "1.0.0",
		Latest:         func() *updates.Release { r := f.release(); return &r }(),
		Status:         updates.StatusUpdateAvailable,
	}
}

func assertNoStagingLeftovers(t *testing.T, dir, base string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), base+".go4updates.download") ||
			strings.HasPrefix(e.Name(), base+".go4updates.new") ||
			strings.HasPrefix(e.Name(), base+".go4updates.tmp") {
			t.Errorf("staging leftover %q survived", e.Name())
		}
	}
}

func TestApplyEndToEnd(t *testing.T) {
	f := newUpdateFixture(t)
	u, err := New(WithTargetPath(f.target), WithAppName(fixtureAppName), WithHTTPClient(f.srv.Client()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	applied, err := u.Apply(t.Context(), f.result())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if applied.Version != "v2.0.0" {
		t.Errorf("Version = %q, want v2.0.0", applied.Version)
	}

	if applied.Asset.Name != f.assetName {
		t.Errorf("Asset = %q, want the zip asset", applied.Asset.Name)
	}

	if applied.Path != f.target {
		t.Errorf("Path = %q, want %q", applied.Path, f.target)
	}

	wantBackup := f.target + BackupSuffix

	if applied.Backup != wantBackup {
		t.Errorf("Backup = %q, want %q", applied.Backup, wantBackup)
	}

	if got := readFileForTest(t, f.target); got != newPayload {
		t.Errorf("installed content = %q, want %q", got, newPayload)
	}

	if got := readFileForTest(t, wantBackup); got != oldPayload {
		t.Errorf("backup content = %q, want %q", got, oldPayload)
	}

	assertNoStagingLeftovers(t, f.dir, fixtureAppName)
}

func TestApplyStatusGuards(t *testing.T) {
	f := newUpdateFixture(t)
	u, _ := New(WithTargetPath(f.target), WithAppName(fixtureAppName), WithHTTPClient(f.srv.Client()))

	t.Run("no release", func(t *testing.T) {
		_, err := u.Apply(t.Context(), updates.Result{Status: updates.StatusUpdateAvailable})
		if !errors.Is(err, ErrNoRelease) {
			t.Fatalf("err = %v, want ErrNoRelease", err)
		}
	})

	t.Run("not update available", func(t *testing.T) {
		result := f.result()
		result.Status = updates.StatusUpToDate

		_, err := u.Apply(t.Context(), result)
		if !errors.Is(err, ErrNotUpdateAvailable) {
			t.Fatalf("err = %v, want ErrNotUpdateAvailable", err)
		}
	})

	if got := readFileForTest(t, f.target); got != oldPayload {
		t.Errorf("target was modified by guarded calls: %q", got)
	}
}

func TestApplyForceBypassesOnlyTheStatusGuard(t *testing.T) {
	f := newUpdateFixture(t)

	u, err := New(
		WithTargetPath(f.target),
		WithAppName(fixtureAppName),
		WithHTTPClient(f.srv.Client()),
		WithForce(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result := f.result()
	result.Status = updates.StatusUnknown // e.g. non-semver current version

	if _, err := u.Apply(t.Context(), result); err != nil {
		t.Fatalf("Apply with force: %v", err)
	}

	if got := readFileForTest(t, f.target); got != newPayload {
		t.Errorf("installed content = %q, want %q", got, newPayload)
	}
}

func TestApplyForceStillVerifies(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, fixtureAppName)

	if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = fmt.Fprint(w, "0000000000000000000000000000000000000000000000000000000000000000")

			return
		}

		_, _ = fmt.Fprint(w, newPayload)
	}))
	t.Cleanup(srv.Close)

	// A raw (non-archive) asset keeps the flow simple; the published
	// checksum belongs to different bytes, so verification must fail.
	assetName := fixtureAppName + "_2.0.0_" + runtime.GOOS + "_" + runtime.GOARCH

	release := updates.Release{
		Version: "v2.0.0",
		Assets: []updates.Asset{
			{Name: assetName, URL: srv.URL + "/payload"},
			{Name: assetName + ".sha256", URL: srv.URL + "/bad.sha256"},
		},
	}

	u, err := New(
		WithTargetPath(target),
		WithAppName(fixtureAppName),
		WithHTTPClient(srv.Client()),
		WithForce(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = u.Apply(t.Context(), updates.Result{Latest: &release, Status: updates.StatusUnknown})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch even with WithForce", err)
	}

	if got := readFileForTest(t, target); got != oldPayload {
		t.Errorf("target was modified by a failed verification: %q", got)
	}

	assertNoStagingLeftovers(t, dir, fixtureAppName)
}

func TestApplyInsecureSkipVerifySkipsOnlyVerification(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, fixtureAppName)

	if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	// The release publishes no checksum at all; a raw binary payload.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, newPayload)
	}))
	t.Cleanup(srv.Close)

	assetName := fixtureAppName + "_2.0.0_" + runtime.GOOS + "_" + runtime.GOARCH

	release := updates.Release{
		Version: "v2.0.0",
		Assets:  []updates.Asset{{Name: assetName, URL: srv.URL + "/payload"}},
	}

	u, err := New(
		WithTargetPath(target),
		WithAppName(fixtureAppName),
		WithHTTPClient(srv.Client()),
		WithInsecureSkipVerify(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	applied, err := u.Apply(t.Context(), updates.Result{Latest: &release, Status: updates.StatusUpdateAvailable})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if applied.Backup == "" {
		t.Error("backup should still be created without verification")
	}

	if got := readFileForTest(t, target); got != newPayload {
		t.Errorf("installed content = %q, want %q", got, newPayload)
	}
}

func TestApplyInsecureSkipVerifyConflictsWithCustomVerifier(t *testing.T) {
	_, err := New(WithInsecureSkipVerify(), WithVerifier(nil))
	if err == nil {
		t.Error("nil verifier should be rejected")
	}

	_, err = New(
		WithInsecureSkipVerify(),
		WithVerifier(VerifierFunc(func(context.Context, updates.Release, updates.Asset, io.Reader) error {
			return nil
		})),
	)
	if err == nil {
		t.Error("WithInsecureSkipVerify and WithVerifier should be mutually exclusive")
	}
}

// VerifierFunc adapts a function to the Verifier interface (test helper).
type VerifierFunc func(ctx context.Context, release updates.Release, asset updates.Asset, data io.Reader) error

func (f VerifierFunc) Verify(ctx context.Context, release updates.Release, asset updates.Asset, data io.Reader) error {
	return f(ctx, release, asset, data)
}

func TestApplyCustomVerifier(t *testing.T) {
	f := newUpdateFixture(t)

	called := false

	custom := VerifierFunc(func(_ context.Context, release updates.Release, asset updates.Asset, data io.Reader) error {
		called = true

		if release.Version != "v2.0.0" {
			t.Errorf("verifier release version = %q", release.Version)
		}

		if asset.Name != f.assetName {
			t.Errorf("verifier asset = %q", asset.Name)
		}

		got, err := io.ReadAll(data)
		if err != nil {
			return err
		}

		// The asset is a compressed zip: the verifier sees the downloaded
		// archive bytes, not the extracted payload.
		if len(got) == 0 {
			t.Error("verifier received empty data")
		}

		return errors.New("signature invalid")
	})

	u, err := New(
		WithTargetPath(f.target),
		WithAppName(fixtureAppName),
		WithHTTPClient(f.srv.Client()),
		WithVerifier(custom),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = u.Apply(t.Context(), f.result())
	if err == nil || !strings.Contains(err.Error(), "signature invalid") {
		t.Fatalf("err = %v, want the custom verifier error", err)
	}

	if !called {
		t.Error("custom verifier was not called")
	}

	if got := readFileForTest(t, f.target); got != oldPayload {
		t.Errorf("target was modified by a failed custom verification: %q", got)
	}

	assertNoStagingLeftovers(t, f.dir, fixtureAppName)
}

func TestApplyFailureLeavesNoStagingLeftovers(t *testing.T) {
	t.Run("download status error", func(t *testing.T) {
		dir := t.TempDir()

		target := filepath.Join(dir, fixtureAppName)

		if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
			t.Fatalf("writing target: %v", err)
		}

		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)

			_, _ = fmt.Fprint(w, "boom")
		}))
		t.Cleanup(srv.Close)

		assetName := fixtureAppName + "_2.0.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip"

		release := updates.Release{
			Version: "v2.0.0",
			Assets:  []updates.Asset{{Name: assetName, URL: srv.URL + "/broken"}},
		}

		u, err := New(
			WithTargetPath(target),
			WithAppName(fixtureAppName),
			WithHTTPClient(srv.Client()),
			WithInsecureSkipVerify(),
		)
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		var statusErr *StatusError

		_, err = u.Apply(t.Context(), updates.Result{Latest: &release, Status: updates.StatusUpdateAvailable})
		if !errors.As(err, &statusErr) {
			t.Fatalf("err = %v (%T), want *StatusError", err, err)
		}

		if got := readFileForTest(t, target); got != oldPayload {
			t.Errorf("target was modified: %q", got)
		}

		assertNoStagingLeftovers(t, dir, fixtureAppName)
	})

	t.Run("extraction failure", func(t *testing.T) {
		dir := t.TempDir()

		target := filepath.Join(dir, fixtureAppName)

		if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
			t.Fatalf("writing target: %v", err)
		}

		// Content that is not a zip, under a .zip asset name; the checksum
		// matches so the flow reaches extraction.
		payload := "this is not a zip file"

		sum := sha256.Sum256([]byte(payload))

		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, ".sha256") {
				_, _ = fmt.Fprint(w, hex.EncodeToString(sum[:]))

				return
			}

			_, _ = fmt.Fprint(w, payload)
		}))
		t.Cleanup(srv.Close)

		assetName := fixtureAppName + "_2.0.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip"

		release := updates.Release{
			Version: "v2.0.0",
			Assets: []updates.Asset{
				{Name: assetName, URL: srv.URL + "/payload.zip"},
				{Name: assetName + ".sha256", URL: srv.URL + "/payload.zip.sha256"},
			},
		}

		u, err := New(
			WithTargetPath(target),
			WithAppName(fixtureAppName),
			WithHTTPClient(srv.Client()),
		)
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		_, err = u.Apply(t.Context(), updates.Result{Latest: &release, Status: updates.StatusUpdateAvailable})
		if err == nil {
			t.Fatal("Apply should fail on garbage archive content")
		}

		if got := readFileForTest(t, target); got != oldPayload {
			t.Errorf("target was modified: %q", got)
		}

		assertNoStagingLeftovers(t, dir, fixtureAppName)
	})
}

func TestApplyAssetSelectionErrors(t *testing.T) {
	f := newUpdateFixture(t)

	u, _ := New(WithTargetPath(f.target), WithAppName(fixtureAppName), WithHTTPClient(f.srv.Client()))

	t.Run("no matching asset", func(t *testing.T) {
		release := updates.Release{
			Version: "v2.0.0",
			Assets:  []updates.Asset{{Name: "totally_other_1.0_linux_amd64.tar.gz"}},
		}

		_, err := u.Apply(t.Context(), updates.Result{Latest: &release, Status: updates.StatusUpdateAvailable})
		if !errors.Is(err, ErrNoMatchingAsset) {
			t.Fatalf("err = %v, want ErrNoMatchingAsset", err)
		}
	})

	t.Run("ambiguous assets", func(t *testing.T) {
		release := updates.Release{
			Version: "v2.0.0",
			Assets: []updates.Asset{
				{Name: f.assetName},
				{Name: fixtureAppName + "-2.0.0-linux-amd64.zip"},
			},
		}

		_, err := u.Apply(t.Context(), updates.Result{Latest: &release, Status: updates.StatusUpdateAvailable})
		if !errors.Is(err, ErrAmbiguousAsset) {
			t.Fatalf("err = %v, want ErrAmbiguousAsset", err)
		}
	})
}

func TestUpdaterCleanupRetiresBackup(t *testing.T) {
	f := newUpdateFixture(t)

	u, err := New(WithTargetPath(f.target), WithAppName(fixtureAppName), WithHTTPClient(f.srv.Client()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := u.Apply(t.Context(), f.result()); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := readFileForTest(t, f.target+BackupSuffix); got != oldPayload {
		t.Fatalf("backup content = %q, want %q", got, oldPayload)
	}

	if err := u.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if _, statErr := os.Lstat(f.target + BackupSuffix); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("backup stat = %v, want removed", statErr)
	}

	if got := readFileForTest(t, f.target); got != newPayload {
		t.Errorf("installed content = %q, want %q", got, newPayload)
	}
}

func TestSelectAssetPreview(t *testing.T) {
	f := newUpdateFixture(t)

	u, _ := New(WithTargetPath(f.target), WithAppName(fixtureAppName), WithHTTPClient(f.srv.Client()))

	asset, err := u.SelectAsset(f.release())
	if err != nil {
		t.Fatalf("SelectAsset: %v", err)
	}

	if asset.Name != f.assetName {
		t.Errorf("selected %q, want the zip asset", asset.Name)
	}
}
