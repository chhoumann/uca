package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chhoumann/uca/internal/agents"
	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
	"strconv"
)

type options struct {
	Parallel bool
	Serial   bool
	Verbose  bool
	Quiet    bool
	DryRun   bool
	Explain  bool
	Only     string
	Skip     string
	Help     bool
	Version  bool
}

type result struct {
	Agent     agents.Agent
	Status    string
	Reason    string
	Before    string
	After     string
	Duration  time.Duration
	Log       string
	UpdateCmd string
	Method    string
	Explain   string
}

const (
	statusUpdated   = "updated"
	statusUnchanged = "unchanged"
	statusSkipped   = "skipped"
	statusFailed    = "failed"
)

var version = "dev"

const (
	reasonMissing       = "missing"
	reasonMissingBun    = "missing bun"
	reasonMissingCode   = "missing vscode"
	reasonManualInstall = "manual install"
)

func main() {
	opts := parseFlags()
	if opts.Help {
		usage()
		return
	}
	if opts.Version {
		fmt.Fprintln(os.Stdout, version)
		return
	}

	all := agents.Default()
	selected, unknown := filterAgents(all, opts.Only, opts.Skip)

	env := newEnv()
	uiEnabled := shouldShowUI(opts)
	results := runAll(selected, env, opts, uiEnabled)

	if !uiEnabled {
		printResults(results, opts)
	} else {
		fmt.Fprintln(os.Stdout)
		if opts.Explain && !opts.Quiet {
			printExplainDetails(results)
		}
	}
	printLogs(results, opts)
	printSummary(results, unknown)

	if hasFailures(results) {
		os.Exit(1)
	}
}

func parseFlags() options {
	var opts options
	flag.BoolVar(&opts.Parallel, "p", false, "run updates in parallel")
	flag.BoolVar(&opts.Parallel, "parallel", false, "run updates in parallel")
	flag.BoolVar(&opts.Serial, "serial", false, "run updates sequentially")
	flag.BoolVar(&opts.Verbose, "v", false, "show update command output")
	flag.BoolVar(&opts.Verbose, "verbose", false, "show update command output")
	flag.BoolVar(&opts.Quiet, "q", false, "summary only")
	flag.BoolVar(&opts.Quiet, "quiet", false, "summary only")
	flag.BoolVar(&opts.DryRun, "n", false, "print commands without executing")
	flag.BoolVar(&opts.DryRun, "dry-run", false, "print commands without executing")
	flag.BoolVar(&opts.Explain, "explain", false, "explain detection and update method")
	flag.StringVar(&opts.Only, "only", "", "comma-separated agent list")
	flag.StringVar(&opts.Skip, "skip", "", "comma-separated agent list to exclude")
	flag.BoolVar(&opts.Help, "h", false, "show help")
	flag.BoolVar(&opts.Help, "help", false, "show help")
	flag.BoolVar(&opts.Version, "version", false, "show version")
	flag.Parse()
	return opts
}

func usage() {
	fmt.Fprintf(os.Stdout, `uca - update multiple coding-agent CLIs

Usage:
  uca [options]

Options:
  -p, --parallel    run updates in parallel (default)
      --serial      run updates sequentially
  -v, --verbose     show update command output for each agent
  -q, --quiet       suppress per-agent version lines (summary only)
  -n, --dry-run     print commands that would run, do not execute
      --explain     show detection details and chosen update method
      --only LIST   comma-separated agent list to include
      --skip LIST   comma-separated agent list to exclude
      --version     show version
  -h, --help        show usage
`)
}

func filterAgents(all []agents.Agent, onlyRaw, skipRaw string) ([]agents.Agent, []string) {
	only := parseList(onlyRaw)
	skip := parseList(skipRaw)

	known := make(map[string]bool, len(all))
	for _, agent := range all {
		known[agent.Name] = true
	}

	unknownSet := map[string]bool{}
	for name := range only {
		if !known[name] {
			unknownSet[name] = true
		}
	}
	for name := range skip {
		if !known[name] {
			unknownSet[name] = true
		}
	}

	selected := make([]agents.Agent, 0, len(all))
	for _, agent := range all {
		name := agent.Name
		if len(only) > 0 && !only[name] {
			continue
		}
		if skip[name] {
			continue
		}
		selected = append(selected, agent)
	}

	unknown := make([]string, 0, len(unknownSet))
	for name := range unknownSet {
		unknown = append(unknown, name)
	}
	sort.Strings(unknown)
	return selected, unknown
}

func parseList(raw string) map[string]bool {
	items := map[string]bool{}
	if strings.TrimSpace(raw) == "" {
		return items
	}
	parts := strings.Split(raw, ",")
	for _, part := range parts {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		items[name] = true
	}
	return items
}

func shouldShowUI(opts options) bool {
	if opts.Quiet {
		return false
	}
	if !isTTY(os.Stdout) {
		return false
	}
	return true
}

func isTTY(file *os.File) bool {
	stat, err := file.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

func runAll(selected []agents.Agent, env *envState, opts options, uiEnabled bool) []result {
	if opts.Serial {
		return runAllWithoutUI(selected, env, opts)
	}

	if uiEnabled {
		return runAllWithUI(selected, env, opts)
	}

	return runAllWithoutUI(selected, env, opts)
}

func runAllWithoutUI(selected []agents.Agent, env *envState, opts options) []result {
	results := make([]result, len(selected))
	if opts.Serial {
		for i, agent := range selected {
			results[i] = runAgent(agent, env, opts)
		}
		return results
	}
	var wg sync.WaitGroup
	wg.Add(len(selected))
	for i := range selected {
		i := i
		go func() {
			defer wg.Done()
			results[i] = runAgent(selected[i], env, opts)
		}()
	}
	wg.Wait()
	return results
}

func shouldShowInUI(agent agents.Agent, env *envState) (bool, string) {
	updateCmd, reason, _, _ := resolveUpdate(agent, env)
	if updateCmd != nil {
		return true, ""
	}
	if reason == reasonManualInstall {
		return true, reason
	}
	return false, reason
}

func runAgent(agent agents.Agent, env *envState, opts options) result {
	res := result{Agent: agent}

	updateCmd, reason, method, detail := resolveUpdate(agent, env)
	if updateCmd == nil {
		res.Status = statusSkipped
		if reason == "" {
			res.Reason = reasonMissing
		} else {
			res.Reason = reason
		}
		res.Explain = detail
		return res
	}

	res.Method = method
	res.Explain = detail
	res.UpdateCmd = cmdString(updateCmd)
	if opts.DryRun {
		res.Status = statusUpdated
		res.Reason = "dry-run"
		return res
	}

	res.Before = getVersion(agent, env, method)
	out, exitCode, duration, _ := runCmd(updateCmd)
	res.Duration = duration
	res.Log = out
	res.After = getVersion(agent, env, method)

	if exitCode != 0 {
		res.Status = statusFailed
		res.Reason = fmt.Sprintf("exit %d", exitCode)
		return res
	}
	if res.Before != "" && res.After != "" && res.Before == res.After && res.Before != "unknown" {
		res.Status = statusUnchanged
	} else {
		res.Status = statusUpdated
	}
	return res
}

type updateEvent struct {
	Index  int
	Phase  string
	Result result
	Time   time.Time
	Show   bool
}

const (
	phaseDetect = "detect"
	phaseStart  = "start"
	phaseFinish = "finish"
)

func runAgentWithEvents(agent agents.Agent, env *envState, opts options, index int, events chan<- updateEvent) result {
	res := result{Agent: agent}

	updateCmd, reason, method, detail := resolveUpdate(agent, env)
	show := updateCmd != nil || reason == reasonManualInstall
	if updateCmd == nil {
		res.Status = statusSkipped
		if reason == "" {
			res.Reason = reasonMissing
		} else {
			res.Reason = reason
		}
		res.Explain = detail
		events <- updateEvent{Index: index, Phase: phaseDetect, Result: res, Time: time.Now(), Show: show}
		events <- updateEvent{Index: index, Phase: phaseFinish, Result: res, Time: time.Now(), Show: show}
		return res
	}

	res.Method = method
	res.Explain = detail
	res.UpdateCmd = cmdString(updateCmd)
	if opts.DryRun {
		res.Status = statusUpdated
		res.Reason = "dry-run"
		events <- updateEvent{Index: index, Phase: phaseDetect, Result: res, Time: time.Now(), Show: show}
		events <- updateEvent{Index: index, Phase: phaseFinish, Result: res, Time: time.Now(), Show: show}
		return res
	}

	res.Before = getVersion(agent, env, method)
	events <- updateEvent{Index: index, Phase: phaseDetect, Result: res, Time: time.Now(), Show: show}
	events <- updateEvent{Index: index, Phase: phaseStart, Result: res, Time: time.Now(), Show: show}

	out, exitCode, duration, _ := runCmd(updateCmd)
	res.Duration = duration
	res.Log = out
	res.After = getVersion(agent, env, method)

	if exitCode != 0 {
		res.Status = statusFailed
		res.Reason = fmt.Sprintf("exit %d", exitCode)
		events <- updateEvent{Index: index, Phase: phaseFinish, Result: res, Time: time.Now(), Show: show}
		return res
	}
	if res.Before != "" && res.After != "" && res.Before == res.After && res.Before != "unknown" {
		res.Status = statusUnchanged
	} else {
		res.Status = statusUpdated
	}
	events <- updateEvent{Index: index, Phase: phaseFinish, Result: res, Time: time.Now(), Show: show}
	return res
}

type uiRow struct {
	name     string
	status   string
	before   string
	after    string
	reason   string
	method   string
	start    time.Time
	duration time.Duration
	visible  bool
	detected bool
}

type uiRenderer struct {
	out        *os.File
	lastLines  int
	useColor   bool
	useUnicode bool
	width      int
}

func newRenderer(out *os.File) *uiRenderer {
	return &uiRenderer{
		out:        out,
		useColor:   shouldUseColor(),
		useUnicode: shouldUseUnicode(),
		width:      termWidth(out),
	}
}

func (r *uiRenderer) Draw(content string) {
	if r.lastLines > 0 {
		fmt.Fprintf(r.out, "\x1b[%dA", r.lastLines)
	}
	fmt.Fprint(r.out, "\x1b[0G\x1b[0J")
	fmt.Fprint(r.out, content)
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

func hideCursor(out *os.File) {
	if out != nil {
		fmt.Fprint(out, "\x1b[?25l")
	}
}

func showCursor(out *os.File) {
	if out != nil {
		fmt.Fprint(out, "\x1b[?25h")
	}
}

func shouldUseColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	term := strings.ToLower(os.Getenv("TERM"))
	if term == "" || term == "dumb" {
		return false
	}
	return true
}

func shouldUseUnicode() bool {
	locale := strings.ToUpper(os.Getenv("LC_ALL") + os.Getenv("LC_CTYPE") + os.Getenv("LANG"))
	return strings.Contains(locale, "UTF-8")
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

func runAllWithUI(selected []agents.Agent, env *envState, opts options) []result {
	results := make([]result, len(selected))
	events := make(chan updateEvent, len(selected))
	done := make(chan struct{})

	rows := make([]uiRow, len(selected))
	nameWidth := 0
	for i, agent := range selected {
		rows[i] = uiRow{name: agent.Name, status: "pending", visible: false}
		if len(agent.Name) > nameWidth {
			nameWidth = len(agent.Name)
		}
	}

	renderer := newRenderer(os.Stdout)
	start := time.Now()
	hideCursor(renderer.out)
	totalAgents := len(selected)
	detectedCount := 0
	renderer.Draw(renderFrame(rows, nameWidth, start, opts, renderer, detectedCount, totalAgents))

	ticker := time.NewTicker(120 * time.Millisecond)
	go func() {
		defer close(done)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					ticker.Stop()
					renderer.Draw(renderFrame(rows, nameWidth, start, opts, renderer, detectedCount, totalAgents))
					return
				}
				if ev.Phase == phaseDetect && !rows[ev.Index].detected {
					rows[ev.Index].detected = true
					detectedCount++
				}
				applyEvent(&rows[ev.Index], ev)
				renderer.Draw(renderFrame(rows, nameWidth, start, opts, renderer, detectedCount, totalAgents))
			case <-ticker.C:
				renderer.Draw(renderFrame(rows, nameWidth, start, opts, renderer, detectedCount, totalAgents))
			}
		}
	}()

	go func() {
		env.npmOnce.Do(env.loadNpmGlobals)
	}()
	go func() {
		env.uvOnce.Do(env.loadUvTools)
	}()
	go func() {
		env.codeOnce.Do(env.loadCodeExtensions)
	}()

	var wg sync.WaitGroup
	wg.Add(len(selected))
	for i := range selected {
		i := i
		go func() {
			defer wg.Done()
			results[i] = runAgentWithEvents(selected[i], env, opts, i, events)
		}()
	}
	wg.Wait()
	close(events)
	<-done
	showCursor(renderer.out)
	return results
}

func applyEvent(row *uiRow, ev updateEvent) {
	res := ev.Result
	switch ev.Phase {
	case phaseDetect:
		row.visible = ev.Show
		row.status = "pending"
		row.reason = res.Reason
		row.method = res.Method
		row.before = res.Before
		if res.Status == statusSkipped && res.Reason == reasonManualInstall {
			row.status = statusSkipped
		}
	case phaseStart:
		row.status = "updating"
		row.before = res.Before
		row.method = res.Method
		row.start = ev.Time
	case phaseFinish:
		row.status = res.Status
		row.before = res.Before
		row.after = res.After
		row.reason = res.Reason
		row.method = res.Method
		row.duration = res.Duration
	}
}

func renderDashboard(rows []uiRow, nameWidth int, start time.Time, opts options, r *uiRenderer, detected, total int) string {
	visibleTotal := 0
	completed := 0
	updated := 0
	unchanged := 0
	failed := 0
	visibleRows := make([]uiRow, 0, len(rows))
	for _, row := range rows {
		if !row.visible {
			continue
		}
		visibleRows = append(visibleRows, row)
		visibleTotal++
		if row.status == statusUpdated || row.status == statusUnchanged || row.status == statusSkipped || row.status == statusFailed {
			completed++
		}
		switch row.status {
		case statusUpdated:
			updated++
		case statusUnchanged:
			unchanged++
		case statusFailed:
			failed++
		}
	}
	header := fmt.Sprintf("uca  %s  %d/%d  ok:%d same:%d fail:%d  %s", spinnerGlyph(time.Since(start), r.useUnicode), completed, visibleTotal, updated, unchanged, failed, fmtElapsed(time.Since(start)))
	if detected < total {
		header = fmt.Sprintf("%s  detecting %d/%d", header, detected, total)
	}
	lines := make([]string, 0, visibleTotal+2)
	lines = append(lines, fitLine(header, r.width, r.useUnicode), "")
	for _, row := range visibleRows {
		lines = append(lines, formatRow(row, nameWidth, opts, r))
	}
	return strings.Join(lines, "\n") + "\n"
}

func renderBoot(start time.Time, detected, total int, r *uiRenderer) string {
	header := fmt.Sprintf("uca  %s  detecting %d/%d  %s", spinnerGlyph(time.Since(start), r.useUnicode), detected, total, fmtElapsed(time.Since(start)))
	return fitLine(header, r.width, r.useUnicode) + "\n"
}

func renderFrame(rows []uiRow, nameWidth int, start time.Time, opts options, r *uiRenderer, detected, total int) string {
	if detected < total {
		for _, row := range rows {
			if row.visible {
				return renderDashboard(rows, nameWidth, start, opts, r, detected, total)
			}
		}
		return renderBoot(start, detected, total, r)
	}
	return renderDashboard(rows, nameWidth, start, opts, r, detected, total)
}

func spinnerGlyph(elapsed time.Duration, unicode bool) string {
	frames := []string{"-", "\\", "|", "/"}
	if unicode {
		frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	}
	index := int(elapsed/(120*time.Millisecond)) % len(frames)
	return frames[index]
}

func formatRow(row uiRow, nameWidth int, opts options, r *uiRenderer) string {
	statusLabel := statusLabelFor(row)
	iconPlain := statusIcon(row, r.useUnicode)
	iconColored := colorize(iconPlain, statusLabel, r.useColor)

	version := "--"
	elapsed := "--"
	info := ""
	switch row.status {
	case "pending":
		statusLabel = statusLabelFor(row)
	case "updating":
		statusLabel = statusLabelFor(row)
		version = fmt.Sprintf("%s → …", safeVersion(row.before))
		if !row.start.IsZero() {
			elapsed = fmtElapsed(time.Since(row.start))
		}
	case statusUpdated:
		version = fmt.Sprintf("%s → %s", safeVersion(row.before), safeVersion(row.after))
		elapsed = fmtElapsed(row.duration)
	case statusUnchanged:
		version = fmt.Sprintf("%s → %s", safeVersion(row.before), safeVersion(row.after))
		elapsed = fmtElapsed(row.duration)
	case statusFailed:
		version = fmt.Sprintf("%s → %s", safeVersion(row.before), safeVersion(row.after))
		elapsed = fmtElapsed(row.duration)
		if row.reason != "" {
			info = row.reason
		}
	case statusSkipped:
		if row.reason != "" && row.reason != reasonManualInstall {
			info = row.reason
		}
	}

	if opts.Explain && info == "" && row.method != "" {
		info = methodLabel(row.method)
	}

	if statusLabel == "dry-run" {
		info = "no changes"
	}

	if info != "" {
		info = " (" + info + ")"
	}

	line := fmt.Sprintf("%-*s %s %-9s %s %6s%s", nameWidth, row.name, iconPlain, statusLabel, version, elapsed, info)
	line = fitLine(line, r.width, r.useUnicode)
	if iconPlain != iconColored {
		line = strings.Replace(line, iconPlain, iconColored, 1)
	}
	return line
}

func statusLabelFor(row uiRow) string {
	if row.status == statusUpdated && row.reason == "dry-run" {
		return "dry-run"
	}
	if row.status == statusUnchanged {
		return "same"
	}
	if row.status == statusSkipped && row.reason == reasonManualInstall {
		return "manual"
	}
	return row.status
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

func statusIcon(row uiRow, unicode bool) string {
	status := row.status
	if status == statusUpdated && row.reason == "dry-run" {
		status = "dry-run"
	}
	if status == statusSkipped && row.reason == reasonManualInstall {
		if unicode {
			return "○"
		}
		return "o"
	}
	switch status {
	case "pending":
		if unicode {
			return "·"
		}
		return "."
	case "updating":
		return spinnerGlyph(time.Since(row.start), unicode)
	case statusUpdated:
		if unicode {
			return "✓"
		}
		return "ok"
	case statusUnchanged:
		if unicode {
			return "≡"
		}
		return "="
	case statusFailed:
		if unicode {
			return "✕"
		}
		return "x"
	case statusSkipped:
		if unicode {
			return "–"
		}
		return "-"
	case "dry-run":
		if unicode {
			return "≈"
		}
		return "dr"
	default:
		return "-"
	}
}

func methodLabel(method string) string {
	switch method {
	case agents.KindNative:
		return "native"
	case agents.KindBun:
		return "bun"
	case agents.KindBrew:
		return "brew"
	case agents.KindNpm:
		return "npm"
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
	case "pending":
		code = "90"
	case "updating":
		code = "36"
	case statusUpdated:
		code = "32"
	case statusUnchanged:
		code = "90"
	case statusFailed:
		code = "31"
	case statusSkipped:
		code = "33"
	case "dry-run":
		code = "35"
	}
	if code == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func resolveUpdate(agent agents.Agent, env *envState) ([]string, string, string, string) {
	bunMissing := false
	codeMissing := false
	detail := ""

	for _, strat := range agent.Strategies {
		switch strat.Kind {
		case agents.KindNative:
			if agent.Binary != "" && !env.hasBinary(agent.Binary) {
				continue
			}
			detail = fmt.Sprintf("binary %s found; using built-in update", agent.Binary)
			return strat.Command, "", strat.Kind, detail
		case agents.KindBun:
			if !env.hasBun {
				bunMissing = true
				continue
			}
			if agent.Binary != "" && !env.hasBinary(agent.Binary) {
				continue
			}
			detail = "bun found; updating via bun"
			return strat.Command, "", strat.Kind, detail
		case agents.KindBrew:
			if !env.hasBrew {
				continue
			}
			if env.brewHas(strat.Package) {
				detail = fmt.Sprintf("brew formula %s installed", strat.Package)
				return []string{"brew", "upgrade", strat.Package}, "", strat.Kind, detail
			}
		case agents.KindNpm:
			if !env.hasNpm {
				continue
			}
			if env.npmHas(strat.Package) {
				detail = fmt.Sprintf("npm global package %s installed", strat.Package)
				return []string{"npm", "install", "-g", strat.Package}, "", strat.Kind, detail
			}
		case agents.KindPip:
			if !env.hasPython {
				continue
			}
			if env.pipHas(strat.Package) {
				detail = fmt.Sprintf("pip package %s installed", strat.Package)
				return []string{"python3", "-m", "pip", "install", "-U", "--upgrade-strategy", "only-if-needed", strat.Package}, "", strat.Kind, detail
			}
		case agents.KindUv:
			if !env.hasUv {
				continue
			}
			if env.uvHas(strat.Package) {
				detail = fmt.Sprintf("uv tool %s installed", strat.Package)
				return []string{"uv", "tool", "install", "--force", "--python", "python3.12", "--with", "pip", strat.Package + "@latest"}, "", strat.Kind, detail
			}
		case agents.KindVSCode:
			if env.codeCmd == "" {
				codeMissing = true
				continue
			}
			if env.vscodeHas(strat.ExtensionID) {
				detail = fmt.Sprintf("VS Code extension %s installed (via %s)", strat.ExtensionID, env.codeCmd)
				return []string{env.codeCmd, "--install-extension", strat.ExtensionID, "--force"}, "", strat.Kind, detail
			}
		}
	}

	if bunMissing {
		return nil, reasonMissingBun, "", "bun not found; required for update"
	}
	if codeMissing {
		return nil, reasonMissingCode, "", "VS Code CLI not found (code/codium/code-insiders)"
	}
	if agent.Binary != "" && env.hasBinary(agent.Binary) {
		return nil, reasonManualInstall, "", "binary found but no supported install method detected"
	}
	return nil, reasonMissing, "", "no supported binary or install method detected"
}

func getVersion(agent agents.Agent, env *envState, method string) string {
	if method == agents.KindVSCode && agent.ExtensionID != "" {
		if version := env.vscodeVersion(agent.ExtensionID); version != "" {
			return version
		}
	}
	if len(agent.VersionCmd) > 0 {
		if agent.Binary == "" || env.hasBinary(agent.Binary) {
			return runVersionCmd(agent.VersionCmd)
		}
	}
	if agent.ExtensionID != "" {
		if version := env.vscodeVersion(agent.ExtensionID); version != "" {
			return version
		}
	}
	return "unknown"
}

func runVersionCmd(args []string) string {
	cmd := exec.Command(args[0], args[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "unknown"
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return "unknown"
	}
	lines := strings.Split(trimmed, "\n")
	return strings.TrimSpace(lines[0])
}

func runCmd(args []string) (string, int, time.Duration, error) {
	start := time.Now()
	cmd := exec.Command(args[0], args[1:]...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.Stdin = nil
	err := cmd.Run()
	duration := time.Since(start)
	if err == nil {
		return buf.String(), 0, duration, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return buf.String(), exitErr.ExitCode(), duration, err
	}
	return buf.String(), 1, duration, err
}

func runCmdStdout(args []string) (string, int, time.Duration, error) {
	start := time.Now()
	cmd := exec.Command(args[0], args[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	duration := time.Since(start)
	if err == nil {
		return string(out), 0, duration, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return string(out), exitErr.ExitCode(), duration, err
	}
	return string(out), 1, duration, err
}

func cmdString(args []string) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, quoteArg(arg))
	}
	return strings.Join(parts, " ")
}

func quoteArg(arg string) string {
	if strings.IndexFunc(arg, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '"' || r == '\'' }) == -1 {
		return arg
	}
	return fmt.Sprintf("%q", arg)
}

func printResults(results []result, opts options) {
	if opts.Quiet {
		return
	}
	for _, res := range results {
		fmt.Fprintln(os.Stdout, formatResult(res, opts))
		if opts.Explain {
			if line := formatExplain(res); line != "" {
				fmt.Fprintln(os.Stdout, line)
			}
		}
	}
}

func printExplainDetails(results []result) {
	for _, res := range results {
		if strings.TrimSpace(res.Explain) == "" {
			continue
		}
		fmt.Fprintf(os.Stdout, "%s: %s\n", res.Agent.Name, res.Explain)
	}
}

func formatResult(res result, opts options) string {
	name := res.Agent.Name
	switch res.Status {
	case statusSkipped:
		return fmt.Sprintf("%s: skipped (%s)", name, res.Reason)
	case statusFailed:
		return fmt.Sprintf("%s: failed (%s -> %s (%s))", name, safeVersion(res.Before), safeVersion(res.After), fmtDuration(res.Duration))
	case statusUpdated:
		if opts.DryRun {
			return fmt.Sprintf("%s: %s", name, res.UpdateCmd)
		}
		return fmt.Sprintf("%s: %s -> %s (%s)", name, safeVersion(res.Before), safeVersion(res.After), fmtDuration(res.Duration))
	case statusUnchanged:
		return fmt.Sprintf("%s: unchanged %s -> %s (%s)", name, safeVersion(res.Before), safeVersion(res.After), fmtDuration(res.Duration))
	default:
		return fmt.Sprintf("%s: unknown", name)
	}
}

func formatExplain(res result) string {
	if strings.TrimSpace(res.Explain) == "" {
		return ""
	}
	return fmt.Sprintf("  info: %s", res.Explain)
}

func safeVersion(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unknown"
	}
	return v
}

func fmtDuration(d time.Duration) string {
	seconds := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%ds", seconds)
}

func printLogs(results []result, opts options) {
	if opts.DryRun {
		return
	}
	for _, res := range results {
		if res.Status == statusFailed {
			printLog(res.Agent.Name, res.Log)
			continue
		}
		if opts.Verbose && res.Status == statusUpdated {
			printLog(res.Agent.Name, res.Log)
		}
	}
}

func printLog(agentName, log string) {
	fmt.Fprintf(os.Stdout, "==> %s\n", agentName)
	trimmed := strings.TrimSpace(log)
	if trimmed == "" {
		fmt.Fprintln(os.Stdout, "(no output)")
		return
	}
	fmt.Fprintln(os.Stdout, trimmed)
}

func printSummary(results []result, unknown []string) {
	updated := []string{}
	unchanged := []string{}
	skippedMissing := []string{}
	skippedBun := []string{}
	skippedCode := []string{}
	skippedManual := []string{}
	failed := []string{}

	for _, res := range results {
		switch res.Status {
		case statusUpdated:
			updated = append(updated, res.Agent.Name)
		case statusUnchanged:
			unchanged = append(unchanged, res.Agent.Name)
		case statusSkipped:
			switch res.Reason {
			case reasonMissingBun:
				skippedBun = append(skippedBun, res.Agent.Name)
			case reasonMissingCode:
				skippedCode = append(skippedCode, res.Agent.Name)
			case reasonManualInstall:
				skippedManual = append(skippedManual, res.Agent.Name)
			default:
				skippedMissing = append(skippedMissing, res.Agent.Name)
			}
		case statusFailed:
			failed = append(failed, res.Agent.Name)
		}
	}

	printSummaryLine("updated", updated)
	printSummaryLine("unchanged", unchanged)
	printSummaryLine("skipped (missing)", skippedMissing)
	printSummaryLine("skipped (missing bun)", skippedBun)
	printSummaryLine("skipped (missing vscode)", skippedCode)
	printSummaryLine("skipped (manual install)", skippedManual)
	if len(unknown) > 0 {
		printSummaryLine("skipped (unknown)", unknown)
	}
	if len(failed) > 0 {
		printSummaryLine("failed", failed)
	}
}

func printSummaryLine(label string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(os.Stdout, "%s: %s\n", label, strings.Join(items, " "))
}

func hasFailures(results []result) bool {
	for _, res := range results {
		if res.Status == statusFailed {
			return true
		}
	}
	return false
}

type envState struct {
	hasBun    bool
	hasBrew   bool
	hasNpm    bool
	hasUv     bool
	hasPython bool
	codeCmd   string

	mu         sync.Mutex
	binCache   map[string]bool
	npmOnce    sync.Once
	npmGlobals map[string]bool
	uvOnce     sync.Once
	uvTools    map[string]bool
	codeOnce   sync.Once
	codeExts   map[string]string
}

func newEnv() *envState {
	return &envState{
		hasBun:    hasBinary("bun"),
		hasBrew:   hasBinary("brew"),
		hasNpm:    hasBinary("npm"),
		hasUv:     hasBinary("uv"),
		hasPython: hasBinary("python3"),
		codeCmd:   detectCodeCmd(),
		binCache:  map[string]bool{},
	}
}

func detectCodeCmd() string {
	candidates := []string{"code", "codium", "code-insiders"}
	for _, candidate := range candidates {
		if hasBinary(candidate) {
			return candidate
		}
	}
	return ""
}

func (e *envState) hasBinary(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if val, ok := e.binCache[name]; ok {
		return val
	}
	_, err := exec.LookPath(name)
	ok := err == nil
	e.binCache[name] = ok
	return ok
}

func hasBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func (e *envState) npmHas(pkg string) bool {
	e.npmOnce.Do(e.loadNpmGlobals)
	return e.npmGlobals[pkg]
}

func (e *envState) loadNpmGlobals() {
	e.npmGlobals = map[string]bool{}
	if !e.hasNpm {
		return
	}
	out, _, _, _ := runCmdStdout([]string{"npm", "list", "-g", "--depth=0", "--json"})
	var payload struct {
		Dependencies map[string]any `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return
	}
	for name := range payload.Dependencies {
		e.npmGlobals[name] = true
	}
}

func (e *envState) uvHas(pkg string) bool {
	e.uvOnce.Do(e.loadUvTools)
	return e.uvTools[pkg]
}

func (e *envState) loadUvTools() {
	e.uvTools = map[string]bool{}
	if !e.hasUv {
		return
	}
	out, _, _, _ := runCmdStdout([]string{"uv", "tool", "list"})
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		e.uvTools[fields[0]] = true
	}
}

func (e *envState) brewHas(formula string) bool {
	if !e.hasBrew {
		return false
	}
	out, exitCode, _, _ := runCmdStdout([]string{"brew", "list", "--formula", "--versions", formula})
	return exitCode == 0 && strings.TrimSpace(out) != ""
}

func (e *envState) pipHas(pkg string) bool {
	if !e.hasPython {
		return false
	}
	_, exitCode, _, _ := runCmdStdout([]string{"python3", "-m", "pip", "show", pkg})
	return exitCode == 0
}

func (e *envState) vscodeHas(extID string) bool {
	e.codeOnce.Do(e.loadCodeExtensions)
	_, ok := e.codeExts[extID]
	return ok
}

func (e *envState) vscodeVersion(extID string) string {
	e.codeOnce.Do(e.loadCodeExtensions)
	return e.codeExts[extID]
}

func (e *envState) loadCodeExtensions() {
	e.codeExts = map[string]string{}
	if e.codeCmd == "" {
		return
	}
	out, _, _, _ := runCmdStdout([]string{e.codeCmd, "--list-extensions", "--show-versions"})
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		idx := strings.LastIndex(line, "@")
		if idx <= 0 {
			continue
		}
		id := line[:idx]
		version := line[idx+1:]
		e.codeExts[id] = version
	}
}
