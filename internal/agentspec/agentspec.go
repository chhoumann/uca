// Package agentspec builds the concrete update commands for an agent's chosen
// strategy. These are pure functions over the agent registry types; the resolver
// that decides which strategy applies (given the environment) lives in cmd/uca
// for now and calls into these builders.
package agentspec

import (
	"strings"

	"github.com/chhoumann/uca/internal/agents"
)

// ShouldLockKind reports whether updates of this kind mutate shared global state
// and must be serialized per manager.
func ShouldLockKind(kind string) bool {
	switch kind {
	case agents.KindNpm, agents.KindPnpm, agents.KindYarn, agents.KindBun, agents.KindBrew, agents.KindPip, agents.KindUv, agents.KindVSCode:
		return true
	default:
		return false
	}
}

// IsNodeKind reports whether the kind is a node package manager (npm/pnpm/yarn/bun).
func IsNodeKind(kind string) bool {
	switch kind {
	case agents.KindNpm, agents.KindPnpm, agents.KindYarn, agents.KindBun:
		return true
	default:
		return false
	}
}

// VersionSpec returns the version selector for a package spec: a pinned version
// when set, otherwise "latest" (forced to avoid getting stuck on old
// minor/prerelease versions, common for 0.x CLIs).
func VersionSpec(v string) string {
	if s := strings.TrimSpace(v); s != "" {
		return s
	}
	return "latest"
}

// NodeUpdateCommand builds the single-package update command for a node strategy
// (honoring a pinned Version, else @latest).
func NodeUpdateCommand(strat agents.UpdateStrategy) []string {
	if len(strat.Command) > 0 {
		return strat.Command
	}
	spec := strat.Package + "@" + VersionSpec(strat.Version)
	switch strat.Kind {
	case agents.KindNpm:
		// `npm update -g` does not accept `pkg@version` specs, so we use install.
		return []string{"npm", "install", "-g", spec}
	case agents.KindPnpm:
		return []string{"pnpm", "add", "-g", spec}
	case agents.KindYarn:
		return []string{"yarn", "global", "add", spec}
	case agents.KindBun:
		return []string{"bun", "add", "-g", spec}
	default:
		return strat.Command
	}
}

// NodeBatchUpdateCommand builds one install command that updates several @latest
// packages at once under a single manager.
func NodeBatchUpdateCommand(kind string, pkgs []string) []string {
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

// NodePackageName returns the package name from an agent's first node strategy.
func NodePackageName(strategies []agents.UpdateStrategy) string {
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
