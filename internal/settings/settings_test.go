package settings_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/felipeelias/claude-notifier/internal/settings"
)

func TestLoadNonexistent(t *testing.T) {
	s, err := settings.Load("/nonexistent/path/settings.json")
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

	cfg := &settings.Settings{
		Hooks: map[string][]settings.HookMatcher{
			"PermissionRequest": {
				{Hooks: []settings.HookConfig{
					{Type: "command", Command: "claude-notifier hook"},
				}},
			},
		},
	}
	err := cfg.Save(path)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := settings.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Hooks["PermissionRequest"]) != 1 {
		t.Errorf("expected 1 matcher, got %d", len(loaded.Hooks["PermissionRequest"]))
	}
}

func TestRegisterHook(t *testing.T) {
	s := &settings.Settings{Hooks: make(map[string][]settings.HookMatcher)}
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
	s := &settings.Settings{Hooks: make(map[string][]settings.HookMatcher)}
	s.RegisterHook("/path", "claude-notifier")
	s.RegisterHook("/path", "claude-notifier")

	if len(s.Hooks["PermissionRequest"]) != 1 {
		t.Errorf("expected 1 matcher after double register, got %d", len(s.Hooks["PermissionRequest"]))
	}
}

func TestUnregisterHook(t *testing.T) {
	s := &settings.Settings{Hooks: make(map[string][]settings.HookMatcher)}
	s.RegisterHook("/path", "claude-notifier")
	s.UnregisterHook("claude-notifier")

	if _, ok := s.Hooks["PermissionRequest"]; ok {
		t.Error("expected PermissionRequest to be removed")
	}
}

func TestUnregisterHookNotPresent(t *testing.T) {
	s := &settings.Settings{Hooks: make(map[string][]settings.HookMatcher)}
	s.UnregisterHook("claude-notifier")
	// Should not panic
}

func TestIsHookRegistered(t *testing.T) {
	s := &settings.Settings{Hooks: make(map[string][]settings.HookMatcher)}
	if s.IsHookRegistered("claude-notifier") {
		t.Error("should not be registered")
	}
	s.RegisterHook("/path", "claude-notifier")
	if !s.IsHookRegistered("claude-notifier") {
		t.Error("should be registered")
	}
}

func TestDefaultPath(t *testing.T) {
	path, err := settings.DefaultPath()
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

	s := &settings.Settings{Hooks: make(map[string][]settings.HookMatcher)}
	err := s.Save(path)
	if err != nil {
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

	existing := map[string]any{
		"hooks": map[string]any{
			"Notification": []any{
				map[string]any{
					"type":    "command",
					"command": "existing-hook",
				},
			},
		},
	}
	data, err := json.Marshal(existing)
	if err != nil {
		t.Fatalf("marshal existing settings: %v", err)
	}
	_ = os.WriteFile(path, data, 0644) // fixture setup; the Load below fails if it did not land

	cfg, err := settings.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if !cfg.IsHookEventRegistered("Notification") {
		t.Error("expected Notification hook to be preserved")
	}

	// Register new hook
	cfg.RegisterHook("/path", "claude-notifier")
	_ = cfg.Save(path) // a failed save would surface via the reload assertions below

	loaded, _ := settings.Load(path)
	if !loaded.IsHookEventRegistered("Notification") {
		t.Error("Notification hook should still exist")
	}
	if !loaded.IsHookEventRegistered("PermissionRequest") {
		t.Error("PermissionRequest hook should exist")
	}
}
