package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/chhoumann/uca/internal/agents"
)

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
		if res.Status != agents.StatusUnchanged {
			t.Fatalf("%s status = %q, want unchanged", res.Agent.Name, res.Status)
		}
	}
}

func TestRunAllPinnedAgentViaBinDirFallbackStaysPinned(t *testing.T) {
	// A pinned node agent whose binary PATH-resolves outside the manager's
	// global bin dir (e.g. a shim earlier in PATH) is detected via the bin-dir
	// containment fallback. It must keep its pin and run as its own command,
	// not join the @latest batch.
	dir := t.TempDir()
	globalBin := filepath.Join(dir, "npm-global", "bin")
	if err := os.MkdirAll(globalBin, 0o755); err != nil {
		t.Fatalf("mkdir global bin: %v", err)
	}
	writeExec(t, globalBin, "one", "#!/bin/sh\n")
	record := filepath.Join(dir, "npm-calls.txt")
	env := nodeIntegrationEnv(t, map[string]string{
		"npm": fakeNpmWithBinDir(record, globalBin, []string{"pkg-two"}, false, ""),
		"one": "#!/bin/sh\ncase \"$1\" in --version) echo 1.0.0 ;; esac\n",
		"two": "#!/bin/sh\ncase \"$1\" in --version) echo 1.0.0 ;; esac\n",
	})

	list := []agents.Agent{
		{Name: "one", Binary: "one", VersionCmd: []string{"one", "--version"}, Strategies: []agents.UpdateStrategy{{Kind: agents.KindNpm, Package: "pkg-one", Version: "1.2.3"}}},
		{Name: "two", Binary: "two", VersionCmd: []string{"two", "--version"}, Strategies: []agents.UpdateStrategy{{Kind: agents.KindNpm, Package: "pkg-two"}}},
	}
	runAllWithEvents(context.Background(), list, env, options{}, nil)

	data, _ := os.ReadFile(record)
	calls := strings.Split(strings.TrimSpace(string(data)), "\n")
	sort.Strings(calls)
	want := []string{"install -g pkg-one@1.2.3", "install -g pkg-two@latest"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("npm calls = %q, want %q", calls, want)
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
		if res.Status != agents.StatusSkipped {
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
		if res.Status == agents.StatusFailed {
			t.Fatalf("%s unexpectedly failed after fallback", res.Agent.Name)
		}
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

func TestRunAllSkipsNodeUpdateWhenAtLatest(t *testing.T) {
	record, env, list := npmSkipFixture(t, "2.0.0", "2.0.0", "")
	results := runAllWithEvents(context.Background(), list, env, options{}, nil)
	if calls := recordedCalls(t, record); calls != "" {
		t.Fatalf("update must be skipped when installed == latest; npm calls = %q", calls)
	}
	if results[0].Status != agents.StatusUnchanged {
		t.Fatalf("status = %q, want unchanged", results[0].Status)
	}
	if !strings.Contains(results[0].Explain, "already at latest") {
		t.Fatalf("explain = %q, want already-at-latest hint", results[0].Explain)
	}
}

func TestRunAllRunsNodeUpdateWhenOutdated(t *testing.T) {
	record, env, list := npmSkipFixture(t, "2.0.0", "3.0.0", "")
	runAllWithEvents(context.Background(), list, env, options{}, nil)
	if calls := recordedCalls(t, record); calls != "install -g pkg-one@latest" {
		t.Fatalf("outdated package must update; npm calls = %q", calls)
	}
}

func TestRunAllForceRunsUpdateWhenAtLatest(t *testing.T) {
	record, env, list := npmSkipFixture(t, "2.0.0", "2.0.0", "")
	runAllWithEvents(context.Background(), list, env, options{Force: true}, nil)
	if calls := recordedCalls(t, record); calls != "install -g pkg-one@latest" {
		t.Fatalf("--force must run the update; npm calls = %q", calls)
	}
}

func TestRunAllSkipsPinnedNodeUpdateOnlyAtExactPin(t *testing.T) {
	// Installed == pin: nothing to do.
	record, env, list := npmSkipFixture(t, "1.2.3", "9.9.9", "1.2.3")
	runAllWithEvents(context.Background(), list, env, options{}, nil)
	if calls := recordedCalls(t, record); calls != "" {
		t.Fatalf("pinned at target must skip; npm calls = %q", calls)
	}

	// Installed newer than pin: the pin is a downgrade target and must run.
	record, env, list = npmSkipFixture(t, "2.0.0", "9.9.9", "1.2.3")
	runAllWithEvents(context.Background(), list, env, options{}, nil)
	if calls := recordedCalls(t, record); calls != "install -g pkg-one@1.2.3" {
		t.Fatalf("pin mismatch must run the pinned install; npm calls = %q", calls)
	}
}
