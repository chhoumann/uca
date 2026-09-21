package detect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/chhoumann/uca/internal/agents"
)

// NodePackageForBinary identifies the package that declares the resolved executable.
// A bin directory identifies a manager, but several packages can publish the same command.
func (e *Env) NodePackageForBinary(name string) string {
	path := resolveSymlinkPath(e.binaryPath(name))
	if path == "" {
		return ""
	}
	for dir := filepath.Dir(path); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		data, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			continue
		}
		var manifest struct {
			Name string          `json:"name"`
			Bin  json.RawMessage `json:"bin"`
		}
		if json.Unmarshal(data, &manifest) != nil || manifest.Name == "" {
			continue
		}
		var bins map[string]string
		if json.Unmarshal(manifest.Bin, &bins) != nil {
			var bin string
			if json.Unmarshal(manifest.Bin, &bin) != nil {
				continue
			}
			bins = map[string]string{filepath.Base(manifest.Name): bin}
		}
		if bin := bins[name]; bin != "" && resolveSymlinkPath(filepath.Join(dir, bin)) == path {
			return manifest.Name
		}
	}
	return ""
}

// Exact installed-version reads from the metadata each package manager itself
// maintains. These let the update path prove "already at latest" and skip the
// manager's update command entirely (a no-op `npm install -g` still costs
// 0.5-2s of manager startup). Every function returns "" when the answer is not
// exactly knowable - callers then run the update command as usual, so a miss
// is never incorrect, only slower.

// NodeInstalledVersion returns the exact installed version of a global node
// package by reading its package.json. Only npm and bun have confidently
// derivable global package dirs; other managers return "".
func (e *Env) NodeInstalledVersion(kind, pkg string) string {
	pkg = strings.TrimSpace(pkg)
	if pkg == "" {
		return ""
	}
	var nodeModules string
	switch kind {
	case agents.KindNpm:
		prefix := e.npmPrefix()
		if prefix == "" {
			return ""
		}
		nodeModules = globalNodeModulesDir(prefix)
	case agents.KindBun:
		nodeModules = bunGlobalNodeModulesDir(e.nodeBinDir(agents.KindBun))
	default:
		return ""
	}
	if nodeModules == "" {
		return ""
	}
	return packageJSONVersion(filepath.Join(nodeModules, filepath.FromSlash(pkg), "package.json"))
}

// bunGlobalNodeModulesDir derives bun's global package dir from its global bin
// dir ($BUN_INSTALL/bin -> $BUN_INSTALL/install/global/node_modules).
func bunGlobalNodeModulesDir(binDir string) string {
	if binDir == "" {
		return ""
	}
	dir := filepath.Join(filepath.Dir(binDir), "install", "global", "node_modules")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	return dir
}

func packageJSONVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return ""
	}
	return strings.TrimSpace(manifest.Version)
}
