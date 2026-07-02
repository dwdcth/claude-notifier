package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withTempCache points dedup state at an isolated temp dir for the test.
func withTempCache(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestShouldSendFirstTime(t *testing.T) {
	withTempCache(t)
	assert.True(t, ShouldSend("session-1", "hello"), "first send must pass")
}

func TestShouldSendDuplicate(t *testing.T) {
	withTempCache(t)
	Record("session-1", "hello")
	assert.False(t, ShouldSend("session-1", "hello"),
		"identical message inside the window must be deduped")
}

func TestShouldSendAfterWindow(t *testing.T) {
	withTempCache(t)

	// Seed an entry whose timestamp is just past the dedup window.
	h := hashMessage("hello")
	store := loadDedup()
	store.Sessions["session-1"] = dedupEntry{
		Hash: h,
		TS:   time.Now().Add(-dedupWindow - time.Second),
	}
	require.NoError(t, saveDedup(store))

	assert.True(t, ShouldSend("session-1", "hello"),
		"same message outside the window must pass")
}

func TestShouldSendDifferentMsg(t *testing.T) {
	withTempCache(t)
	Record("session-1", "hello")
	assert.True(t, ShouldSend("session-1", "different"),
		"different message under the same session must pass")
}

func TestShouldSendDifferentSession(t *testing.T) {
	withTempCache(t)
	Record("session-1", "hello")
	assert.True(t, ShouldSend("session-2", "hello"),
		"same message under a different session must pass")
}

func TestShouldSendEmptySession(t *testing.T) {
	withTempCache(t)
	Record("", "hello")                     // no-op, must not error
	assert.True(t, ShouldSend("", "hello")) // empty session always passes
}

func TestShouldSendCorruptedStoreFailsOpen(t *testing.T) {
	withTempCache(t)

	p, err := dedupPath()
	require.NoError(t, err)
	require.NoError(t, writeGarbage(p))

	// Corrupted store must fail open: first send returns true.
	assert.True(t, ShouldSend("session-1", "hello"))
}

// writeGarbage writes invalid JSON to the dedup path so we can verify
// fail-open behaviour. Lives here (not in dedup.go) because it is test-only.
func writeGarbage(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("{not valid json"), 0o600)
}
