package update

import (
	"errors"
	"fmt"
	"net/url"

	"fyne.io/fyne/v2"
)

// errNoApplication reports an attempt to open a URL before any Fyne
// application was created.
var errNoApplication = errors.New("no running Fyne application to open the release URL")

// open opens rawURL with opener, falling back to the running Fyne
// application when no opener is configured.
func open(rawURL string, opener func(string) error) error {
	if opener != nil {
		return opener(rawURL)
	}

	return openWithFyne(rawURL)
}

// openWithFyne opens rawURL through the running Fyne application.
func openWithFyne(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("go4updates: ui/fyne/update: parsing release URL %q: %w", rawURL, err)
	}

	app := fyne.CurrentApp()
	if app == nil {
		return errNoApplication
	}

	return app.OpenURL(parsed)
}
