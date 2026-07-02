package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

const (
	// dedupWindow is the time window within which an identical message
	// (same sessionID + same hash) is treated as a duplicate and skipped.
	dedupWindow = 2 * time.Minute
	// dedupRetain limits how long entries are kept before being eligible
	// for cleanup. Set to 10x the window so transient read failures don't
	// cause us to forget recent state too quickly.
	dedupRetain = dedupWindow * 10
)

// dedupEntry records the last sent message hash and timestamp for a session.
type dedupEntry struct {
	Hash string    `json:"hash"`
	TS   time.Time `json:"ts"`
}

// dedupStore is the on-disk JSON shape used to track per-session state.
type dedupStore struct {
	Sessions map[string]dedupEntry `json:"sessions"`
}

// dedupPath returns the dedup state file location:
// $XDG_CACHE_HOME/claude-notifier/dedup.json or
// ~/.cache/claude-notifier/dedup.json when XDG_CACHE_HOME is unset.
func dedupPath() (string, error) {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "claude-notifier", "dedup.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home dir: %w", err)
	}
	return filepath.Join(home, ".cache", "claude-notifier", "dedup.json"), nil
}

// hashMessage returns the sha256 hex digest of the final message body.
func hashMessage(msg string) string {
	h := sha256.Sum256([]byte(msg))
	return hex.EncodeToString(h[:])
}

// loadDedup reads the dedup state file. Missing or corrupted files yield
// an empty store rather than an error — dedup is best-effort.
func loadDedup() dedupStore {
	p, err := dedupPath()
	if err != nil {
		return dedupStore{Sessions: map[string]dedupEntry{}}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return dedupStore{Sessions: map[string]dedupEntry{}}
	}
	var s dedupStore
	if err := json.Unmarshal(data, &s); err != nil {
		return dedupStore{Sessions: map[string]dedupEntry{}}
	}
	if s.Sessions == nil {
		s.Sessions = map[string]dedupEntry{}
	}
	return s
}

// saveDedup persists the dedup state, creating parent dirs as needed.
func saveDedup(s dedupStore) error {
	p, err := dedupPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("creating cache dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// ShouldSend reports whether a message should be sent. Returns false only
// when the same sessionID + same message hash was already recorded within
// the dedup window. Empty sessionID bypasses dedup entirely.
func ShouldSend(sessionID, msg string) bool {
	if sessionID == "" {
		return true
	}
	h := hashMessage(msg)
	s := loadDedup()
	if entry, ok := s.Sessions[sessionID]; ok {
		if entry.Hash == h && time.Since(entry.TS) < dedupWindow {
			return false
		}
	}
	return true
}

// Record marks a message as sent for the given session. Best-effort:
// failures are logged at warn level and never propagated, so dedup issues
// can never break the "never fail the hook" contract.
func Record(sessionID, msg string) {
	if sessionID == "" {
		return
	}
	h := hashMessage(msg)
	s := loadDedup()
	s.Sessions[sessionID] = dedupEntry{Hash: h, TS: time.Now()}

	// Drop expired entries so the file can't grow without bound.
	cutoff := time.Now().Add(-dedupRetain)
	for sid, e := range s.Sessions {
		if e.TS.Before(cutoff) {
			delete(s.Sessions, sid)
		}
	}

	if err := saveDedup(s); err != nil {
		slog.Warn("saving dedup state", "error", err)
	}
}
