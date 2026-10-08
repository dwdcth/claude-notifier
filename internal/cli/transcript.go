package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/felipeelias/claude-notifier/internal/ntfyclient"
)

const (
	// transcriptMaxTruncate is the maximum length (in chars) of the
	// user prompt or assistant reply shown in stop notifications.
	transcriptMaxTruncate = 50
	// transcriptScannerMaxSize is the maximum size of a single jsonl
	// line we are willing to parse. transcript.jsonl lines are usually
	// small, but tool_result payloads can be large.
	transcriptScannerMaxSize = 4 * 1024 * 1024
	// transcriptScannerInitialBuf is the initial buffer size of the
	// jsonl scanner; it grows on demand up to transcriptScannerMaxSize.
	transcriptScannerInitialBuf = 64 * 1024
	// transcriptRaceRetries is how many times we re-read the transcript
	// when the last entry is a user message (meaning the assistant
	// reply for the current turn has not been flushed yet). The Stop
	// hook often fires before the final assistant entry hits disk.
	transcriptRaceRetries = 5
	// transcriptRaceInterval is the delay between race-condition
	// retries. 5 retries * 200ms = up to 1s of total wait.
	transcriptRaceInterval = 200 * time.Millisecond
)

// transcriptEntry is a single line of transcript.jsonl. We only care
// about a few fields; Content is left as RawMessage because it can be
// either a string or an array of blocks.
type transcriptEntry struct {
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message"`
}

type transcriptMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type transcriptContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// extractLastPromptAndReply reads transcriptPath (jsonl) and returns
// the last user prompt and the last assistant reply.
//
// A "user prompt" is the text block of the last entry whose
// message.role == "user" and whose content contains a text block
// (tool_result-only entries are skipped).
// An "assistant reply" is the text block of the last entry whose
// message.role == "assistant" and whose content contains a text block
// (thinking/tool_use-only entries are skipped).
//
// Either value is "" if no matching entry is found.
//
// Race-condition guard: the Stop hook can fire before the assistant's
// final reply is flushed to disk, leaving the last entry on disk as a
// user message. When that happens we retry a few times so the
// notification shows the actual current reply instead of the previous
// turn's.
func extractLastPromptAndReply(transcriptPath string) (string, string, error) {
	userPrompt, assistantReply, lastRole, err := readOnce(transcriptPath)
	if err != nil {
		return "", "", err
	}

	// If the last entry on disk is a user message, the assistant reply
	// for this turn may still be in flight — wait and retry.
	for i := 0; i < transcriptRaceRetries && lastRole == "user"; i++ {
		time.Sleep(transcriptRaceInterval)

		retriedPrompt, retriedReply, retriedRole, retryErr := readOnce(transcriptPath)
		if retryErr != nil {
			return "", "", retryErr
		}

		userPrompt, assistantReply, lastRole = retriedPrompt, retriedReply, retriedRole
	}

	userPrompt = truncateForNotification(userPrompt)
	assistantReply = truncateForNotification(assistantReply)

	return userPrompt, assistantReply, nil
}

// readOnce does a single pass over the transcript file. It returns the
// last text-bearing user prompt, the last text-bearing assistant reply
// and the role of the final parsed entry in the file ("" if empty).
func readOnce(transcriptPath string) (string, string, string, error) {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return "", "", "", fmt.Errorf("opening transcript: %w", err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, transcriptScannerInitialBuf), transcriptScannerMaxSize)

	var userPrompt, assistantReply, lastRole string
	for scanner.Scan() {
		role, text, ok := parseTranscriptLine(scanner.Text())
		if !ok {
			continue
		}

		// Track the role of the most recent parseable entry regardless
		// of whether it carries text — the caller uses this to detect
		// a still-pending assistant reply.
		lastRole = role

		if text == "" {
			continue
		}

		switch role {
		case "user":
			userPrompt = text
		case "assistant":
			assistantReply = text
		}
	}

	err = scanner.Err()
	if err != nil {
		return "", "", "", fmt.Errorf("reading transcript: %w", err)
	}

	return userPrompt, assistantReply, lastRole, nil
}

// parseTranscriptLine decodes a single transcript.jsonl line into the
// entry's message role and its first text block. ok is false for blank
// or malformed lines and for entries without a parseable message; text
// is empty when the message carries no text block (e.g. tool_result
// only).
func parseTranscriptLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", false
	}

	var entry transcriptEntry
	err := json.Unmarshal([]byte(line), &entry)
	if err != nil {
		// Skip malformed lines silently — transcript.jsonl is
		// append-only and we only need the entries we can parse.
		return "", "", false
	}

	if len(entry.Message) == 0 {
		return "", "", false
	}

	var msg transcriptMessage
	err = json.Unmarshal(entry.Message, &msg)
	if err != nil {
		return "", "", false
	}

	return msg.Role, firstTextBlock(msg.Content), true
}

// firstTextBlock extracts the first text content from a transcript
// message content field, which can be either a plain string or an
// array of content blocks.
func firstTextBlock(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}

	// Try string first.
	var s string
	err := json.Unmarshal(content, &s)
	if err == nil {
		return s
	}

	// Fall back to array of blocks; return the first text block.
	var blocks []transcriptContentBlock
	err = json.Unmarshal(content, &blocks)
	if err == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				return b.Text
			}
		}
	}

	return ""
}

// truncateForNotification strips markdown, collapses whitespace and
// truncates to transcriptMaxTruncate runes. It operates on runes rather
// than bytes so multi-byte UTF-8 sequences (e.g. Chinese characters)
// are never split, which would produce invalid UTF-8 output.
func truncateForNotification(s string) string {
	s = ntfyclient.StripMarkdown(s)

	return ntfyclient.TruncateRunes(s, transcriptMaxTruncate)
}
