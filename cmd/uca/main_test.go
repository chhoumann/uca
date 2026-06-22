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

func newTestEnv() *detect.Env {
	return detect.New(context.Background())
}

func TestApplyEnvDefaults(t *testing.T) {
	t.Setenv("UCA_TIMEOUT", "42s")
	t.Setenv("UCA_CONCURRENCY", "3")
	t.Setenv("UCA_SKIP", "claude,codex")
	t.Setenv("UCA_SERIAL", "1")
	t.Setenv("UCA_SAFE", "yes")

	opts, err := parseFlags([]string{})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.Timeout != 42*time.Second {
		t.Fatalf("Timeout = %v, want 42s from env", opts.Timeout)
	}
	if opts.Concurrency != 3 {
		t.Fatalf("Concurrency = %d, want 3 from env", opts.Concurrency)
	}
	if opts.Skip != "claude,codex" {
		t.Fatalf("Skip = %q, want from env", opts.Skip)
	}
	if !opts.Serial || !opts.Safe {
		t.Fatalf("Serial/Safe not applied from env: %+v", opts)
	}

	// Explicit flags win over the environment.
	opts, err = parseFlags([]string{"--timeout", "5s", "--skip", "amp"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.Timeout != 5*time.Second {
		t.Fatalf("flag --timeout should override env; got %v", opts.Timeout)
	}
	if opts.Skip != "amp" {
		t.Fatalf("flag --skip should override env; got %q", opts.Skip)
	}
	if !opts.Serial {
		t.Fatalf("UCA_SERIAL should still apply when --serial absent; got %+v", opts)
	}
}

func TestApplyEnvDefaultsIgnoresInvalid(t *testing.T) {
	t.Setenv("UCA_TIMEOUT", "not-a-duration")
	opts, err := parseFlags([]string{})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.Timeout != 15*time.Minute {
		t.Fatalf("invalid UCA_TIMEOUT should keep the default 15m; got %v", opts.Timeout)
	}
}

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

func TestRunCheckDetectsOutdated(t *testing.T) {
	env := nodeIntegrationEnv(t, map[string]string{
		"npm": fakeNpm("", []string{"pkg-one"}, false, "2.0.0"),
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

func writeExec(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// fakePathEnv writes fake executables to a temp dir, points PATH at it, and
// returns a detect.Env that probes that PATH (so capabilities are auto-detected
// from the fakes — no field-poking needed).
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
	return dir, detect.New(context.Background())
}

func TestResolveUpdateBrew(t *testing.T) {
	_, env := fakePathEnv(t, map[string]string{
		"brew": "#!/bin/sh\ncase \"$1\" in\n  list) echo \"copilot-cli 1.2.3\" ;;\nesac\n",
	})
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
		"python3": "#!/bin/sh\nif [ \"$3\" = \"list\" ]; then echo \"aider-chat==1.2.3\"; exit 0; fi\nexit 1\n",
	})
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
	deps := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		deps = append(deps, "\""+p+"\":{}")
	}
	depsJSON := "{\"dependencies\":{" + strings.Join(deps, ",") + "}}"
	install := ":"
	if record != "" {
		install = "echo \"$@\" >> '" + record + "'"
	}
	if failBatch {
		install += "; if [ $# -gt 3 ]; then exit 1; fi"
	}
	return "#!/bin/sh\ncase \"$1\" in\n" +
		"  bin) ;;\n" +
		"  prefix) ;;\n" +
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

func TestRunAllWithEventsBatchesNodeUpdates(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "npm-calls.txt")
	env := nodeIntegrationEnv(t, map[string]string{
		"npm": fakeNpm(record, []string{"pkg-one", "pkg-two"}, false, ""),
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

func TestRunAllWithEventsCanceledKeepsPerAgentResults(t *testing.T) {
	// A context canceled before scheduling must not collapse every unscheduled
	// agent onto results[0]; each agent keeps its own slot (as skipped).
	env := nodeIntegrationEnv(t, map[string]string{
		"npm": fakeNpm("", []string{"pkg-one", "pkg-two"}, false, ""),
		"one": "#!/bin/sh\ncase \"$1\" in --version) echo 1.0.0 ;; esac\n",
		"two": "#!/bin/sh\ncase \"$1\" in --version) echo 1.0.0 ;; esac\n",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := runAllWithEvents(ctx, nodeTestAgents(), env, options{}, nil)
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].Agent.Name != "one" || results[1].Agent.Name != "two" {
		t.Fatalf("results collapsed: [0]=%q [1]=%q", results[0].Agent.Name, results[1].Agent.Name)
	}
	for _, res := range results {
		if res.Status != statusSkipped {
			t.Fatalf("%s status = %q, want skipped", res.Agent.Name, res.Status)
		}
	}
}

func TestRunAllWithEventsBatchFailureFallsBackPerPackage(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "npm-calls.txt")
	// The batch (>1 package) fails; each single-package retry succeeds.
	env := nodeIntegrationEnv(t, map[string]string{
		"npm": fakeNpm(record, []string{"pkg-one", "pkg-two"}, true, ""),
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

func TestRunAllWithEventsExcludesPinnedFromBatch(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "npm-calls.txt")
	env := nodeIntegrationEnv(t, map[string]string{
		"npm":   fakeNpm(record, []string{"pkg-one", "pkg-two", "pkg-three"}, false, ""),
		"one":   "#!/bin/sh\ncase \"$1\" in --version) echo 1.0.0 ;; esac\n",
		"two":   "#!/bin/sh\ncase \"$1\" in --version) echo 1.0.0 ;; esac\n",
		"three": "#!/bin/sh\ncase \"$1\" in --version) echo 1.0.0 ;; esac\n",
	})
	list := []agents.Agent{
		{Name: "one", Binary: "one", VersionCmd: []string{"one", "--version"}, Strategies: []agents.UpdateStrategy{{Kind: agents.KindNpm, Package: "pkg-one", Version: "9.9.9"}}},
		{Name: "two", Binary: "two", VersionCmd: []string{"two", "--version"}, Strategies: []agents.UpdateStrategy{{Kind: agents.KindNpm, Package: "pkg-two"}}},
		{Name: "three", Binary: "three", VersionCmd: []string{"three", "--version"}, Strategies: []agents.UpdateStrategy{{Kind: agents.KindNpm, Package: "pkg-three"}}},
	}
	runAllWithEvents(context.Background(), list, env, options{}, nil)

	calls := strings.Split(strings.TrimSpace(string(mustRead(t, record))), "\n")
	var pinned, batch bool
	for _, c := range calls {
		if c == "install -g pkg-one@9.9.9" {
			pinned = true
		}
		// the two unpinned agents batch together (package order is sorted)
		if strings.Contains(c, "pkg-two@latest") && strings.Contains(c, "pkg-three@latest") {
			batch = true
		}
		if strings.Contains(c, "pkg-one") && strings.Contains(c, "pkg-two") {
			t.Fatalf("pinned agent was batched: %q", c)
		}
	}
	if !pinned {
		t.Fatalf("missing pinned single call; calls=%q", calls)
	}
	if !batch {
		t.Fatalf("missing batched @latest call; calls=%q", calls)
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
