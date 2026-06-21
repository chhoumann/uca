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
