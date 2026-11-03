package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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
// An Updater is immutable after construction and safe for concurrent use;
// it starts no background work.
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

	// The default matcher tokenizes asset names on "_" and "-", so an
	// application name containing either could never match an asset. Reject
	// the misconfiguration here, with the escape hatch, instead of failing
	// with a confusing ErrNoMatchingAsset on the first Apply. A custom
	// matcher lifts the restriction: the name is also used for exact
	// archive-member lookup, which has no separator issue.
	if o.assetMatcher == nil && strings.ContainsAny(appName, "_-") {
		return nil, fmt.Errorf("go4updates/selfupdate: application name %q contains '_' or '-', which the default asset matcher cannot match; provide a custom matcher with WithAssetMatcher", appName)
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
		downloader:   downloader,
		verifier:     verifier,
		installer:    installer,
		matcher:      matcher,
		appName:      appName,
		force:        o.force,
		maxExtracted: maxExtracted,
	}, nil
}

// SelectAsset returns the release asset matching the configured platform
// policy. Exported so applications can preview or confirm the choice with
// the user before calling Apply.
func (u *Updater) SelectAsset(release updates.Release) (updates.Asset, error) {
	return selectAsset(release.Assets, u.matcher)
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
func (u *Updater) Apply(ctx context.Context, result updates.Result) (Applied, error) {
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
			_ = os.Remove(downloadPath)
		}

		if stagedPath != "" {
			_ = os.Remove(stagedPath)
		}
	}()

	downloadTemp, err := os.CreateTemp(dir, base+".go4updates.download*")
	if err != nil {
		return Applied{}, fmt.Errorf("go4updates/selfupdate: creating download staging file: %w", err)
	}

	downloadPath = downloadTemp.Name()

	if err := u.downloader.Download(ctx, asset, downloadTemp); err != nil {
		_ = downloadTemp.Close()

		return Applied{}, err
	}

	if err := downloadTemp.Close(); err != nil {
		return Applied{}, fmt.Errorf("go4updates/selfupdate: closing download staging file: %w", err)
	}

	if u.verifier != nil {
		data, err := os.Open(downloadPath)
		if err != nil {
			return Applied{}, fmt.Errorf("go4updates/selfupdate: opening downloaded asset: %w", err)
		}

		err = u.verifier.Verify(ctx, *result.Latest, asset, data)

		_ = data.Close()

		if err != nil {
			return Applied{}, err
		}
	}

	stagedTemp, err := os.CreateTemp(dir, base+".go4updates.new*")
	if err != nil {
		return Applied{}, fmt.Errorf("go4updates/selfupdate: creating executable staging file: %w", err)
	}

	stagedPath = stagedTemp.Name()
	_ = stagedTemp.Close()

	if err := extractBinary(downloadPath, asset.Name, u.appName, runtime.GOOS, stagedPath, u.maxExtracted); err != nil {
		return Applied{}, err
	}

	// The archive is consumed; free it before the rename dance.
	_ = os.Remove(downloadPath)

	downloadPath = ""

	backup, err := u.installer.Install(ctx, stagedPath)
	if err != nil {
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
// installer.
//
// The library never calls Cleanup on its own. Call it after successful
// initialization or an application-defined health check — reaching the new
// binary proves it starts, not that it is healthy — and never before: until
// then the backup is the only rollback material. Cleanup is an artifact
// collector and interrupted-update recovery tool, not a rollback mechanism.
func (u *Updater) Cleanup() error {
	return u.installer.Cleanup()
}
