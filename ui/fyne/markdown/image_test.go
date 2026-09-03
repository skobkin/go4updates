package markdown

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
)

// imageFormat enumerates the image encodings the tests exercise.
type imageFormat int

// The image encodings the standard library decoders are registered for.
const (
	formatPNG imageFormat = iota
	formatJPEG
	formatGIF
)

func TestNewRichTextRemoteImagesDisabledLoadsNothing(t *testing.T) {
	newTestApp(t)

	server := newImageServer(t, testImage(t, formatPNG))
	rich := NewRichText(imageMarkdown(server.URL), Options{})
	segment := lazySegment(t, rich)

	objects := render(t, rich)
	if len(objects) == 0 {
		t.Fatal("expected the rendered rich text to hold objects")
	}

	if segment.decoded != nil {
		t.Fatal("expected no image to be decoded while remote images are disabled")
	}

	if got := statusTextOf(segment); got != textImageDisabled {
		t.Fatalf("expected the %q placeholder, got %q", textImageDisabled, got)
	}

	assertNever(t, func() bool { return server.count() > 0 })
}

func TestNewRichTextDisallowedSchemesLoadNothing(t *testing.T) {
	newTestApp(t)

	server := newImageServer(t, testImage(t, formatPNG))

	sources := []string{
		"file:///etc/passwd",
		"ftp://example.com/logo.png",
		"data:image/png;base64,iVBORw0KGgo=",
		"logo.png",
		"./logo.png",
	}

	for _, source := range sources {
		rich := NewRichText(imageMarkdown(source), Options{RemoteImages: true})
		segment := lazySegment(t, rich)
		render(t, rich)

		if segment.decoded != nil {
			t.Fatalf("expected no image to be decoded for %q", source)
		}

		if got := statusTextOf(segment); got != textImageUnsupported {
			t.Fatalf("expected the %q placeholder for %q, got %q", textImageUnsupported, source, got)
		}
	}

	assertNever(t, func() bool { return server.count() > 0 })
}

func TestNewRichTextLoadsRemoteImage(t *testing.T) {
	skipUnderRace(t)

	newTestApp(t)

	server := newImageServer(t, testImage(t, formatPNG))
	body := fmt.Sprintf(
		"# Release\n\n![logo](%s/logo.png)\n\n```go\nfmt.Println(\"hi\")\n```\n",
		server.URL,
	)

	rich := NewRichText(body, Options{RemoteImages: true})
	segment := lazySegment(t, rich)
	render(t, rich)

	waitFor(t, func() bool { return containsImage(segment.visual) })

	if segment.decoded == nil {
		t.Fatal("expected the image to be decoded")
	}

	if got := segment.decoded.size; got.Width != smallImageSide || got.Height != smallImageSide {
		t.Fatalf("expected the image at its own size, got %vx%v", got.Width, got.Height)
	}

	if !strings.Contains(segmentText(rich.Segments), "fmt.Println") {
		t.Fatal("expected the fenced code block to survive rendering")
	}

	if got := server.count(); got != 1 {
		t.Fatalf("expected exactly one image request, got %d", got)
	}
}

func TestNewRichTextDecodesRegisteredFormats(t *testing.T) {
	skipUnderRace(t)

	newTestApp(t)

	for _, format := range []imageFormat{formatPNG, formatJPEG, formatGIF} {
		server := newImageServer(t, testImage(t, format))

		rich := NewRichText(imageMarkdown(server.URL), Options{RemoteImages: true})
		segment := lazySegment(t, rich)
		render(t, rich)

		waitFor(t, func() bool {
			return segment.decoded != nil && containsImage(segment.visual)
		})
	}
}

func TestNewRichTextRejectsOversizedContentLength(t *testing.T) {
	skipUnderRace(t)

	newTestApp(t)

	body := testImage(t, formatPNG)
	server := newCountingServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		_, _ = writer.Write(body)
	})

	rich := NewRichText(imageMarkdown(server.URL), Options{
		RemoteImages:  true,
		MaxImageBytes: 8,
	})

	segment := lazySegment(t, rich)
	render(t, rich)

	waitFor(t, func() bool {
		return statusTextOf(segment) == textImageUnavailable
	})

	if segment.decoded != nil {
		t.Fatal("expected the oversized image to be rejected without decoding")
	}

	if got := server.count(); got != 1 {
		t.Fatalf("expected the header check to need exactly one request, got %d", got)
	}
}

func TestNewRichTextRejectsOversizedBody(t *testing.T) {
	skipUnderRace(t)

	newTestApp(t)

	body := testImage(t, formatPNG)
	server := newCountingServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		// Writing in two flushed chunks keeps the length unknown, so the
		// read itself has to discover that the body is too large.
		_, _ = writer.Write(body[:len(body)/2])

		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}

		_, _ = writer.Write(body[len(body)/2:])
	})

	rich := NewRichText(imageMarkdown(server.URL), Options{
		RemoteImages:  true,
		MaxImageBytes: 8,
	})

	segment := lazySegment(t, rich)
	render(t, rich)

	waitFor(t, func() bool {
		return statusTextOf(segment) == textImageUnavailable
	})

	if segment.decoded != nil {
		t.Fatal("expected the oversized image to be rejected without decoding")
	}
}

func TestNewRichTextHonorsImageTimeout(t *testing.T) {
	skipUnderRace(t)

	newTestApp(t)

	server := newCountingServer(t, func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-time.After(5 * time.Second):

		case <-request.Context().Done():
		}
	})

	rich := NewRichText(imageMarkdown(server.URL), Options{
		RemoteImages:     true,
		ImageLoadTimeout: 100 * time.Millisecond,
	})

	segment := lazySegment(t, rich)
	render(t, rich)

	waitFor(t, func() bool {
		return statusTextOf(segment) == textImageUnavailable
	})

	if segment.decoded != nil {
		t.Fatal("expected the timed out image to stay unloaded")
	}
}

func TestLoadRejectsUnexpectedStatus(t *testing.T) {
	server := newCountingServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
	})

	loader := newImageLoader(Options{RemoteImages: true}.withDefaults())
	source := testURI(t, server.URL+"/logo.png")

	if _, err := loader.load(context.Background(), source); err == nil {
		t.Fatal("expected an error for a non-2xx response")
	}
}

func TestLoadRejectsUndecodableBody(t *testing.T) {
	server := newImageServer(t, []byte("definitely not an image"))

	loader := newImageLoader(Options{RemoteImages: true}.withDefaults())
	source := testURI(t, server.URL+"/logo.png")

	if _, err := loader.load(context.Background(), source); err == nil {
		t.Fatal("expected an error for a body that is not an image")
	}
}

func TestLoadReportsImagesPastTheLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", "999999")
	}))

	defer server.Close()

	loader := newImageLoader(Options{RemoteImages: true, MaxImageBytes: 4}.withDefaults())
	source := testURI(t, server.URL+"/logo.png")

	_, err := loader.load(context.Background(), source)
	if !errors.Is(err, errImageTooLarge) {
		t.Fatalf("expected errImageTooLarge, got %v", err)
	}
}

func TestDisplaySizeFitsTheConfiguredLimits(t *testing.T) {
	tests := map[string]struct {
		width         int
		height        int
		expectedWidth float32
		expected      float32
	}{
		"small image keeps its size": {
			width: 4, height: 3,
			expectedWidth: 4, expected: 3,
		},

		"wide image fits the width": {
			width: 1200, height: 800,
			expectedWidth: 480, expected: 320,
		},

		"tall image fits the height": {
			width: 800, height: 1200,
			expectedWidth: 213.33334, expected: 320,
		},

		"unknown size falls back to the limits": {
			width: 0, height: 0,
			expectedWidth: 560, expected: 320,
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			config := image.Config{Width: testCase.width, Height: testCase.height}
			size := displaySize(config, DefaultMaxImageWidth, DefaultMaxImageHeight)

			assertClose(t, testCase.expectedWidth, size.Width, "width")
			assertClose(t, testCase.expected, size.Height, "height")
		})
	}
}

// assertClose fails the test when got differs from expected by more than a
// hundredth, which keeps float32 rounding out of the assertions.
func assertClose(t *testing.T, expected, got float32, name string) {
	t.Helper()

	if math.Abs(float64(got-expected)) > 0.01 {
		t.Fatalf("expected %s %f, got %f", name, expected, got)
	}
}

// imageMarkdown returns a Markdown document referencing url as an image.
func imageMarkdown(url string) string {
	return fmt.Sprintf("![logo](%s/logo.png)", url)
}

// testURI parses url into the URI the Markdown parser produces.
func testURI(t *testing.T, url string) fyne.URI {
	t.Helper()

	source, err := storage.ParseURI(url)
	if err != nil {
		t.Fatalf("parsing test URI: %v", err)
	}

	return source
}

// statusTextOf returns the placeholder status text a segment shows.
func statusTextOf(segment *lazyImageSegment) string {
	for _, text := range labelTexts(segment.visual) {
		switch text {
		case textImageLoading, textImageDisabled, textImageUnsupported, textImageUnavailable:
			return text
		}
	}

	return ""
}

// labelTexts returns the texts of the labels in the tree rooted at object.
func labelTexts(object fyne.CanvasObject) []string {
	switch current := object.(type) {
	case *widget.Label:
		return []string{current.Text}

	case *fyne.Container:
		var texts []string

		for _, child := range current.Objects {
			texts = append(texts, labelTexts(child)...)
		}

		return texts
	}

	return nil
}
