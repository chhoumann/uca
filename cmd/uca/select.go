// Agent selection (--only/--skip) and the derived prefetch/prewarm sets.
package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chhoumann/uca/internal/agents"
	"github.com/chhoumann/uca/internal/detect"
)

func noSelectionMessage(unknown []string, all []agents.Agent) string {
	if len(unknown) > 0 {
		return fmt.Sprintf("no agents selected (unknown: %s; valid: %s)", strings.Join(unknown, " "), knownAgentNames(all))
	}
	return "no agents selected"
}

func knownAgentNames(all []agents.Agent) string {
	names := make([]string, 0, len(all))
	for _, a := range all {
		names = append(names, a.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func filterAgents(all []agents.Agent, onlyRaw, skipRaw string) ([]agents.Agent, []string) {
	known := make(map[string]string, len(all))
	for _, agent := range all {
		name := strings.ToLower(agent.Name)
		known[name] = name
		for _, alias := range agent.Aliases {
			alias = strings.ToLower(strings.TrimSpace(alias))
			if alias == "" {
				continue
			}
			known[alias] = name
		}
	}

	only, onlyUnknown := normalizeAgentList(parseList(onlyRaw), known)
	skip, skipUnknown := normalizeAgentList(parseList(skipRaw), known)

	// Distinguish "no --only given" (select all) from "--only given but every
	// entry was unknown" (select none). Without this, a typo like `--only bogus`
	// would fall through to selecting every agent.
	onlyProvided := strings.TrimSpace(onlyRaw) != ""

	unknownSet := map[string]bool{}
	for _, name := range onlyUnknown {
		unknownSet[name] = true
	}
	for _, name := range skipUnknown {
		unknownSet[name] = true
	}

	selected := make([]agents.Agent, 0, len(all))
	for _, agent := range all {
		// only/skip are keyed by lowercased canonical names; match the same way
		// so a user-defined agent with an uppercase name is still targetable.
		name := strings.ToLower(agent.Name)
		if onlyProvided && !only[name] {
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

func normalizeAgentList(items map[string]bool, known map[string]string) (map[string]bool, []string) {
	normalized := map[string]bool{}
	unknown := []string{}
	for name := range items {
		canonical, ok := known[name]
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		normalized[canonical] = true
	}
	return normalized, unknown
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

// prewarmNeeds computes which detection loaders the selected agents can consult,
// so Prewarm only spawns manager probes that resolution might actually read
// (e.g. `--only claude` needs no package-manager probes at all).
func prewarmNeeds(selected []agents.Agent) detect.PrewarmNeeds {
	var needs detect.PrewarmNeeds
	for _, agent := range selected {
		// getVersion falls back to the VS Code extension version whenever an
		// extension ID is present, independent of the chosen strategy.
		if agent.ExtensionID != "" {
			needs.VSCode = true
		}
		for _, strat := range agent.Strategies {
			switch strat.Kind {
			case agents.KindNpm, agents.KindPnpm, agents.KindYarn, agents.KindBun:
				needs.Node = true
			case agents.KindBrew:
				needs.Brew = true
			case agents.KindPip:
				needs.Pip = true
			case agents.KindUv:
				needs.Uv = true
			case agents.KindVSCode:
				needs.VSCode = true
			}
		}
	}
	return needs
}

// nodePackages returns the distinct node package specs the selected agents
// reference, in selection order.
func nodePackages(selected []agents.Agent) []detect.PackageQuery {
	pkgs := []detect.PackageQuery{}
	seen := map[string]bool{}
	for _, agent := range selected {
		for _, strat := range agent.Strategies {
			pkg := strings.TrimSpace(strat.Package)
			if !agents.IsNodeKind(strat.Kind) || pkg == "" {
				continue
			}
			spec := strings.TrimSpace(strat.Version)
			keySpec := spec
			if keySpec == "" {
				keySpec = "latest"
			}
			key := pkg + "\x00" + keySpec
			if !seen[key] {
				seen[key] = true
				pkgs = append(pkgs, detect.PackageQuery{Package: pkg, Spec: spec})
			}
			break
		}
	}
	return pkgs
}

// extensionIDs returns the distinct VS Code extension IDs the selected agents
// reference, in selection order.
func extensionIDs(selected []agents.Agent) []string {
	ids := []string{}
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, agent := range selected {
		add(agent.ExtensionID)
		for _, strat := range agent.Strategies {
			if strat.Kind == agents.KindVSCode {
				add(strat.ExtensionID)
			}
		}
	}
	return ids
}
