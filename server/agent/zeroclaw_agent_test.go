package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestZeroClawAgentCapabilities(t *testing.T) {
	capabilities := NewZeroClawAgent().Capabilities()
	if capabilities.Fork || capabilities.Interject || capabilities.ChromeDevTools {
		t.Fatalf("unexpected ZeroClaw capabilities: %+v", capabilities)
	}
	if capabilities.ModelSwitchMode != ModelSwitchNewSessionOnly {
		t.Fatalf("model switch mode = %q, want %q", capabilities.ModelSwitchMode, ModelSwitchNewSessionOnly)
	}
}

func TestZeroClawAgentListsConfiguredAliases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI shell script is POSIX-only")
	}
	path := filepath.Join(t.TempDir(), "zeroclaw")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'fast\\ncareful\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	models, source := (&ZeroClawAgent{CLIPath: path}).ListSupportedModels(context.Background())
	if source != "dynamic" {
		t.Fatalf("source = %q, want dynamic", source)
	}
	if len(models) != 2 || models[0].ID != "fast" || models[1].ID != "careful" {
		t.Fatalf("models = %#v", models)
	}
}

func TestZeroClawAgentPassesSelectedAliasToNewSession(t *testing.T) {
	t.Setenv("NEXBOX_ZEROCLAW_TEST_SCENARIO", "alias")
	agent := newZeroClawHelperAgent(t)

	_, err := agent.SendMessage(context.Background(), Request{
		WorkDir: t.TempDir(),
		Model:   "careful",
		Message: "use the selected alias",
	}, func(StreamEvent) {})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
}

func TestZeroClawAgentStreamsACPEvents(t *testing.T) {
	t.Setenv("NEXBOX_ZEROCLAW_TEST_SCENARIO", "stream")
	agent := newZeroClawHelperAgent(t)

	var mu sync.Mutex
	var events []StreamEvent
	result, err := agent.SendMessage(context.Background(), Request{
		WorkDir:        t.TempDir(),
		StaticContext:  "static project context",
		DynamicContext: "dynamic session context",
		Message:        "implement the change",
	}, func(event StreamEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if result.SessionID != "s-test" {
		t.Fatalf("SessionID = %q, want s-test", result.SessionID)
	}
	if result.Metadata["stopReason"] != "end_turn" {
		t.Fatalf("stopReason = %#v", result.Metadata["stopReason"])
	}

	mu.Lock()
	defer mu.Unlock()
	wantTypes := []string{
		EventSessionInfo,
		EventTextDelta,
		EventThinking,
		EventToolStart,
		EventToolResult,
		EventDone,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("events = %#v, want types %#v", events, wantTypes)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Fatalf("event[%d].Type = %q, want %q", i, events[i].Type, want)
		}
	}
	if events[1].Content != "hello" || events[2].Content != "thinking" {
		t.Fatalf("unexpected streamed content: %#v", events)
	}
	if events[3].ToolName != "delegate" || events[3].ToolID != "tc-1" {
		t.Fatalf("unexpected tool start: %#v", events[3])
	}
	if events[4].Output != "delegated result" || events[4].IsError {
		t.Fatalf("unexpected tool result: %#v", events[4])
	}
	for _, event := range events {
		if strings.HasPrefix(event.Type, "subtask_") {
			t.Fatalf("ZeroClaw delegate must stay a normal tool call: %#v", event)
		}
	}
}

func TestZeroClawAgentResumesPersistedSession(t *testing.T) {
	t.Setenv("NEXBOX_ZEROCLAW_TEST_SCENARIO", "resume")
	agent := newZeroClawHelperAgent(t)

	result, err := agent.SendMessage(context.Background(), Request{
		SessionID: "s-existing",
		WorkDir:   t.TempDir(),
		Message:   "continue",
	}, func(StreamEvent) {})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if result.SessionID != "s-existing" {
		t.Fatalf("SessionID = %q, want s-existing", result.SessionID)
	}
}

func TestZeroClawAgentMapsMissingResumeToExpired(t *testing.T) {
	t.Setenv("NEXBOX_ZEROCLAW_TEST_SCENARIO", "expired")
	agent := newZeroClawHelperAgent(t)

	_, err := agent.SendMessage(context.Background(), Request{
		SessionID: "s-missing",
		WorkDir:   t.TempDir(),
		Message:   "continue",
	}, func(StreamEvent) {})
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("error = %v, want ErrSessionExpired", err)
	}
}

func TestZeroClawAgentAnswersPermissionRequest(t *testing.T) {
	t.Setenv("NEXBOX_ZEROCLAW_TEST_SCENARIO", "permission")
	agent := newZeroClawHelperAgent(t)

	var handlerCalled bool
	_, err := agent.SendMessage(context.Background(), Request{
		WorkDir: t.TempDir(),
		Message: "run status",
		UserInputHandler: func(requestID, toolName, input string) (string, error) {
			handlerCalled = true
			if requestID != "approval-1" || toolName != "ZeroClawPermission" {
				t.Fatalf("unexpected permission identity: id=%q tool=%q", requestID, toolName)
			}
			if !strings.Contains(input, "Approve shell?") || !strings.Contains(input, "Allow once") {
				t.Fatalf("permission input = %s", input)
			}
			return `{"answers":{"Approve shell?":"Allow once"}}`, nil
		},
	}, func(StreamEvent) {})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !handlerCalled {
		t.Fatal("permission handler was not called")
	}
}

func TestZeroClawAgentCancelUsesACPNotification(t *testing.T) {
	t.Setenv("NEXBOX_ZEROCLAW_TEST_SCENARIO", "cancel")
	agent := newZeroClawHelperAgent(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := agent.SendMessage(ctx, Request{
		WorkDir: t.TempDir(),
		Message: "long task",
	}, func(event StreamEvent) {
		if event.Type == EventTextDelta && event.Content == "started" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestBuildZeroClawPromptEmbedsImages(t *testing.T) {
	imagePath := t.TempDir() + "/sample.png"
	if err := os.WriteFile(imagePath, []byte("\x89PNG\r\n\x1a\nimage"), 0600); err != nil {
		t.Fatal(err)
	}
	prompt, err := buildZeroClawPrompt(Request{
		StaticContext: "context",
		Message:       "inspect this",
		Images:        []string{imagePath},
	}, true)
	if err != nil {
		t.Fatalf("buildZeroClawPrompt: %v", err)
	}
	parts, ok := prompt.([]interface{})
	if !ok || len(parts) != 2 {
		t.Fatalf("prompt = %#v, want two content parts", prompt)
	}
	resourcePart, ok := parts[1].(map[string]interface{})
	if !ok || resourcePart["type"] != "resource" {
		t.Fatalf("resource part = %#v", parts[1])
	}
	resource, ok := resourcePart["resource"].(map[string]interface{})
	if !ok || resource["mimeType"] != "image/png" || resource["blob"] == "" {
		t.Fatalf("resource = %#v", resourcePart["resource"])
	}
}

func newZeroClawHelperAgent(t *testing.T) *ZeroClawAgent {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &ZeroClawAgent{
		CLIPath: executable,
		CLIArgs: []string{"-test.run=TestZeroClawACPHelperProcess", "--", "acp"},
		Verbose: false,
	}
}

// TestZeroClawACPHelperProcess acts as a tiny ZeroClaw ACP v1 subprocess. The
// parent tests execute the current test binary with only this test selected.
func TestZeroClawACPHelperProcess(t *testing.T) {
	if os.Getenv("NEXBOX_ZEROCLAW_TEST_SCENARIO") == "" {
		return
	}
	os.Exit(runZeroClawACPHelper(os.Getenv("NEXBOX_ZEROCLAW_TEST_SCENARIO")))
}

func runZeroClawACPHelper(scenario string) int {
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	write := func(value interface{}) bool { return encoder.Encode(value) == nil }
	respond := func(id json.RawMessage, result interface{}) bool {
		return write(map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": result})
	}
	fail := func(format string, args ...interface{}) int {
		_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
		return 2
	}

	for scanner.Scan() {
		var message acpWireMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return fail("invalid JSON: %v", err)
		}
		switch message.Method {
		case "initialize":
			if !respond(message.ID, map[string]interface{}{
				"protocolVersion": 1,
				"agentInfo": map[string]interface{}{
					"name": "zeroclaw-acp", "version": "test",
				},
				"_meta": map[string]interface{}{
					"zeroclaw": map[string]interface{}{"defaultModel": "openrouter/test-model"},
				},
			}) {
				return 2
			}

		case "session/new":
			if scenario == "resume" || scenario == "expired" {
				return fail("unexpected session/new in %s scenario", scenario)
			}
			if scenario == "alias" {
				var params struct {
					AgentAlias string `json:"agentAlias"`
				}
				if json.Unmarshal(message.Params, &params) != nil || params.AgentAlias != "careful" {
					return fail("unexpected session/new params: %s", message.Params)
				}
			}
			if !respond(message.ID, map[string]interface{}{"sessionId": "s-test", "workspaceDir": "/tmp"}) {
				return 2
			}

		case "session/resume":
			if scenario == "expired" {
				if !write(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      message.ID,
					"error":   map[string]interface{}{"code": -32000, "message": "session not found"},
				}) {
					return 2
				}
				continue
			}
			if scenario != "resume" {
				return fail("unexpected session/resume in %s scenario", scenario)
			}
			if !respond(message.ID, map[string]interface{}{}) {
				return 2
			}

		case "session/prompt":
			if scenario == "stream" {
				var params struct {
					Prompt string `json:"prompt"`
				}
				if json.Unmarshal(message.Params, &params) != nil ||
					!strings.Contains(params.Prompt, "static project context") ||
					!strings.Contains(params.Prompt, "dynamic session context") ||
					!strings.Contains(params.Prompt, "implement the change") {
					return fail("unexpected prompt: %s", message.Params)
				}
				updates := []interface{}{
					acpTestUpdate("agent_message_chunk", map[string]interface{}{"content": map[string]interface{}{"type": "text", "text": "hello"}}),
					acpTestUpdate("agent_thought_chunk", map[string]interface{}{"content": map[string]interface{}{"type": "text", "text": "thinking"}}),
					acpTestUpdate("tool_call", map[string]interface{}{"toolCallId": "tc-1", "title": "delegate", "status": "pending", "rawInput": map[string]interface{}{"task": "child"}}),
					acpTestUpdate("tool_call_update", map[string]interface{}{"toolCallId": "tc-1", "name": "delegate", "status": "completed", "rawOutput": "delegated result"}),
				}
				for _, update := range updates {
					if !write(update) {
						return 2
					}
				}
			}

			if scenario == "permission" {
				if !write(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      "zc-out-0",
					"method":  "session/request_permission",
					"params": map[string]interface{}{
						"sessionId": "s-test",
						"options": []interface{}{
							map[string]interface{}{"optionId": "allow-once", "name": "Allow once", "kind": "allow_once"},
							map[string]interface{}{"optionId": "reject-once", "name": "Reject", "kind": "reject_once"},
						},
						"toolCall": map[string]interface{}{
							"toolCallId": "approval-1",
							"title":      "Approve shell?",
							"rawInput":   map[string]interface{}{"tool": "shell", "summary": "git status"},
						},
					},
				}) {
					return 2
				}
				if !scanner.Scan() {
					return fail("missing permission response")
				}
				var response struct {
					ID     string `json:"id"`
					Result struct {
						Outcome struct {
							Outcome  string `json:"outcome"`
							OptionID string `json:"optionId"`
						} `json:"outcome"`
					} `json:"result"`
				}
				if json.Unmarshal(scanner.Bytes(), &response) != nil || response.ID != "zc-out-0" ||
					response.Result.Outcome.Outcome != "selected" || response.Result.Outcome.OptionID != "allow-once" {
					return fail("unexpected permission response: %s", scanner.Bytes())
				}
			}

			if scenario == "cancel" {
				if !write(acpTestUpdate("agent_message_chunk", map[string]interface{}{"content": map[string]interface{}{"type": "text", "text": "started"}})) {
					return 2
				}
				if !scanner.Scan() {
					return fail("missing session/cancel notification")
				}
				var cancelMessage acpWireMessage
				if json.Unmarshal(scanner.Bytes(), &cancelMessage) != nil || cancelMessage.Method != "session/cancel" || len(cancelMessage.ID) != 0 {
					return fail("unexpected cancel message: %s", scanner.Bytes())
				}
				if !respond(message.ID, map[string]interface{}{"sessionId": "s-test", "stopReason": "cancelled"}) {
					return 2
				}
				continue
			}

			resultSessionID := "s-test"
			if scenario == "resume" {
				resultSessionID = "s-existing"
			}
			if !respond(message.ID, map[string]interface{}{
				"sessionId": resultSessionID, "stopReason": "end_turn", "content": "done",
			}) {
				return 2
			}

		default:
			return fail("unexpected method %q", message.Method)
		}
	}
	if err := scanner.Err(); err != nil {
		return fail("stdin scan failed: %v", err)
	}
	return 0
}

func acpTestUpdate(kind string, fields map[string]interface{}) map[string]interface{} {
	update := map[string]interface{}{"sessionUpdate": kind}
	for key, value := range fields {
		update[key] = value
	}
	return map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]interface{}{
			"sessionId": "s-test",
			"update":    update,
		},
	}
}
