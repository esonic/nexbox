package agent

// End-to-end CodexAgent test that exercises a real `codex app-server` doing
// web search and asserts the high-level StreamEvents the UI consumes (i.e.
// that webSearch tool calls actually surface as ToolStart + ToolResult).
//
// Run with:
//
//	CODEX_INTEGRATION=1 go test -v -run TestCodexAgentWebSearchE2E -timeout 240s ./server/agent/

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCodexAgentWebSearchE2E(t *testing.T) {
	if os.Getenv("CODEX_INTEGRATION") == "" {
		t.Skip("Set CODEX_INTEGRATION=1 to run this test (requires codex CLI installed & authenticated)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Second)
	defer cancel()

	a := NewCodexAgent()
	a.Verbose = false

	req := Request{
		WorkDir:  t.TempDir(),
		Features: []string{"web_search"},
		Message:  "Search the web for 'OpenAI Codex CLI release notes 2025' (use the web_search tool, do not guess) and tell me the latest version.",
	}

	var (
		mu          sync.Mutex
		toolStarts  []StreamEvent
		toolResults []StreamEvent
		eventLog    []StreamEvent
	)
	_, err := a.SendMessage(ctx, req, func(ev StreamEvent) {
		mu.Lock()
		defer mu.Unlock()
		eventLog = append(eventLog, ev)
		switch ev.Type {
		case EventToolStart:
			toolStarts = append(toolStarts, ev)
		case EventToolResult:
			toolResults = append(toolResults, ev)
		}
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	fmt.Printf("\n[E2E] tool_start count=%d, tool_result count=%d\n", len(toolStarts), len(toolResults))
	for i, ev := range toolStarts {
		fmt.Printf("  start[%d] tool=%s id=%s input=%s\n", i, ev.ToolName, ev.ToolID, ev.Input)
	}
	for i, ev := range toolResults {
		fmt.Printf("  result[%d] id=%s output=%q\n", i, ev.ToolID, ev.Output)
	}

	webSearchOrFetchStarts := 0
	for _, ev := range toolStarts {
		if ev.ToolName == "WebSearch" || ev.ToolName == "WebFetch" {
			webSearchOrFetchStarts++
			if !strings.Contains(ev.Input, "query") && !strings.Contains(ev.Input, "url") {
				t.Errorf("expected query/url in input, got: %s", ev.Input)
			}
		}
	}
	if webSearchOrFetchStarts == 0 {
		t.Fatalf("expected at least one WebSearch/WebFetch ToolStart, got 0 — webSearch items are being silently dropped")
	}
}
