package agents

// UpdateStrategy is one candidate way to update an agent, tried in order by
// the resolver. Kind selects the mechanism (see the Kind* constants); the
// other fields configure it. Like Agent, it is part of the user-facing JSON
// config schema (see cmd/uca loadConfigAgents).
type UpdateStrategy struct {
	Kind         string   `json:"kind"`
	Command      []string `json:"command,omitempty"`
	Package      string   `json:"package,omitempty"`
	ExtensionID  string   `json:"extensionId,omitempty"`
	Binary       string   `json:"binary,omitempty"`
	VersionCmd   []string `json:"versionCmd,omitempty"`
	HelpContains string   `json:"helpContains,omitempty"`
	// Version pins a specific version for node/uv strategies instead of @latest
	// (e.g. to hold a 0.x CLI back from a breaking release). Empty means @latest.
	Version string `json:"version,omitempty"`
}

// Agent defines how to update and version a CLI tool. The JSON tags let users
// describe their own agents in a config file (see cmd/uca loadConfigAgents).
type Agent struct {
	Name        string           `json:"name"`
	Binary      string           `json:"binary,omitempty"`
	VersionCmd  []string         `json:"versionCmd,omitempty"`
	ExtensionID string           `json:"extensionId,omitempty"`
	Aliases     []string         `json:"aliases,omitempty"`
	Strategies  []UpdateStrategy `json:"strategies,omitempty"`
}

// Update result status vocabulary, shared by the resolver, the renderer, and the
// orchestrator so all sites compare against one source of truth.
const (
	StatusPending   = "pending"
	StatusUpdating  = "updating"
	StatusUpdated   = "updated"
	StatusUnchanged = "unchanged"
	StatusSkipped   = "skipped"
	StatusFailed    = "failed"
)

// Skip/failure reason vocabulary surfaced to the user.
const (
	ReasonMissing       = "missing"
	ReasonMissingCode   = "missing vscode"
	ReasonManualInstall = "manual install"
	ReasonQuota         = "quota"
	ReasonNpmNotEmpty   = "npm ENOTEMPTY"
	ReasonDryRun        = "dry-run"
)

// Update lifecycle phases emitted to the live UI.
const (
	PhaseDetect = "detect"
	PhaseStart  = "start"
	PhaseFinish = "finish"
)

const (
	KindNative = "native"
	KindBun    = "bun"
	KindBrew   = "brew"
	KindNpm    = "npm"
	KindPnpm   = "pnpm"
	KindYarn   = "yarn"
	KindPip    = "pip"
	KindUv     = "uv"
	KindVSCode = "vscode"
)

// ValidKind reports whether kind is a recognized update-strategy kind. Used to
// validate user-supplied config so a typo'd kind fails loudly instead of being
// silently dropped by the resolver.
func ValidKind(kind string) bool {
	switch kind {
	case KindNative, KindBun, KindBrew, KindNpm, KindPnpm, KindYarn, KindPip, KindUv, KindVSCode:
		return true
	default:
		return false
	}
}

// IsNodeKind reports whether kind is a node package manager (npm/pnpm/yarn/bun).
func IsNodeKind(kind string) bool {
	switch kind {
	case KindNpm, KindPnpm, KindYarn, KindBun:
		return true
	default:
		return false
	}
}

func nodePackageStrategies(pkg string) []UpdateStrategy {
	return []UpdateStrategy{
		{Kind: KindNpm, Package: pkg},
		{Kind: KindPnpm, Package: pkg},
		{Kind: KindYarn, Package: pkg},
		{Kind: KindBun, Package: pkg},
	}
}

// Default returns the built-in supported agents.
func Default() []Agent {
	return []Agent{
		{
			Name:       "amp",
			Binary:     "amp",
			VersionCmd: []string{"amp", "--version"},
			Strategies: []UpdateStrategy{{Kind: KindNative, Command: []string{"amp", "update"}}},
		},
		{
			Name:       "gemini",
			Binary:     "gemini",
			VersionCmd: []string{"gemini", "--version"},
			Strategies: nodePackageStrategies("@google/gemini-cli"),
		},
		{
			Name:       "claude",
			Binary:     "claude",
			VersionCmd: []string{"claude", "--version"},
			Strategies: []UpdateStrategy{{Kind: KindNative, Command: []string{"claude", "update"}}},
		},
		{
			Name:       "codex",
			Binary:     "codex",
			VersionCmd: []string{"codex", "--version"},
			Strategies: nodePackageStrategies("@openai/codex"),
		},
		{
			Name:       "opencode",
			Binary:     "opencode",
			VersionCmd: []string{"opencode", "--version"},
			Strategies: nodePackageStrategies("opencode-ai"),
		},
		{
			Name:       "droid",
			Binary:     "droid",
			VersionCmd: []string{"droid", "--version"},
			Strategies: append(nodePackageStrategies("droid"), UpdateStrategy{Kind: KindNative, Command: []string{"droid", "update"}}),
		},
		{
			Name:       "cursor",
			Binary:     "cursor-agent",
			VersionCmd: []string{"cursor-agent", "--version"},
			Aliases:    []string{"agent"},
			Strategies: []UpdateStrategy{
				{
					Kind:         KindNative,
					Binary:       "agent",
					Command:      []string{"agent", "update"},
					VersionCmd:   []string{"agent", "--version"},
					HelpContains: "Cursor Agent",
				},
				{
					Kind:    KindNative,
					Command: []string{"cursor-agent", "update"},
				},
			},
		},
		{
			Name:       "copilot",
			Binary:     "copilot",
			VersionCmd: []string{"copilot", "--version"},
			Strategies: append([]UpdateStrategy{{Kind: KindBrew, Package: "copilot-cli"}}, nodePackageStrategies("@github/copilot")...),
		},
		{
			Name:        "cline",
			Binary:      "cline",
			VersionCmd:  []string{"cline", "--version"},
			ExtensionID: "saoudrizwan.claude-dev",
			Strategies:  append(nodePackageStrategies("cline"), UpdateStrategy{Kind: KindVSCode, ExtensionID: "saoudrizwan.claude-dev"}),
		},
		{
			Name:        "roocode",
			ExtensionID: "RooVeterinaryInc.roo-cline",
			Strategies: []UpdateStrategy{
				{Kind: KindVSCode, ExtensionID: "RooVeterinaryInc.roo-cline"},
			},
		},
		{
			Name:       "aider",
			Binary:     "aider",
			VersionCmd: []string{"aider", "--version"},
			Strategies: []UpdateStrategy{
				{Kind: KindUv, Package: "aider-chat"},
				{Kind: KindPip, Package: "aider-chat"},
			},
		},
		{
			Name:       "pi",
			Binary:     "pi",
			VersionCmd: []string{"pi", "--version"},
			Strategies: nodePackageStrategies("@earendil-works/pi-coding-agent"),
		},
		{
			Name:       "omp",
			Binary:     "omp",
			VersionCmd: []string{"omp", "--version"},
			Strategies: []UpdateStrategy{
				// Native first: `omp update` checks the npm registry for latest,
				// detects the installer that owns the active executable, and for a
				// brew-owned binary runs `brew update` before `brew upgrade` - so
				// it sees a just-published release immediately, while a plain
				// `brew upgrade` only refreshes taps per Homebrew's periodic
				// auto-update policy. Brew/bun remain as fallbacks for a missing
				// binary.
				{Kind: KindNative, Command: []string{"omp", "update"}},
				{Kind: KindBrew, Package: "omp"},
				{Kind: KindBun, Package: "@oh-my-pi/pi-coding-agent"},
			},
		},
		{
			Name:       "grok",
			Binary:     "grok",
			VersionCmd: []string{"grok", "--version"},
			Strategies: append(nodePackageStrategies("@xai-official/grok"), UpdateStrategy{Kind: KindNative, Command: []string{"grok", "update"}}),
		},
	}
}
