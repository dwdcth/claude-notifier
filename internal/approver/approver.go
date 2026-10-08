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
	HookEventName         string           `json:"hook_event_name"`
	ToolName              string           `json:"tool_name"`
	ToolInput             json.RawMessage  `json:"tool_input"`
	PermissionSuggestions []map[string]any `json:"permission_suggestions"`
}

type HookOutput struct {
	HookSpecificOutput HookSpecificOutput `json:"hookSpecificOutput"` //nolint:tagliatelle // hook wire format
}

type HookSpecificOutput struct {
	HookEventName      string           `json:"hookEventName"` //nolint:tagliatelle // hook wire format
	Decision           *Decision        `json:"decision,omitempty"`
	UpdatedPermissions []map[string]any `json:"updatedPermissions,omitempty"` //nolint:tagliatelle // hook wire format
}

type Decision struct {
	Behavior     string         `json:"behavior"`
	UpdatedInput map[string]any `json:"updatedInput,omitempty"` //nolint:tagliatelle // hook wire format
}

const (
	// toolNameAskUserQuestion and toolNameExitPlanMode are hook tool names
	// that receive special handling.
	toolNameAskUserQuestion = "AskUserQuestion"
	toolNameExitPlanMode    = "ExitPlanMode"

	// defaultApprovalTimeout is used when no timeout is configured.
	defaultApprovalTimeout = 120 * time.Second

	// exitPlanModeTimeout gives plan-mode approvals longer to be answered.
	exitPlanModeTimeout = 300 * time.Second

	// maxNotificationRunes caps the notification body before publishing.
	maxNotificationRunes = 4000

	// publishAttempts is how many times a notification publish is retried.
	publishAttempts = 3

	// maxButtons is the maximum number of answer buttons per notification.
	maxButtons = 3
)

// marshalHookOutput encodes a hook reply. The payload contains only strings
// and JSON-decoded values, so failures are practically impossible; on failure
// it logs and returns nil, mirroring the previous ignore-the-error behavior.
func marshalHookOutput(out HookOutput) json.RawMessage {
	encoded, err := json.Marshal(out)
	if err != nil {
		slog.Error("marshaling hook output", "error", err)

		return nil
	}

	return encoded
}

func AskOutput() json.RawMessage {
	return marshalHookOutput(HookOutput{
		HookSpecificOutput: HookSpecificOutput{
			HookEventName: "PermissionRequest",
		},
	})
}

func ApproveOutput() json.RawMessage {
	return marshalHookOutput(HookOutput{
		HookSpecificOutput: HookSpecificOutput{
			HookEventName: "PermissionRequest",
			Decision:      &Decision{Behavior: "allow"},
		},
	})
}

func DenyOutput() json.RawMessage {
	return marshalHookOutput(HookOutput{
		HookSpecificOutput: HookSpecificOutput{
			HookEventName: "PermissionRequest",
			Decision:      &Decision{Behavior: "deny"},
		},
	})
}

func AlwaysApproveOutput(suggestions []map[string]any) json.RawMessage {
	return marshalHookOutput(HookOutput{
		HookSpecificOutput: HookSpecificOutput{
			HookEventName:      "PermissionRequest",
			Decision:           &Decision{Behavior: "allow"},
			UpdatedPermissions: suggestions,
		},
	})
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

// FormatToolInfo renders the tool input of a permission request as the
// notification body text.
func FormatToolInfo(req PermissionRequest) string {
	if req.ToolName == toolNameAskUserQuestion {
		return formatAskUserQuestion(req.ToolInput)
	}

	return formatToolFields(req.ToolName, req.ToolInput)
}

// formatAskUserQuestion renders an AskUserQuestion tool input as the
// notification body text.
func formatAskUserQuestion(toolInput json.RawMessage) string {
	var builder strings.Builder
	builder.WriteString("Claude is asking a question:\n\n")

	var input struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	err := json.Unmarshal(toolInput, &input)
	if err != nil {
		builder.WriteString(string(toolInput))

		return builder.String()
	}
	for _, question := range input.Questions {
		builder.WriteString(question.Question + "\n")
		for _, opt := range question.Options {
			fmt.Fprintf(&builder, "  - %s: %s\n", opt.Label, opt.Description)
		}
	}

	return builder.String()
}

// formatToolFields renders a generic tool input as the notification body text.
func formatToolFields(toolName string, toolInput json.RawMessage) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Tool: %s\n", toolName)

	var input map[string]any
	err := json.Unmarshal(toolInput, &input)
	if err != nil {
		builder.WriteString(string(toolInput))

		return builder.String()
	}
	for key, val := range input {
		fmt.Fprintf(&builder, "%s: %v\n", key, val)
	}

	return builder.String()
}

// BuildNotificationTitle builds the notification title for a permission
// request, falling back to the default prefix when none is configured.
func BuildNotificationTitle(req PermissionRequest, prefix string) string {
	if prefix == "" {
		prefix = "Claude Code"
	}
	if req.ToolName == toolNameAskUserQuestion {
		return prefix + " - Question"
	}

	return fmt.Sprintf("%s - %s Permission", prefix, req.ToolName)
}

// ProcessHook handles a PreToolUse permission request: it publishes an
// approval notification and waits for the user's decision.
func ProcessHook(ctx context.Context, req PermissionRequest, cfg ApproverConfig) json.RawMessage {
	if cfg.Topic == "" {
		slog.Debug("no approver topic configured, falling back to ask")

		return AskOutput()
	}

	if req.ToolName == toolNameAskUserQuestion {
		return processAskUserQuestion(ctx, req, cfg)
	}

	requestID := cfg.newID()
	info := FormatToolInfo(req)
	info = ntfyclient.StripMarkdown(info)
	info = ntfyclient.TruncateRunes(info, maxNotificationRunes)
	title := BuildNotificationTitle(req, cfg.TitlePrefix)

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

	ctx, cancel := context.WithTimeout(ctx, approvalTimeout(cfg, req.ToolName))
	defer cancel()

	msgID, err := ntfyclient.PublishWithRetry(ctx, pubReq, publishAttempts)
	if err != nil {
		slog.Error("failed to publish approval request", "error", err)

		return AskOutput()
	}

	resp, err := ntfyclient.WaitForResponse(ctx, cfg.Server, cfg.Topic, requestID, cfg.Auth)
	if err != nil {
		slog.Error("waiting for response", "error", err)
		deletePublishedNotification(ctx, cfg, msgID)

		return AskOutput()
	}

	deletePublishedNotification(ctx, cfg, msgID)

	return decisionOutput(resp, req.PermissionSuggestions)
}

// approvalTimeout returns the configured timeout, falling back to a per-tool
// default when none is configured.
func approvalTimeout(cfg ApproverConfig, toolName string) time.Duration {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultApprovalTimeout
	}
	if toolName == toolNameExitPlanMode {
		timeout = exitPlanModeTimeout
	}

	return timeout
}

// deletePublishedNotification deletes the approval notification from ntfy on
// a best-effort basis. The context is detached from the parent so cleanup
// still runs after the (possibly expired) wait timeout.
func deletePublishedNotification(ctx context.Context, cfg ApproverConfig, msgID string) {
	err := ntfyclient.DeleteNotification(context.WithoutCancel(ctx), cfg.Server, cfg.Topic, msgID, cfg.Auth)
	if err != nil {
		slog.Debug("failed to delete notification", "error", err)
	}
}

// decisionOutput maps the received ntfy decision to the matching hook output,
// falling back to ask for unknown decisions.
func decisionOutput(resp *ntfyclient.Response, suggestions []map[string]any) json.RawMessage {
	switch resp.Decision {
	case "approve":
		return ApproveOutput()
	case "deny":
		return DenyOutput()
	case "always_approve":
		return AlwaysApproveOutput(suggestions)
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
	MultiSelect bool             `json:"multiSelect"` //nolint:tagliatelle // hook wire format
	Options     []questionOption `json:"options"`
}

// processAskUserQuestion publishes each question of an AskUserQuestion tool
// call to ntfy, waits for every answer, and returns the combined allow output.
func processAskUserQuestion(ctx context.Context, req PermissionRequest, cfg ApproverConfig) json.RawMessage {
	var input struct {
		Questions []questionInput `json:"questions"`
	}
	err := json.Unmarshal(req.ToolInput, &input)
	if err != nil {
		slog.Error("parsing AskUserQuestion input", "error", err)

		return AskOutput()
	}
	if len(input.Questions) == 0 {
		slog.Warn("AskUserQuestion with no questions")

		return AskOutput()
	}

	timeout := approvalTimeout(cfg, req.ToolName)
	responseTopicURL := strings.TrimRight(cfg.Server, "/") + "/" + cfg.Topic + "-response"
	answers := map[string]string{}

	for _, question := range input.Questions {
		requestID := cfg.newID()

		err = publishQuestionBatches(ctx, question, cfg, requestID, responseTopicURL, timeout)
		if err != nil {
			return AskOutput()
		}

		// Wait for the response for this question.
		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		resp, waitErr := ntfyclient.WaitForResponse(waitCtx, cfg.Server, cfg.Topic, requestID, cfg.Auth)
		cancel()
		if waitErr != nil {
			slog.Error("waiting for answer", "error", waitErr)

			return AskOutput()
		}
		if resp.Answer == "" {
			slog.Warn("no answer received, falling back to CLI")

			return AskOutput()
		}

		answers[question.Question] = resp.Answer
	}

	return askUserQuestionAllowOutput(req, answers)
}

// publishQuestionBatches publishes one AskUserQuestion question, splitting
// its options into notifications of at most maxButtons buttons each.
func publishQuestionBatches(
	ctx context.Context,
	question questionInput,
	cfg ApproverConfig,
	requestID, responseTopicURL string,
	timeout time.Duration,
) error {
	batches := splitOptionBatches(question.Options)
	for batchNumber, batch := range batches {
		pubReq := ntfyclient.PublishRequest{
			Server:   cfg.Server,
			Topic:    cfg.Topic,
			Title:    questionTitle(question),
			Message:  ntfyclient.StripMarkdown(questionBatchMessage(question, batch, batchNumber+1, len(batches))),
			Priority: "high",
			Actions:  answerActions(batch, requestID, responseTopicURL),
			Auth:     cfg.Auth,
		}

		publishCtx, cancel := context.WithTimeout(ctx, timeout)
		_, err := ntfyclient.PublishWithRetry(publishCtx, pubReq, publishAttempts)
		cancel()
		if err != nil {
			slog.Error("publishing question batch", "error", err)

			return err
		}
	}

	return nil
}

// splitOptionBatches splits options into consecutive batches of at most
// maxButtons options each.
func splitOptionBatches(options []questionOption) [][]questionOption {
	batches := [][]questionOption{}
	for start := 0; start < len(options); start += maxButtons {
		end := min(start+maxButtons, len(options))
		batches = append(batches, options[start:end])
	}

	return batches
}

// questionBatchMessage renders the notification body for one batch of answer
// options.
func questionBatchMessage(question questionInput, batch []questionOption, batchNumber, batchCount int) string {
	var builder strings.Builder
	if batchCount > 1 {
		fmt.Fprintf(&builder, "%s (%d/%d)", question.Question, batchNumber, batchCount)
	} else {
		builder.WriteString(question.Question)
	}
	if question.MultiSelect {
		builder.WriteString("\n(multiple selections allowed)")
	}
	builder.WriteString("\n\n")
	for _, opt := range batch {
		fmt.Fprintf(&builder, "• %s: %s\n", opt.Label, opt.Description)
	}

	return strings.TrimRight(builder.String(), "\n")
}

// questionTitle renders the notification title for a question, falling back
// to a generic title when the question has no header.
func questionTitle(question questionInput) string {
	if question.Header == "" {
		return "Claude Code: Question"
	}

	return "Claude Code: " + question.Header
}

// answerActions builds the ntfy buttons that post the chosen answer back to
// the response topic.
func answerActions(batch []questionOption, requestID, responseTopicURL string) []ntfyclient.Action {
	actions := make([]ntfyclient.Action, 0, len(batch))
	for _, opt := range batch {
		actions = append(actions, ntfyclient.Action{
			Action: "http",
			Label:  opt.Label,
			URL:    responseTopicURL,
			Method: "POST",
			Body:   answerActionBody(requestID, opt.Label),
			Clear:  true,
		})
	}

	return actions
}

// answerActionBody encodes the JSON body posted by an answer button. The
// payload only contains strings, so encoding cannot fail in practice; an
// empty body is kept if it ever does, matching prior behavior.
func answerActionBody(requestID, answer string) string {
	body, err := json.Marshal(map[string]string{
		"requestId": requestID,
		"answer":    answer,
	})
	if err != nil {
		slog.Error("marshaling answer action body", "error", err)
	}

	return string(body)
}

// askUserQuestionAllowOutput builds the allow output that echoes the original
// questions back unchanged together with the collected answers.
func askUserQuestionAllowOutput(req PermissionRequest, answers map[string]string) json.RawMessage {
	// Re-parse tool_input.questions so we echo it back unchanged inside updatedInput.
	var parsed struct {
		Questions []json.RawMessage `json:"questions"`
	}
	_ = json.Unmarshal(req.ToolInput, &parsed)

	questionsField := map[string]any{"questions": nil}
	if len(parsed.Questions) > 0 {
		// Convert []json.RawMessage into []any for clean marshaling.
		questions := make([]any, len(parsed.Questions))
		for index, rawQuestion := range parsed.Questions {
			questions[index] = rawQuestion
		}
		questionsField = map[string]any{"questions": questions}
	}

	return marshalHookOutput(HookOutput{
		HookSpecificOutput: HookSpecificOutput{
			HookEventName: "PermissionRequest",
			Decision: &Decision{
				Behavior: "allow",
				UpdatedInput: map[string]any{
					"questions": questionsField["questions"],
					"answers":   answers,
				},
			},
		},
	})
}
