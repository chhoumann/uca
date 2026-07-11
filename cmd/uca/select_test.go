package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/chhoumann/uca/internal/agents"
	"github.com/chhoumann/uca/internal/detect"
)

func TestFilterAgentsAcceptsCursorAgentAlias(t *testing.T) {
	defaults := agents.Default()

	selected, unknown := filterAgents(defaults, "agent", "")
	if len(unknown) != 0 {
		t.Fatalf("unknown = %#v, want none", unknown)
	}
	if len(selected) != 1 || selected[0].Name != "cursor" {
		t.Fatalf("--only agent selected %#v, want only cursor", agentNames(selected))
	}

	selected, unknown = filterAgents(defaults, "agent,cursor", "")
	if len(unknown) != 0 {
		t.Fatalf("unknown = %#v, want none", unknown)
	}
	if len(selected) != 1 || selected[0].Name != "cursor" {
		t.Fatalf("--only agent,cursor selected %#v, want only cursor", agentNames(selected))
	}

	selected, unknown = filterAgents(defaults, "", "agent")
	if len(unknown) != 0 {
		t.Fatalf("unknown = %#v, want none", unknown)
	}
	if hasAgentName(selected, "cursor") {
		t.Fatalf("--skip agent selected cursor in %#v", agentNames(selected))
	}

	selected, unknown = filterAgents(defaults, "agent", "cursor")
	if len(unknown) != 0 {
		t.Fatalf("unknown = %#v, want none", unknown)
	}
	if len(selected) != 0 {
		t.Fatalf("--only agent --skip cursor selected %#v, want none", agentNames(selected))
	}

	selected, unknown = filterAgents(defaults, "agent,nope", "")
	if !reflect.DeepEqual(unknown, []string{"nope"}) {
		t.Fatalf("unknown = %#v, want %#v", unknown, []string{"nope"})
	}
	if len(selected) != 1 || selected[0].Name != "cursor" {
		t.Fatalf("--only agent,nope selected %#v, want only cursor", agentNames(selected))
	}
}

func TestFilterAgentsUserAgentUppercaseTargetable(t *testing.T) {
	all := append(agents.Default(), agents.Agent{Name: "MyTool", Binary: "mytool"})
	selected, unknown := filterAgents(all, "mytool", "")
	if len(unknown) != 0 {
		t.Fatalf("unknown = %#v, want none", unknown)
	}
	if len(selected) != 1 || selected[0].Name != "MyTool" {
		t.Fatalf("--only mytool selected %#v, want the uppercase-named user agent", agentNames(selected))
	}
}

func TestFilterAgentsOnlyAllUnknownSelectsNone(t *testing.T) {
	defaults := agents.Default()

	// A typo where every --only entry is unknown must select ZERO agents, not all.
	selected, unknown := filterAgents(defaults, "bogus", "")
	if len(selected) != 0 {
		t.Fatalf("--only bogus selected %#v, want none", agentNames(selected))
	}
	if !reflect.DeepEqual(unknown, []string{"bogus"}) {
		t.Fatalf("--only bogus unknown = %#v, want [bogus]", unknown)
	}

	// No --only given selects everything.
	selected, _ = filterAgents(defaults, "", "")
	if len(selected) != len(defaults) {
		t.Fatalf("empty --only selected %d, want all %d", len(selected), len(defaults))
	}

	// A known + unknown mix still selects the known one.
	selected, unknown = filterAgents(defaults, "claude,bogus", "")
	if len(selected) != 1 || selected[0].Name != "claude" {
		t.Fatalf("--only claude,bogus selected %#v, want [claude]", agentNames(selected))
	}
	if !reflect.DeepEqual(unknown, []string{"bogus"}) {
		t.Fatalf("--only claude,bogus unknown = %#v, want [bogus]", unknown)
	}
}

func TestKnownAgentNamesSorted(t *testing.T) {
	got := knownAgentNames(agents.Default())
	for _, name := range []string{"amp", "claude", "codex", "cursor"} {
		if !strings.Contains(got, name) {
			t.Fatalf("knownAgentNames() = %q, missing %q", got, name)
		}
	}
	if !strings.Contains(got, "aider, amp") {
		t.Fatalf("knownAgentNames() not sorted: %q", got)
	}
	// user-defined agents appear too
	custom := append(agents.Default(), agents.Agent{Name: "zzcustom"})
	if !strings.Contains(knownAgentNames(custom), "zzcustom") {
		t.Fatal("knownAgentNames should include user-defined agents")
	}
}

func TestPrewarmNeeds(t *testing.T) {
	tests := []struct {
		name string
		in   []agents.Agent
		want detect.PrewarmNeeds
	}{
		{name: "empty", in: nil, want: detect.PrewarmNeeds{}},
		{
			name: "native only needs nothing",
			in:   []agents.Agent{{Name: "claude", Binary: "claude", Strategies: []agents.UpdateStrategy{{Kind: agents.KindNative}}}},
			want: detect.PrewarmNeeds{},
		},
		{
			name: "node strategy needs node",
			in:   []agents.Agent{{Name: "codex", Binary: "codex", Strategies: []agents.UpdateStrategy{{Kind: agents.KindNpm, Package: "p"}}}},
			want: detect.PrewarmNeeds{Node: true},
		},
		{
			name: "extension id implies vscode (version fallback)",
			in:   []agents.Agent{{Name: "cline", ExtensionID: "x.y", Strategies: []agents.UpdateStrategy{{Kind: agents.KindBun, Package: "p"}}}},
			want: detect.PrewarmNeeds{Node: true, VSCode: true},
		},
		{
			name: "brew pip uv vscode",
			in: []agents.Agent{
				{Name: "omp", Strategies: []agents.UpdateStrategy{{Kind: agents.KindBrew, Package: "omp"}}},
				{Name: "aider", Strategies: []agents.UpdateStrategy{{Kind: agents.KindUv, Package: "a"}, {Kind: agents.KindPip, Package: "a"}}},
				{Name: "roocode", Strategies: []agents.UpdateStrategy{{Kind: agents.KindVSCode, ExtensionID: "r.r"}}},
			},
			want: detect.PrewarmNeeds{Brew: true, Pip: true, Uv: true, VSCode: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prewarmNeeds(tt.in); got != tt.want {
				t.Fatalf("prewarmNeeds = %+v, want %+v", got, tt.want)
			}
		})
	}
}
