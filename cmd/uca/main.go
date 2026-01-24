package main

import (
	"bytes"
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
}

const (
	statusUpdated = "updated"
	statusSkipped = "skipped"
	statusFailed  = "failed"
)

func main() {
	opts := parseFlags()
	if opts.Help {
		usage()
		return
	}

	all := agents.Default()
	selected, unknown := filterAgents(all, opts.Only, opts.Skip)

	bunAvailable := hasBinary("bun")
	results := runAll(selected, bunAvailable, opts)

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

func runAll(selected []agents.Agent, bunAvailable bool, opts options) []result {
	results := make([]result, len(selected))
	if opts.Parallel {
		var wg sync.WaitGroup
		wg.Add(len(selected))
		for i := range selected {
			i := i
			go func() {
				defer wg.Done()
				results[i] = runAgent(selected[i], bunAvailable, opts)
			}()
		}
		wg.Wait()
		return results
	}

	for i, agent := range selected {
		results[i] = runAgent(agent, bunAvailable, opts)
	}
	return results
}

func runAgent(agent agents.Agent, bunAvailable bool, opts options) result {
	res := result{Agent: agent, UpdateCmd: cmdString(agent.UpdateCmd)}

	if !hasBinary(agent.Binary) {
		res.Status = statusSkipped
		res.Reason = "missing"
		return res
	}
	if agent.RequiresBun && !bunAvailable {
		res.Status = statusSkipped
		res.Reason = "missing bun"
		return res
	}

	if opts.DryRun {
		res.Status = statusUpdated
		res.Reason = "dry-run"
		return res
	}

	res.Before = getVersion(agent.VersionCmd)
	out, exitCode, duration, _ := runCmd(agent.UpdateCmd)
	res.Duration = duration
	res.Log = out
	res.After = getVersion(agent.VersionCmd)

	if exitCode != 0 {
		res.Status = statusFailed
		return res
	}
	res.Status = statusUpdated
	return res
}

func hasBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
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

func getVersion(args []string) string {
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
		if res.Reason == "" {
			return fmt.Sprintf("%s: skipped", name)
		}
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
	failed := []string{}

	for _, res := range results {
		switch res.Status {
		case statusUpdated:
			updated = append(updated, res.Agent.Name)
		case statusSkipped:
			switch res.Reason {
			case "missing bun":
				skippedBun = append(skippedBun, res.Agent.Name)
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
