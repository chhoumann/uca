# Repository Guidelines

## Project Structure & Module Organization
- `cmd/uca/` is the CLI (package main), split by concern: `main.go` (flags/entry), `config.go` (user config), `select.go` (agent selection), `run.go` (update orchestration), `failure.go` (failure classification, npm retry), `check.go` (--check mode), `print.go` (text/JSON output). Tests mirror the source files; shared fixtures live in `testutil_test.go`.
- `internal/agents/` defines the built-in agents and the shared status/reason/kind vocabulary.
- `internal/agentspec/` resolves which update strategy applies; `internal/detect/` probes the environment (managers, packages, registries); `internal/runner/` executes subprocesses; `internal/ui/` renders the live dashboard; `internal/version/` parses/compares versions; `internal/vercache/` caches version-command output.
- `internal/` is reserved for non-exported packages; avoid new public packages unless required.
- `go.mod` / `go.sum` define module dependencies. The repo is intentionally lightweight.

## Build, Test, and Development Commands
- `go build ./cmd/uca` — build the `uca` binary locally.
- `go test ./...` — run unit tests.
- `UCA_TRACE=1 uca ...` — log every subprocess (start offset, duration, exit code) to stderr; use this when measuring performance.
- `UCA_NO_REGISTRY_HTTP=1` — disable the direct-registry HTTP fast path for latest-version lookups (falls back to the package-manager CLIs). Test helpers set this to stay hermetic.
- `UCA_NO_VERSION_CACHE=1` — disable the persistent version-command cache (`<user-cache-dir>/uca/versions.json`, keyed by binary path+size+mtime so any update invalidates it).
- `gofmt -w cmd internal` — format Go files.
- `go mod tidy` — clean up dependencies after adding/removing imports.

## Coding Style & Naming Conventions
- Use standard Go formatting (`gofmt`) and idiomatic naming (mixedCaps, no underscores).
- Keep functions small and single-purpose; avoid deep nesting in UI rendering code.
- Favor explicit, readable control flow over clever abstractions (this is a tiny CLI).
- Strings in UI output should be width-safe (avoid line wrapping in TTY mode).

## Testing Guidelines
- No formal test suite yet; add tests for parsing, detection, and formatting logic when introducing non-trivial changes.
- Prefer table-driven tests in `*_test.go` colocated with the package.
- Keep tests deterministic and independent of local machine state.

## Commit & Pull Request Guidelines
- Commit messages are short, imperative, and scoped (e.g., “Add live UI and serial flag”).
- Keep commits focused and avoid mixing unrelated changes.
- PRs should include: summary of changes, how to verify, and any UX screenshots/gifs when output changes.

## Release & Distribution Notes
- Releases are tagged (e.g., `v0.2.0`) and built via GoReleaser.
- Homebrew tap repo: `chhoumann/homebrew-tap`; cask updates (`Casks/uca.rb`) are handled by CI.
- When changing CLI behavior, update `README.md` examples and any release notes.
