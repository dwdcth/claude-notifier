package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/felipeelias/claude-notifier/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	err := os.WriteFile(path, []byte(`
[global]
timeout = "5s"

[[notifiers.ntfy]]
url = "https://ntfy.example.com/topic1"

[[notifiers.ntfy]]
url = "https://ntfy.example.com/topic2"
`), 0644)
	require.NoError(t, err)

	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, 5*time.Second, cfg.Global.Timeout)
	require.Len(t, cfg.Notifiers, 1)         // one key: "ntfy"
	require.Len(t, cfg.Notifiers["ntfy"], 2) // two instances
}

func TestLoadConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	err := os.WriteFile(path, []byte(`
[[notifiers.ntfy]]
url = "https://ntfy.example.com/topic"
`), 0644)
	require.NoError(t, err)

	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, 10*time.Second, cfg.Global.Timeout) // default
}

func TestLoadConfigMissing(t *testing.T) {
	_, err := config.Load("/nonexistent/config.toml")
	assert.Error(t, err)
}

func TestDefaultPath(t *testing.T) {
	home := t.TempDir()

	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)

	got := config.DefaultPath()
	want := home + "/.config/claude-notifier/config.toml"
	assert.Equal(t, want, got)
}

func TestDefaultPathXDGConfigHome(t *testing.T) {
	xdg := t.TempDir()

	t.Setenv("XDG_CONFIG_HOME", xdg)

	got := config.DefaultPath()
	want := xdg + "/claude-notifier/config.toml"
	assert.Equal(t, want, got)
}

func TestSaveLoadRoundTrip(t *testing.T) {
	original := `# claude-notifier configuration

[global]
timeout = "10s"

[approver]
server = "https://ntfy.sh"
topic = "test-topic"
timeout = "60s"

[[notifiers.ntfy]]
url = "https://ntfy.sh/test"
priority = "default"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(original), 0600))

	cfg, err := config.Load(path)
	require.NoError(t, err)

	require.NoError(t, config.Save(path, cfg))

	cfg2, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, cfg.Global.Timeout, cfg2.Global.Timeout, "global timeout should round-trip")
	assert.Equal(t, cfg.Approver.Topic, cfg2.Approver.Topic, "approver topic should round-trip")
	assert.Equal(t, cfg.Approver.Server, cfg2.Approver.Server, "approver server should round-trip")
	require.Len(t, cfg2.Notifiers["ntfy"], 1, "ntfy notifier should be preserved")

	var ntfyCfg struct {
		URL      string `toml:"url"`
		Priority string `toml:"priority"`
	}
	require.NoError(t, cfg2.Decode(cfg2.Notifiers["ntfy"][0], &ntfyCfg))
	assert.Equal(t, "https://ntfy.sh/test", ntfyCfg.URL, "ntfy url should round-trip")
	assert.Equal(t, "default", ntfyCfg.Priority, "ntfy priority should round-trip")
}

func TestSaveCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "config.toml")

	cfg := &config.Config{}
	require.NoError(t, config.Save(path, cfg))

	_, err := os.Stat(path)
	assert.NoError(t, err, "config file should be created in nested dir")
}

func TestSaveClearsApproverKeepsNotifiers(t *testing.T) {
	original := `[global]
timeout = "10s"

[approver]
server = "https://ntfy.sh"
topic = "test-topic"

[[notifiers.ntfy]]
url = "https://ntfy.sh/test"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(original), 0600))

	cfg, err := config.Load(path)
	require.NoError(t, err)

	// Simulate uninstall: clear approver
	cfg.Approver = config.Approver{}
	require.NoError(t, config.Save(path, cfg))

	cfg2, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, "", cfg2.Approver.Topic, "approver should be cleared")
	assert.Equal(t, 10*time.Second, cfg2.Global.Timeout, "global should be preserved")
	require.Len(t, cfg2.Notifiers["ntfy"], 1, "ntfy notifier should survive approver clear")

	// The notifier's inner fields must survive textually.
	var ntfyCfg struct {
		URL string `toml:"url"`
	}
	require.NoError(t, cfg2.Decode(cfg2.Notifiers["ntfy"][0], &ntfyCfg))
	assert.Equal(t, "https://ntfy.sh/test", ntfyCfg.URL, "ntfy url must survive approver clear")
}

func TestSaveInsertsApproverWhenAbsent(t *testing.T) {
	original := `[global]
timeout = "10s"

[[notifiers.ntfy]]
url = "https://ntfy.sh/test"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(original), 0600))

	cfg, err := config.Load(path)
	require.NoError(t, err)

	cfg.Approver = config.Approver{
		Server:  "https://ntfy.sh",
		Topic:   "cra-abc",
		Timeout: 60 * time.Second,
	}
	require.NoError(t, config.Save(path, cfg))

	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	body := string(saved)

	// Approver section must appear once, between global and notifiers.
	assert.Contains(t, body, "[approver]\nserver = \"https://ntfy.sh\"\ntopic = \"cra-abc\"")
	assert.Contains(t, body, "url = \"https://ntfy.sh/test\"", "notifier must be preserved")

	cfg2, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "cra-abc", cfg2.Approver.Topic)
	require.Len(t, cfg2.Notifiers["ntfy"], 1)

	var ntfyCfg struct {
		URL string `toml:"url"`
	}
	require.NoError(t, cfg2.Decode(cfg2.Notifiers["ntfy"][0], &ntfyCfg))
	assert.Equal(t, "https://ntfy.sh/test", ntfyCfg.URL)
}

func TestSavePreservesCommentsAndNotifiers(t *testing.T) {
	original := `# claude-notifier configuration

# Global timeout for notifications
[global]
timeout = "10s"

# Multiple ntfy instances
[[notifiers.ntfy]]
url = "https://ntfy.sh/a"

[[notifiers.ntfy]]
url = "https://ntfy.sh/b"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(original), 0600))

	cfg, err := config.Load(path)
	require.NoError(t, err)

	cfg.Approver = config.Approver{Topic: "x", Server: "https://ntfy.sh"}
	require.NoError(t, config.Save(path, cfg))

	cfg2, err := config.Load(path)
	require.NoError(t, err)
	require.Len(t, cfg2.Notifiers["ntfy"], 2, "both ntfy instances must survive")

	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(saved), "# claude-notifier configuration", "top comment must survive")
	assert.Contains(t, string(saved), "# Global timeout for notifications")
	assert.Contains(t, string(saved), "# Multiple ntfy instances")
}
