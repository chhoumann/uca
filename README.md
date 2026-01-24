# uca

`uca` updates multiple coding-agent CLIs with one command. Quiet by default, parallel by default.

## Install

### Homebrew (recommended)
```bash
brew install chhoumann/tap/uca
```

### Go install
```bash
go install github.com/chhoumann/uca@latest
```

## Usage
```bash
uca [options]
```

Options:
- `-p, --parallel` run updates in parallel (default)
- `--serial` run updates sequentially
- `-v, --verbose` show update command output for each agent
- `-q, --quiet` suppress per-agent version lines (summary only)
- `-n, --dry-run` print commands that would run, do not execute
- `--explain` show detection details and chosen update method
- `--only <list>` comma-separated agent list to include (e.g. `claude,codex`)
- `--skip <list>` comma-separated agent list to exclude
- `-h, --help` show usage

## Examples

Update everything:
```bash
uca
```

Parallel update with verbose logs:
```bash
uca -v
```

Serial update:
```bash
uca --serial
```

Dry run only for claude + codex:
```bash
uca --only claude,codex --dry-run
```

Explain detection and method:
```bash
uca --explain
```

## Supported agents

- amp (`amp update`)
- gemini (`gemini update`)
- claude (`claude update`)
- codex (`bun update -g @openai/codex --latest`)
- opencode (`bun update -g opencode-ai --latest`)
- cursor (`cursor-agent update`)
- copilot (Homebrew `copilot-cli` or npm `@github/copilot`)
- cline (npm `cline` or VS Code extension `saoudrizwan.claude-dev`)
- roocode (VS Code extension `RooVeterinaryInc.roo-cline`)
- aider (uv tool `aider-chat` or pip `aider-chat`)
- pi (npm `@mariozechner/pi-coding-agent`)

## Live output

When `uca` is run in a TTY, it shows a live status dashboard with progress, versions, and timings. When output is piped (or `--quiet`), it prints only completed lines and the summary.

## Detection strategy

`uca` only updates agents it can confidently detect. It checks:
- built-in update commands for native CLIs
- Homebrew formulas
- npm global packages
- uv tool installs
- pip packages
- VS Code extensions (via `code`, `codium`, or `code-insiders`)

If a tool is installed but managed by an unknown method, it is marked as a manual install and skipped.

## Output (default)
```
claude: 2.1.19 -> 2.1.19 (8s)
...
updated: amp claude codex opencode
skipped (missing): gemini
```

## Development
```bash
go build ./cmd/uca
```

## License
MIT
