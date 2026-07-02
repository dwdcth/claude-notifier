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

func TestStopCommandIncludesLastPromptAndReply(t *testing.T) {
	reg, fake := newFakeRegistry(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	writeConfig(t, configPath)

	transcriptPath := filepath.Join(dir, "transcript.jsonl")
	content := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"please fix the bug"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"done, all green"}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(transcriptPath, []byte(content), 0o600))

	oldStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = oldStdin })

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = w.WriteString(`{"session_id":"abc","transcript_path":"` + transcriptPath + `","cwd":"/Users/me/myproject","hook_event_name":"Stop"}`)
	require.NoError(t, err)
	w.Close()
	os.Stdin = r

	app := appcli.New("test", reg)
	err = app.Run([]string{"claude-notifier", "--config", configPath, "stop"})
	require.NoError(t, err)

	assert.Contains(t, fake.last.Message, "💬 你: please fix the bug")
	assert.Contains(t, fake.last.Message, "🤖 AI: done, all green")
	assert.NotContains(t, fake.last.Message, "Conversation ended")
	assert.Equal(t, "stop", fake.last.NotificationType)
}

func TestStopCommandFallsBackWhenTranscriptMissing(t *testing.T) {
	reg, fake := newFakeRegistry(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	writeConfig(t, configPath)

	oldStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = oldStdin })

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = w.WriteString(`{"session_id":"abc","transcript_path":"` + filepath.Join(dir, "missing.jsonl") + `","cwd":"/Users/me/myproject","hook_event_name":"Stop"}`)
	require.NoError(t, err)
	w.Close()
	os.Stdin = r

	app := appcli.New("test", reg)
	err = app.Run([]string{"claude-notifier", "--config", configPath, "stop"})
	require.NoError(t, err)

	assert.Equal(t, "Conversation ended in myproject", fake.last.Message)
}

// runSend feeds the given JSON payload to the default `claude-notifier`
// command (which invokes sendAction) and returns the captured fake
// notification.
func runSend(t *testing.T, reg *notifier.Registry, configPath, payload string) {
	t.Helper()
	oldStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = oldStdin })

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = w.WriteString(payload)
	require.NoError(t, err)
	w.Close()
	os.Stdin = r

	app := appcli.New("test", reg)
	err = app.Run([]string{"claude-notifier", "--config", configPath})
	require.NoError(t, err)
}

func TestSendActionIdlePromptAppendsPromptReply(t *testing.T) {
	reg, fake := newFakeRegistry(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	writeConfig(t, configPath)

	transcriptPath := filepath.Join(dir, "transcript.jsonl")
	content := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"what is 1+1?"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"it is 2"}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(transcriptPath, []byte(content), 0o600))

	payload := `{"message":"Claude is waiting for your input","notification_type":"idle_prompt","transcript_path":"` + transcriptPath + `","cwd":"/Users/me/myproject"}`
	runSend(t, reg, configPath, payload)

	assert.Contains(t, fake.last.Message, "Claude is waiting for your input")
	assert.Contains(t, fake.last.Message, "💬 你: what is 1+1?")
	assert.Contains(t, fake.last.Message, "🤖 AI: it is 2")
	assert.Equal(t, "idle_prompt", fake.last.NotificationType)
}

func TestSendActionOtherTypesDoNotModify(t *testing.T) {
	reg, fake := newFakeRegistry(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	writeConfig(t, configPath)

	transcriptPath := filepath.Join(dir, "transcript.jsonl")
	content := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"should not appear"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"neither should this"}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(transcriptPath, []byte(content), 0o600))

	payload := `{"message":"Claude needs permission","notification_type":"permission_prompt","transcript_path":"` + transcriptPath + `","cwd":"/Users/me/myproject"}`
	runSend(t, reg, configPath, payload)

	assert.Equal(t, "Claude needs permission", fake.last.Message)
	assert.NotContains(t, fake.last.Message, "💬 你:")
	assert.NotContains(t, fake.last.Message, "🤖 AI:")
	assert.Equal(t, "permission_prompt", fake.last.NotificationType)
}

func TestSendActionIdlePromptWithoutTranscript(t *testing.T) {
	reg, fake := newFakeRegistry(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	writeConfig(t, configPath)

	payload := `{"message":"Claude is waiting for your input","notification_type":"idle_prompt","transcript_path":"` + filepath.Join(dir, "missing.jsonl") + `","cwd":"/Users/me/myproject"}`
	runSend(t, reg, configPath, payload)

	assert.Equal(t, "Claude is waiting for your input", fake.last.Message)
	assert.NotContains(t, fake.last.Message, "💬 你:")
	assert.NotContains(t, fake.last.Message, "🤖 AI:")
	assert.Equal(t, "idle_prompt", fake.last.NotificationType)
}
