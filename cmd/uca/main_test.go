package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chhoumann/uca/internal/agents"
)

func TestParseVersionOutput(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "empty",
			out:  "",
			want: "unknown",
		},
		{
			name: "version_only",
			out:  "1.2.3\n",
			want: "1.2.3",
		},
		{
			name: "version_only_with_v",
			out:  "v2.0.1\n",
			want: "v2.0.1",
		},
		{
			name: "first_line_default",
			out:  "claude 2.1.19\n",
			want: "claude 2.1.19",
		},
		{
			name: "selects_last_version_only_line",
			out:  "INFO something\n1.1.36\n",
			want: "1.1.36",
		},
		{
			name: "skips_blank_lines",
			out:  "\n\n1.4.0\n\n",
			want: "1.4.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseVersionOutput(tt.out); got != tt.want {
				t.Fatalf("parseVersionOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractVersionToken(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "", want: "", ok: false},
		{in: "codex-cli 0.90.0-alpha.5", want: "0.90.0-alpha.5", ok: true},
		{in: "v2.0.1", want: "v2.0.1", ok: true},
		{in: "no version here", want: "", ok: false},
	}
	for _, tt := range tests {
		got, ok := extractVersionToken(tt.in)
		if ok != tt.ok {
			t.Fatalf("extractVersionToken(%q) ok=%v, want %v (got %q)", tt.in, ok, tt.ok, got)
		}
		if got != tt.want {
			t.Fatalf("extractVersionToken(%q)=%q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatVersionWithToken(t *testing.T) {
	tests := []struct {
		before string
		newVer string
		want   string
	}{
		{before: "codex-cli 0.90.0-alpha.5", newVer: "0.98.0", want: "codex-cli 0.98.0"},
		{before: "v2.0.1", newVer: "2.0.2", want: "v2.0.2"},
		{before: "unknown", newVer: "1.2.3", want: "1.2.3"},
		{before: "", newVer: "1.2.3", want: "1.2.3"},
	}
	for _, tt := range tests {
		if got := formatVersionWithToken(tt.before, tt.newVer); got != tt.want {
			t.Fatalf("formatVersionWithToken(%q,%q)=%q, want %q", tt.before, tt.newVer, got, tt.want)
		}
	}
}

func TestFormatRowUpdatingShowsTargetVersion(t *testing.T) {
	row := uiRow{
		name:   "codex",
		status: "updating",
		before: "codex-cli 0.90.0-alpha.5",
		after:  "codex-cli 0.98.0",
		start:  time.Now(),
	}
	r := &uiRenderer{width: 200, useColor: false, useUnicode: true}

	got := formatRow(row, len(row.name), options{}, r)
	if !strings.Contains(got, "codex-cli 0.90.0-alpha.5 → codex-cli 0.98.0") {
		t.Fatalf("formatRow() did not include target version; got %q", got)
	}
}

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

func TestResolveUpdateCursorPrefersAgentWhenBothInstalled(t *testing.T) {
	cursor := defaultAgent(t, "cursor")
	withFakeCommands(t, map[string]fakeCommand{
		"agent":        {help: "Usage: agent\nStart the Cursor Agent", version: "2026.6.15"},
		"cursor-agent": {help: "Cursor Agent", version: "2026.6.14"},
	})

	resolved := resolveUpdate(cursor, newTestEnv())
	if !reflect.DeepEqual(resolved.cmd, []string{"agent", "update"}) {
		t.Fatalf("cmd = %#v, want %#v", resolved.cmd, []string{"agent", "update"})
	}
	if !reflect.DeepEqual(resolved.versionCmd, []string{"agent", "--version"}) {
		t.Fatalf("versionCmd = %#v, want %#v", resolved.versionCmd, []string{"agent", "--version"})
	}
	if !strings.Contains(resolved.detail, "binary agent found") {
		t.Fatalf("detail = %q, want selected agent binary", resolved.detail)
	}
}

func TestResolveUpdateCursorUsesAgentWhenOnlyAgentInstalled(t *testing.T) {
	cursor := defaultAgent(t, "cursor")
	withFakeCommands(t, map[string]fakeCommand{
		"agent": {help: "Usage: agent\nStart the Cursor Agent", version: "2026.6.15"},
	})

	resolved := resolveUpdate(cursor, newTestEnv())
	if !reflect.DeepEqual(resolved.cmd, []string{"agent", "update"}) {
		t.Fatalf("cmd = %#v, want %#v", resolved.cmd, []string{"agent", "update"})
	}
	if !reflect.DeepEqual(resolved.versionCmd, []string{"agent", "--version"}) {
		t.Fatalf("versionCmd = %#v, want %#v", resolved.versionCmd, []string{"agent", "--version"})
	}
}

func TestResolveUpdateCursorFallsBackToCursorAgent(t *testing.T) {
	cursor := defaultAgent(t, "cursor")
	withFakeCommands(t, map[string]fakeCommand{
		"cursor-agent": {help: "Cursor Agent", version: "2026.6.14"},
	})

	resolved := resolveUpdate(cursor, newTestEnv())
	if !reflect.DeepEqual(resolved.cmd, []string{"cursor-agent", "update"}) {
		t.Fatalf("cmd = %#v, want %#v", resolved.cmd, []string{"cursor-agent", "update"})
	}
	if !reflect.DeepEqual(resolved.versionCmd, []string{"cursor-agent", "--version"}) {
		t.Fatalf("versionCmd = %#v, want %#v", resolved.versionCmd, []string{"cursor-agent", "--version"})
	}
}

func TestResolveUpdateCursorFallsBackWhenAgentIdentityFails(t *testing.T) {
	cursor := defaultAgent(t, "cursor")
	withFakeCommands(t, map[string]fakeCommand{
		"agent":        {help: "Usage: agent\nOther Agent", version: "1.2.3"},
		"cursor-agent": {help: "Cursor Agent", version: "2026.6.14"},
	})

	resolved := resolveUpdate(cursor, newTestEnv())
	if !reflect.DeepEqual(resolved.cmd, []string{"cursor-agent", "update"}) {
		t.Fatalf("cmd = %#v, want %#v", resolved.cmd, []string{"cursor-agent", "update"})
	}
	if resolved.reason != "" {
		t.Fatalf("reason = %q, want empty", resolved.reason)
	}
}

func TestResolveUpdateCursorRejectsUnrelatedAgent(t *testing.T) {
	cursor := defaultAgent(t, "cursor")
	withFakeCommands(t, map[string]fakeCommand{
		"agent": {help: "Usage: agent\nOther Agent", version: "1.2.3"},
	})

	resolved := resolveUpdate(cursor, newTestEnv())
	if resolved.cmd != nil {
		t.Fatalf("cmd = %#v, want nil", resolved.cmd)
	}
	if resolved.reason != reasonMissing {
		t.Fatalf("reason = %q, want %q", resolved.reason, reasonMissing)
	}
	if !strings.Contains(resolved.detail, "did not identify Cursor Agent") {
		t.Fatalf("detail = %q, want Cursor Agent identity miss", resolved.detail)
	}
}

func TestResolveUpdateNativeStrategyDefaultsToAgentFields(t *testing.T) {
	amp := defaultAgent(t, "amp")
	withFakeCommands(t, map[string]fakeCommand{
		"amp": {help: "Usage: amp", version: "1.2.3"},
	})

	resolved := resolveUpdate(amp, newTestEnv())
	if !reflect.DeepEqual(resolved.cmd, []string{"amp", "update"}) {
		t.Fatalf("cmd = %#v, want %#v", resolved.cmd, []string{"amp", "update"})
	}
	if !reflect.DeepEqual(resolved.versionCmd, []string{"amp", "--version"}) {
		t.Fatalf("versionCmd = %#v, want %#v", resolved.versionCmd, []string{"amp", "--version"})
	}
	if !strings.Contains(resolved.detail, "binary amp found") {
		t.Fatalf("detail = %q, want selected amp binary", resolved.detail)
	}
}

func TestRunAllDryRunCursorUsesSelectedVersionCommand(t *testing.T) {
	cursor := defaultAgent(t, "cursor")
	withFakeCommands(t, map[string]fakeCommand{
		"agent": {help: "Usage: agent\nStart the Cursor Agent", version: "2026.6.15"},
	})

	results := runAllWithEvents(context.Background(), []agents.Agent{cursor}, newTestEnv(), options{DryRun: true}, nil)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Agent.Name != "cursor" {
		t.Fatalf("Agent.Name = %q, want cursor", results[0].Agent.Name)
	}
	if results[0].UpdateCmd != "agent update" {
		t.Fatalf("UpdateCmd = %q, want %q", results[0].UpdateCmd, "agent update")
	}
	if results[0].Before != "2026.6.15" || results[0].After != "2026.6.15" {
		t.Fatalf("Before/After = %q/%q, want selected agent version", results[0].Before, results[0].After)
	}
}

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

func newTestEnv() *envState {
	return &envState{
		ctx:          context.Background(),
		binPathCache: map[string]string{},
		helpChecks:   map[string]bool{},
	}
}

func TestValidateOptions(t *testing.T) {
	tests := []struct {
		name    string
		opts    options
		wantErr bool
	}{
		{name: "ok_default", opts: options{}, wantErr: false},
		{name: "ok_serial", opts: options{Serial: true}, wantErr: false},
		{name: "ok_parallel", opts: options{Parallel: true}, wantErr: false},
		{name: "ok_concurrency_zero", opts: options{Concurrency: 0}, wantErr: false},
		{name: "serial_and_parallel", opts: options{Serial: true, Parallel: true}, wantErr: true},
		{name: "negative_concurrency", opts: options{Concurrency: -1}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOptions(tt.opts)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateOptions(%#v) err = %v, wantErr %v", tt.opts, err, tt.wantErr)
			}
		})
	}
}

func TestParseFlagsReportsUnknownFlag(t *testing.T) {
	if _, err := parseFlags([]string{"--definitely-not-a-flag"}); err == nil {
		t.Fatal("parseFlags() with unknown flag returned nil error, want error")
	}
	opts, err := parseFlags([]string{"--serial", "-n", "--only", "claude"})
	if err != nil {
		t.Fatalf("parseFlags() valid args err = %v", err)
	}
	if !opts.Serial || !opts.DryRun || opts.Only != "claude" {
		t.Fatalf("parseFlags() parsed = %#v, want Serial/DryRun/Only=claude", opts)
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

func TestEffectiveConcurrencyNonPositive(t *testing.T) {
	// --safe must win over a non-positive --concurrency rather than being
	// silently overridden into unlimited.
	if got := effectiveConcurrency(options{Safe: true, Concurrency: -1}, 8); got != 1 {
		t.Fatalf("safe + negative concurrency = %d, want 1", got)
	}
	if got := effectiveConcurrency(options{Concurrency: -5}, 6); got != 6 {
		t.Fatalf("negative concurrency (no safe) = %d, want numTasks 6", got)
	}
}

func TestRecolorIconDoesNotCorruptName(t *testing.T) {
	// droid in dry-run: ASCII icon is "dr", which also starts the name "droid".
	row := uiRow{name: "droid", status: statusUpdated, reason: "dry-run", before: "1.0.0", after: "1.0.0"}
	r := &uiRenderer{width: 200, useColor: true, useUnicode: false}

	line := formatRow(row, len(row.name), options{}, r)

	if !strings.Contains(line, "droid \x1b[35mdr\x1b[0m") {
		t.Fatalf("formatRow() did not color the icon at its position; got %q", line)
	}
	if strings.Contains(line, "\x1b[35mdr\x1b[0moid") {
		t.Fatalf("formatRow() corrupted the name by coloring 'dr' inside 'droid'; got %q", line)
	}
}

func TestFormatRowUsesAsciiArrowWithoutUnicode(t *testing.T) {
	row := uiRow{name: "codex", status: statusUpdated, before: "1.0.0", after: "1.1.0"}
	r := &uiRenderer{width: 200, useColor: false, useUnicode: false}

	line := formatRow(row, len(row.name), options{}, r)
	if !strings.Contains(line, "1.0.0 -> 1.1.0") {
		t.Fatalf("formatRow() ASCII should use '->'; got %q", line)
	}
	if strings.Contains(line, "→") {
		t.Fatalf("formatRow() leaked a unicode arrow under !useUnicode; got %q", line)
	}
}

func TestRenderDashboardSuppressesDetectingAfterCompletion(t *testing.T) {
	r := &uiRenderer{width: 200, useColor: false, useUnicode: false}
	start := time.Now()
	rows := []uiRow{
		{name: "claude", status: statusUpdated, visible: true, before: "1", after: "1"},
		{name: "codex", status: "pending", visible: true},
	}
	// detected (1) < total (2) but one visible row already completed: the
	// "detecting" suffix must not be shown (it is misleading at that point).
	out := renderDashboard(rows, 6, start, options{}, r, 1, 2)
	if strings.Contains(out, "detecting") {
		t.Fatalf("renderDashboard showed 'detecting' after a row completed; got %q", out)
	}

	// While nothing has completed yet, detection progress is still advertised.
	pending := []uiRow{
		{name: "claude", status: "pending", visible: true},
		{name: "codex", status: "pending", visible: true},
	}
	out = renderDashboard(pending, 6, start, options{}, r, 1, 2)
	if !strings.Contains(out, "detecting 1/2") {
		t.Fatalf("renderDashboard should advertise detection progress before completion; got %q", out)
	}
}

func TestParseLatestVersionOutput(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{name: "bare", out: "0.141.0\n", want: "0.141.0"},
		{name: "quoted", out: "\"0.79.9\"\n", want: "0.79.9"},
		{name: "v_prefix", out: "v2.0.1\n", want: "v2.0.1"},
		{name: "banner_then_version", out: "npm notice using safe-chain\n0.141.0\n", want: "0.141.0"},
		{name: "version_then_trailing_banner", out: "0.141.0\nnpm notice update available\n", want: "0.141.0"},
		// A trailing banner that itself carries a version must NOT win over the
		// real standalone version line.
		{name: "version_then_versioned_banner", out: "0.141.0\nnpm notice New major version 10.0.0 -> 11.5.2\n", want: "0.141.0"},
		{name: "bun_json_object", out: "{\"version\":\"0.79.9\"}\n", want: "0.79.9"},
		{name: "no_version", out: "no version here\n", want: ""},
		{name: "empty", out: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseLatestVersionOutput(tt.out); got != tt.want {
				t.Fatalf("parseLatestVersionOutput(%q) = %q, want %q", tt.out, got, tt.want)
			}
		})
	}
}

func TestNodeLatestVersionCaches(t *testing.T) {
	env := newTestEnv()
	env.latestCache = map[string]string{agents.KindNpm + "\x00" + "pkg": "9.9.9"}
	// A cached value is returned without running any command (the fake PATH has
	// no npm, so a real query would yield "").
	if got := env.nodeLatestVersion(context.Background(), agents.KindNpm, "pkg"); got != "9.9.9" {
		t.Fatalf("nodeLatestVersion cached = %q, want 9.9.9", got)
	}
}

func TestDryRunPlanLinesGroupsBatch(t *testing.T) {
	batch := "bun add -g @openai/codex@latest opencode-ai@latest"
	results := []result{
		{Agent: agents.Agent{Name: "claude"}, Status: statusUpdated, Reason: "dry-run", UpdateCmd: "claude update"},
		{Agent: agents.Agent{Name: "codex"}, Status: statusUpdated, Reason: "dry-run", UpdateCmd: batch},
		{Agent: agents.Agent{Name: "gemini"}, Status: statusSkipped, Reason: reasonMissing},
		{Agent: agents.Agent{Name: "opencode"}, Status: statusUpdated, Reason: "dry-run", UpdateCmd: batch},
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

func TestParseBunVersionJSON(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{name: "scalar", out: "\"6.0.3\"\n", want: "6.0.3"},
		{name: "object_version", out: "{\"version\":\"0.79.9\"}\n", want: "0.79.9"},
		// A full manifest dump: the top-level version, not a dependency's.
		{name: "manifest", out: "{\"name\":\"pkg\",\"version\":\"0.79.9\",\"dependencies\":{\"dep\":\"0.3.3\"}}\n", want: "0.79.9"},
		{name: "not_json", out: "0.79.9\n", want: ""},
		{name: "empty", out: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseBunVersionJSON(tt.out); got != tt.want {
				t.Fatalf("parseBunVersionJSON(%q) = %q, want %q", tt.out, got, tt.want)
			}
		})
	}
}

func TestBuildReport(t *testing.T) {
	results := []result{
		{Agent: agents.Agent{Name: "claude"}, Method: "native", Status: statusUpdated, Before: "1", After: "2", Duration: 8 * time.Second, UpdateCmd: "claude update"},
		{Agent: agents.Agent{Name: "codex"}, Method: "bun", Status: statusUnchanged, Before: "1", After: "1"},
		{Agent: agents.Agent{Name: "gemini"}, Status: statusSkipped, Reason: "missing"},
		{Agent: agents.Agent{Name: "amp"}, Method: "native", Status: statusUpdated, Reason: "dry-run", Before: "1", After: "1", UpdateCmd: "amp update", Explain: "binary amp found"},
		{Agent: agents.Agent{Name: "droid"}, Method: "native", Status: statusFailed, Reason: "timeout"},
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
		{name: "dry-run", res: result{Status: statusUpdated, Reason: "dry-run"}, want: "dry-run"},
		{name: "updated", res: result{Status: statusUpdated}, want: "updated"},
		{name: "unchanged", res: result{Status: statusUnchanged}, want: "unchanged"},
		{name: "skipped", res: result{Status: statusSkipped, Reason: "missing"}, want: "skipped"},
		{name: "failed", res: result{Status: statusFailed}, want: "failed"},
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

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    checkState
	}{
		{name: "equal", current: "0.141.0", latest: "0.141.0", want: checkUpToDate},
		{name: "embedded_current", current: "codex-cli 0.141.0", latest: "0.141.0", want: checkUpToDate},
		{name: "v_prefix_normalized", current: "v2.0.1", latest: "2.0.1", want: checkUpToDate},
		{name: "outdated", current: "0.140.0", latest: "0.141.0", want: checkOutdated},
		{name: "no_latest", current: "1.0.0", latest: "", want: checkUnknown},
		{name: "unparseable_current", current: "n/a", latest: "1.0.0", want: checkUnknown},
		// Ordering, not equality: a build newer than the published latest is NOT outdated.
		{name: "newer_than_latest", current: "1.2.0", latest: "1.1.0", want: checkUpToDate},
		{name: "newer_prerelease", current: "0.142.0-rc.1", latest: "0.141.0", want: checkUpToDate},
		{name: "build_metadata_ignored", current: "1.2.3+build.5", latest: "1.2.3", want: checkUpToDate},
		{name: "component_count_equiv", current: "1.2", latest: "1.2.0", want: checkUpToDate},
		{name: "prerelease_behind_release", current: "1.2.0-rc.1", latest: "1.2.0", want: checkOutdated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compareVersions(tt.current, tt.latest); got != tt.want {
				t.Fatalf("compareVersions(%q,%q) = %q, want %q", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestHasOutdated(t *testing.T) {
	if hasOutdated([]checkResult{{State: checkUpToDate}, {State: checkUnknown}, {State: checkMissing}}) {
		t.Fatal("hasOutdated = true for no-outdated slice, want false")
	}
	if !hasOutdated([]checkResult{{State: checkUpToDate}, {State: checkOutdated}}) {
		t.Fatal("hasOutdated = false with an outdated entry, want true")
	}
}

func TestLatestVersionDispatch(t *testing.T) {
	env := newTestEnv()
	// Non-knowable methods short-circuit to "".
	for _, m := range []string{agents.KindNative, agents.KindPip, agents.KindUv, agents.KindVSCode} {
		if got := env.latestVersion(context.Background(), m, "pkg"); got != "" {
			t.Fatalf("latestVersion(%q) = %q, want empty", m, got)
		}
	}
}

func TestBrewLatestLivePath(t *testing.T) {
	_, env := fakePathEnv(t, map[string]string{
		"brew": "#!/bin/sh\necho '{\"formulae\":[{\"versions\":{\"stable\":\"1.2.3\"}}]}'\n",
	})
	if got := env.brewLatest(context.Background(), "copilot-cli"); got != "1.2.3" {
		t.Fatalf("brewLatest = %q, want 1.2.3", got)
	}
	if got := env.latestVersion(context.Background(), agents.KindBrew, "copilot-cli"); got != "1.2.3" {
		t.Fatalf("latestVersion(brew) = %q, want 1.2.3", got)
	}
}

func TestRunCheckMissingAgent(t *testing.T) {
	// No managers, binary absent -> missing.
	_, env := fakePathEnv(t, map[string]string{})
	list := []agents.Agent{{Name: "ghost", Binary: "ghost", VersionCmd: []string{"ghost", "--version"}, Strategies: []agents.UpdateStrategy{{Kind: agents.KindNpm, Package: "ghost"}}}}
	results := runCheck(context.Background(), list, env)
	if len(results) != 1 || results[0].State != checkMissing {
		t.Fatalf("runCheck(missing) = %#v, want one checkMissing", results)
	}
	if hasOutdated(results) {
		t.Fatal("missing agent should not be outdated")
	}
}

func TestPrintCheck(t *testing.T) {
	results := []checkResult{
		{Agent: agents.Agent{Name: "codex"}, Method: "bun", State: checkUpToDate, Current: "1.0.0"},
		{Agent: agents.Agent{Name: "copilot"}, Method: "brew", State: checkOutdated, Current: "1.0.0", Latest: "1.1.0"},
		{Agent: agents.Agent{Name: "claude"}, Method: "native", State: checkUnknown, Current: "2.0.0"},
		{Agent: agents.Agent{Name: "gemini"}, State: checkMissing, Reason: "missing"},
	}
	out := captureStdout(t, func() { printCheck(results, []string{"bogus"}, options{}) })
	for _, want := range []string{
		"outdated (1.0.0 -> 1.1.0)",
		"up-to-date (1.0.0)",
		"2.0.0 (latest unknown)",
		"outdated: copilot",
		"up-to-date: codex",
		"skipped (unknown): bogus",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("printCheck output missing %q; got:\n%s", want, out)
		}
	}
	// --explain appends the method label.
	out = captureStdout(t, func() { printCheck(results[:1], nil, options{Explain: true}) })
	if !strings.Contains(out, "[bun]") {
		t.Fatalf("printCheck --explain missing method label; got:\n%s", out)
	}
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

func TestParseBrewLatest(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{name: "v2", out: `{"formulae":[{"versions":{"stable":"1.2.3"}}]}`, want: "1.2.3"},
		{name: "empty_formulae", out: `{"formulae":[]}`, want: ""},
		{name: "bad_json", out: "not json", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseBrewLatest(tt.out); got != tt.want {
				t.Fatalf("parseBrewLatest(%q) = %q, want %q", tt.out, got, tt.want)
			}
		})
	}
}

func TestBuildCheckReport(t *testing.T) {
	results := []checkResult{
		{Agent: agents.Agent{Name: "codex"}, Method: "bun", State: checkUpToDate, Current: "1.0.0", Latest: "1.0.0"},
		{Agent: agents.Agent{Name: "copilot"}, Method: "brew", State: checkOutdated, Current: "1.0.0", Latest: "1.1.0"},
		{Agent: agents.Agent{Name: "claude"}, Method: "native", State: checkUnknown, Current: "2.0.0"},
		{Agent: agents.Agent{Name: "gemini"}, State: checkMissing, Reason: "missing"},
	}
	rep := buildCheckReport(results, []string{"bogus"})
	want := map[string]int{"up-to-date": 1, "outdated": 1, "unknown": 1, "missing": 1}
	if !reflect.DeepEqual(rep.Summary, want) {
		t.Fatalf("summary = %#v, want %#v", rep.Summary, want)
	}
	if !reflect.DeepEqual(rep.UnknownNames, []string{"bogus"}) {
		t.Fatalf("unknownNames = %#v", rep.UnknownNames)
	}
	if !hasOutdated(results) {
		t.Fatal("hasOutdated = false, want true")
	}
}

func TestKnownAgentNamesSorted(t *testing.T) {
	got := knownAgentNames()
	for _, name := range []string{"amp", "claude", "codex", "cursor"} {
		if !strings.Contains(got, name) {
			t.Fatalf("knownAgentNames() = %q, missing %q", got, name)
		}
	}
	if !strings.Contains(got, "aider, amp") {
		t.Fatalf("knownAgentNames() not sorted: %q", got)
	}
}

func TestRunCheckDetectsOutdated(t *testing.T) {
	env := nodeIntegrationEnv(t, map[string]string{
		"npm": "#!/bin/sh\ncase \"$1\" in\n  view) echo 2.0.0 ;;\nesac\n",
		"one": "#!/bin/sh\ncase \"$1\" in --version) echo 1.0.0 ;; esac\n",
	})
	list := []agents.Agent{{Name: "one", Binary: "one", VersionCmd: []string{"one", "--version"}, Strategies: []agents.UpdateStrategy{{Kind: agents.KindNpm, Package: "pkg-one"}}}}
	results := runCheck(context.Background(), list, env)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].State != checkOutdated {
		t.Fatalf("state = %q, want outdated (current=%q latest=%q)", results[0].State, results[0].Current, results[0].Latest)
	}
	if results[0].Current != "1.0.0" || results[0].Latest != "2.0.0" {
		t.Fatalf("current/latest = %q/%q", results[0].Current, results[0].Latest)
	}
}

func TestApplyEvent(t *testing.T) {
	// detect of a manual-install agent -> visible, shown as skipped.
	row := uiRow{}
	applyEvent(&row, updateEvent{Phase: phaseDetect, Show: true, Result: result{Status: statusSkipped, Reason: reasonManualInstall, Method: agents.KindNative}})
	if !row.visible || row.status != statusSkipped || row.reason != reasonManualInstall {
		t.Fatalf("detect(manual) row = %+v", row)
	}

	// detect of a normal updatable agent -> pending.
	row = uiRow{}
	applyEvent(&row, updateEvent{Phase: phaseDetect, Show: true, Result: result{Method: agents.KindNpm, Before: "1.0.0"}})
	if !row.visible || row.status != "pending" || row.before != "1.0.0" {
		t.Fatalf("detect(normal) row = %+v", row)
	}

	// start -> updating, target version captured.
	start := time.Now()
	applyEvent(&row, updateEvent{Phase: phaseStart, Time: start, Result: result{Before: "1.0.0", After: "1.1.0", Method: agents.KindNpm}})
	if row.status != "updating" || row.after != "1.1.0" || row.start != start {
		t.Fatalf("start row = %+v", row)
	}

	// finish -> final status + duration.
	applyEvent(&row, updateEvent{Phase: phaseFinish, Result: result{Status: statusUpdated, Before: "1.0.0", After: "1.1.0", Duration: 3 * time.Second}})
	if row.status != statusUpdated || row.duration != 3*time.Second {
		t.Fatalf("finish row = %+v", row)
	}
}

func TestRenderFrameBootVsDashboard(t *testing.T) {
	r := &uiRenderer{width: 200, useColor: false, useUnicode: false}
	start := time.Now()

	// detected < total and no visible row yet -> boot line only.
	rows := []uiRow{{name: "a", visible: false}, {name: "b", visible: false}}
	boot := renderFrame(rows, 1, start, options{}, r, 0, 2)
	if !strings.Contains(boot, "detecting 0/2") {
		t.Fatalf("boot frame missing 'detecting 0/2': %q", boot)
	}
	if strings.Contains(boot, "\na ") {
		t.Fatalf("boot frame should not render rows: %q", boot)
	}

	// detected < total with a visible row -> dashboard, still advertising detection.
	rows[0].visible = true
	rows[0].status = "pending"
	dash := renderFrame(rows, 1, start, options{}, r, 1, 2)
	if !strings.Contains(dash, "uca") || !strings.Contains(dash, "detecting 1/2") {
		t.Fatalf("partial dashboard = %q", dash)
	}

	// all detected -> dashboard with no detecting suffix.
	full := renderFrame(rows, 1, start, options{}, r, 2, 2)
	if strings.Contains(full, "detecting") {
		t.Fatalf("full dashboard should not show 'detecting': %q", full)
	}
}

func writeExec(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func fakePathEnv(t *testing.T, scripts map[string]string) (string, *envState) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script PATH fixtures are POSIX-only")
	}
	dir := t.TempDir()
	for name, body := range scripts {
		writeExec(t, dir, name, body)
	}
	t.Setenv("PATH", dir)
	return dir, &envState{ctx: context.Background(), binPathCache: map[string]string{}, helpChecks: map[string]bool{}}
}

func TestResolveUpdateBrew(t *testing.T) {
	_, env := fakePathEnv(t, map[string]string{
		"brew": "#!/bin/sh\ncase \"$1\" in\n  list) echo \"copilot-cli 1.2.3\" ;;\nesac\n",
	})
	env.hasBrew = true
	resolved := resolveUpdate(defaultAgent(t, "copilot"), env)
	if !reflect.DeepEqual(resolved.cmd, []string{"brew", "upgrade", "copilot-cli"}) {
		t.Fatalf("brew cmd = %#v", resolved.cmd)
	}
	if resolved.method != agents.KindBrew {
		t.Fatalf("brew method = %q", resolved.method)
	}
}

func TestResolveUpdatePip(t *testing.T) {
	// uv absent, pip present -> pip strategy (the fallback after uv).
	_, env := fakePathEnv(t, map[string]string{
		"python3": "#!/bin/sh\nif [ \"$3\" = \"show\" ]; then exit 0; fi\nexit 1\n",
	})
	env.hasPython = true
	resolved := resolveUpdate(defaultAgent(t, "aider"), env)
	want := []string{"python3", "-m", "pip", "install", "-U", "--upgrade-strategy", "only-if-needed", "aider-chat"}
	if !reflect.DeepEqual(resolved.cmd, want) {
		t.Fatalf("pip cmd = %#v, want %#v", resolved.cmd, want)
	}
	if resolved.method != agents.KindPip {
		t.Fatalf("pip method = %q", resolved.method)
	}
}

func TestResolveUpdateUv(t *testing.T) {
	// uv present -> uv strategy (preferred over pip).
	_, env := fakePathEnv(t, map[string]string{
		"uv": "#!/bin/sh\necho \"aider-chat v1.2.3\"\n",
	})
	env.hasUv = true
	resolved := resolveUpdate(defaultAgent(t, "aider"), env)
	want := []string{"uv", "tool", "install", "--force", "--python", "python3.12", "--with", "pip", "aider-chat@latest"}
	if !reflect.DeepEqual(resolved.cmd, want) {
		t.Fatalf("uv cmd = %#v, want %#v", resolved.cmd, want)
	}
	if resolved.method != agents.KindUv {
		t.Fatalf("uv method = %q", resolved.method)
	}
}

func TestResolveUpdateVSCode(t *testing.T) {
	_, env := fakePathEnv(t, map[string]string{
		"code": "#!/bin/sh\necho \"RooVeterinaryInc.roo-cline@1.2.3\"\n",
	})
	env.codeCmd = "code"
	resolved := resolveUpdate(defaultAgent(t, "roocode"), env)
	want := []string{"code", "--install-extension", "RooVeterinaryInc.roo-cline", "--force"}
	if !reflect.DeepEqual(resolved.cmd, want) {
		t.Fatalf("vscode cmd = %#v, want %#v", resolved.cmd, want)
	}
	if resolved.method != agents.KindVSCode {
		t.Fatalf("vscode method = %q", resolved.method)
	}
}

func TestResolveUpdateVSCodeMissingCode(t *testing.T) {
	_, env := fakePathEnv(t, map[string]string{})
	env.codeCmd = "" // no VS Code CLI
	resolved := resolveUpdate(defaultAgent(t, "roocode"), env)
	if resolved.cmd != nil || resolved.reason != reasonMissingCode {
		t.Fatalf("missing-code resolved = %#v (reason %q), want reasonMissingCode", resolved.cmd, resolved.reason)
	}
}

func TestResolveUpdateManualInstall(t *testing.T) {
	// Binary present but no supported install method -> manual.
	_, env := fakePathEnv(t, map[string]string{
		"gemini": "#!/bin/sh\n",
	})
	resolved := resolveUpdate(defaultAgent(t, "gemini"), env)
	if resolved.cmd != nil || resolved.reason != reasonManualInstall {
		t.Fatalf("manual resolved = %#v (reason %q), want reasonManualInstall", resolved.cmd, resolved.reason)
	}
}

func nodeIntegrationEnv(t *testing.T, scripts map[string]string) *envState {
	t.Helper()
	_, env := fakePathEnv(t, scripts)
	env.hasNpm = true
	// Pre-seed npm detection so resolveUpdate doesn't shell out to `npm bin -g`
	// etc. (which would pollute the recorded npm calls). A dummy bin dir won't
	// match any agent's bin dir, so detection falls to the package list.
	env.npmBin = t.TempDir()
	env.npmBinOnce.Do(func() {})
	env.npmPkgs = map[string]bool{"pkg-one": true, "pkg-two": true}
	env.npmPkgOnce.Do(func() {})
	return env
}

func nodeTestAgents() []agents.Agent {
	return []agents.Agent{
		{Name: "one", Binary: "one", VersionCmd: []string{"one", "--version"}, Strategies: []agents.UpdateStrategy{{Kind: agents.KindNpm, Package: "pkg-one"}}},
		{Name: "two", Binary: "two", VersionCmd: []string{"two", "--version"}, Strategies: []agents.UpdateStrategy{{Kind: agents.KindNpm, Package: "pkg-two"}}},
	}
}

func TestRunAllWithEventsBatchesNodeUpdates(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "npm-calls.txt")
	env := nodeIntegrationEnv(t, map[string]string{
		"npm": "#!/bin/sh\ncase \"$1\" in\n  install) echo \"$@\" >> '" + record + "' ;;\nesac\nexit 0\n",
		"one": "#!/bin/sh\ncase \"$1\" in --version) echo 1.0.0 ;; esac\n",
		"two": "#!/bin/sh\ncase \"$1\" in --version) echo 1.0.0 ;; esac\n",
	})

	results := runAllWithEvents(context.Background(), nodeTestAgents(), env, options{}, nil)

	data, _ := os.ReadFile(record)
	calls := strings.TrimSpace(string(data))
	if calls == "" {
		t.Fatal("npm install was never invoked")
	}
	if strings.Contains(calls, "\n") {
		t.Fatalf("expected ONE batched npm call, got:\n%s", calls)
	}
	if calls != "install -g pkg-one@latest pkg-two@latest" {
		t.Fatalf("batch call = %q", calls)
	}
	for _, res := range results {
		if res.Status != statusUnchanged {
			t.Fatalf("%s status = %q, want unchanged", res.Agent.Name, res.Status)
		}
	}
}

func TestRunAllWithEventsBatchFailureFallsBackPerPackage(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "npm-calls.txt")
	// The batch (>1 package) fails; each single-package retry succeeds.
	env := nodeIntegrationEnv(t, map[string]string{
		"npm": "#!/bin/sh\ncase \"$1\" in\n  install)\n    echo \"$@\" >> '" + record + "'\n    if [ $# -gt 3 ]; then exit 1; fi\n    exit 0 ;;\nesac\nexit 0\n",
		"one": "#!/bin/sh\ncase \"$1\" in --version) echo 1.0.0 ;; esac\n",
		"two": "#!/bin/sh\ncase \"$1\" in --version) echo 1.0.0 ;; esac\n",
	})

	results := runAllWithEvents(context.Background(), nodeTestAgents(), env, options{}, nil)

	data, _ := os.ReadFile(record)
	calls := string(data)
	if !strings.Contains(calls, "install -g pkg-one@latest pkg-two@latest") {
		t.Fatalf("expected a batch attempt, calls:\n%s", calls)
	}
	if !strings.Contains(calls, "install -g pkg-one@latest\n") || !strings.Contains(calls, "install -g pkg-two@latest\n") {
		t.Fatalf("expected per-package fallback calls, calls:\n%s", calls)
	}
	// Both fell back to a successful single update -> not failed.
	for _, res := range results {
		if res.Status == statusFailed {
			t.Fatalf("%s unexpectedly failed after fallback", res.Agent.Name)
		}
	}
}

func TestShouldRetryNpm(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		output string
		want   bool
	}{
		{
			name:   "enotempty",
			args:   []string{"npm", "install", "-g", "pkg"},
			output: "npm error ENOTEMPTY: directory not empty",
			want:   true,
		},
		{
			name:   "enotempty_update",
			args:   []string{"npm", "update", "-g", "pkg"},
			output: "npm error ENOTEMPTY: directory not empty",
			want:   true,
		},
		{
			name:   "errno",
			args:   []string{"npm", "install", "-g", "pkg"},
			output: "npm error errno -66",
			want:   true,
		},
		{
			name:   "directory_not_empty",
			args:   []string{"npm", "install", "-g", "pkg"},
			output: "npm error directory not empty",
			want:   true,
		},
		{
			name:   "no_match",
			args:   []string{"npm", "install", "-g", "pkg"},
			output: "some other error",
			want:   false,
		},
		{
			name:   "not_install",
			args:   []string{"npm", "i", "-g", "pkg"},
			output: "npm error ENOTEMPTY",
			want:   false,
		},
		{
			name:   "not_npm",
			args:   []string{"bun", "install", "-g", "pkg"},
			output: "npm error ENOTEMPTY",
			want:   false,
		},
		{
			name:   "args_too_short",
			args:   []string{"npm"},
			output: "npm error ENOTEMPTY",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRetryNpm(tt.args, tt.output); got != tt.want {
				t.Fatalf("shouldRetryNpm() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatRetryOutput(t *testing.T) {
	tests := []struct {
		name   string
		first  string
		msg    string
		second string
		want   string
	}{
		{
			name:   "first_empty",
			first:  "",
			msg:    "",
			second: "retry output",
			want:   "retry output",
		},
		{
			name:   "second_empty",
			first:  "first output",
			msg:    "",
			second: "   ",
			want:   "first output",
		},
		{
			name:   "both_present",
			first:  "first output",
			msg:    "",
			second: "second output",
			want:   "first output\n\n(uca) retrying npm after ENOTEMPTY\nsecond output",
		},
		{
			name:   "with_cleanup_msg",
			first:  "first output",
			msg:    "removed stale npm temp dir /tmp/.pkg-abc",
			second: "second output",
			want:   "first output\n\n(uca) removed stale npm temp dir /tmp/.pkg-abc\n(uca) retrying npm after ENOTEMPTY\nsecond output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatRetryOutput(tt.first, tt.msg, tt.second)
			if got != tt.want {
				t.Fatalf("formatRetryOutput() = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "\n\n\n") {
				t.Fatalf("formatRetryOutput() has extra newlines: %q", got)
			}
		})
	}
}

func TestClassifyUpdateFailure(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		output     string
		wantReason string
		wantHint   string
	}{
		{
			name:       "quota",
			args:       []string{"gemini", "--version"},
			output:     "TerminalQuotaError: You have exhausted your capacity on this model.",
			wantReason: reasonQuota,
			wantHint:   "quota exceeded",
		},
		{
			name:       "npm_enotempty",
			args:       []string{"npm", "install", "-g", "pkg"},
			output:     "npm error ENOTEMPTY: directory not empty",
			wantReason: reasonNpmNotEmpty,
			wantHint:   "npm rename failed",
		},
		{
			name:       "enotempty_non_npm",
			args:       []string{"gemini", "--version"},
			output:     "ENOTEMPTY",
			wantReason: "",
			wantHint:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotReason, gotHint := classifyUpdateFailure(tt.args, tt.output)
			if gotReason != tt.wantReason {
				t.Fatalf("classifyUpdateFailure() reason = %q, want %q", gotReason, tt.wantReason)
			}
			if tt.wantHint != "" && !strings.Contains(gotHint, tt.wantHint) {
				t.Fatalf("classifyUpdateFailure() hint = %q, want to contain %q", gotHint, tt.wantHint)
			}
			if tt.wantHint == "" && gotHint != "" {
				t.Fatalf("classifyUpdateFailure() hint = %q, want empty", gotHint)
			}
		})
	}
}

func TestAppendHint(t *testing.T) {
	tests := []struct {
		name   string
		detail string
		hint   string
		want   string
	}{
		{
			name:   "empty_detail",
			detail: "",
			hint:   "try again",
			want:   "hint: try again",
		},
		{
			name:   "with_detail",
			detail: "binary found",
			hint:   "try again",
			want:   "binary found; hint: try again",
		},
		{
			name:   "empty_hint",
			detail: "binary found",
			hint:   "",
			want:   "binary found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := appendHint(tt.detail, tt.hint); got != tt.want {
				t.Fatalf("appendHint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsNpmGlobalMutate(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{
			name: "npm_install",
			args: []string{"npm", "install", "-g", "pkg"},
			want: true,
		},
		{
			name: "npm_update",
			args: []string{"npm", "update", "-g", "pkg"},
			want: true,
		},
		{
			name: "npm_i",
			args: []string{"npm", "i", "-g", "pkg"},
			want: false,
		},
		{
			name: "short",
			args: []string{"npm"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNpmGlobalMutate(tt.args); got != tt.want {
				t.Fatalf("isNpmGlobalMutate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNodeUpdateCommand_UsesLatestTag(t *testing.T) {
	tests := []struct {
		name  string
		strat agents.UpdateStrategy
		want  []string
	}{
		{
			name:  "npm",
			strat: agents.UpdateStrategy{Kind: agents.KindNpm, Package: "pkg"},
			want:  []string{"npm", "install", "-g", "pkg@latest"},
		},
		{
			name:  "pnpm",
			strat: agents.UpdateStrategy{Kind: agents.KindPnpm, Package: "pkg"},
			want:  []string{"pnpm", "add", "-g", "pkg@latest"},
		},
		{
			name:  "yarn",
			strat: agents.UpdateStrategy{Kind: agents.KindYarn, Package: "pkg"},
			want:  []string{"yarn", "global", "add", "pkg@latest"},
		},
		{
			name:  "bun",
			strat: agents.UpdateStrategy{Kind: agents.KindBun, Package: "pkg"},
			want:  []string{"bun", "add", "-g", "pkg@latest"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeUpdateCommand(tt.strat); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("nodeUpdateCommand() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNodeBatchUpdateCommand(t *testing.T) {
	tests := []struct {
		name string
		kind string
		pkgs []string
		want []string
	}{
		{name: "npm", kind: agents.KindNpm, pkgs: []string{"a", "b"}, want: []string{"npm", "install", "-g", "a@latest", "b@latest"}},
		{name: "pnpm", kind: agents.KindPnpm, pkgs: []string{"a", "b"}, want: []string{"pnpm", "add", "-g", "a@latest", "b@latest"}},
		{name: "yarn", kind: agents.KindYarn, pkgs: []string{"a", "b"}, want: []string{"yarn", "global", "add", "a@latest", "b@latest"}},
		{name: "bun", kind: agents.KindBun, pkgs: []string{"a", "b"}, want: []string{"bun", "add", "-g", "a@latest", "b@latest"}},
		{name: "npm_skips_empty", kind: agents.KindNpm, pkgs: []string{"a", "", "  ", "b"}, want: []string{"npm", "install", "-g", "a@latest", "b@latest"}},
		{name: "unknown", kind: "nope", pkgs: []string{"a", "b"}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeBatchUpdateCommand(tt.kind, tt.pkgs); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("nodeBatchUpdateCommand() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestEffectiveConcurrency(t *testing.T) {
	tests := []struct {
		name  string
		opts  options
		tasks int
		want  int
	}{
		{name: "serial", opts: options{Serial: true}, tasks: 10, want: 1},
		{name: "safe_default", opts: options{Safe: true}, tasks: 10, want: 1},
		{name: "safe_override", opts: options{Safe: true, Concurrency: 3}, tasks: 10, want: 3},
		{name: "explicit_concurrency", opts: options{Concurrency: 2}, tasks: 10, want: 2},
		{name: "default_unlimited", opts: options{}, tasks: 7, want: 7},
		{name: "no_tasks", opts: options{}, tasks: 0, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveConcurrency(tt.opts, tt.tasks); got != tt.want {
				t.Fatalf("effectiveConcurrency() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNodeManagerForBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping PATH-based binary detection test on windows")
	}
	dir := t.TempDir()
	binName := "fakecli"
	binPath := filepath.Join(dir, binName)
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	origPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+origPath); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Setenv("PATH", origPath)
	})

	env := &envState{
		hasNpm:       true,
		binPathCache: map[string]string{},
		npmBin:       dir,
	}
	env.npmBinOnce.Do(func() {})

	if got := env.nodeManagerForBinary(binName); got != agents.KindNpm {
		t.Fatalf("nodeManagerForBinary() = %q, want %q", got, agents.KindNpm)
	}
}

func TestNodeManagerForBinarySymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping symlink detection test on windows")
	}
	binDir := t.TempDir()
	targetDir := t.TempDir()
	binName := "fakecli"
	targetPath := filepath.Join(targetDir, binName)
	if err := os.WriteFile(targetPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write target binary: %v", err)
	}
	linkPath := filepath.Join(binDir, binName)
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	origPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Setenv("PATH", origPath)
	})

	env := &envState{
		hasNpm:       true,
		binPathCache: map[string]string{},
		npmBin:       targetDir,
	}
	env.npmBinOnce.Do(func() {})

	if got := env.nodeManagerForBinary(binName); got != agents.KindNpm {
		t.Fatalf("nodeManagerForBinary() = %q, want %q", got, agents.KindNpm)
	}
}

func TestParsePackageFromToken(t *testing.T) {
	tests := []struct {
		token string
		want  string
	}{
		{token: "\"@google/gemini-cli@1.2.3\"", want: "@google/gemini-cli"},
		{token: "opencode-ai@0.1.0", want: "opencode-ai"},
		{token: "nope", want: ""},
		{token: "@scope/nover@", want: ""},
	}
	for _, tt := range tests {
		if got := parsePackageFromToken(tt.token); got != tt.want {
			t.Fatalf("parsePackageFromToken(%q) = %q, want %q", tt.token, got, tt.want)
		}
	}
}

func TestExtractNpmRenamePaths(t *testing.T) {
	dir := "/tmp/npm"
	path := filepath.Join(dir, "pi-coding-agent")
	dest := filepath.Join(dir, ".pi-coding-agent-abc")
	tests := []struct {
		name   string
		output string
		wantP  string
		wantD  string
	}{
		{
			name: "path_dest_lines",
			output: "npm error path " + path + "\n" +
				"npm error dest " + dest + "\n",
			wantP: path,
			wantD: dest,
		},
		{
			name:   "rename_line",
			output: "npm error ENOTEMPTY: directory not empty, rename '" + path + "' -> '" + dest + "'\n",
			wantP:  path,
			wantD:  dest,
		},
		{
			name:   "no_match",
			output: "some other error",
			wantP:  "",
			wantD:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotP, gotD := extractNpmRenamePaths(tt.output)
			if gotP != tt.wantP || gotD != tt.wantD {
				t.Fatalf("extractNpmRenamePaths() = %q, %q want %q, %q", gotP, gotD, tt.wantP, tt.wantD)
			}
		})
	}
}

func TestIsSafeNpmRenameTarget(t *testing.T) {
	baseDir := "/tmp/npm"
	path := filepath.Join(baseDir, "pi-coding-agent")
	dest := filepath.Join(baseDir, ".pi-coding-agent-abc")

	tests := []struct {
		name string
		p    string
		d    string
		want bool
	}{
		{
			name: "ok",
			p:    path,
			d:    dest,
			want: true,
		},
		{
			name: "different_dir",
			p:    path,
			d:    filepath.Join("/tmp/other", ".pi-coding-agent-abc"),
			want: false,
		},
		{
			name: "wrong_prefix",
			p:    path,
			d:    filepath.Join(baseDir, ".other-abc"),
			want: false,
		},
		{
			name: "relative",
			p:    "pi-coding-agent",
			d:    ".pi-coding-agent-abc",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSafeNpmRenameTarget(tt.p, tt.d); got != tt.want {
				t.Fatalf("isSafeNpmRenameTarget() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCleanupNpmENotEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pi-coding-agent")
	dest := filepath.Join(dir, ".pi-coding-agent-abc")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	output := "npm error path " + path + "\n" +
		"npm error dest " + dest + "\n"
	msg := cleanupNpmENotEmpty(output)
	if msg == "" {
		t.Fatalf("cleanupNpmENotEmpty() returned empty message")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatalf("cleanupNpmENotEmpty() did not remove %q", dest)
	}
}
