package agent

// Integration debug test: actually invokes `codex app-server` and prints all
// raw notifications/events so we can compare with our parsing code.
//
// Run with:
//
//	go test -v -run TestCodexIntegrationDebug -timeout 120s ./server/agent/
//
// Requires `codex` CLI to be installed and authenticated.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCodexIntegrationDebugReasoning specifically uses a reasoning model (e.g. o4-mini)
// to verify that reasoning item `content`/`summary` fields are []string on the wire.
// Run with:
//
//	CODEX_INTEGRATION=1 CODEX_MODEL=o4-mini go test -v -run TestCodexIntegrationDebugReasoning -timeout 120s ./server/agent/
func TestCodexIntegrationDebugReasoning(t *testing.T) {
	if os.Getenv("CODEX_INTEGRATION") == "" {
		t.Skip("Set CODEX_INTEGRATION=1 to run this test (requires codex CLI installed & authenticated)")
	}
	model := os.Getenv("CODEX_MODEL")
	if model == "" {
		t.Skip("Set CODEX_MODEL=o4-mini (or other reasoning model) to run this test")
	}

	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "hello.txt"), []byte("Hello, world!\n"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	fmt.Printf("\n====== CODEX REASONING DEBUG TEST (model=%s) ======\n", model)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	args := []string{"app-server", "--listen", "stdio://"}
	client := NewCodexAppServerClient("codex", args)
	client.Verbose = false

	var reasoningItems []map[string]interface{}

	client.SetNotificationHandler(func(method string, params json.RawMessage) {
		// Capture item/started and item/completed for reasoning items
		if method != "item/started" && method != "item/completed" {
			return
		}
		var n struct {
			Item map[string]interface{} `json:"item"`
		}
		if err := json.Unmarshal(params, &n); err != nil {
			return
		}
		itemType, _ := n.Item["type"].(string)
		if itemType != "reasoning" {
			return
		}
		prettyBytes, _ := json.MarshalIndent(n.Item, "  ", "  ")
		fmt.Printf("\n[REASONING ITEM] method=%s\n  %s\n", method, string(prettyBytes))
		reasoningItems = append(reasoningItems, n.Item)
	})

	client.SetServerRequestHandler(func(method string, params json.RawMessage) (interface{}, *codexWireError) {
		switch method {
		case "item/commandExecution/requestApproval":
			return map[string]interface{}{"decision": "accept"}, nil
		case "item/fileChange/requestApproval":
			return map[string]interface{}{"decision": "accept"}, nil
		case "item/permissions/requestApproval":
			return map[string]interface{}{"permissions": map[string]interface{}{}, "scope": "turn"}, nil
		case "item/tool/call":
			call, err := parseCodexDynamicToolCall(params)
			if err != nil {
				return buildCodexDynamicToolResponse(false, err.Error()), nil
			}
			out, execErr := runCodexDynamicToolCall(ctx, workDir, call.ToolName, call.Arguments)
			if execErr != nil {
				return buildCodexDynamicToolResponse(false, execErr.Error()), nil
			}
			return buildCodexDynamicToolResponse(true, out), nil
		default:
			return map[string]interface{}{}, nil
		}
	})

	if err := client.Start(ctx, workDir); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.Initialize(ctx, "nexbox-debug", "1.0.0"); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	var threadResp codexThreadResponse
	if err := client.Request(ctx, "thread/start", map[string]interface{}{
		"approvalPolicy": "never",
		"sandbox":        "danger-full-access",
		"cwd":            workDir,
		"model":          model,
	}, &threadResp); err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	threadID := threadResp.Thread.ID

	turnDone := make(chan struct{}, 1)
	origHandler := client.notificationHandler
	client.notificationHandler = func(method string, params json.RawMessage) {
		origHandler(method, params)
		if method == "turn/completed" {
			select {
			case turnDone <- struct{}{}:
			default:
			}
		}
	}

	var turnResp codexTurnStartResponse
	if err := client.Request(ctx, "turn/start", map[string]interface{}{
		"threadId": threadID,
		"input": []map[string]interface{}{
			{"type": "text", "text": "What is 2+2? Think step by step."},
		},
		"model": model,
	}, &turnResp); err != nil {
		t.Fatalf("turn/start: %v", err)
	}

	select {
	case <-turnDone:
	case <-ctx.Done():
		t.Fatal("timeout")
	case <-client.readDone:
	}

	fmt.Printf("\n[SUMMARY] Captured %d reasoning item notifications\n", len(reasoningItems))
	if len(reasoningItems) == 0 {
		fmt.Printf("[WARNING] No reasoning items found — model may not emit reasoning items\n")
	} else {
		// Check if content/summary fields are []string or []object
		for i, item := range reasoningItems {
			content := item["content"]
			summary := item["summary"]
			fmt.Printf("[reasoning item %d] content type=%T value=%v\n", i, content, content)
			fmt.Printf("[reasoning item %d] summary type=%T value=%v\n", i, summary, summary)
		}
	}
}

func TestCodexIntegrationDebug(t *testing.T) {
	if os.Getenv("CODEX_INTEGRATION") == "" {
		t.Skip("Set CODEX_INTEGRATION=1 to run this test (requires codex CLI installed & authenticated)")
	}

	// ---- 1. Setup: create a temp dir with a simple text file ----
	workDir := t.TempDir()
	targetFile := filepath.Join(workDir, "hello.txt")
	if err := os.WriteFile(targetFile, []byte("Hello, world!\nThis is a test file.\n"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	fmt.Printf("\n====== CODEX INTEGRATION DEBUG TEST ======\n")
	fmt.Printf("workDir: %s\n", workDir)
	fmt.Printf("target file: %s\n", targetFile)
	fmt.Printf("initial content: %q\n", "Hello, world!\nThis is a test file.\n")

	// ---- 2. Raw-level test: capture all notifications directly ----
	fmt.Printf("\n------ PHASE 1: Raw CodexAppServerClient ------\n")
	testRawClient(t, workDir, targetFile)

	// ---- 3. High-level test: use CodexAgent.SendMessage and see StreamEvents ----
	fmt.Printf("\n------ PHASE 2: CodexAgent.SendMessage StreamEvents ------\n")
	testAgentSendMessage(t, workDir, targetFile)
}

// testRawClient uses CodexAppServerClient directly, intercepts all raw JSON
// notifications, and prints them so we can see exact wire format.
func testRawClient(t *testing.T, workDir, targetFile string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	args := []string{"app-server", "--listen", "stdio://"}
	client := NewCodexAppServerClient("codex", args)
	client.Verbose = false // we'll do our own printing

	// Capture every raw notification
	var notifCount int
	client.SetNotificationHandler(func(method string, params json.RawMessage) {
		notifCount++
		// Pretty-print params
		var pretty interface{}
		if err := json.Unmarshal(params, &pretty); err == nil {
			prettyBytes, _ := json.MarshalIndent(pretty, "    ", "  ")
			fmt.Printf("\n[NOTIFY #%d] method=%s\n    params=%s\n", notifCount, method, string(prettyBytes))
		} else {
			fmt.Printf("\n[NOTIFY #%d] method=%s\n    params(raw)=%s\n", notifCount, method, string(params))
		}
	})

	// Intercept all server requests and print them too
	client.SetServerRequestHandler(func(method string, params json.RawMessage) (interface{}, *codexWireError) {
		var pretty interface{}
		if err := json.Unmarshal(params, &pretty); err == nil {
			prettyBytes, _ := json.MarshalIndent(pretty, "    ", "  ")
			fmt.Printf("\n[SERVER REQUEST] method=%s\n    params=%s\n", method, string(prettyBytes))
		} else {
			fmt.Printf("\n[SERVER REQUEST] method=%s\n    params(raw)=%s\n", method, string(params))
		}

		// Handle the same cases our production code handles
		switch method {
		case "item/commandExecution/requestApproval":
			return map[string]interface{}{"decision": "accept"}, nil
		case "item/fileChange/requestApproval":
			return map[string]interface{}{"decision": "accept"}, nil
		case "item/permissions/requestApproval":
			var p struct {
				Permissions interface{} `json:"permissions"`
			}
			_ = json.Unmarshal(params, &p)
			if p.Permissions == nil {
				p.Permissions = map[string]interface{}{}
			}
			return map[string]interface{}{
				"permissions": p.Permissions,
				"scope":       "turn",
			}, nil
		case "item/tool/call":
			// Dynamic tool call - we need to handle it
			call, err := parseCodexDynamicToolCall(params)
			if err != nil {
				fmt.Printf("    [DYNAMIC TOOL PARSE ERROR] %v\n", err)
				return buildCodexDynamicToolResponse(false, "parse error: "+err.Error()), nil
			}
			fmt.Printf("    [DYNAMIC TOOL CALL] tool=%s itemID=%s args=%s\n",
				call.ToolName, call.ItemID, string(call.Arguments))
			output, execErr := runCodexDynamicToolCall(ctx, workDir, call.ToolName, call.Arguments)
			if execErr != nil {
				fmt.Printf("    [DYNAMIC TOOL ERROR] %v\n", execErr)
				return buildCodexDynamicToolResponse(false, execErr.Error()), nil
			}
			fmt.Printf("    [DYNAMIC TOOL OUTPUT] %q\n", output)
			return buildCodexDynamicToolResponse(true, output), nil
		default:
			return map[string]interface{}{}, nil
		}
	})

	if err := client.Start(ctx, workDir); err != nil {
		t.Fatalf("start codex app-server: %v", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			fmt.Printf("[CLOSE] warning: %v\n", err)
		}
	}()

	fmt.Println("\n[STEP] Initializing...")
	if err := client.Initialize(ctx, "nexbox-debug", "1.0.0"); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	fmt.Println("[STEP] Initialize OK")

	// Start a thread
	fmt.Println("\n[STEP] Starting thread...")
	threadParams := map[string]interface{}{
		"approvalPolicy": "never",
		"sandbox":        "danger-full-access",
		"cwd":            workDir,
	}
	var threadResp codexThreadResponse
	if err := client.Request(ctx, "thread/start", threadParams, &threadResp); err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	threadID := threadResp.Thread.ID
	fmt.Printf("[STEP] Thread started, id=%s\n", threadID)

	// Start a turn: ask codex to modify the text file
	prompt := fmt.Sprintf("Please modify the file hello.txt in the current directory. "+
		"Replace the line 'Hello, world!' with 'Hello, NexBox!'. "+
		"The file is at %s", targetFile)
	fmt.Printf("\n[STEP] Starting turn with prompt:\n  %s\n", prompt)

	turnDone := make(chan struct{}, 1)
	// We'll use a background goroutine to detect turn completion from notifications
	// (the raw client doesn't have the same completion logic, so we wait for turn/completed)
	// Actually, we need to set notification handler before starting turn, so we need to
	// add a small wrapper. Let's override it.
	innerNotifHandler := client.notificationHandler
	client.notificationHandler = func(method string, params json.RawMessage) {
		innerNotifHandler(method, params)
		if method == "turn/completed" {
			select {
			case turnDone <- struct{}{}:
			default:
			}
		}
	}

	turnStartParams := map[string]interface{}{
		"threadId": threadID,
		"input": []map[string]interface{}{
			{
				"type": "text",
				"text": prompt,
			},
		},
	}
	var turnResp codexTurnStartResponse
	if err := client.Request(ctx, "turn/start", turnStartParams, &turnResp); err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	fmt.Printf("[STEP] Turn started, id=%s\n", turnResp.Turn.ID)

	// Wait for completion
	fmt.Println("[STEP] Waiting for turn to complete...")
	select {
	case <-turnDone:
		fmt.Println("[STEP] Turn completed!")
	case <-ctx.Done():
		t.Fatal("timeout waiting for turn completion")
	case <-client.readDone:
		fmt.Printf("[STEP] Client connection closed: %v\n", client.readErr)
	}

	// Check if file was modified
	content, err := os.ReadFile(targetFile)
	if err != nil {
		fmt.Printf("[CHECK] Could not read target file: %v\n", err)
	} else {
		fmt.Printf("\n[CHECK] Final file content:\n%s\n", string(content))
	}

	fmt.Printf("\n[SUMMARY] Total notifications received: %d\n", notifCount)
}

// testAgentSendMessage uses the high-level CodexAgent.SendMessage and prints
// all StreamEvents emitted, so we can compare with the raw notifications above.
func testAgentSendMessage(t *testing.T, workDir, targetFile string) {
	t.Helper()

	// Reset file content
	if err := os.WriteFile(targetFile, []byte("Hello, world!\nThis is a test file.\n"), 0644); err != nil {
		t.Fatalf("reset file: %v", err)
	}
	fmt.Println("[SETUP] File reset to original content")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	agent := NewCodexAgent()
	agent.Verbose = false // suppress built-in logs; we'll print events manually

	req := Request{
		WorkDir: workDir,
		Message: fmt.Sprintf("Please modify the file hello.txt in the current directory. "+
			"Replace the line 'Hello, world!' with 'Hello, NexBox!'. "+
			"The file is at %s", targetFile),
	}

	var eventCount int
	result, err := agent.SendMessage(ctx, req, func(ev StreamEvent) {
		eventCount++
		switch ev.Type {
		case EventTextDelta:
			fmt.Printf("\n[EVENT #%d] TextDelta: %q\n", eventCount, ev.Content)
		case EventToolStart:
			fmt.Printf("\n[EVENT #%d] ToolStart: id=%s name=%s\n    input=%s\n",
				eventCount, ev.ToolID, ev.ToolName, ev.Input)
		case EventToolResult:
			fmt.Printf("\n[EVENT #%d] ToolResult: id=%s isError=%v\n    output=%q\n",
				eventCount, ev.ToolID, ev.IsError, ev.Output)
		case EventThinking:
			fmt.Printf("\n[EVENT #%d] Thinking: id=%s content=%q\n",
				eventCount, ev.ToolID, ev.Content)
		case EventSystem:
			fmt.Printf("\n[EVENT #%d] System: %q\n", eventCount, ev.Content)
		case EventError:
			fmt.Printf("\n[EVENT #%d] Error: %q\n", eventCount, ev.Content)
		case EventDone:
			fmt.Printf("\n[EVENT #%d] Done: sessionID=%s metadata=%v\n",
				eventCount, ev.SessionID, ev.Metadata)
		default:
			fmt.Printf("\n[EVENT #%d] Unknown type=%v content=%q\n",
				eventCount, ev.Type, ev.Content)
		}
	})

	if err != nil {
		fmt.Printf("\n[RESULT] Error: %v\n", err)
	} else {
		fmt.Printf("\n[RESULT] OK: sessionID=%s metadata=%v\n", result.SessionID, result.Metadata)
	}

	// Check file
	content, readErr := os.ReadFile(targetFile)
	if readErr != nil {
		fmt.Printf("[CHECK] Could not read target file: %v\n", readErr)
	} else {
		fmt.Printf("\n[CHECK] Final file content:\n%s\n", string(content))
	}

	fmt.Printf("\n[SUMMARY] Total StreamEvents received: %d\n", eventCount)
}
