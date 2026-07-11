package detect

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chhoumann/uca/internal/runner"
	"github.com/chhoumann/uca/internal/version"
)

// Homebrew keeps its state on disk in predictable places: installed formulae
// are version directories under the Cellar, and tap formulae are local .rb
// files. Reading those directly answers "is it installed" and often "what is
// the latest" without ~0.5-0.7s of Ruby startup per brew invocation. Anything
// ambiguous falls back to the brew CLI.

func (e *Env) brewLatest(ctx context.Context, formula string) string {
	if formula == "" {
		return ""
	}
	// Fast path: tap formulae are local .rb files, and release-pipeline-generated
	// ones (GoReleaser etc.) carry an explicit `version "x"` literal. `brew info`
	// reads the same clone, so this is the same data without ~0.7s of Ruby
	// startup. Anything ambiguous or literal-less falls through to brew info.
	if v := tapFormulaVersion(brewTapsDirs(), formula); v != "" {
		return v
	}
	out, exitCode, _, _ := runner.RunStdout(ctx, []string{"brew", "info", "--json=v2", formula}, latestVersionCmdTimeout)
	if exitCode != 0 {
		return ""
	}
	return version.ParseBrewLatest(out)
}

// brewCellarDir resolves the Cellar location: $HOMEBREW_CELLAR, else
// <prefix>/Cellar with the prefix derived from the brew binary's location
// (unresolved, since e.g. /usr/local/bin/brew symlinks into the Homebrew
// repository while the Cellar stays under /usr/local).
func brewCellarDir() string {
	if dir := strings.TrimSpace(os.Getenv("HOMEBREW_CELLAR")); dir != "" {
		return dir
	}
	brewPath, err := exec.LookPath("brew")
	if err != nil {
		return ""
	}
	prefix := filepath.Dir(filepath.Dir(brewPath))
	return filepath.Join(prefix, "Cellar")
}

// cellarFormulae lists installed formulae by reading the Cellar directory.
// Returns nil when the Cellar can't be read (caller falls back to `brew list`).
func cellarFormulae(cellar string) map[string]bool {
	if cellar == "" {
		return nil
	}
	entries, err := os.ReadDir(cellar)
	if err != nil {
		return nil
	}
	formulae := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(cellar, entry.Name()))
		if err != nil || len(versions) == 0 {
			continue
		}
		formulae[entry.Name()] = true
	}
	return formulae
}

// brewTapsDirs returns candidate Library/Taps directories: the configured
// repository, the prefix itself (Apple Silicon layout), and <prefix>/Homebrew
// (Intel macOS layout).
func brewTapsDirs() []string {
	roots := []string{}
	if repo := strings.TrimSpace(os.Getenv("HOMEBREW_REPOSITORY")); repo != "" {
		roots = append(roots, repo)
	}
	if brewPath, err := exec.LookPath("brew"); err == nil {
		prefix := filepath.Dir(filepath.Dir(brewPath))
		roots = append(roots, prefix, filepath.Join(prefix, "Homebrew"))
	}
	dirs := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, root := range roots {
		dir := filepath.Join(root, "Library", "Taps")
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// tapFormulaVersion finds formula .rb files across locally-cloned taps and
// extracts an explicit version literal. Empty when the formula is absent,
// present in more than one tap (ambiguous), or has no literal (version derived
// from its url, e.g. most homebrew/core formulae).
func tapFormulaVersion(tapsDirs []string, formula string) string {
	matches := []string{}
	for _, dir := range tapsDirs {
		for _, pattern := range []string{
			filepath.Join(dir, "*", "*", "Formula", formula+".rb"),
			filepath.Join(dir, "*", "*", formula+".rb"),
		} {
			found, err := filepath.Glob(pattern)
			if err != nil {
				continue
			}
			matches = append(matches, found...)
		}
	}
	if len(matches) != 1 {
		return ""
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return ""
	}
	return formulaVersionLiteral(string(data))
}

// formulaVersionLiteral extracts the first `version "x"` literal from formula
// source, or "".
func formulaVersionLiteral(src string) string {
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "version ")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if len(rest) < 2 || rest[0] != '"' {
			continue
		}
		if end := strings.IndexByte(rest[1:], '"'); end > 0 {
			return rest[1 : 1+end]
		}
	}
	return ""
}
