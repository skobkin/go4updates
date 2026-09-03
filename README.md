# go4updates [![CI](https://ci.skobk.in/api/badges/24/status.svg)](https://ci.skobk.in/repos/24)

`go4updates` is a small, composable Go library for checking software releases.

It is designed for applications that need to:

- fetch releases from GitHub, Forgejo, or GitLab;
- compare the latest upstream release with the currently running version;
- perform one-shot checks or periodically watch multiple independently-versioned targets;
- keep update-checking logic independent from GUI/TUI frameworks;
- render release notes through optional presentation helpers;
- consume release asset metadata without forcing a particular download or self-update workflow.

The root module intentionally does **not** depend on Fyne or another UI framework.
UI integrations live in separate nested Go modules, so a headless service or TUI
can use the core library without pulling GUI dependencies into its module graph.

## Design

The library separates responsibilities into small layers:

```text
source adapter
    ↓
Checker.Check(...)
    ↓
Result
    │
    ├── direct consumer (CLI/server)
    ├── watch.Watcher
    │      ↓
    │    State / Event
    ├── releasefmt
    └── optional UI integration
```

The checker does not start background goroutines. The watcher uses the checker
for periodic checks. UI packages consume completed results or watcher events and
do not own update scheduling.

Release descriptions are kept as raw upstream Markdown in core types.
Presentation-specific linkification or formatting is applied separately.

## Modules and packages

The repository uses a multi-module layout:

```text
github.com/skobkin/go4updates
├── updates              # root package
├── source/github
├── source/forgejo
├── source/gitlab
├── watch
├── releasefmt
└── ui/fyne              # separate Go module
    ├── markdown
    └── update
```

Using the root module does not add Fyne to the consumer's dependencies.

## Core concepts

A **source** fetches normalized release information from an upstream hosting
service.

A **target** combines a source with the application's current version and
comparison/fetch policy.

A **checker** performs one or more one-shot checks.

A **watcher** periodically invokes the checker for registered targets and keeps
the most recent successful result together with information about the latest
attempt.

A **result** contains the current version, upstream feed, latest release when
one exists, comparison status, and check timestamp.

Release metadata includes upstream asset information, but v0.1 intentionally
does not download, install, verify, or roll back artifacts.

## One-shot check

A CLI or headless program that only needs to check its own version does not need
the watcher package:

```go
package main

import (
    "context"
    "fmt"
    "log"

    updates "github.com/skobkin/go4updates"
    "github.com/skobkin/go4updates/source/github"
)

func main() {
    src, err := github.New("example/example-app")
    if err != nil {
        log.Fatal(err)
    }

    checker := updates.NewChecker(updates.CheckerOptions{})

    result, err := checker.Check(context.Background(), updates.Target{
        CurrentVersion: "v1.4.0",
        Source:         src,
        Fetch: updates.FetchOptions{
            Limit: 10,
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    switch result.Status {
    case updates.StatusUpdateAvailable:
        fmt.Printf("update available: %s\n", result.Latest.Version)
    case updates.StatusUpToDate:
        fmt.Println("up to date")
    default:
        fmt.Println("unable to compare versions")
    }
}
```

The default comparator accepts semantic versions both with and without a leading
`v`, for example `1.4.0` and `v1.4.0`.

## Periodic check for a desktop application

A desktop application may want a single background target, while keeping its own
application event bus and UI lifecycle:

```go
src, err := forgejo.New(
    "https://git.example.org",
    "team/desktop-app",
)
if err != nil {
    return err
}

checker := updates.NewChecker(updates.CheckerOptions{})

w := watch.New(checker, watch.Options{
    Interval: 12 * time.Hour,
    Timeout:  15 * time.Second,
})

err = w.Add("application", updates.Target{
    CurrentVersion: buildVersion,
    Source:         src,
    Fetch: updates.FetchOptions{
        Limit: 15,
    },
})
if err != nil {
    return err
}

events, unsubscribe := w.Subscribe()
defer unsubscribe()

go func() {
    for event := range events {
        // Bridge the generic watcher event into the application's own
        // event bus, notification service, GUI thread, etc.
        appEvents.Publish(event)
    }
}()

go func() {
    if err := w.Run(ctx); err != nil {
        logger.Error("update watcher stopped", "error", err)
    }
}()
```

The library does not require the application to replace its existing message bus
or UI event model.

## Watching multiple components

A server/web application may expose update information for several independently
versioned components:

```go
w := watch.New(checker, watch.Options{
    Interval: 12 * time.Hour,
    Timeout:  15 * time.Second,
})

_ = w.Add("server", serverTarget)

_ = w.Add(
    "device-firmware",
    firmwareTarget,
    watch.WithInterval(24*time.Hour),
)

go func() {
    if err := w.Run(ctx); err != nil {
        logger.Error("update watcher stopped", "error", err)
    }
}()

states := w.States()
```

Each target has its own ID and may override the watcher's default interval.

A failed refresh does not have to erase previously successful information.
Watcher state keeps the last successful result together with `LastAttemptAt` and
`LastError`, allowing a UI or API to show stale-but-useful data while also
reporting that the latest refresh failed.

## Custom HTTP client

Source adapters accept a caller-provided `*http.Client` instead of implementing
their own proxy, authentication, TLS, or tracing configuration:

```go
transport := http.DefaultTransport.(*http.Transport).Clone()

client := &http.Client{
    Transport: transport,
    Timeout:   20 * time.Second,
}

src, err := gitlab.New(
    "team/component",
    gitlab.WithServerURL("https://git.example.org"),
    gitlab.WithHTTPClient(client),
)
```

Consumers can use a custom `RoundTripper` for authentication headers, proxies,
mTLS, tracing, custom CA behavior, or other network requirements.

## Custom version comparison

Semantic Versioning is the default policy, but applications may provide their
own comparator:

```go
type BuildNumberComparator struct{}

func (BuildNumberComparator) Compare(current, latest string) updates.Status {
    currentBuild, currentErr := strconv.Atoi(strings.TrimSpace(current))
    latestBuild, latestErr := strconv.Atoi(strings.TrimSpace(latest))

    // Build numbers that are missing or unreadable cannot be ordered.
    if currentErr != nil || latestErr != nil || currentBuild == latestBuild {
        return updates.StatusUnknown
    }

    if currentBuild < latestBuild {
        return updates.StatusUpdateAvailable
    }

    return updates.StatusUpToDate
}

result, err := checker.Check(ctx, updates.Target{
    CurrentVersion: currentBuild,
    Source:         src,
    Comparator:     BuildNumberComparator{},
})
```

The checker stores and returns raw version strings; normalization required by a
comparison policy belongs to the comparator.

## Release assets

Release metadata includes assets exposed by the provider:

```go
for _, asset := range result.Latest.Assets {
    fmt.Printf("%s (%d bytes): %s\n", asset.Name, asset.Size, asset.URL)
}
```

The core library does not download these assets in v0.1. This is deliberate:
different consumers may want to write an artifact to a chosen path, stream it to
another service, verify it while streaming, or implement an application-specific
installer.

Download and verification APIs should be designed independently rather than
forcing filesystem-oriented behavior into release checking.

## Release-note formatting

`releasefmt` contains framework-independent presentation helpers. It may build a
multi-release Markdown changelog and linkify provider-specific commit, issue, or
mention references without mutating the raw release body stored by core types.

```go
text := releasefmt.Changelog(
    result.Feed.Releases,
    releasefmt.Options{
        Linker: releasefmt.ForgejoLinker(result.Feed.RepositoryURL),
        ShortCommitSHA: true,
    },
)
```

Formatting helpers must avoid rewriting fenced code, inline code, existing links,
and images.

## Fyne integration

Fyne support is provided by a separate module:

```text
github.com/skobkin/go4updates/ui/fyne
```

A Fyne application can import the update dialog without making Fyne a dependency
of the root `go4updates` module:

```go
import (
    fyneupdate "github.com/skobkin/go4updates/ui/fyne/update"
)

// ShowDialog creates and shows the dialog in one call.
fyneupdate.ShowDialog(window, result, fyneupdate.Options{})

// NewDialog returns the dialog instead, for custom presentation.
d := fyneupdate.NewDialog(window, result, fyneupdate.Options{})
d.Show()
```

The dialog renders the changelog through `releasefmt` and the generic Markdown
renderer, so its presentation is customizable through `Options.Format` and
`Options.Markdown`.

The generic Fyne Markdown renderer is available separately under
`ui/fyne/markdown`, so applications can reuse it without using the update dialog.

Remote Markdown images are disabled by default. When explicitly enabled, the
renderer only permits HTTP(S) image sources and applies configured size/time
limits.

## Non-goals for v0.1

The first version intentionally does not include:

- automatic self-update;
- release-asset downloading;
- checksum/signature verification;
- installation or rollback;
- repository-host autodetection;
- persistent watcher cache;
- cron-style scheduling;
- provider-specific authentication helpers;
- Gitea, Qt, or Charm integrations.

The public model is intended to leave room for these features without coupling
them to the initial checker implementation.

## License

`go4updates` is licensed under the MIT License. See [LICENSE](LICENSE).

## Local multi-module development with `go.work`

For local development, Go workspaces can make several modules use local source
copies without publishing temporary library releases or adding `replace`
directives to committed `go.mod` files.

To work on the root module and the nested Fyne module together:

```sh
go work init .
go work use ./ui/fyne
```

To develop `go4updates` together with another local application, create a
workspace in a common parent directory, for example:

```sh
go work init ./go4updates ./example-app
```

The application's normal `require github.com/skobkin/go4updates ...` dependency
can remain unchanged while commands executed inside the workspace use the local
checkout.

`go.work` and `go.work.sum` are intentionally ignored by this repository and
should normally remain local development files.
