package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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

// TestExtractLastPromptAndReply_TruncatesChineseAsRunes verifies that a
// long Chinese string is truncated on rune boundaries, never producing
// invalid UTF-8. The old byte-based truncation `s[:50]` sliced through
// the middle of 3-byte Chinese characters, causing mojibake in ntfy
// notifications and sometimes making the ntfy client mis-detect the
// message as an attachment.
func TestExtractLastPromptAndReply_TruncatesChineseAsRunes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	longPrompt := strings.Repeat("中", 200) // 600 bytes
	longReply := strings.Repeat("答", 200)  // 600 bytes
	writeLines(t, path,
		`{"type":"user","message":{"role":"user","content":"`+longPrompt+`"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"`+longReply+`"}}`,
	)

	user, reply, err := extractLastPromptAndReply(path)
	require.NoError(t, err)

	require.True(t, utf8.ValidString(user), "user prompt must be valid UTF-8 after truncation")
	require.True(t, utf8.ValidString(reply), "assistant reply must be valid UTF-8 after truncation")

	// 50 Chinese runes (50 * 3 = 150 bytes) + "..." (3 bytes) = 153 bytes,
	// and 53 runes total.
	require.Len(t, []rune(user), 53, "expected 50 runes + ellipsis")
	require.Len(t, []rune(reply), 53)

	require.True(t, strings.HasPrefix(user, strings.Repeat("中", 50)),
		"user prompt should start with 50 Chinese characters, got %q", user)
	require.True(t, strings.HasSuffix(user, "..."))
	require.True(t, strings.HasPrefix(reply, strings.Repeat("答", 50)))
	require.True(t, strings.HasSuffix(reply, "..."))

	// Sanity check: byte length must be 153, NOT 50 — 50 bytes would mean
	// we sliced through multi-byte characters (the old buggy behavior).
	require.Len(t, user, 153)
	require.Len(t, reply, 153)
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
