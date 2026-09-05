# Repository Guidelines

## Project Purpose

`go4updates` is a reusable Go library for discovering software releases,
comparing versions, periodically checking multiple update targets, formatting
release notes, and providing optional UI integrations.

The project deliberately separates the update-checking core from UI frameworks.
The root module must remain usable by headless services, CLIs, web backends, and
TUI applications without pulling Fyne or another UI framework into their module
graph.

## Module and Package Structure

- Root module: `github.com/skobkin/go4updates`.
- Root package: `updates`.
- `source/github`: GitHub release source adapter.
- `source/forgejo`: Forgejo release source adapter.
- `source/gitlab`: GitLab release source adapter.
- `watch`: periodic multi-target checking, state, and subscriptions.
- `releasefmt`: framework-independent release-note/Markdown formatting helpers.
- `ui/fyne`: separate nested Go module, `github.com/skobkin/go4updates/ui/fyne`.
- `ui/fyne/markdown`: reusable Fyne Markdown rendering with optional lazy remote images.
- `ui/fyne/update`: Fyne update/release dialog built on core result types.
- `docs/wip`: local-only implementation specifications and agent tasks. This
  directory is intentionally ignored by `docs/.gitignore` and must never be committed.

Future UI integrations may use sibling nested modules such as `ui/charm` or
`ui/qt`. Do not add them speculatively.

## Architectural Rules

- Keep the root module lightweight and independent from Fyne or other UI frameworks.
- Keep responsibilities separated:
  - sources fetch release metadata;
  - checker compares a current version with fetched releases;
  - watcher schedules checks and keeps transient state;
  - `releasefmt` performs presentation-oriented transformations;
  - UI modules render already-produced results.
- Do not mutate upstream release Markdown in the checker or source adapters.
- Do not add self-update, asset download, signature verification, rollback, or
  installer behavior unless explicitly requested and designed first.
- Avoid speculative abstractions. Add extension points where confirmed
  requirements exist, but prefer small interfaces and standard-library types.
- Use `context.Context` on network and other IO boundaries.
- Prefer `net/http` and `encoding/json` over heavy provider SDKs unless there is
  a concrete reason to introduce one.
- HTTP clients must be injectable in source adapters so consumers can configure
  proxies, TLS, authentication transports, tracing, and other networking behavior.
- Source adapters must normalize their output contract, including newest-first
  release ordering, rather than relying on undocumented upstream ordering.
- Preserve raw provider metadata that is generally useful to consumers, including
  release assets, without implying that the library downloads those assets.
- Do not add repository-host autodetection to normal source constructors.
  If autodetection is added later, it belongs in a separate optional component.
- When an architectural choice affects public API compatibility, call it out
  before implementing it.

## Go and API Conventions

- Go version: `1.25` unless the repository is deliberately retargeted.
- Run `gofmt` on all changed Go code.
- Package names are short lowercase nouns.
- Exported identifiers use standard Go naming conventions.
- Keep exported APIs minimal. Prefer private implementation types until consumers
  actually need them.
- Public types, methods, functions, options, constants, and non-obvious behavior
  require useful GoDoc comments.
- Preserve zero-value usability when it is natural and does not complicate the API.
- Errors should carry actionable context and be compatible with `errors.Is` /
  `errors.As` where sentinel or typed errors are useful.
- Do not expose provider-specific implementation IDs in common types without a
  demonstrated cross-provider need.
- Breaking public API changes are acceptable before v1 when justified, but must
  be deliberate and documented.

## Dependency Discipline

- The root module must never depend on `github.com/skobkin/go4updates/ui/fyne`.
- The root module must never acquire Fyne transitively.
- Nested UI modules may depend on the root module.
- Nested modules must not be released while their `go.mod` contains filesystem
  `replace` directives pointing at sibling modules. Before tagging a nested
  module release, point the dependency at its published version and remove the
  replace directive.
- Keep provider adapters dependency-light; prefer direct HTTP APIs over pulling
  large GitHub/GitLab/Gitea SDKs into the root module.
- Before adding a dependency, check whether the standard library or an existing
  small dependency already solves the problem.

## Testing Guidelines

- Place tests next to implementation using `*_test.go`.
- Prefer table-driven tests for version comparison, release normalization,
  filtering, URL construction, and formatting.
- Use `httptest.Server` for source adapter tests. Do not make tests depend on
  live GitHub, GitLab, Forgejo, or other external services.
- Add regression tests for bugs when practical.
- Test cancellation and timeout behavior for IO paths.
- Test malformed/partial provider responses and empty release feeds.
- Test that adapters return releases newest-first and correctly apply prerelease
  and limit policies.
- Test watcher behavior with deterministic short intervals or injected timing
  seams; avoid flaky sleeps where possible.
- Fyne-specific tests belong only to the `ui/fyne` module.
- Security-sensitive Markdown image behavior must have tests proving that disabled
  remote images and disallowed URI schemes do not trigger unexpected IO.

## Documentation Discipline

- Update `README.md` in the same change whenever public API, package layout,
  supported providers, defaults, examples, or documented behavior changes.
- Keep README examples compilable and aligned with the current API.
- If a new meaningful usage pattern is introduced, add or update a README example.
- Keep package GoDoc synchronized with implementation behavior.
- If a nested module is added or removed, update both README documentation and
  repository development instructions.
- Never move material from `docs/wip` into committed documentation verbatim.
  Internal specifications are planning artifacts, not product documentation.
- Do not commit internal agent prompts, scratch plans, private implementation
  discussions, or other working material.

## README Usage Examples

The README should continue to demonstrate distinct use cases rather than variants
of the same example. At minimum retain examples for:

- one-shot checking in a CLI/headless program;
- periodic checking of one application with a UI consuming watcher events;
- periodic checking of multiple independently-versioned components in a server/web backend;
- custom HTTP client injection;
- custom version comparator;
- optional Fyne update dialog usage once the Fyne module is implemented.

Examples inspired by real projects must use generic/anonymized application,
repository, channel, and component names.

## Completion Checklist

Before considering implementation work finished, run checks for every affected Go module.

### Root module

- `go fmt ./...`
- `go vet ./...`
- `go test ./...`

If `golangci-lint` configuration is present:

- `golangci-lint run ./...`

### Fyne module, when changed

From `ui/fyne`:

- `go fmt ./...`
- `go vet ./...`
- `go test ./...`

If lint configuration applies to the nested module, run the corresponding lint command.

### Project hygiene

- Verify `git status` does not contain files from `docs/wip`.
- Verify the root module has not acquired UI framework dependencies.
- Update README and GoDoc when public behavior changed.
- Do not state that work is complete while required checks are failing.
- At the end of a completed task, provide a concise summary of changes, tests run,
  any known limitations, and a recommended Conventional Commit message.

## Commit Guidelines

- Use Conventional Commits.
- Keep commits focused and describe behavioral impact.
- Suggested scopes include `source`, `watch`, `releasefmt`, `fyne`, `docs`, and `deps`.
- Examples:
  - `feat(source): add GitLab release adapter`
  - `feat(watch): support per-target intervals`
  - `fix(releasefmt): preserve fenced code during linkification`
  - `docs: add multi-target watcher example`
- Do not commit generated binaries, local workspaces, internal specifications, or
  agent task files.

## When in Doubt

- Prefer simpler architecture over speculative abstraction.
- Preserve module boundaries and dependency isolation.
- For public API choices with long-term compatibility impact, surface the tradeoff
  before coding instead of guessing.
- Call out weak technical decisions early and propose concrete alternatives.
