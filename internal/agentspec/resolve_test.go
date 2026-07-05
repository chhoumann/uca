package agentspec

import (
	"reflect"
	"strings"
	"testing"

	"github.com/chhoumann/uca/internal/agents"
)

// fakeEnv is a configurable in-memory implementation of the Env interface, so
// Resolve can be unit-tested without touching PATH or running commands.
type fakeEnv struct {
	bins      map[string]bool
	nodeMgrs  map[string]bool
	mgrForBin map[string]string
	mgrForPkg map[string]string
	binInMgr  map[string]bool // key: kind+"|"+name
	brew      bool
	brewSet   map[string]bool
	python    bool
	pipSet    map[string]bool
	uv        bool
	uvSet     map[string]bool
	code      string
	vscodeSet map[string]bool
	help      map[string]bool // key: binary+"|"+contains
}

func (f fakeEnv) HasBinary(n string) bool               { return f.bins[n] }
func (f fakeEnv) HasNodeManager(k string) bool          { return f.nodeMgrs[k] }
func (f fakeEnv) NodeManagerForBinary(n string) string  { return f.mgrForBin[n] }
func (f fakeEnv) NodeBinHasBinary(k, n string) bool     { return f.binInMgr[k+"|"+n] }
func (f fakeEnv) NodeManagerForPackage(p string) string { return f.mgrForPkg[p] }
func (f fakeEnv) HasBrew() bool                         { return f.brew }
func (f fakeEnv) BrewHas(x string) bool                 { return f.brewSet[x] }
func (f fakeEnv) HasPython() bool                       { return f.python }
func (f fakeEnv) PipHas(x string) bool                  { return f.pipSet[x] }
func (f fakeEnv) HasUv() bool                           { return f.uv }
func (f fakeEnv) UvHas(x string) bool                   { return f.uvSet[x] }
func (f fakeEnv) CodeCmd() string                       { return f.code }
func (f fakeEnv) VscodeHas(x string) bool               { return f.vscodeSet[x] }
func (f fakeEnv) HelpMatches(b, c string) bool {
	if c == "" {
		return true
	}
	return f.help[b+"|"+c]
}

func agentByName(t *testing.T, name string) agents.Agent {
	t.Helper()
	for _, a := range agents.Default() {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("Default() missing %s", name)
	return agents.Agent{}
}

func TestResolveCursorPrefersAgentWhenBothInstalled(t *testing.T) {
	env := fakeEnv{bins: map[string]bool{"agent": true, "cursor-agent": true}, help: map[string]bool{"agent|Cursor Agent": true}}
	r := Resolve(agentByName(t, "cursor"), env)
	if !reflect.DeepEqual(r.Cmd, []string{"agent", "update"}) {
		t.Fatalf("cmd = %#v", r.Cmd)
	}
	if !reflect.DeepEqual(r.VersionCmd, []string{"agent", "--version"}) {
		t.Fatalf("versionCmd = %#v", r.VersionCmd)
	}
	if !strings.Contains(r.Detail, "binary agent found") {
		t.Fatalf("detail = %q", r.Detail)
	}
}

func TestResolveCursorUsesAgentWhenOnlyAgentInstalled(t *testing.T) {
	env := fakeEnv{bins: map[string]bool{"agent": true}, help: map[string]bool{"agent|Cursor Agent": true}}
	r := Resolve(agentByName(t, "cursor"), env)
	if !reflect.DeepEqual(r.Cmd, []string{"agent", "update"}) {
		t.Fatalf("cmd = %#v", r.Cmd)
	}
}

func TestResolveCursorFallsBackToCursorAgent(t *testing.T) {
	env := fakeEnv{bins: map[string]bool{"cursor-agent": true}}
	r := Resolve(agentByName(t, "cursor"), env)
	if !reflect.DeepEqual(r.Cmd, []string{"cursor-agent", "update"}) {
		t.Fatalf("cmd = %#v", r.Cmd)
	}
	if !reflect.DeepEqual(r.VersionCmd, []string{"cursor-agent", "--version"}) {
		t.Fatalf("versionCmd = %#v", r.VersionCmd)
	}
}

func TestResolveCursorFallsBackWhenAgentIdentityFails(t *testing.T) {
	// agent present but --help doesn't identify Cursor; cursor-agent present.
	env := fakeEnv{bins: map[string]bool{"agent": true, "cursor-agent": true}}
	r := Resolve(agentByName(t, "cursor"), env)
	if !reflect.DeepEqual(r.Cmd, []string{"cursor-agent", "update"}) {
		t.Fatalf("cmd = %#v", r.Cmd)
	}
	if r.Reason != "" {
		t.Fatalf("reason = %q, want empty", r.Reason)
	}
}

func TestResolveCursorRejectsUnrelatedAgent(t *testing.T) {
	env := fakeEnv{bins: map[string]bool{"agent": true}}
	r := Resolve(agentByName(t, "cursor"), env)
	if r.Cmd != nil {
		t.Fatalf("cmd = %#v, want nil", r.Cmd)
	}
	if r.Reason != agents.ReasonMissing {
		t.Fatalf("reason = %q, want %q", r.Reason, agents.ReasonMissing)
	}
	if !strings.Contains(r.Detail, "did not identify Cursor Agent") {
		t.Fatalf("detail = %q", r.Detail)
	}
}

func TestResolveNativeDefaultsToAgentFields(t *testing.T) {
	env := fakeEnv{bins: map[string]bool{"amp": true}}
	r := Resolve(agentByName(t, "amp"), env)
	if !reflect.DeepEqual(r.Cmd, []string{"amp", "update"}) {
		t.Fatalf("cmd = %#v", r.Cmd)
	}
	if !reflect.DeepEqual(r.VersionCmd, []string{"amp", "--version"}) {
		t.Fatalf("versionCmd = %#v", r.VersionCmd)
	}
	if !strings.Contains(r.Detail, "binary amp found") {
		t.Fatalf("detail = %q, want selected amp binary", r.Detail)
	}
}

func TestResolveBrew(t *testing.T) {
	env := fakeEnv{brew: true, brewSet: map[string]bool{"copilot-cli": true}}
	r := Resolve(agentByName(t, "copilot"), env)
	if !reflect.DeepEqual(r.Cmd, []string{"brew", "upgrade", "copilot-cli"}) || r.Method != agents.KindBrew {
		t.Fatalf("brew resolve = %#v method %q", r.Cmd, r.Method)
	}
}

func TestResolvePip(t *testing.T) {
	env := fakeEnv{python: true, pipSet: map[string]bool{"aider-chat": true}}
	r := Resolve(agentByName(t, "aider"), env)
	want := []string{"python3", "-m", "pip", "install", "-U", "--upgrade-strategy", "only-if-needed", "aider-chat"}
	if !reflect.DeepEqual(r.Cmd, want) || r.Method != agents.KindPip {
		t.Fatalf("pip resolve = %#v method %q", r.Cmd, r.Method)
	}
}

func TestResolveUv(t *testing.T) {
	env := fakeEnv{uv: true, uvSet: map[string]bool{"aider-chat": true}}
	r := Resolve(agentByName(t, "aider"), env)
	want := []string{"uv", "tool", "install", "--force", "--python", "python3.12", "--with", "pip", "aider-chat@latest"}
	if !reflect.DeepEqual(r.Cmd, want) || r.Method != agents.KindUv {
		t.Fatalf("uv resolve = %#v method %q", r.Cmd, r.Method)
	}
}

func TestResolveVSCode(t *testing.T) {
	env := fakeEnv{code: "code", vscodeSet: map[string]bool{"RooVeterinaryInc.roo-cline": true}}
	r := Resolve(agentByName(t, "roocode"), env)
	want := []string{"code", "--install-extension", "RooVeterinaryInc.roo-cline", "--force"}
	if !reflect.DeepEqual(r.Cmd, want) || r.Method != agents.KindVSCode {
		t.Fatalf("vscode resolve = %#v method %q", r.Cmd, r.Method)
	}
}

func TestResolveVSCodeMissingCode(t *testing.T) {
	env := fakeEnv{code: ""}
	r := Resolve(agentByName(t, "roocode"), env)
	if r.Cmd != nil || r.Reason != agents.ReasonMissingCode {
		t.Fatalf("missing-code resolve = %#v reason %q", r.Cmd, r.Reason)
	}
}

func TestResolveManualInstall(t *testing.T) {
	// binary present but no supported install method -> manual.
	env := fakeEnv{bins: map[string]bool{"gemini": true}}
	r := Resolve(agentByName(t, "gemini"), env)
	if r.Cmd != nil || r.Reason != agents.ReasonManualInstall {
		t.Fatalf("manual resolve = %#v reason %q", r.Cmd, r.Reason)
	}
}

func TestResolveNodeByPackageList(t *testing.T) {
	// installed via a node manager's global package list (not bin dir).
	env := fakeEnv{
		nodeMgrs:  map[string]bool{agents.KindNpm: true, agents.KindPnpm: true, agents.KindYarn: true, agents.KindBun: true},
		mgrForPkg: map[string]string{"@openai/codex": agents.KindNpm},
		bins:      map[string]bool{"codex": true},
	}
	r := Resolve(agentByName(t, "codex"), env)
	if !reflect.DeepEqual(r.Cmd, []string{"npm", "install", "-g", "@openai/codex@latest"}) || r.Method != agents.KindNpm {
		t.Fatalf("node resolve = %#v method %q", r.Cmd, r.Method)
	}
	if r.Pkg != "@openai/codex" {
		t.Fatalf("pkg = %q", r.Pkg)
	}
}

func TestResolveNodeBinDirFallbackKeepsPin(t *testing.T) {
	// Binary PATH-resolves outside every manager bin dir (e.g. a shim), package
	// list lookup misses, so resolution falls back to bin-dir containment. The
	// pinned version must survive that path, or the agent is silently batched
	// to @latest by the orchestrator.
	agent := agents.Agent{
		Name:       "one",
		Binary:     "one",
		VersionCmd: []string{"one", "--version"},
		Strategies: []agents.UpdateStrategy{{Kind: agents.KindNpm, Package: "pkg-one", Version: "1.2.3"}},
	}
	env := fakeEnv{
		nodeMgrs: map[string]bool{agents.KindNpm: true},
		bins:     map[string]bool{"one": true},
		binInMgr: map[string]bool{"npm|one": true},
	}
	r := Resolve(agent, env)
	if r.Version != "1.2.3" {
		t.Fatalf("version = %q, want %q (pin must survive the bin-dir fallback)", r.Version, "1.2.3")
	}
	if !reflect.DeepEqual(r.Cmd, []string{"npm", "install", "-g", "pkg-one@1.2.3"}) || r.Method != agents.KindNpm || r.Pkg != "pkg-one" {
		t.Fatalf("resolve = %#v", r)
	}
}
