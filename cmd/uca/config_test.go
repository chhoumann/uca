package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chhoumann/uca/internal/agents"
)

func TestParseConfigAgents(t *testing.T) {
	good := `{"agents":[{"name":"foo","binary":"foo","strategies":[{"kind":"npm","package":"foo-pkg"}]}]}`
	got, err := parseConfigAgents([]byte(good), "cfg")
	if err != nil {
		t.Fatalf("parseConfigAgents(good) err = %v", err)
	}
	if len(got) != 1 || got[0].Name != "foo" || got[0].Strategies[0].Package != "foo-pkg" {
		t.Fatalf("parseConfigAgents(good) = %#v", got)
	}

	if _, err := parseConfigAgents([]byte(`{"agents":[{"naem":"typo"}]}`), "cfg"); err == nil {
		t.Fatal("unknown field should error")
	}
	if _, err := parseConfigAgents([]byte(`{"agents":[{"binary":"x"}]}`), "cfg"); err == nil {
		t.Fatal("missing name should error")
	}
	if _, err := parseConfigAgents([]byte(`not json`), "cfg"); err == nil {
		t.Fatal("invalid json should error")
	}
	// An unknown strategy kind fails loudly rather than being silently dropped.
	if _, err := parseConfigAgents([]byte(`{"agents":[{"name":"x","strategies":[{"kind":"cargo"}]}]}`), "cfg"); err == nil {
		t.Fatal("unknown strategy kind should error")
	}
	// encoding/json matches keys case-insensitively, so a case-variant of a real
	// key is accepted (documented behavior, not flagged by DisallowUnknownFields).
	if _, err := parseConfigAgents([]byte(`{"agents":[{"NAME":"x"}]}`), "cfg"); err != nil {
		t.Fatalf("case-variant key should be accepted; got %v", err)
	}
}

func TestParseConfigAgentsRejectsUnusableStrategies(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string // substring of the expected error
	}{
		{"native without command", `{"agents":[{"name":"x","binary":"x","strategies":[{"kind":"native"}]}]}`, `"command"`},
		{"npm without package", `{"agents":[{"name":"x","binary":"x","strategies":[{"kind":"npm"}]}]}`, `"package"`},
		{"node without agent binary", `{"agents":[{"name":"x","strategies":[{"kind":"bun","package":"p"}]}]}`, `"binary"`},
		{"brew without package", `{"agents":[{"name":"x","binary":"x","strategies":[{"kind":"brew"}]}]}`, `"package"`},
		{"pip without package", `{"agents":[{"name":"x","binary":"x","strategies":[{"kind":"pip"}]}]}`, `"package"`},
		{"uv without package", `{"agents":[{"name":"x","binary":"x","strategies":[{"kind":"uv"}]}]}`, `"package"`},
		{"vscode without extension id", `{"agents":[{"name":"x","strategies":[{"kind":"vscode"}]}]}`, `"extensionId"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseConfigAgents([]byte(tt.json), "cfg")
			if err == nil {
				t.Fatalf("parseConfigAgents(%s) accepted an unusable strategy", tt.json)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not name the missing field %s", err, tt.want)
			}
		})
	}
}

func TestMergeAgents(t *testing.T) {
	base := []agents.Agent{{Name: "claude"}, {Name: "codex"}}
	user := []agents.Agent{
		{Name: "Claude", Binary: "custom-claude"}, // overrides built-in (case-insensitive)
		{Name: "mytool", Binary: "mytool"},        // new
	}
	merged := mergeAgents(base, user)
	if len(merged) != 3 {
		t.Fatalf("len(merged) = %d, want 3", len(merged))
	}
	if merged[0].Name != "Claude" || merged[0].Binary != "custom-claude" {
		t.Fatalf("override failed: merged[0] = %#v", merged[0])
	}
	if merged[2].Name != "mytool" {
		t.Fatalf("append failed: merged[2] = %#v", merged[2])
	}
}

func TestLoadConfigAgentsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"agents":[{"name":"mytool","binary":"mytool"}]}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("UCA_CONFIG", path)
	got, err := loadConfigAgents()
	if err != nil {
		t.Fatalf("loadConfigAgents err = %v", err)
	}
	if len(got) != 1 || got[0].Name != "mytool" {
		t.Fatalf("loadConfigAgents = %#v", got)
	}

	// A non-existent config is not an error.
	t.Setenv("UCA_CONFIG", filepath.Join(dir, "does-not-exist.json"))
	got, err = loadConfigAgents()
	if err != nil || got != nil {
		t.Fatalf("missing config: got %#v, err %v; want nil,nil", got, err)
	}
}
