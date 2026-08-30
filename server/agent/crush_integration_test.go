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
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("OPENROUTER_API_KEY is required")
	}
	a := NewCrushAgent()
	if cliPath := os.Getenv("CRUSH_CLI"); cliPath != "" {
		a.CLIPath = cliPath
	}
	a.DataDir = t.TempDir()
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "crushrc"), []byte("option default-providers false\nprovider add openrouter --type openai-compat --base-url \"https://openrouter.ai/api/v1\" --api-key \"$OPENROUTER_API_KEY\"\nmodel add openrouter/minimax/minimax-m3:free --name \"MiniMax M3 Free\"\n"), 0600); err != nil {
		t.Fatalf("write Crush config: %v", err)
	}
	t.Setenv("CRUSH_GLOBAL_CONFIG", configDir)
	t.Setenv("CRUSH_GLOBAL_DATA", t.TempDir())
	defer a.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	models, source := a.ListSupportedModels(ctx)
	if source != "dynamic" || len(models) != 1 || models[0].ID != "openrouter/minimax/minimax-m3:free" || models[0].Label != "MiniMax M3 Free" {
		t.Fatalf("native configured models = %#v, source=%q", models, source)
	}
	workDir := t.TempDir()
	send := func(sessionID, prompt, expected string) Result {
		t.Helper()
		var text strings.Builder
		result, err := a.SendMessage(ctx, Request{
			SessionID: sessionID,
			WorkDir:   workDir,
			Model:     "openrouter/minimax/minimax-m3:free",
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

func TestCrushProcessEnvDoesNotMapOpenRouterKey(t *testing.T) {
	t.Setenv("OPEN_ROUTER_KEY", "source-key")
	t.Setenv("OPENROUTER_API_KEY", "")
	for _, entry := range crushProcessEnv() {
		if entry == "OPENROUTER_API_KEY=source-key" {
			t.Fatal("Crush environment must not map OPEN_ROUTER_KEY to OPENROUTER_API_KEY")
		}
	}
}

func TestConfiguredCrushModelsUsesOnlyNativeConfig(t *testing.T) {
	models := configuredCrushModels(map[string]crushConfiguredProvider{
		"openrouter": {
			ID:     "openrouter",
			Models: []crushModelConfig{{ID: "minimax/minimax-m3:free", Name: "MiniMax M3 Free", ContextWindow: 200000}},
		},
		"disabled": {
			ID:      "disabled",
			Disable: true,
			Models:  []crushModelConfig{{ID: "should-not-appear"}},
		},
	})

	if len(models) != 1 {
		t.Fatalf("models = %#v", models)
	}
	if models[0].ID != "openrouter/minimax/minimax-m3:free" || models[0].Label != "MiniMax M3 Free" || models[0].ContextWindow != 200000 {
		t.Fatalf("first model = %#v", models[0])
	}
}

func TestCrushSelectedModelUsesNativeProviderPrefix(t *testing.T) {
	a := NewCrushAgent()
	model, err := a.selectedModel("openrouter/minimax/minimax-m3:free")
	if err != nil {
		t.Fatalf("selectedModel: %v", err)
	}
	if model.Provider != "openrouter" || model.ID != "minimax/minimax-m3:free" {
		t.Fatalf("selected model = %#v", model)
	}
	if _, err := a.selectedModel(""); err == nil || !strings.Contains(err.Error(), "select a Crush model") {
		t.Fatalf("selectedModel empty error = %v", err)
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
