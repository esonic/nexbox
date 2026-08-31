package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
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
	probeID, err := newCrushID()
	if err != nil {
		t.Fatalf("create tool probe id: %v", err)
	}
	probeName := "nexbox-crush-tool-probe-" + probeID + ".txt"
	if err := os.WriteFile(filepath.Join(workDir, probeName), []byte("private tool probe\n"), 0600); err != nil {
		t.Fatalf("write tool probe: %v", err)
	}
	var toolCallID string
	var toolResult StreamEvent
	third, err := a.SendMessage(ctx, Request{
		SessionID: second.SessionID,
		WorkDir:   workDir,
		Model:     "openrouter/minimax/minimax-m3:free",
		Message:   "Call the ls tool to inspect the current working directory, then briefly describe what it contains. Do not answer from memory or assumptions.",
	}, func(event StreamEvent) {
		switch event.Type {
		case EventToolStart:
			if strings.EqualFold(event.ToolName, "ls") {
				toolCallID = event.ToolID
			}
		case EventToolResult:
			if event.ToolID == toolCallID {
				toolResult = event
			}
		}
	})
	if err != nil {
		t.Fatalf("tool SendMessage: %v", err)
	}
	if third.SessionID != second.SessionID {
		t.Fatalf("tool resumed session ID = %q, want %q", third.SessionID, second.SessionID)
	}
	if toolCallID == "" {
		t.Fatal("real Crush call did not emit an ls tool start")
	}
	if toolResult.Type != EventToolResult || !strings.Contains(toolResult.Output, probeName) {
		t.Fatalf("real Crush tool result = %#v, expected hidden filename %q", toolResult, probeName)
	}
	if a.serverStarted || a.serverCmd != nil {
		t.Fatal("Crush server must stop after the tool turn")
	}
	questionSeen := make(chan string, 1)
	var questionText strings.Builder
	fourth, err := a.SendMessage(ctx, Request{
		SessionID: third.SessionID,
		WorkDir:   workDir,
		Model:     "openrouter/minimax/minimax-m3:free",
		Message:   "Call the question tool now. Ask exactly one single_choice question: Choose a deployment color. Offer Blue and Green. After the user answers, report the selected color.",
		UserInputHandler: func(_ string, toolName, input string) (string, error) {
			if toolName != "CrushQuestion" {
				t.Errorf("interactive tool = %q, want CrushQuestion", toolName)
			}
			var form struct {
				Questions []struct {
					Question string `json:"question"`
					Header   string `json:"header"`
				} `json:"questions"`
			}
			if err := json.Unmarshal([]byte(input), &form); err != nil {
				return "", fmt.Errorf("decode Crush question form: %w", err)
			}
			if len(form.Questions) != 1 {
				return "", fmt.Errorf("Crush question count = %d, want 1", len(form.Questions))
			}
			select {
			case questionSeen <- form.Questions[0].Header:
			default:
			}
			answer, _ := json.Marshal(map[string]interface{}{"answers": map[string]string{form.Questions[0].Question: "Green"}})
			return string(answer), nil
		},
	}, func(event StreamEvent) {
		if event.Type == EventTextDelta {
			questionText.WriteString(event.Content)
		}
	})
	if err != nil {
		t.Fatalf("question SendMessage: %v", err)
	}
	if fourth.SessionID != third.SessionID {
		t.Fatalf("question resumed session ID = %q, want %q", fourth.SessionID, third.SessionID)
	}
	select {
	case header := <-questionSeen:
		if !strings.Contains(header, "Choose a deployment color") {
			t.Fatalf("question header = %q", header)
		}
	default:
		t.Fatal("real Crush call did not request structured user input")
	}
	if !strings.Contains(strings.ToLower(questionText.String()), "green") {
		t.Fatalf("question response = %q", questionText.String())
	}
	if a.serverStarted || a.serverCmd != nil {
		t.Fatal("Crush server must stop after the question turn")
	}
	cancelCtx, cancelTurn := context.WithCancel(ctx)
	questionStarted := make(chan struct{}, 1)
	cancelled := make(chan error, 1)
	go func() {
		_, sendErr := a.SendMessage(cancelCtx, Request{
			SessionID: fourth.SessionID,
			WorkDir:   workDir,
			Model:     "openrouter/minimax/minimax-m3:free",
			Message:   "Call the question tool now. Ask exactly one yes_no question: Should this operation continue? Wait for the user's answer before doing anything else.",
			UserInputHandler: func(_ string, toolName, _ string) (string, error) {
				if toolName != "CrushQuestion" {
					return "", fmt.Errorf("interactive tool = %q, want CrushQuestion", toolName)
				}
				select {
				case questionStarted <- struct{}{}:
				default:
				}
				<-cancelCtx.Done()
				return "", cancelCtx.Err()
			},
		}, func(StreamEvent) {})
		cancelled <- sendErr
	}()
	select {
	case <-questionStarted:
		cancelTurn()
	case sendErr := <-cancelled:
		cancelTurn()
		t.Fatalf("cancellable Crush turn ended before requesting input: %v", sendErr)
	case <-time.After(60 * time.Second):
		cancelTurn()
		t.Fatal("real Crush call did not reach a cancellable question")
	}
	select {
	case sendErr := <-cancelled:
		if !errors.Is(sendErr, context.Canceled) {
			t.Fatalf("cancelled Crush turn error = %v, want context.Canceled", sendErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled Crush turn did not return")
	}
	if a.serverStarted || a.serverCmd != nil {
		t.Fatal("Crush server must stop after the cancelled turn")
	}
	a.beginTurn()
	defer a.finishTurn()
	if err := a.ensureServer(ctx); err != nil {
		t.Fatalf("restart Crush for YOLO check: %v", err)
	}
	workspace, err := a.ensureWorkspace(ctx, workDir)
	if err != nil {
		t.Fatalf("create YOLO workspace: %v", err)
	}
	var permissions struct {
		Skip bool `json:"skip"`
	}
	if err := a.getJSON(ctx, "/v1/workspaces/"+workspace.id+"/permissions/skip", &permissions); err != nil {
		t.Fatalf("read YOLO permissions: %v", err)
	}
	if !permissions.Skip {
		t.Fatal("Nexbox Crush workspace must skip all permission requests")
	}
	if _, err := os.Stat(filepath.Join(workDir, ".crush")); !os.IsNotExist(err) {
		t.Fatalf("Crush must not create project-local state: stat .crush: %v", err)
	}
}

// TestCrushAgentProviderErrorIntegration proves that an error produced by a
// real OpenRouter request terminates the matching Nexbox turn instead of
// hanging while it waits for a successful run_complete event.
func TestCrushAgentProviderErrorIntegration(t *testing.T) {
	if os.Getenv("CRUSH_INTEGRATION") != "1" {
		t.Skip("set CRUSH_INTEGRATION=1 to run against an installed Crush and OpenRouter")
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
	t.Setenv("OPENROUTER_API_KEY", "invalid-nexbox-integration-key")
	defer a.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := a.SendMessage(ctx, Request{
		WorkDir: t.TempDir(),
		Model:   "openrouter/minimax/minimax-m3:free",
		Message: "Reply with OK.",
	}, func(StreamEvent) {})
	if err == nil {
		t.Fatal("real OpenRouter authentication failure unexpectedly succeeded")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Crush provider error was not correlated to its run: %v", err)
	}
	if result.SessionID == "" {
		t.Fatal("provider failure should preserve the created Crush session ID")
	}
	if a.serverStarted || a.serverCmd != nil {
		t.Fatal("Crush server must stop after the failed turn")
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

func TestCrushPermissionUsesStructuredChoices(t *testing.T) {
	title, input := crushPermissionInput(crushPermissionRequest{
		Action:      "view",
		ToolName:    "View",
		Description: "Read the requested source file",
		Path:        "/workspace/main.go",
	})
	if title != "Allow view?" {
		t.Fatalf("permission title = %q", title)
	}
	for _, want := range []string{"questions", "Allow once", "Allow for this turn", "Deny", "/workspace/main.go"} {
		if !strings.Contains(input, want) {
			t.Fatalf("permission input missing %q: %s", want, input)
		}
	}
	if got := crushPermissionAction(`{"answers":{"Allow view?":"Allow once"}}`, title); got != "allow" {
		t.Fatalf("allow-once action = %q", got)
	}
	if got := crushPermissionAction(`{"answers":{"Allow view?":"Allow for this turn"}}`, title); got != "allow_session" {
		t.Fatalf("allow-session action = %q", got)
	}
	if got := crushPermissionAction(`{"answers":{"Allow view?":"Deny"}}`, title); got != "deny" {
		t.Fatalf("deny action = %q", got)
	}
}

func TestCrushQuestionUsesStructuredChoices(t *testing.T) {
	request := crushQuestionRequest{
		ID:           "batch-1",
		ConfirmTitle: "Choose an approach",
		Questions: []crushQuestionItem{
			{ID: "style", Type: "multi_choice", Question: "What should we optimize?", Description: "Choose all that apply", Choices: []crushQuestionChoice{{ID: "speed", Label: "Speed"}, {ID: "safety", Label: "Safety"}}},
			{ID: "confirm", Type: "yes_no", Question: "Proceed?"},
			{ID: "notes", Type: "free_text", Question: "Anything else?"},
		},
	}
	title, input := crushQuestionInput(request)
	if title != "Choose an approach" {
		t.Fatalf("question title = %q", title)
	}
	for _, want := range []string{"questions", "What should we optimize?", "Speed", "Safety", "Yes", "No"} {
		if !strings.Contains(input, want) {
			t.Fatalf("question input missing %q: %s", want, input)
		}
	}

	answers := crushQuestionAnswers(`{"answers":{"style":"Speed, Safety","confirm":"Yes","notes":"Keep the API stable"}}`, request)
	if len(answers) != 3 {
		t.Fatalf("answers = %#v", answers)
	}
	if got := answers[0].SelectedIDs; len(got) != 2 || got[0] != "speed" || got[1] != "safety" {
		t.Fatalf("choice answers = %#v", got)
	}
	if answers[1].Yes == nil || !*answers[1].Yes {
		t.Fatalf("yes/no answer = %#v", answers[1])
	}
	if answers[2].FillInText != "Keep the API stable" {
		t.Fatalf("free-text answer = %#v", answers[2])
	}
}

func TestCrushQuestionPostsNativeAnswers(t *testing.T) {
	var received struct {
		BatchRequestID string                `json:"batch_request_id"`
		Responses      []crushQuestionAnswer `json:"responses"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/workspaces/workspace-1/questions/answer" {
			t.Errorf("unexpected question request: %s %s", request.Method, request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode question answer: %v", err)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	a := NewCrushAgent()
	a.serverURL = server.URL
	a.httpClient = server.Client()
	workspace := &crushWorkspace{
		id: "workspace-1",
		runs: map[string]*crushRun{
			"run-1": {
				sessionID: "session-1",
				input: func(_ string, toolName, input string) (string, error) {
					if toolName != "CrushQuestion" || !strings.Contains(input, "Pick one") {
						t.Errorf("unexpected question input: tool=%q input=%s", toolName, input)
					}
					return `{"answers":{"choice":"Green"}}`, nil
				},
			},
		},
	}
	a.handleCrushQuestion(workspace, json.RawMessage(`{"id":"batch-1","session_id":"session-1","questions":[{"id":"choice","type":"single_choice","question":"Pick one","description":"Choose a color","choices":[{"id":"green","label":"Green"},{"id":"blue","label":"Blue"}]}]}`))
	if received.BatchRequestID != "batch-1" || len(received.Responses) != 1 {
		t.Fatalf("question answer = %#v", received)
	}
	if got := received.Responses[0].SelectedIDs; len(got) != 1 || got[0] != "green" {
		t.Fatalf("selected ids = %#v", got)
	}
}

func TestBuildCrushPromptIncludesImages(t *testing.T) {
	prompt := buildCrushPrompt(Request{
		StaticContext:  "project context",
		DynamicContext: "dynamic context",
		Message:        "inspect this",
		Images:         []string{"/tmp/screenshot.png"},
	}, true)
	for _, want := range []string{"project context", "dynamic context", "[Attached Images]", "/tmp/screenshot.png", "[User Message]", "inspect this"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
}

func TestCrushAgentEventFailsItsRun(t *testing.T) {
	run := &crushRun{
		sessionID: "session-1",
		callback:  func(StreamEvent) {},
		done:      make(chan crushRunComplete, 1),
	}
	workspace := &crushWorkspace{runs: map[string]*crushRun{"run-1": run}}
	NewCrushAgent().handleCrushAgentEvent(workspace, crushAgentEvent{
		Type: "error", SessionID: "session-1", RunID: "run-1", Error: "provider unavailable",
	})
	select {
	case complete := <-run.done:
		if complete.err != "provider unavailable" {
			t.Fatalf("completion = %#v", complete)
		}
	default:
		t.Fatal("agent error did not finish its run")
	}
}

func TestCrushNativeMessageEnvelopeMapsToUnifiedEvents(t *testing.T) {
	var events []StreamEvent
	workspace := &crushWorkspace{
		runs:         map[string]*crushRun{"run-1": {sessionID: "session-1", callback: func(event StreamEvent) { events = append(events, event) }}},
		messageState: make(map[string]crushMessageState),
	}
	a := NewCrushAgent()
	a.handleWorkspaceEvent(workspace, []byte(`{"type":"message","payload":{"type":"updated","payload":{"session_id":"session-1","role":"assistant","parts":[{"type":"tool_call","data":{"id":"call-1","name":"view","input":"{","finished":false}}]}}}`))
	a.handleWorkspaceEvent(workspace, []byte(`{"type":"message","payload":{"type":"created","payload":{"session_id":"session-1","role":"assistant","parts":[{"type":"text","data":{"text":"Checking"}},{"type":"reasoning","data":{"thinking":"Need the file"}},{"type":"tool_call","data":{"id":"call-1","name":"view","input":"{\"path\":\"README.md\"}","finished":true}}]}}}`))
	a.handleWorkspaceEvent(workspace, []byte(`{"type":"message","payload":{"type":"created","payload":{"session_id":"session-1","role":"tool","parts":[{"type":"tool_result","data":{"tool_call_id":"call-1","name":"view","content":"file content","is_error":false}}]}}}`))
	if len(events) != 4 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Type != EventToolStart || events[0].ToolID != "call-1" || events[0].ToolName != "view" || !strings.Contains(events[0].Input, "README.md") {
		t.Fatalf("tool start = %#v", events[0])
	}
	if events[1].Type != EventTextDelta || events[1].Content != "Checking" {
		t.Fatalf("text = %#v", events[1])
	}
	if events[2].Type != EventThinking || events[2].Content != "Need the file" {
		t.Fatalf("thinking = %#v", events[2])
	}
	if events[3].Type != EventToolResult || events[3].ToolID != "call-1" || events[3].Output != "file content" {
		t.Fatalf("tool result = %#v", events[3])
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
