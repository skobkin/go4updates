package selfupdate

import "errors"

var (
	// ErrNoRelease reports that the checked result carried no release, so
	// there is nothing to apply.
	ErrNoRelease = errors.New("go4updates/selfupdate: result carries no release")

	// ErrNotUpdateAvailable reports that the result status does not allow an
	// update. [WithForce] bypasses this guard; it never bypasses
	// verification.
	ErrNotUpdateAvailable = errors.New("go4updates/selfupdate: result status is not update_available")

	// ErrNoMatchingAsset reports that no release asset matches the target
	// platform under the configured asset matcher.
	ErrNoMatchingAsset = errors.New("go4updates/selfupdate: no release asset matches the target platform")

	// ErrAmbiguousAsset reports that several release assets match equally
	// well, so no safe automatic choice exists. Applications can disambiguate
	// with [WithAssetMatcher].
	ErrAmbiguousAsset = errors.New("go4updates/selfupdate: multiple release assets match equally")

	// ErrInsecureURL reports that an asset or checksum URL is not HTTPS and
	// plain HTTP was not allowed with [WithAllowHTTP]. Downgrade redirects
	// from HTTPS to HTTP are rejected with the same error.
	ErrInsecureURL = errors.New("go4updates/selfupdate: insecure HTTP URL refused")

	// ErrAssetTooLarge reports that an asset exceeds the configured download
	// size limit ([WithMaxAssetBytes]).
	ErrAssetTooLarge = errors.New("go4updates/selfupdate: asset exceeds the configured size limit")

	// ErrNoChecksum reports that the release publishes no checksum asset for
	// the selected asset. Updates fail loudly rather than proceeding
	// unverified; [WithInsecureSkipVerify] is the explicit opt-out.
	ErrNoChecksum = errors.New("go4updates/selfupdate: no checksum asset found for the selected asset")

	// ErrChecksumMismatch reports that the downloaded asset digest does not
	// match the published checksum.
	ErrChecksumMismatch = errors.New("go4updates/selfupdate: asset digest does not match the published checksum")

	// ErrExtractedTooLarge reports that the extracted executable exceeds the
	// configured extraction size limit ([WithMaxExtractedBytes]). Enforced
	// while streaming decompressed data to stop decompression bombs early.
	ErrExtractedTooLarge = errors.New("go4updates/selfupdate: extracted executable exceeds the configured size limit")

	// ErrExecutableNotFound reports that the release archive does not contain
	// a usable executable entry.
	ErrExecutableNotFound = errors.New("go4updates/selfupdate: executable not found inside the release archive")

	// ErrAmbiguousExecutable reports that several archive entries qualify as
	// the executable, so no safe automatic choice exists.
	ErrAmbiguousExecutable = errors.New("go4updates/selfupdate: multiple archive members match the executable name")

	// ErrUnsupportedArchive reports that the release asset is packed in a
	// supported-by-name but not implemented archive format.
	ErrUnsupportedArchive = errors.New("go4updates/selfupdate: archive format is not supported")

	// ErrBackupExists reports that a backup of the previous executable
	// already exists at the target. While this process installs, an
	// existing backup is never overwritten, written, or removed: confirm
	// the installed version is healthy and call [Installer.Cleanup] to
	// retire it, then retry. Cross-process updates of one target are
	// unsupported — the existence checks fail fast in the common races and
	// backup publication is no-clobber where the filesystem provides
	// atomic hardlinks, but this is not a locking system.
	ErrBackupExists = errors.New("go4updates/selfupdate: backup of the previous executable already exists")

	// ErrTargetNotRegular reports that the installation target exists but is
	// not a regular file (a directory, symbolic link, or device node), so
	// the updater refuses to replace it. Reported by
	// [Updater.CheckInstallability]; Apply fails on the same condition with
	// its own wrapped error.
	ErrTargetNotRegular = errors.New("go4updates/selfupdate: installation target is not a regular file")

	// ErrTargetNotExecutable reports that the target exists without
	// executable permission bits and no mode is forced with [WithFileMode]:
	// the replacement inherits the target mode and could not be run
	// afterwards. Write bits on the target file itself are deliberately not
	// checked — on Unix, directory permissions govern replacement, so a
	// read-only executable is perfectly replaceable.
	ErrTargetNotExecutable = errors.New("go4updates/selfupdate: installation target has no executable permission bits")

	// ErrReplaced reports that the attempt changed the target name before
	// the error being reported: either the replacement itself succeeded and
	// the error concerns post-replacement work only (for example a
	// directory sync failure), or — the Windows restore-failure paths — the
	// target was moved aside and could not be restored, so the target path
	// is missing and the returned backup path holds the old executable.
	// On the restore-failure paths the original installation failure and
	// the restoration failure are joined into the chain, so errors.Is can
	// identify all three. When errors.Is finds ErrReplaced, the target is
	// not necessarily the old binary: run [Installer.Cleanup] to recover a
	// consistent state before retrying. Otherwise a failed install left
	// the target untouched.
	ErrReplaced = errors.New("go4updates/selfupdate: executable was replaced before the failure")
)
