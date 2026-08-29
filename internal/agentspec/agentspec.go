// Package agentspec resolves how to update an agent. Resolve walks an agent's
// strategies in order, probing the environment through the Env interface
// (satisfied structurally by *detect.Env), and returns the first applicable
// update command. The command builders alongside it are pure functions over
// the agent registry types.
package agentspec

import (
	"fmt"
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

// versionSpec returns the version selector for a package spec: a tag or exact
// version when set, otherwise "latest" (forced to avoid getting stuck on old
// minor/prerelease versions, common for 0.x CLIs).
func versionSpec(v string) string {
	if s := strings.TrimSpace(v); s != "" {
		return s
	}
	return "latest"
}

// nodeInstallArgv maps each node package manager to its global-install argv
// prefix, shared by nodeUpdateCommand and NodeBatchUpdateCommand. npm uses
// install because `npm update -g` does not accept `pkg@version` specs.
var nodeInstallArgv = map[string][]string{
	agents.KindNpm:  {"npm", "install", "-g"},
	agents.KindPnpm: {"pnpm", "add", "-g"},
	agents.KindYarn: {"yarn", "global", "add"},
	agents.KindBun:  {"bun", "add", "-g"},
}

func nodeUpdateCommand(strat agents.UpdateStrategy) []string {
	if len(strat.Command) > 0 {
		return strat.Command
	}
	prefix, ok := nodeInstallArgv[strat.Kind]
	if !ok {
		return nil
	}
	cmd := make([]string, 0, len(prefix)+1)
	cmd = append(cmd, prefix...)
	return append(cmd, strat.Package+"@"+versionSpec(strat.Version))
}

// NodeBatchUpdateCommand builds one install command that updates several @latest
// packages at once under a single manager.
func NodeBatchUpdateCommand(kind string, pkgs []string) []string {
	prefix, ok := nodeInstallArgv[kind]
	if !ok {
		return nil
	}
	args := make([]string, 0, len(prefix)+len(pkgs))
	args = append(args, prefix...)
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
		if agents.IsNodeKind(strat.Kind) && strat.Package != "" {
			return strat.Package
		}
	}
	return ""
}

// Env is the minimal environment-capability surface Resolve needs. *detect.Env
// satisfies it structurally, so cmd passes the concrete type without an adapter.
type Env interface {
	HasBinary(name string) bool
	HasNodeManager(kind string) bool
	NodeManagerForBinary(name string) string
	NodeBinHasBinary(kind, name string) bool
	NodeManagerForPackage(pkg string) string
	HasBrew() bool
	BrewHas(formula string) bool
	HasPython() bool
	PipHas(pkg string) bool
	HasUv() bool
	UvHas(pkg string) bool
	CodeCmd() string
	VscodeHas(extID string) bool
	HelpMatches(binary, contains string) bool
}

// Resolved is the outcome of resolving an agent against the environment: the
// update command and how it was chosen.
type Resolved struct {
	Cmd        []string
	Reason     string
	Method     string
	Detail     string
	VersionCmd []string
	// Pkg is the package/formula identifier the update targets, used by --check to
	// look up the latest version. Empty when latest is not knowable.
	Pkg string
	// Version is the version spec for a resolved node strategy (empty means
	// @latest). Only node kinds populate it; a uv spec is baked directly into Cmd.
	Version string
}

func nativeBinary(agent agents.Agent, strat agents.UpdateStrategy) string {
	if strat.Binary != "" {
		return strat.Binary
	}
	return agent.Binary
}

func nativeVersionCmd(agent agents.Agent, strat agents.UpdateStrategy) []string {
	if len(strat.VersionCmd) > 0 {
		return strat.VersionCmd
	}
	return agent.VersionCmd
}

// Resolve decides how to update an agent given the detected environment, walking
// its strategies in order and returning the first applicable update command (or
// a reason it was skipped).
func Resolve(agent agents.Agent, env Env) Resolved {
	codeMissing := false
	nativeIdentityMiss := ""
	// The node-manager match is computed lazily on the first node strategy, so
	// agents that resolve natively (or have no node strategies at all) never
	// trigger the node bin-dir / package-list probes.
	nodeManager := ""
	packageManager := ""
	packageName := NodePackageName(agent.Strategies)
	managersResolved := false
	resolveManagers := func() {
		if managersResolved {
			return
		}
		managersResolved = true
		if agent.Binary != "" {
			nodeManager = env.NodeManagerForBinary(agent.Binary)
		}
		if nodeManager == "" && packageName != "" {
			packageManager = env.NodeManagerForPackage(packageName)
		}
	}

	for _, strat := range agent.Strategies {
		switch strat.Kind {
		case agents.KindNative:
			binary := nativeBinary(agent, strat)
			if binary != "" && !env.HasBinary(binary) {
				continue
			}
			if !env.HelpMatches(binary, strat.HelpContains) {
				nativeIdentityMiss = fmt.Sprintf("binary %s found but help text did not identify %s", binary, strat.HelpContains)
				continue
			}
			return Resolved{
				Cmd:        strat.Command,
				Method:     strat.Kind,
				Detail:     fmt.Sprintf("binary %s found; using built-in update", binary),
				VersionCmd: nativeVersionCmd(agent, strat),
			}
		case agents.KindBun, agents.KindNpm, agents.KindPnpm, agents.KindYarn:
			if !env.HasNodeManager(strat.Kind) {
				continue
			}
			if agent.Binary == "" || strat.Package == "" {
				continue
			}
			resolveManagers()
			switch {
			case nodeManager != "":
				if nodeManager != strat.Kind {
					continue
				}
			case packageManager != "":
				if packageManager != strat.Kind {
					continue
				}
				detail := fmt.Sprintf("%s global package %s installed; matched by package list; updating via %s", strat.Kind, strat.Package, strat.Kind)
				return Resolved{Cmd: nodeUpdateCommand(strat), Method: strat.Kind, Detail: detail, Pkg: strat.Package, Version: strat.Version}
			default:
				if !env.NodeBinHasBinary(strat.Kind, agent.Binary) {
					continue
				}
			}
			detail := fmt.Sprintf("%s global bin has %s; matched by bin dir; updating via %s", strat.Kind, agent.Binary, strat.Kind)
			return Resolved{Cmd: nodeUpdateCommand(strat), Method: strat.Kind, Detail: detail, Pkg: strat.Package, Version: strat.Version}
		case agents.KindBrew:
			if !env.HasBrew() {
				continue
			}
			if env.BrewHas(strat.Package) {
				detail := fmt.Sprintf("brew formula %s installed", strat.Package)
				return Resolved{Cmd: []string{"brew", "upgrade", strat.Package}, Method: strat.Kind, Detail: detail, Pkg: strat.Package}
			}
		case agents.KindPip:
			if !env.HasPython() {
				continue
			}
			if env.PipHas(strat.Package) {
				detail := fmt.Sprintf("pip package %s installed", strat.Package)
				return Resolved{Cmd: []string{"python3", "-m", "pip", "install", "-U", "--upgrade-strategy", "only-if-needed", strat.Package}, Method: strat.Kind, Detail: detail, Pkg: strat.Package}
			}
		case agents.KindUv:
			if !env.HasUv() {
				continue
			}
			if env.UvHas(strat.Package) {
				detail := fmt.Sprintf("uv tool %s installed", strat.Package)
				return Resolved{Cmd: []string{"uv", "tool", "install", "--force", "--python", "python3.12", "--with", "pip", strat.Package + "@" + versionSpec(strat.Version)}, Method: strat.Kind, Detail: detail, Pkg: strat.Package}
			}
		case agents.KindVSCode:
			if env.CodeCmd() == "" {
				codeMissing = true
				continue
			}
			if env.VscodeHas(strat.ExtensionID) {
				detail := fmt.Sprintf("VS Code extension %s installed (via %s)", strat.ExtensionID, env.CodeCmd())
				// Pkg carries the extension ID: the marketplace lookup key.
				return Resolved{Cmd: []string{env.CodeCmd(), "--install-extension", strat.ExtensionID, "--force"}, Method: strat.Kind, Detail: detail, Pkg: strat.ExtensionID}
			}
		}
	}

	if codeMissing {
		return Resolved{Reason: agents.ReasonMissingCode, Detail: "VS Code CLI not found (code/codium/code-insiders)"}
	}
	if agent.Binary != "" && env.HasBinary(agent.Binary) {
		return Resolved{Reason: agents.ReasonManualInstall, Detail: "binary found but no supported install method detected"}
	}
	if nativeIdentityMiss != "" {
		return Resolved{Reason: agents.ReasonMissing, Detail: nativeIdentityMiss + "; no supported binary or install method detected"}
	}
	return Resolved{Reason: agents.ReasonMissing, Detail: "no supported binary or install method detected"}
}
