package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCrushAgentIntegration exercises the installed Crush native server and
// OpenRouter. It is intentionally opt-in because it makes a real model call.
func TestCrushAgentIntegration(t *testing.T) {
	if os.Getenv("CRUSH_INTEGRATION") != "1" {
		t.Skip("set CRUSH_INTEGRATION=1 to run against an installed Crush and OpenRouter")
	}
	if os.Getenv("OPEN_ROUTER_KEY") == "" {
		t.Skip("OPEN_ROUTER_KEY is required")
	}
	a := NewCrushAgent([]CrushModel{{ID: "minimax/minimax-m3:free", Provider: "openrouter"}})
	if cliPath := os.Getenv("CRUSH_CLI"); cliPath != "" {
		a.CLIPath = cliPath
	}
	a.DataDir = t.TempDir()
	defer a.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	workDir := t.TempDir()
	send := func(sessionID, prompt, expected string) Result {
		t.Helper()
		var text strings.Builder
		result, err := a.SendMessage(ctx, Request{
			SessionID: sessionID,
			WorkDir:   workDir,
			Model:     "minimax/minimax-m3:free",
			Message:   prompt,
		}, func(event StreamEvent) {
			if event.Type == EventTextDelta {
				text.WriteString(event.Content)
			}
		})
		if err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		if result.SessionID == "" {
			t.Fatal("Crush returned an empty session ID")
		}
		if !strings.Contains(text.String(), expected) {
			t.Fatalf("unexpected Crush response: %q", text.String())
		}
		return result
	}

	first := send("", "Reply with exactly: NEXBOX_CRUSH_NATIVE_OK", "NEXBOX_CRUSH_NATIVE_OK")
	if a.serverStarted || a.serverCmd != nil {
		t.Fatal("Crush server must stop after the completed turn")
	}
	second := send(first.SessionID, "Reply with exactly: NEXBOX_CRUSH_RESTART_OK", "NEXBOX_CRUSH_RESTART_OK")
	if second.SessionID != first.SessionID {
		t.Fatalf("resumed session ID = %q, want %q", second.SessionID, first.SessionID)
	}
	if a.serverStarted || a.serverCmd != nil {
		t.Fatal("Crush server must stop after the resumed turn")
	}
	if _, err := os.Stat(filepath.Join(workDir, ".crush")); !os.IsNotExist(err) {
		t.Fatalf("Crush must not create project-local state: stat .crush: %v", err)
	}
}

func TestCrushWorkspaceTurnLockRespectsCancellation(t *testing.T) {
	workspace := &crushWorkspace{turnSlot: make(chan struct{}, 1)}
	workspace.turnSlot <- struct{}{}
	if err := workspace.acquireTurn(context.Background()); err != nil {
		t.Fatalf("acquire first turn: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := workspace.acquireTurn(ctx); err != context.Canceled {
		t.Fatalf("blocked turn error = %v, want context.Canceled", err)
	}

	workspace.releaseTurn()
	if err := workspace.acquireTurn(context.Background()); err != nil {
		t.Fatalf("acquire released turn: %v", err)
	}
	workspace.releaseTurn()
}
