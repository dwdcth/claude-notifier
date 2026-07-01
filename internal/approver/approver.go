package approver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/felipeelias/claude-notifier/internal/ntfyclient"
	"github.com/google/uuid"
)

type PermissionRequest struct {
	HookEventName         string                   `json:"hook_event_name"`
	ToolName              string                   `json:"tool_name"`
	ToolInput             json.RawMessage          `json:"tool_input"`
	PermissionSuggestions []map[string]interface{} `json:"permission_suggestions"`
}

type HookOutput struct {
	HookSpecificOutput HookSpecificOutput `json:"hookSpecificOutput"`
}

type HookSpecificOutput struct {
	HookEventName      string                   `json:"hookEventName"`
	Decision           *Decision                `json:"decision,omitempty"`
	UpdatedPermissions []map[string]interface{} `json:"updatedPermissions,omitempty"`
}

type Decision struct {
	Behavior     string                 `json:"behavior"`
	UpdatedInput map[string]interface{} `json:"updatedInput,omitempty"`
}

func AskOutput() json.RawMessage {
	out := HookOutput{
		HookSpecificOutput: HookSpecificOutput{
			HookEventName: "PermissionRequest",
		},
	}
	b, _ := json.Marshal(out)
	return b
}

func ApproveOutput() json.RawMessage {
	out := HookOutput{
		HookSpecificOutput: HookSpecificOutput{
			HookEventName: "PermissionRequest",
			Decision:      &Decision{Behavior: "allow"},
		},
	}
	b, _ := json.Marshal(out)
	return b
}

func DenyOutput() json.RawMessage {
	out := HookOutput{
		HookSpecificOutput: HookSpecificOutput{
			HookEventName: "PermissionRequest",
			Decision:      &Decision{Behavior: "deny"},
		},
	}
	b, _ := json.Marshal(out)
	return b
}

func AlwaysApproveOutput(suggestions []map[string]interface{}) json.RawMessage {
	out := HookOutput{
		HookSpecificOutput: HookSpecificOutput{
			HookEventName:      "PermissionRequest",
			Decision:           &Decision{Behavior: "allow"},
			UpdatedPermissions: suggestions,
		},
	}
	b, _ := json.Marshal(out)
	return b
}

type ApproverConfig struct {
	Server      string
	Topic       string
	Timeout     time.Duration
	Auth        ntfyclient.AuthConfig
	TitlePrefix string
	GenerateID  func() string
}

func (c *ApproverConfig) newID() string {
	if c.GenerateID != nil {
		return c.GenerateID()
	}
	return uuid.New().String()
}

func formatToolInfo(req PermissionRequest) string {
	var sb strings.Builder

	if req.ToolName == "AskUserQuestion" {
		sb.WriteString("Claude is asking a question:\n\n")
		var input struct {
			Questions []struct {
				Question string `json:"question"`
				Options  []struct {
					Label       string `json:"label"`
					Description string `json:"description"`
				} `json:"options"`
			} `json:"questions"`
		}
		if err := json.Unmarshal(req.ToolInput, &input); err == nil {
			for _, q := range input.Questions {
				sb.WriteString(q.Question + "\n")
				for _, opt := range q.Options {
					sb.WriteString(fmt.Sprintf("  - %s: %s\n", opt.Label, opt.Description))
				}
			}
		} else {
			sb.WriteString(string(req.ToolInput))
		}
		return sb.String()
	}

	sb.WriteString(fmt.Sprintf("Tool: %s\n", req.ToolName))

	var input map[string]interface{}
	if err := json.Unmarshal(req.ToolInput, &input); err == nil {
		for key, val := range input {
			sb.WriteString(fmt.Sprintf("%s: %v\n", key, val))
		}
	} else {
		sb.WriteString(string(req.ToolInput))
	}

	return sb.String()
}

func buildNotificationTitle(req PermissionRequest, prefix string) string {
	if prefix == "" {
		prefix = "Claude Code"
	}
	if req.ToolName == "AskUserQuestion" {
		return prefix + " - Question"
	}
	return fmt.Sprintf("%s - %s Permission", prefix, req.ToolName)
}

func ProcessHook(ctx context.Context, req PermissionRequest, cfg ApproverConfig) json.RawMessage {
	if cfg.Topic == "" {
		slog.Debug("no approver topic configured, falling back to ask")
		return AskOutput()
	}

	if req.ToolName == "AskUserQuestion" {
		return processAskUserQuestion(ctx, req, cfg)
	}

	requestID := cfg.newID()
	info := formatToolInfo(req)
	title := buildNotificationTitle(req, cfg.TitlePrefix)
	info = ntfyclient.StripMarkdown(info)

	if len(info) > 4000 {
		info = info[:4000]
	}

	withAlways := len(req.PermissionSuggestions) > 0
	actions := ntfyclient.BuildApprovalActions(cfg.Server, cfg.Topic, requestID, withAlways, req.PermissionSuggestions)

	pubReq := ntfyclient.PublishRequest{
		Server:   cfg.Server,
		Topic:    cfg.Topic,
		Title:    title,
		Message:  info,
		Priority: "high",
		Actions:  actions,
		Auth:     cfg.Auth,
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	if req.ToolName == "ExitPlanMode" {
		timeout = 300 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	msgID, err := ntfyclient.PublishWithRetry(ctx, pubReq, 3)
	if err != nil {
		slog.Error("failed to publish approval request", "error", err)
		return AskOutput()
	}

	resp, err := ntfyclient.WaitForResponse(ctx, cfg.Server, cfg.Topic, requestID, cfg.Auth)
	if err != nil {
		slog.Error("waiting for response", "error", err)
		// Best-effort cleanup on timeout/error
		if delErr := ntfyclient.DeleteNotification(context.Background(), cfg.Server, cfg.Topic, msgID, cfg.Auth); delErr != nil {
			slog.Debug("failed to delete notification", "error", delErr)
		}
		return AskOutput()
	}

	// Best-effort cleanup after receiving a response
	if delErr := ntfyclient.DeleteNotification(context.Background(), cfg.Server, cfg.Topic, msgID, cfg.Auth); delErr != nil {
		slog.Debug("failed to delete notification", "error", delErr)
	}

	switch resp.Decision {
	case "approve":
		return ApproveOutput()
	case "deny":
		return DenyOutput()
	case "always_approve":
		return AlwaysApproveOutput(req.PermissionSuggestions)
	default:
		slog.Warn("unknown decision", "decision", resp.Decision)
		return AskOutput()
	}
}

type questionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type questionInput struct {
	Question    string           `json:"question"`
	Header      string           `json:"header"`
	MultiSelect bool             `json:"multiSelect"`
	Options     []questionOption `json:"options"`
}

func processAskUserQuestion(ctx context.Context, req PermissionRequest, cfg ApproverConfig) json.RawMessage {
	var input struct {
		Questions []questionInput `json:"questions"`
	}
	if err := json.Unmarshal(req.ToolInput, &input); err != nil {
		slog.Error("parsing AskUserQuestion input", "error", err)
		return AskOutput()
	}
	if len(input.Questions) == 0 {
		slog.Warn("AskUserQuestion with no questions")
		return AskOutput()
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}

	responseTopicURL := strings.TrimRight(cfg.Server, "/") + "/" + cfg.Topic + "-response"
	answers := map[string]string{}

	const maxButtons = 3

	for _, q := range input.Questions {
		requestID := cfg.newID()

		// Split options into batches of maxButtons each.
		batches := [][]questionOption{}
		for j := 0; j < len(q.Options); j += maxButtons {
			end := j + maxButtons
			if end > len(q.Options) {
				end = len(q.Options)
			}
			batches = append(batches, q.Options[j:end])
		}

		for i, batch := range batches {
			actions := make([]ntfyclient.Action, 0, len(batch))
			for _, opt := range batch {
				body, _ := json.Marshal(map[string]string{
					"requestId": requestID,
					"answer":    opt.Label,
				})
				actions = append(actions, ntfyclient.Action{
					Action: "http",
					Label:  opt.Label,
					URL:    responseTopicURL,
					Method: "POST",
					Body:   string(body),
					Clear:  true,
				})
			}

			var msg string
			if len(batches) > 1 {
				msg = fmt.Sprintf("%s (%d/%d)", q.Question, i+1, len(batches))
			} else {
				msg = q.Question
			}
			if q.MultiSelect {
				msg += "\n(multiple selections allowed)"
			}
			msg += "\n\n"
			for _, opt := range batch {
				msg += fmt.Sprintf("• %s: %s\n", opt.Label, opt.Description)
			}
			msg = strings.TrimRight(msg, "\n")

			title := "Claude Code: " + q.Header
			if q.Header == "" {
				title = "Claude Code: Question"
			}

			pubReq := ntfyclient.PublishRequest{
				Server:   cfg.Server,
				Topic:    cfg.Topic,
				Title:    title,
				Message:  ntfyclient.StripMarkdown(msg),
				Priority: "high",
				Actions:  actions,
				Auth:     cfg.Auth,
			}

			ctx2, cancel := context.WithTimeout(ctx, timeout)
			_, err := ntfyclient.PublishWithRetry(ctx2, pubReq, 3)
			cancel()
			if err != nil {
				slog.Error("publishing question batch", "error", err)
				return AskOutput()
			}
		}

		// Wait for the response for this question.
		ctx3, cancel := context.WithTimeout(ctx, timeout)
		resp, err := ntfyclient.WaitForResponse(ctx3, cfg.Server, cfg.Topic, requestID, cfg.Auth)
		cancel()
		if err != nil {
			slog.Error("waiting for answer", "error", err)
			return AskOutput()
		}
		if resp.Answer == "" {
			slog.Warn("no answer received, falling back to CLI")
			return AskOutput()
		}
		answers[q.Question] = resp.Answer
	}

	return askUserQuestionAllowOutput(req, answers)
}

func askUserQuestionAllowOutput(req PermissionRequest, answers map[string]string) json.RawMessage {
	// Re-parse tool_input.questions so we echo it back unchanged inside updatedInput.
	var parsed struct {
		Questions []json.RawMessage `json:"questions"`
	}
	_ = json.Unmarshal(req.ToolInput, &parsed)

	questionsField := map[string]interface{}{"questions": nil}
	if len(parsed.Questions) > 0 {
		// Convert []json.RawMessage into []interface{} for clean marshaling.
		qs := make([]interface{}, len(parsed.Questions))
		for i, q := range parsed.Questions {
			qs[i] = json.RawMessage(q)
		}
		questionsField = map[string]interface{}{"questions": qs}
	}

	out := HookOutput{
		HookSpecificOutput: HookSpecificOutput{
			HookEventName: "PermissionRequest",
			Decision: &Decision{
				Behavior: "allow",
				UpdatedInput: map[string]interface{}{
					"questions": questionsField["questions"],
					"answers":   answers,
				},
			},
		},
	}
	b, _ := json.Marshal(out)
	return b
}
