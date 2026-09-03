package markdown

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

// smallImageSide is the side length of the images the tests encode.
const smallImageSide = 4

// newTestApp prepares a headless application for one test.
func newTestApp(t *testing.T) {
	t.Helper()

	test.NewTempApp(t)
}

// testImage encodes a small image in the given format.
func testImage(t *testing.T, format imageFormat) []byte {
	t.Helper()

	picture := image.NewNRGBA(image.Rect(0, 0, smallImageSide, smallImageSide))
	for y := range smallImageSide {
		for x := range smallImageSide {
			picture.Set(x, y, color.NRGBA{
				R: uint8(16 * x),
				G: uint8(16 * y),
				B: 96,
				A: 255,
			})
		}
	}

	var buffer bytes.Buffer

	var err error

	switch format {
	case formatJPEG:
		err = jpeg.Encode(&buffer, picture, nil)

	case formatGIF:
		err = gif.Encode(&buffer, picture, nil)

	default:
		err = png.Encode(&buffer, picture)
	}

	if err != nil {
		t.Fatalf("encoding test image: %v", err)
	}

	return buffer.Bytes()
}

// countingServer serves handler and records how many requests it received.
type countingServer struct {
	*httptest.Server

	mu       sync.Mutex
	requests int
}

// count returns the number of requests the server received so far.
func (s *countingServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.requests
}

// newCountingServer starts a server counting every request it serves. It is
// closed when the test ends.
func newCountingServer(t *testing.T, handler http.HandlerFunc) *countingServer {
	t.Helper()

	server := &countingServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		server.mu.Lock()
		server.requests++
		server.mu.Unlock()

		handler(writer, request)
	}))

	t.Cleanup(server.Close)

	return server
}

// newImageServer starts a server that always serves body as an image.
func newImageServer(t *testing.T, body []byte) *countingServer {
	t.Helper()

	return newCountingServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "image/png")
		_, _ = writer.Write(body)
	})
}

// render renders rich, which makes every segment build its visual.
func render(t *testing.T, rich *widget.RichText) []fyne.CanvasObject {
	t.Helper()

	return test.TempWidgetRenderer(t, rich).Objects()
}

// lazySegment returns the single image segment of rich.
func lazySegment(t *testing.T, rich *widget.RichText) *lazyImageSegment {
	t.Helper()

	segments := collectLazySegments(rich.Segments)
	if len(segments) != 1 {
		t.Fatalf("expected one image segment, found %d", len(segments))
	}

	return segments[0]
}

// collectLazySegments returns the lazy image segments found in segments,
// including the ones nested in container segments.
func collectLazySegments(segments []widget.RichTextSegment) []*lazyImageSegment {
	var found []*lazyImageSegment

	for _, segment := range segments {
		switch current := segment.(type) {
		case *lazyImageSegment:
			found = append(found, current)

		case *widget.ListSegment:
			found = append(found, collectLazySegments(current.Items)...)

		case *widget.ParagraphSegment:
			found = append(found, collectLazySegments(current.Texts)...)
		}
	}

	return found
}

// containsImage reports whether the object tree rooted at object shows an
// image.
func containsImage(object fyne.CanvasObject) bool {
	switch current := object.(type) {
	case *canvas.Image:
		return true

	case *fyne.Container:
		for _, child := range current.Objects {
			if containsImage(child) {
				return true
			}
		}
	}

	return false
}

// containsText reports whether the object tree rooted at object shows a label
// holding text.
func containsText(object fyne.CanvasObject, text string) bool {
	switch current := object.(type) {
	case *widget.Label:
		return current.Text == text

	case *fyne.Container:
		for _, child := range current.Objects {
			if containsText(child, text) {
				return true
			}
		}
	}

	return false
}

// segmentText returns the plain text a set of segments renders, recursing
// into the container segments a Markdown document is built from.
func segmentText(segments []widget.RichTextSegment) string {
	var builder strings.Builder

	for _, segment := range segments {
		if container, ok := segment.(widget.RichTextBlock); ok {
			builder.WriteString(segmentText(container.Segments()))

			continue
		}

		builder.WriteString(segment.Textual())
		builder.WriteString("\n")
	}

	return builder.String()
}

// skipUnderRace skips the tests that load an image into a rendered widget.
//
// Those tests are stable on their own, but the headless test driver runs
// fyne.Do callbacks inline on the loading goroutine instead of on a dedicated
// main thread. The mutation of the rendered container is therefore not
// serialized against the test goroutine building the widget renderer, which
// the race detector reports even though a real application serializes both on
// its main thread.
func skipUnderRace(t *testing.T) {
	t.Helper()

	if raceDetector {
		t.Skip("the headless Fyne driver runs fyne.Do inline, so background image loads cannot be serialized against the test goroutine")
	}
}

// loadDeadline is how long a test waits for a background image load. It is
// generous, because the loads are driven by an httptest server on localhost
// rather than by the network.
const loadDeadline = 10 * time.Second

// waitFor polls condition until it holds or the load deadline passes.
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(loadDeadline)

	for time.Now().Before(deadline) {
		if condition() {
			return
		}

		time.Sleep(2 * time.Millisecond)
	}

	t.Fatalf("timed out after %s waiting for a condition", loadDeadline)
}

// assertNever fails the test if condition becomes true within the window it
// gives any background work to run. It is used to prove that nothing happens.
func assertNever(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(50 * time.Millisecond)

	for time.Now().Before(deadline) {
		if condition() {
			t.Fatal("expected a condition to stay false, but it became true")

			return
		}

		time.Sleep(2 * time.Millisecond)
	}
}
