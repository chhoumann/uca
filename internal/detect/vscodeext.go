package detect

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/chhoumann/uca/internal/version"
)

// VS Code maintains ~/.vscode/extensions/extensions.json (the CLI variants use
// their own dotdirs) as the source of truth for installed extensions, with a
// sibling .obsolete file marking entries pending removal. Reading these
// replaces a `code --list-extensions` spawn (~250ms of Electron CLI startup).
// Any read/parse problem falls back to the CLI.

func codeExtensionsDir(codeCmd string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch codeCmd {
	case "code":
		return filepath.Join(home, ".vscode", "extensions")
	case "codium":
		return filepath.Join(home, ".vscode-oss", "extensions")
	case "code-insiders":
		return filepath.Join(home, ".vscode-insiders", "extensions")
	default:
		return ""
	}
}

// readCodeExtensions parses an extensions dir's extensions.json into
// id -> version, skipping obsolete entries and keeping the highest version when
// an extension appears more than once. Returns nil when the file can't be read
// or parsed (caller falls back to the CLI).
func readCodeExtensions(dir string) map[string]string {
	if dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "extensions.json"))
	if err != nil {
		return nil
	}
	var entries []struct {
		Identifier struct {
			ID string `json:"id"`
		} `json:"identifier"`
		Version          string `json:"version"`
		RelativeLocation string `json:"relativeLocation"`
	}
	if json.Unmarshal(data, &entries) != nil {
		return nil
	}

	obsolete := map[string]bool{}
	if raw, err := os.ReadFile(filepath.Join(dir, ".obsolete")); err == nil {
		// Best-effort: an unparseable .obsolete just means nothing is filtered.
		_ = json.Unmarshal(raw, &obsolete)
	}

	exts := map[string]string{}
	for _, e := range entries {
		if e.Identifier.ID == "" || e.Version == "" {
			continue
		}
		if e.RelativeLocation != "" && obsolete[e.RelativeLocation] {
			continue
		}
		if prev, ok := exts[e.Identifier.ID]; ok && version.Compare(prev, e.Version) >= 0 {
			continue
		}
		exts[e.Identifier.ID] = e.Version
	}
	return exts
}
