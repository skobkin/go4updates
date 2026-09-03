package update

import (
	"errors"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	updates "github.com/skobkin/go4updates"
)

func TestNewDialogShowsVersionsAndStatus(t *testing.T) {
	newTestApp(t)

	result := populatedResult()
	content := newDialogContent(result, Options{})

	texts := strings.Join(labelTexts(content.root), "\n")

	for _, expected := range []string{
		installedLabel + "1.0.0",
		latestLabel + "1.2.0",
		textUpdateAvailable,
	} {
		if !strings.Contains(texts, expected) {
			t.Fatalf("expected the dialog to show %q, got:\n%s", expected, texts)
		}
	}

	if got := content.openURL; got != result.Latest.URL {
		t.Fatalf("expected the release URL %q, got %q", result.Latest.URL, got)
	}

	if content.open.Disabled() {
		t.Fatal("expected the release button to be enabled")
	}

	notes := changelogText(t, content)
	for _, expected := range []string{"1.2.0", "Fixed a crash", "1.1.0"} {
		if !strings.Contains(notes, expected) {
			t.Fatalf("expected the changelog to contain %q, got:\n%s", expected, notes)
		}
	}
}

func TestNewDialogHandlesAnEmptyFeed(t *testing.T) {
	newTestApp(t)

	content := newDialogContent(updates.Result{CurrentVersion: "2.0.0"}, Options{})
	texts := strings.Join(labelTexts(content.root), "\n")

	for _, expected := range []string{
		installedLabel + "2.0.0",
		textStatusUnknown,
		noNotesText,
	} {
		if !strings.Contains(texts, expected) {
			t.Fatalf("expected the dialog to show %q, got:\n%s", expected, texts)
		}
	}

	if strings.Contains(texts, latestLabel) {
		t.Fatalf("expected no latest version line, got:\n%s", texts)
	}

	if !content.open.Disabled() {
		t.Fatal("expected the release button to be disabled without a URL")
	}

	if content.openURL != "" {
		t.Fatalf("expected no release URL, got %q", content.openURL)
	}
}

func TestNewDialogHandlesAnUpToDateResult(t *testing.T) {
	newTestApp(t)

	result := populatedResult()
	result.Status = updates.StatusUpToDate
	result.CurrentVersion = "1.2.0"

	content := newDialogContent(result, Options{})
	texts := strings.Join(labelTexts(content.root), "\n")

	if !strings.Contains(texts, textUpToDate) {
		t.Fatalf("expected the up to date text, got:\n%s", texts)
	}
}

func TestNewDialogOpensTheReleasePage(t *testing.T) {
	newTestApp(t)

	tests := map[string]struct {
		result   updates.Result
		expected string
	}{
		"latest release URL": {
			result:   populatedResult(),
			expected: "https://example.com/acme/app/releases/v1.2.0",
		},

		"feed releases URL": {
			result:   withoutReleaseURL(populatedResult()),
			expected: "https://example.com/acme/app/releases",
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			var opened []string

			content := newDialogContent(testCase.result, Options{
				OpenURL: func(url string) error {
					opened = append(opened, url)

					return nil
				},
			})

			content.open.OnTapped()

			if len(opened) != 1 {
				t.Fatalf("expected one opened URL, got %v", opened)
			}

			if opened[0] != testCase.expected {
				t.Fatalf("expected %q to be opened, got %q", testCase.expected, opened[0])
			}
		})
	}
}

func TestNewDialogDisablesTheReleaseButtonWithoutAURL(t *testing.T) {
	newTestApp(t)

	result := populatedResult()
	result.Feed.ReleasesURL = ""
	result.Latest.URL = ""

	var opened []string

	content := newDialogContent(result, Options{
		OpenURL: func(url string) error {
			opened = append(opened, url)

			return nil
		},
	})

	if !content.open.Disabled() {
		t.Fatal("expected the release button to be disabled")
	}

	content.open.Tapped(&fyne.PointEvent{})

	if len(opened) != 0 {
		t.Fatalf("expected no URL to be opened, got %v", opened)
	}
}

func TestOpenReportsTheHookFailure(t *testing.T) {
	failure := errors.New("no browser")

	hook := func(string) error { return failure }

	if !errors.Is(open("https://example.com/releases", hook), failure) {
		t.Fatal("expected the open failure to be reported")
	}
}

func TestOpenFallsBackToTheFyneApplication(t *testing.T) {
	newTestApp(t)

	if err := open("https://example.com/releases", nil); err != nil {
		t.Fatalf("expected the Fyne opener to be used, got %v", err)
	}
}

func TestOpenRejectAnUnparsableURL(t *testing.T) {
	newTestApp(t)

	if err := open("://example.com", nil); err == nil {
		t.Fatal("expected an error for an unparsable URL")
	}
}

func TestNewDialogCanBeShown(t *testing.T) {
	newTestApp(t)

	window := test.NewWindow(nil)

	releaseDialog := NewDialog(window, populatedResult(), Options{})
	releaseDialog.Show()

	if top := window.Canvas().Overlays().Top(); top == nil {
		t.Fatal("expected the dialog to be shown as an overlay")
	}

	releaseDialog.Hide()

	if top := window.Canvas().Overlays().Top(); top != nil {
		t.Fatal("expected the dialog to be hidden")
	}
}

func TestNewDialogWorksWithZeroOptions(t *testing.T) {
	newTestApp(t)

	window := test.NewWindow(nil)

	releaseDialog := NewDialog(window, populatedResult(), Options{})
	releaseDialog.Show()

	content := newDialogContent(populatedResult(), Options{})
	content.open.OnTapped()

	releaseDialog.Hide()
}

func TestDisplayVersionNamesTheUnknownCase(t *testing.T) {
	tests := map[string]struct {
		given    string
		expected string
	}{
		"version":        {given: "1.2.3", expected: "1.2.3"},
		"padded version": {given: "  v1.2.3 ", expected: "v1.2.3"},
		"empty version":  {given: "", expected: unknownVersionText},
		"blank version":  {given: "   ", expected: unknownVersionText},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			if got := displayVersion(testCase.given); got != testCase.expected {
				t.Fatalf("expected %q, got %q", testCase.expected, got)
			}
		})
	}
}

func TestStatusTextDescribesTheOutcome(t *testing.T) {
	tests := map[string]struct {
		status   updates.Status
		expected string
	}{
		"update available": {status: updates.StatusUpdateAvailable, expected: textUpdateAvailable},
		"up to date":       {status: updates.StatusUpToDate, expected: textUpToDate},
		"unknown":          {status: updates.StatusUnknown, expected: textStatusUnknown},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			result := updates.Result{Status: testCase.status}
			if got := statusText(result); got != testCase.expected {
				t.Fatalf("expected %q, got %q", testCase.expected, got)
			}

			content := newStatusLabel(result)
			labels := labelTexts(content)
			if len(labels) != 1 || labels[0] != testCase.expected {
				t.Fatalf("expected the status label to show %q, got %v", testCase.expected, labels)
			}
		})
	}
}

func TestReleaseURLFallsBackToTheFeed(t *testing.T) {
	tests := map[string]struct {
		result   updates.Result
		expected string
	}{
		"release URL wins": {
			result:   populatedResult(),
			expected: "https://example.com/acme/app/releases/v1.2.0",
		},

		"feed URL is the fallback": {
			result:   withoutReleaseURL(populatedResult()),
			expected: "https://example.com/acme/app/releases",
		},

		"no URL stays empty": {
			result: func() updates.Result {
				result := populatedResult()
				result.Feed.ReleasesURL = ""
				result.Latest.URL = ""

				return result
			}(),
			expected: "",
		},

		"no release at all": {
			result:   updates.Result{Feed: updates.Feed{ReleasesURL: "https://example.com/releases"}},
			expected: "https://example.com/releases",
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			if got := releaseURL(testCase.result); got != testCase.expected {
				t.Fatalf("expected %q, got %q", testCase.expected, got)
			}
		})
	}
}

// populatedResult returns a result describing an available update.
func populatedResult() updates.Result {
	latest := updates.Release{
		Version: "1.2.0",
		Title:   "Winter release",
		Body:    "Fixed a crash on startup.",
		URL:     "https://example.com/acme/app/releases/v1.2.0",
	}

	return updates.Result{
		CurrentVersion: "1.0.0",
		Status:         updates.StatusUpdateAvailable,
		Feed: updates.Feed{
			RepositoryURL: "https://example.com/acme/app",
			ReleasesURL:   "https://example.com/acme/app/releases",
			Releases: []updates.Release{
				latest,
				{Version: "1.1.0", Body: "Second beta. Improved start-up time."},
			},
		},
		Latest: &latest,
	}
}

// withoutReleaseURL removes the release page of the newest release.
func withoutReleaseURL(result updates.Result) updates.Result {
	latest := *result.Latest
	latest.URL = ""
	result.Latest = &latest

	return result
}

// newTestApp prepares a headless application for one test.
func newTestApp(t *testing.T) {
	t.Helper()

	test.NewTempApp(t)
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

// changelogText returns the plain text of the scrollable changelog.
func changelogText(t *testing.T, content dialogContent) string {
	t.Helper()

	var notes *widget.RichText

	var visit func(object fyne.CanvasObject)

	visit = func(object fyne.CanvasObject) {
		switch current := object.(type) {
		case *widget.RichText:
			notes = current

		case *container.Scroll:
			visit(current.Content)

		case *fyne.Container:
			for _, child := range current.Objects {
				visit(child)
			}
		}
	}

	visit(content.root)

	if notes == nil {
		t.Fatal("expected the dialog body to hold a rendered changelog")
	}

	var builder strings.Builder

	for _, segment := range notes.Segments {
		builder.WriteString(segment.Textual())
		builder.WriteString("\n")
	}

	return builder.String()
}
