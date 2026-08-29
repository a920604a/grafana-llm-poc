package plugin

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/acme/ai-assistant/pkg/chat"
	"github.com/acme/ai-assistant/pkg/dashboard"
	"github.com/acme/ai-assistant/pkg/grafanamcp"
	"github.com/acme/ai-assistant/pkg/llm"
	"github.com/acme/ai-assistant/pkg/llmopenai"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/backend/resource/httpadapter"
)

var (
	_ backend.CallResourceHandler   = (*App)(nil)
	_ instancemgmt.InstanceDisposer = (*App)(nil)
	_ backend.CheckHealthHandler    = (*App)(nil)
	_ backend.StreamHandler         = (*App)(nil)
)

type App struct {
	backend.CallResourceHandler
	config      Config
	provider    llm.Provider
	coordinator *chat.Coordinator
	drafts      *dashboard.Registry
	dashboard   *dashboard.Service
	rootCtx     context.Context
	cancel      context.CancelFunc
	mcpMu       sync.Mutex
	mcp         *grafanamcp.Client
}

func NewApp(ctx context.Context, _ backend.AppInstanceSettings) (instancemgmt.Instance, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	httpClient := &http.Client{Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}}
	provider, err := llmopenai.New(httpClient, llmopenai.Config{BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey})
	if err != nil {
		return nil, err
	}
	// The context passed to the instance factory can be canceled after instance
	// creation. MCP is a long-lived child process, so keep its values but bind
	// cancellation to Dispose instead of the factory call lifecycle.
	rootCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	app := &App{
		config:      cfg,
		provider:    provider,
		coordinator: chat.NewCoordinator(cfg.LLMModel, cfg.PendingTTL, cfg.TerminalTTL),
		drafts:      dashboard.NewRegistry(cfg.DraftTTL),
		dashboard:   dashboard.NewService(provider, cfg.LLMModel),
		rootCtx:     rootCtx,
		cancel:      cancel,
	}
	mux := http.NewServeMux()
	app.registerRoutes(mux)
	app.CallResourceHandler = httpadapter.New(mux)
	return app, nil
}

func (a *App) Dispose() {
	a.cancel()
	a.coordinator.Dispose()
	a.drafts.Dispose()
	a.mcpMu.Lock()
	if a.mcp != nil {
		a.mcp.Close()
		a.mcp = nil
	}
	a.mcpMu.Unlock()
}

func (a *App) mcpClient() (*grafanamcp.Client, error) {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	if a.mcp != nil {
		return a.mcp, nil
	}
	client, err := grafanamcp.Start(a.rootCtx, a.config.MCPGrafanaPath)
	if err != nil {
		return nil, err
	}
	a.mcp = client
	return client, nil
}

func (a *App) CheckHealth(_ context.Context, _ *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	details := []byte(`{"provider":"openai-compatible","model":"` + a.config.LLMModel + `"}`)
	return &backend.CheckHealthResult{Status: backend.HealthStatusOk, Message: "AI Assistant backend is configured", JSONDetails: details}, nil
}
