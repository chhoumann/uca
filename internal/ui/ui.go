// Package ui renders the live terminal dashboard. It owns a flat view-model
// (Row/Event) so it never depends on the orchestrator's result/options types;
// the caller adapts its events into ui.Event. Everything here is presentation:
// the renderers are pure string functions over Row, and Renderer is the only
// piece that touches the terminal.
package ui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chhoumann/uca/internal/agents"
	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

// Row is one agent's line in the dashboard.
type Row struct {
	Name     string
	Status   string
	Before   string
	After    string
	Reason   string
	Method   string
	Start    time.Time
	Duration time.Duration
	Visible  bool
	Detected bool
}

// Event is a flat update-lifecycle event the dashboard consumes. The caller
// adapts its richer event type into this view-model.
type Event struct {
	Index    int
	Phase    string
	Status   string
	Reason   string
	Method   string
	Before   string
	After    string
	Duration time.Duration
	Time     time.Time
	Show     bool
}

// Renderer draws frames to a terminal, tracking how many lines the last frame
// occupied so it can redraw in place.
type Renderer struct {
	Out        *os.File
	LastLines  int
	UseColor   bool
	UseUnicode bool
	Width      int
}

// NewRenderer builds a Renderer with capabilities detected from the environment.
func NewRenderer(out *os.File) *Renderer {
	return &Renderer{
		Out:        out,
		UseColor:   shouldUseColor(),
		UseUnicode: shouldUseUnicode(),
		Width:      termWidth(out),
	}
}

// Draw redraws the frame in place over the previous one.
func (r *Renderer) Draw(content string) {
	if r.LastLines > 0 {
		fmt.Fprintf(r.Out, "\x1b[%dA", r.LastLines)
	}
	fmt.Fprint(r.Out, "\x1b[0G\x1b[0J")
	fmt.Fprint(r.Out, content)
	r.LastLines = countLines(content)
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	lines := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		lines++
	}
	return lines
}

// HideCursor / ShowCursor toggle the terminal cursor around a live render.
func HideCursor(out *os.File) {
	if out != nil {
		fmt.Fprint(out, "\x1b[?25l")
	}
}

func ShowCursor(out *os.File) {
	if out != nil {
		fmt.Fprint(out, "\x1b[?25h")
	}
}

func shouldUseColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	t := strings.ToLower(os.Getenv("TERM"))
	if t == "" || t == "dumb" {
		return false
	}
	return true
}

func shouldUseUnicode() bool {
	locale := strings.ToUpper(os.Getenv("LC_ALL") + os.Getenv("LC_CTYPE") + os.Getenv("LANG"))
	// Match both the macOS canonical form (en_US.UTF-8) and the glibc/Linux
	// canonical lowercase form (en_US.utf8) so Linux UTF-8 terminals still get
	// the unicode glyphs.
	return strings.Contains(locale, "UTF-8") || strings.Contains(locale, "UTF8")
}

func termWidth(out *os.File) int {
	if out == nil {
		return 80
	}
	width, _, err := term.GetSize(int(out.Fd()))
	if err == nil && width > 0 {
		return width
	}
	if cols := strings.TrimSpace(os.Getenv("COLUMNS")); cols != "" {
		if val, err := strconv.Atoi(cols); err == nil && val > 0 {
			return val
		}
	}
	return 80
}

// ApplyEvent folds an event into a row's display state.
func ApplyEvent(row *Row, ev Event) {
	switch ev.Phase {
	case agents.PhaseDetect:
		row.Visible = ev.Show
		row.Status = agents.StatusPending
		row.Reason = ev.Reason
		row.Method = ev.Method
		row.Before = ev.Before
		if ev.Status == agents.StatusSkipped && ev.Reason == agents.ReasonManualInstall {
			row.Status = agents.StatusSkipped
		}
	case agents.PhaseStart:
		row.Status = agents.StatusUpdating
		row.Before = ev.Before
		row.After = ev.After
		row.Method = ev.Method
		row.Start = ev.Time
	case agents.PhaseFinish:
		row.Status = ev.Status
		row.Before = ev.Before
		row.After = ev.After
		row.Reason = ev.Reason
		row.Method = ev.Method
		row.Duration = ev.Duration
	}
}

// RenderFrame builds one dashboard frame: a boot line until the first visible
// row is detected, then the full dashboard.
func (r *Renderer) RenderFrame(rows []Row, nameWidth int, start time.Time, explain bool, detected, total int) string {
	if detected < total {
		for _, row := range rows {
			if row.Visible {
				return r.renderDashboard(rows, nameWidth, start, explain, detected, total)
			}
		}
		return r.renderBoot(start, detected, total)
	}
	return r.renderDashboard(rows, nameWidth, start, explain, detected, total)
}

func (r *Renderer) renderDashboard(rows []Row, nameWidth int, start time.Time, explain bool, detected, total int) string {
	visibleTotal := 0
	completed := 0
	updated := 0
	unchanged := 0
	failed := 0
	visibleRows := make([]Row, 0, len(rows))
	for _, row := range rows {
		if !row.Visible {
			continue
		}
		visibleRows = append(visibleRows, row)
		visibleTotal++
		if row.Status == agents.StatusUpdated || row.Status == agents.StatusUnchanged || row.Status == agents.StatusSkipped || row.Status == agents.StatusFailed {
			completed++
		}
		switch row.Status {
		case agents.StatusUpdated:
			updated++
		case agents.StatusUnchanged:
			unchanged++
		case agents.StatusFailed:
			failed++
		}
	}
	header := fmt.Sprintf("uca  %s  %d/%d  ok:%d same:%d fail:%d  %s", spinnerGlyph(time.Since(start), r.UseUnicode), completed, visibleTotal, updated, unchanged, failed, fmtElapsed(time.Since(start)))
	// Only advertise detection progress while it is genuinely ongoing. Once any
	// row has finished, a lingering "detecting" suffix is misleading (this shows
	// up in dry-run, where detect and finish events are emitted back-to-back).
	if detected < total && completed == 0 {
		header = fmt.Sprintf("%s  detecting %d/%d", header, detected, total)
	}
	lines := make([]string, 0, visibleTotal+2)
	lines = append(lines, fitLine(header, r.Width, r.UseUnicode), "")
	for _, row := range visibleRows {
		lines = append(lines, formatRow(row, nameWidth, explain, r))
	}
	return strings.Join(lines, "\n") + "\n"
}

func (r *Renderer) renderBoot(start time.Time, detected, total int) string {
	header := fmt.Sprintf("uca  %s  detecting %d/%d  %s", spinnerGlyph(time.Since(start), r.UseUnicode), detected, total, fmtElapsed(time.Since(start)))
	return fitLine(header, r.Width, r.UseUnicode) + "\n"
}

func spinnerGlyph(elapsed time.Duration, unicode bool) string {
	frames := []string{"-", "\\", "|", "/"}
	if unicode {
		frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	}
	index := int(elapsed/(120*time.Millisecond)) % len(frames)
	return frames[index]
}

func formatRow(row Row, nameWidth int, explain bool, r *Renderer) string {
	statusLabel := statusLabelFor(row)
	iconPlain := statusIcon(row, r.UseUnicode)
	iconColored := colorize(iconPlain, statusLabel, r.UseColor)

	// Gate the version separators on the locale like every other glyph, so a
	// non-UTF-8 terminal does not get a stray unicode arrow/ellipsis.
	arrow := "->"
	ellipsis := "..."
	if r.UseUnicode {
		arrow = "→"
		ellipsis = "…"
	}

	version := "--"
	elapsed := "--"
	info := ""
	switch row.Status {
	case agents.StatusPending:
		statusLabel = statusLabelFor(row)
	case agents.StatusUpdating:
		statusLabel = statusLabelFor(row)
		if strings.TrimSpace(row.After) != "" {
			version = fmt.Sprintf("%s %s %s", safeVersion(row.Before), arrow, safeVersion(row.After))
		} else {
			version = fmt.Sprintf("%s %s %s", safeVersion(row.Before), arrow, ellipsis)
		}
		if !row.Start.IsZero() {
			elapsed = fmtElapsed(time.Since(row.Start))
		}
	case agents.StatusUpdated:
		version = fmt.Sprintf("%s %s %s", safeVersion(row.Before), arrow, safeVersion(row.After))
		elapsed = fmtElapsed(row.Duration)
	case agents.StatusUnchanged:
		version = fmt.Sprintf("%s %s %s", safeVersion(row.Before), arrow, safeVersion(row.After))
		elapsed = fmtElapsed(row.Duration)
	case agents.StatusFailed:
		version = fmt.Sprintf("%s %s %s", safeVersion(row.Before), arrow, safeVersion(row.After))
		elapsed = fmtElapsed(row.Duration)
		if row.Reason != "" {
			info = row.Reason
		}
	case agents.StatusSkipped:
		if row.Reason != "" && row.Reason != agents.ReasonManualInstall {
			info = row.Reason
		}
	}

	if explain && info == "" && row.Method != "" {
		info = MethodLabel(row.Method)
	}

	if statusLabel == "dry-run" {
		info = "preview"
	}

	if info != "" {
		info = " (" + info + ")"
	}

	line := fmt.Sprintf("%-*s %s %-9s %s %6s%s", nameWidth, row.Name, iconPlain, statusLabel, version, elapsed, info)
	line = fitLine(line, r.Width, r.UseUnicode)
	if iconPlain != iconColored {
		line = recolorIcon(line, nameWidth, iconPlain, iconColored)
	}
	return line
}

// recolorIcon wraps the status icon in its colored form at its known position
// (immediately after the name column and one space) rather than at the first
// textual match. A blind strings.Replace would corrupt rows whose name contains
// the plain icon token, e.g. the "dr" dry-run icon inside "droid" or the "x"
// failed icon inside "codex" on ASCII (non-unicode) terminals.
func recolorIcon(line string, nameWidth int, iconPlain, iconColored string) string {
	start := nameWidth + 1
	if start+len(iconPlain) <= len(line) && line[start:start+len(iconPlain)] == iconPlain {
		return line[:start] + iconColored + line[start+len(iconPlain):]
	}
	// Fallback (e.g. if the line was truncated before the icon): best effort.
	return strings.Replace(line, iconPlain, iconColored, 1)
}

func statusLabelFor(row Row) string {
	if row.Status == agents.StatusUpdated && row.Reason == agents.ReasonDryRun {
		return "dry-run"
	}
	if row.Status == agents.StatusUnchanged {
		return "same"
	}
	if row.Status == agents.StatusSkipped && row.Reason == agents.ReasonManualInstall {
		return "manual"
	}
	return row.Status
}

func fmtElapsed(d time.Duration) string {
	total := int(d.Seconds())
	if total < 0 {
		total = 0
	}
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	mins := total / 60
	secs := total % 60
	if mins < 60 {
		return fmt.Sprintf("%dm%02ds", mins, secs)
	}
	hours := mins / 60
	mins = mins % 60
	return fmt.Sprintf("%dh%02dm", hours, mins)
}

func fitLine(line string, width int, unicode bool) string {
	if width <= 0 {
		return line
	}
	line = strings.TrimRight(line, "\n")
	if runewidth.StringWidth(line) == width {
		return line
	}
	if runewidth.StringWidth(line) > width {
		ellipsis := "..."
		if unicode {
			ellipsis = "…"
		}
		target := width - runewidth.StringWidth(ellipsis)
		if target < 0 {
			target = 0
		}
		var b strings.Builder
		current := 0
		for _, r := range line {
			rw := runewidth.RuneWidth(r)
			if current+rw > target {
				break
			}
			b.WriteRune(r)
			current += rw
		}
		line = b.String() + ellipsis
	}
	pad := width - runewidth.StringWidth(line)
	if pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	return line
}

func statusIcon(row Row, unicode bool) string {
	status := row.Status
	if status == agents.StatusUpdated && row.Reason == agents.ReasonDryRun {
		status = agents.StatusDryRun
	}
	if status == agents.StatusSkipped && row.Reason == agents.ReasonManualInstall {
		if unicode {
			return "○"
		}
		return "o"
	}
	switch status {
	case agents.StatusPending:
		if unicode {
			return "·"
		}
		return "."
	case agents.StatusUpdating:
		return spinnerGlyph(time.Since(row.Start), unicode)
	case agents.StatusUpdated:
		if unicode {
			return "✓"
		}
		return "ok"
	case agents.StatusUnchanged:
		if unicode {
			return "≡"
		}
		return "="
	case agents.StatusFailed:
		if unicode {
			return "✕"
		}
		return "x"
	case agents.StatusSkipped:
		if unicode {
			return "–"
		}
		return "-"
	case agents.StatusDryRun:
		if unicode {
			return "≈"
		}
		return "dr"
	default:
		return "-"
	}
}

// MethodLabel maps an update method kind to its short display label.
func MethodLabel(method string) string {
	switch method {
	case agents.KindNative:
		return "native"
	case agents.KindBun:
		return "bun"
	case agents.KindBrew:
		return "brew"
	case agents.KindNpm:
		return "npm"
	case agents.KindPnpm:
		return "pnpm"
	case agents.KindYarn:
		return "yarn"
	case agents.KindPip:
		return "pip"
	case agents.KindUv:
		return "uv"
	case agents.KindVSCode:
		return "vscode"
	default:
		return method
	}
}

func colorize(text, status string, enabled bool) string {
	if !enabled {
		return text
	}
	code := ""
	switch status {
	case agents.StatusPending:
		code = "90"
	case agents.StatusUpdating:
		code = "36"
	case agents.StatusUpdated:
		code = "32"
	case agents.StatusUnchanged:
		code = "90"
	case agents.StatusFailed:
		code = "31"
	case agents.StatusSkipped:
		code = "33"
	case agents.StatusDryRun:
		code = "35"
	}
	if code == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func safeVersion(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unknown"
	}
	return v
}
