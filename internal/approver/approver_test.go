package approver_test

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

	"github.com/felipeelias/claude-notifier/internal/approver"
)

const testRequestID = "test-req-fixed-12345"

func fixedID() string { return testRequestID }

// writeBody writes a handler response body, ignoring the write error: an
// httptest handler has no meaningful way to report a failed write.
func writeBody(responseWriter http.ResponseWriter, body string) {
	_, _ = responseWriter.Write([]byte(body))
}

func TestAskOutput(t *testing.T) {
	out := approver.AskOutput()
	if !strings.Contains(string(out), `"hookEventName":"PermissionRequest"`) {
		t.Errorf("expected PermissionRequest in output: %s", out)
	}
	if strings.Contains(string(out), `"decision"`) {
		t.Errorf("ask output should not contain decision: %s", out)
	}
}

func TestApproveOutput(t *testing.T) {
	out := approver.ApproveOutput()
	if !strings.Contains(string(out), `"behavior":"allow"`) {
		t.Errorf("expected allow in output: %s", out)
	}
}

func TestDenyOutput(t *testing.T) {
	out := approver.DenyOutput()
	if !strings.Contains(string(out), `"behavior":"deny"`) {
		t.Errorf("expected deny in output: %s", out)
	}
}

func TestAlwaysApproveOutput(t *testing.T) {
	suggestions := []map[string]any{
		{"type": "toolAlwaysAllow", "tool": "Bash"},
	}
	out := approver.AlwaysApproveOutput(suggestions)
	output := string(out)
	if !strings.Contains(output, `"behavior":"allow"`) {
		t.Errorf("expected allow: %s", output)
	}
	if !strings.Contains(output, `"toolAlwaysAllow"`) {
		t.Errorf("expected updatedPermissions: %s", output)
	}
}

func TestProcessHookNoTopic(t *testing.T) {
	req := approver.PermissionRequest{
		HookEventName: "PreToolUse",
		ToolName:      "Bash",
		ToolInput:     json.RawMessage(`{"command":"ls"}`),
	}
	out := approver.ProcessHook(context.Background(), req, approver.ApproverConfig{})
	if !strings.Contains(string(out), `"hookEventName":"PermissionRequest"`) {
		t.Errorf("expected ask fallback: %s", out)
	}
}

func newTestServer(decision string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			responseWriter.WriteHeader(http.StatusOK)

			return
		}
		if request.Method == http.MethodPost {
			responseWriter.WriteHeader(http.StatusOK)
			writeBody(responseWriter, `{"id":"test-msg-id"}`)

			return
		}
		// ntfy SSE /json returns message as a JSON string literal
		inner := fmt.Sprintf(`{"requestId":"%s","decision":"%s"}`, testRequestID, decision)
		resp := fmt.Sprintf(
			`{"id":"1","event":"message","message":%s}`,
			strconv.Quote(inner))
		responseWriter.WriteHeader(http.StatusOK)
		writeBody(responseWriter, resp+"\n")
	}))
}

func TestProcessHookApprove(t *testing.T) {
	server := newTestServer("approve")
	defer server.Close()

	req := approver.PermissionRequest{
		HookEventName: "PreToolUse",
		ToolName:      "Bash",
		ToolInput:     json.RawMessage(`{"command":"ls -la"}`),
	}

	out := approver.ProcessHook(context.Background(), req, approver.ApproverConfig{
		Server:     server.URL,
		Topic:      "test-topic",
		Timeout:    5 * time.Second,
		GenerateID: fixedID,
	})

	if !strings.Contains(string(out), `"behavior":"allow"`) {
		t.Errorf("expected approve: %s", out)
	}
}

func TestProcessHookWithSuggestions(t *testing.T) {
	var gotActions string
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			gotActions = request.Header.Get("Actions")
			responseWriter.WriteHeader(http.StatusOK)
			writeBody(responseWriter, `{"id":"test-msg-id"}`)

			return
		}
		if request.Method == http.MethodDelete {
			responseWriter.WriteHeader(http.StatusOK)

			return
		}
		responseWriter.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	req := approver.PermissionRequest{
		HookEventName: "PreToolUse",
		ToolName:      "Bash",
		ToolInput:     json.RawMessage(`{"command":"npm test"}`),
		PermissionSuggestions: []map[string]any{
			{"type": "toolAlwaysAllow", "tool": "Bash"},
		},
	}

	approver.ProcessHook(context.Background(), req, approver.ApproverConfig{
		Server:     server.URL,
		Topic:      "test-topic",
		Timeout:    200 * time.Millisecond,
		GenerateID: fixedID,
	})

	if !strings.Contains(gotActions, "Always Approve") {
		t.Errorf("expected Always Approve in actions: %s", gotActions)
	}
}

func TestProcessHookAlwaysApprove(t *testing.T) {
	server := newTestServer("always_approve")
	defer server.Close()

	req := approver.PermissionRequest{
		ToolName:  "Bash",
		ToolInput: json.RawMessage(`{"command":"npm test"}`),
		PermissionSuggestions: []map[string]any{
			{"type": "toolAlwaysAllow", "tool": "Bash"},
		},
	}

	out := approver.ProcessHook(context.Background(), req, approver.ApproverConfig{
		Server:     server.URL,
		Topic:      "test-topic",
		Timeout:    5 * time.Second,
		GenerateID: fixedID,
	})

	if !strings.Contains(string(out), `"toolAlwaysAllow"`) {
		t.Errorf("expected updatedPermissions: %s", out)
	}
}

func TestProcessHookDeny(t *testing.T) {
	server := newTestServer("deny")
	defer server.Close()

	req := approver.PermissionRequest{
		ToolName:  "Write",
		ToolInput: json.RawMessage(`{"file_path":"/tmp/test.txt"}`),
	}

	out := approver.ProcessHook(context.Background(), req, approver.ApproverConfig{
		Server:     server.URL,
		Topic:      "test",
		Timeout:    5 * time.Second,
		GenerateID: fixedID,
	})

	if !strings.Contains(string(out), `"behavior":"deny"`) {
		t.Errorf("expected deny: %s", out)
	}
}

func TestFormatToolInfo(t *testing.T) {
	tests := []struct {
		name string
		req  approver.PermissionRequest
		want string
	}{
		{
			name: "bash command",
			req: approver.PermissionRequest{
				ToolName:  "Bash",
				ToolInput: json.RawMessage(`{"command":"npm test"}`),
			},
			want: "command: npm test",
		},
		{
			name: "ask user question",
			req: approver.PermissionRequest{
				ToolName: "AskUserQuestion",
				ToolInput: json.RawMessage(`{"questions":[{"question":"Which?",` +
					`"options":[{"label":"A","description":"Option A"}]}]}`),
			},
			want: "Which?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := approver.FormatToolInfo(tt.req)
			if !strings.Contains(got, tt.want) {
				t.Errorf("FormatToolInfo() = %q, want to contain %q", got, tt.want)
			}
		})
	}
}

func TestBuildNotificationTitle(t *testing.T) {
	tests := []struct {
		tool   string
		prefix string
		want   string
	}{
		{"Bash", "", "Claude Code - Bash Permission"},
		{"AskUserQuestion", "", "Claude Code - Question"},
		{"Write", "", "Claude Code - Write Permission"},
		{"Bash", "MyMachine", "MyMachine - Bash Permission"},
		{"AskUserQuestion", "Server1", "Server1 - Question"},
	}
	for _, tt := range tests {
		got := approver.BuildNotificationTitle(approver.PermissionRequest{ToolName: tt.tool}, tt.prefix)
		if got != tt.want {
			t.Errorf("BuildNotificationTitle(%q, %q) = %q, want %q", tt.tool, tt.prefix, got, tt.want)
		}
	}
}

func TestProcessHookTimeout(t *testing.T) {
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			responseWriter.WriteHeader(http.StatusOK)
			writeBody(responseWriter, `{"id":"test-msg-id"}`)

			return
		}
		if request.Method == http.MethodDelete {
			responseWriter.WriteHeader(http.StatusOK)

			return
		}
		responseWriter.WriteHeader(http.StatusOK)
		writeBody(responseWriter, "\n")
		<-done
	}))
	defer server.Close()
	defer close(done)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	req := approver.PermissionRequest{
		ToolName:  "Bash",
		ToolInput: json.RawMessage(`{"command":"ls"}`),
	}

	out := approver.ProcessHook(ctx, req, approver.ApproverConfig{
		Server:     server.URL,
		Topic:      "test",
		Timeout:    200 * time.Millisecond,
		GenerateID: fixedID,
	})

	if strings.Contains(string(out), `"decision"`) {
		t.Errorf("expected ask fallback on timeout: %s", out)
	}
}

func TestProcessHookPublishFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	req := approver.PermissionRequest{
		ToolName:  "Bash",
		ToolInput: json.RawMessage(`{"command":"ls"}`),
	}

	out := approver.ProcessHook(context.Background(), req, approver.ApproverConfig{
		Server:     server.URL,
		Topic:      "test",
		Timeout:    2 * time.Second,
		GenerateID: fixedID,
	})

	if strings.Contains(string(out), `"decision"`) {
		t.Errorf("expected ask fallback on publish failure: %s", out)
	}
}

// newAskServer returns a test server that records the Actions header from the
// publish POST and then responds on the SSE stream with the given answer.
func newAskServer(answer string, capturedActions *string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			responseWriter.WriteHeader(http.StatusOK)

			return
		}
		if request.Method == http.MethodPost {
			if capturedActions != nil {
				*capturedActions = request.Header.Get("Actions")
			}
			responseWriter.WriteHeader(http.StatusOK)
			writeBody(responseWriter, `{"id":"test-msg-id"}`)

			return
		}
		// SSE response carrying the answer
		inner := fmt.Sprintf(`{"requestId":"%s","answer":%s}`, testRequestID, strconv.Quote(answer))
		resp := fmt.Sprintf(
			`{"id":"1","event":"message","message":%s}`,
			strconv.Quote(inner))
		responseWriter.WriteHeader(http.StatusOK)
		writeBody(responseWriter, resp+"\n")
	}))
}

func TestProcessAskUserQuestionSingle(t *testing.T) {
	var actions string
	server := newAskServer("Option A", &actions)
	defer server.Close()

	toolInput := `{"questions":[{"question":"Which?","header":"Pick","options":[` +
		`{"label":"Option A","description":"first"},` +
		`{"label":"Option B","description":"second"}]}]}`

	req := approver.PermissionRequest{
		ToolName:  "AskUserQuestion",
		ToolInput: json.RawMessage(toolInput),
	}

	out := approver.ProcessHook(context.Background(), req, approver.ApproverConfig{
		Server:     server.URL,
		Topic:      "test-topic",
		Timeout:    5 * time.Second,
		GenerateID: fixedID,
	})

	output := string(out)
	// Buttons must use the "answer" field, not "decision". The actions header is
	// a JSON array of objects, so the inner quotes are escaped.
	wantAnswer := `\"answer\":\"Option A\"`
	if !strings.Contains(actions, wantAnswer) {
		t.Errorf("expected answer field in actions: %s", actions)
	}
	if strings.Contains(actions, `\"decision\":\"Option A\"`) {
		t.Errorf("actions should not use decision field for AskUserQuestion: %s", actions)
	}
	if !strings.Contains(output, `"behavior":"allow"`) {
		t.Errorf("expected allow decision: %s", output)
	}
	if !strings.Contains(output, `"updatedInput"`) {
		t.Errorf("expected updatedInput in output: %s", output)
	}
	if !strings.Contains(output, `"Which?":"Option A"`) {
		t.Errorf("expected answers map entry: %s", output)
	}
}

func TestProcessAskUserQuestionNoAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			responseWriter.WriteHeader(http.StatusOK)
			writeBody(responseWriter, `{"id":"test-msg-id"}`)

			return
		}
		// SSE returns a response with no answer
		inner := fmt.Sprintf(`{"requestId":"%s","decision":"approve"}`, testRequestID)
		resp := fmt.Sprintf(`{"id":"1","event":"message","message":%s}`, strconv.Quote(inner))
		responseWriter.WriteHeader(http.StatusOK)
		writeBody(responseWriter, resp+"\n")
	}))
	defer server.Close()

	toolInput := `{"questions":[{"question":"Which?","options":[{"label":"A","description":"x"}]}]}`
	req := approver.PermissionRequest{
		ToolName:  "AskUserQuestion",
		ToolInput: json.RawMessage(toolInput),
	}

	out := approver.ProcessHook(context.Background(), req, approver.ApproverConfig{
		Server:     server.URL,
		Topic:      "test-topic",
		Timeout:    5 * time.Second,
		GenerateID: fixedID,
	})

	if strings.Contains(string(out), `"updatedInput"`) {
		t.Errorf("expected ask fallback when no answer: %s", out)
	}
}

func TestProcessAskUserQuestionBatched(t *testing.T) {
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			postCount++
			responseWriter.WriteHeader(http.StatusOK)
			writeBody(responseWriter, `{"id":"test-msg-id"}`)

			return
		}
		if request.Method == http.MethodDelete {
			responseWriter.WriteHeader(http.StatusOK)

			return
		}
		inner := fmt.Sprintf(`{"requestId":"%s","answer":"D"}`, testRequestID)
		resp := fmt.Sprintf(`{"id":"1","event":"message","message":%s}`, strconv.Quote(inner))
		responseWriter.WriteHeader(http.StatusOK)
		writeBody(responseWriter, resp+"\n")
	}))
	defer server.Close()

	// 5 options -> 2 batches (3 + 2).
	toolInput := `{"questions":[{"question":"Pick one","options":[` +
		`{"label":"A","description":"a"},{"label":"B","description":"b"},{"label":"C","description":"c"},` +
		`{"label":"D","description":"d"},{"label":"E","description":"e"}]}]}`

	req := approver.PermissionRequest{
		ToolName:  "AskUserQuestion",
		ToolInput: json.RawMessage(toolInput),
	}

	out := approver.ProcessHook(context.Background(), req, approver.ApproverConfig{
		Server:     server.URL,
		Topic:      "test-topic",
		Timeout:    5 * time.Second,
		GenerateID: fixedID,
	})

	if postCount < 2 {
		t.Errorf("expected at least 2 batched publishes, got %d", postCount)
	}
	if !strings.Contains(string(out), `"Pick one":"D"`) {
		t.Errorf("expected answer D in output: %s", out)
	}
}
