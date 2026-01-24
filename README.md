# uca

`uca` updates multiple coding-agent CLIs with one command. Quiet by default, parallel when you want it.

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
- `-p, --parallel` run updates in parallel (no tty output from workers)
- `-v, --verbose` show update command output for each agent
- `-q, --quiet` suppress per-agent version lines (summary only)
- `-n, --dry-run` print commands that would run, do not execute
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
uca -p -v
```

Dry run only for claude + codex:
```bash
uca --only claude,codex --dry-run
```

## Supported agents

- amp (`amp update`)
- gemini (`gemini update`)
- claude (`claude update`)
- codex (`bun update -g @openai/codex --latest`)
- opencode (`bun update -g opencode-ai --latest`)

Missing binaries are skipped and reported in the summary.

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
