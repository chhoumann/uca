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
)

type options struct {
	Parallel bool
	Verbose  bool
	Quiet    bool
	DryRun   bool
	Only     string
	Skip     string
	Help     bool
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
}

const (
	statusUpdated = "updated"
	statusSkipped = "skipped"
	statusFailed  = "failed"
)

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

	all := agents.Default()
	selected, unknown := filterAgents(all, opts.Only, opts.Skip)

	env := newEnv()
	results := runAll(selected, env, opts)

	printResults(results, opts)
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
	flag.BoolVar(&opts.Verbose, "v", false, "show update command output")
	flag.BoolVar(&opts.Verbose, "verbose", false, "show update command output")
	flag.BoolVar(&opts.Quiet, "q", false, "summary only")
	flag.BoolVar(&opts.Quiet, "quiet", false, "summary only")
	flag.BoolVar(&opts.DryRun, "n", false, "print commands without executing")
	flag.BoolVar(&opts.DryRun, "dry-run", false, "print commands without executing")
	flag.StringVar(&opts.Only, "only", "", "comma-separated agent list")
	flag.StringVar(&opts.Skip, "skip", "", "comma-separated agent list to exclude")
	flag.BoolVar(&opts.Help, "h", false, "show help")
	flag.BoolVar(&opts.Help, "help", false, "show help")
	flag.Parse()
	return opts
}

func usage() {
	fmt.Fprintf(os.Stdout, `uca - update multiple coding-agent CLIs

Usage:
  uca [options]

Options:
  -p, --parallel    run updates in parallel (no tty output from workers)
  -v, --verbose     show update command output for each agent
  -q, --quiet       suppress per-agent version lines (summary only)
  -n, --dry-run     print commands that would run, do not execute
      --only LIST   comma-separated agent list to include
      --skip LIST   comma-separated agent list to exclude
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

func runAll(selected []agents.Agent, env *envState, opts options) []result {
	results := make([]result, len(selected))
	if opts.Parallel {
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

	for i, agent := range selected {
		results[i] = runAgent(agent, env, opts)
	}
	return results
}

func runAgent(agent agents.Agent, env *envState, opts options) result {
	res := result{Agent: agent}

	updateCmd, reason, method := resolveUpdate(agent, env)
	if updateCmd == nil {
		res.Status = statusSkipped
		if reason == "" {
			res.Reason = reasonMissing
		} else {
			res.Reason = reason
		}
		return res
	}

	res.Method = method
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
		return res
	}
	res.Status = statusUpdated
	return res
}

func resolveUpdate(agent agents.Agent, env *envState) ([]string, string, string) {
	bunMissing := false
	codeMissing := false

	for _, strat := range agent.Strategies {
		switch strat.Kind {
		case agents.KindNative:
			if agent.Binary != "" && !env.hasBinary(agent.Binary) {
				continue
			}
			return strat.Command, "", strat.Kind
		case agents.KindBun:
			if !env.hasBun {
				bunMissing = true
				continue
			}
			if agent.Binary != "" && !env.hasBinary(agent.Binary) {
				continue
			}
			return strat.Command, "", strat.Kind
		case agents.KindBrew:
			if !env.hasBrew {
				continue
			}
			if env.brewHas(strat.Package) {
				return []string{"brew", "upgrade", strat.Package}, "", strat.Kind
			}
		case agents.KindNpm:
			if !env.hasNpm {
				continue
			}
			if env.npmHas(strat.Package) {
				return []string{"npm", "install", "-g", strat.Package}, "", strat.Kind
			}
		case agents.KindPip:
			if !env.hasPython {
				continue
			}
			if env.pipHas(strat.Package) {
				return []string{"python3", "-m", "pip", "install", "-U", "--upgrade-strategy", "only-if-needed", strat.Package}, "", strat.Kind
			}
		case agents.KindUv:
			if !env.hasUv {
				continue
			}
			if env.uvHas(strat.Package) {
				return []string{"uv", "tool", "install", "--force", "--python", "python3.12", "--with", "pip", strat.Package + "@latest"}, "", strat.Kind
			}
		case agents.KindVSCode:
			if env.codeCmd == "" {
				codeMissing = true
				continue
			}
			if env.vscodeHas(strat.ExtensionID) {
				return []string{env.codeCmd, "--install-extension", strat.ExtensionID, "--force"}, "", strat.Kind
			}
		}
	}

	if bunMissing {
		return nil, reasonMissingBun, ""
	}
	if codeMissing {
		return nil, reasonMissingCode, ""
	}
	if agent.Binary != "" && env.hasBinary(agent.Binary) {
		return nil, reasonManualInstall, ""
	}
	return nil, reasonMissing, ""
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
	default:
		return fmt.Sprintf("%s: unknown", name)
	}
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
	skippedMissing := []string{}
	skippedBun := []string{}
	skippedCode := []string{}
	skippedManual := []string{}
	failed := []string{}

	for _, res := range results {
		switch res.Status {
		case statusUpdated:
			updated = append(updated, res.Agent.Name)
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
