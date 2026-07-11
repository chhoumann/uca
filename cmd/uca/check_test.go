package main

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/chhoumann/uca/internal/agents"
)

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
