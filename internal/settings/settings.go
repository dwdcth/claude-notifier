package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// settingsFilePerms restricts settings.json to owner-only access.
const settingsFilePerms = 0600

type HookConfig struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type HookMatcher struct {
	Hooks []HookConfig `json:"hooks"`
}

type Settings struct {
	Hooks map[string][]HookMatcher `json:"hooks,omitempty"`
}

func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home dir: %w", err)
	}

	return filepath.Join(home, ".claude", "settings.json"), nil
}

func Load(path string) (*Settings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Settings{Hooks: make(map[string][]HookMatcher)}, nil
		}

		return nil, fmt.Errorf("reading settings: %w", err)
	}

	var parsed Settings
	err = json.Unmarshal(data, &parsed)
	if err != nil {
		return nil, fmt.Errorf("parsing settings: %w", err)
	}
	if parsed.Hooks == nil {
		parsed.Hooks = make(map[string][]HookMatcher)
	}

	return &parsed, nil
}

func (s *Settings) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling settings: %w", err)
	}
	err = os.WriteFile(path, data, settingsFilePerms)
	if err != nil {
		return fmt.Errorf("writing settings: %w", err)
	}

	return nil
}

func (s *Settings) RegisterHook(hookPath, command string) {
	const event = "PermissionRequest"
	commandStr := command + " hook"

	if s.Hooks == nil {
		s.Hooks = make(map[string][]HookMatcher)
	}

	matchers := s.Hooks[event]
	for _, m := range matchers {
		for _, h := range m.Hooks {
			if h.Command == commandStr {
				return
			}
		}
	}

	s.Hooks[event] = append(s.Hooks[event], HookMatcher{
		Hooks: []HookConfig{
			{Type: "command", Command: commandStr},
		},
	})
}

func (s *Settings) UnregisterHook(command string) {
	const event = "PermissionRequest"
	commandStr := command + " hook"

	matchers, ok := s.Hooks[event]
	if !ok {
		return
	}

	filtered := matchers[:0]
	for _, matcher := range matchers {
		keep := true
		for _, h := range matcher.Hooks {
			if h.Command == commandStr {
				keep = false

				break
			}
		}
		if keep {
			filtered = append(filtered, matcher)
		}
	}

	if len(filtered) == 0 {
		delete(s.Hooks, event)
	} else {
		s.Hooks[event] = filtered
	}
}

func (s *Settings) IsHookRegistered(command string) bool {
	const event = "PermissionRequest"
	commandStr := command + " hook"

	matchers, ok := s.Hooks[event]
	if !ok {
		return false
	}
	for _, m := range matchers {
		for _, h := range m.Hooks {
			if h.Command == commandStr {
				return true
			}
		}
	}

	return false
}

func (s *Settings) IsHookEventRegistered(event string) bool {
	_, ok := s.Hooks[event]

	return ok
}
