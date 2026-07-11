package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/chhoumann/uca/internal/agents"
)

func TestDryRunPlanLinesGroupsBatch(t *testing.T) {
	batch := "bun add -g @openai/codex@latest opencode-ai@latest"
	results := []result{
		{Agent: agents.Agent{Name: "claude"}, Status: agents.StatusUpdated, Reason: "dry-run", UpdateCmd: "claude update"},
		{Agent: agents.Agent{Name: "codex"}, Status: agents.StatusUpdated, Reason: "dry-run", UpdateCmd: batch},
		{Agent: agents.Agent{Name: "gemini"}, Status: agents.StatusSkipped, Reason: agents.ReasonMissing},
		{Agent: agents.Agent{Name: "opencode"}, Status: agents.StatusUpdated, Reason: "dry-run", UpdateCmd: batch},
	}
	lines := dryRunPlanLines(results)
	want := []string{
		"claude: claude update",
		"codex, opencode: " + batch,
		"gemini: skipped (missing)",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("dryRunPlanLines() =\n%#v\nwant\n%#v", lines, want)
	}
}

func TestBuildReport(t *testing.T) {
	results := []result{
		{Agent: agents.Agent{Name: "claude"}, Method: "native", Status: agents.StatusUpdated, Before: "1", After: "2", Duration: 8 * time.Second, UpdateCmd: "claude update"},
		{Agent: agents.Agent{Name: "codex"}, Method: "bun", Status: agents.StatusUnchanged, Before: "1", After: "1"},
		{Agent: agents.Agent{Name: "gemini"}, Status: agents.StatusSkipped, Reason: "missing"},
		{Agent: agents.Agent{Name: "amp"}, Method: "native", Status: agents.StatusUpdated, Reason: "dry-run", Before: "1", After: "1", UpdateCmd: "amp update", Explain: "binary amp found"},
		{Agent: agents.Agent{Name: "droid"}, Method: "native", Status: agents.StatusFailed, Reason: "timeout"},
	}
	rep := buildReport(results, []string{"bogus"}, options{})

	wantSummary := map[string]int{"updated": 1, "unchanged": 1, "skipped": 1, "dry-run": 1, "failed": 1}
	if !reflect.DeepEqual(rep.Summary, wantSummary) {
		t.Fatalf("summary = %#v, want %#v", rep.Summary, wantSummary)
	}
	if !reflect.DeepEqual(rep.UnknownNames, []string{"bogus"}) {
		t.Fatalf("unknownNames = %#v, want [bogus]", rep.UnknownNames)
	}
	// dry-run reason is cleared (redundant with status); explain is surfaced.
	var dryAgent jsonAgentResult
	for _, a := range rep.Agents {
		if a.Name == "amp" {
			dryAgent = a
		}
	}
	if dryAgent.Status != "dry-run" || dryAgent.Reason != "" {
		t.Fatalf("dry-run agent = %+v, want status=dry-run reason=''", dryAgent)
	}
	if dryAgent.Explain != "binary amp found" {
		t.Fatalf("dry-run agent explain = %q, want 'binary amp found'", dryAgent.Explain)
	}
	// duration is rounded to whole seconds.
	for _, a := range rep.Agents {
		if a.Name == "claude" && a.DurationSeconds != 8 {
			t.Fatalf("claude durationSeconds = %d, want 8", a.DurationSeconds)
		}
	}
}

func TestJSONStatus(t *testing.T) {
	tests := []struct {
		name string
		res  result
		want string
	}{
		{name: "dry-run", res: result{Status: agents.StatusUpdated, Reason: "dry-run"}, want: "dry-run"},
		{name: "updated", res: result{Status: agents.StatusUpdated}, want: "updated"},
		{name: "unchanged", res: result{Status: agents.StatusUnchanged}, want: "unchanged"},
		{name: "skipped", res: result{Status: agents.StatusSkipped, Reason: "missing"}, want: "skipped"},
		{name: "failed", res: result{Status: agents.StatusFailed}, want: "failed"},
		{name: "empty", res: result{}, want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jsonStatus(tt.res); got != tt.want {
				t.Fatalf("jsonStatus(%+v) = %q, want %q", tt.res, got, tt.want)
			}
		})
	}
}
