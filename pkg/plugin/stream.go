package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/acme/ai-assistant/pkg/chat"
	"github.com/acme/ai-assistant/pkg/dashboard"
	"github.com/acme/ai-assistant/pkg/llm"
	"github.com/google/uuid"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
)

const requestPathPrefix = "chat/v1/requests/"
const draftPathPrefix = "dashboard/v1/drafts/"

type eventError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type streamEvent struct {
	Version      int         `json:"version"`
	RequestID    string      `json:"requestId"`
	SessionID    string      `json:"sessionId"`
	Sequence     int         `json:"sequence"`
	Type         string      `json:"type"`
	Timestamp    string      `json:"timestamp"`
	Model        string      `json:"model,omitempty"`
	Content      string      `json:"content,omitempty"`
	FinishReason string      `json:"finishReason,omitempty"`
	Usage        *llm.Usage  `json:"usage,omitempty"`
	Error        *eventError `json:"error,omitempty"`
	DraftID      string      `json:"draftId,omitempty"`
	Revision     int         `json:"revision,omitempty"`
	Title        string      `json:"title,omitempty"`
	WarningCount int         `json:"warningCount,omitempty"`
}

func requestPath(requestID string) string      { return requestPathPrefix + requestID }
func draftRequestPath(requestID string) string { return draftPathPrefix + requestID }

func parseRequestPath(path string) (string, bool) {
	if !strings.HasPrefix(path, requestPathPrefix) {
		return "", false
	}
	requestID := strings.TrimPrefix(path, requestPathPrefix)
	if strings.Contains(requestID, "/") {
		return "", false
	}
	if _, err := uuid.Parse(requestID); err != nil {
		return "", false
	}
	return requestID, true
}

func parseDraftPath(path string) (string, bool) {
	if !strings.HasPrefix(path, draftPathPrefix) {
		return "", false
	}
	requestID := strings.TrimPrefix(path, draftPathPrefix)
	if strings.Contains(requestID, "/") {
		return "", false
	}
	if _, err := uuid.Parse(requestID); err != nil {
		return "", false
	}
	return requestID, true
}

func principalFromPluginContext(pluginContext backend.PluginContext) (chat.Principal, error) {
	if pluginContext.User == nil || strings.TrimSpace(pluginContext.User.Login) == "" {
		return chat.Principal{}, errors.New("missing Grafana user")
	}
	return chat.Principal{OrgID: pluginContext.OrgID, Login: pluginContext.User.Login}, nil
}

func (a *App) SubscribeStream(_ context.Context, req *backend.SubscribeStreamRequest) (*backend.SubscribeStreamResponse, error) {
	requestID, ok := parseRequestPath(req.Path)
	principal, err := principalFromPluginContext(req.PluginContext)
	if err != nil {
		return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusNotFound}, nil
	}
	if ok && a.coordinator.Authorize(principal, requestID) == nil {
		return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusOK}, nil
	}
	if draftID, draftOK := parseDraftPath(req.Path); draftOK && a.drafts.Authorize(principal, draftID) == nil {
		return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusOK}, nil
	}
	{
		return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusNotFound}, nil
	}
}

func (a *App) PublishStream(_ context.Context, _ *backend.PublishStreamRequest) (*backend.PublishStreamResponse, error) {
	return &backend.PublishStreamResponse{Status: backend.PublishStreamStatusPermissionDenied}, nil
}

func (a *App) RunStream(ctx context.Context, req *backend.RunStreamRequest, sender *backend.StreamSender) error {
	requestID, ok := parseRequestPath(req.Path)
	if ok {
		return a.runChatStream(ctx, req, sender, requestID)
	}
	if draftID, draftOK := parseDraftPath(req.Path); draftOK {
		return a.runDashboardStream(ctx, req, sender, draftID)
	}
	return nil
}

func (a *App) runChatStream(ctx context.Context, req *backend.RunStreamRequest, sender *backend.StreamSender, requestID string) error {
	principal, err := principalFromPluginContext(req.PluginContext)
	if err != nil {
		return nil
	}
	providerCtx, cancel := context.WithTimeout(ctx, a.config.RequestTimeout)
	defer cancel()
	execution, err := a.coordinator.Claim(principal, requestID, cancel)
	if err != nil {
		return nil
	}
	status := chat.StatusFailed
	defer func() { a.coordinator.Finish(requestID, status) }()

	sequence := 0
	send := func(event streamEvent) error {
		event.Version, event.RequestID, event.SessionID, event.Sequence = 1, execution.RequestID, execution.SessionID, sequence
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
		data, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			return marshalErr
		}
		if sendErr := sender.SendJSON(data); sendErr != nil {
			return sendErr
		}
		sequence++
		return nil
	}
	if err := send(streamEvent{Type: "start", Model: execution.Model}); err != nil {
		return err
	}
	started := time.Now()
	var finishReason string
	var usage *llm.Usage
	err = a.provider.ChatStream(providerCtx, llm.ChatRequest{Model: execution.Model, Messages: execution.Messages}, func(chunk llm.Chunk) error {
		if chunk.FinishReason != "" {
			finishReason = chunk.FinishReason
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if chunk.Content == "" {
			return nil
		}
		return send(streamEvent{Type: "token", Content: chunk.Content})
	})
	if err != nil {
		code, message, retryable := normalizeStreamError(err)
		_ = send(streamEvent{Type: "error", Error: &eventError{Code: code, Message: message, Retryable: retryable}})
		if errors.Is(err, context.Canceled) {
			status = chat.StatusCancelled
		}
		log.DefaultLogger.Error("chat stream failed", "request_id", requestID, "session_id", execution.SessionID, "model", execution.Model, "status", status, "error_code", code, "total_duration_ms", time.Since(started).Milliseconds())
		return nil
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	if err := send(streamEvent{Type: "complete", FinishReason: finishReason, Usage: usage}); err != nil {
		return err
	}
	status = chat.StatusCompleted
	log.DefaultLogger.Info("chat stream completed", "request_id", requestID, "session_id", execution.SessionID, "model", execution.Model, "status", status, "total_duration_ms", time.Since(started).Milliseconds())
	return nil
}

func (a *App) runDashboardStream(ctx context.Context, req *backend.RunStreamRequest, sender *backend.StreamSender, requestID string) error {
	principal, err := principalFromPluginContext(req.PluginContext)
	if err != nil {
		return nil
	}
	providerCtx, cancel := context.WithTimeout(ctx, a.config.RequestTimeout)
	defer cancel()
	descriptor, command, err := a.drafts.Claim(principal, requestID, cancel)
	if err != nil {
		return nil
	}
	sequence := 0
	send := func(event streamEvent) error {
		event.Version, event.RequestID, event.SessionID, event.Sequence = 1, descriptor.RequestID, descriptor.SessionID, sequence
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if err := sender.SendJSON(data); err != nil {
			return err
		}
		sequence++
		return nil
	}
	if err := send(streamEvent{Type: "start", Model: a.config.LLMModel}); err != nil {
		return err
	}
	started := time.Now()
	client, err := a.mcpClient()
	if err == nil {
		var value map[string]any
		value, err = a.dashboard.Generate(providerCtx, client, command)
		if err == nil {
			draft := a.drafts.Store(principal, command, value)
			if err = send(streamEvent{Type: "draft", DraftID: draft.ID, Revision: draft.Revision, Title: draft.Title, WarningCount: len(draft.Warnings)}); err == nil {
				err = send(streamEvent{Type: "complete", FinishReason: "draft_ready"})
			}
		}
	}
	if err != nil {
		code, message, retryable := normalizeStreamError(err)
		_ = send(streamEvent{Type: "error", Error: &eventError{Code: code, Message: message, Retryable: retryable}})
		log.DefaultLogger.Error("dashboard draft failed", "request_id", requestID, "model", a.config.LLMModel, "error_code", code, "total_duration_ms", time.Since(started).Milliseconds())
		return nil
	}
	log.DefaultLogger.Info("dashboard draft completed", "request_id", requestID, "model", a.config.LLMModel, "status", "completed", "total_duration_ms", time.Since(started).Milliseconds())
	return nil
}

func normalizeStreamError(err error) (string, string, bool) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "LLM_TIMEOUT", "模型未在時限內完成回應。", true
	case errors.Is(err, context.Canceled):
		return "REQUEST_CANCELLED", "請求已取消。", false
	case errors.Is(err, llm.ErrUnauthorized):
		return "LLM_UNAVAILABLE", "模型服務設定錯誤。", false
	case errors.Is(err, llm.ErrResponseTooLarge):
		return "STREAM_LIMIT_EXCEEDED", "模型回應超過大小限制。", false
	case errors.Is(err, llm.ErrProtocol):
		return "LLM_PROTOCOL_ERROR", "模型回傳了不相容的串流格式。", true
	case errors.Is(err, dashboard.ErrConflict):
		return "DRAFT_CONFLICT", "草稿工作已經開始。", false
	default:
		return "LLM_UNAVAILABLE", "目前無法連線模型服務。", true
	}
}
