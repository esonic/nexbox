package agent

// Web-search-focused debug test for codex.
// Run with:
//
//	CODEX_INTEGRATION=1 go test -v -run TestCodexWebSearchDebug -timeout 180s ./server/agent/ > /tmp/codex_websearch.log 2>&1
//
// Requires `codex` CLI installed & authenticated.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCodexWebSearchDebug(t *testing.T) {
	if os.Getenv("CODEX_INTEGRATION") == "" {
		t.Skip("Set CODEX_INTEGRATION=1 to run this test (requires codex CLI installed & authenticated)")
	}

	workDir := t.TempDir()
	fmt.Printf("\n====== CODEX WEB-SEARCH DEBUG TEST ======\nworkDir: %s\n", workDir)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	args := []string{"app-server", "--listen", "stdio://", "-c", `web_search_mode="live"`}
	client := NewCodexAppServerClient("codex", args)
	client.Verbose = false

	type rawNotif struct {
		method string
		params json.RawMessage
	}

	var (
		mu             sync.Mutex
		notifs         []rawNotif
		methodCounts   = map[string]int{}
		webSearchSeen  bool
	)

	client.SetNotificationHandler(func(method string, params json.RawMessage) {
		mu.Lock()
		notifs = append(notifs, rawNotif{method: method, params: append(json.RawMessage(nil), params...)})
		methodCounts[method]++
		if strings.Contains(strings.ToLower(method), "search") {
			webSearchSeen = true
		}
		mu.Unlock()

		// Pretty-print interesting notifications immediately
		lower := strings.ToLower(method)
		interesting := strings.Contains(lower, "search") ||
			strings.Contains(lower, "tool") ||
			method == "item/started" ||
			method == "item/completed" ||
			method == "turn/completed"
		if !interesting {
			return
		}
		var pretty interface{}
		if err := json.Unmarshal(params, &pretty); err == nil {
			b, _ := json.MarshalIndent(pretty, "  ", "  ")
			fmt.Printf("\n[NOTIFY] method=%s\n  params=%s\n", method, string(b))
		} else {
			fmt.Printf("\n[NOTIFY] method=%s\n  params(raw)=%s\n", method, string(params))
		}
	})

	client.SetServerRequestHandler(func(method string, params json.RawMessage) (interface{}, *codexWireError) {
		var pretty interface{}
		_ = json.Unmarshal(params, &pretty)
		b, _ := json.MarshalIndent(pretty, "  ", "  ")
		fmt.Printf("\n[SERVER REQUEST] method=%s\n  params=%s\n", method, string(b))
		switch method {
		case "item/commandExecution/requestApproval",
			"item/fileChange/requestApproval":
			return map[string]interface{}{"decision": "accept"}, nil
		case "item/permissions/requestApproval":
			return map[string]interface{}{
				"permissions": map[string]interface{}{},
				"scope":       "turn",
			}, nil
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
	fmt.Printf("\n[STEP] threadID=%s\n", threadID)

	turnDone := make(chan struct{}, 1)
	innerNotif := client.notificationHandler
	client.notificationHandler = func(method string, params json.RawMessage) {
		innerNotif(method, params)
		if method == "turn/completed" {
			select {
			case turnDone <- struct{}{}:
			default:
			}
		}
	}

	prompt := "Please search the web for 'OpenAI Codex CLI release notes 2025' and tell me the latest version. " +
		"You MUST use the web_search tool to look this up; do not guess from training data."
	turnStartParams := map[string]interface{}{
		"threadId": threadID,
		"input": []map[string]interface{}{
			{"type": "text", "text": prompt},
		},
	}
	var turnResp codexTurnStartResponse
	if err := client.Request(ctx, "turn/start", turnStartParams, &turnResp); err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	fmt.Printf("[STEP] turnID=%s\n", turnResp.Turn.ID)

	select {
	case <-turnDone:
	case <-ctx.Done():
		t.Fatal("timeout")
	case <-client.readDone:
		t.Fatalf("client closed: %v stderr=%s", client.readErr, client.Stderr())
	}

	mu.Lock()
	defer mu.Unlock()

	fmt.Printf("\n[SUMMARY] total notifications: %d, webSearchSeen=%v\n", len(notifs), webSearchSeen)
	keys := make([]string, 0, len(methodCounts))
	for k := range methodCounts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("\n[METHOD COUNTS]\n")
	for _, k := range keys {
		fmt.Printf("  %-60s %d\n", k, methodCounts[k])
	}

	// Print all item/started + item/completed payloads since those decide what tool calls render in UI.
	fmt.Printf("\n[ITEM NOTIFICATIONS]\n")
	for _, n := range notifs {
		if n.method != "item/started" && n.method != "item/completed" {
			continue
		}
		var pretty interface{}
		_ = json.Unmarshal(n.params, &pretty)
		b, _ := json.MarshalIndent(pretty, "  ", "  ")
		fmt.Printf("\n>>> %s\n%s\n", n.method, string(b))
	}
}
