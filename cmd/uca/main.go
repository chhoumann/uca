package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chhoumann/uca/internal/agents"
	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

type options struct {
	Parallel bool
	Serial   bool
	Safe     bool
	Timeout  time.Duration
	// Concurrency limits how many update commands are allowed to run at once.
	// 0 means "no limit" (default).
	Concurrency int
	Verbose     bool
	Quiet       bool
	DryRun      bool
	Explain     bool
	Only        string
	Skip        string
	Help        bool
	Version     bool
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	env := newEnv(ctx)
	uiEnabled := shouldShowUI(opts)
	results := runAll(ctx, selected, env, opts, uiEnabled)

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
	flag.BoolVar(&opts.Safe, "safe", false, "use safer execution (limits concurrency)")
	flag.DurationVar(&opts.Timeout, "timeout", 15*time.Minute, "timeout per update command (0 disables)")
	flag.IntVar(&opts.Concurrency, "concurrency", 0, "max concurrent update commands (0 disables)")
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
      --safe        safer execution (limits concurrency)
      --timeout D   timeout per update command (0 disables, default 15m)
      --concurrency N max concurrent update commands (0 disables)
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

func runAll(ctx context.Context, selected []agents.Agent, env *envState, opts options, uiEnabled bool) []result {
	if uiEnabled {
		return runAllWithUI(ctx, selected, env, opts)
	}
	return runAllWithEvents(ctx, selected, env, opts, nil)
}

type agentWork struct {
	agent           agents.Agent
	index           int
	show            bool
	method          string
	explain         string
	reason          string
	nodePackageName string
	// updateCmd is the final command to run (may be a batch command).
	updateCmd []string
	// updateCmdSingle is the per-agent command (used for fallback when batch updates fail).
	updateCmdSingle []string
}

type updateTask struct {
	kind   string
	cmd    []string
	agents []agentWork
}

type managerLocker struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newManagerLocker() *managerLocker {
	return &managerLocker{locks: map[string]*sync.Mutex{}}
}

func (l *managerLocker) lock(kind string) func() {
	if kind == "" {
		return func() {}
	}
	l.mu.Lock()
	m, ok := l.locks[kind]
	if !ok {
		m = &sync.Mutex{}
		l.locks[kind] = m
	}
	l.mu.Unlock()
	m.Lock()
	return func() { m.Unlock() }
}

func shouldLockKind(kind string) bool {
	switch kind {
	case agents.KindNpm, agents.KindPnpm, agents.KindYarn, agents.KindBun, agents.KindBrew, agents.KindPip, agents.KindUv, agents.KindVSCode:
		return true
	default:
		return false
	}
}

func isNodeKind(kind string) bool {
	switch kind {
	case agents.KindNpm, agents.KindPnpm, agents.KindYarn, agents.KindBun:
		return true
	default:
		return false
	}
}

func effectiveConcurrency(opts options, numTasks int) int {
	if opts.Serial {
		return 1
	}
	if opts.Safe && opts.Concurrency == 0 {
		return 1
	}
	if opts.Concurrency > 0 {
		return opts.Concurrency
	}
	if numTasks <= 0 {
		return 1
	}
	return numTasks
}

func nodeBatchUpdateCommand(kind string, pkgs []string) []string {
	args := []string{}
	switch kind {
	case agents.KindNpm:
		args = append(args, "npm", "install", "-g")
	case agents.KindPnpm:
		args = append(args, "pnpm", "add", "-g")
	case agents.KindYarn:
		args = append(args, "yarn", "global", "add")
	case agents.KindBun:
		args = append(args, "bun", "add", "-g")
	default:
		return nil
	}
	for _, pkg := range pkgs {
		if strings.TrimSpace(pkg) == "" {
			continue
		}
		args = append(args, pkg+"@latest")
	}
	return args
}

func runAllWithEvents(ctx context.Context, selected []agents.Agent, env *envState, opts options, events chan<- updateEvent) []result {
	results := make([]result, len(selected))
	works := make([]agentWork, len(selected))

	for i, agent := range selected {
		updateCmd, reason, method, detail := resolveUpdate(agent, env)
		show := updateCmd != nil || reason == reasonManualInstall
		work := agentWork{
			agent:           agent,
			index:           i,
			show:            show,
			method:          method,
			explain:         detail,
			reason:          reason,
			updateCmdSingle: updateCmd,
		}
		if isNodeKind(method) {
			work.nodePackageName = nodePackageName(agent.Strategies)
		}
		works[i] = work
	}

	// Build tasks (batch node updates by manager kind).
	tasks := []updateTask{}
	nodeGroups := map[string][]int{}
	for i := range works {
		work := &works[i]
		if work.updateCmdSingle == nil {
			continue
		}
		if isNodeKind(work.method) {
			nodeGroups[work.method] = append(nodeGroups[work.method], i)
			continue
		}
		work.updateCmd = work.updateCmdSingle
		tasks = append(tasks, updateTask{kind: work.method, cmd: work.updateCmd, agents: []agentWork{*work}})
	}
	for kind, indexes := range nodeGroups {
		pkgSet := map[string]bool{}
		pkgs := make([]string, 0, len(indexes))
		batchIndexes := make([]int, 0, len(indexes))
		for _, idx := range indexes {
			pkg := strings.TrimSpace(works[idx].nodePackageName)
			if pkg == "" {
				works[idx].updateCmd = works[idx].updateCmdSingle
				tasks = append(tasks, updateTask{kind: kind, cmd: works[idx].updateCmd, agents: []agentWork{works[idx]}})
				continue
			}
			if !pkgSet[pkg] {
				pkgSet[pkg] = true
				pkgs = append(pkgs, pkg)
			}
			batchIndexes = append(batchIndexes, idx)
		}
		if len(batchIndexes) == 0 {
			continue
		}
		sort.Strings(pkgs)
		cmd := nodeBatchUpdateCommand(kind, pkgs)
		group := make([]agentWork, 0, len(indexes))
		for _, idx := range batchIndexes {
			works[idx].updateCmd = cmd
			group = append(group, works[idx])
		}
		tasks = append(tasks, updateTask{kind: kind, cmd: cmd, agents: group})
	}

	// Emit detect events and handle skipped/dry-run results.
	now := time.Now()
	for _, work := range works {
		res := result{
			Agent:     work.agent,
			Method:    work.method,
			Explain:   work.explain,
			UpdateCmd: cmdString(work.updateCmd),
		}

		if work.updateCmdSingle == nil {
			res.Status = statusSkipped
			if work.reason == "" {
				res.Reason = reasonMissing
			} else {
				res.Reason = work.reason
			}
			results[work.index] = res
			if events != nil {
				events <- updateEvent{Index: work.index, Phase: phaseDetect, Result: res, Time: now, Show: work.show}
				events <- updateEvent{Index: work.index, Phase: phaseFinish, Result: res, Time: now, Show: work.show}
			}
			continue
		}

		if opts.DryRun {
			// Emit detect first so the UI can render quickly, then populate versions.
			if events != nil {
				events <- updateEvent{Index: work.index, Phase: phaseDetect, Result: res, Time: now, Show: work.show}
			}

			res.Status = statusUpdated
			res.Reason = "dry-run"
			res.Before = getVersion(ctx, work.agent, env, work.method)
			res.After = res.Before
			if isNodeKind(work.method) {
				if latest := nodeLatestVersion(ctx, work.method, work.nodePackageName); latest != "" {
					if formatted := formatVersionWithToken(res.Before, latest); formatted != "" {
						res.After = formatted
					} else {
						res.After = latest
					}
				}
			}
			results[work.index] = res
			if events != nil {
				events <- updateEvent{Index: work.index, Phase: phaseFinish, Result: res, Time: now, Show: work.show}
			}
			continue
		}

		if events != nil {
			events <- updateEvent{Index: work.index, Phase: phaseDetect, Result: res, Time: now, Show: work.show}
		}
	}

	if opts.DryRun {
		return results
	}

	locker := newManagerLocker()
	taskCh := make(chan updateTask)
	var wg sync.WaitGroup
	workerCount := effectiveConcurrency(opts, len(tasks))
	if workerCount > len(tasks) {
		workerCount = len(tasks)
	}
	if workerCount < 1 {
		workerCount = 1
	}
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer wg.Done()
			for task := range taskCh {
				runTask(ctx, task, env, opts, locker, events, results)
			}
		}()
	}
	for _, task := range tasks {
		taskCh <- task
	}
	close(taskCh)
	wg.Wait()

	return results
}

func runTask(ctx context.Context, task updateTask, env *envState, opts options, locker *managerLocker, events chan<- updateEvent, results []result) {
	if len(task.agents) == 0 {
		return
	}

	kind := task.kind
	unlock := func() {}
	if shouldLockKind(kind) {
		unlock = locker.lock(kind)
	}
	defer unlock()

	// Prepare results and emit start events.
	prepared := make([]result, len(task.agents))
	for i, work := range task.agents {
		res := result{
			Agent:     work.agent,
			Method:    work.method,
			Explain:   work.explain,
			UpdateCmd: cmdString(work.updateCmd),
		}
		res.Before = getVersion(ctx, work.agent, env, work.method)
		prepared[i] = res
	}
	startTime := time.Now()
	if events != nil {
		for i, work := range task.agents {
			events <- updateEvent{Index: work.index, Phase: phaseStart, Result: prepared[i], Time: startTime, Show: work.show}
		}
	}

	out, classifyOut, exitCode, duration, _ := runUpdateCmd(ctx, task.cmd, opts.Timeout)

	// If a batched node update fails, fall back to per-package updates so we can still make progress and
	// attribute failures precisely.
	if exitCode != 0 && len(task.agents) > 1 && isNodeKind(kind) {
		for i, work := range task.agents {
			res := prepared[i]
			res.Explain = appendHint(res.Explain, "batch update failed; retrying individually")

			indOut, indClassifyOut, indExitCode, indDuration, _ := runUpdateCmd(ctx, work.updateCmdSingle, opts.Timeout)
			res.Duration = indDuration
			res.Log = strings.TrimRight(out, "\n")
			if strings.TrimSpace(res.Log) != "" && strings.TrimSpace(indOut) != "" {
				res.Log += "\n\n(uca) retrying individually after batch failure\n"
			} else if strings.TrimSpace(res.Log) != "" {
				res.Log += "\n"
			}
			res.Log += strings.TrimSpace(indOut)
			res.After = getVersion(ctx, work.agent, env, work.method)

			if indExitCode != 0 {
				setFailureResult(&res, indExitCode, work.updateCmdSingle, indClassifyOut, opts.Timeout)
			} else if res.Before != "" && res.After != "" && res.Before == res.After && res.Before != "unknown" {
				res.Status = statusUnchanged
			} else {
				res.Status = statusUpdated
			}
			results[work.index] = res
			if events != nil {
				events <- updateEvent{Index: work.index, Phase: phaseFinish, Result: res, Time: time.Now(), Show: work.show}
			}
		}
		return
	}

	// Batch success or non-batch failure path.
	for i, work := range task.agents {
		res := prepared[i]
		res.Duration = duration
		res.Log = out
		res.After = getVersion(ctx, work.agent, env, work.method)

		if exitCode != 0 {
			setFailureResult(&res, exitCode, task.cmd, classifyOut, opts.Timeout)
		} else if res.Before != "" && res.After != "" && res.Before == res.After && res.Before != "unknown" {
			res.Status = statusUnchanged
		} else {
			res.Status = statusUpdated
		}
		results[work.index] = res
		if events != nil {
			events <- updateEvent{Index: work.index, Phase: phaseFinish, Result: res, Time: time.Now(), Show: work.show}
		}
	}
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

func runAllWithUI(ctx context.Context, selected []agents.Agent, env *envState, opts options) []result {
	events := make(chan updateEvent, len(selected)*4)
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
		env.npmPkgOnce.Do(env.loadNpmPkgs)
	}()
	go func() {
		env.pnpmBinOnce.Do(env.loadPnpmBin)
	}()
	go func() {
		env.pnpmPkgOnce.Do(env.loadPnpmPkgs)
	}()
	go func() {
		env.yarnBinOnce.Do(env.loadYarnBin)
	}()
	go func() {
		env.yarnPkgOnce.Do(env.loadYarnPkgs)
	}()
	go func() {
		env.bunBinOnce.Do(env.loadBunGlobalBin)
	}()
	go func() {
		env.bunPkgOnce.Do(env.loadBunPkgs)
	}()
	go func() {
		env.uvOnce.Do(env.loadUvTools)
	}()
	go func() {
		env.codeOnce.Do(env.loadCodeExtensions)
	}()

	results := runAllWithEvents(ctx, selected, env, opts, events)
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
		info = "preview"
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
				detail = fmt.Sprintf("%s global bin has %s; matched by bin dir; updating via %s", strat.Kind, agent.Binary, strat.Kind)
				return nodeUpdateCommand(strat), "", strat.Kind, detail
			}
			if packageManager != "" {
				if packageManager != strat.Kind {
					continue
				}
				detail = fmt.Sprintf("%s global package %s installed; matched by package list; updating via %s", strat.Kind, strat.Package, strat.Kind)
				return nodeUpdateCommand(strat), "", strat.Kind, detail
			}
			if !env.nodeBinHasBinary(strat.Kind, agent.Binary) {
				continue
			}
			detail = fmt.Sprintf("%s global bin has %s; matched by bin dir; updating via %s", strat.Kind, agent.Binary, strat.Kind)
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
		// Force `@latest` to avoid getting stuck on old minor/prerelease versions (common for 0.x CLIs).
		// `npm update -g` does not accept `pkg@latest` specs, so we use install.
		return []string{"npm", "install", "-g", strat.Package + "@latest"}
	case agents.KindPnpm:
		return []string{"pnpm", "add", "-g", strat.Package + "@latest"}
	case agents.KindYarn:
		return []string{"yarn", "global", "add", strat.Package + "@latest"}
	case agents.KindBun:
		return []string{"bun", "add", "-g", strat.Package + "@latest"}
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

const versionCmdTimeout = 10 * time.Second

func getVersion(ctx context.Context, agent agents.Agent, env *envState, method string) string {
	if method == agents.KindVSCode && agent.ExtensionID != "" {
		if version := env.vscodeVersion(agent.ExtensionID); version != "" {
			return version
		}
	}
	if len(agent.VersionCmd) > 0 {
		if agent.Binary == "" || env.hasBinary(agent.Binary) {
			return runVersionCmd(ctx, agent.VersionCmd)
		}
	}
	if agent.ExtensionID != "" {
		if version := env.vscodeVersion(agent.ExtensionID); version != "" {
			return version
		}
	}
	return "unknown"
}

const latestVersionCmdTimeout = 12 * time.Second

var semverTokenRe = regexp.MustCompile(`(?i)\bv?\d+\.\d+(?:\.\d+)?(?:-[0-9a-z.-]+)?(?:\+[0-9a-z.-]+)?\b`)

func extractVersionToken(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if match := semverTokenRe.FindString(s); match != "" {
		return match, true
	}
	return "", false
}

func formatVersionWithToken(before, newVersion string) string {
	newVersion = strings.TrimSpace(newVersion)
	if newVersion == "" {
		return ""
	}
	before = strings.TrimSpace(before)
	if before == "" || before == "unknown" {
		return newVersion
	}
	token, ok := extractVersionToken(before)
	if !ok {
		return newVersion
	}
	if strings.HasPrefix(token, "v") && !strings.HasPrefix(newVersion, "v") {
		newVersion = "v" + newVersion
	}
	return strings.Replace(before, token, newVersion, 1)
}

func nodeLatestVersion(ctx context.Context, kind, pkg string) string {
	pkg = strings.TrimSpace(pkg)
	if pkg == "" {
		return ""
	}
	args := []string{}
	switch kind {
	case agents.KindNpm:
		args = []string{"npm", "view", pkg, "dist-tags.latest"}
	case agents.KindPnpm:
		args = []string{"pnpm", "view", pkg, "dist-tags.latest", "--silent"}
	case agents.KindYarn:
		args = []string{"yarn", "info", pkg, "dist-tags.latest", "--silent"}
	case agents.KindBun:
		// `bun info` needs `-g` to work outside of a JS project.
		args = []string{"bun", "info", "-g", pkg, "version", "--json"}
	default:
		return ""
	}

	out, exitCode, _, _ := runCmdStdout(ctx, args, latestVersionCmdTimeout)
	if exitCode != 0 {
		return ""
	}
	trimmed := strings.TrimSpace(out)
	trimmed = strings.Trim(trimmed, "\"'")
	return strings.TrimSpace(trimmed)
}

func runVersionCmd(ctx context.Context, args []string) string {
	if len(args) == 0 {
		return "unknown"
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmdCtx, cancel := context.WithTimeout(ctx, versionCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, args[0], args[1:]...)
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

const (
	exitCodeTimeout  = 124
	exitCodeCanceled = 130
)

func runCmd(ctx context.Context, args []string, timeout time.Duration) (string, int, time.Duration, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()
	cmdCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, args[0], args[1:]...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.Stdin = nil
	err := cmd.Run()
	duration := time.Since(start)
	if err == nil {
		return buf.String(), 0, duration, nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return buf.String(), exitCodeTimeout, duration, err
	}
	if errors.Is(err, context.Canceled) {
		return buf.String(), exitCodeCanceled, duration, err
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return buf.String(), exitErr.ExitCode(), duration, err
	}
	return buf.String(), 1, duration, err
}

func runUpdateCmd(ctx context.Context, args []string, timeout time.Duration) (string, string, int, time.Duration, error) {
	out, exitCode, duration, err := runCmd(ctx, args, timeout)
	classifyOut := out
	if exitCode == 0 {
		return out, classifyOut, exitCode, duration, err
	}
	if shouldRetryNpm(args, out) {
		cleanupMsg := cleanupNpmENotEmpty(out)
		retryOut, retryCode, retryDuration, retryErr := runCmd(ctx, args, timeout)
		combined := formatRetryOutput(out, cleanupMsg, retryOut)
		classifyOut = retryOut
		if strings.TrimSpace(classifyOut) == "" {
			classifyOut = out
		}
		return combined, classifyOut, retryCode, duration + retryDuration, retryErr
	}
	return out, classifyOut, exitCode, duration, err
}

func setFailureResult(res *result, exitCode int, updateCmd []string, output string, timeout time.Duration) {
	res.Status = statusFailed
	switch exitCode {
	case exitCodeTimeout:
		res.Reason = "timeout"
		if timeout > 0 {
			res.Explain = appendHint(res.Explain, fmt.Sprintf("command timed out after %s; rerun with --timeout 0 or increase it", timeout.Round(time.Second)))
		} else {
			res.Explain = appendHint(res.Explain, "command timed out; rerun with a larger --timeout")
		}
		return
	case exitCodeCanceled:
		res.Reason = "canceled"
		res.Explain = appendHint(res.Explain, "interrupted; retry the update")
		return
	}
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
	if isNpmGlobalMutate(updateCmd) && (strings.Contains(output, "ENOTEMPTY") ||
		strings.Contains(output, "errno -66") ||
		strings.Contains(lower, "directory not empty")) {
		return reasonNpmNotEmpty, "npm rename failed; retry or remove leftover temp directory under the global npm prefix"
	}
	if strings.Contains(lower, "eacces") || strings.Contains(lower, "eperm") || strings.Contains(lower, "permission denied") {
		return "permission", "permission error; check your global install prefix and file permissions"
	}
	if strings.Contains(lower, "etimedout") ||
		strings.Contains(lower, "timed out") ||
		strings.Contains(lower, "econnreset") ||
		strings.Contains(lower, "enotfound") ||
		strings.Contains(lower, "eai_again") ||
		strings.Contains(lower, "econnrefused") ||
		strings.Contains(lower, "socket hang up") {
		return "network", "network error; check connectivity/proxy/VPN and retry"
	}
	if strings.Contains(lower, "self signed certificate") ||
		strings.Contains(lower, "unable to get local issuer certificate") ||
		strings.Contains(lower, "cert has expired") ||
		strings.Contains(lower, "ssl routines") ||
		strings.Contains(lower, "tls") && strings.Contains(lower, "certificate") {
		return "tls", "TLS/CA error; check corporate proxy settings or system certificates"
	}
	if len(updateCmd) > 0 && updateCmd[0] == "brew" &&
		(strings.Contains(lower, "another active homebrew update process") ||
			strings.Contains(lower, "homebrew is already updating") ||
			strings.Contains(lower, "cannot install in homebrew prefix")) {
		return "brew busy", "homebrew is locked/busy; wait for other brew process and retry"
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

func shouldRetryNpm(args []string, output string) bool {
	if !isNpmGlobalMutate(args) {
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
		return fmt.Sprintf("%s\n\n(uca) %s\n(uca) retrying npm after ENOTEMPTY\n%s", first, cleanupMsg, second)
	}
	return fmt.Sprintf("%s\n\n(uca) retrying npm after ENOTEMPTY\n%s", first, second)
}

func isNpmGlobalMutate(args []string) bool {
	if len(args) < 2 || args[0] != "npm" {
		return false
	}
	switch args[1] {
	case "install", "update":
		return true
	default:
		return false
	}
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

const detectCmdTimeout = 30 * time.Second

func runCmdStdout(ctx context.Context, args []string, timeout time.Duration) (string, int, time.Duration, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()
	cmdCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, args[0], args[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	duration := time.Since(start)
	if err == nil {
		return string(out), 0, duration, nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return string(out), exitCodeTimeout, duration, err
	}
	if errors.Is(err, context.Canceled) {
		return string(out), exitCodeCanceled, duration, err
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
	type logGroup struct {
		names []string
		log   string
	}
	groups := map[string]*logGroup{}
	order := []string{}

	for _, res := range results {
		if res.Status != statusFailed && !(opts.Verbose && res.Status == statusUpdated) {
			continue
		}
		key := res.UpdateCmd + "\n" + res.Status + "\n" + res.Log
		group := groups[key]
		if group == nil {
			group = &logGroup{log: res.Log}
			groups[key] = group
			order = append(order, key)
		}
		group.names = append(group.names, res.Agent.Name)
	}

	for _, key := range order {
		group := groups[key]
		printLog(strings.Join(group.names, ", "), group.log)
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
	ctx context.Context

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

func newEnv(ctx context.Context) *envState {
	return &envState{
		ctx:          ctx,
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

func (e *envState) baseCtx() context.Context {
	if e == nil || e.ctx == nil {
		return context.Background()
	}
	return e.ctx
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
	out, exitCode, _, _ := runCmdStdout(e.baseCtx(), []string{"npm", "bin", "-g"}, detectCmdTimeout)
	if exitCode == 0 {
		if dir := strings.TrimSpace(out); dir != "" {
			e.npmBin = dir
			return
		}
	}

	// npm v11 removed `npm bin`, but `npm prefix -g` still works.
	prefixOut, exitCode, _, _ := runCmdStdout(e.baseCtx(), []string{"npm", "prefix", "-g"}, detectCmdTimeout)
	if exitCode != 0 {
		return
	}
	prefix := strings.TrimSpace(prefixOut)
	if prefix == "" {
		return
	}
	// On Unix-like systems, global binaries are installed under <prefix>/bin.
	// On Windows, global binaries are typically installed directly under <prefix>.
	if runtime.GOOS == "windows" {
		bin := filepath.Join(prefix, "bin")
		if info, err := os.Stat(bin); err == nil && info.IsDir() {
			e.npmBin = bin
			return
		}
		e.npmBin = prefix
		return
	}
	e.npmBin = filepath.Join(prefix, "bin")
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
	out, _, _, _ := runCmdStdout(e.baseCtx(), []string{"npm", "list", "-g", "--depth=0", "--json"}, detectCmdTimeout)
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
	out, exitCode, _, _ := runCmdStdout(e.baseCtx(), []string{"pnpm", "bin", "-g"}, detectCmdTimeout)
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
	out, _, _, _ := runCmdStdout(e.baseCtx(), []string{"pnpm", "list", "-g", "--depth=0", "--json"}, detectCmdTimeout)
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
	out, exitCode, _, _ := runCmdStdout(e.baseCtx(), []string{"yarn", "global", "bin"}, detectCmdTimeout)
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
	out, exitCode, _, _ := runCmdStdout(e.baseCtx(), []string{"yarn", "global", "list", "--depth=0"}, detectCmdTimeout)
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
	out, exitCode, _, _ := runCmdStdout(e.baseCtx(), []string{"bun", "pm", "bin", "-g"}, detectCmdTimeout)
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
	out, exitCode, _, _ := runCmdStdout(e.baseCtx(), []string{"bun", "pm", "ls", "-g"}, detectCmdTimeout)
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
	out, _, _, _ := runCmdStdout(e.baseCtx(), []string{"uv", "tool", "list"}, detectCmdTimeout)
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
	out, exitCode, _, _ := runCmdStdout(e.baseCtx(), []string{"brew", "list", "--formula", "--versions", formula}, detectCmdTimeout)
	return exitCode == 0 && strings.TrimSpace(out) != ""
}

func (e *envState) pipHas(pkg string) bool {
	if !e.hasPython {
		return false
	}
	_, exitCode, _, _ := runCmdStdout(e.baseCtx(), []string{"python3", "-m", "pip", "show", pkg}, detectCmdTimeout)
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
	out, _, _, _ := runCmdStdout(e.baseCtx(), []string{e.codeCmd, "--list-extensions", "--show-versions"}, detectCmdTimeout)
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
