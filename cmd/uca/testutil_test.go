// Shared test fixtures: fake PATH commands, fake node managers, and capture helpers.
package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/chhoumann/uca/internal/agents"
	"github.com/chhoumann/uca/internal/detect"
)

func agentNames(list []agents.Agent) []string {
	names := make([]string, 0, len(list))
	for _, agent := range list {
		names = append(names, agent.Name)
	}
	return names
}

func hasAgentName(list []agents.Agent, name string) bool {
	for _, agent := range list {
		if agent.Name == name {
			return true
		}
	}
	return false
}

func defaultAgent(t *testing.T, name string) agents.Agent {
	t.Helper()
	for _, agent := range agents.Default() {
		if agent.Name == name {
			return agent
		}
	}
	t.Fatalf("Default() is missing %s", name)
	return agents.Agent{}
}

type fakeCommand struct {
	help    string
	version string
}

func withFakeCommands(t *testing.T, commands map[string]fakeCommand) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell-script PATH fixture on windows")
	}
	dir := t.TempDir()
	for name, cmd := range commands {
		writeFakeCommand(t, dir, name, cmd)
	}
	t.Setenv("PATH", dir)
}

func writeFakeCommand(t *testing.T, dir, name string, cmd fakeCommand) {
	t.Helper()
	body := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  --help)\n" +
		"    printf '%s\\n' " + shellQuote(cmd.help) + "\n" +
		"    ;;\n" +
		"  --version)\n" +
		"    printf '%s\\n' " + shellQuote(cmd.version) + "\n" +
		"    ;;\n" +
		"  update)\n" +
		"    printf \"%s\\n\" \"updated\"\n" +
		"    ;;\n" +
		"  *)\n" +
		"    printf \"%s\\n\" \"unknown\"\n" +
		"    ;;\n" +
		"esac\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func newTestEnv() *detect.Env {
	return detect.New(context.Background())
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				b.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

func writeExec(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// fakePathEnv writes fake executables to a temp dir, points PATH at it, and
// returns a detect.Env that probes that PATH (so capabilities are auto-detected
// from the fakes - no field-poking needed).
func fakePathEnv(t *testing.T, scripts map[string]string) (string, *detect.Env) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script PATH fixtures are POSIX-only")
	}
	dir := t.TempDir()
	for name, body := range scripts {
		writeExec(t, dir, name, body)
	}
	t.Setenv("PATH", dir)
	// Keep latest-version lookups on the fake manager CLIs instead of the live
	// registry HTTP fast path, and isolate the filesystem fast paths (npmrc
	// prefix, VS Code extensions manifest, brew Cellar/taps) from host state.
	t.Setenv("UCA_NO_REGISTRY_HTTP", "1")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("npm_config_prefix", "")
	t.Setenv("NPM_CONFIG_PREFIX", "")
	t.Setenv("HOMEBREW_CELLAR", "")
	t.Setenv("HOMEBREW_REPOSITORY", "")
	return dir, detect.New(context.Background())
}

func nodeIntegrationEnv(t *testing.T, scripts map[string]string) *detect.Env {
	t.Helper()
	_, env := fakePathEnv(t, scripts)
	return env
}

// fakeNpm builds a fake `npm` that answers the detection probes (bin/prefix
// empty, list -> the given global packages, view -> latest) so detect.New
// populates the package list from it, plus records install argv to record (when
// non-empty) and optionally fails the batch (>1 package) install.
func fakeNpm(record string, pkgs []string, failBatch bool, latest string) string {
	return fakeNpmWithBinDir(record, "", pkgs, failBatch, latest)
}

// fakeNpmWithBinDir is fakeNpm plus a `npm prefix -g` answer (binDir's parent,
// mirroring npm's <prefix>/bin layout), so detection sees a real global bin dir
// (used to exercise the bin-dir containment fallback).
func fakeNpmWithBinDir(record, binDir string, pkgs []string, failBatch bool, latest string) string {
	deps := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		deps = append(deps, "\""+p+"\":{}")
	}
	depsJSON := "{\"dependencies\":{" + strings.Join(deps, ",") + "}}"
	prefixCase := ""
	if binDir != "" {
		prefixCase = "echo '" + filepath.Dir(binDir) + "'"
	}
	install := ":"
	if record != "" {
		install = "echo \"$@\" >> '" + record + "'"
	}
	if failBatch {
		install += "; if [ $# -gt 3 ]; then exit 1; fi"
	}
	return "#!/bin/sh\ncase \"$1\" in\n" +
		"  prefix) " + prefixCase + " ;;\n" +
		"  list) echo '" + depsJSON + "' ;;\n" +
		"  view) echo '" + latest + "' ;;\n" +
		"  install) " + install + " ;;\n" +
		"esac\nexit 0\n"
}

func nodeTestAgents() []agents.Agent {
	return []agents.Agent{
		{Name: "one", Binary: "one", VersionCmd: []string{"one", "--version"}, Strategies: []agents.UpdateStrategy{{Kind: agents.KindNpm, Package: "pkg-one"}}},
		{Name: "two", Binary: "two", VersionCmd: []string{"two", "--version"}, Strategies: []agents.UpdateStrategy{{Kind: agents.KindNpm, Package: "pkg-two"}}},
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// npmSkipFixture builds a fake npm environment where pkg-one is installed
// globally at installedVersion (exact package.json metadata) and the registry
// reports latestVersion. Returns the install-call record file.
func npmSkipFixture(t *testing.T, installedVersion, latestVersion, pin string) (string, *detect.Env, []agents.Agent) {
	t.Helper()
	dir := t.TempDir()
	prefix := filepath.Join(dir, "npm-global")
	pkgDir := filepath.Join(prefix, "lib", "node_modules", "pkg-one")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"pkg-one","version":"` + installedVersion + `"}`
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "npm-calls.txt")
	env := nodeIntegrationEnv(t, map[string]string{
		"npm": fakeNpmWithBinDir(record, filepath.Join(prefix, "bin"), nil, false, latestVersion),
		"one": "#!/bin/sh\ncase \"$1\" in --version) echo " + installedVersion + " ;; esac\n",
	})
	list := []agents.Agent{{
		Name: "one", Binary: "one", VersionCmd: []string{"one", "--version"},
		Strategies: []agents.UpdateStrategy{{Kind: agents.KindNpm, Package: "pkg-one", Version: pin}},
	}}
	return record, env, list
}

func recordedCalls(t *testing.T, record string) string {
	t.Helper()
	data, _ := os.ReadFile(record)
	return strings.TrimSpace(string(data))
}
