package plugin

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	LLMBaseURL      string
	LLMModel        string
	LLMAPIKey       string
	RequestTimeout  time.Duration
	PendingTTL      time.Duration
	TerminalTTL     time.Duration
	MaxRequestBytes int64
	MCPGrafanaPath  string
	DraftTTL        time.Duration
}

func loadConfig() (Config, error) {
	cfg := Config{
		LLMBaseURL:      envOr("LLM_BASE_URL", "http://ollama:11434/v1"),
		LLMModel:        envOr("LLM_MODEL", "qwen2.5:3b"),
		LLMAPIKey:       os.Getenv("LLM_API_KEY"),
		RequestTimeout:  120 * time.Second,
		PendingTTL:      30 * time.Second,
		TerminalTTL:     5 * time.Minute,
		MaxRequestBytes: 256 << 10,
		MCPGrafanaPath:  envOr("MCP_GRAFANA_PATH", "mcp-grafana"),
		DraftTTL:        15 * time.Minute,
	}
	var err error
	if cfg.RequestTimeout, err = durationEnv("LLM_REQUEST_TIMEOUT", cfg.RequestTimeout); err != nil {
		return Config{}, err
	}
	if cfg.PendingTTL, err = durationEnv("CHAT_PENDING_TTL", cfg.PendingTTL); err != nil {
		return Config{}, err
	}
	if cfg.TerminalTTL, err = durationEnv("CHAT_TERMINAL_TTL", cfg.TerminalTTL); err != nil {
		return Config{}, err
	}
	if raw := strings.TrimSpace(os.Getenv("CHAT_MAX_REQUEST_BYTES")); raw != "" {
		value, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || value <= 0 {
			return Config{}, fmt.Errorf("invalid CHAT_MAX_REQUEST_BYTES")
		}
		cfg.MaxRequestBytes = value
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return value, nil
}
