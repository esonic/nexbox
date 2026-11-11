package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPiAgentRPCMapsTurnAndCheckpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-pi.sh")
	script := `#!/bin/sh
printf '%s\n' "$*" > "$0.args"
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  case "$line" in
    *'"type":"get_state"'*)
      printf '{"id":"%s","type":"response","command":"get_state","success":true,"data":{"sessionId":"pi-session"}}\n' "$id"
      ;;
    *'"type":"prompt"'*)
      printf '{"id":"%s","type":"response","command":"prompt","success":true}\n' "$id"
      printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hello"}}'
      printf '%s\n' '{"type":"tool_execution_start","toolCallId":"call-1","toolName":"bash","args":{"command":"pwd"}}'
      printf '%s\n' '{"type":"tool_execution_end","toolCallId":"call-1","toolName":"bash","result":{"content":[{"type":"text","text":"/repo"}]}}'
      printf '%s\n' '{"type":"agent_settled"}'
      ;;
    *'"type":"get_session_stats"'*)
      printf '{"id":"%s","type":"response","command":"get_session_stats","success":true,"data":{"contextUsage":{"tokens":42,"contextWindow":1000}}}\n' "$id"
      ;;
    *'"type":"get_entries"'*)
      printf '{"id":"%s","type":"response","command":"get_entries","success":true,"data":{"entries":[],"leafId":"assistant-entry-1"}}\n' "$id"
      ;;
  esac
done
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	a := &PiAgent{CLIPath: path}
	var events []StreamEvent
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := a.SendMessage(ctx, Request{WorkDir: dir, Message: "hello", Model: "openrouter/vendor/model:free:high", Effort: "low"}, func(event StreamEvent) { events = append(events, event) })
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	args, err := os.ReadFile(path + ".args")
	if err != nil || !strings.Contains(string(args), "--model openrouter/vendor/model:free --thinking low") || strings.Contains(string(args), ":high") {
		t.Fatalf("legacy suffix overrides selected effort: %s, %v", args, err)
	}
	if result.SessionID != "pi-session" || result.Checkpoint != piLeafCheckpointPrefix+"assistant-entry-1" {
		t.Fatalf("result = %+v", result)
	}
	var gotText, gotStart, gotResult, gotDone bool
	for _, event := range events {
		switch event.Type {
		case EventTextDelta:
			gotText = event.Content == "hello"
		case EventToolStart:
			gotStart = event.ToolID == "call-1" && event.ToolName == "bash"
		case EventToolResult:
			gotResult = event.Output == "/repo"
		case EventDone:
			gotDone = event.SessionID == "pi-session"
		}
	}
	if !gotText || !gotStart || !gotResult || !gotDone {
		t.Fatalf("missing mapped events: %#v", events)
	}
}

func TestPiAgentCapabilities(t *testing.T) {
	a := NewPiAgent()
	caps := a.Capabilities()
	if !caps.Fork || !caps.Interject || caps.ChromeDevTools || caps.ModelSwitchMode != ModelSwitchCurrentSession {
		t.Fatalf("unexpected capabilities: %+v", caps)
	}
	if !strings.Contains(a.CLIPath, "pi") {
		t.Fatalf("unexpected default pi path")
	}
}

func TestPiModelCapabilitiesFromRPC(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pi")
	script := `#!/bin/sh
while IFS= read -r line; do
 id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
 case "$line" in
 *'"type":"get_available_models"'*) printf '{"id":"%s","type":"response","success":true,"data":{"models":[{"provider":"openrouter","id":"vendor/model:free","contextWindow":262144,"reasoning":true},{"provider":"openrouter","id":"plain","contextWindow":8192,"reasoning":false}]}}\n' "$id" ;;
 *'"type":"set_model"'*) printf '{"id":"%s","type":"response","success":true}\n' "$id" ;;
 *'"type":"get_state"'*) printf '{"id":"%s","type":"response","success":true,"data":{"thinkingLevel":"low"}}\n' "$id" ;;
 *'"type":"get_available_thinking_levels"'*) printf '{"id":"%s","type":"response","success":true,"data":{"levels":["off","low","high"]}}\n' "$id" ;;
 esac
done
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	models := []ModelInfo{{ID: "openrouter/vendor/model:free"}, {ID: "openrouter/plain"}, {ID: "openrouter/missing"}}
	(&PiAgent{CLIPath: path}).enrichPiModels(context.Background(), models)
	if models[0].ContextWindow != 262144 || strings.Join(models[0].SupportedEfforts, ",") != "off,low,high" {
		t.Fatalf("reasoning model: %+v", models[0])
	}
	if models[1].ContextWindow != 8192 || len(models[1].SupportedEfforts) != 0 {
		t.Fatalf("non-reasoning model: %+v", models[1])
	}
	if models[2].ContextWindow != 0 {
		t.Fatalf("missing model: %+v", models[2])
	}
}

func TestPiAgentListsOnlyEnabledModels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-pi.sh")
	script := `#!/bin/sh
case "$*" in
  *openrouter/foo*) printf '%s\n' 'provider    model    context  max-out  thinking  images' 'openrouter  foo      128K     8K       yes       no' 'openrouter  foo-mini 128K     8K       yes       no' ;;
  *openai/bar*) printf '%s\n' 'provider    model    context  max-out  thinking  images' 'openai      bar      128K     8K       no        no' ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	settings := `{"defaultProvider":"openai","defaultModel":"bar","enabledModels":["openrouter/foo","openai/bar"]}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0644); err != nil {
		t.Fatal(err)
	}
	a := &PiAgent{CLIPath: path, AgentDir: dir}
	models, source := a.ListSupportedModels(context.Background())
	if source != "configured" || len(models) != 2 {
		t.Fatalf("models=%+v source=%q", models, source)
	}
	if models[0].ID != "openai/bar" || models[1].ID != "openrouter/foo" {
		t.Fatalf("unexpected model order: %+v", models)
	}
}

// The catalog columns drive NexBox's effort picker and context-usage
// indicator, so the `--list-models` table must be read past the model id.
func TestPiAgentCatalogCarriesContextWindowAndEfforts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-pi.sh")
	script := `#!/bin/sh
printf '%s\n' 'provider    model    context  max-out  thinking  images' \
  'openrouter  foo      262.1K   32.8K    yes       no' \
  'openai      bar      1.0M     8K       no        no'
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	settings := `{"enabledModels":["openrouter/foo","openai/bar","openrouter/foo:high"]}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0644); err != nil {
		t.Fatal(err)
	}
	models, _ := (&PiAgent{CLIPath: path, AgentDir: dir}).ListSupportedModels(context.Background())
	byID := map[string]ModelInfo{}
	for _, model := range models {
		byID[model.ID] = model
	}
	thinking, ok := byID["openrouter/foo"]
	if !ok || thinking.ContextWindow != 262100 || thinking.DefaultEffort != "medium" || len(thinking.SupportedEfforts) == 0 {
		t.Fatalf("thinking model = %+v", thinking)
	}
	plain, ok := byID["openai/bar"]
	if !ok || plain.ContextWindow != 1_000_000 || plain.DefaultEffort != "" || len(plain.SupportedEfforts) != 0 {
		t.Fatalf("non-thinking model = %+v", plain)
	}
	// Old suffix entries merge into the ordinary model, retaining the picker.
	if len(models) != 2 {
		t.Fatalf("legacy suffix produced duplicate models: %+v", models)
	}
}

func TestPiModelMatchesPatternPreservesPiGlobAndFuzzyMatching(t *testing.T) {
	tests := []struct {
		pattern string
		id      string
		want    bool
	}{
		{"openrouter/foo", "openrouter/foo", true},
		{"openrouter/foo", "openrouter/foo-mini", false},
		{"openrouter/*", "openrouter/~anthropic/claude-sonnet-latest", true},
		{"sonnet", "openrouter/anthropic/claude-sonnet-latest", true},
	}
	for _, test := range tests {
		if got := piModelMatchesPattern(test.id, test.pattern); got != test.want {
			t.Errorf("piModelMatchesPattern(%q, %q) = %v, want %v", test.id, test.pattern, got, test.want)
		}
	}
}

func TestPiAgentForkCancellationDoesNotPromptParent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-pi.sh")
	script := `#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$0.calls"
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  case "$line" in
    *'"type":"get_entries"'*) printf '{"id":"%s","type":"response","command":"get_entries","success":true,"data":{"entries":[],"leafId":null}}\n' "$id" ;;
    *'"type":"fork"'*) printf '{"id":"%s","type":"response","command":"fork","success":true,"data":{"cancelled":true}}\n' "$id" ;;
  esac
done
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	_, err := (&PiAgent{CLIPath: path}).SendMessage(context.Background(), Request{
		WorkDir: dir,
		Message: "must not reach the parent session",
		Fork:    &ForkContext{SessionID: "parent", Checkpoint: "entry"},
	}, func(StreamEvent) {})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("fork cancellation error = %v", err)
	}
	calls, err := os.ReadFile(path + ".calls")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), `"type":"prompt"`) || strings.Contains(string(calls), `"type":"get_state"`) {
		t.Fatalf("cancelled fork sent a command to the parent: %s", calls)
	}
}

func TestPiAgentProviderErrorReturnsErrorWithoutDoneOrCheckpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-pi.sh")
	script := `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  case "$line" in
    *'"type":"get_state"'*) printf '{"id":"%s","type":"response","command":"get_state","success":true,"data":{"sessionId":"pi-session"}}\n' "$id" ;;
    *'"type":"prompt"'*)
      printf '{"id":"%s","type":"response","command":"prompt","success":true}\n' "$id"
      printf '%s\n' '{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"provider unavailable","usage":{"input":12,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":12,"cost":{"total":0}}}}' '{"type":"agent_settled"}'
      ;;
  esac
done
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	var events []StreamEvent
	result, err := (&PiAgent{CLIPath: path}).SendMessage(context.Background(), Request{WorkDir: dir, Message: "hello"}, func(event StreamEvent) {
		events = append(events, event)
	})
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("provider error = %v", err)
	}
	if result.Checkpoint != "" {
		t.Fatalf("failed turn checkpoint = %q", result.Checkpoint)
	}
	for _, event := range events {
		if event.Type == EventDone {
			t.Fatal("failed turn emitted EventDone")
		}
	}
}

func TestPiAgentUsageUsesAPICompatibleNumbers(t *testing.T) {
	var events []StreamEvent
	result := Result{}
	var mu sync.Mutex
	(&PiAgent{}).handleEvent(piRPCRecord{Type: "message_end", Message: json.RawMessage(`{"role":"assistant","stopReason":"stop","usage":{"input":12,"output":3,"cacheRead":4,"cacheWrite":5,"reasoning":2,"totalTokens":15,"cost":{"total":0.42}}}`)}, func(event StreamEvent) {
		events = append(events, event)
	}, &result, &mu, new(error), func() {})
	if len(events) != 1 || events[0].Type != EventContextUsage {
		t.Fatalf("usage events = %#v", events)
	}
	metadata := events[0].Metadata
	if metadata["inputTokens"] != 12 || metadata["outputTokens"] != 3 || metadata["cacheReadInputTokens"] != 4 || metadata["cacheCreationInputTokens"] != 5 {
		t.Fatalf("usage metadata = %#v", metadata)
	}
	if _, ok := metadata["inputTokens"].(int); !ok {
		t.Fatalf("inputTokens type = %T, want int", metadata["inputTokens"])
	}
}

// TestPiAgentRPCIntegration exercises the published pi CLI against a real
// configured provider. It is opt-in because it consumes a provider request
// and leaves normal Pi-owned session files behind.
func TestPiCompletedTurnLeafUpgradesLegacyUserCheckpoint(t *testing.T) {
	entry := func(id, parent, role string) piEntry {
		e := piEntry{ID: id, ParentID: parent, Type: "message"}
		e.Message.Role = role
		return e
	}
	state := piEntriesState{
		Entries: []piEntry{
			entry("user-1", "", "user"),
			entry("assistant-tool-call", "user-1", "assistant"),
			entry("tool-result", "assistant-tool-call", "toolResult"),
			entry("assistant-final", "tool-result", "assistant"),
			entry("user-2", "assistant-final", "user"),
			entry("assistant-2", "user-2", "assistant"),
		},
		LeafID: "assistant-2",
	}
	if got := piCompletedTurnLeaf(state, "user-1"); got != "assistant-final" {
		t.Fatalf("completed turn leaf = %q, want assistant-final", got)
	}
	if got := piCompletedTurnLeaf(state, "missing"); got != "" {
		t.Fatalf("missing legacy checkpoint resolved to %q", got)
	}
}

func TestPiAgentExactForkRetainsCompletedTurnLeaf(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-pi.sh")
	script := `#!/bin/sh
session=parent-session
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$0.calls"
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  case "$line" in
    *nexbox-fork-at-entry*)
      session=child-session
      printf '{"id":"%s","type":"response","command":"prompt","success":true}\n' "$id"
      ;;
    *'"type":"get_state"'*)
      printf '{"id":"%s","type":"response","command":"get_state","success":true,"data":{"sessionId":"%s"}}\n' "$id" "$session"
      ;;
    *'"type":"prompt"'*)
      printf '{"id":"%s","type":"response","command":"prompt","success":true}\n' "$id"
      printf '%s\n' '{"type":"agent_settled"}'
      ;;
    *'"type":"get_entries"'*)
      printf '{"id":"%s","type":"response","command":"get_entries","success":true,"data":{"entries":[],"leafId":"new-assistant-leaf"}}\n' "$id"
      ;;
  esac
done
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	result, err := (&PiAgent{CLIPath: path}).SendMessage(context.Background(), Request{
		WorkDir: dir,
		Message: "continue from the completed turn",
		Fork: &ForkContext{
			SessionID:  "parent-session",
			Checkpoint: piLeafCheckpointPrefix + "assistant-leaf",
		},
	}, func(StreamEvent) {})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if result.SessionID != "child-session" || result.Checkpoint != piLeafCheckpointPrefix+"new-assistant-leaf" {
		t.Fatalf("result = %+v", result)
	}
	calls, err := os.ReadFile(path + ".calls")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), `/nexbox-fork-at-entry assistant-leaf`) {
		t.Fatalf("exact fork command missing: %s", calls)
	}
	if strings.Contains(string(calls), `"type":"fork"`) {
		t.Fatalf("exact checkpoint used legacy fork command: %s", calls)
	}
}

func TestPiAgentSendToSessionAllowsFullTurn(t *testing.T) {
	// Pi loads this embedded extension . Unlike normal
	// MCP calls, send_to_session must survive a full target-session turn.
	source := string(piNexboxExtension)
	for _, want := range []string{
		"const sendToSessionTimeout = 10 * 60 * 60 * 1000;",
		`spec.key === "nexbox" && tool.name === "send_to_session" ? sendToSessionTimeout : defaultMCPTimeout`,
		"signal: timeout(signal, timeoutMs)",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("Pi MCP extension lost the long send_to_session timeout: missing %q", want)
		}
	}
}

func TestPiAgentRPCIntegration(t *testing.T) {
	if os.Getenv("PI_INTEGRATION") == "" {
		t.Skip("set PI_INTEGRATION=1 to run against a configured Pi provider")
	}
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("OPENROUTER_API_KEY is required")
	}

	workDir := t.TempDir()
	a := NewPiAgent()
	model := "nvidia/nemotron-3.5-lightning:free"
	var contextWindow int
	run := func(req Request) (Result, string, error) {
		var mu sync.Mutex
		var text string
		result, err := a.SendMessage(context.Background(), req, func(event StreamEvent) {
			if value, ok := event.Metadata["contextWindow"].(int); event.Type == EventContextUsage && ok {
				mu.Lock()
				contextWindow = value
				mu.Unlock()
			}
			if event.Type == EventTextDelta {
				mu.Lock()
				text += event.Content
				mu.Unlock()
			}
		})
		mu.Lock()
		defer mu.Unlock()
		return result, text, err
	}

	first, firstText, err := run(Request{
		WorkDir: workDir,
		Model:   model,
		Effort:  "low",
		Message: "Reply with exactly: PI_RPC_FIRST",
	})
	if err != nil {
		t.Fatalf("first Pi RPC turn: %v", err)
	}
	if first.SessionID == "" || first.Checkpoint == "" || !strings.Contains(firstText, "PI_RPC_FIRST") {
		t.Fatalf("first result=%+v text=%q", first, firstText)
	}
	if contextWindow <= 0 {
		t.Fatal("Pi did not report its context window")
	}

	second, secondText, err := run(Request{
		WorkDir:   workDir,
		SessionID: first.SessionID,
		Model:     model,
		Effort:    "low",
		Message:   "Reply with exactly: PI_RPC_RESUMED",
	})
	if err != nil {
		t.Fatalf("resumed Pi RPC turn: %v", err)
	}
	if second.SessionID != first.SessionID || second.Checkpoint == "" || !strings.Contains(secondText, "PI_RPC_RESUMED") {
		t.Fatalf("resumed result=%+v text=%q", second, secondText)
	}

	forked, forkedText, err := run(Request{
		WorkDir: workDir,
		Model:   model,
		Effort:  "low",
		Message: "Reply with exactly: PI_RPC_FORKED",
		Fork: &ForkContext{
			SessionID:  first.SessionID,
			Checkpoint: first.Checkpoint,
		},
	})
	if err != nil {
		t.Fatalf("forked Pi RPC turn: %v", err)
	}
	if forked.SessionID == "" || forked.SessionID == first.SessionID || forked.Checkpoint == "" || !strings.Contains(forkedText, "PI_RPC_FORKED") {
		t.Fatalf("forked result=%+v text=%q", forked, forkedText)
	}
}

func TestPiAgentCapabilitiesIntegration(t *testing.T) {
	if os.Getenv("PI_INTEGRATION") == "" || os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("Pi integration requires OpenRouter")
	}
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	settings := `{"enabledModels":["openrouter/nvidia/nemotron-3.5-lightning:free"]}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	models, source := NewPiAgent().ListSupportedModels(context.Background())
	if source != "configured" || len(models) != 1 || models[0].ContextWindow <= 0 || len(models[0].SupportedEfforts) == 0 {
		t.Fatalf("Pi returned no model capabilities: source=%s models=%+v", source, models)
	}
}

func TestPiAgentRPCIntegrationInterject(t *testing.T) {
	if os.Getenv("PI_INTEGRATION") == "" || os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("set PI_INTEGRATION=1 with OPENROUTER_API_KEY to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	injectors := make(chan func(string) error, 1)
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := NewPiAgent().SendMessage(ctx, Request{
			WorkDir: t.TempDir(),
			Model:   "nvidia/nemotron-3.5-lightning:free",
			Effort:  "low",
			Message: "First use the bash tool to run `sleep 3`. Only then reply with exactly: PI_RPC_BASE.",
			RegisterInterject: func(inject func(string) error) {
				injectors <- inject
			},
		}, func(StreamEvent) {})
		done <- outcome{result: result, err: err}
	}()

	select {
	case inject := <-injectors:
		if err := inject("After the current work, also reply with exactly: PI_RPC_STEERED."); err != nil {
			t.Fatalf("Pi steer: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Pi never registered an interject handler")
	}
	select {
	case got := <-done:
		if got.err != nil || got.result.SessionID == "" {
			t.Fatalf("interjected turn result=%+v err=%v", got.result, got.err)
		}
	case <-ctx.Done():
		t.Fatal("interjected Pi turn did not settle")
	}
}

func TestPiAgentMCPToolsReactivatedBeforePrompt(t *testing.T) {
	// A resumed Pi transcript restores its previous active loadout after the
	// extension factory registers tools. Reconcile at the prompt boundary so
	// newly configured MCPs are available in the same turn, including forks.
	source := string(piNexboxExtension)
	for _, want := range []string{
		`const managedToolNames = new Set<string>();`,
		`pi.on("before_agent_start", () => {`,
		`pi.setActiveTools([...new Set([...pi.getActiveTools(), ...managedToolNames])]);`,
		"managedToolNames.add(`mcp__${spec.key}__${tool.name}`);",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("Pi MCP tools must be reactivated after transcript restoration: missing %q", want)
		}
	}
}
