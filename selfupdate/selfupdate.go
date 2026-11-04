package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	updates "github.com/skobkin/go4updates"
)

// Applied describes one completed update application.
type Applied struct {
	// Version is the release version that was installed.
	Version string

	// Asset is the release asset that was installed.
	Asset updates.Asset

	// Path is the installation target path.
	Path string

	// Backup is the path of the retained previous binary, empty when the
	// target did not previously exist. Removed by [Installer.Cleanup] after
	// the application confirms the new version is healthy.
	Backup string
}

// Updater applies update results produced by the updates package by
// selecting, downloading, verifying, extracting, and installing one release
// asset. It never re-fetches release metadata and never compares versions:
// the decision that an update exists belongs to the checker, the watcher,
// and the application. The library never calls Apply or Cleanup on its own.
//
// An Updater is safe for concurrent use and starts no background work.
// Apply and Cleanup on one Updater are serialized against each other for
// the whole life of the call: a Cleanup waits while an Apply runs — or
// fails with its context's cancellation error if the wait is canceled — so
// it never removes the download or staging files of an in-progress update,
// and concurrent Apply calls run one at a time. SelectAsset touches no
// files and stays freely callable. The Updater's [Installer] keeps its own
// locking for direct Installer consumers.
//
// The pipeline steps are also available as composable pieces: [Downloader]
// streams any release asset to an arbitrary [io.Writer], [Verifier]
// implementations validate downloaded bytes, and [Installer] replaces a
// binary with a staged file.
type Updater struct {
	downloader *Downloader
	verifier   Verifier
	installer  *Installer
	matcher    AssetMatcher
	appName    string
	force      bool

	maxExtracted int64

	// progressInterval and nowFunc are seams for the progress reporting of
	// ApplyWithProgress: the minimum time between intermediate byte events
	// and the clock used to enforce it. Tests inject both for deterministic
	// throttling assertions; production values are the defaults set in New.
	progressInterval time.Duration
	nowFunc          func() time.Time

	// lifecycle is a one-slot semaphore held for the whole of an Apply
	// pipeline or a Cleanup sweep: Cleanup must never sweep the download
	// and staging files an in-progress Apply is still writing. The
	// Installer keeps its own mutex for direct Installer consumers.
	lifecycle chan struct{}
}

// New creates an Updater from opts, resolving defaults for anything unset:
// the target path defaults to the running executable, the application name
// to the target file name, the matcher to [DefaultAssetMatcher], the
// verifier to [ChecksumVerifier], and all sizes to their defaults.
func New(opts ...Option) (*Updater, error) {
	o, err := applyOptions("go4updates/selfupdate", opts)
	if err != nil {
		return nil, err
	}

	target := o.targetPath

	if target == "" {
		target, err = runningExecutablePath()
		if err != nil {
			return nil, err
		}
	} else {
		target = resolveSymlinks(target)
	}

	appName := o.appName

	if appName == "" {
		appName = strings.TrimSuffix(filepath.Base(target), ".exe")
	}

	if appName == "" {
		return nil, errors.New("go4updates/selfupdate: application name is empty")
	}

	matcher := o.assetMatcher
	if matcher == nil {
		matcher = DefaultAssetMatcher(appName)
	}

	downloader, err := NewDownloader(opts...)
	if err != nil {
		return nil, err
	}

	var verifier Verifier

	switch {
	case o.insecureSkipVerify && o.verifier != nil:
		return nil, errors.New("go4updates/selfupdate: WithInsecureSkipVerify and WithVerifier are mutually exclusive")

	case o.insecureSkipVerify:
		// Verification disabled by explicit request.

	case o.verifier != nil:
		verifier = o.verifier

	default:
		verifier, err = NewChecksumVerifier(opts...)
		if err != nil {
			return nil, err
		}
	}

	installOpts := []Option{WithTargetPath(target)}

	if o.fileModeSet {
		installOpts = append(installOpts, WithFileMode(o.fileMode))
	}

	installer, err := NewInstaller(installOpts...)
	if err != nil {
		return nil, err
	}

	maxExtracted := o.maxExtractedBytes
	if maxExtracted <= 0 {
		maxExtracted = DefaultMaxExtractedBytes
	}

	return &Updater{
		downloader:       downloader,
		verifier:         verifier,
		installer:        installer,
		matcher:          matcher,
		appName:          appName,
		force:            o.force,
		maxExtracted:     maxExtracted,
		progressInterval: defaultProgressInterval,
		nowFunc:          time.Now,
		lifecycle:        make(chan struct{}, 1),
	}, nil
}

// SelectAsset returns the release asset matching the configured platform
// policy. Exported so applications can preview or confirm the choice with
// the user before calling Apply.
func (u *Updater) SelectAsset(release updates.Release) (updates.Asset, error) {
	return selectAsset(release.Assets, u.matcher)
}

// acquireLifecycle takes the Updater-wide lifecycle lock, returning a
// release function. Apply holds it for the whole pipeline and Cleanup for
// its sweep, so a concurrent Cleanup can never remove the download or
// staging files an in-progress Apply is still writing. A context canceled
// while another Apply or Cleanup holds the lock fails the wait; a lock
// acquired instantly proceeds even on an already-canceled context, so the
// pipeline's own checks produce their usual, more specific errors.
func (u *Updater) acquireLifecycle(ctx context.Context) (func(), error) {
	select {
	case u.lifecycle <- struct{}{}:
		return func() { <-u.lifecycle }, nil
	default:
	}

	select {
	case u.lifecycle <- struct{}{}:
		return func() { <-u.lifecycle }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("go4updates/selfupdate: another Apply or Cleanup on this Updater is still running: %w", ctx.Err())
	}
}

// Apply installs the release described by result:
//
//	select asset -> download to a temp file -> verify -> extract ->
//	atomic install (with backup).
//
// It never re-fetches release metadata and touches nothing until every
// guard passes. The status guard requires result.Status to be
// [updates.StatusUpdateAvailable] unless the Updater was built with
// [WithForce]; [WithForce] bypasses only this guard — verification always
// applies.
//
// Failure semantics: when Apply returns an error and errors.Is does not
// find [ErrReplaced] in it, nothing was modified and the returned Applied
// is meaningless; with [ErrReplaced], the attempt changed the target name —
// the replacement took effect, or (a documented Windows failure shape) the
// target was moved aside and could not be restored — and the returned
// Applied describes the paths involved; run [Updater.Cleanup] to recover a
// consistent state before retrying. Staging files are removed on every
// failure path; on success the previous binary remains under Applied.Backup
// until the application calls [Updater.Cleanup] — after initialization or a
// health check, never before.
//
// The call holds the Updater-wide lifecycle lock for the whole pipeline
// (see [Updater]): a context canceled while another Apply or Cleanup on
// the same Updater holds the lock fails the call without touching
// anything.
func (u *Updater) Apply(ctx context.Context, result updates.Result) (Applied, error) {
	return u.apply(ctx, result, nil)
}

// ApplyWithProgress installs the release exactly like [Updater.Apply] and
// additionally reports pipeline progress to onProgress. A nil callback
// behaves exactly like Apply.
//
// The callback is invoked synchronously on the goroutine calling
// ApplyWithProgress, while the Updater-wide lifecycle lock is held. It must
// not call Apply, ApplyWithProgress, or Cleanup on the same Updater — the
// lock is already taken and the call would deadlock — and it must not
// block: a slow callback slows the update. Thread-safety of any state the
// callback touches is the caller's concern.
//
// One [Progress] event carries a [Stage], the asset file name, and byte
// counts. The event stream per successful pipeline is:
//
//   - one transition event at the start of each stage, byte counts zeroed;
//   - intermediate byte events during [StageDownloading],
//     [StageVerifying], and [StageExtracting]. They are throttled: at most
//     one per progress interval, and never for an unchanged byte count;
//   - one final byte event per successfully completed stage, never
//     throttled, carrying the exact final byte counts;
//   - one [StageCompleted] event, after everything succeeded.
//
// A failed stage emits no final event and no events for later stages; the
// returned error is the authoritative outcome. [StageCompleted] and the
// [StageInstalling] final event are never emitted for a failed pipeline —
// including failures carrying [ErrReplaced], whose returned Applied still
// describes the paths involved.
//
// Byte-count semantics: a Total of zero means unknown and must not be
// turned into percentages or ETAs; see [Progress]. Download totals come
// from the actual response, not from release metadata. Verification byte
// counts measure the input the verifier actually consumed: the built-in
// checksum verifier reads the whole file, so its events are determinate
// (Total is the downloaded file size); a custom [Verifier] may read any
// part of its input, so its events always carry Total of zero, and its
// final event reports the measured consumption without implying
// completion. Extract totals are the archive member's declared size where
// the format declares one, and zero otherwise.
//
// Cancellation is honored throughout the pre-installation stages. The
// [StageInstalling] transition is the boundary after which a UI must stop
// offering cancellation: the installer may still observe a canceled
// context just before the replacement and abort cleanly, but the
// replacement may also already have taken effect, and canceling never
// implies undoing it (see [Updater.Apply] failure semantics).
func (u *Updater) ApplyWithProgress(ctx context.Context, result updates.Result, onProgress func(Progress)) (Applied, error) {
	return u.apply(ctx, result, onProgress)
}

// apply is the single pipeline behind Apply and ApplyWithProgress.
func (u *Updater) apply(ctx context.Context, result updates.Result, onProgress func(Progress)) (Applied, error) {
	release, err := u.acquireLifecycle(ctx)
	if err != nil {
		return Applied{}, err
	}

	defer release()

	if result.Latest == nil {
		return Applied{}, ErrNoRelease
	}

	if !u.force && result.Status != updates.StatusUpdateAvailable {
		return Applied{}, fmt.Errorf("%w: status is %s", ErrNotUpdateAvailable, result.Status)
	}

	asset, err := u.SelectAsset(*result.Latest)
	if err != nil {
		return Applied{}, err
	}

	emit := newProgressEmitter(onProgress, asset.Name, u.progressInterval, u.nowFunc)

	emit.transition(StageDownloading)

	target := u.installer.TargetPath()
	dir := filepath.Dir(target)
	base := sanitizePattern(filepath.Base(target))

	downloadPath := ""

	stagedPath := ""

	done := false

	defer func() {
		if done {
			return
		}

		if downloadPath != "" {
			_ = u.installer.removeRetirable(downloadPath)
		}

		if stagedPath != "" {
			_ = u.installer.removeRetirable(stagedPath)
		}
	}()

	downloadTemp, err := os.CreateTemp(dir, base+".go4updates.download*")
	if err != nil {
		return Applied{}, fmt.Errorf("go4updates/selfupdate: creating download staging file: %w", err)
	}

	downloadPath = downloadTemp.Name()

	onDownloadBytes := emit.observer()

	if err := u.downloader.download(ctx, asset, downloadTemp, onDownloadBytes); err != nil {
		_ = downloadTemp.Close()

		return Applied{}, err
	}

	if err := downloadTemp.Close(); err != nil {
		return Applied{}, fmt.Errorf("go4updates/selfupdate: closing download staging file: %w", err)
	}

	emit.finalize()

	if u.verifier != nil {
		emit.transition(StageVerifying)

		// Only the built-in checksum verifier is known to consume its
		// whole input, so only its events can honestly carry a total. A
		// custom verifier may read any part of its input; its events stay
		// indeterminate (Total of zero).
		var verifyTotal int64

		if _, builtIn := u.verifier.(*ChecksumVerifier); builtIn {
			if info, statErr := os.Stat(downloadPath); statErr == nil {
				verifyTotal = info.Size()
			}
		}

		onVerifyBytes := emit.observer()

		data, err := os.Open(downloadPath)
		if err != nil {
			return Applied{}, fmt.Errorf("go4updates/selfupdate: opening downloaded asset: %w", err)
		}

		// The counting wrapper also enforces the context between reads, so
		// verifiers that ignore their context still abort on cancellation.
		counted := &countingReader{ctx: ctx, r: data, onBytes: onVerifyBytes, total: verifyTotal}

		err = u.verifier.Verify(ctx, *result.Latest, asset, counted)

		_ = data.Close()

		if err != nil {
			return Applied{}, err
		}

		emit.finalize()
	}

	stagedTemp, err := os.CreateTemp(dir, base+".go4updates.new*")
	if err != nil {
		return Applied{}, fmt.Errorf("go4updates/selfupdate: creating executable staging file: %w", err)
	}

	stagedPath = stagedTemp.Name()
	_ = stagedTemp.Close()

	emit.transition(StageExtracting)

	onExtractBytes := emit.observer()

	if err := extractBinary(ctx, downloadPath, asset.Name, u.appName, runtime.GOOS, stagedPath, u.maxExtracted, onExtractBytes); err != nil {
		return Applied{}, err
	}

	emit.finalize()

	// The archive is consumed; free it before the rename dance.
	_ = u.installer.removeRetirable(downloadPath)

	downloadPath = ""

	emit.transition(StageInstalling)

	backup, err := u.installer.Install(ctx, stagedPath)
	if err != nil {
		// No installing final and no StageCompleted on failure: the error
		// is the authoritative outcome, ErrReplaced failures included.
		if errors.Is(err, ErrReplaced) {
			return Applied{
				Version: result.Latest.Version,
				Asset:   asset,
				Path:    target,
				Backup:  backup,
			}, err
		}

		return Applied{}, err
	}

	done = true

	emit.finalize()

	emit.completed()

	return Applied{
		Version: result.Latest.Version,
		Asset:   asset,
		Path:    target,
		Backup:  backup,
	}, nil
}

// Cleanup removes the artifacts of a completed Apply — most importantly the
// backup of the previous version — and recovers an interrupted install where
// possible. It is shorthand for [Installer.Cleanup] on the Updater's
// installer; ctx is checked between its steps, and a canceled run never
// retires the backup.
//
// The library never calls Cleanup on its own. Call it after successful
// initialization or an application-defined health check — reaching the new
// binary proves it starts, not that it is healthy — and never before: until
// then the backup is the only rollback material. Cleanup is an artifact
// collector and interrupted-update recovery tool, not a rollback mechanism.
//
// Cleanup holds the Updater-wide lifecycle lock while it runs (see
// [Updater]): it waits for an in-progress Apply on the same Updater — its
// context may cancel the wait — so it never sweeps that update's download
// or staging files.
func (u *Updater) Cleanup(ctx context.Context) error {
	release, err := u.acquireLifecycle(ctx)
	if err != nil {
		return err
	}

	defer release()

	return u.installer.Cleanup(ctx)
}
