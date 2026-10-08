package ntfyclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	// httpTimeout bounds ordinary publish and delete HTTP requests.
	httpTimeout = 30 * time.Second

	// sseErrorBodyReadLimit caps how much of the error response body is
	// read when the SSE endpoint replies with an HTTP error status.
	sseErrorBodyReadLimit = 512

	// sseInitialBufferSize is the initial scan buffer for SSE lines.
	sseInitialBufferSize = 64 * 1024

	// sseMaxBufferSize caps a single SSE line; notification payloads can
	// exceed bufio's 64 KiB default limit.
	sseMaxBufferSize = 1024 * 1024

	// stripMarkdownMaxRunes caps the plain-text output of StripMarkdown.
	stripMarkdownMaxRunes = 4000
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
	// RequestID keeps a camelCase JSON key: it is the wire format of the
	// approval action bodies, which internal/approver also builds by hand
	// with the same key, so it cannot be renamed here unilaterally.
	RequestID string `json:"requestId"` //nolint:tagliatelle // camelCase wire format shared with internal/approver.
	Decision  string `json:"decision"`
	Answer    string `json:"answer"`
}

type SSEMessage struct {
	ID      string          `json:"id"`
	Event   string          `json:"event"`
	Message json.RawMessage `json:"message"`
}

var httpClient = &http.Client{
	Timeout: httpTimeout,
}

var sseClient = &http.Client{}

// TopicURL joins a ntfy server base URL and a topic into the topic endpoint URL.
func TopicURL(server, topic string) string {
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

// ActionsHeader serializes approval actions into the value of the ntfy
// "Actions" HTTP header.
func ActionsHeader(actions []Action) (string, error) {
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
	actions, err := ActionsHeader(req.Actions)
	if err != nil {
		return "", fmt.Errorf("building actions: %w", err)
	}

	body := req.Message
	url := TopicURL(req.Server, req.Topic)

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

	if resp.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("server returned %s", resp.Status)
	}

	var publishResp ntfyPublishResponse
	err = json.NewDecoder(resp.Body).Decode(&publishResp)
	if err != nil {
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
	url := TopicURL(server, responseTopic) + "/json"

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

	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, sseErrorBodyReadLimit))

		return nil, fmt.Errorf("SSE returned %s: %s", resp.Status, string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, sseInitialBufferSize), sseMaxBufferSize)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		response := decodeSSEResponse(line)
		if response == nil {
			continue
		}

		if response.RequestID == requestID {
			return response, nil
		}
	}

	err = scanner.Err()
	if err != nil {
		return nil, fmt.Errorf("reading SSE stream: %w", err)
	}

	return nil, errors.New("SSE stream ended without matching response")
}

// decodeSSEResponse decodes a single ntfy /json SSE line into a Response.
// It returns nil for lines that are not "message" events carrying a valid
// Response payload; such lines are skipped so the stream keeps draining.
func decodeSSEResponse(line string) *Response {
	var msg SSEMessage
	err := json.Unmarshal([]byte(line), &msg)
	if err != nil {
		// The raw line is untrusted network input; do not log its contents
		// to avoid log injection.
		slog.Debug("skipping non-JSON SSE line")

		return nil
	}

	if msg.Event != "message" {
		return nil
	}

	var body string
	err = json.Unmarshal(msg.Message, &body)
	if err != nil {
		slog.Debug("skipping non-string message body", "error", err)

		return nil
	}

	var response Response
	err = json.Unmarshal([]byte(body), &response)
	if err != nil {
		slog.Debug("skipping unparseable message", "error", err)

		return nil
	}

	return &response
}

// DeleteNotification deletes a cached notification from the ntfy server.
// This is best-effort; errors are logged but not returned to callers.
func DeleteNotification(ctx context.Context, server, topic, messageID string, auth AuthConfig) error {
	if messageID == "" {
		return nil
	}

	url := TopicURL(server, topic) + "/" + messageID
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

	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("delete returned %s", resp.Status)
	}

	return nil
}

func BuildApprovalURL(server, topic, requestID, decision string) string {
	responseTopic := topic + "-response"

	return strings.TrimRight(server, "/") + "/" + responseTopic
}

func BuildApprovalActions(
	server, topic, requestID string,
	withAlwaysApprove bool,
	permissionSuggestions []map[string]any,
) []Action {
	makeAction := func(label, decision string) Action {
		body, err := json.Marshal(Response{RequestID: requestID, Decision: decision})
		if err != nil {
			// Unreachable in practice: Response holds only string fields.
			// The error is checked so encoding failures stay visible.
			slog.Debug("marshaling approval response body", "error", err)
		}

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

// TruncateRunes truncates text to at most maxRunes runes, appending "..." if
// truncation occurred. It operates on runes rather than bytes so that
// multi-byte UTF-8 sequences (e.g. Chinese characters) are never split
// in half, which would produce invalid UTF-8 and confuse downstream
// consumers (some ntfy clients mis-detect invalid UTF-8 as an attachment).
func TruncateRunes(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}

	return text
}

// TruncateRunesNoEllipsis is like TruncateRunes but does not append the
// trailing "...". Used by callers that already impose a hard size limit
// and cannot afford the extra bytes (e.g. StripMarkdown's 4000-rune cap).
func TruncateRunesNoEllipsis(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}

	return text
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
	result = TruncateRunesNoEllipsis(result, stripMarkdownMaxRunes)

	return result
}

func stripLinks(text string) string {
	for {
		start := strings.Index(text, "[")
		if start == -1 {
			break
		}

		end := strings.Index(text[start:], "](")
		if end == -1 {
			break
		}

		end += start
		closeParen := strings.Index(text[end:], ")")
		if closeParen == -1 {
			break
		}

		closeParen += end
		linkText := text[start+1 : end]
		text = text[:start] + linkText + text[closeParen+1:]
	}

	return text
}

func stripBold(text string) string {
	const marker = "**"

	for {
		start := strings.Index(text, marker)
		if start == -1 {
			break
		}

		end := strings.Index(text[start+len(marker):], marker)
		if end == -1 {
			break
		}

		end += start + len(marker)
		boldText := text[start+len(marker) : end]
		text = text[:start] + boldText + text[end+len(marker):]
	}

	return text
}

func stripItalic(text string) string {
	for {
		start := strings.Index(text, "*")
		if start == -1 {
			break
		}

		if start > 0 && text[start-1] == '*' {
			text = text[:start] + text[start+1:]

			continue
		}

		end := strings.Index(text[start+1:], "*")
		if end == -1 {
			break
		}

		end += start + 1
		italicText := text[start+1 : end]
		text = text[:start] + italicText + text[end+1:]
	}

	return text
}
