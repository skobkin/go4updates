package markdown

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register the GIF decoder for image.Decode
	_ "image/jpeg" // register the JPEG decoder for image.Decode
	_ "image/png"  // register the PNG decoder for image.Decode
	"io"
	"net/http"
	"strings"

	"fyne.io/fyne/v2"
)

// errImageTooLarge reports an image body that exceeds the configured size
// limit, whether the size was known up front or discovered while reading.
var errImageTooLarge = errors.New("image exceeds the configured size limit")

const (
	// maxDecodedImageSide bounds one side of a declared image size before
	// the pixel count is compared, so that comparison cannot overflow.
	maxDecodedImageSide = 1<<15 - 1

	// maxDecodedImagePixels bounds the number of pixels a remote image may
	// decode to. The byte limit on the download does not bound the decoded
	// size: a highly compressed file can declare enormous dimensions, and
	// decoding it would allocate far beyond the limit. The bound is
	// independent of MaxImageWidth and MaxImageHeight, which only scale the
	// rendered result, and leaves room for any realistically sized
	// screenshot.
	maxDecodedImagePixels = 1 << 24 // 16.7 Mpx, about 64 MiB of RGBA pixels
)

// imageLoader carries the image loading policy of a single [NewRichText]
// call. It is not shared between calls and never stored globally.
type imageLoader struct {
	options Options
	client  *http.Client
}

// newImageLoader builds the loader for one set of options. A nil HTTP client
// is replaced by a client owned by the loader; download deadlines come from
// the request context rather than from the client.
func newImageLoader(options Options) *imageLoader {
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{}
	}

	return &imageLoader{options: options, client: client}
}

// allows reports whether the policy permits fetching source at all.
func (l *imageLoader) allows(source fyne.URI) bool {
	return l.options.RemoteImages && uriSchemeAllowed(source)
}

// blockedText returns the placeholder text explaining why source is not
// loaded. It returns the loading text when source may be loaded.
func (l *imageLoader) blockedText(source fyne.URI) string {
	if !l.options.RemoteImages {
		return textImageDisabled
	}

	if !uriSchemeAllowed(source) {
		return textImageUnsupported
	}

	return textImageLoading
}

// placeholder builds the box shown for an image that is loading, that failed
// to load, or that the policy keeps hidden.
func (l *imageLoader) placeholder(title, status string) fyne.CanvasObject {
	return newImagePlaceholder(l.options.MaxImageWidth, title, status)
}

// load fetches, decodes, and sizes one image. The download is bounded by the
// configured timeout and body limit, and decoding happens on bytes that are
// already in memory, so an image can never grow past the limit by being slow.
func (l *imageLoader) load(ctx context.Context, source fyne.URI) (*loadedImage, error) {
	ctx, cancel := context.WithTimeout(ctx, l.options.ImageLoadTimeout)
	defer cancel()

	data, err := l.fetch(ctx, source)
	if err != nil {
		return nil, err
	}

	return decodeImage(data, l.options.MaxImageWidth, l.options.MaxImageHeight)
}

// fetch downloads the body behind source.
//
// Only http and https locations reach this method. The body is read through a
// limiter so a server that under-reports or omits its size cannot grow the
// read past the configured limit, and a body known to be too large is never
// read at all.
func (l *imageLoader) fetch(ctx context.Context, source fyne.URI) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("building image request: %w", err)
	}

	response, err := l.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetching image: %w", err)
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching image: unexpected status %s", response.Status)
	}

	if response.ContentLength > l.options.MaxImageBytes {
		return nil, errImageTooLarge
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, l.options.MaxImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading image: %w", err)
	}

	if int64(len(data)) > l.options.MaxImageBytes {
		return nil, errImageTooLarge
	}

	return data, nil
}

// displaySize returns the size an image with config is rendered at: its own
// size, scaled down to fit inside the configured maximum when it is larger.
// Small images are never scaled up.
func displaySize(config image.Config, maxWidth, maxHeight float32) fyne.Size {
	width := float32(config.Width)
	height := float32(config.Height)
	if width <= 0 || height <= 0 {
		return fyne.NewSize(maxWidth, maxHeight)
	}

	scale := min(maxWidth/width, maxHeight/height)
	if scale >= 1 {
		return fyne.NewSize(width, height)
	}

	return fyne.NewSize(width*scale, height*scale)
}

// decodeImage decodes an image body twice on purpose: the metadata pass
// consumes its reader, so sizing and rendering each get their own reader over
// the same bytes. The declared size is validated before decoding, so an image
// cannot grow past the download limit by decompressing into a huge pixel
// buffer.
func decodeImage(data []byte, maxWidth, maxHeight float32) (*loadedImage, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("reading image size: %w", err)
	}

	if err := checkDimensions(config); err != nil {
		return nil, err
	}

	picture, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decoding image: %w", err)
	}

	return &loadedImage{picture: picture, size: displaySize(config, maxWidth, maxHeight)}, nil
}

// checkDimensions reports whether the declared size of an image is plausible
// and small enough to decode safely.
func checkDimensions(config image.Config) error {
	if config.Width < 1 || config.Height < 1 {
		return fmt.Errorf("image declares invalid dimensions %dx%d", config.Width, config.Height)
	}

	if config.Width > maxDecodedImageSide || config.Height > maxDecodedImageSide {
		return fmt.Errorf("image dimensions %dx%d exceed the limit of %d pixels per side", config.Width, config.Height, maxDecodedImageSide)
	}

	if config.Width*config.Height > maxDecodedImagePixels {
		return fmt.Errorf("image dimensions %dx%d exceed the limit of %d pixels", config.Width, config.Height, maxDecodedImagePixels)
	}

	return nil
}

// uriSchemeAllowed reports whether the scheme of source is an image scheme.
// Sources without an allowed scheme, including relative paths the Markdown
// parser turned into file URIs, are never allowed.
func uriSchemeAllowed(source fyne.URI) bool {
	if source == nil {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(source.Scheme())) {
	case "http", "https":
		return true

	default:
		return false
	}
}
