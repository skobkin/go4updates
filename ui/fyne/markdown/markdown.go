package markdown

import (
	"net/http"
	"time"

	"fyne.io/fyne/v2/widget"
)

// DefaultImageLoadTimeout bounds a single remote image download when
// Options.ImageLoadTimeout is zero.
const DefaultImageLoadTimeout = 12 * time.Second

// DefaultMaxImageBytes is the largest accepted image body, in bytes, when
// Options.MaxImageBytes is zero.
const DefaultMaxImageBytes = int64(1 << 20)

// DefaultMaxImageWidth is the widest a rendered image may become, in Fyne
// units, when Options.MaxImageWidth is zero.
const DefaultMaxImageWidth = float32(560)

// DefaultMaxImageHeight is the tallest a rendered image may become, in Fyne
// units, when Options.MaxImageHeight is zero.
const DefaultMaxImageHeight = float32(320)

// Options configures [NewRichText].
//
// The zero value renders Markdown without any image loading: no request is
// made and no other source is read.
type Options struct {
	// RemoteImages enables downloading the images referenced by the
	// Markdown from http and https locations. It is disabled by default,
	// and enabling it never widens the set of allowed schemes.
	RemoteImages bool

	// HTTPClient downloads remote images. A nil client makes every
	// [NewRichText] call create its own client; the package never keeps a
	// client of its own.
	HTTPClient *http.Client

	// ImageLoadTimeout bounds a single remote image download, covering
	// connection, transfer, and decoding deadlines. Zero selects
	// [DefaultImageLoadTimeout].
	ImageLoadTimeout time.Duration

	// MaxImageBytes is the largest accepted image body in bytes. Larger
	// images are reported by their Content-Length and rejected without
	// being read, and a body that grows past the limit while it is read is
	// abandoned. Zero selects [DefaultMaxImageBytes].
	MaxImageBytes int64

	// MaxImageWidth limits the rendered width of an image, in Fyne units.
	// Larger images are scaled down to fit. Zero selects
	// [DefaultMaxImageWidth].
	MaxImageWidth float32

	// MaxImageHeight limits the rendered height of an image, in Fyne
	// units. Larger images are scaled down to fit. Zero selects
	// [DefaultMaxImageHeight].
	MaxImageHeight float32
}

// withDefaults returns the options with every zero field replaced by its
// documented default.
func (o Options) withDefaults() Options {
	if o.ImageLoadTimeout <= 0 {
		o.ImageLoadTimeout = DefaultImageLoadTimeout
	}

	if o.MaxImageBytes <= 0 {
		o.MaxImageBytes = DefaultMaxImageBytes
	}

	if o.MaxImageWidth <= 0 {
		o.MaxImageWidth = DefaultMaxImageWidth
	}

	if o.MaxImageHeight <= 0 {
		o.MaxImageHeight = DefaultMaxImageHeight
	}

	return o
}

// NewRichText parses Markdown text into a rich text widget.
//
// Image references are rewritten into lazily loaded segments, so parsing and
// rendering never block on image downloads. Images are loaded in the
// background and only when opts allows them: with the zero-value options the
// placeholder is rendered and nothing is read, and with remote images enabled
// only http and https locations are fetched.
//
// Each call is independent: the image loading policy and any HTTP client
// created for it live for this widget only.
func NewRichText(text string, opts Options) *widget.RichText {
	loader := newImageLoader(opts.withDefaults())
	rich := widget.NewRichTextFromMarkdown(text)
	rich.Segments = lazySegments(rich.Segments, loader)

	return rich
}

// lazySegments returns segments with every image segment replaced by a
// lazily loaded one, recursing into the container segments a Markdown
// document is built from.
func lazySegments(segments []widget.RichTextSegment, loader *imageLoader) []widget.RichTextSegment {
	rewritten := make([]widget.RichTextSegment, 0, len(segments))

	for _, segment := range segments {
		switch current := segment.(type) {
		case *widget.ImageSegment:
			rewritten = append(rewritten, newLazyImageSegment(current, loader))

		case *widget.ListSegment:
			clone := *current
			clone.Items = lazySegments(current.Items, loader)
			rewritten = append(rewritten, &clone)

		case *widget.ParagraphSegment:
			clone := *current
			clone.Texts = lazySegments(current.Texts, loader)
			rewritten = append(rewritten, &clone)

		default:
			rewritten = append(rewritten, segment)
		}
	}

	return rewritten
}
