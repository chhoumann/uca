package agents

type UpdateStrategy struct {
	Kind        string
	Command     []string
	Package     string
	ExtensionID string
}

// Agent defines how to update and version a CLI tool.
type Agent struct {
	Name        string
	Binary      string
	VersionCmd  []string
	ExtensionID string
	Strategies  []UpdateStrategy
}

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
			Name:       "cursor",
			Binary:     "cursor-agent",
			VersionCmd: []string{"cursor-agent", "--version"},
			Strategies: []UpdateStrategy{{Kind: KindNative, Command: []string{"cursor-agent", "update"}}},
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
			Strategies: nodePackageStrategies("@mariozechner/pi-coding-agent"),
		},
	}
}
