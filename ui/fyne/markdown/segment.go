package markdown

import (
	"context"
	"image"
	"image/color"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	// placeholderHeight is the height of the box shown while an image is
	// being loaded.
	placeholderHeight = float32(72)

	// placeholderBorderWidth is the stroke width of the placeholder border.
	placeholderBorderWidth = float32(1)

	// placeholderLabel is the description used when an image has no title.
	placeholderLabel = "Image"
)

// Placeholder texts. They double as the state a rendered segment is in.
const (
	textImageLoading     = "Loading image..."
	textImageDisabled    = "Remote images are disabled"
	textImageUnsupported = "Unsupported image link"
	textImageUnavailable = "Image unavailable"
)

// loadedImage is a decoded image together with the size it is rendered at.
type loadedImage struct {
	picture image.Image
	size    fyne.Size
}

// visual builds the canvas object showing the image.
func (l *loadedImage) visual() fyne.CanvasObject {
	picture := canvas.NewImageFromImage(l.picture)
	picture.FillMode = canvas.ImageFillContain
	picture.SetMinSize(l.size)

	return container.NewGridWrap(l.size, picture)
}

// lazyImageSegment is a rich text image segment that shows a placeholder
// immediately and loads the image in the background.
//
// It is created for a single [NewRichText] call and rendered by a single
// widget, so the background work it starts is owned by this segment alone.
type lazyImageSegment struct {
	// Source is the image location.
	Source fyne.URI

	// Title is a short human readable description of the image.
	Title string

	// Alignment is the horizontal alignment of the image inside its text.
	Alignment fyne.TextAlign

	loader *imageLoader

	// mu guards the rendering state below, and is held only for the short
	// stretches where that state or the placeholder contents change.
	mu      sync.Mutex
	visual  *fyne.Container
	decoded *loadedImage
}

// newLazyImageSegment replaces an image segment produced by the Markdown
// parser.
func newLazyImageSegment(origin *widget.ImageSegment, loader *imageLoader) *lazyImageSegment {
	return &lazyImageSegment{
		Source:    origin.Source,
		Title:     origin.Title,
		Alignment: origin.Alignment,
		loader:    loader,
	}
}

// Inline returns false: images are rendered as blocks.
func (s *lazyImageSegment) Inline() bool {
	return false
}

// Textual returns the plain text representation of the segment.
func (s *lazyImageSegment) Textual() string {
	return "Image " + strings.TrimSpace(s.Title)
}

// Visual returns the placeholder for the image and starts the background load
// when the loading policy allows it.
//
// Every call builds a fresh visual, so a widget that re-renders the segment
// gets a placeholder of its own and earlier background loads can no longer
// apply their result to it.
func (s *lazyImageSegment) Visual() fyne.CanvasObject {
	s.mu.Lock()
	defer s.mu.Unlock()

	root, slot := alignedContainer(s.Alignment, s.loader.placeholder(s.label(), s.loader.blockedText(s.Source)))
	s.visual = root

	if s.decoded != nil {
		root.Objects[slot] = s.decoded.visual()
		root.Refresh()

		return root
	}

	if s.loader.allows(s.Source) {
		go s.load(root, slot)
	}

	return root
}

// Update keeps an existing visual in sync with the segment. A visual keeps
// its own contents once built, so there is nothing to copy over.
func (s *lazyImageSegment) Update(fyne.CanvasObject) {}

// Select does nothing: images have no selectable text.
func (s *lazyImageSegment) Select(_, _ fyne.Position) {}

// SelectedText returns the empty string: images have no text.
func (s *lazyImageSegment) SelectedText() string {
	return ""
}

// Unselect does nothing: images have no selectable text.
func (s *lazyImageSegment) Unselect() {}

// load fetches and decodes the image and hands the outcome to the visual it
// was started for. It runs in its own goroutine. A failure is logged and
// reported through the placeholder the visual keeps showing.
func (s *lazyImageSegment) load(root *fyne.Container, slot int) {
	decoded, err := s.loader.load(context.Background(), s.Source)
	if err != nil {
		fyne.LogError("go4updates: ui/fyne/markdown: loading image", err)
	}

	fyne.Do(func() {
		s.apply(root, slot, decoded)
	})
}

// apply replaces the placeholder of root with the loaded image, or with a
// placeholder naming the failure when no image was decoded. It runs on the
// Fyne main thread and drops results for visuals that have been replaced in
// the meantime, so a recycled or discarded segment never receives a stale
// image.
func (s *lazyImageSegment) apply(root *fyne.Container, slot int, decoded *loadedImage) {
	s.mu.Lock()

	if s.visual != root || slot >= len(root.Objects) {
		// Keep a successful result: a later visual renders it immediately.
		s.decoded = decoded

		s.mu.Unlock()

		return
	}

	if decoded != nil {
		s.decoded = decoded
		root.Objects[slot] = decoded.visual()
	} else {
		root.Objects[slot] = s.loader.placeholder(s.label(), textImageUnavailable)
	}

	s.mu.Unlock()

	root.Refresh()
}

// label returns the description shown inside the placeholder.
func (s *lazyImageSegment) label() string {
	title := strings.TrimSpace(s.Title)
	if title == "" {
		return placeholderLabel
	}

	return title
}

// alignedContainer places object inside a container that aligns it according
// to alignment, and returns the container together with the slot holding the
// object.
func alignedContainer(alignment fyne.TextAlign, object fyne.CanvasObject) (*fyne.Container, int) {
	switch alignment {
	case fyne.TextAlignLeading:
		return container.NewHBox(object, layout.NewSpacer()), 0

	case fyne.TextAlignTrailing:
		return container.NewHBox(layout.NewSpacer(), object), 1

	default:
		return container.NewHBox(layout.NewSpacer(), object, layout.NewSpacer()), 1
	}
}

// newImagePlaceholder builds the box shown while an image is loading, when it
// failed to load, and when the policy keeps it hidden. It is width wide.
func newImagePlaceholder(width float32, title, status string) fyne.CanvasObject {
	titleLabel := widget.NewLabel(title)
	titleLabel.Alignment = fyne.TextAlignCenter
	titleLabel.Importance = widget.MediumImportance
	titleLabel.Truncation = fyne.TextTruncateEllipsis

	statusLabel := widget.NewLabel(status)
	statusLabel.Alignment = fyne.TextAlignCenter
	statusLabel.Importance = widget.LowImportance

	return container.NewGridWrap(
		fyne.NewSize(width, placeholderHeight),
		container.NewStack(
			placeholderBackground(),
			placeholderFrame(),
			container.NewPadded(container.NewVBox(
				container.NewCenter(titleLabel),
				container.NewCenter(statusLabel),
			)),
		),
	)
}

// placeholderFrame builds the outlined rectangle framing a placeholder.
func placeholderFrame() *canvas.Rectangle {
	frame := canvas.NewRectangle(color.Transparent)
	frame.StrokeColor = borderColor()
	frame.StrokeWidth = placeholderBorderWidth

	return frame
}

// placeholderBackground builds the filled rectangle behind a placeholder.
func placeholderBackground() *canvas.Rectangle {
	background := canvas.NewRectangle(placeholderColor())
	background.CornerRadius = 4

	return background
}

// placeholderColor returns a faint input background color, falling back to
// plain grey before an application exists.
func placeholderColor() color.Color {
	app := fyne.CurrentApp()
	if app == nil {
		return color.NRGBA{R: 0, G: 0, B: 0, A: 10}
	}

	background := toNRGBA(app.Settings().Theme().Color(theme.ColorNameInputBackground, app.Settings().ThemeVariant()))
	background.A = 55

	return background
}

// borderColor returns the placeholder border color, falling back to plain
// grey before an application exists.
func borderColor() color.Color {
	app := fyne.CurrentApp()
	if app == nil {
		return color.NRGBA{R: 120, G: 120, B: 120, A: 90}
	}

	border := toNRGBA(app.Settings().Theme().Color(theme.ColorNameForeground, app.Settings().ThemeVariant()))
	border.A = 80

	return border
}

// toNRGBA converts c into an 8-bit RGBA color so its alpha can be toned down.
func toNRGBA(c color.Color) color.NRGBA {
	red, green, blue, alpha := c.RGBA()

	//nolint:gosec // RGBA() returns 16-bit channels, so shifting them right by 8 yields the 8-bit value.
	return color.NRGBA{
		R: uint8(red >> 8),
		G: uint8(green >> 8),
		B: uint8(blue >> 8),
		A: uint8(alpha >> 8),
	}
}
