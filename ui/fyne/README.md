# go4updates/ui/fyne

Fyne integration for [go4updates](https://github.com/skobkin/go4updates),
provided as a separate Go module so the core library stays free of GUI
dependencies.

Two packages:

- [`markdown`](markdown/) — generic Markdown renderer for Fyne `widget.RichText`,
  reusable independently of update checking. Remote images are disabled by
  default; when explicitly enabled, only HTTP(S) sources are fetched, under
  configurable size and time limits.
- [`update`](update/) — a ready-made "update available" dialog that renders
  release notes through `releasefmt` and the `markdown` package.

## Installation

Requires Go 1.25 or newer and
[`github.com/skobkin/go4updates`](https://github.com/skobkin/go4updates), which
is added automatically as a dependency:

```sh
go get github.com/skobkin/go4updates/ui/fyne
```

## Usage

```go
import (
    fyneupdate "github.com/skobkin/go4updates/ui/fyne/update"
)

// Show the dialog in one call, or use NewDialog for custom presentation.
fyneupdate.ShowDialog(window, result, fyneupdate.Options{})
```

`result` is an `updates.Result` produced by the core library — see the
[root README](https://github.com/skobkin/go4updates#readme) for checking
releases and watching multiple targets, and the package documentation for the
full option sets of both packages.

## License

MIT — see [LICENSE](LICENSE).
