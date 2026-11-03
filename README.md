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

## Installation

```sh
go get github.com/skobkin/go4updates
```

For the Fyne update dialog and Markdown renderer:

```sh
go get github.com/skobkin/go4updates/ui/fyne
```

The nested module automatically pulls in the core library; the reverse is never
true — depending on the core module does not add Fyne to your build.

## Requirements

Go 1.25 or newer. Both modules declare `go 1.25.0` and are tested in CI against
both the minimum supported and the current Go release.

## Status

This is a v0.x release: until v1.0 the API may still change between minor
versions. The core checking model (sources, checker, watcher, results) is
considered settled. The `selfupdate` package covers asset download,
verification, and installation with the guarantees described in
[Self-update](#self-update); as a newer addition its API may still adjust as
real-world usage arrives.

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
├── selfupdate
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

Release metadata includes upstream asset information, but the core checking
model only inspects it: downloading, verification, and installation are
opt-in through the [selfupdate](#self-update) package.

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

The core library does not download assets while checking. Download and
verification were designed independently of release checking in the
`selfupdate` package: [`selfupdate.Downloader`](#self-update) streams any
asset to an arbitrary `io.Writer` over HTTPS with hard size bounds, and the
full select/download/verify/extract/install pipeline is described in the next
section. Consumers that only need the raw metadata can keep writing artifacts
to chosen paths or implement application-specific installers on top of it.

## Self-update

The `selfupdate` package applies results produced by the checking model: it
selects the release asset matching the current platform, downloads it over
HTTPS with hard size bounds, verifies its SHA-256 checksum, extracts the
executable, and atomically replaces the running binary while keeping the
previous version for recovery. It never re-fetches release metadata and never
applies updates on its own — the decision to apply belongs to the application.

The package is opt-in by import: applications that never reference it get
nothing in their binary, and the root module gains no new dependencies
(everything is standard library).

### One-shot check and apply

```go
source, err := github.New("example-org/example-app")
if err != nil {
    return err
}

checker := updates.NewChecker(updates.CheckerOptions{})

result, err := checker.Check(ctx, updates.Target{
    CurrentVersion: "1.2.2",
    Source:         source,
})
if err != nil {
    return err
}

if result.Status != updates.StatusUpdateAvailable {
    return nil
}

updater, err := selfupdate.New(selfupdate.WithAppName("example-app"))
if err != nil {
    return err
}

// Optional: preview the asset the platform policy picked before downloading.
asset, err := updater.SelectAsset(*result.Latest)
if err != nil {
    return err
}

fmt.Printf("installing %s from %s\n", result.Latest.Version, asset.Name)

applied, err := updater.Apply(ctx, result)
if err != nil {
    return err
}

fmt.Printf("installed %s; previous binary kept at %s\n", applied.Version, applied.Backup)

// After restarting into the new version and confirming it is healthy
// (initialization or a health check), retire the backup:
if err := updater.Cleanup(); err != nil {
    return err
}
```

### Periodic checking with a watcher

The watcher reports updates; the application decides whether to apply. The
library never applies updates from watcher events on its own:

```go
for event := range events {
    result := event.State.Result

    if result == nil || result.Status != updates.StatusUpdateAvailable {
        continue
    }

    // Application policy decides: user confirmation, maintenance window, etc.
    if !userConfirmed(result.Latest.Version) {
        continue
    }

    applied, err := updater.Apply(ctx, *result)
    if err != nil {
        log.Printf("update failed: %v", err)

        continue
    }

    log.Printf("installed %s; restart to run it, then call updater.Cleanup after a healthy startup (backup: %s)",
        applied.Version, applied.Backup)
}
```

### Guarantees

**Verification is mandatory.** Updates whose checksum asset is missing,
unreachable, or wrong fail loudly; `WithInsecureSkipVerify` is the explicit
escape hatch and defeats the package's central safety property. A checksum
published in the same release provides *integrity* — protection against
corrupted, truncated, or substituted downloads — but not independent publisher
authentication: a compromised release can publish a matching pair, and HTTPS
protects transport only. Publisher authentication requires cryptographic
signatures: implement the `selfupdate.Verifier` interface; the package
documentation contains a complete ed25519 example.

**Transport is HTTPS-only by default**, enforced on every redirect hop.
`WithAllowHTTP` opts in to plain HTTP for private, self-hosted deployments. It
is independent of `WithInsecureSkipVerify`, which controls content
verification, not transport.

**Transfers are bounded while streaming.** Downloads
(`DefaultMaxAssetBytes`, `WithMaxAssetBytes`) and decompression
(`DefaultMaxExtractedBytes`, `WithMaxExtractedBytes`) abort early on oversized
or lying payloads instead of filling memory or disk. The extraction bound
covers tar-family archives as a whole (the stream must be read through) and
zip archives in the selected member only.

**Replacement differs by platform.**

- *Unix*: the previous binary is preserved under the backup name before the
  new one takes it with a single rename, so the target path always refers to a
  complete executable — never missing, never torn. This is an atomic
  *pathname* replacement; durability against sudden power loss additionally
  depends on platform fsync behavior (the staged file is always synced; the
  containing directory is synced best-effort, strongest on Linux).
- *Windows*: a running executable cannot be replaced in place, only moved
  aside. A crash between the two renames leaves the target path missing.
  Recovery is automatic when the original target path is known — `Cleanup`
  restores the backup (see [Backup lifecycle](#backup-lifecycle)) — and is
  otherwise manual:

  ```text
  move app.go4updates.old app.exe
  ```

  The Windows sequence is not atomic and is documented as such.

Runtime behavior is tested on Linux. Windows and darwin paths are kept
compiling in CI (including Windows-tagged tests for native runners), but this
repository does not run native runtime tests for them. Replacing a standalone
executable is not updating a macOS `.app` bundle (bundle structure,
codesigning, notarization) and not a package-manager installation; both are out
of scope.

### Backup lifecycle

After a successful update the previous binary remains at
`<exe>.go4updates.old`. It is removed only by `Updater.Cleanup` (or
`Installer.Cleanup` directly), which the library never calls on its own. Call it from the application **after
successful initialization or a health check** — reaching the new binary proves
it starts, not that it works — and never before: until then the backup is the
only rollback material. `Cleanup` is an artifact collector and interrupted-
update recovery tool, **not a rollback mechanism**; it cannot revert a working
install.

Rules worth knowing:

- A second install while an unconfirmed backup exists fails with
  `selfupdate.ErrBackupExists`; confirm health, run `Cleanup`, retry.
- `Cleanup` restores the backup automatically when the target is missing (the
  Windows crash window). This needs the original target path: when the
  application is launched from the backup executable itself,
  `os.Executable` resolves to the backup path, so construct the installer with
  an explicit `selfupdate.WithTargetPath(originalPath)` — otherwise recovery is
  the manual rename shown above.
- Removing a backup whose old process still runs fails on Windows (sharing
  violation); the failure is collected and the removal succeeds at the next
  startup.

### Asset naming convention

The default matcher expects goreleaser-style names:

```text
{app}[_{-}{version}]_{os}_{arch}[.{archive-ext}]
```

OS and architecture tokens are matched through alias tables (`macos`/`osx` for
`darwin`, `x86_64`/`x64` for `amd64`, `i386`/`x86` for `386`, `aarch64` for
`arm64`, `armv6`/`armv7` for `arm`, `win32`/`win64` for `windows`). Supported
archive extensions: `.zip`, `.tar.gz`, `.tgz`, `.tar`, `.gz`, and raw binaries
(`.exe` required on Windows). Checksum, signature, manifest, SBOM, and
documentation files never match, and unrecognized tokens make the match fail
conservatively.

When several assets match, a raw binary is preferred over `.zip`, then
`.tar.gz`/`.tgz`, then `.tar`, then `.gz`. Several equally ranked candidates
are an error (`ErrAmbiguousAsset`), never an arbitrary pick — and the same
rule applies to members inside archives (`ErrAmbiguousExecutable`). Use
`WithAssetMatcher` for exotic naming schemes.

Limitations worth knowing: Go does not report which ARM variant a binary was
built for, so publishers shipping separate `armv6` and `armv7` assets need a
custom matcher; unmapped platforms (`freebsd`, `riscv64`, …) never match; and
application names containing `_` or `-` cannot be matched by the default
matcher — `New` rejects such a name at construction and points at
`WithAssetMatcher`, which lifts the restriction.

### Making self-update opt-in at build time

The library ships no build tags or ldflags variables — activation policy
belongs to the application. Two common patterns:

1. A variable set at link time, runtime-off by default:

   ```go
   var enableSelfUpdate string // -ldflags "-X main.enableSelfUpdate=true"

   func main() {
       if enableSelfUpdate == "true" {
           startSelfUpdater()
       }
   }
   ```

2. A build-tagged file, so the code is absent from package-manager builds
   entirely (recommended for distribution packaging):

   ```go
   //go:build selfupdate
   ```

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

## Non-goals

The project intentionally does not include:

- repository-host autodetection;
- persistent watcher cache;
- cron-style scheduling;
- provider-specific authentication helpers;
- Gitea, Qt, or Charm integrations.

Within self-update, the following are deliberately deferred until real
requirements arrive: built-in signature schemes beyond the `Verifier`
extension point (ed25519, minisign, cosign), `.xz`/`.zst`/`.7z`/`.bz2`
archives, macOS universal binaries, ARM variant disambiguation beyond the
alias class, delta updates, automatic restart after update, automatic updates
from watcher events, download progress reporting and resume, checksum digests
embedded in `updates.Asset`, and a public rollback API.

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
