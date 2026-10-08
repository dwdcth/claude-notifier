package ntfyclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/felipeelias/claude-notifier/internal/ntfyclient"
)

func TestPublish(t *testing.T) {
	var received struct {
		title   string
		body    string
		actions string
		auth    string
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		received.title = req.Header.Get("Title")
		received.actions = req.Header.Get("Actions")
		received.auth = req.Header.Get("Authorization")
		buf := make([]byte, 1024)
		n, _ := req.Body.Read(buf)
		received.body = string(buf[:n])
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := ntfyclient.Publish(context.Background(), ntfyclient.PublishRequest{
		Server:  server.URL,
		Topic:   "test-topic",
		Title:   "Test Title",
		Message: "Hello World",
		Auth:    ntfyclient.AuthConfig{Token: "my-token"},
	})
	if err != nil {
		t.Fatalf("Publish() error: %v", err)
	}
	if received.title != "Test Title" {
		t.Errorf("title = %q, want %q", received.title, "Test Title")
	}
	if received.body != "Hello World" {
		t.Errorf("body = %q, want %q", received.body, "Hello World")
	}
	if received.auth != "Bearer my-token" {
		t.Errorf("auth = %q, want %q", received.auth, "Bearer my-token")
	}
}

func TestPublishWithBasicAuth(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		auth = req.Header.Get("Authorization")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := ntfyclient.Publish(context.Background(), ntfyclient.PublishRequest{
		Server:  server.URL,
		Topic:   "test",
		Message: "test",
		Auth:    ntfyclient.AuthConfig{Username: "user", Password: "pass"},
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.HasPrefix(auth, "Basic ") {
		t.Errorf("expected Basic auth, got %q", auth)
	}
}

func TestPublishRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		calls++
		if calls < 3 {
			writer.WriteHeader(http.StatusInternalServerError)

			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := ntfyclient.PublishWithRetry(context.Background(), ntfyclient.PublishRequest{
		Server:  server.URL,
		Topic:   "test",
		Message: "retry test",
	}, 3)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestPublishRetryExhausted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := ntfyclient.PublishWithRetry(context.Background(), ntfyclient.PublishRequest{
		Server:  server.URL,
		Topic:   "test",
		Message: "fail",
	}, 2)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "all 2 attempts failed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWaitForResponse(t *testing.T) {
	requestID := "test-req-123"
	// ntfy SSE /json returns message as a JSON string literal (escaped JSON inside string)
	body := fmt.Sprintf(`{"requestId":"%s","decision":"approve"}`, requestID)
	responseJSON := fmt.Sprintf(
		`{"id":"1","event":"message","topic":"test-topic-response","message":%s}`, strconv.Quote(body))

	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
		// The write result does not matter to this test.
		_, _ = rw.Write([]byte(responseJSON + "\n"))
	}))
	defer server.Close()

	resp, err := ntfyclient.WaitForResponse(context.Background(), server.URL,
		"test-topic", requestID, ntfyclient.AuthConfig{})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if resp.Decision != "approve" {
		t.Errorf("decision = %q, want %q", resp.Decision, "approve")
	}
	if resp.RequestID != requestID {
		t.Errorf("requestID = %q, want %q", resp.RequestID, requestID)
	}
}

func TestWaitForResponseSkipsUnmatched(t *testing.T) {
	targetID := "target-123"
	mkLine := func(id, event, inner string) string {
		return fmt.Sprintf(`{"id":%q,"event":%q,"message":%s}`, id, event, strconv.Quote(inner))
	}
	lines := []string{
		mkLine("1", "message", `{"requestId":"other-456","decision":"deny"}`),
		`{"id":"2","event":"open","message":null}`,
		mkLine("3", "message", fmt.Sprintf(`{"requestId":"%s","decision":"approve"}`, targetID)),
	}

	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
		for _, line := range lines {
			// The write result does not matter to this test.
			_, _ = rw.Write([]byte(line + "\n"))
		}
	}))
	defer server.Close()

	resp, err := ntfyclient.WaitForResponse(context.Background(), server.URL,
		"test-topic", targetID, ntfyclient.AuthConfig{})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if resp.Decision != "approve" {
		t.Errorf("decision = %q", resp.Decision)
	}
}

func TestWaitForResponseContextCancel(t *testing.T) {
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
		// The write result does not matter to this test.
		_, _ = rw.Write([]byte("\n")) // send one empty line so scanner doesn't block on first read
		<-done                        // block until test is done
	}))
	defer server.Close()
	defer close(done)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := ntfyclient.WaitForResponse(ctx, server.URL, "test-topic", "req", ntfyclient.AuthConfig{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildApprovalURL(t *testing.T) {
	url := ntfyclient.BuildApprovalURL("https://ntfy.sh", "my-topic", "req-123", "approve")
	want := "https://ntfy.sh/my-topic-response"
	if url != want {
		t.Errorf("got %q, want %q", url, want)
	}
}

// assertApprovalAction checks that an approval action carries the expected
// label and decision payload.
func assertApprovalAction(t *testing.T, action ntfyclient.Action, wantLabel, wantDecision string) {
	t.Helper()

	if action.Label != wantLabel {
		t.Errorf("action Label = %q, want %q", action.Label, wantLabel)
	}
	if !strings.Contains(action.Body, `"decision":"`+wantDecision+`"`) {
		t.Errorf("action %q Body = %s, want decision %q", action.Label, action.Body, wantDecision)
	}
}

func TestBuildApprovalActions(t *testing.T) {
	actions := ntfyclient.BuildApprovalActions("https://ntfy.sh", "topic", "req1", false, nil)
	if len(actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(actions))
	}

	assertApprovalAction(t, actions[0], "Approve", "approve")
	assertApprovalAction(t, actions[1], "Deny", "deny")

	for _, action := range actions {
		if action.Method != http.MethodPost {
			t.Errorf("action %q Method = %q, want %s", action.Label, action.Method, http.MethodPost)
		}
		if action.URL != "https://ntfy.sh/topic-response" {
			t.Errorf("action %q URL = %q, want https://ntfy.sh/topic-response", action.Label, action.URL)
		}
		if !strings.Contains(action.Body, `"requestId":"req1"`) {
			t.Errorf("action %q Body missing requestId: %s", action.Label, action.Body)
		}
	}
}

func TestBuildApprovalActionsWithAlwaysApprove(t *testing.T) {
	actions := ntfyclient.BuildApprovalActions("https://ntfy.sh", "topic", "req1", true,
		[]map[string]any{{"type": "toolAlwaysAllow", "tool": "Bash"}})
	if len(actions) != 3 {
		t.Fatalf("expected 3 actions with always approve, got %d", len(actions))
	}

	assertApprovalAction(t, actions[2], "Always Approve", "always_approve")
}

func TestStripMarkdown(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "# Hello World",
			want:  "Hello World",
		},
		{
			input: "**bold text**",
			want:  "bold text",
		},
		{
			input: "*italic text*",
			want:  "italic text",
		},
		{
			input: "[link](https://example.com)",
			want:  "link",
		},
		{
			input: "`code`",
			want:  "code",
		},
		{
			input: "```\ncode block\n```",
			want:  "code block",
		},
		{
			input: "~~strikethrough~~",
			want:  "strikethrough",
		},
		{
			input: "> blockquote",
			want:  "blockquote",
		},
		{
			input: "- list item",
			want:  "list item",
		},
		{
			input: "---",
			want:  "",
		},
	}

	for _, tt := range tests {
		got := ntfyclient.StripMarkdown(tt.input)
		if strings.TrimSpace(got) != strings.TrimSpace(tt.want) {
			t.Errorf("StripMarkdown(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestActionsHeader(t *testing.T) {
	actions := []ntfyclient.Action{
		{Action: "http", Label: "Approve", URL: "https://example.com/approve", Clear: true},
	}
	header, err := ntfyclient.ActionsHeader(actions)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(header, `"label":"Approve"`) {
		t.Errorf("header missing Approve label: %s", header)
	}
}

func TestTopicURL(t *testing.T) {
	tests := []struct {
		server, topic, want string
	}{
		{"https://ntfy.sh", "my-topic", "https://ntfy.sh/my-topic"},
		{"https://ntfy.sh/", "my-topic", "https://ntfy.sh/my-topic"},
	}
	for _, tt := range tests {
		got := ntfyclient.TopicURL(tt.server, tt.topic)
		if got != tt.want {
			t.Errorf("TopicURL(%q, %q) = %q, want %q", tt.server, tt.topic, got, tt.want)
		}
	}
}

func TestPublishServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := ntfyclient.Publish(context.Background(), ntfyclient.PublishRequest{
		Server:  server.URL,
		Topic:   "test",
		Message: "fail",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWaitForResponseServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusUnauthorized)
		// The write result does not matter to this test.
		_, _ = rw.Write([]byte("unauthorized"))
	}))
	defer server.Close()

	_, err := ntfyclient.WaitForResponse(context.Background(), server.URL, "topic", "req", ntfyclient.AuthConfig{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPublishWithActions(t *testing.T) {
	var actionsHdr string
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		actionsHdr = req.Header.Get("Actions")
		rw.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := ntfyclient.Publish(context.Background(), ntfyclient.PublishRequest{
		Server:  server.URL,
		Topic:   "test",
		Title:   "Approval",
		Message: "Allow Bash?",
		Actions: []ntfyclient.Action{
			{Action: "http", Label: "Approve", URL: "https://e.com/a", Clear: true},
			{Action: "http", Label: "Deny", URL: "https://e.com/d", Clear: true},
		},
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	var parsed []map[string]any
	err = json.Unmarshal([]byte(actionsHdr), &parsed)
	if err != nil {
		t.Fatalf("parse actions header: %v (header=%s)", err, actionsHdr)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(parsed))
	}
	if parsed[0]["label"] != "Approve" {
		t.Errorf("first action label = %v", parsed[0]["label"])
	}
}

func TestPublishReturnsMessageID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
		// The write result does not matter to this test.
		_, _ = rw.Write([]byte(`{"id":"msg-abc123","time":1234567890,"event":"message"}`))
	}))
	defer server.Close()

	msgID, err := ntfyclient.Publish(context.Background(), ntfyclient.PublishRequest{
		Server:  server.URL,
		Topic:   "test",
		Message: "hello",
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if msgID != "msg-abc123" {
		t.Errorf("msgID = %q, want %q", msgID, "msg-abc123")
	}
}

func TestPublishReturnsEmptyIDOnUnparseableResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
		// No JSON body
	}))
	defer server.Close()

	msgID, err := ntfyclient.Publish(context.Background(), ntfyclient.PublishRequest{
		Server:  server.URL,
		Topic:   "test",
		Message: "hello",
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if msgID != "" {
		t.Errorf("expected empty msgID, got %q", msgID)
	}
}

func TestDeleteNotification(t *testing.T) {
	var method string
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		method = req.Method
		path = req.URL.Path
		rw.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := ntfyclient.DeleteNotification(context.Background(), server.URL, "test-topic", "msg-123",
		ntfyclient.AuthConfig{Token: "tok"})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", method)
	}
	if path != "/test-topic/msg-123" {
		t.Errorf("path = %q, want /test-topic/msg-123", path)
	}
}

func TestDeleteNotificationEmptyID(t *testing.T) {
	err := ntfyclient.DeleteNotification(context.Background(), "https://ntfy.sh", "topic", "", ntfyclient.AuthConfig{})
	if err != nil {
		t.Fatalf("expected nil error for empty ID, got: %v", err)
	}
}

func TestDeleteNotificationServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	err := ntfyclient.DeleteNotification(context.Background(), server.URL, "topic", "msg-123", ntfyclient.AuthConfig{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("unexpected error: %v", err)
	}
}
