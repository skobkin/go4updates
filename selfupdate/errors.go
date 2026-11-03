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
	// already exists at the target. The backup of an unconfirmed update is
	// never overwritten: confirm the installed version is healthy and call
	// [Installer.Cleanup] to retire it, then retry.
	ErrBackupExists = errors.New("go4updates/selfupdate: backup of the previous executable already exists")

	// ErrReplaced reports that the executable replacement itself succeeded
	// before the error being reported, which concerns post-replacement work
	// only (for example a directory sync failure). When errors.Is finds it,
	// the installed binary is the new one; otherwise a failed install left
	// the target untouched.
	ErrReplaced = errors.New("go4updates/selfupdate: executable was replaced before the failure")
)
