package markdown

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2/widget"
)

func TestNewRichTextParsesSegments(t *testing.T) {
	newTestApp(t)

	body := "# Heading\n\nA paragraph.\n\n```go\nfmt.Println(\"hi\")\n```\n\n- first\n- second\n"

	rich := NewRichText(body, Options{})

	if len(rich.Segments) == 0 {
		t.Fatal("expected the parsed Markdown to produce segments")
	}

	text := segmentText(rich.Segments)
	for _, expected := range []string{"Heading", "A paragraph.", "fmt.Println(\"hi\")", "first", "second"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected the rendered text to contain %q, got:\n%s", expected, text)
		}
	}
}

func TestNewRichTextRewritesImageSegments(t *testing.T) {
	newTestApp(t)

	body := "![logo](https://example.com/logo.png)\n\n- ![nested](https://example.com/nested.png)\n"

	rich := NewRichText(body, Options{})

	if segments := collectLazySegments(rich.Segments); len(segments) != 2 {
		t.Fatalf("expected two lazily loaded image segments, found %d", len(segments))
	}

	if containsPlainImageSegment(rich.Segments) {
		t.Fatal("expected every parsed image segment to be replaced")
	}
}

// containsPlainImageSegment reports whether an image segment produced by the
// Markdown parser is still present.
func containsPlainImageSegment(segments []widget.RichTextSegment) bool {
	for _, segment := range segments {
		switch current := segment.(type) {
		case *widget.ImageSegment:
			return true

		case *widget.ListSegment:
			if containsPlainImageSegment(current.Items) {
				return true
			}

		case *widget.ParagraphSegment:
			if containsPlainImageSegment(current.Texts) {
				return true
			}
		}
	}

	return false
}
