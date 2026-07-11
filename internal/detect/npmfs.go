package detect

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// npm's global prefix decides both the global bin dir (<prefix>/bin on Unix,
// <prefix> on Windows) and the global package dir (<prefix>/lib/node_modules on
// Unix, <prefix>/node_modules on Windows). Spawning `npm prefix -g` costs an
// ~100ms Node startup and `npm list -g` another ~200ms, so we resolve the
// prefix the way npm itself does for the common cases - environment, ~/.npmrc,
// else derived from the node binary's location - and read the directories
// directly. Anything unusual (env-var interpolation in npmrc, a shim manager
// like volta where the derivation fails validation) falls back to the npm CLI.

// npmPrefixFast returns npm's global prefix without spawning npm, or "" when it
// can't be determined confidently.
func npmPrefixFast() string {
	for _, envVar := range []string{"npm_config_prefix", "NPM_CONFIG_PREFIX"} {
		if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
			return expandHome(v)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if prefix, found := npmrcPrefix(filepath.Join(home, ".npmrc")); found {
		return prefix
	}
	// Default prefix: where node is installed. Use the unresolved PATH entry
	// (a homebrew node symlinks into the Cellar, but the prefix is the link's
	// home) and require the global package dir to exist so shim managers whose
	// layout differs (e.g. volta) fall back to the npm CLI.
	nodePath, err := exec.LookPath("node")
	if err != nil {
		return ""
	}
	prefix := filepath.Dir(filepath.Dir(nodePath))
	if info, err := os.Stat(globalNodeModulesDir(prefix)); err == nil && info.IsDir() {
		return prefix
	}
	return ""
}

// eachNpmrcEntry visits every key=value pair in an npmrc file (keys and values
// whitespace-trimmed, comments and malformed lines skipped); an unreadable file
// visits nothing. Values are passed through verbatim; any expansion (~, ${...})
// is the caller's business.
func eachNpmrcEntry(path string, visit func(key, value string)) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		visit(strings.TrimSpace(key), strings.TrimSpace(value))
	}
}

// npmrcPrefix reads the `prefix=` key from an npmrc file. found=false means the
// key is absent (try the next source); found=true with an empty prefix means
// the key is set but needs npm's own env-var interpolation (ask the npm CLI).
func npmrcPrefix(path string) (prefix string, found bool) {
	eachNpmrcEntry(path, func(key, value string) {
		if found || key != "prefix" {
			return
		}
		found = true
		if value == "" || strings.Contains(value, "${") {
			return // set, but needs npm's own expansion
		}
		prefix = expandHome(value)
	})
	return prefix, found
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

func globalNodeModulesDir(prefix string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(prefix, "node_modules")
	}
	return filepath.Join(prefix, "lib", "node_modules")
}

// listGlobalNodePackages enumerates the packages installed under a global
// node_modules dir (expanding @scope directories). Returns nil when the dir
// can't be read (caller falls back to the manager CLI).
func listGlobalNodePackages(nodeModules string) map[string]bool {
	entries, err := os.ReadDir(nodeModules)
	if err != nil {
		return nil
	}
	pkgs := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if strings.HasPrefix(name, "@") {
			scoped, err := os.ReadDir(filepath.Join(nodeModules, name))
			if err != nil {
				continue
			}
			for _, sub := range scoped {
				if !strings.HasPrefix(sub.Name(), ".") {
					pkgs[name+"/"+sub.Name()] = true
				}
			}
			continue
		}
		pkgs[name] = true
	}
	return pkgs
}
