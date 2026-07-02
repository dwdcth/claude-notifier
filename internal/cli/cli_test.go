package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcli "github.com/felipeelias/claude-notifier/internal/cli"
	"github.com/felipeelias/claude-notifier/internal/notifier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeNotifier captures the last notification it received.
type fakeNotifier struct {
	last notifier.Notification
}

func (f *fakeNotifier) Name() string { return "fake" }
func (f *fakeNotifier) Send(_ context.Context, n notifier.Notification) error {
	f.last = n
	return nil
}

func TestInitCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "claude-notifier", "config.toml")

	app := appcli.New("test", notifier.NewRegistry())
	err := app.Run([]string{"claude-notifier", "--config", configPath, "init"})
	require.NoError(t, err)

	_, err = os.Stat(configPath)
	require.NoError(t, err, "config file should exist")

	content, _ := os.ReadFile(configPath)
	assert.Contains(t, string(content), "[global]")
}

func TestInitCommandAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	err := os.WriteFile(configPath, []byte("existing"), 0644)
	require.NoError(t, err)

	app := appcli.New("test", notifier.NewRegistry())
	err = app.Run([]string{"claude-notifier", "--config", configPath, "init"})
	assert.Error(t, err, "should fail if config already exists")
}

func TestVersionFlag(t *testing.T) {
	var buf bytes.Buffer
	app := appcli.New("1.2.3", notifier.NewRegistry())
	app.Writer = &buf

	err := app.Run([]string{"claude-notifier", "--version"})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "1.2.3")
}

// newFakeRegistry registers a single fakeNotifier under "fake" and returns
// both the registry and the fake so tests can inspect what was sent.
func newFakeRegistry(t *testing.T) (*notifier.Registry, *fakeNotifier) {
	t.Helper()
	fake := &fakeNotifier{}
	reg := notifier.NewRegistry()
	require.NoError(t, reg.Register("fake", func() notifier.Notifier { return fake }))
	return reg, fake
}

// writeConfig writes a minimal TOML config enabling the "fake" notifier.
func writeConfig(t *testing.T, configPath string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o750))
	content := `[[notifiers.fake]]
`
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o600))
}

func TestStopCommand(t *testing.T) {
	reg, fake := newFakeRegistry(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	writeConfig(t, configPath)

	oldStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = oldStdin })

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = w.WriteString(`{"session_id":"abc","transcript_path":"/tmp/t.jsonl","cwd":"/Users/me/myproject","hook_event_name":"Stop"}`)
	require.NoError(t, err)
	w.Close()
	os.Stdin = r

	app := appcli.New("test", reg)
	err = app.Run([]string{"claude-notifier", "--config", configPath, "stop"})
	require.NoError(t, err)

	assert.Equal(t, "Conversation ended in myproject", fake.last.Message)
	assert.Equal(t, "Claude Code", fake.last.Title)
	assert.Equal(t, "/Users/me/myproject", fake.last.Cwd)
	assert.Equal(t, "abc", fake.last.SessionID)
	assert.Equal(t, "/tmp/t.jsonl", fake.last.TranscriptPath)
	assert.Equal(t, "stop", fake.last.NotificationType)
}

func TestStopCommandInvalidJSON(t *testing.T) {
	reg, fake := newFakeRegistry(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	writeConfig(t, configPath)

	oldStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = oldStdin })

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = w.WriteString(`not json`)
	require.NoError(t, err)
	w.Close()
	os.Stdin = r

	var errBuf bytes.Buffer
	app := appcli.New("test", reg)
	app.ErrWriter = &errBuf
	err = app.Run([]string{"claude-notifier", "--config", configPath, "stop"})
	require.NoError(t, err, "stop must not fail the hook on bad input")

	assert.Empty(t, fake.last.Message, "no notification should be sent on bad input")
}

func TestStopCommandEmptyCwdFallsBackToGetwd(t *testing.T) {
	reg, fake := newFakeRegistry(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	writeConfig(t, configPath)

	oldStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = oldStdin })

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = w.WriteString(`{"session_id":"abc","hook_event_name":"Stop"}`)
	require.NoError(t, err)
	w.Close()
	os.Stdin = r

	wd, err := os.Getwd()
	require.NoError(t, err)

	app := appcli.New("test", reg)
	err = app.Run([]string{"claude-notifier", "--config", configPath, "stop"})
	require.NoError(t, err)

	assert.Equal(t, wd, fake.last.Cwd)
	assert.True(t, strings.HasSuffix(fake.last.Message, filepath.Base(wd)),
		"message should include project name; got %q", fake.last.Message)
	assert.Equal(t, "stop", fake.last.NotificationType)
}
