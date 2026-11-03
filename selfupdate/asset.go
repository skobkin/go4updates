package selfupdate

import (
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"

	updates "github.com/skobkin/go4updates"
)

// AssetMatcher reports whether an asset is an installable candidate for the
// configured application and platform.
//
// A matcher sees the whole [updates.Asset], so custom schemes can decide
// based on the name, the content type, the size, or the URL. The default
// matcher is built by [DefaultAssetMatcher].
type AssetMatcher func(asset updates.Asset) bool

// archiveKind is the packaging format of a release asset, detected from the
// file name extension.
type archiveKind int

const (
	// kindRaw is a bare executable file with no archive extension.
	kindRaw archiveKind = iota

	// kindZip is a ZIP archive.
	kindZip

	// kindTarGz is a gzipped tar archive (.tar.gz or .tgz).
	kindTarGz

	// kindTar is an uncompressed tar archive.
	kindTar

	// kindGz is a single-member gzip stream.
	kindGz

	// kindUnknown marks names with no recognized or with a recognized but
	// unsupported archive extension. Used only for ranking; classification
	// returns the error instead.
	kindUnknown
)

// unsupportedArchiveExts lists archive extensions that are recognized but
// not implemented. Longer suffixes must come first so ".tar.xz" wins over
// ".xz".
var unsupportedArchiveExts = []string{
	".tar.xz", ".tar.bz2", ".tar.zst", ".txz", ".tbz2",
	".xz", ".bz2", ".zst", ".7z", ".rar",
}

// classifyAssetName detects the archive format of a release asset from its
// file name. Names with a known but unsupported compression extension
// return an error wrapping [ErrUnsupportedArchive].
func classifyAssetName(name string) (archiveKind, error) {
	lower := strings.ToLower(name)

	for _, ext := range unsupportedArchiveExts {
		if strings.HasSuffix(lower, ext) {
			return kindUnknown, fmt.Errorf("%w: %q", ErrUnsupportedArchive, name)
		}
	}

	switch {
	case strings.HasSuffix(lower, ".zip"):
		return kindZip, nil

	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return kindTarGz, nil

	case strings.HasSuffix(lower, ".tar"):
		return kindTar, nil

	case strings.HasSuffix(lower, ".gz"):
		return kindGz, nil

	default:
		return kindRaw, nil
	}
}

// stripArchiveExt removes the archive extension detected by
// classifyAssetName from name (which may have any case).
func stripArchiveExt(name string, kind archiveKind) string {
	lower := strings.ToLower(name)

	switch kind {
	case kindZip:
		return strings.TrimSuffix(lower, ".zip")

	case kindTarGz:
		if strings.HasSuffix(lower, ".tar.gz") {
			return strings.TrimSuffix(lower, ".tar.gz")
		}

		return strings.TrimSuffix(lower, ".tgz")

	case kindTar:
		return strings.TrimSuffix(lower, ".tar")

	case kindGz:
		return strings.TrimSuffix(lower, ".gz")

	default:
		return lower
	}
}

// osAliases maps a Go GOOS value to the tokens release publishers use for
// it in asset file names.
var osAliases = map[string][]string{
	"linux":   {"linux"},
	"darwin":  {"darwin", "macos", "osx", "mac"},
	"windows": {"windows", "win32", "win64"},
}

// archAliases maps a Go GOARCH value to the tokens release publishers use
// for it in asset file names. ARM variants are one class because Go does
// not report which ARM variant a binary was built for.
var archAliases = map[string][]string{
	"amd64": {"amd64", "x86_64", "x64"},
	"386":   {"386", "i386", "x86"},
	"arm64": {"arm64", "aarch64"},
	"arm":   {"arm", "armv6", "armv7"},
}

// MatchAssetName reports whether a release asset file name follows the
// conventional goreleaser-style layout for appName on goos/goarch:
//
//	{app}[_{-}{version}]_{os}_{arch}[.{archive-ext}]
//
// Tokens are the parts of the name split on "_" and "-". The application,
// operating system, and architecture tokens must all be present; further
// tokens are tolerated only when they look like version labels ("1.2.3",
// "v2", "1.0.0-rc1"). OS and architecture tokens are matched through the
// alias tables (for example "macos" and "osx" for "darwin", "x86_64" for
// "amd64"). Token comparison is exact after lowercasing, so an application
// name containing "_" or "-" never matches.
//
// Checksum, signature, manifest, SBOM, and documentation file names never
// match: their file-name extensions and trailing tokens are not part of the
// convention, so the conservative token match rejects them structurally.
// An application whose own name happens to contain such words ("signal",
// "design") is matched normally. Raw Windows assets must end in ".exe".
// Names with recognized but unsupported archive extensions (.xz, .bz2,
// .zst, .7z, .rar) do not match.
func MatchAssetName(name, appName, goos, goarch string) bool {
	if name == "" || appName == "" {
		return false
	}

	kind, err := classifyAssetName(name)
	if err != nil {
		return false
	}

	base := stripArchiveExt(name, kind)

	if kind == kindRaw && goos == "windows" {
		// A raw Windows asset must be an .exe file; archives are exempt
		// because the .exe lives inside them.
		if !strings.HasSuffix(base, ".exe") {
			return false
		}

		base = strings.TrimSuffix(base, ".exe")
	}

	wantOS, ok := osAliases[goos]
	if !ok {
		return false
	}

	wantArch, ok := archAliases[goarch]
	if !ok {
		return false
	}

	app := strings.ToLower(appName)
	if strings.ContainsAny(app, "_-") {
		return false
	}

	var foundApp, foundOS, foundArch bool

	tokens := strings.FieldsFunc(base, func(r rune) bool {
		return r == '_' || r == '-'
	})

	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]

		// The application token is claimed first: an application named like
		// a platform alias ("mac", "linux", "arm") keeps its token, and the
		// convention places the platform token after it anyway. A later
		// equal token falls through to the platform checks.
		if !foundApp && tok == app {
			foundApp = true

			continue
		}

		if slices.Contains(wantOS, tok) {
			foundOS = true

			continue
		}

		if slices.Contains(wantArch, tok) {
			foundArch = true

			continue
		}

		if versionish(tok) {
			// Version labels are tolerated between the application and
			// platform tokens or after them.
			continue
		}

		// Compound aliases such as "x86_64" contain a token separator and
		// are split by tokenization; retry with the next token joined back
		// together before rejecting the name.
		if i+1 < len(tokens) {
			merged := tok + "_" + tokens[i+1]

			switch {
			case slices.Contains(wantOS, merged):
				foundOS = true

			case slices.Contains(wantArch, merged):
				foundArch = true

			case versionish(merged):
				// Joined version fragments stay tolerated.

			default:
				merged = ""
			}

			if merged != "" {
				i++

				continue
			}
		}

		// Pre-release suffixes such as "1.2.3-rc1" are split the same way;
		// retry joined with the previous version token. The suffix must be
		// plausible as a version label: the joined check alone never
		// validates the appended tail, so any token ("musl", "flavor")
		// would be swallowed and the conservative match would silently
		// accept names outside the convention.
		if i > 0 && versionSuffixish(tok) {
			for _, sep := range []string{"-", "_"} {
				if versionish(tokens[i-1] + sep + tok) {
					tok = "" // consumed as part of the version label

					break
				}
			}

			if tok == "" {
				continue
			}
		}

		// An unrecognized token means the naming scheme differs from the
		// convention; matching conservatively fails.
		return false
	}

	return foundApp && foundOS && foundArch
}

// versionish reports whether a token looks like a version or build label,
// for example "1.2.3", "v2", "1.0.0-rc1", or "20240101".
func versionish(token string) bool {
	rest := strings.TrimPrefix(token, "v")
	if rest == "" {
		return false
	}

	if idx := strings.IndexAny(rest, "-+"); idx >= 0 {
		rest = rest[:idx]
	}

	if rest == "" {
		return false
	}

	for _, part := range strings.Split(rest, ".") {
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}

	return true
}

// versionChannelLabels are release-channel words accepted as version-label
// suffixes without a digit: a channel like "nightly" carries no build
// number, while "rc1" or "beta2" carry one and pass the digit rule.
var versionChannelLabels = []string{
	"alpha", "beta", "rc", "pre", "dev", "snapshot", "nightly", "canary", "debug",
}

// versionSuffixish reports whether tok is a plausible pre-release or build
// label following a version token: it must consist of version-identifier
// characters and carry distinguishing information — either a digit
// ("rc1", "beta2", "20240101") or a known release-channel word. Flavor
// tokens such as "musl" carry neither and are rejected, keeping the match
// conservative.
func versionSuffixish(tok string) bool {
	if tok == "" {
		return false
	}

	hasDigit := false

	for _, r := range tok {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true

		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '.', r == '-':
			// Part of a version identifier.

		default:
			return false
		}
	}

	if hasDigit {
		return true
	}

	return slices.Contains(versionChannelLabels, strings.ToLower(tok))
}

// DefaultAssetMatcher returns the standard [AssetMatcher] for appName on
// the current platform (runtime.GOOS and runtime.GOARCH). See
// [MatchAssetName] for the matched naming convention and its limitations.
func DefaultAssetMatcher(appName string) AssetMatcher {
	return func(asset updates.Asset) bool {
		return MatchAssetName(asset.Name, appName, runtime.GOOS, runtime.GOARCH)
	}
}

// kindRank orders equally matched assets by preference: a raw binary beats
// archives because it involves no extraction step, and less-nested archives
// beat more generic ones. kindUnknown ranks last: a custom matcher may
// deliberately select an unsupported format, which then fails at extraction
// with a clear error instead of being silently skipped.
var kindRank = map[archiveKind]int{
	kindRaw:     0,
	kindZip:     1,
	kindTarGz:   2,
	kindTar:     3,
	kindGz:      4,
	kindUnknown: 5,
}

// rankAssetName returns the preference rank of an asset file name.
func rankAssetName(name string) int {
	kind, err := classifyAssetName(name)
	if err != nil {
		return kindRank[kindUnknown]
	}

	return kindRank[kind]
}

// selectAsset applies matcher to assets and picks the installable
// candidate. When several assets match, the best-ranked archive format wins
// (raw over zip over tar.gz over tar over gz). Several matches of the same
// rank are an error rather than an arbitrary pick; zero matches are an
// error listing every considered asset name.
func selectAsset(assets []updates.Asset, matcher AssetMatcher) (updates.Asset, error) {
	var matched []updates.Asset

	considered := make([]string, 0, len(assets))

	for _, asset := range assets {
		considered = append(considered, asset.Name)

		if matcher(asset) {
			matched = append(matched, asset)
		}
	}

	if len(matched) == 0 {
		return updates.Asset{}, fmt.Errorf("%w: considered: %s",
			ErrNoMatchingAsset, strings.Join(considered, ", "))
	}

	best := matched[0]
	bestRank := rankAssetName(best.Name)

	var tied []string

	for _, asset := range matched[1:] {
		rank := rankAssetName(asset.Name)

		switch {
		case rank < bestRank:
			best = asset
			bestRank = rank
			tied = nil

		case rank == bestRank:
			tied = append(tied, asset.Name)
		}
	}

	if len(tied) > 0 {
		return updates.Asset{}, fmt.Errorf("%w: %s, %s (use WithAssetMatcher to disambiguate)",
			ErrAmbiguousAsset, best.Name, strings.Join(tied, ", "))
	}

	return best, nil
}
