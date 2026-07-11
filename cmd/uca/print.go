// Plain-text and JSON result rendering for non-UI runs.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chhoumann/uca/internal/agents"
)

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
	// In dry-run, node agents under one manager share a single batch command.
	// Group them so the command prints once under all involved agents instead of
	// repeating the full batch line (with every other agent's package) per agent.
	// --explain stays per-agent because its detail differs per agent.
	if opts.DryRun && !opts.Explain {
		printDryRunPlan(results)
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

func printDryRunPlan(results []result) {
	for _, line := range dryRunPlanLines(results) {
		fmt.Fprintln(os.Stdout, line)
	}
}

// dryRunPlanLines renders the dry-run plan, collapsing agents that share a batch
// command onto a single line (e.g. "codex, opencode, pi: bun add -g ...") so the
// batch command is shown once rather than repeated per agent.
func dryRunPlanLines(results []result) []string {
	lines := make([]string, 0, len(results))
	printedCmd := map[string]bool{}
	for i, res := range results {
		if res.Status != agents.StatusUpdated {
			// skipped / other: print individually, in place.
			lines = append(lines, formatResult(res, options{DryRun: true}))
			continue
		}
		if printedCmd[res.UpdateCmd] {
			continue
		}
		printedCmd[res.UpdateCmd] = true
		names := []string{res.Agent.Name}
		for _, other := range results[i+1:] {
			if other.Status == agents.StatusUpdated && other.UpdateCmd == res.UpdateCmd {
				names = append(names, other.Agent.Name)
			}
		}
		lines = append(lines, fmt.Sprintf("%s: %s", strings.Join(names, ", "), res.UpdateCmd))
	}
	return lines
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
	case agents.StatusSkipped:
		return fmt.Sprintf("%s: skipped (%s)", name, res.Reason)
	case agents.StatusFailed:
		reason := strings.TrimSpace(res.Reason)
		if reason != "" {
			return fmt.Sprintf("%s: failed (%s; %s -> %s (%s))", name, reason, safeVersion(res.Before), safeVersion(res.After), fmtDuration(res.Duration))
		}
		return fmt.Sprintf("%s: failed (%s -> %s (%s))", name, safeVersion(res.Before), safeVersion(res.After), fmtDuration(res.Duration))
	case agents.StatusUpdated:
		if opts.DryRun {
			return fmt.Sprintf("%s: %s", name, res.UpdateCmd)
		}
		return fmt.Sprintf("%s: %s -> %s (%s)", name, safeVersion(res.Before), safeVersion(res.After), fmtDuration(res.Duration))
	case agents.StatusUnchanged:
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

type jsonAgentResult struct {
	Name            string `json:"name"`
	Method          string `json:"method,omitempty"`
	Status          string `json:"status"`
	Reason          string `json:"reason,omitempty"`
	Before          string `json:"before,omitempty"`
	After           string `json:"after,omitempty"`
	DurationSeconds int    `json:"durationSeconds"`
	UpdateCmd       string `json:"updateCmd,omitempty"`
	Explain         string `json:"explain,omitempty"`
}

type jsonReport struct {
	DryRun       bool              `json:"dryRun"`
	Agents       []jsonAgentResult `json:"agents"`
	UnknownNames []string          `json:"unknownNames,omitempty"`
	Summary      map[string]int    `json:"summary"`
}

// jsonStatus normalizes the internal status into a stable, self-describing token
// for machine consumers (dry-run surfaces as its own status rather than
// "updated" with reason "dry-run").
func jsonStatus(res result) string {
	if res.Status == agents.StatusUpdated && res.Reason == agents.ReasonDryRun {
		return "dry-run"
	}
	if res.Status == "" {
		return "unknown"
	}
	return res.Status
}

func buildReport(results []result, unknown []string, opts options) jsonReport {
	report := jsonReport{
		DryRun:       opts.DryRun,
		Agents:       make([]jsonAgentResult, 0, len(results)),
		UnknownNames: unknown,
		Summary:      map[string]int{},
	}
	for _, res := range results {
		status := jsonStatus(res)
		reason := res.Reason
		if status == "dry-run" {
			reason = "" // redundant with the status
		}
		report.Agents = append(report.Agents, jsonAgentResult{
			Name:            res.Agent.Name,
			Method:          res.Method,
			Status:          status,
			Reason:          reason,
			Before:          res.Before,
			After:           res.After,
			DurationSeconds: int(res.Duration.Round(time.Second).Seconds()),
			UpdateCmd:       res.UpdateCmd,
			Explain:         res.Explain,
		})
		report.Summary[status]++
	}
	return report
}

func printJSON(results []result, unknown []string, opts options) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(buildReport(results, unknown, opts)); err != nil {
		fmt.Fprintf(os.Stderr, "uca: failed to encode JSON: %v\n", err)
	}
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
		if res.Status != agents.StatusFailed && !(opts.Verbose && res.Status == agents.StatusUpdated) {
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
	skippedCode := []string{}
	skippedManual := []string{}
	failed := []string{}

	for _, res := range results {
		switch res.Status {
		case agents.StatusUpdated:
			updated = append(updated, res.Agent.Name)
		case agents.StatusUnchanged:
			unchanged = append(unchanged, res.Agent.Name)
		case agents.StatusSkipped:
			switch res.Reason {
			case agents.ReasonMissingCode:
				skippedCode = append(skippedCode, res.Agent.Name)
			case agents.ReasonManualInstall:
				skippedManual = append(skippedManual, res.Agent.Name)
			default:
				skippedMissing = append(skippedMissing, res.Agent.Name)
			}
		case agents.StatusFailed:
			failed = append(failed, res.Agent.Name)
		}
	}

	printSummaryLine("updated", updated)
	printSummaryLine("unchanged", unchanged)
	printSummaryLine("skipped (missing)", skippedMissing)
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

func hasFailures(results []result) bool {
	for _, res := range results {
		if res.Status == agents.StatusFailed {
			return true
		}
	}
	return false
}
