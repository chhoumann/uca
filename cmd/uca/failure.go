// Update-failure classification and the npm ENOTEMPTY cleanup/retry heuristics.
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chhoumann/uca/internal/agents"
	runner "github.com/chhoumann/uca/internal/exec"
)

func setFailureResult(res *result, exitCode int, updateCmd []string, output string, timeout time.Duration) {
	res.Status = agents.StatusFailed
	switch exitCode {
	case runner.ExitCodeTimeout:
		res.Reason = "timeout"
		if timeout > 0 {
			res.Explain = appendHint(res.Explain, fmt.Sprintf("command timed out after %s; rerun with --timeout 0 or increase it", timeout.Round(time.Second)))
		} else {
			res.Explain = appendHint(res.Explain, "command timed out; rerun with a larger --timeout")
		}
		return
	case runner.ExitCodeCanceled:
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
		return agents.ReasonQuota, "quota exceeded; retry later or update via npm (@google/gemini-cli)"
	}
	if isNpmGlobalMutate(updateCmd) && (strings.Contains(output, "ENOTEMPTY") ||
		strings.Contains(output, "errno -66") ||
		strings.Contains(lower, "directory not empty")) {
		return agents.ReasonNpmNotEmpty, "npm rename failed; retry or remove leftover temp directory under the global npm prefix"
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
