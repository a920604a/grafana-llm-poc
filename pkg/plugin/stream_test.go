package plugin

import (
	"errors"
	"fmt"
	"testing"

	"github.com/acme/ai-assistant/pkg/dashboard"
	"github.com/acme/ai-assistant/pkg/grafanamcp"
)

func TestNormalizeStreamErrorIdentifiesMCPFailure(t *testing.T) {
	code, message, retryable := normalizeStreamError(errors.Join(errors.New("start child"), grafanamcp.ErrUnavailable))
	if code != "MCP_UNAVAILABLE" {
		t.Fatalf("code = %q, want MCP_UNAVAILABLE", code)
	}
	if message != "Grafana MCP 無法使用。" {
		t.Fatalf("message = %q", message)
	}
	if !retryable {
		t.Fatal("MCP availability failure should be retryable")
	}
}

func TestNormalizeStreamErrorSeparatesModelOutputFromConnectivity(t *testing.T) {
	invalid := fmt.Errorf("%w: invalid dashboard JSON: unexpected character", dashboard.ErrInvalidOutput)
	if code, _, _ := normalizeStreamError(invalid); code != "DRAFT_INVALID_OUTPUT" {
		t.Fatalf("code = %q, want DRAFT_INVALID_OUTPUT", code)
	}
	toolFailure := fmt.Errorf("list Grafana datasources: %w", fmt.Errorf("%w: MCP tools/call failed (-32603): boom", grafanamcp.ErrToolFailed))
	if code, _, _ := normalizeStreamError(toolFailure); code != "MCP_TOOL_FAILED" {
		t.Fatalf("code = %q, want MCP_TOOL_FAILED", code)
	}
}
