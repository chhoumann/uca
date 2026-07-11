// Optional user config: extra agent definitions merged over the built-ins.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chhoumann/uca/internal/agents"
)

// loadAgents returns the built-in agents merged with any user-defined agents from
// the optional config file. A malformed config is a hard error (fail fast rather
// than silently ignoring something the user wrote).
func loadAgents() ([]agents.Agent, error) {
	base := agents.Default()
	user, err := loadConfigAgents()
	if err != nil {
		return nil, err
	}
	if len(user) == 0 {
		return base, nil
	}
	return mergeAgents(base, user), nil
}

// configPath resolves the optional config file: $UCA_CONFIG if set, else
// <user-config-dir>/uca/config.json. Empty when no location can be determined.
func configPath() string {
	if p := strings.TrimSpace(os.Getenv("UCA_CONFIG")); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "uca", "config.json")
}

func loadConfigAgents() ([]agents.Agent, error) {
	path := configPath()
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // no config is the normal case
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return parseConfigAgents(data, path)
}

func parseConfigAgents(data []byte, path string) ([]agents.Agent, error) {
	var cfg struct {
		Agents []agents.Agent `json:"agents"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	// Surface keys that match no field. (encoding/json still matches known fields
	// case-insensitively, so a case-variant of a real key is accepted, not flagged.)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	for i, a := range cfg.Agents {
		if strings.TrimSpace(a.Name) == "" {
			return nil, fmt.Errorf("parse config %s: agent #%d is missing a name", path, i+1)
		}
		for j, s := range a.Strategies {
			if !agents.ValidKind(s.Kind) {
				return nil, fmt.Errorf("parse config %s: agent %q strategy #%d has unknown kind %q", path, a.Name, j+1, s.Kind)
			}
			if err := validateStrategy(a, s); err != nil {
				return nil, fmt.Errorf("parse config %s: agent %q strategy #%d (%s): %w", path, a.Name, j+1, s.Kind, err)
			}
		}
	}
	return cfg.Agents, nil
}

// validateStrategy rejects a strategy missing the field its kind cannot work
// without, so a config mistake fails at load time instead of surfacing as a
// permanently "missing" agent. Requirements mirror agentspec.Resolve: node
// strategies additionally need the agent-level binary for bin-dir matching.
func validateStrategy(a agents.Agent, s agents.UpdateStrategy) error {
	switch s.Kind {
	case agents.KindNative:
		if len(s.Command) == 0 {
			return errors.New(`missing "command"`)
		}
	case agents.KindBrew, agents.KindPip, agents.KindUv:
		if strings.TrimSpace(s.Package) == "" {
			return errors.New(`missing "package"`)
		}
	case agents.KindVSCode:
		if strings.TrimSpace(s.ExtensionID) == "" {
			return errors.New(`missing "extensionId"`)
		}
	case agents.KindNpm, agents.KindPnpm, agents.KindYarn, agents.KindBun:
		if strings.TrimSpace(s.Package) == "" {
			return errors.New(`missing "package"`)
		}
		if strings.TrimSpace(a.Binary) == "" {
			return errors.New(`node strategies require the agent "binary" for bin-dir matching`)
		}
	}
	return nil
}

// mergeAgents appends user-defined agents to the built-ins; a user agent whose
// name matches a built-in (case-insensitively) overrides it.
func mergeAgents(base, user []agents.Agent) []agents.Agent {
	out := append([]agents.Agent(nil), base...)
	idx := make(map[string]int, len(out))
	for i, a := range out {
		idx[strings.ToLower(a.Name)] = i
	}
	for _, ua := range user {
		key := strings.ToLower(ua.Name)
		if i, ok := idx[key]; ok {
			out[i] = ua
		} else {
			idx[key] = len(out)
			out = append(out, ua)
		}
	}
	return out
}
