package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
func extractLastPromptAndReply(transcriptPath string) (userPrompt, assistantReply string, err error) {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return "", "", fmt.Errorf("opening transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), transcriptScannerMaxSize)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var entry transcriptEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// Skip malformed lines silently — transcript.jsonl is
			// append-only and we only need the entries we can parse.
			continue
		}

		var msg transcriptMessage
		if len(entry.Message) == 0 {
			continue
		}
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}

		text := firstTextBlock(msg.Content)
		if text == "" {
			continue
		}

		switch msg.Role {
		case "user":
			userPrompt = text
		case "assistant":
			assistantReply = text
		}
	}

	if err := scanner.Err(); err != nil {
		return "", "", fmt.Errorf("reading transcript: %w", err)
	}

	userPrompt = truncateForNotification(userPrompt)
	assistantReply = truncateForNotification(assistantReply)
	return userPrompt, assistantReply, nil
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
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}

	// Fall back to array of blocks; return the first text block.
	var blocks []transcriptContentBlock
	if err := json.Unmarshal(content, &blocks); err == nil {
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
