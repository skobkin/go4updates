package markdown

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
)

func TestLazySegmentLoadsItsOwnVisual(t *testing.T) {
	skipUnderRace(t)

	newTestApp(t)

	server := newImageServer(t, testImage(t, formatPNG))
	rich := NewRichText(imageMarkdown(server.URL), Options{RemoteImages: true})
	segment := lazySegment(t, rich)

	first := segment.Visual()
	if containsImage(first) {
		t.Fatal("expected a placeholder before the image is loaded")
	}

	waitFor(t, func() bool { return containsImage(first) })
}

func TestLazySegmentKeepsRecycledVisualsClean(t *testing.T) {
	skipUnderRace(t)

	newTestApp(t)

	release := make(chan struct{})
	body := testImage(t, formatPNG)
	server := newCountingServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = writer.Write(body)
	})

	rich := NewRichText(imageMarkdown(server.URL), Options{RemoteImages: true})
	segment := lazySegment(t, rich)

	stale := segment.Visual()
	current := segment.Visual()

	if stale == current {
		t.Fatal("expected every visual call to build a fresh placeholder")
	}

	close(release)

	waitFor(t, func() bool { return containsImage(current) })

	if containsImage(stale) {
		t.Fatal("expected the replaced visual to stay a placeholder")
	}

	if segment.decoded == nil {
		t.Fatal("expected the loaded image to be kept for later visuals")
	}
}

func TestLazySegmentRendersACachedImageImmediately(t *testing.T) {
	skipUnderRace(t)

	newTestApp(t)

	server := newImageServer(t, testImage(t, formatPNG))
	rich := NewRichText(imageMarkdown(server.URL), Options{RemoteImages: true})
	segment := lazySegment(t, rich)

	render(t, rich)
	waitFor(t, func() bool { return segment.decoded != nil })

	again := segment.Visual()
	if !containsImage(again) {
		t.Fatal("expected the cached image to be rendered without another load")
	}

	if got := server.count(); got != 1 {
		t.Fatalf("expected the image to be loaded once, got %d requests", got)
	}
}

func TestLazySegmentReportsAFailedLoad(t *testing.T) {
	skipUnderRace(t)

	newTestApp(t)

	server := newImageServer(t, []byte("not an image"))
	rich := NewRichText(imageMarkdown(server.URL), Options{RemoteImages: true})
	segment := lazySegment(t, rich)

	visual := segment.Visual()
	waitFor(t, func() bool { return containsText(visual, textImageUnavailable) })

	if segment.decoded != nil {
		t.Fatal("expected no decoded image after a failed load")
	}
}

func TestLazySegmentImplementsRichTextSegment(t *testing.T) {
	origin := &widget.ImageSegment{
		Source:    storage.NewFileURI("logo.png"),
		Title:     "  Logo  ",
		Alignment: fyne.TextAlignTrailing,
	}

	segment := newLazyImageSegment(origin, newImageLoader(Options{}.withDefaults()))

	var asSegment widget.RichTextSegment = segment

	if asSegment.Inline() {
		t.Fatal("expected an image segment to be a block")
	}

	if got := asSegment.Textual(); got != "Image Logo" {
		t.Fatalf("expected %q, got %q", "Image Logo", got)
	}

	if got := asSegment.SelectedText(); got != "" {
		t.Fatalf("expected no selectable text, got %q", got)
	}

	if got := segment.label(); got != "Logo" {
		t.Fatalf("expected the trimmed title, got %q", got)
	}

	untitled := newLazyImageSegment(&widget.ImageSegment{Source: origin.Source}, segment.loader)
	if got := untitled.label(); got != placeholderLabel {
		t.Fatalf("expected the placeholder label, got %q", got)
	}

	// Selection and updates are no-ops, but they must not panic.
	asSegment.Update(container.NewWithoutLayout())
	asSegment.Select(fyne.Position{}, fyne.Position{})
	asSegment.Unselect()
}

func TestAlignedContainerPlacesTheContent(t *testing.T) {
	placeholder := newImagePlaceholder(DefaultMaxImageWidth, placeholderLabel, textImageLoading)

	tests := map[string]struct {
		alignment fyne.TextAlign
		slot      int
	}{
		"leading content":   {alignment: fyne.TextAlignLeading, slot: 0},
		"trailing content":  {alignment: fyne.TextAlignTrailing, slot: 1},
		"centered content":  {alignment: fyne.TextAlignCenter, slot: 1},
		"unknown alignment": {alignment: fyne.TextAlign(42), slot: 1},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			root, slot := alignedContainer(testCase.alignment, placeholder)

			if slot != testCase.slot {
				t.Fatalf("expected slot %d, got %d", testCase.slot, slot)
			}

			if root.Objects[slot] != placeholder {
				t.Fatalf("expected the placeholder in slot %d", slot)
			}
		})
	}
}

func TestOptionsDefaultToDocumentedValues(t *testing.T) {
	tests := map[string]struct {
		given    Options
		expected string
	}{
		"zero options": {
			given:    Options{},
			expected: "12s 1048576 560.000000 320.000000",
		},

		"negative options": {
			given: Options{
				ImageLoadTimeout: -time.Second,
				MaxImageBytes:    -1,
				MaxImageWidth:    -1,
				MaxImageHeight:   -1,
			},
			expected: "12s 1048576 560.000000 320.000000",
		},

		"configured options": {
			given: Options{
				ImageLoadTimeout: time.Second,
				MaxImageBytes:    128,
				MaxImageWidth:    64,
				MaxImageHeight:   32,
			},
			expected: "1s 128 64.000000 32.000000",
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			configured := testCase.given.withDefaults()

			got := fmt.Sprintf(
				"%s %d %f %f",
				configured.ImageLoadTimeout,
				configured.MaxImageBytes,
				configured.MaxImageWidth,
				configured.MaxImageHeight,
			)

			if got != testCase.expected {
				t.Fatalf("expected %q, got %q", testCase.expected, got)
			}
		})
	}
}
