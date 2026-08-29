package agents

import (
	"reflect"
	"slices"
	"testing"
)

func agentByName(t *testing.T, name string) Agent {
	t.Helper()
	for _, a := range Default() {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("Default() is missing %s", name)
	return Agent{}
}

func TestDefaultIncludesExpectedAgents(t *testing.T) {
	present := map[string]bool{}
	for _, a := range Default() {
		present[a.Name] = true
	}
	want := []string{
		"amp", "gemini", "claude", "codex", "opencode", "opencode2", "droid", "cursor",
		"copilot", "cline", "roocode", "aider", "pi", "omp", "grok", "muse",
	}
	for _, name := range want {
		if !present[name] {
			t.Errorf("Default() is missing %s", name)
		}
	}
}

func TestOpenCode2UsesBetaChannel(t *testing.T) {
	agent := agentByName(t, "opencode2")
	if agent.Name != "opencode2" {
		t.Fatalf("Name = %q, want opencode2", agent.Name)
	}
	if agent.Binary != "opencode2" {
		t.Fatalf("Binary = %q, want opencode2", agent.Binary)
	}
	wantVersion := []string{"opencode2", "--version"}
	if !reflect.DeepEqual(agent.VersionCmd, wantVersion) {
		t.Fatalf("VersionCmd = %#v, want %#v", agent.VersionCmd, wantVersion)
	}
	wantKinds := []string{KindNpm, KindPnpm, KindYarn, KindBun}
	if len(agent.Strategies) != len(wantKinds) {
		t.Fatalf("Strategies count = %d, want %d", len(agent.Strategies), len(wantKinds))
	}
	for i, s := range agent.Strategies {
		if s.Kind != wantKinds[i] {
			t.Fatalf("Strategies[%d].Kind = %q, want %q", i, s.Kind, wantKinds[i])
		}
		if s.Package != "@opencode-ai/cli" {
			t.Fatalf("Strategies[%d].Package = %q, want @opencode-ai/cli", i, s.Package)
		}
		if s.Version != "beta" {
			t.Fatalf("Strategies[%d].Version = %q, want beta", i, s.Version)
		}
		switch s.Kind {
		case KindPnpm:
			if !slices.Contains(s.Command, "--allow-build=@opencode-ai/cli") {
				t.Fatalf("pnpm Command = %#v, want --allow-build=@opencode-ai/cli", s.Command)
			}
		case KindBun:
			if !slices.Contains(s.Command, "--trust") {
				t.Fatalf("bun Command = %#v, want --trust", s.Command)
			}
		}
	}
}

func TestMuseUsesActiveLauncher(t *testing.T) {
	muse := agentByName(t, "muse")
	if muse.Binary != "muse" {
		t.Fatalf("Binary = %q, want muse", muse.Binary)
	}
	if !muse.DisableVersionCache {
		t.Fatal("Muse version cache must be disabled for its stable launcher")
	}
	wantVersion := []string{"env", "MUSE_NO_AUTO_UPDATE=1", "muse", "--version"}
	if !reflect.DeepEqual(muse.VersionCmd, wantVersion) {
		t.Fatalf("VersionCmd = %#v, want %#v", muse.VersionCmd, wantVersion)
	}
	wantUpdate := []string{"env", "MUSE_SYNC_UPDATE=1", "muse", "--version"}
	if len(muse.Strategies) != 1 || muse.Strategies[0].Kind != KindNative ||
		!reflect.DeepEqual(muse.Strategies[0].Command, wantUpdate) {
		t.Fatalf("Strategies = %#v, want native %#v", muse.Strategies, wantUpdate)
	}
}

// The resolver takes the first applicable strategy, so ordering is a real
// contract: it decides which updater wins when several are viable.
func TestStrategyOrderingContracts(t *testing.T) {
	label := func(s UpdateStrategy) string {
		if s.Kind != KindNative {
			return s.Kind
		}
		target := s.Binary
		if target == "" && len(s.Command) > 0 {
			target = s.Command[0]
		}
		return "native:" + target
	}
	tests := []struct {
		agent  string
		want   []string
		reason string
	}{
		{
			agent:  "grok",
			want:   []string{KindNpm, KindPnpm, KindYarn, KindBun, "native:grok"},
			reason: "native last so a node-manager install wins when present",
		},
		{
			agent:  "omp",
			want:   []string{"native:omp", KindBrew, KindBun},
			reason: "native first: omp update refreshes the registry, a plain brew upgrade may not",
		},
		{
			agent:  "cursor",
			want:   []string{"native:agent", "native:cursor-agent"},
			reason: "new agent binary first, legacy cursor-agent as fallback",
		},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			strategies := agentByName(t, tt.agent).Strategies
			got := make([]string, len(strategies))
			for i, s := range strategies {
				got[i] = label(s)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("%s strategy order = %v, want %v (%s)", tt.agent, got, tt.want, tt.reason)
			}
		})
	}
}

func TestIsNodeKind(t *testing.T) {
	tests := []struct {
		kind string
		want bool
	}{
		{KindNpm, true},
		{KindPnpm, true},
		{KindYarn, true},
		{KindBun, true},
		{KindNative, false},
		{KindBrew, false},
		{KindPip, false},
		{KindUv, false},
		{KindVSCode, false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsNodeKind(tt.kind); got != tt.want {
			t.Errorf("IsNodeKind(%q) = %v, want %v", tt.kind, got, tt.want)
		}
	}
}
