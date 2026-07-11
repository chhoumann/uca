package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/chhoumann/uca/internal/agents"
)

func TestFormatRowUpdatingShowsTargetVersion(t *testing.T) {
	row := Row{
		Name:   "codex",
		Status: agents.StatusUpdating,
		Before: "codex-cli 0.90.0-alpha.5",
		After:  "codex-cli 0.98.0",
		Start:  time.Now(),
	}
	r := &Renderer{Width: 200, UseColor: false, UseUnicode: true}
	got := r.formatRow(row, len(row.Name), false)
	if !strings.Contains(got, "codex-cli 0.90.0-alpha.5 → codex-cli 0.98.0") {
		t.Fatalf("formatRow() did not include target version; got %q", got)
	}
}

func TestRecolorIconDoesNotCorruptName(t *testing.T) {
	// droid in dry-run: ASCII icon is "dr", which also starts the name "droid".
	row := Row{Name: "droid", Status: agents.StatusUpdated, Reason: agents.ReasonDryRun, Before: "1.0.0", After: "1.0.0"}
	r := &Renderer{Width: 200, UseColor: true, UseUnicode: false}
	line := r.formatRow(row, len(row.Name), false)
	if !strings.Contains(line, "droid \x1b[35mdr\x1b[0m") {
		t.Fatalf("formatRow() did not color the icon at its position; got %q", line)
	}
	if strings.Contains(line, "\x1b[35mdr\x1b[0moid") {
		t.Fatalf("formatRow() corrupted the name by coloring 'dr' inside 'droid'; got %q", line)
	}
}

func TestFormatRowColorsIconBySemanticStatus(t *testing.T) {
	r := &Renderer{Width: 200, UseColor: true, UseUnicode: false}

	// Unchanged rows render the "same" label but must still color the icon gray.
	unchanged := Row{Name: "codex", Status: agents.StatusUnchanged, Before: "1.0.0", After: "1.0.0"}
	line := r.formatRow(unchanged, len(unchanged.Name), false)
	if !strings.Contains(line, "codex \x1b[90m=\x1b[0m") {
		t.Fatalf("formatRow() unchanged row icon should be gray; got %q", line)
	}

	// Manual-install skipped rows render the "manual" label but must keep the
	// skipped yellow on the icon.
	manual := Row{Name: "amp", Status: agents.StatusSkipped, Reason: agents.ReasonManualInstall}
	line = r.formatRow(manual, len(manual.Name), false)
	if !strings.Contains(line, "amp \x1b[33mo\x1b[0m") {
		t.Fatalf("formatRow() manual-install row icon should be yellow; got %q", line)
	}
}

func TestShouldUseUnicodeLocalePrecedence(t *testing.T) {
	// LC_ALL wins: LC_ALL=C must disable unicode even when LANG is UTF-8.
	t.Setenv("LC_ALL", "C")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "en_US.UTF-8")
	if shouldUseUnicode() {
		t.Fatal("shouldUseUnicode() = true with LC_ALL=C, want false despite LANG=en_US.UTF-8")
	}

	// With LC_ALL and LC_CTYPE unset, LANG decides.
	t.Setenv("LC_ALL", "")
	if !shouldUseUnicode() {
		t.Fatal("shouldUseUnicode() = false with LANG=en_US.UTF-8, want true")
	}
}

func TestFormatRowUsesAsciiArrowWithoutUnicode(t *testing.T) {
	row := Row{Name: "codex", Status: agents.StatusUpdated, Before: "1.0.0", After: "1.1.0"}
	r := &Renderer{Width: 200, UseColor: false, UseUnicode: false}
	line := r.formatRow(row, len(row.Name), false)
	if !strings.Contains(line, "1.0.0 -> 1.1.0") {
		t.Fatalf("formatRow() ASCII should use '->'; got %q", line)
	}
	if strings.Contains(line, "→") {
		t.Fatalf("formatRow() leaked a unicode arrow under !UseUnicode; got %q", line)
	}
}

func TestRenderDashboardSuppressesDetectingAfterCompletion(t *testing.T) {
	r := &Renderer{Width: 200, UseColor: false, UseUnicode: false}
	start := time.Now()
	rows := []Row{
		{Name: "claude", Status: agents.StatusUpdated, Visible: true, Before: "1", After: "1"},
		{Name: "codex", Status: agents.StatusPending, Visible: true},
	}
	out := r.renderDashboard(rows, 6, start, false, 1, 2)
	if strings.Contains(out, "detecting") {
		t.Fatalf("renderDashboard showed 'detecting' after a row completed; got %q", out)
	}

	pending := []Row{
		{Name: "claude", Status: agents.StatusPending, Visible: true},
		{Name: "codex", Status: agents.StatusPending, Visible: true},
	}
	out = r.renderDashboard(pending, 6, start, false, 1, 2)
	if !strings.Contains(out, "detecting 1/2") {
		t.Fatalf("renderDashboard should advertise detection progress before completion; got %q", out)
	}
}

func TestApplyEvent(t *testing.T) {
	// detect of a manual-install agent -> visible, shown as skipped.
	row := Row{}
	ApplyEvent(&row, Event{Phase: agents.PhaseDetect, Show: true, Status: agents.StatusSkipped, Reason: agents.ReasonManualInstall, Method: agents.KindNative})
	if !row.Visible || row.Status != agents.StatusSkipped || row.Reason != agents.ReasonManualInstall {
		t.Fatalf("detect(manual) row = %+v", row)
	}

	// detect of a normal updatable agent -> pending.
	row = Row{}
	ApplyEvent(&row, Event{Phase: agents.PhaseDetect, Show: true, Method: agents.KindNpm, Before: "1.0.0"})
	if !row.Visible || row.Status != agents.StatusPending || row.Before != "1.0.0" {
		t.Fatalf("detect(normal) row = %+v", row)
	}

	// start -> updating, target version captured.
	start := time.Now()
	ApplyEvent(&row, Event{Phase: agents.PhaseStart, Time: start, Before: "1.0.0", After: "1.1.0", Method: agents.KindNpm})
	if row.Status != agents.StatusUpdating || row.After != "1.1.0" || row.Start != start {
		t.Fatalf("start row = %+v", row)
	}

	// finish -> final status + duration.
	ApplyEvent(&row, Event{Phase: agents.PhaseFinish, Status: agents.StatusUpdated, Before: "1.0.0", After: "1.1.0", Duration: 3 * time.Second})
	if row.Status != agents.StatusUpdated || row.Duration != 3*time.Second {
		t.Fatalf("finish row = %+v", row)
	}
}

func TestRenderFrameBootVsDashboard(t *testing.T) {
	r := &Renderer{Width: 200, UseColor: false, UseUnicode: false}
	start := time.Now()

	// detected < total and no visible row yet -> boot line only.
	rows := []Row{{Name: "a", Visible: false}, {Name: "b", Visible: false}}
	boot := r.RenderFrame(rows, 1, start, false, 0, 2)
	if !strings.Contains(boot, "detecting 0/2") {
		t.Fatalf("boot frame missing 'detecting 0/2': %q", boot)
	}
	if strings.Contains(boot, "\na ") {
		t.Fatalf("boot frame should not render rows: %q", boot)
	}

	// detected < total with a visible row -> dashboard, still advertising detection.
	rows[0].Visible = true
	rows[0].Status = agents.StatusPending
	dash := r.RenderFrame(rows, 1, start, false, 1, 2)
	if !strings.Contains(dash, "uca") || !strings.Contains(dash, "detecting 1/2") {
		t.Fatalf("partial dashboard = %q", dash)
	}

	// all detected -> dashboard with no detecting suffix.
	full := r.RenderFrame(rows, 1, start, false, 2, 2)
	if strings.Contains(full, "detecting") {
		t.Fatalf("full dashboard should not show 'detecting': %q", full)
	}
}
