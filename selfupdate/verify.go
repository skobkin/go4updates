package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	updates "github.com/skobkin/go4updates"
)

// maxChecksumBytes bounds a checksum asset download. Checksum files are
// small manifests; a larger "checksum" is a broken or hostile publisher.
const maxChecksumBytes int64 = 1 << 20 // 1 MiB

// Verifier validates the downloaded bytes of a release asset before they
// are extracted or installed.
//
// Verify receives the complete downloaded asset as data and the release it
// was published in, so implementations can download a sibling checksum or
// signature asset through the package's HTTPS-only [Downloader]. A nil
// error commits the bytes to extraction and installation; a non-nil error
// aborts the update and no verified byte is ever written to the target.
type Verifier interface {
	// Verify checks data against the verifier's source of truth.
	Verify(ctx context.Context, release updates.Release, asset updates.Asset, data io.Reader) error
}

// ChecksumVerifier verifies the SHA-256 digest of a downloaded asset
// against a checksum asset published alongside it in the same release.
//
// The checksum source is resolved among release.Assets in this order:
//
//  1. an asset named "<asset>.sha256";
//  2. an asset named "<asset>.sha256sum";
//  3. a checksum manifest whose lowercased name contains "checksums" or
//     "sha256sums" (for example "checksums.txt" or "SHA256SUMS").
//     Signature sidecars of the manifest itself ("checksums.txt.sig",
//     "checksums.txt.asc", ".minisig", ".gpg", ".bundle", ".sigstore")
//     do not count as manifests. Several manifests are ambiguous and fail.
//
// A sibling file holds the bare digest, optionally followed by the asset
// file name. Manifest lines follow the GNU sha256sum format
// "<digest>[ *]<filename>"; the line whose file name case-insensitively
// equals asset.Name wins, and a manifest consisting of a single bare digest
// line is accepted as the digest of the asset.
//
// The checksum asset is downloaded through the same HTTPS-only, size
// bounded downloader machinery as the binary, with its own 1 MiB limit.
//
// A published checksum provides integrity, not independent publisher
// authentication: the checksum comes from the same release as the binary,
// so a compromised release can publish a matching pair. Transport is
// protected by HTTPS only. Cryptographic publisher signatures are the
// reserved [Verifier] extension point.
type ChecksumVerifier struct {
	downloader *Downloader
}

// compile-time check that ChecksumVerifier implements Verifier.
var _ Verifier = (*ChecksumVerifier)(nil)

// NewChecksumVerifier creates a ChecksumVerifier from opts. It understands
// [WithHTTPClient] and [WithAllowHTTP]. The checksum download limit is
// fixed at 1 MiB regardless of [WithMaxAssetBytes].
func NewChecksumVerifier(opts ...Option) (*ChecksumVerifier, error) {
	d, err := NewDownloader(opts...)
	if err != nil {
		return nil, err
	}

	d.maxBytes = maxChecksumBytes

	return &ChecksumVerifier{downloader: d}, nil
}

// Verify implements [Verifier] by resolving, downloading, and checking the
// published SHA-256 checksum of asset.
func (v *ChecksumVerifier) Verify(ctx context.Context, release updates.Release, asset updates.Asset, data io.Reader) error {
	source, err := findChecksumSource(release, asset)
	if err != nil {
		return err
	}

	var contents bytes.Buffer

	if err := v.downloader.Download(ctx, source.asset, &contents); err != nil {
		return fmt.Errorf("go4updates/selfupdate: downloading checksum %q: %w", source.asset.Name, err)
	}

	var expected string

	if source.manifest {
		expected, err = manifestDigestFor(contents.Bytes(), asset.Name)
	} else {
		expected, err = digestFileContents(contents.Bytes(), asset.Name)
	}

	if err != nil {
		return err
	}

	actual, err := hexDigest(ctx, data)
	if err != nil {
		return fmt.Errorf("go4updates/selfupdate: hashing asset %q: %w", asset.Name, err)
	}

	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("%w: asset %q digest is %s, published checksum is %s",
			ErrChecksumMismatch, asset.Name, actual, expected)
	}

	return nil
}

// checksumSource is the resolved checksum asset for a release asset.
type checksumSource struct {
	asset updates.Asset

	// manifest reports whether the asset is a multi-file checksum manifest
	// (parsed line by line) instead of a per-file digest file.
	manifest bool
}

// manifestSidecarExts lists file-name extensions of signature and
// attestation sidecars that belong to a checksum manifest itself
// ("checksums.txt.sig", "checksums.txt.asc", cosign bundles, sigstore
// attestations). Their names contain "checksums" or "sha256sums" too, but
// they are not additional manifests.
var manifestSidecarExts = []string{
	".sig", ".asc", ".minisig", ".gpg", ".bundle", ".sigstore", ".sigstore.json",
}

// isManifestSidecar reports whether a lowercased asset name is a signature
// sidecar of a checksum manifest rather than a manifest itself.
func isManifestSidecar(lower string) bool {
	for _, ext := range manifestSidecarExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}

	return false
}

// findChecksumSource resolves the checksum asset for asset among
// release.Assets: exact "<name>.sha256" / "<name>.sha256sum" siblings
// first, then a single checksum manifest.
func findChecksumSource(release updates.Release, asset updates.Asset) (checksumSource, error) {
	lower := strings.ToLower(asset.Name)
	siblings := []string{lower + ".sha256", lower + ".sha256sum"}

	var (
		sha256Asset, sha256sumAsset updates.Asset

		foundSha256, foundSha256sum bool

		manifests []updates.Asset
	)

	// One pass over the assets; the sibling choice happens afterwards so
	// the documented precedence ("<asset>.sha256", then
	// "<asset>.sha256sum") holds regardless of the release asset order.
	for _, a := range release.Assets {
		name := strings.ToLower(a.Name)

		switch name {
		case siblings[0]:
			if !foundSha256 {
				foundSha256 = true
				sha256Asset = a
			}

		case siblings[1]:
			if !foundSha256sum {
				foundSha256sum = true
				sha256sumAsset = a
			}
		}

		if (strings.Contains(name, "checksums") || strings.Contains(name, "sha256sums")) &&
			!isManifestSidecar(name) {
			manifests = append(manifests, a)
		}
	}

	if foundSha256 {
		return checksumSource{asset: sha256Asset}, nil
	}

	if foundSha256sum {
		return checksumSource{asset: sha256sumAsset}, nil
	}

	switch len(manifests) {
	case 1:
		return checksumSource{asset: manifests[0], manifest: true}, nil

	case 0:
		return checksumSource{}, fmt.Errorf(
			"%w: looked for %s.sha256, %s.sha256sum, or a checksums manifest among %d release assets",
			ErrNoChecksum, asset.Name, asset.Name, len(release.Assets))

	default:
		names := make([]string, 0, len(manifests))
		for _, a := range manifests {
			names = append(names, a.Name)
		}

		return checksumSource{}, fmt.Errorf(
			"%w: several checksum manifests are published (%s); use a custom Verifier to select one",
			ErrNoChecksum, strings.Join(names, ", "))
	}
}

// digestFileContents parses a per-asset checksum file: the first
// non-empty, non-comment line holds the bare digest, optionally followed by
// the asset file name.
func digestFileContents(contents []byte, fileName string) (string, error) {
	for line := range strings.SplitSeq(string(contents), "\n") {
		line = strings.TrimSpace(line)

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		digest, file, ok := parseChecksumLine(line)
		if !ok {
			return "", fmt.Errorf("go4updates/selfupdate: malformed checksum file for %q", fileName)
		}

		// Publishers disagree about letter case; the recorded file name is
		// compared case-insensitively, like the sibling resolution.
		if file != "" && !strings.EqualFold(file, fileName) {
			return "", fmt.Errorf("go4updates/selfupdate: checksum file for %q lists %q", fileName, file)
		}

		return digest, nil
	}

	return "", fmt.Errorf("go4updates/selfupdate: empty checksum file for %q", fileName)
}

// manifestDigestFor parses a GNU sha256sum-style manifest and returns the
// digest recorded for fileName. A manifest consisting of a single bare
// digest line is accepted as the digest of the asset.
func manifestDigestFor(contents []byte, fileName string) (string, error) {
	var (
		matching string
		bare     []string
	)

	for line := range strings.SplitSeq(string(contents), "\n") {
		line = strings.TrimSpace(line)

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		digest, file, ok := parseChecksumLine(line)
		if !ok {
			// Manifests list many files and may include formats this
			// parser does not know; unrelated lines are skipped.
			continue
		}

		switch {
		case file == "":
			bare = append(bare, digest)

		case strings.EqualFold(file, fileName):
			matching = digest
		}
	}

	switch {
	case matching != "":
		return matching, nil

	case len(bare) == 1:
		return bare[0], nil

	case len(bare) > 1:
		return "", fmt.Errorf("%w: manifest lists %d bare digests for %q",
			ErrNoChecksum, len(bare), fileName)

	default:
		return "", fmt.Errorf("%w: manifest does not list %q", ErrNoChecksum, fileName)
	}
}

// parseChecksumLine parses one "<digest>[ *]<filename>" line. A line
// holding only a 64-character hex digest is a bare digest with an empty
// file name. Malformed lines report ok=false.
func parseChecksumLine(line string) (digest, file string, ok bool) {
	if len(line) == 64 {
		if isHex64(line) {
			return strings.ToLower(line), "", true
		}

		return "", "", false
	}

	if len(line) <= 64 {
		return "", "", false
	}

	head := strings.ToLower(line[:64])
	if !isHex64(head) {
		return "", "", false
	}

	rest := line[64:]

	switch {
	case strings.HasPrefix(rest, " *"): // binary mode
		file = rest[2:]

	case strings.HasPrefix(rest, "  "): // text mode: digest, mode space, name
		file = rest[2:]

	case strings.HasPrefix(rest, " "): // tolerant single-space variant
		file = rest[1:]

	default:
		return "", "", false
	}

	return head, strings.TrimSpace(strings.TrimPrefix(file, "./")), true
}

// isHex64 reports whether s is exactly 64 hexadecimal characters.
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}

	for _, c := range s {
		isHex := (c >= '0' && c <= '9') ||
			(c >= 'a' && c <= 'f') ||
			(c >= 'A' && c <= 'F')

		if !isHex {
			return false
		}
	}

	return true
}

// hexDigest computes the lowercase hex SHA-256 digest of data. The context
// is checked between chunks, so hashing a large asset aborts promptly on
// cancellation instead of running to completion.
func hexDigest(ctx context.Context, data io.Reader) (string, error) {
	h := sha256.New()

	buf := make([]byte, 32<<10)

	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		n, err := data.Read(buf)

		if n > 0 {
			_, _ = h.Write(buf[:n])
		}

		switch {
		case err == io.EOF:
			return hex.EncodeToString(h.Sum(nil)), nil

		case err != nil:
			return "", err
		}
	}
}
