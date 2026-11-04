package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	updates "github.com/skobkin/go4updates"
)

// progressRecorder collects events from an ApplyWithProgress callback.
type progressRecorder struct {
	events []Progress
}

func (r *progressRecorder) record(p Progress) {
	r.events = append(r.events, p)
}

func (r *progressRecorder) stages() []Stage {
	stages := make([]Stage, 0, len(r.events))

	for _, p := range r.events {
		stages = append(stages, p.Stage)
	}

	return stages
}

func TestApplyWithProgressEndToEnd(t *testing.T) {
	f := newUpdateFixture(t)

	u, err := New(WithTargetPath(f.target), WithAppName(fixtureAppName), WithHTTPClient(f.srv.Client()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Interval zero disables throttling: every changed byte observation is
	// reported, which makes the byte-count assertions exact.
	u.progressInterval = 0

	var rec progressRecorder

	applied, err := u.ApplyWithProgress(t.Context(), f.result(), rec.record)
	if err != nil {
		t.Fatalf("ApplyWithProgress: %v", err)
	}

	if applied.Version != "v2.0.0" || applied.Path != f.target {
		t.Fatalf("applied = %+v, want the fixture update", applied)
	}

	if len(rec.events) == 0 {
		t.Fatal("no progress events were reported")
	}

	for _, p := range rec.events {
		if p.AssetName != f.assetName {
			t.Errorf("AssetName = %q, want %q", p.AssetName, f.assetName)
		}
	}

	// Stage transitions: strict pipeline order, never backwards.
	order := map[Stage]int{
		StageDownloading: 0,
		StageVerifying:   1,
		StageExtracting:  2,
		StageInstalling:  3,
		StageCompleted:   4,
	}

	highest := -1

	for _, p := range rec.events {
		i := order[p.Stage]

		if i < highest {
			t.Fatalf("stage %s reported after a later stage", p.Stage)
		}

		if i > highest {
			highest = i
		}
	}

	for stage := range order {
		if highest < order[stage] {
			t.Errorf("stage %s was never reported", stage)
		}
	}

	// Byte events, per stage. The first event of each stage is its
	// transition and is asserted to carry zeroed byte counts separately.
	var (
		downloadBytes, verifyBytes, extractBytes []Progress

		seenDownloading, seenVerifying, seenExtracting bool
	)

	for _, p := range rec.events {
		switch p.Stage {
		case StageDownloading:
			if !seenDownloading {
				seenDownloading = true

				if p.Current != 0 || p.Total != 0 {
					t.Errorf("downloading transition = (%d, %d), want zeroes", p.Current, p.Total)
				}

				continue
			}

			if p.Total != f.zipSize {
				t.Errorf("downloading event Current=%d Total=%d, want Total %d", p.Current, p.Total, f.zipSize)
			}

			downloadBytes = append(downloadBytes, p)

		case StageVerifying:
			if !seenVerifying {
				seenVerifying = true

				if p.Current != 0 || p.Total != 0 {
					t.Errorf("verifying transition = (%d, %d), want zeroes", p.Current, p.Total)
				}

				continue
			}

			verifyBytes = append(verifyBytes, p)

		case StageExtracting:
			if !seenExtracting {
				seenExtracting = true

				if p.Current != 0 || p.Total != 0 {
					t.Errorf("extracting transition = (%d, %d), want zeroes", p.Current, p.Total)
				}

				continue
			}

			extractBytes = append(extractBytes, p)
		}
	}

	if len(downloadBytes) < 2 {
		t.Fatalf("downloading events = %d, want at least the start and the final", len(downloadBytes))
	}

	if downloadBytes[0].Current != 0 {
		t.Errorf("first downloading event Current = %d, want 0", downloadBytes[0].Current)
	}

	for i, p := range downloadBytes {
		if i > 0 && p.Current < downloadBytes[i-1].Current {
			t.Errorf("downloading Current went backwards: %d after %d", p.Current, downloadBytes[i-1].Current)
		}
	}

	if final := downloadBytes[len(downloadBytes)-1]; final.Current != f.zipSize {
		t.Errorf("final downloading event Current = %d, want exact %d", final.Current, f.zipSize)
	}

	// Verification is the built-in checksum verifier: determinate, and the
	// hashed input is the whole downloaded file.
	if len(verifyBytes) == 0 {
		t.Fatal("no verifying events were reported")
	}

	if last := verifyBytes[len(verifyBytes)-1]; last.Current != f.zipSize || last.Total != f.zipSize {
		t.Errorf("final verifying event = (%d, %d), want exact (%d, %d)", last.Current, last.Total, f.zipSize, f.zipSize)
	}

	// Extraction streams the zip member: its declared size is the payload.
	if len(extractBytes) == 0 {
		t.Fatal("no extracting events were reported")
	}

	if last := extractBytes[len(extractBytes)-1]; last.Current != int64(len(newPayload)) || last.Total != int64(len(newPayload)) {
		t.Errorf("final extracting event = (%d, %d), want exact (%d, %d)",
			last.Current, last.Total, len(newPayload), len(newPayload))
	}

	// Installing and Completed carry zeroed byte counts, once each.
	var installing, completed []Progress

	for _, p := range rec.events {
		switch p.Stage {
		case StageInstalling:
			installing = append(installing, p)

		case StageCompleted:
			completed = append(completed, p)
		}
	}

	if len(installing) != 2 {
		t.Errorf("installing events = %d, want transition + final", len(installing))
	}

	if len(completed) != 1 {
		t.Fatalf("completed events = %d, want exactly one", len(completed))
	}

	for _, p := range append(installing, completed...) {
		if p.Current != 0 || p.Total != 0 {
			t.Errorf("event %s carries bytes (%d, %d), want zeroes", p.Stage, p.Current, p.Total)
		}
	}

	if got := readFileForTest(t, f.target); got != newPayload {
		t.Errorf("installed content = %q, want %q", got, newPayload)
	}

	assertNoStagingLeftovers(t, f.dir, fixtureAppName)
}

// A server that does not declare Content-Length must yield indeterminate
// download events: Total of zero everywhere, but the exact final count.
func TestApplyWithProgressUnknownDownloadTotal(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, fixtureAppName)

	if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	// Flush between chunks, so the response is transferred with chunked
	// encoding and no Content-Length.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher := w.(http.Flusher)

		_, _ = w.Write([]byte("v2"))

		flusher.Flush()

		_, _ = w.Write([]byte("-binary"))

		flusher.Flush()
	}))
	t.Cleanup(srv.Close)

	assetName := fixtureAppName + "_2.0.0_" + runtime.GOOS + "_" + runtime.GOARCH

	release := updates.Release{
		Version: "v2.0.0",
		Assets:  []updates.Asset{{Name: assetName, URL: srv.URL + "/asset"}},
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

	u.progressInterval = 0

	var rec progressRecorder

	result := updates.Result{
		CurrentVersion: "1.0.0",
		Latest:         &release,
		Status:         updates.StatusUpdateAvailable,
	}

	if _, err := u.ApplyWithProgress(t.Context(), result, rec.record); err != nil {
		t.Fatalf("ApplyWithProgress: %v", err)
	}

	var lastDownload Progress

	for _, p := range rec.events {
		if p.Stage != StageDownloading {
			continue
		}

		if p.Total != 0 {
			t.Errorf("downloading event Total = %d with no Content-Length, want 0", p.Total)
		}

		lastDownload = p
	}

	if lastDownload.Current != int64(len(newPayload)) {
		t.Errorf("final downloading Current = %d, want exact %d", lastDownload.Current, len(newPayload))
	}

	assertNoStagingLeftovers(t, dir, fixtureAppName)
}

// A nil callback must behave exactly like Apply.
func TestApplyWithProgressNilCallbackMatchesApply(t *testing.T) {
	f := newUpdateFixture(t)

	u, err := New(WithTargetPath(f.target), WithAppName(fixtureAppName), WithHTTPClient(f.srv.Client()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := u.ApplyWithProgress(t.Context(), f.result(), nil); err != nil {
		t.Fatalf("ApplyWithProgress(nil): %v", err)
	}

	if got := readFileForTest(t, f.target); got != newPayload {
		t.Errorf("installed content = %q, want %q", got, newPayload)
	}
}

// Throttling: a huge interval suppresses every intermediate byte event,
// but the first observation (which carries the Total) and the exact final
// event are always delivered.
func TestApplyWithProgressThrottleSuppressesIntermediates(t *testing.T) {
	f := newUpdateFixture(t)

	u, err := New(WithTargetPath(f.target), WithAppName(fixtureAppName), WithHTTPClient(f.srv.Client()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	u.progressInterval = time.Hour

	var rec progressRecorder

	if _, err := u.ApplyWithProgress(t.Context(), f.result(), rec.record); err != nil {
		t.Fatalf("ApplyWithProgress: %v", err)
	}

	var byteEvents []Progress

	for _, p := range rec.events {
		switch p.Stage {
		case StageDownloading, StageVerifying, StageExtracting:
			if p.Current != 0 || p.Total != 0 {
				byteEvents = append(byteEvents, p)
			}
		}
	}

	// Per byte stage: at most the first observation (which may carry the
	// total with Current still zero) and the final event.
	if len(byteEvents) > 6 {
		t.Errorf("byte events = %d with a one-hour throttle, want at most first+final per stage", len(byteEvents))
	}

	for _, p := range byteEvents {
		if p.Stage == StageDownloading && p.Current != 0 && p.Current != f.zipSize {
			t.Errorf("downloading event Current = %d, want only 0 or the exact final %d", p.Current, f.zipSize)
		}
	}
}

// The throttling logic itself, driven deterministically by an injected
// step clock: every observe call advances time by one step.
func TestProgressEmitterThrottleTiming(t *testing.T) {
	var (
		events []Progress
		now    time.Time
	)

	emit := newProgressEmitter(func(p Progress) { events = append(events, p) }, "asset.zip", 2500*time.Millisecond, func() time.Time {
		now = now.Add(1000 * time.Millisecond)

		return now
	})

	emit.transition(StageDownloading)

	// Observations at t=1s..5s (the clock advances 1s per observe): the
	// first emits immediately at t=1s; 2s and 3s are inside the 2.5s
	// window; 4s emits; 5s is again inside the window from 4s.
	for _, n := range []int64{100, 200, 300, 400, 500} {
		emit.observe(n, 500)
	}

	emit.finalize()

	var got []Progress

	for _, p := range events {
		if p.Current != 0 || p.Total != 0 {
			got = append(got, p)
		}
	}

	want := []Progress{
		{Stage: StageDownloading, AssetName: "asset.zip", Current: 100, Total: 500},
		{Stage: StageDownloading, AssetName: "asset.zip", Current: 400, Total: 500},
		{Stage: StageDownloading, AssetName: "asset.zip", Current: 500, Total: 500},
	}

	if len(got) != len(want) {
		t.Fatalf("emitted byte events = %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Unchanged byte counts must not produce intermediate events, with or
// without an elapsed interval.
func TestProgressEmitterNoEventsForUnchangedCounts(t *testing.T) {
	var count int

	emit := newProgressEmitter(func(Progress) { count++ }, "a", 0, func() time.Time { return time.Time{} })

	emit.transition(StageDownloading)

	emit.observe(0, 100) // first observation: emitted

	before := count

	emit.observe(0, 100) // unchanged: suppressed

	if count != before {
		t.Fatalf("unchanged count emitted an event")
	}

	emit.observe(50, 100)

	if count != before+1 {
		t.Fatalf("changed count was not emitted")
	}
}

// A verifier that consumed an unknown amount of work must stay
// indeterminate: its final event reports measured bytes with Total zero.
func TestApplyWithProgressCustomVerifierIndeterminate(t *testing.T) {
	f := newUpdateFixture(t)

	// The verifier reads half of the input and returns success.
	verifier := VerifierFunc(func(_ context.Context, _ updates.Release, _ updates.Asset, data io.Reader) error {
		_, err := io.CopyN(io.Discard, data, f.zipSize/2)

		return err
	})

	u, err := New(WithTargetPath(f.target), WithAppName(fixtureAppName), WithHTTPClient(f.srv.Client()), WithVerifier(verifier))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	u.progressInterval = 0

	var rec progressRecorder

	if _, err := u.ApplyWithProgress(t.Context(), f.result(), rec.record); err != nil {
		t.Fatalf("ApplyWithProgress: %v", err)
	}

	var verifying []Progress

	for _, p := range rec.events {
		if p.Stage == StageVerifying {
			verifying = append(verifying, p)

			if p.Total != 0 {
				t.Errorf("custom verifier event Total = %d, want 0 (unknown work)", p.Total)
			}
		}
	}

	if len(verifying) == 0 {
		t.Fatal("no verifying events for a custom verifier")
	}

	if last := verifying[len(verifying)-1]; last.Current != f.zipSize/2 {
		t.Errorf("final verifying Current = %d, want the measured %d", last.Current, f.zipSize/2)
	}
}

// A failing custom verifier aborts the pipeline with no extraction events.
func TestApplyWithProgressFailingVerifier(t *testing.T) {
	f := newUpdateFixture(t)

	verifier := VerifierFunc(func(_ context.Context, _ updates.Release, _ updates.Asset, _ io.Reader) error {
		return errors.New("rejected by policy")
	})

	u, err := New(WithTargetPath(f.target), WithAppName(fixtureAppName), WithHTTPClient(f.srv.Client()), WithVerifier(verifier))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var rec progressRecorder

	_, err = u.ApplyWithProgress(t.Context(), f.result(), rec.record)
	if err == nil || !strings.Contains(err.Error(), "rejected by policy") {
		t.Fatalf("err = %v, want the verifier failure", err)
	}

	for _, p := range rec.events {
		if p.Stage == StageExtracting || p.Stage == StageInstalling || p.Stage == StageCompleted {
			t.Errorf("event %s reported after a failed verification", p.Stage)
		}
	}

	if got := readFileForTest(t, f.target); got != oldPayload {
		t.Errorf("target was modified by a failed update: %q", got)
	}
}

// Verification disabled: no verifying events at all.
func TestApplyWithProgressSkippedVerification(t *testing.T) {
	f := newUpdateFixture(t)

	u, err := New(WithTargetPath(f.target), WithAppName(fixtureAppName), WithHTTPClient(f.srv.Client()), WithInsecureSkipVerify())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var rec progressRecorder

	if _, err := u.ApplyWithProgress(t.Context(), f.result(), rec.record); err != nil {
		t.Fatalf("ApplyWithProgress: %v", err)
	}

	for _, p := range rec.events {
		if p.Stage == StageVerifying {
			t.Error("verifying events reported with verification disabled")
		}
	}
}

// Cancellation while the download is parked must surface the context
// error, stop the event stream at downloading, and leave no leftovers.
func TestApplyWithProgressCancelDuringDownload(t *testing.T) {
	u, dir, target, started, release, result := blockedReleaseFixture(t)

	ctx, cancel := context.WithCancel(context.Background())

	var rec progressRecorder

	applyDone := make(chan error, 1)

	go func() {
		_, err := u.ApplyWithProgress(ctx, result, rec.record)

		applyDone <- err
	}()

	<-started

	cancel()

	if err := <-applyDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	close(release)

	for _, p := range rec.events {
		if p.Stage != StageDownloading {
			t.Errorf("event %s reported after a canceled download", p.Stage)
		}
	}

	if got := readFileForTest(t, target); got != oldPayload {
		t.Errorf("target content = %q, want untouched %q", got, oldPayload)
	}

	assertNoStagingLeftovers(t, dir, fixtureAppName)
}

// Cancellation while a custom verifier is parked must surface the context
// error and stop the event stream at verifying.
func TestApplyWithProgressCancelDuringVerify(t *testing.T) {
	f := newUpdateFixture(t)

	entered := make(chan struct{})

	verifier := VerifierFunc(func(ctx context.Context, _ updates.Release, _ updates.Asset, _ io.Reader) error {
		close(entered)

		<-ctx.Done()

		return ctx.Err()
	})

	u, err := New(WithTargetPath(f.target), WithAppName(fixtureAppName), WithHTTPClient(f.srv.Client()), WithVerifier(verifier))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	var rec progressRecorder

	applyDone := make(chan error, 1)

	go func() {
		_, err := u.ApplyWithProgress(ctx, f.result(), rec.record)

		applyDone <- err
	}()

	<-entered

	cancel()

	if err := <-applyDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	for _, p := range rec.events {
		if p.Stage == StageExtracting || p.Stage == StageInstalling || p.Stage == StageCompleted {
			t.Errorf("event %s reported after a canceled verification", p.Stage)
		}
	}

	if got := readFileForTest(t, f.target); got != oldPayload {
		t.Errorf("target content = %q, want untouched %q", got, oldPayload)
	}

	assertNoStagingLeftovers(t, f.dir, fixtureAppName)
}

// Cancellation during extraction: the progress callback cancels the
// context on the first extracting byte observation; the streaming
// extraction must abort and clean up. The payload is raw and larger than
// one copy chunk, so extraction has several read boundaries.
func TestApplyWithProgressCancelDuringExtract(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, fixtureAppName)

	bigPayload := bytes.Repeat([]byte("v2"), 64<<10) // 128 KiB

	if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	assetName := fixtureAppName + "_2.0.0_" + runtime.GOOS + "_" + runtime.GOARCH

	sum := sha256.Sum256(bigPayload)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			_, _ = w.Write(bigPayload)

			return
		}

		_, _ = fmt.Fprint(w, hex.EncodeToString(sum[:]))
	}))
	t.Cleanup(srv.Close)

	release := updates.Release{
		Version: "v2.0.0",
		Assets: []updates.Asset{
			{Name: assetName, URL: srv.URL + "/asset"},
			{Name: assetName + ".sha256", URL: srv.URL + "/" + assetName + ".sha256"},
		},
	}

	u, err := New(WithTargetPath(target), WithAppName(fixtureAppName), WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	u.progressInterval = 0

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var rec progressRecorder

	result := updates.Result{
		CurrentVersion: "1.0.0",
		Latest:         &release,
		Status:         updates.StatusUpdateAvailable,
	}

	_, err = u.ApplyWithProgress(ctx, result, func(p Progress) {
		rec.record(p)

		if p.Stage == StageExtracting && p.Current > 0 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	if got := readFileForTest(t, target); got != oldPayload {
		t.Errorf("target content = %q, want untouched %q", got, oldPayload)
	}

	assertNoStagingLeftovers(t, dir, fixtureAppName)
}

// Installation boundary: the StageInstalling transition is the last point
// where cancellation is offered. A context canceled right after it must
// abort cleanly at the installer entry check, with no installing final, no
// StageCompleted, and an untouched target.
func TestApplyWithProgressCancelAtInstallBoundary(t *testing.T) {
	f := newUpdateFixture(t)

	u, err := New(WithTargetPath(f.target), WithAppName(fixtureAppName), WithHTTPClient(f.srv.Client()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	u.progressInterval = 0

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var rec progressRecorder

	_, err = u.ApplyWithProgress(ctx, f.result(), func(p Progress) {
		rec.record(p)

		if p.Stage == StageInstalling {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	// Exactly one installing event — the transition the callback canceled
	// on. No installing final, no StageCompleted: the pipeline failed.
	var installing, completed int

	for _, p := range rec.events {
		switch p.Stage {
		case StageInstalling:
			installing++

		case StageCompleted:
			completed++
		}
	}

	if installing != 1 {
		t.Errorf("installing events = %d, want only the canceled transition", installing)
	}

	if completed != 0 {
		t.Error("StageCompleted reported for a canceled pipeline")
	}

	if got := readFileForTest(t, f.target); got != oldPayload {
		t.Errorf("target content = %q, want untouched %q", got, oldPayload)
	}

	assertNoStagingLeftovers(t, f.dir, fixtureAppName)
}

// ErrReplaced failures keep the documented semantics: the returned Applied
// describes the paths involved, but no installing final and no
// StageCompleted are reported for the failed pipeline.
func TestApplyWithProgressErrReplacedReportsNoCompletion(t *testing.T) {
	f := newUpdateFixture(t)

	u, err := New(WithTargetPath(f.target), WithAppName(fixtureAppName), WithHTTPClient(f.srv.Client()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	syncCalled := 0

	u.installer.syncDirFn = func(dir string) error {
		syncCalled++

		// Fail only after the replacement, like the installer-level test.
		if syncCalled > 1 {
			return errors.New("sync failed")
		}

		return nil
	}

	var rec progressRecorder

	applied, err := u.ApplyWithProgress(t.Context(), f.result(), rec.record)
	if !errors.Is(err, ErrReplaced) {
		t.Fatalf("err = %v, want ErrReplaced", err)
	}

	if applied.Path != f.target || applied.Backup != f.target+BackupSuffix {
		t.Errorf("applied = %+v, want populated paths", applied)
	}

	for _, p := range rec.events {
		if p.Stage == StageCompleted {
			t.Error("StageCompleted reported for an ErrReplaced failure")
		}
	}
}
