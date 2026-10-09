package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFakeCodexSubAgentServer replays the ordering observed from
// codex-cli 0.147.0: the root announces subAgentActivity, the child streams a
// complete turn on its own thread id, and only then does the root finish.
func writeFakeCodexSubAgentServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-codex-subagent.sh")
	script := `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"id":%s,"result":{}}\n' "$id"
      ;;
    *'"method":"thread/start"'*)
      printf '%s\n' '{"method":"thread/started","params":{"thread":{"id":"root-thread","parentThreadId":null}}}'
      printf '{"id":%s,"result":{"thread":{"id":"root-thread"}}}\n' "$id"
      ;;
    *'"method":"turn/start"'*)
      printf '{"id":%s,"result":{"turn":{"id":"root-turn"}}}\n' "$id"
      printf '%s\n' '{"method":"turn/started","params":{"threadId":"root-thread","turn":{"id":"root-turn","status":"inProgress"}}}'
      printf '%s\n' '{"method":"item/started","params":{"threadId":"root-thread","turnId":"root-turn","item":{"type":"userMessage","id":"root-user","content":[]}}}'
      printf '%s\n' '{"method":"item/completed","params":{"threadId":"root-thread","turnId":"root-turn","item":{"type":"userMessage","id":"root-user","content":[]}}}'
      printf '%s\n' '{"method":"item/started","params":{"threadId":"root-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"activity-1","agentThreadId":"child-thread","agentPath":"/root/protocol_child","kind":"started"}}}'
      printf '%s\n' '{"method":"item/completed","params":{"threadId":"root-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"activity-1","agentThreadId":"child-thread","agentPath":"/root/protocol_child","kind":"started"}}}'
      printf '%s\n' '{"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"child-turn","status":"inProgress"}}}'
      printf '%s\n' '{"method":"item/started","params":{"threadId":"child-thread","turnId":"child-turn","item":{"type":"reasoning","id":"child-reasoning","summary":[],"content":[]}}}'
      printf '%s\n' '{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"child-thread","turnId":"child-turn","itemId":"child-reasoning","delta":"CHILD_THINKING"}}'
      printf '%s\n' '{"method":"item/completed","params":{"threadId":"child-thread","turnId":"child-turn","item":{"type":"reasoning","id":"child-reasoning","summary":["CHILD_THINKING"],"content":[]}}}'
      printf '%s\n' '{"method":"item/started","params":{"threadId":"child-thread","turnId":"child-turn","item":{"type":"commandExecution","id":"child-command","command":"pwd","status":"inProgress"}}}'
      printf '%s\n' '{"method":"item/completed","params":{"threadId":"child-thread","turnId":"child-turn","item":{"type":"commandExecution","id":"child-command","command":"pwd","status":"completed","aggregatedOutput":"/workspace\n","exitCode":0}}}'
      printf '%s\n' '{"method":"item/started","params":{"threadId":"child-thread","turnId":"child-turn","item":{"type":"agentMessage","id":"child-message","phase":"final_answer","text":""}}}'
      printf '%s\n' '{"method":"item/agentMessage/delta","params":{"threadId":"child-thread","turnId":"child-turn","itemId":"child-message","delta":"CHILD_ONLY"}}'
      printf '%s\n' '{"method":"item/completed","params":{"threadId":"child-thread","turnId":"child-turn","item":{"type":"agentMessage","id":"child-message","phase":"final_answer","text":"CHILD_ONLY"}}}'
      printf '%s\n' '{"method":"thread/tokenUsage/updated","params":{"threadId":"child-thread","turnId":"child-turn","tokenUsage":{"last":{"cachedInputTokens":0,"inputTokens":99,"outputTokens":1,"reasoningOutputTokens":0,"totalTokens":100},"total":{"cachedInputTokens":0,"inputTokens":99,"outputTokens":1,"reasoningOutputTokens":0,"totalTokens":100}}}}'
      printf '%s\n' '{"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"child-turn","status":"completed"}}}'
      sleep 0.05
      printf '%s\n' '{"method":"item/started","params":{"threadId":"root-thread","turnId":"root-turn","item":{"type":"collabAgentToolCall","id":"wait-1","tool":"wait","status":"inProgress","senderThreadId":"root-thread","receiverThreadIds":[],"agentsStates":{}}}}'
      printf '%s\n' '{"method":"item/completed","params":{"threadId":"root-thread","turnId":"root-turn","item":{"type":"collabAgentToolCall","id":"wait-1","tool":"wait","status":"completed","senderThreadId":"root-thread","receiverThreadIds":[],"agentsStates":{}}}}'
      printf '%s\n' '{"method":"item/started","params":{"threadId":"root-thread","turnId":"root-turn","item":{"type":"agentMessage","id":"root-message","phase":"final_answer","text":""}}}'
      printf '%s\n' '{"method":"item/agentMessage/delta","params":{"threadId":"root-thread","turnId":"root-turn","itemId":"root-message","delta":"PARENT_ONLY"}}'
      printf '%s\n' '{"method":"item/completed","params":{"threadId":"root-thread","turnId":"root-turn","item":{"type":"agentMessage","id":"root-message","phase":"final_answer","text":"PARENT_ONLY"}}}'
      printf '%s\n' '{"method":"turn/completed","params":{"threadId":"root-thread","turn":{"id":"root-turn","status":"completed"}}}'
      ;;
  esac
done
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex sub-agent server: %v", err)
	}
	return scriptPath
}

func TestCodexAgentRoutesSubAgentEventsByThread(t *testing.T) {
	a := NewCodexAgent()
	a.CLIPath = writeFakeCodexSubAgentServer(t)
	a.Verbose = false

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var events []StreamEvent
	result, err := a.SendMessage(ctx, Request{
		ConversationID: "codex-subagent-routing",
		WorkDir:        t.TempDir(),
		Message:        "run the protocol fixture",
	}, func(event StreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if result.SessionID != "root-thread" {
		t.Fatalf("SessionID = %q, want root-thread", result.SessionID)
	}
	if result.Checkpoint != "root-turn" {
		t.Fatalf("Checkpoint = %q, want root-turn", result.Checkpoint)
	}

	var text strings.Builder
	doneCount := 0
	agentStarts := 0
	subtaskStarts := 0
	subtaskDone := 0
	childToolStarts := 0
	childToolResults := 0
	for _, event := range events {
		switch event.Type {
		case EventTextDelta:
			text.WriteString(event.Content)
		case EventDone:
			doneCount++
		case EventThinking:
			if strings.Contains(event.Content, "CHILD_THINKING") {
				t.Fatalf("child reasoning leaked into root events: %+v", event)
			}
		case EventToolStart:
			switch event.ToolName {
			case "Agent":
				agentStarts++
				if event.ToolID != "activity-1" || event.ParentToolID != "" {
					t.Fatalf("unexpected Agent start: %+v", event)
				}
			case "Bash":
				childToolStarts++
				if event.ToolID != "child-command" || event.ParentToolID != "activity-1" {
					t.Fatalf("child command was not nested under Agent: %+v", event)
				}
			case "subAgentActivity", "collab/wait", "userMessage":
				t.Fatalf("protocol bookkeeping leaked as a tool card: %+v", event)
			}
		case EventToolResult:
			if event.ToolID == "child-command" {
				childToolResults++
				if event.ParentToolID != "activity-1" {
					t.Fatalf("child command result was not nested under Agent: %+v", event)
				}
			}
		case EventSubTaskStart:
			subtaskStarts++
		case EventSubTaskDone:
			subtaskDone++
			if event.Content != "CHILD_ONLY" {
				t.Fatalf("subtask summary = %q, want CHILD_ONLY", event.Content)
			}
		}
	}

	if got := text.String(); got != "PARENT_ONLY" {
		t.Fatalf("root text = %q, want PARENT_ONLY", got)
	}
	if doneCount != 1 {
		t.Fatalf("done events = %d, want 1", doneCount)
	}
	if agentStarts != 1 || subtaskStarts != 1 || subtaskDone != 1 {
		t.Fatalf("Agent lifecycle counts = start:%d subtask_start:%d subtask_done:%d, want 1/1/1", agentStarts, subtaskStarts, subtaskDone)
	}
	if childToolStarts != 1 || childToolResults != 1 {
		t.Fatalf("child tool lifecycle counts = start:%d result:%d, want 1/1", childToolStarts, childToolResults)
	}
	for _, event := range events {
		if strings.Contains(event.Content, "CHILD_ONLY") && event.Type == EventTextDelta {
			t.Fatalf("child answer leaked into root transcript: %+v", event)
		}
		if event.Type == EventContextUsage {
			t.Fatalf("child token usage leaked into root context usage: %+v", event)
		}
	}
}

// TestCodexIntegrationSubAgentRouting exercises the same contract against the
// installed Codex app-server. It is opt-in because it requires authentication
// and makes a real model request.
//
// Run with:
//
//	CODEX_INTEGRATION=1 go test -v -run TestCodexIntegrationSubAgentRouting -timeout 240s ./agent/
func TestCodexIntegrationSubAgentRouting(t *testing.T) {
	if os.Getenv("CODEX_INTEGRATION") == "" {
		t.Skip("Set CODEX_INTEGRATION=1 to run this test (requires codex CLI installed & authenticated)")
	}

	a := NewCodexAgent()
	a.Verbose = false
	ctx, cancel := context.WithTimeout(context.Background(), 210*time.Second)
	defer cancel()

	var events []StreamEvent
	result, err := a.SendMessage(ctx, Request{
		ConversationID: "codex-live-subagent-routing",
		WorkDir:        t.TempDir(),
		Message: "For an integration test, spawn exactly one subagent. Tell the child to run only `pwd`, then reply exactly CHILD_ONLY. " +
			"Wait for it to finish. Then reply exactly PARENT_ONLY. Do nothing else.",
	}, func(event StreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if result.SessionID == "" || result.Checkpoint == "" {
		t.Fatalf("missing durable root result: %+v", result)
	}

	var text strings.Builder
	doneCount := 0
	agentToolID := ""
	nestedCommand := false
	for _, event := range events {
		switch event.Type {
		case EventTextDelta:
			text.WriteString(event.Content)
		case EventDone:
			doneCount++
		case EventToolStart:
			if event.ToolName == "Agent" && event.ParentToolID == "" {
				agentToolID = event.ToolID
			}
			if event.ToolName == "Bash" && event.ParentToolID != "" {
				nestedCommand = true
			}
		}
	}
	rootText := text.String()
	if strings.Contains(rootText, "CHILD_ONLY") {
		t.Fatalf("child answer leaked into root transcript: %q", rootText)
	}
	if !strings.Contains(rootText, "PARENT_ONLY") {
		t.Fatalf("root transcript missing parent answer: %q", rootText)
	}
	if doneCount != 1 {
		t.Fatalf("done events = %d, want 1", doneCount)
	}
	if agentToolID == "" {
		t.Fatal("live protocol did not produce a top-level Agent card")
	}
	if !nestedCommand {
		t.Fatalf("live child command was not nested under Agent; events=%+v", events)
	}
}
