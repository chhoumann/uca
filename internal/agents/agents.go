package agents

// Agent defines how to update and version a CLI tool.
type Agent struct {
	Name        string
	UpdateCmd   []string
	VersionCmd  []string
	RequiresBun bool
	Binary      string
}

// Default returns the built-in supported agents.
func Default() []Agent {
	return []Agent{
		{
			Name:       "amp",
			UpdateCmd:  []string{"amp", "update"},
			VersionCmd: []string{"amp", "--version"},
			Binary:     "amp",
		},
		{
			Name:       "gemini",
			UpdateCmd:  []string{"gemini", "update"},
			VersionCmd: []string{"gemini", "--version"},
			Binary:     "gemini",
		},
		{
			Name:       "claude",
			UpdateCmd:  []string{"claude", "update"},
			VersionCmd: []string{"claude", "--version"},
			Binary:     "claude",
		},
		{
			Name:        "codex",
			UpdateCmd:   []string{"bun", "update", "-g", "@openai/codex", "--latest"},
			VersionCmd:  []string{"codex", "--version"},
			Binary:      "codex",
			RequiresBun: true,
		},
		{
			Name:        "opencode",
			UpdateCmd:   []string{"bun", "update", "-g", "opencode-ai", "--latest"},
			VersionCmd:  []string{"opencode", "--version"},
			Binary:      "opencode",
			RequiresBun: true,
		},
	}
}
