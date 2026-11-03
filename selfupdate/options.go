package selfupdate

import (
	"fmt"
	"net/http"
)

// options carries the configuration shared by the selfupdate components.
// Options are applied in order and values are resolved once after all
// options are applied, so the final configuration does not depend on
// option order.
type options struct {
	httpClient *http.Client
	allowHTTP  bool

	maxAssetBytes int64
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
// omits Content-Length. Zero selects [DefaultMaxAssetBytes]; non-positive
// values are rejected.
func WithMaxAssetBytes(n int64) Option {
	return func(o *options) error {
		if n <= 0 {
			return fmt.Errorf("max asset bytes must be positive, got %d", n)
		}

		o.maxAssetBytes = n

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
