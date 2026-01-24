# Repository Guidelines

## Project Structure & Module Organization
- `cmd/uca/main.go` contains the CLI entry point and user-facing behavior (flags, output, UI).
- `internal/agents/` defines supported agents and their update strategies.
- `internal/` is reserved for non-exported packages; avoid new public packages unless required.
- `go.mod` / `go.sum` define module dependencies. The repo is intentionally lightweight.

## Build, Test, and Development Commands
- `go build ./cmd/uca` — build the `uca` binary locally.
- `go test ./...` — run unit tests (if/when added).
- `gofmt -w cmd/uca/main.go internal/**/*.go` — format Go files.
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
- Homebrew tap repo: `chhoumann/homebrew-tap`; formula updates are handled by CI.
- When changing CLI behavior, update `README.md` examples and any release notes.
