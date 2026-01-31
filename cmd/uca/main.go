package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chhoumann/uca/internal/agents"
	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
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
	reasonQuota         = "quota"
	reasonNpmNotEmpty   = "npm ENOTEMPTY"
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
	out, classifyOut, exitCode, duration, _ := runUpdateCmd(updateCmd)
	res.Duration = duration
	res.Log = out
	res.After = getVersion(agent, env, method)

	if exitCode != 0 {
		setFailureResult(&res, exitCode, updateCmd, classifyOut)
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

	out, classifyOut, exitCode, duration, _ := runUpdateCmd(updateCmd)
	res.Duration = duration
	res.Log = out
	res.After = getVersion(agent, env, method)

	if exitCode != 0 {
		setFailureResult(&res, exitCode, updateCmd, classifyOut)
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
		env.npmBinOnce.Do(env.loadNpmBin)
	}()
	go func() {
		env.pnpmBinOnce.Do(env.loadPnpmBin)
	}()
	go func() {
		env.yarnBinOnce.Do(env.loadYarnBin)
	}()
	go func() {
		env.bunBinOnce.Do(env.loadBunGlobalBin)
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
	codeMissing := false
	detail := ""
	nodeManager := ""
	if agent.Binary != "" {
		nodeManager = env.nodeManagerForBinary(agent.Binary)
	}
	packageManager := ""
	packageName := nodePackageName(agent.Strategies)
	if nodeManager == "" && packageName != "" {
		packageManager = env.nodeManagerForPackage(packageName)
	}

	for _, strat := range agent.Strategies {
		switch strat.Kind {
		case agents.KindNative:
			if agent.Binary != "" && !env.hasBinary(agent.Binary) {
				continue
			}
			detail = fmt.Sprintf("binary %s found; using built-in update", agent.Binary)
			return strat.Command, "", strat.Kind, detail
		case agents.KindBun, agents.KindNpm, agents.KindPnpm, agents.KindYarn:
			if !env.hasNodeManager(strat.Kind) {
				continue
			}
			if agent.Binary == "" || strat.Package == "" {
				continue
			}
			if nodeManager != "" {
				if nodeManager != strat.Kind {
					continue
				}
				detail = fmt.Sprintf("%s global bin has %s; updating via %s", strat.Kind, agent.Binary, strat.Kind)
				return nodeUpdateCommand(strat), "", strat.Kind, detail
			}
			if packageManager != "" {
				if packageManager != strat.Kind {
					continue
				}
				detail = fmt.Sprintf("%s global package %s installed; updating via %s", strat.Kind, strat.Package, strat.Kind)
				return nodeUpdateCommand(strat), "", strat.Kind, detail
			}
			if !env.nodeBinHasBinary(strat.Kind, agent.Binary) {
				continue
			}
			detail = fmt.Sprintf("%s global bin has %s; updating via %s", strat.Kind, agent.Binary, strat.Kind)
			return nodeUpdateCommand(strat), "", strat.Kind, detail
		case agents.KindBrew:
			if !env.hasBrew {
				continue
			}
			if env.brewHas(strat.Package) {
				detail = fmt.Sprintf("brew formula %s installed", strat.Package)
				return []string{"brew", "upgrade", strat.Package}, "", strat.Kind, detail
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

	if codeMissing {
		return nil, reasonMissingCode, "", "VS Code CLI not found (code/codium/code-insiders)"
	}
	if agent.Binary != "" && env.hasBinary(agent.Binary) {
		return nil, reasonManualInstall, "", "binary found but no supported install method detected"
	}
	return nil, reasonMissing, "", "no supported binary or install method detected"
}

func nodeUpdateCommand(strat agents.UpdateStrategy) []string {
	if len(strat.Command) > 0 {
		return strat.Command
	}
	switch strat.Kind {
	case agents.KindNpm:
		return []string{"npm", "install", "-g", strat.Package}
	case agents.KindPnpm:
		return []string{"pnpm", "add", "-g", strat.Package}
	case agents.KindYarn:
		return []string{"yarn", "global", "add", strat.Package}
	case agents.KindBun:
		return []string{"bun", "add", "-g", strat.Package}
	default:
		return strat.Command
	}
}

func nodePackageName(strategies []agents.UpdateStrategy) string {
	for _, strat := range strategies {
		switch strat.Kind {
		case agents.KindNpm, agents.KindPnpm, agents.KindYarn, agents.KindBun:
			if strat.Package != "" {
				return strat.Package
			}
		}
	}
	return ""
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
	return parseVersionOutput(string(out))
}

func parseVersionOutput(out string) string {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return "unknown"
	}
	lines := strings.Split(trimmed, "\n")
	first := ""
	versionOnly := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if first == "" {
			first = line
		}
		if isVersionOnlyLine(line) {
			versionOnly = line
		}
	}
	if versionOnly != "" {
		return versionOnly
	}
	if first != "" {
		return first
	}
	return "unknown"
}

func isVersionOnlyLine(line string) bool {
	if strings.ContainsAny(line, " \t") {
		return false
	}
	if strings.HasPrefix(line, "v") {
		line = line[1:]
	}
	parts := strings.Split(line, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
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

func runUpdateCmd(args []string) (string, string, int, time.Duration, error) {
	out, exitCode, duration, err := runCmd(args)
	classifyOut := out
	if exitCode == 0 {
		return out, classifyOut, exitCode, duration, err
	}
	if shouldRetryNpmInstall(args, out) {
		cleanupMsg := cleanupNpmENotEmpty(out)
		retryOut, retryCode, retryDuration, retryErr := runCmd(args)
		combined := formatRetryOutput(out, cleanupMsg, retryOut)
		classifyOut = retryOut
		if strings.TrimSpace(classifyOut) == "" {
			classifyOut = out
		}
		return combined, classifyOut, retryCode, duration + retryDuration, retryErr
	}
	return out, classifyOut, exitCode, duration, err
}

func setFailureResult(res *result, exitCode int, updateCmd []string, output string) {
	res.Status = statusFailed
	reason, hint := classifyUpdateFailure(updateCmd, output)
	if reason == "" {
		res.Reason = fmt.Sprintf("exit %d", exitCode)
	} else {
		res.Reason = reason
	}
	if hint != "" {
		res.Explain = appendHint(res.Explain, hint)
	}
}

func classifyUpdateFailure(updateCmd []string, output string) (string, string) {
	lower := strings.ToLower(output)
	if strings.Contains(output, "TerminalQuotaError") ||
		strings.Contains(lower, "exhausted your capacity") ||
		strings.Contains(lower, "quota will reset") {
		return reasonQuota, "quota exceeded; retry later or update via npm (@google/gemini-cli)"
	}
	if isNpmInstall(updateCmd) && (strings.Contains(output, "ENOTEMPTY") ||
		strings.Contains(output, "errno -66") ||
		strings.Contains(lower, "directory not empty")) {
		return reasonNpmNotEmpty, "npm rename failed; retry or remove leftover temp directory under the global npm prefix"
	}
	return "", ""
}

func appendHint(detail, hint string) string {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return detail
	}
	if strings.TrimSpace(detail) == "" {
		return "hint: " + hint
	}
	return detail + "; hint: " + hint
}

func shouldRetryNpmInstall(args []string, output string) bool {
	if !isNpmInstall(args) {
		return false
	}
	if strings.Contains(output, "ENOTEMPTY") {
		return true
	}
	if strings.Contains(output, "errno -66") {
		return true
	}
	if strings.Contains(output, "directory not empty") {
		return true
	}
	return false
}

func formatRetryOutput(first, cleanupMsg, second string) string {
	first = strings.TrimRight(first, "\n")
	cleanupMsg = strings.TrimSpace(cleanupMsg)
	second = strings.TrimSpace(second)
	if first == "" {
		return second
	}
	if second == "" {
		return first
	}
	if cleanupMsg != "" {
		return fmt.Sprintf("%s\n\n(uca) %s\n(uca) retrying npm install after ENOTEMPTY\n%s", first, cleanupMsg, second)
	}
	return fmt.Sprintf("%s\n\n(uca) retrying npm install after ENOTEMPTY\n%s", first, second)
}

func isNpmInstall(args []string) bool {
	return len(args) >= 2 && args[0] == "npm" && args[1] == "install"
}

func cleanupNpmENotEmpty(output string) string {
	path, dest := extractNpmRenamePaths(output)
	if !isSafeNpmRenameTarget(path, dest) {
		return ""
	}
	if _, err := os.Stat(dest); err != nil {
		return ""
	}
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Sprintf("failed to remove stale npm temp dir %s: %v", dest, err)
	}
	return fmt.Sprintf("removed stale npm temp dir %s", dest)
}

func extractNpmRenamePaths(output string) (string, string) {
	var path string
	var dest string
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "npm error path ") {
			path = strings.TrimSpace(strings.TrimPrefix(line, "npm error path "))
			continue
		}
		if strings.HasPrefix(line, "npm error dest ") {
			dest = strings.TrimSpace(strings.TrimPrefix(line, "npm error dest "))
		}
	}
	if path != "" && dest != "" {
		return path, dest
	}
	scanner = bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.Contains(line, "rename '") || !strings.Contains(line, "' -> '") {
			continue
		}
		start := strings.Index(line, "rename '")
		if start == -1 {
			continue
		}
		start += len("rename '")
		mid := strings.Index(line[start:], "' -> '")
		if mid == -1 {
			continue
		}
		path = line[start : start+mid]
		rest := line[start+mid+len("' -> '"):]
		end := strings.Index(rest, "'")
		if end == -1 {
			continue
		}
		dest = rest[:end]
		break
	}
	return path, dest
}

func isSafeNpmRenameTarget(path, dest string) bool {
	if path == "" || dest == "" {
		return false
	}
	if !filepath.IsAbs(dest) || !filepath.IsAbs(path) {
		return false
	}
	if filepath.Dir(path) != filepath.Dir(dest) {
		return false
	}
	base := filepath.Base(path)
	destBase := filepath.Base(dest)
	if destBase == "." || destBase == ".." || base == "." || base == ".." {
		return false
	}
	prefix := "." + base
	if !strings.HasPrefix(destBase, prefix) {
		return false
	}
	return true
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
		reason := strings.TrimSpace(res.Reason)
		if reason != "" {
			return fmt.Sprintf("%s: failed (%s; %s -> %s (%s))", name, reason, safeVersion(res.Before), safeVersion(res.After), fmtDuration(res.Duration))
		}
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
	hasPnpm   bool
	hasYarn   bool
	hasUv     bool
	hasPython bool
	codeCmd   string

	mu           sync.Mutex
	binPathCache map[string]string
	npmBinOnce   sync.Once
	npmBin       string
	npmPkgOnce   sync.Once
	npmPkgs      map[string]bool
	pnpmBinOnce  sync.Once
	pnpmBin      string
	pnpmPkgOnce  sync.Once
	pnpmPkgs     map[string]bool
	yarnBinOnce  sync.Once
	yarnBin      string
	yarnPkgOnce  sync.Once
	yarnPkgs     map[string]bool
	bunBinOnce   sync.Once
	bunGlobalBin string
	bunPkgOnce   sync.Once
	bunPkgs      map[string]bool
	uvOnce       sync.Once
	uvTools      map[string]bool
	codeOnce     sync.Once
	codeExts     map[string]string
}

func newEnv() *envState {
	return &envState{
		hasBun:       hasBinary("bun"),
		hasBrew:      hasBinary("brew"),
		hasNpm:       hasBinary("npm"),
		hasPnpm:      hasBinary("pnpm"),
		hasYarn:      hasBinary("yarn"),
		hasUv:        hasBinary("uv"),
		hasPython:    hasBinary("python3"),
		codeCmd:      detectCodeCmd(),
		binPathCache: map[string]string{},
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
	return e.binaryPath(name) != ""
}

func hasBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func (e *envState) binaryPath(name string) string {
	if name == "" {
		return ""
	}
	e.mu.Lock()
	if path, ok := e.binPathCache[name]; ok {
		e.mu.Unlock()
		return path
	}
	e.mu.Unlock()
	path, err := exec.LookPath(name)
	if err != nil {
		path = ""
	} else {
		path = filepath.Clean(path)
	}
	e.mu.Lock()
	e.binPathCache[name] = path
	e.mu.Unlock()
	return path
}

func (e *envState) hasNodeManager(kind string) bool {
	switch kind {
	case agents.KindNpm:
		return e.hasNpm
	case agents.KindPnpm:
		return e.hasPnpm
	case agents.KindYarn:
		return e.hasYarn
	case agents.KindBun:
		return e.hasBun
	default:
		return false
	}
}

func (e *envState) nodeManagerForBinary(name string) string {
	binPath := e.binaryPath(name)
	if binPath == "" {
		return ""
	}
	binDir := filepath.Dir(binPath)
	resolvedBinDir := ""
	if resolvedPath := resolveSymlinkPath(binPath); resolvedPath != "" {
		resolvedBinDir = filepath.Dir(resolvedPath)
	}
	matches := []string{}
	for _, kind := range []string{agents.KindNpm, agents.KindPnpm, agents.KindYarn, agents.KindBun} {
		if !e.hasNodeManager(kind) {
			continue
		}
		dir := e.nodeBinDir(kind)
		if dir == "" {
			continue
		}
		if samePath(dir, binDir) || (resolvedBinDir != "" && samePath(dir, resolvedBinDir)) {
			matches = append(matches, kind)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	if len(matches) > 1 {
		bestKind := ""
		bestLen := -1
		tie := false
		for _, kind := range matches {
			dir := e.nodeBinDir(kind)
			if len(dir) > bestLen {
				bestLen = len(dir)
				bestKind = kind
				tie = false
				continue
			}
			if len(dir) == bestLen {
				tie = true
			}
		}
		if !tie {
			return bestKind
		}
	}
	return ""
}

func (e *envState) nodeBinHasBinary(kind, name string) bool {
	return binDirHasBinary(e.nodeBinDir(kind), name)
}

func (e *envState) nodeBinDir(kind string) string {
	switch kind {
	case agents.KindNpm:
		return e.npmBinDir()
	case agents.KindPnpm:
		return e.pnpmBinDir()
	case agents.KindYarn:
		return e.yarnBinDir()
	case agents.KindBun:
		return e.bunGlobalBinDir()
	default:
		return ""
	}
}

func (e *envState) nodeManagerForPackage(pkg string) string {
	if pkg == "" {
		return ""
	}
	matches := []string{}
	for _, kind := range []string{agents.KindNpm, agents.KindPnpm, agents.KindYarn, agents.KindBun} {
		if !e.hasNodeManager(kind) {
			continue
		}
		if e.nodeManagerHasPackage(kind, pkg) {
			matches = append(matches, kind)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

func (e *envState) nodeManagerHasPackage(kind, pkg string) bool {
	switch kind {
	case agents.KindNpm:
		return e.npmHas(pkg)
	case agents.KindPnpm:
		return e.pnpmHas(pkg)
	case agents.KindYarn:
		return e.yarnHas(pkg)
	case agents.KindBun:
		return e.bunHas(pkg)
	default:
		return false
	}
}

func (e *envState) npmBinDir() string {
	e.npmBinOnce.Do(e.loadNpmBin)
	return e.npmBin
}

func (e *envState) loadNpmBin() {
	e.npmBin = ""
	if !e.hasNpm {
		return
	}
	out, exitCode, _, _ := runCmdStdout([]string{"npm", "bin", "-g"})
	if exitCode != 0 {
		return
	}
	e.npmBin = strings.TrimSpace(out)
}

func (e *envState) npmHas(pkg string) bool {
	e.npmPkgOnce.Do(e.loadNpmPkgs)
	return e.npmPkgs[pkg]
}

func (e *envState) loadNpmPkgs() {
	e.npmPkgs = map[string]bool{}
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
		e.npmPkgs[name] = true
	}
}

func (e *envState) pnpmBinDir() string {
	e.pnpmBinOnce.Do(e.loadPnpmBin)
	return e.pnpmBin
}

func (e *envState) loadPnpmBin() {
	e.pnpmBin = ""
	if !e.hasPnpm {
		return
	}
	out, exitCode, _, _ := runCmdStdout([]string{"pnpm", "bin", "-g"})
	if exitCode != 0 {
		return
	}
	e.pnpmBin = strings.TrimSpace(out)
}

func (e *envState) pnpmHas(pkg string) bool {
	e.pnpmPkgOnce.Do(e.loadPnpmPkgs)
	return e.pnpmPkgs[pkg]
}

func (e *envState) loadPnpmPkgs() {
	e.pnpmPkgs = map[string]bool{}
	if !e.hasPnpm {
		return
	}
	out, _, _, _ := runCmdStdout([]string{"pnpm", "list", "-g", "--depth=0", "--json"})
	type pnpmPayload struct {
		Dependencies map[string]any `json:"dependencies"`
	}
	var list []pnpmPayload
	if err := json.Unmarshal([]byte(out), &list); err == nil {
		for _, entry := range list {
			for name := range entry.Dependencies {
				e.pnpmPkgs[name] = true
			}
		}
		return
	}
	var single pnpmPayload
	if err := json.Unmarshal([]byte(out), &single); err != nil {
		return
	}
	for name := range single.Dependencies {
		e.pnpmPkgs[name] = true
	}
}

func (e *envState) yarnBinDir() string {
	e.yarnBinOnce.Do(e.loadYarnBin)
	return e.yarnBin
}

func (e *envState) loadYarnBin() {
	e.yarnBin = ""
	if !e.hasYarn {
		return
	}
	out, exitCode, _, _ := runCmdStdout([]string{"yarn", "global", "bin"})
	if exitCode != 0 {
		return
	}
	e.yarnBin = strings.TrimSpace(out)
}

func (e *envState) yarnHas(pkg string) bool {
	e.yarnPkgOnce.Do(e.loadYarnPkgs)
	return e.yarnPkgs[pkg]
}

func (e *envState) loadYarnPkgs() {
	e.yarnPkgs = map[string]bool{}
	if !e.hasYarn {
		return
	}
	out, exitCode, _, _ := runCmdStdout([]string{"yarn", "global", "list", "--depth=0"})
	if exitCode != 0 {
		return
	}
	for name := range parsePackageListOutput(out) {
		e.yarnPkgs[name] = true
	}
}

func (e *envState) bunGlobalBinDir() string {
	e.bunBinOnce.Do(e.loadBunGlobalBin)
	return e.bunGlobalBin
}

func (e *envState) loadBunGlobalBin() {
	e.bunGlobalBin = ""
	if !e.hasBun {
		return
	}
	out, exitCode, _, _ := runCmdStdout([]string{"bun", "pm", "bin", "-g"})
	if exitCode != 0 {
		return
	}
	e.bunGlobalBin = strings.TrimSpace(out)
}

func (e *envState) bunHas(pkg string) bool {
	e.bunPkgOnce.Do(e.loadBunPkgs)
	return e.bunPkgs[pkg]
}

func (e *envState) loadBunPkgs() {
	e.bunPkgs = map[string]bool{}
	if !e.hasBun {
		return
	}
	out, exitCode, _, _ := runCmdStdout([]string{"bun", "pm", "ls", "-g"})
	if exitCode != 0 {
		return
	}
	for name := range parsePackageListOutput(out) {
		e.bunPkgs[name] = true
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func binDirHasBinary(binDir, name string) bool {
	if binDir == "" || name == "" {
		return false
	}
	candidates := []string{filepath.Join(binDir, name)}
	if runtime.GOOS == "windows" {
		candidates = append(candidates,
			filepath.Join(binDir, name+".exe"),
			filepath.Join(binDir, name+".cmd"),
			filepath.Join(binDir, name+".bat"),
		)
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return true
		}
	}
	return false
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	if a == b {
		return true
	}
	ra := resolveSymlinkPath(a)
	rb := resolveSymlinkPath(b)
	if ra != "" && rb != "" {
		return ra == rb
	}
	if ra != "" && ra == b {
		return true
	}
	if rb != "" && rb == a {
		return true
	}
	return false
}

func resolveSymlinkPath(path string) string {
	if path == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	return filepath.Clean(resolved)
}

func parsePackageListOutput(out string) map[string]bool {
	pkgs := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		for _, token := range strings.Fields(line) {
			if name := parsePackageFromToken(token); name != "" {
				pkgs[name] = true
			}
		}
	}
	return pkgs
}

func parsePackageFromToken(token string) string {
	if token == "" {
		return ""
	}
	token = strings.Trim(token, "\"'`,")
	token = strings.TrimRight(token, "):,")
	token = strings.TrimLeft(token, "(")
	if !strings.Contains(token, "@") {
		return ""
	}
	idx := strings.LastIndex(token, "@")
	if idx <= 0 || idx == len(token)-1 {
		return ""
	}
	return token[:idx]
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
