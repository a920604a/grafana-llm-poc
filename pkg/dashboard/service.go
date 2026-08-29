package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/acme/ai-assistant/pkg/llm"
)

// ErrInvalidOutput marks a draft that the model itself got wrong: unparsable JSON
// or a dashboard that violates the policy. It is not a transport or availability
// problem, so it must not be reported as an LLM connectivity failure.
var ErrInvalidOutput = errors.New("model produced an invalid dashboard")

type ToolCaller interface {
	CallTool(context.Context, string, any) (json.RawMessage, error)
}

type Service struct {
	provider llm.Provider
	model    string
}

func NewService(provider llm.Provider, model string) *Service {
	return &Service{provider: provider, model: model}
}

func (s *Service) Generate(ctx context.Context, tools ToolCaller, command Command) (map[string]any, error) {
	datasources, err := tools.CallTool(ctx, "list_datasources", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list Grafana datasources: %w", err)
	}
	var existing json.RawMessage
	if command.Operation == ModifyDashboard {
		existing, err = tools.CallTool(ctx, "get_dashboard_by_uid", map[string]any{"uid": command.DashboardUID})
		if err != nil {
			return nil, fmt.Errorf("get target dashboard: %w", err)
		}
	}
	prompt := fmt.Sprintf(`你是 Grafana dashboard JSON 專家。只輸出一個 JSON object，不可使用 markdown code fence。
需求：%s
operation：%s
folder UID：%s
允許 datasource UID：%s
Grafana datasource context：%s
既有 dashboard context：%s
輸出必須是完整 dashboard JSON，含 title、schemaVersion、version、time、panels。每個 panel 必須含 id、title、type、gridPos、datasource、targets。最多 12 panels；不可包含 alert、links、libraryPanel、permissions。Prometheus query 使用 targets[].expr。`,
		command.Instruction, command.Operation, command.FolderUID, strings.Join(command.DatasourceUIDs, ","), truncate(datasources, 64<<10), truncate(existing, 128<<10))
	var output strings.Builder
	err = s.provider.ChatStream(ctx, llm.ChatRequest{Model: s.model, Messages: []llm.Message{{Role: llm.RoleSystem, Content: "輸出嚴格 JSON。"}, {Role: llm.RoleUser, Content: prompt}}}, func(chunk llm.Chunk) error {
		if output.Len()+len(chunk.Content) > 1<<20 {
			return llm.ErrResponseTooLarge
		}
		output.WriteString(chunk.Content)
		return nil
	})
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(output.String())
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)
	var dashboard map[string]any
	if err := json.Unmarshal([]byte(text), &dashboard); err != nil {
		return nil, fmt.Errorf("%w: invalid dashboard JSON: %v", ErrInvalidOutput, err)
	}
	if err := Validate(dashboard, command); err != nil {
		return nil, err
	}
	return dashboard, nil
}

func Validate(value map[string]any, command Command) error {
	title, _ := value["title"].(string)
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("%w: dashboard title is required", ErrInvalidOutput)
	}
	delete(value, "uid")
	delete(value, "id")
	delete(value, "alert")
	delete(value, "permissions")
	if command.Operation == ModifyDashboard {
		value["uid"] = command.DashboardUID
	}
	panels, ok := value["panels"].([]any)
	if !ok || len(panels) == 0 || len(panels) > 12 {
		return fmt.Errorf("%w: dashboard panels must contain 1..12 items", ErrInvalidOutput)
	}
	allowedDatasource := map[string]bool{}
	for _, uid := range command.DatasourceUIDs {
		allowedDatasource[uid] = true
	}
	allowedTypes := map[string]bool{"timeseries": true, "stat": true, "gauge": true, "table": true, "logs": true}
	ids := map[int64]bool{}
	for _, raw := range panels {
		panel, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: invalid panel", ErrInvalidOutput)
		}
		panelType, _ := panel["type"].(string)
		if !allowedTypes[panelType] {
			return fmt.Errorf("%w: panel type %q is not allowed", ErrInvalidOutput, panelType)
		}
		idFloat, ok := panel["id"].(float64)
		if !ok || idFloat <= 0 || ids[int64(idFloat)] {
			return fmt.Errorf("%w: panel IDs must be unique positive integers", ErrInvalidOutput)
		}
		ids[int64(idFloat)] = true
		ds, _ := panel["datasource"].(map[string]any)
		uid, _ := ds["uid"].(string)
		if len(allowedDatasource) > 0 && !allowedDatasource[uid] {
			return fmt.Errorf("%w: datasource UID %q is not allowed", ErrInvalidOutput, uid)
		}
		targets, ok := panel["targets"].([]any)
		if !ok || len(targets) == 0 || len(targets) > 4 {
			return fmt.Errorf("%w: panel targets must contain 1..4 items", ErrInvalidOutput)
		}
	}
	if command.Operation == CreateDashboard {
		value["version"] = 0
	}
	if _, ok := value["schemaVersion"]; !ok {
		value["schemaVersion"] = 41
	}
	return nil
}

func truncate(data []byte, max int) string {
	if len(data) > max {
		return string(data[:max])
	}
	return string(data)
}
