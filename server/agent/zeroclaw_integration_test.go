package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestZeroClawAgentIntegration exercises the real `zeroclaw acp` subprocess
// and configured model provider. It is opt-in because it performs a live model
// request and requires ZeroClaw to be installed and configured.
func TestZeroClawAgentIntegration(t *testing.T) {
	if os.Getenv("ZEROCLAW_INTEGRATION") != "1" {
		t.Skip("set ZEROCLAW_INTEGRATION=1 to run against a configured zeroclaw installation")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	a := NewZeroClawAgent()
	a.Verbose = false
	var text strings.Builder
	result, err := a.SendMessage(ctx, Request{
		WorkDir: t.TempDir(),
		Message: "Reply with exactly: NEXBOX_ZEROCLAW_ACP_OK",
	}, func(event StreamEvent) {
		if event.Type == EventTextDelta {
			text.WriteString(event.Content)
		}
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if result.SessionID == "" {
		t.Fatal("ZeroClaw ACP returned an empty session ID")
	}
	if !strings.Contains(text.String(), "NEXBOX_ZEROCLAW_ACP_OK") {
		t.Fatalf("response = %q", text.String())
	}
}
