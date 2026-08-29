package plugin

import (
	"errors"
	"testing"

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
