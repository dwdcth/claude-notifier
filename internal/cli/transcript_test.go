package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
}

func TestExtractLastPromptAndReply_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	writeLines(t, path)

	user, reply, err := extractLastPromptAndReply(path)
	require.NoError(t, err)
	assert.Equal(t, "", user)
	assert.Equal(t, "", reply)
}

func TestExtractLastPromptAndReply_FileMissing(t *testing.T) {
	_, _, err := extractLastPromptAndReply("/does/not/exist.jsonl")
	assert.Error(t, err)
}

func TestExtractLastPromptAndReply_OnlyUser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	writeLines(t, path, `{"type":"user","message":{"role":"user","content":"hello there"}}`)

	user, reply, err := extractLastPromptAndReply(path)
	require.NoError(t, err)
	assert.Equal(t, "hello there", user)
	assert.Equal(t, "", reply)
}

func TestExtractLastPromptAndReply_StringContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	writeLines(t, path,
		`{"type":"user","message":{"role":"user","content":"first prompt"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"sure thing"}}`,
	)

	user, reply, err := extractLastPromptAndReply(path)
	require.NoError(t, err)
	assert.Equal(t, "first prompt", user)
	assert.Equal(t, "sure thing", reply)
}

func TestExtractLastPromptAndReply_ArrayContentPicksText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	writeLines(t, path,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"what is 2+2?"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","text":"internal"},{"type":"text","text":"it is 4"},{"type":"tool_use","text":"ignored"}]}}`,
	)

	user, reply, err := extractLastPromptAndReply(path)
	require.NoError(t, err)
	assert.Equal(t, "what is 2+2?", user)
	assert.Equal(t, "it is 4", reply)
}

func TestExtractLastPromptAndReply_SkipsToolResultOnlyUserEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	writeLines(t, path,
		`{"type":"user","message":{"role":"user","content":"real question"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"real answer"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","text":"some output"}]}}`,
	)

	user, reply, err := extractLastPromptAndReply(path)
	require.NoError(t, err)
	assert.Equal(t, "real question", user, "should keep last text-bearing user message")
	assert.Equal(t, "real answer", reply)
}

func TestExtractLastPromptAndReply_PicksLatestOfMultipleTurns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	writeLines(t, path,
		`{"type":"user","message":{"role":"user","content":"old question"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"old answer"}}`,
		`{"type":"user","message":{"role":"user","content":"new question"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"new answer"}}`,
	)

	user, reply, err := extractLastPromptAndReply(path)
	require.NoError(t, err)
	assert.Equal(t, "new question", user)
	assert.Equal(t, "new answer", reply)
}

func TestExtractLastPromptAndReply_TruncatesLongText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	longPrompt := strings.Repeat("a", 200)
	longReply := strings.Repeat("b", 200)
	writeLines(t, path,
		`{"type":"user","message":{"role":"user","content":"`+longPrompt+`"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"`+longReply+`"}}`,
	)

	user, reply, err := extractLastPromptAndReply(path)
	require.NoError(t, err)
	require.Len(t, user, 53) // 50 + "..."
	require.True(t, strings.HasPrefix(user, strings.Repeat("a", 50)))
	require.True(t, strings.HasSuffix(user, "..."))
	require.Len(t, reply, 53)
	require.True(t, strings.HasPrefix(reply, strings.Repeat("b", 50)))
}

func TestExtractLastPromptAndReply_StripsMarkdown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	writeLines(t, path,
		`{"type":"user","message":{"role":"user","content":"**bold** [link](https://x.com) end"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"# heading\n\nreply with `+"`code`"+`"}}`,
	)

	user, reply, err := extractLastPromptAndReply(path)
	require.NoError(t, err)
	assert.NotContains(t, user, "**")
	assert.NotContains(t, user, "[link]")
	assert.Contains(t, user, "link")
	assert.NotContains(t, reply, "#")
	assert.NotContains(t, reply, "`")
}

func TestExtractLastPromptAndReply_SkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	writeLines(t, path,
		`not json`,
		`{"type":"user","message":{"role":"user","content":"good prompt"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"good reply"}}`,
	)

	user, reply, err := extractLastPromptAndReply(path)
	require.NoError(t, err)
	assert.Equal(t, "good prompt", user)
	assert.Equal(t, "good reply", reply)
}

func TestBuildStopMessage_FallbackWhenNoPromptOrReply(t *testing.T) {
	assert.Equal(t, "Conversation ended in myproject", buildStopMessage("myproject", "", ""))
}

func TestBuildStopMessage_OnlyUser(t *testing.T) {
	msg := buildStopMessage("p", "fix the bug", "")
	assert.Contains(t, msg, "💬 你: fix the bug")
	assert.NotContains(t, msg, "🤖")
}

func TestBuildStopMessage_OnlyReply(t *testing.T) {
	msg := buildStopMessage("p", "", "done")
	assert.Contains(t, msg, "🤖 AI: done")
	assert.NotContains(t, msg, "💬")
}

func TestBuildStopMessage_Both(t *testing.T) {
	msg := buildStopMessage("p", "question", "answer")
	assert.Contains(t, msg, "💬 你: question")
	assert.Contains(t, msg, "🤖 AI: answer")
	assert.Contains(t, msg, "\n\n")
}
