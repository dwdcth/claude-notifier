package ntfyclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type Action struct {
	Action string `json:"action"`
	Label  string `json:"label"`
	URL    string `json:"url"`
	Method string `json:"method,omitempty"`
	Body   string `json:"body,omitempty"`
	Clear  bool   `json:"clear,omitempty"`
}

type PublishRequest struct {
	Server   string
	Topic    string
	Title    string
	Message  string
	Priority string
	Actions  []Action
	Auth     AuthConfig
}

type AuthConfig struct {
	Token    string
	Username string
	Password string
}

type Response struct {
	RequestID string `json:"requestId"`
	Decision  string `json:"decision"`
	Answer    string `json:"answer"`
}

type SSEMessage struct {
	ID      string          `json:"id"`
	Event   string          `json:"event"`
	Message json.RawMessage `json:"message"`
}

var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

var sseClient = &http.Client{}

func topicURL(server, topic string) string {
	return strings.TrimRight(server, "/") + "/" + topic
}

func setAuth(req *http.Request, auth AuthConfig) {
	if auth.Token != "" {
		req.Header.Set("Authorization", "Bearer "+auth.Token)
	} else if auth.Username != "" && auth.Password != "" {
		creds := base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Password))
		req.Header.Set("Authorization", "Basic "+creds)
	}
}

func actionsHeader(actions []Action) (string, error) {
	if len(actions) == 0 {
		return "", nil
	}
	b, err := json.Marshal(actions)
	if err != nil {
		return "", fmt.Errorf("marshaling actions: %w", err)
	}
	return string(b), nil
}

// ntfyPublishResponse represents the JSON response from ntfy publish endpoint.
type ntfyPublishResponse struct {
	ID string `json:"id"`
}

func Publish(ctx context.Context, req PublishRequest) (string, error) {
	actions, err := actionsHeader(req.Actions)
	if err != nil {
		return "", fmt.Errorf("building actions: %w", err)
	}

	body := req.Message
	url := topicURL(req.Server, req.Topic)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Title", req.Title)
	if req.Priority != "" {
		httpReq.Header.Set("Priority", req.Priority)
	}
	if actions != "" {
		httpReq.Header.Set("Actions", actions)
	}
	setAuth(httpReq, req.Auth)

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("sending request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("server returned %s", resp.Status)
	}

	var publishResp ntfyPublishResponse
	if err := json.NewDecoder(resp.Body).Decode(&publishResp); err != nil {
		// If we can't parse the response, return empty ID but no error.
		// The message was still published successfully.
		return "", nil
	}

	return publishResp.ID, nil
}

func PublishWithRetry(ctx context.Context, req PublishRequest, maxAttempts int) (string, error) {
	var lastErr error
	var lastMsgID string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		msgID, err := Publish(ctx, req)
		if err == nil {
			return msgID, nil
		}
		if msgID != "" {
			lastMsgID = msgID
		}
		lastErr = err
		slog.Warn("publish attempt failed", "attempt", attempt, "error", err)
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return lastMsgID, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
	}
	return lastMsgID, fmt.Errorf("all %d attempts failed: %w", maxAttempts, lastErr)
}

func WaitForResponse(ctx context.Context, server, topic, requestID string, auth AuthConfig) (*Response, error) {
	responseTopic := topic + "-response"
	url := topicURL(server, responseTopic) + "/json"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating SSE request: %w", err)
	}
	setAuth(req, auth)

	resp, err := sseClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connecting to SSE: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("SSE returned %s: %s", resp.Status, string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var msg SSEMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			slog.Debug("skipping non-JSON SSE line", "line", line)
			continue
		}

		if msg.Event != "message" {
			continue
		}

		var body string
		if err := json.Unmarshal(msg.Message, &body); err != nil {
			slog.Debug("skipping non-string message body", "error", err)
			continue
		}

		var response Response
		if err := json.Unmarshal([]byte(body), &response); err != nil {
			slog.Debug("skipping unparseable message", "error", err)
			continue
		}

		if response.RequestID == requestID {
			return &response, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading SSE stream: %w", err)
	}

	return nil, fmt.Errorf("SSE stream ended without matching response")
}

// DeleteNotification deletes a cached notification from the ntfy server.
// This is best-effort; errors are logged but not returned to callers.
func DeleteNotification(ctx context.Context, server, topic, messageID string, auth AuthConfig) error {
	if messageID == "" {
		return nil
	}

	url := topicURL(server, topic) + "/" + messageID
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("creating delete request: %w", err)
	}
	setAuth(req, auth)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending delete request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("delete returned %s", resp.Status)
	}

	return nil
}

func BuildApprovalURL(server, topic, requestID, decision string) string {
	responseTopic := topic + "-response"
	return strings.TrimRight(server, "/") + "/" + responseTopic
}

func BuildApprovalActions(server, topic, requestID string, withAlwaysApprove bool, permissionSuggestions []map[string]interface{}) []Action {
	makeAction := func(label, decision string) Action {
		body, _ := json.Marshal(Response{RequestID: requestID, Decision: decision})
		return Action{
			Action: "http",
			Label:  label,
			URL:    BuildApprovalURL(server, topic, "", ""),
			Method: "POST",
			Body:   string(body),
			Clear:  true,
		}
	}

	actions := []Action{
		makeAction("Approve", "approve"),
		makeAction("Deny", "deny"),
	}

	if withAlwaysApprove && len(permissionSuggestions) > 0 {
		actions = append(actions, makeAction("Always Approve", "always_approve"))
	}

	return actions
}

// TruncateRunes truncates s to at most max runes, appending "..." if
// truncation occurred. It operates on runes rather than bytes so that
// multi-byte UTF-8 sequences (e.g. Chinese characters) are never split
// in half, which would produce invalid UTF-8 and confuse downstream
// consumers (some ntfy clients mis-detect invalid UTF-8 as an attachment).
func TruncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max]) + "..."
	}
	return s
}

// truncateRunesNoEllipsis is like TruncateRunes but does not append the
// trailing "...". Used by callers that already impose a hard size limit
// and cannot afford the extra bytes (e.g. StripMarkdown's 4000-rune cap).
func truncateRunesNoEllipsis(s string, max int) string {
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max])
	}
	return s
}

func StripMarkdown(input string) string {
	var buf bytes.Buffer
	lines := strings.Split(input, "\n")
	inCodeBlock := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			buf.WriteString(line + "\n")
			continue
		}
		if strings.HasPrefix(trimmed, "---") && len(trimmed) >= 3 {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			line = strings.TrimLeft(trimmed, "# ")
		}
		if strings.HasPrefix(trimmed, ">") {
			line = strings.TrimPrefix(trimmed, "> ")
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			line = "  " + trimmed[2:]
		}
		line = strings.ReplaceAll(line, "`", "")
		line = stripLinks(line)
		line = stripBold(line)
		line = stripItalic(line)
		line = strings.ReplaceAll(line, "~~", "")
		line = strings.ReplaceAll(line, "\\", "")
		buf.WriteString(line + "\n")
	}

	result := buf.String()
	result = strings.Join(strings.Fields(result), " ")
	result = truncateRunesNoEllipsis(result, 4000)
	return result
}

func stripLinks(s string) string {
	for {
		start := strings.Index(s, "[")
		if start == -1 {
			break
		}
		end := strings.Index(s[start:], "](")
		if end == -1 {
			break
		}
		end += start
		closeParen := strings.Index(s[end:], ")")
		if closeParen == -1 {
			break
		}
		closeParen += end
		text := s[start+1 : end]
		s = s[:start] + text + s[closeParen+1:]
	}
	return s
}

func stripBold(s string) string {
	for {
		i := strings.Index(s, "**")
		if i == -1 {
			break
		}
		j := strings.Index(s[i+2:], "**")
		if j == -1 {
			break
		}
		j += i + 2
		text := s[i+2 : j]
		s = s[:i] + text + s[j+2:]
	}
	return s
}

func stripItalic(s string) string {
	for {
		i := strings.Index(s, "*")
		if i == -1 {
			break
		}
		if i > 0 && s[i-1] == '*' {
			s = s[:i] + s[i+1:]
			continue
		}
		j := strings.Index(s[i+1:], "*")
		if j == -1 {
			break
		}
		j += i + 1
		text := s[i+1 : j]
		s = s[:i] + text + s[j+1:]
	}
	return s
}
