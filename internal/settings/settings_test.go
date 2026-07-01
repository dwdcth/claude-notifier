package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadNonexistent(t *testing.T) {
	s, err := Load("/nonexistent/path/settings.json")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if s.Hooks == nil {
		t.Error("expected non-nil hooks map")
	}
}

func TestLoadAndSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	s := &Settings{
		Hooks: map[string][]HookMatcher{
			"PermissionRequest": {
				{Hooks: []HookConfig{
					{Type: "command", Command: "claude-notifier hook"},
				}},
			},
		},
	}
	if err := s.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Hooks["PermissionRequest"]) != 1 {
		t.Errorf("expected 1 matcher, got %d", len(loaded.Hooks["PermissionRequest"]))
	}
}

func TestRegisterHook(t *testing.T) {
	s := &Settings{Hooks: make(map[string][]HookMatcher)}
	s.RegisterHook("/usr/local/bin/claude-notifier", "claude-notifier")

	matchers := s.Hooks["PermissionRequest"]
	if len(matchers) != 1 {
		t.Fatalf("expected 1 matcher, got %d", len(matchers))
	}
	if len(matchers[0].Hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(matchers[0].Hooks))
	}
	cmd := matchers[0].Hooks[0].Command
	if cmd != "claude-notifier hook" {
		t.Errorf("command = %q", cmd)
	}
}

func TestRegisterHookIdempotent(t *testing.T) {
	s := &Settings{Hooks: make(map[string][]HookMatcher)}
	s.RegisterHook("/path", "claude-notifier")
	s.RegisterHook("/path", "claude-notifier")

	if len(s.Hooks["PermissionRequest"]) != 1 {
		t.Errorf("expected 1 matcher after double register, got %d", len(s.Hooks["PermissionRequest"]))
	}
}

func TestUnregisterHook(t *testing.T) {
	s := &Settings{Hooks: make(map[string][]HookMatcher)}
	s.RegisterHook("/path", "claude-notifier")
	s.UnregisterHook("claude-notifier")

	if _, ok := s.Hooks["PermissionRequest"]; ok {
		t.Error("expected PermissionRequest to be removed")
	}
}

func TestUnregisterHookNotPresent(t *testing.T) {
	s := &Settings{Hooks: make(map[string][]HookMatcher)}
	s.UnregisterHook("claude-notifier")
	// Should not panic
}

func TestIsHookRegistered(t *testing.T) {
	s := &Settings{Hooks: make(map[string][]HookMatcher)}
	if s.IsHookRegistered("claude-notifier") {
		t.Error("should not be registered")
	}
	s.RegisterHook("/path", "claude-notifier")
	if !s.IsHookRegistered("claude-notifier") {
		t.Error("should be registered")
	}
}

func TestDefaultPath(t *testing.T) {
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if path == "" {
		t.Error("expected non-empty path")
	}
}

func TestSaveFilePerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	s := &Settings{Hooks: make(map[string][]HookMatcher)}
	if err := s.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("perms = %o, want 0600", info.Mode().Perm())
	}
}

func TestLoadExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	existing := map[string]interface{}{
		"hooks": map[string]interface{}{
			"Notification": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "existing-hook",
						},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(existing)
	os.WriteFile(path, data, 0644)

	s, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if !s.IsHookEventRegistered("Notification") {
		t.Error("expected Notification hook to be preserved")
	}

	// Register new hook
	s.RegisterHook("/path", "claude-notifier")
	s.Save(path)

	loaded, _ := Load(path)
	if !loaded.IsHookEventRegistered("Notification") {
		t.Error("Notification hook should still exist")
	}
	if !loaded.IsHookEventRegistered("PermissionRequest") {
		t.Error("PermissionRequest hook should exist")
	}
}
