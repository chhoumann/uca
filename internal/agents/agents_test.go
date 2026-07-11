package agents

import (
	"reflect"
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
		"amp", "gemini", "claude", "codex", "opencode", "droid", "cursor",
		"copilot", "cline", "roocode", "aider", "pi", "omp", "grok",
	}
	for _, name := range want {
		if !present[name] {
			t.Errorf("Default() is missing %s", name)
		}
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
