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

// statusDryRun and statusManual are presentation-only statuses derived from
// (StatusUpdated, ReasonDryRun) and (StatusSkipped, ReasonManualInstall) for
// label, icon, and color selection; the orchestrator never emits them.
const (
	statusDryRun = "dry-run"
	statusManual = "manual"
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
	lastLines  int
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
	if r.lastLines > 0 {
		fmt.Fprintf(r.Out, "\x1b[%dA", r.lastLines)
	}
	fmt.Fprint(r.Out, "\x1b[0G\x1b[0J")
	fmt.Fprint(r.Out, content)
	r.lastLines = countLines(content)
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

// HideCursor hides the terminal cursor for the duration of a live render.
func (r *Renderer) HideCursor() {
	if r.Out != nil {
		fmt.Fprint(r.Out, "\x1b[?25l")
	}
}

// ShowCursor restores the terminal cursor after a live render.
func (r *Renderer) ShowCursor() {
	if r.Out != nil {
		fmt.Fprint(r.Out, "\x1b[?25h")
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
	// POSIX precedence: LC_ALL overrides LC_CTYPE, which overrides LANG. The
	// first non-empty variable decides; concatenating them would let a UTF-8
	// LANG leak past LC_ALL=C.
	locale := os.Getenv("LC_ALL")
	if locale == "" {
		locale = os.Getenv("LC_CTYPE")
	}
	if locale == "" {
		locale = os.Getenv("LANG")
	}
	locale = strings.ToUpper(locale)
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
	header := fmt.Sprintf("uca  %s  %d/%d  ok:%d same:%d fail:%d  %s", spinnerGlyph(time.Since(start), r.UseUnicode), completed, len(visibleRows), updated, unchanged, failed, fmtElapsed(time.Since(start)))
	// Only advertise detection progress while it is genuinely ongoing. Once any
	// row has finished, a lingering "detecting" suffix is misleading (this shows
	// up in dry-run, where detect and finish events are emitted back-to-back).
	if detected < total && completed == 0 {
		header = fmt.Sprintf("%s  detecting %d/%d", header, detected, total)
	}
	lines := make([]string, 0, len(visibleRows)+2)
	lines = append(lines, r.fitLine(header), "")
	for _, row := range visibleRows {
		lines = append(lines, r.formatRow(row, nameWidth, explain))
	}
	return strings.Join(lines, "\n") + "\n"
}

func (r *Renderer) renderBoot(start time.Time, detected, total int) string {
	header := fmt.Sprintf("uca  %s  detecting %d/%d  %s", spinnerGlyph(time.Since(start), r.UseUnicode), detected, total, fmtElapsed(time.Since(start)))
	return r.fitLine(header) + "\n"
}

func spinnerGlyph(elapsed time.Duration, unicode bool) string {
	frames := []string{"-", "\\", "|", "/"}
	if unicode {
		frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	}
	index := int(elapsed/(120*time.Millisecond)) % len(frames)
	return frames[index]
}

// arrow and ellipsis pick the separator glyphs for the renderer's charset, so
// a non-UTF-8 terminal never gets a stray unicode glyph.
func (r *Renderer) arrow() string {
	if r.UseUnicode {
		return "→"
	}
	return "->"
}

func (r *Renderer) ellipsis() string {
	if r.UseUnicode {
		return "…"
	}
	return "..."
}

// versionTransition renders the "before -> after" version column.
func (r *Renderer) versionTransition(before, after string) string {
	return fmt.Sprintf("%s %s %s", safeVersion(before), r.arrow(), safeVersion(after))
}

func (r *Renderer) formatRow(row Row, nameWidth int, explain bool) string {
	status := displayStatus(row)
	label := statusLabel(status)
	iconPlain := statusIcon(status, row.Start, r.UseUnicode)
	iconColored := colorize(iconPlain, status, r.UseColor)

	version := "--"
	elapsed := "--"
	info := ""
	switch status {
	case agents.StatusUpdating:
		if strings.TrimSpace(row.After) != "" {
			version = r.versionTransition(row.Before, row.After)
		} else {
			version = fmt.Sprintf("%s %s %s", safeVersion(row.Before), r.arrow(), r.ellipsis())
		}
		if !row.Start.IsZero() {
			elapsed = fmtElapsed(time.Since(row.Start))
		}
	case agents.StatusUpdated, agents.StatusUnchanged, statusDryRun:
		version = r.versionTransition(row.Before, row.After)
		elapsed = fmtElapsed(row.Duration)
	case agents.StatusFailed:
		version = r.versionTransition(row.Before, row.After)
		elapsed = fmtElapsed(row.Duration)
		if row.Reason != "" {
			info = row.Reason
		}
	case agents.StatusSkipped:
		if row.Reason != "" {
			info = row.Reason
		}
	}

	if explain && info == "" && row.Method != "" {
		info = MethodLabel(row.Method)
	}

	if status == statusDryRun {
		info = "preview"
	}

	if info != "" {
		info = " (" + info + ")"
	}

	line := fmt.Sprintf("%-*s %s %-9s %s %6s%s", nameWidth, row.Name, iconPlain, label, version, elapsed, info)
	line = r.fitLine(line)
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

// displayStatus derives the one semantic status a row is presented as:
// updated+dry-run renders as dry-run, skipped+manual-install as manual. Label,
// icon, and color all key on this value so they cannot drift apart.
func displayStatus(row Row) string {
	if row.Status == agents.StatusUpdated && row.Reason == agents.ReasonDryRun {
		return statusDryRun
	}
	if row.Status == agents.StatusSkipped && row.Reason == agents.ReasonManualInstall {
		return statusManual
	}
	return row.Status
}

// statusLabel maps a display status to the text shown in the status column.
func statusLabel(status string) string {
	if status == agents.StatusUnchanged {
		return "same"
	}
	return status
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

// fitLine truncates the line to the renderer's width (ending in an ellipsis)
// or pads it with spaces to exactly that width.
func (r *Renderer) fitLine(line string) string {
	width := r.Width
	if width <= 0 {
		return line
	}
	line = strings.TrimRight(line, "\n")
	w := runewidth.StringWidth(line)
	if w > width {
		ellipsis := r.ellipsis()
		ellipsisWidth := runewidth.StringWidth(ellipsis)
		target := width - ellipsisWidth
		if target < 0 {
			target = 0
		}
		var b strings.Builder
		current := 0
		for _, c := range line {
			cw := runewidth.RuneWidth(c)
			if current+cw > target {
				break
			}
			b.WriteRune(c)
			current += cw
		}
		line = b.String() + ellipsis
		w = current + ellipsisWidth
	}
	if pad := width - w; pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	return line
}

// statusIcon picks the icon glyph for a display status; start feeds the
// updating spinner's animation phase.
func statusIcon(status string, start time.Time, unicode bool) string {
	switch status {
	case agents.StatusPending:
		if unicode {
			return "·"
		}
		return "."
	case agents.StatusUpdating:
		return spinnerGlyph(time.Since(start), unicode)
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
	case statusManual:
		if unicode {
			return "○"
		}
		return "o"
	case statusDryRun:
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
	case statusManual:
		code = "33"
	case statusDryRun:
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
