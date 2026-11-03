package selfupdate

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
)

// options carries the configuration shared by the selfupdate components.
// Options are applied in order and values are resolved once after all
// options are applied, so the final configuration does not depend on
// option order.
type options struct {
	httpClient *http.Client
	allowHTTP  bool

	maxAssetBytes     int64
	maxExtractedBytes int64

	targetPath  string
	fileMode    fs.FileMode
	fileModeSet bool

	appName            string
	assetMatcher       AssetMatcher
	verifier           Verifier
	insecureSkipVerify bool
	force              bool
}

// Option configures the selfupdate components. Each constructor applies the
// options it understands; invalid values are rejected with an error from
// the constructor the option is passed to.
type Option func(*options) error

// WithHTTPClient sets the HTTP client used for asset and checksum
// downloads. The client is never mutated: scheme guarding wraps its
// transport in a private copy. When unset, a client without an overall
// timeout is used — download size is bounded by [WithMaxAssetBytes] and
// duration by the request context, so a large binary cannot be cut off by
// a total-timeout client.
func WithHTTPClient(client *http.Client) Option {
	return func(o *options) error {
		o.httpClient = client

		return nil
	}
}

// WithAllowHTTP allows plain HTTP URLs for asset and checksum downloads,
// including downgrade redirects from HTTPS. This weakens transport
// protection and is intended for private, self-hosted deployments where
// HTTPS is provided by other means (for example a trusted internal
// network). It is independent of [WithInsecureSkipVerify], which controls
// content verification, not transport.
func WithAllowHTTP() Option {
	return func(o *options) error {
		o.allowHTTP = true

		return nil
	}
}

// WithMaxAssetBytes sets the hard size limit for a single asset download.
// The limit is enforced through the declared Content-Length before the
// transfer and while streaming the body, including when the server lies or
// omits Content-Length. Non-positive values are rejected; when the option
// is not used, [DefaultMaxAssetBytes] applies.
func WithMaxAssetBytes(n int64) Option {
	return func(o *options) error {
		if n <= 0 {
			return fmt.Errorf("max asset bytes must be positive, got %d", n)
		}

		o.maxAssetBytes = n

		return nil
	}
}

// WithTargetPath installs to path instead of the running executable. The
// path must be absolute. Symbolic links in the path are resolved when the
// path exists, so the backup is created next to the real binary.
//
// An explicit target path is required when the process may be launched from
// the backup executable (for example manually, after a failed update):
// os.Executable then resolves to the backup path, and automatic recovery
// would look in the wrong place.
func WithTargetPath(path string) Option {
	return func(o *options) error {
		if path == "" {
			return errors.New("target path must not be empty")
		}

		if !filepath.IsAbs(path) {
			return fmt.Errorf("target path must be absolute, got %q", path)
		}

		o.targetPath = path

		return nil
	}
}

// WithFileMode sets the permission bits applied to the installed
// executable. Without it, a replaced target keeps the mode of the binary it
// replaces, and a newly created target is installed with 0755. Only
// non-zero permission bits are accepted: the zero mode would install a
// non-executable target whose permissions every following install would
// inherit.
func WithFileMode(mode fs.FileMode) Option {
	return func(o *options) error {
		if mode&^fs.ModePerm != 0 || mode.Perm() == 0 {
			return fmt.Errorf("file mode must be non-zero permission bits, got %v", mode)
		}

		o.fileMode = mode
		o.fileModeSet = true

		return nil
	}
}

// WithAppName sets the application name used for asset matching and for
// locating the executable inside archives. When the option is not used, the
// target file name without its ".exe" suffix is used; an empty name is
// rejected. The default asset matcher matches the complete name — "_" and
// "-" included — against the opening of the asset file name; exotic naming
// schemes can bypass it with [WithAssetMatcher].
func WithAppName(name string) Option {
	return func(o *options) error {
		if name == "" {
			return errors.New("application name must not be empty")
		}

		o.appName = name

		return nil
	}
}

// WithAssetMatcher overrides the default platform asset matching entirely.
// See [AssetMatcher] and [MatchAssetName] for the convention the default
// matcher implements and its conservative limitations.
func WithAssetMatcher(matcher AssetMatcher) Option {
	return func(o *options) error {
		if matcher == nil {
			return errors.New("asset matcher must not be nil")
		}

		o.assetMatcher = matcher

		return nil
	}
}

// WithVerifier replaces the default SHA-256 checksum verification with a
// custom implementation, for example an ed25519 signature check. See
// [Verifier] for the contract.
func WithVerifier(verifier Verifier) Option {
	return func(o *options) error {
		if verifier == nil {
			return errors.New("verifier must not be nil")
		}

		o.verifier = verifier

		return nil
	}
}

// WithInsecureSkipVerify disables verification of downloaded updates
// entirely: binaries whose checksums are missing, unreachable, or wrong are
// installed anyway.
//
// This defeats the package's central safety property and must only be used
// when the download channel is fully trusted by other means (private
// infrastructure, tests). It is independent of [WithAllowHTTP], which
// controls transport security, not content verification.
func WithInsecureSkipVerify() Option {
	return func(o *options) error {
		o.insecureSkipVerify = true

		return nil
	}
}

// WithMaxExtractedBytes sets the hard size limit enforced while streaming
// decompressed archive data, so a decompression bomb is aborted early
// instead of filling the disk. What the limit covers is format-dependent:
// tar-family archives are bounded as a whole (the stream must be read
// through), zip archives only in the selected member. Non-positive values
// are rejected; when the option is not used, [DefaultMaxExtractedBytes]
// applies.
func WithMaxExtractedBytes(n int64) Option {
	return func(o *options) error {
		if n <= 0 {
			return fmt.Errorf("max extracted bytes must be positive, got %d", n)
		}

		o.maxExtractedBytes = n

		return nil
	}
}

// WithForce lets Apply run regardless of the result status, for example to
// reinstall the same version or to apply a release whose comparison was
// inconclusive. It bypasses only the status guard: verification, download
// bounds, and the backup rules always apply.
func WithForce() Option {
	return func(o *options) error {
		o.force = true

		return nil
	}
}

// applyOptions applies opts to a fresh options value, skipping nil options
// and wrapping failures. The prefix identifies the failing constructor.
func applyOptions(prefix string, opts []Option) (*options, error) {
	o := &options{}

	for _, opt := range opts {
		if opt == nil {
			continue
		}

		if err := opt(o); err != nil {
			return nil, fmt.Errorf("%s: %w", prefix, err)
		}
	}

	return o, nil
}
