package update

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/releasefmt"
	"github.com/skobkin/go4updates/ui/fyne/markdown"
)

// Texts shown by the dialog.
const (
	dialogTitle        = "Software update"
	dismissLabel       = "Close"
	openReleaseLabel   = "Open release"
	installedLabel     = "Installed version: "
	latestLabel        = "Latest release: "
	unknownVersionText = "unknown"
	noNotesText        = "No release notes are available."

	textUpdateAvailable = "A newer release is available."
	textUpToDate        = "This installation is up to date."
	textStatusUnknown   = "No release information is available."
)

// Sizes of the scrollable release notes area.
const (
	changelogMinWidth  = float32(480)
	changelogMinHeight = float32(280)
)

// Options configures [NewDialog].
//
// The zero value renders the changelog from the raw release bodies with
// remote Markdown images disabled, and opens the release page through the
// running Fyne application.
type Options struct {
	// Markdown configures how the changelog Markdown is rendered. Remote
	// images stay disabled unless it says otherwise.
	Markdown markdown.Options

	// Format configures how the raw release bodies are turned into the
	// changelog before they are rendered.
	Format releasefmt.Options

	// OpenURL opens the release page. It receives the URL after the
	// fallback was applied. A nil function opens the URL through the
	// running Fyne application.
	OpenURL func(string) error
}

// dialogContent holds the parts of an update dialog a caller may want to
// inspect: the body it shows, the release button, and the URL that button
// opens after the fallback was applied.
type dialogContent struct {
	// root is the object shown as the dialog body.
	root fyne.CanvasObject

	// open is the release button. It is disabled when no release URL is
	// known.
	open *widget.Button

	// openURL is the URL the release button opens.
	openURL string
}

// NewDialog builds the update dialog for result over parent.
//
// The dialog shows the installed version, the latest version when the feed
// has one, a status line, and the scrollable release changelog. Its actions
// are Close and "Open release"; the latter opens the latest release page, or
// the release listing of the feed when the release has no page of its own,
// and is disabled when neither is known.
//
// An empty feed is a normal outcome and is presented as such: the dialog
// shows that no release information is available instead of failing. The
// dialog is not shown; call Show, or use [ShowDialog], to show it.
func NewDialog(parent fyne.Window, result updates.Result, opts Options) dialog.Dialog {
	content := newDialogContent(result, opts)

	releaseDialog := dialog.NewCustomWithoutButtons(dialogTitle, content.root, parent)
	releaseDialog.SetButtons([]fyne.CanvasObject{
		widget.NewButton(dismissLabel, releaseDialog.Hide),
		content.open,
	})

	return releaseDialog
}

// ShowDialog creates and shows an update dialog in one call.
func ShowDialog(parent fyne.Window, result updates.Result, opts Options) {
	NewDialog(parent, result, opts).Show()
}

// newDialogContent builds the dialog body and its release action.
func newDialogContent(result updates.Result, opts Options) dialogContent {
	content := dialogContent{
		root: container.NewVBox(
			newVersionHeader(result),
			newStatusLabel(result),
			newChangelogBody(result, opts),
		),
	}

	content.openURL = releaseURL(result)
	content.open = newOpenButton(content.openURL, opts)

	return content
}

// newVersionHeader builds the lines naming the installed and the latest
// version. The latest line is omitted when the feed has no release.
func newVersionHeader(result updates.Result) fyne.CanvasObject {
	installed := widget.NewLabel(installedLabel + displayVersion(result.CurrentVersion))
	installed.TextStyle = fyne.TextStyle{Bold: true}

	if result.Latest == nil {
		return installed
	}

	latest := widget.NewLabel(latestLabel + displayVersion(result.Latest.Version))

	return container.NewVBox(installed, latest)
}

// newStatusLabel builds the line describing the comparison outcome.
func newStatusLabel(result updates.Result) fyne.CanvasObject {
	status := widget.NewLabel(statusText(result))
	status.Importance = statusImportance(result.Status)
	status.Wrapping = fyne.TextWrapWord

	return status
}

// newChangelogBody builds the scrollable release notes, rendered from the raw
// release bodies. A feed without release bodies shows a short note instead of
// an empty scroll area.
func newChangelogBody(result updates.Result, opts Options) fyne.CanvasObject {
	changelog := releasefmt.Changelog(result.Feed.Releases, opts.Format)
	if changelog == "" {
		notes := widget.NewLabel(noNotesText)
		notes.Wrapping = fyne.TextWrapWord

		return notes
	}

	notes := markdown.NewRichText(changelog, opts.Markdown)
	notes.Wrapping = fyne.TextWrapWord

	body := container.NewVScroll(notes)
	body.SetMinSize(fyne.NewSize(changelogMinWidth, changelogMinHeight))

	return body
}

// newOpenButton builds the release button for rawURL, disabling it when no
// URL is known.
func newOpenButton(rawURL string, opts Options) *widget.Button {
	button := widget.NewButton(openReleaseLabel, func() {
		if err := open(rawURL, opts.OpenURL); err != nil {
			fyne.LogError("go4updates: ui/fyne/update: opening release page", err)
		}
	})

	button.Importance = widget.HighImportance
	if rawURL == "" {
		button.Disable()
	}

	return button
}

// releaseURL returns the page to open for result: the latest release page
// when the release has one, otherwise the release listing of the feed.
func releaseURL(result updates.Result) string {
	if result.Latest != nil {
		if release := strings.TrimSpace(result.Latest.URL); release != "" {
			return release
		}
	}

	return strings.TrimSpace(result.Feed.ReleasesURL)
}

// statusText describes the comparison outcome of result in one line.
func statusText(result updates.Result) string {
	switch result.Status {
	case updates.StatusUpdateAvailable:
		return textUpdateAvailable

	case updates.StatusUpToDate:
		return textUpToDate

	default:
		return textStatusUnknown
	}
}

// statusImportance returns how prominently the status line is shown.
func statusImportance(status updates.Status) widget.Importance {
	if status == updates.StatusUpdateAvailable {
		return widget.HighImportance
	}

	return widget.MediumImportance
}

// displayVersion returns version for display, naming the unknown case when it
// is empty.
func displayVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return unknownVersionText
	}

	return version
}
