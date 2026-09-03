// Package update presents a release check result in a Fyne dialog.
//
// [NewDialog] builds a dialog from an updates.Result: it names the installed
// and the latest version, renders the release changelog as Markdown through
// the [markdown] package, and offers an "Open release" action next to a plain
// close action.
//
//	update.ShowDialog(window, result, update.Options{
//		Markdown: markdown.Options{RemoteImages: true},
//		Format:   releasefmt.Options{ShortCommitSHA: true},
//	})
//
// The dialog only renders what it is given: it never performs a check, never
// subscribes to a watcher, and never downloads release assets. The open
// action opens the release page through Options.OpenURL, or through the
// running Fyne application when no opener is configured.
package update
