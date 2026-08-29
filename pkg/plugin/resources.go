package plugin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/acme/ai-assistant/pkg/chat"
	"github.com/acme/ai-assistant/pkg/dashboard"
	"github.com/acme/ai-assistant/pkg/llm"
	"github.com/google/uuid"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

type chatCommandRequest struct {
	ClientRequestID string        `json:"clientRequestId"`
	SessionID       string        `json:"sessionId,omitempty"`
	Messages        []llm.Message `json:"messages"`
}

type channelAddress struct {
	Scope     string `json:"scope"`
	Namespace string `json:"namespace"`
	Path      string `json:"path"`
}

type chatCommandResponse struct {
	RequestID string         `json:"requestId"`
	SessionID string         `json:"sessionId"`
	Model     string         `json:"model"`
	Channel   channelAddress `json:"channel"`
	ExpiresAt string         `json:"expiresAt"`
}

type errorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"requestId,omitempty"`
	} `json:"error"`
}

type dashboardDraftRequest struct {
	Operation      dashboard.Operation `json:"operation"`
	Instruction    string              `json:"instruction"`
	FolderUID      string              `json:"folderUid"`
	DatasourceUIDs []string            `json:"datasourceUids"`
	DashboardUID   string              `json:"dashboardUid,omitempty"`
	TargetPanelID  *int64              `json:"targetPanelId,omitempty"`
}

type applyDraftRequest struct {
	Revision int  `json:"revision"`
	Confirm  bool `json:"confirm"`
}

func (a *App) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/health", a.handleHealth)
	mux.HandleFunc("/v1/chat", a.handleCreateChat)
	mux.HandleFunc("/v1/chat/", a.handleChatByID)
	mux.HandleFunc("/v1/dashboard-drafts", a.handleCreateDashboardDraft)
	mux.HandleFunc("/v1/dashboard-drafts/", a.handleDashboardDraftByID)
}

func (a *App) handleCreateDashboardDraft(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支援此 HTTP method", "")
		return
	}
	principal, err := principalFromContext(req)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "需要 Grafana 使用者身分", "")
		return
	}
	var body dashboardDraftRequest
	if !decodeJSON(w, req, a.config.MaxRequestBytes, &body) {
		return
	}
	if message := validateDashboardCommand(body); message != "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", message, "")
		return
	}
	descriptor := a.drafts.Create(principal, dashboard.Command{Operation: body.Operation, Instruction: body.Instruction, FolderUID: body.FolderUID, DatasourceUIDs: body.DatasourceUIDs, DashboardUID: body.DashboardUID, TargetPanelID: body.TargetPanelID})
	writeJSON(w, http.StatusAccepted, map[string]any{"requestId": descriptor.RequestID, "sessionId": descriptor.SessionID, "model": a.config.LLMModel,
		"channel": channelAddress{Scope: "plugin", Namespace: "acme-aiassistant-app", Path: draftRequestPath(descriptor.RequestID)}, "expiresAt": descriptor.ExpiresAt})
}

func (a *App) handleDashboardDraftByID(w http.ResponseWriter, req *http.Request) {
	principal, err := principalFromContext(req)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "需要 Grafana 使用者身分", "")
		return
	}
	remainder := strings.TrimPrefix(req.URL.Path, "/v1/dashboard-drafts/")
	parts := strings.Split(strings.Trim(remainder, "/"), "/")
	if len(parts) == 0 {
		writeError(w, http.StatusNotFound, "DRAFT_NOT_FOUND", "找不到草稿", "")
		return
	}
	if _, err := uuid.Parse(parts[0]); err != nil {
		writeError(w, http.StatusNotFound, "DRAFT_NOT_FOUND", "找不到草稿", "")
		return
	}
	if len(parts) == 1 && req.Method == http.MethodGet {
		draft, err := a.drafts.Get(principal, parts[0])
		if errors.Is(err, dashboard.ErrNotFound) {
			writeError(w, http.StatusNotFound, "DRAFT_NOT_FOUND", "找不到草稿", "")
			return
		}
		writeJSON(w, http.StatusOK, draft)
		return
	}
	if len(parts) != 2 || parts[1] != "apply" || req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支援此操作", "")
		return
	}
	var body applyDraftRequest
	if !decodeJSON(w, req, 16<<10, &body) {
		return
	}
	if !body.Confirm || body.Revision <= 0 {
		writeError(w, http.StatusBadRequest, "CONFIRMATION_REQUIRED", "必須確認指定草稿版本", "")
		return
	}
	draft, already, err := a.drafts.BeginApply(principal, parts[0], body.Revision)
	if errors.Is(err, dashboard.ErrNotFound) {
		writeError(w, http.StatusNotFound, "DRAFT_NOT_FOUND", "找不到草稿", "")
		return
	}
	if errors.Is(err, dashboard.ErrConflict) {
		writeError(w, http.StatusConflict, "DRAFT_CONFLICT", "草稿版本不符或正在套用", "")
		return
	}
	if already {
		writeJSON(w, http.StatusOK, draft)
		return
	}
	client, err := a.mcpClient()
	if err != nil {
		_, _ = a.drafts.FinishApply(principal, draft.ID, nil, err)
		writeError(w, http.StatusServiceUnavailable, "MCP_UNAVAILABLE", "Grafana MCP 無法使用", "")
		return
	}
	arguments := map[string]any{"dashboard": draft.Dashboard, "folderUid": draft.FolderUID, "overwrite": draft.Operation == dashboard.ModifyDashboard, "message": "Applied by local AI Assistant"}
	raw, callErr := client.CallTool(req.Context(), "update_dashboard", arguments)
	result := map[string]any{"raw": json.RawMessage(raw)}
	updated, _ := a.drafts.FinishApply(principal, draft.ID, result, callErr)
	if callErr != nil {
		writeError(w, http.StatusBadGateway, "DASHBOARD_APPLY_FAILED", "Grafana 無法套用草稿", "")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func decodeJSON(w http.ResponseWriter, req *http.Request, max int64, destination any) bool {
	req.Body = http.MaxBytesReader(w, req.Body, max)
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "請求格式不正確", "")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "請求只能包含一個 JSON object", "")
		return false
	}
	return true
}

func validateDashboardCommand(body dashboardDraftRequest) string {
	if body.Operation != dashboard.CreateDashboard && body.Operation != dashboard.ModifyDashboard {
		return "operation 必須是 create_dashboard 或 modify_dashboard"
	}
	if len(strings.TrimSpace(body.Instruction)) < 3 || len(body.Instruction) > 16<<10 {
		return "instruction 長度必須介於 3 到 16384 字元"
	}
	if len(body.FolderUID) > 128 || len(body.DatasourceUIDs) == 0 || len(body.DatasourceUIDs) > 10 {
		return "必須指定 1 到 10 個 datasource UID"
	}
	for _, uid := range body.DatasourceUIDs {
		if strings.TrimSpace(uid) == "" || len(uid) > 128 {
			return "datasource UID 不正確"
		}
	}
	if body.Operation == dashboard.ModifyDashboard && strings.TrimSpace(body.DashboardUID) == "" {
		return "修改 dashboard 時必須指定 dashboardUid"
	}
	return ""
}

func (a *App) handleHealth(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支援此 HTTP method", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "provider": "openai-compatible", "model": a.config.LLMModel})
}

func (a *App) handleCreateChat(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支援此 HTTP method", "")
		return
	}
	principal, err := principalFromContext(req)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "需要 Grafana 使用者身分", "")
		return
	}
	req.Body = http.MaxBytesReader(w, req.Body, a.config.MaxRequestBytes)
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	var body chatCommandRequest
	if err := decoder.Decode(&body); err != nil {
		code, status := "VALIDATION_ERROR", http.StatusBadRequest
		if strings.Contains(err.Error(), "request body too large") {
			code, status = "PAYLOAD_TOO_LARGE", http.StatusRequestEntityTooLarge
		}
		writeError(w, status, code, "請求格式不正確", "")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "請求只能包含一個 JSON object", "")
		return
	}
	if message := validateChatCommand(body); message != "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", message, "")
		return
	}
	descriptor, err := a.coordinator.Create(principal, chat.Command{ClientRequestID: body.ClientRequestID, SessionID: body.SessionID, Messages: body.Messages})
	if errors.Is(err, chat.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "clientRequestId 已用於不同請求", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "無法建立請求", "")
		return
	}
	writeJSON(w, http.StatusAccepted, chatCommandResponse{
		RequestID: descriptor.RequestID,
		SessionID: descriptor.SessionID,
		Model:     descriptor.Model,
		Channel:   channelAddress{Scope: "plugin", Namespace: "acme-aiassistant-app", Path: requestPath(descriptor.RequestID)},
		ExpiresAt: descriptor.ExpiresAt.Format("2006-01-02T15:04:05.000Z07:00"),
	})
}

func (a *App) handleChatByID(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支援此 HTTP method", "")
		return
	}
	principal, err := principalFromContext(req)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "需要 Grafana 使用者身分", "")
		return
	}
	requestID := strings.TrimPrefix(req.URL.Path, "/v1/chat/")
	if _, err := uuid.Parse(requestID); err != nil {
		writeError(w, http.StatusNotFound, "REQUEST_NOT_FOUND", "找不到請求", "")
		return
	}
	status, err := a.coordinator.Cancel(principal, requestID)
	if errors.Is(err, chat.ErrNotFound) {
		writeError(w, http.StatusNotFound, "REQUEST_NOT_FOUND", "找不到請求", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requestId": requestID, "status": status})
}

func validateChatCommand(body chatCommandRequest) string {
	if _, err := uuid.Parse(body.ClientRequestID); err != nil {
		return "clientRequestId 必須是 UUID"
	}
	if body.SessionID != "" {
		if _, err := uuid.Parse(body.SessionID); err != nil {
			return "sessionId 必須是 UUID"
		}
	}
	if len(body.Messages) == 0 || len(body.Messages) > 50 {
		return "messages 數量必須介於 1 到 50"
	}
	systemCount := 0
	for index, message := range body.Messages {
		if strings.TrimSpace(message.Content) == "" || len(message.Content) > 16<<10 {
			return "message content 不可為空且不得超過 16 KiB"
		}
		switch message.Role {
		case llm.RoleSystem:
			systemCount++
			if index != 0 || systemCount > 1 {
				return "system message 最多一個且必須在最前面"
			}
		case llm.RoleUser, llm.RoleAssistant:
		default:
			return "不支援的 message role"
		}
	}
	if body.Messages[len(body.Messages)-1].Role != llm.RoleUser {
		return "最後一則 message 必須是 user"
	}
	return ""
}

func principalFromContext(req *http.Request) (chat.Principal, error) {
	pluginContext := backend.PluginConfigFromContext(req.Context())
	if pluginContext.User == nil || strings.TrimSpace(pluginContext.User.Login) == "" {
		return chat.Principal{}, errors.New("missing Grafana user")
	}
	return chat.Principal{OrgID: pluginContext.OrgID, Login: pluginContext.User.Login}, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message, requestID string) {
	var envelope errorEnvelope
	envelope.Error.Code, envelope.Error.Message, envelope.Error.RequestID = code, message, requestID
	writeJSON(w, status, envelope)
}
