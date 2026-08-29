// --check mode: compare installed versions against latest without updating.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chhoumann/uca/internal/agents"
	"github.com/chhoumann/uca/internal/agentspec"
	"github.com/chhoumann/uca/internal/detect"
	"github.com/chhoumann/uca/internal/ui"
	"github.com/chhoumann/uca/internal/version"
)

type checkState string

const (
	checkUpToDate checkState = "up-to-date"
	checkOutdated checkState = "outdated"
	checkUnknown  checkState = "unknown"
	checkMissing  checkState = "missing"
)

type checkResult struct {
	Agent   agents.Agent
	Method  string
	State   checkState
	Current string
	Latest  string
	Reason  string
}

const (
	checkConcurrency = 8
	checkBudget      = 15 * time.Second
)

// compareVersions decides an agent's check state. It treats "current >= latest"
// as up-to-date (so a build that is newer than the published latest is not
// falsely flagged) and only "current strictly older than latest" as outdated.
func compareVersions(current, latest string) checkState {
	if strings.TrimSpace(latest) == "" {
		return checkUnknown
	}
	curToken, ok := version.ExtractToken(current)
	if !ok {
		return checkUnknown
	}
	latToken, ok := version.ExtractToken(latest)
	if !ok {
		latToken = latest
	}
	if version.Compare(curToken, latToken) < 0 {
		return checkOutdated
	}
	return checkUpToDate
}

// runCheck resolves every selected agent and compares its installed version to
// the latest available one, without changing anything. Lookups run concurrently
// under a shared budget.
func runCheck(ctx context.Context, selected []agents.Agent, env *detect.Env) []checkResult {
	results := make([]checkResult, len(selected))
	checkCtx, cancel := context.WithTimeout(ctx, checkBudget)
	defer cancel()
	var wg sync.WaitGroup
	sem := make(chan struct{}, checkConcurrency)
	for i, agent := range selected {
		wg.Add(1)
		go func(i int, agent agents.Agent) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			resolved := agentspec.Resolve(agent, env)
			res := checkResult{Agent: agent, Method: resolved.Method}
			if resolved.Cmd == nil {
				res.State = checkMissing
				res.Reason = resolved.Reason
				if res.Reason == "" {
					res.Reason = agents.ReasonMissing
				}
				results[i] = res
				return
			}
			latestCh := make(chan string, 1)
			go func() { latestCh <- env.LatestVersion(checkCtx, resolved.Method, resolved.Pkg, resolved.Version) }()
			res.Current = getVersion(checkCtx, agent, env, resolved.Method, resolved.VersionCmd)
			res.Latest = <-latestCh
			res.State = compareVersions(res.Current, res.Latest)
			results[i] = res
		}(i, agent)
	}
	wg.Wait()
	return results
}

func hasOutdated(results []checkResult) bool {
	for _, res := range results {
		if res.State == checkOutdated {
			return true
		}
	}
	return false
}

func printCheck(results []checkResult, unknown []string, opts options) {
	nameWidth := 0
	for _, res := range results {
		if len(res.Agent.Name) > nameWidth {
			nameWidth = len(res.Agent.Name)
		}
	}
	upToDate, outdated, unknownCnt, missing := []string{}, []string{}, []string{}, []string{}
	for _, res := range results {
		var detail string
		switch res.State {
		case checkOutdated:
			detail = fmt.Sprintf("outdated (%s -> %s)", safeVersion(res.Current), safeVersion(res.Latest))
			outdated = append(outdated, res.Agent.Name)
		case checkUpToDate:
			detail = fmt.Sprintf("up-to-date (%s)", safeVersion(res.Current))
			upToDate = append(upToDate, res.Agent.Name)
		case checkMissing:
			detail = res.Reason
			if detail == "" {
				detail = agents.ReasonMissing
			}
			missing = append(missing, res.Agent.Name)
		default: // unknown
			detail = fmt.Sprintf("%s (latest unknown)", safeVersion(res.Current))
			unknownCnt = append(unknownCnt, res.Agent.Name)
		}
		if opts.Explain && res.Method != "" {
			detail = fmt.Sprintf("%s [%s]", detail, ui.MethodLabel(res.Method))
		}
		fmt.Fprintf(os.Stdout, "%-*s  %s\n", nameWidth, res.Agent.Name, detail)
	}
	printSummaryLine("outdated", outdated)
	printSummaryLine("up-to-date", upToDate)
	printSummaryLine("unknown", unknownCnt)
	printSummaryLine("missing", missing)
	if len(unknown) > 0 {
		printSummaryLine("skipped (unknown)", unknown)
	}
}

type jsonCheckAgent struct {
	Name    string `json:"name"`
	Method  string `json:"method,omitempty"`
	State   string `json:"state"`
	Current string `json:"current,omitempty"`
	Latest  string `json:"latest,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type jsonCheckReport struct {
	Agents       []jsonCheckAgent `json:"agents"`
	UnknownNames []string         `json:"unknownNames,omitempty"`
	Summary      map[string]int   `json:"summary"`
}

func buildCheckReport(results []checkResult, unknown []string) jsonCheckReport {
	report := jsonCheckReport{
		Agents:       make([]jsonCheckAgent, 0, len(results)),
		UnknownNames: unknown,
		Summary:      map[string]int{},
	}
	for _, res := range results {
		report.Agents = append(report.Agents, jsonCheckAgent{
			Name:    res.Agent.Name,
			Method:  res.Method,
			State:   string(res.State),
			Current: res.Current,
			Latest:  res.Latest,
			Reason:  res.Reason,
		})
		report.Summary[string(res.State)]++
	}
	return report
}

func printCheckJSON(results []checkResult, unknown []string) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(buildCheckReport(results, unknown)); err != nil {
		fmt.Fprintf(os.Stderr, "uca: failed to encode JSON: %v\n", err)
	}
}
