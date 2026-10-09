package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClaudeAgentRoutesSubAgentResultToCard(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"S-agent","model":"claude-sonnet-4-6"}`,
		`{"type":"assistant","session_id":"S-agent","uuid":"u1","message":{"role":"assistant","id":"m1","content":[{"type":"tool_use","id":"toolu_agent","name":"Task","input":{"description":"protocol check","prompt":"run pwd"}}]}}`,
		`{"type":"system","subtype":"task_started","session_id":"S-agent","task_id":"task-1","task_type":"local_agent","tool_use_id":"toolu_agent","description":"protocol check","uuid":"u2"}`,
		`{"type":"user","session_id":"S-agent","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_agent","content":"agent launched"}]}}`,
		`{"type":"assistant","session_id":"S-agent","uuid":"u3","parent_tool_use_id":"toolu_agent","message":{"role":"assistant","id":"child-1","content":[{"type":"tool_use","id":"child-bash","name":"Bash","input":{"command":"pwd"}}]}}`,
		`{"type":"user","session_id":"S-agent","parent_tool_use_id":"toolu_agent","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"child-bash","content":"/workspace"}]}}`,
		`{"type":"assistant","session_id":"S-agent","uuid":"u4","parent_tool_use_id":"toolu_agent","message":{"role":"assistant","id":"child-2","content":[{"type":"text","text":"CLAUDE_CHILD_ONLY"}]}}`,
		`{"type":"system","subtype":"task_updated","session_id":"S-agent","task_id":"task-1","patch":{"status":"completed"},"uuid":"u5"}`,
		`{"type":"system","subtype":"task_notification","session_id":"S-agent","task_id":"task-1","status":"completed","summary":"generic summary","uuid":"u6"}`,
		`{"type":"assistant","session_id":"S-agent","uuid":"u7","message":{"role":"assistant","id":"m2","content":[{"type":"text","text":"CLAUDE_PARENT_ONLY"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"num_turns":2,"session_id":"S-agent","duration_ms":200}`,
	}

	a := NewClaudeAgent()
	a.CLIPath = writeFakeCLI(t, lines)
	a.DataDir = t.TempDir()
	a.Verbose = false

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var events []StreamEvent
	result, err := a.SendMessage(ctx, Request{
		ConversationID: "claude-subagent-routing",
		WorkDir:        t.TempDir(),
		Message:        "run the fixture",
	}, func(event StreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if result.SessionID != "S-agent" {
		t.Fatalf("SessionID = %q, want S-agent", result.SessionID)
	}

	var rootText strings.Builder
	startCount := 0
	doneCount := 0
	nestedStart := 0
	nestedResult := 0
	for _, event := range events {
		switch event.Type {
		case EventTextDelta:
			rootText.WriteString(event.Content)
		case EventSubTaskStart:
			startCount++
			if event.Metadata["task_id"] != "task-1" || event.Metadata["tool_use_id"] != "toolu_agent" {
				t.Fatalf("subtask start IDs were not preserved: %+v", event)
			}
		case EventSubTaskDone:
			doneCount++
			if event.Content != "CLAUDE_CHILD_ONLY" || event.IsError {
				t.Fatalf("subtask result = %+v, want child final text", event)
			}
			if event.Metadata["tool_use_id"] != "toolu_agent" {
				t.Fatalf("subtask completion lost tool_use_id: %+v", event)
			}
		case EventToolStart:
			if event.ToolID == "child-bash" {
				nestedStart++
				if event.ParentToolID != "toolu_agent" {
					t.Fatalf("child tool was not nested: %+v", event)
				}
			}
		case EventToolResult:
			if event.ToolID == "child-bash" {
				nestedResult++
				if event.ParentToolID != "toolu_agent" {
					t.Fatalf("child tool result was not nested: %+v", event)
				}
			}
		}
	}

	if got := rootText.String(); got != "CLAUDE_PARENT_ONLY" {
		t.Fatalf("root text = %q, want CLAUDE_PARENT_ONLY", got)
	}
	if startCount != 1 || doneCount != 1 {
		t.Fatalf("subtask lifecycle counts = start:%d done:%d, want 1/1", startCount, doneCount)
	}
	if nestedStart != 1 || nestedResult != 1 {
		t.Fatalf("nested tool lifecycle counts = start:%d result:%d, want 1/1", nestedStart, nestedResult)
	}
}

// TestClaudeIntegrationSubAgentRouting exercises the contract against the
// installed Claude CLI. It is opt-in because it requires authentication and
// makes a real model request.
//
// Run with:
//
//	CLAUDE_INTEGRATION=1 go test -v -run TestClaudeIntegrationSubAgentRouting -timeout 240s ./agent/
func TestClaudeIntegrationSubAgentRouting(t *testing.T) {
	if os.Getenv("CLAUDE_INTEGRATION") == "" {
		t.Skip("Set CLAUDE_INTEGRATION=1 to run this test (requires claude CLI installed & authenticated)")
	}

	a := NewClaudeAgent()
	a.DataDir = t.TempDir()
	a.Verbose = false
	ctx, cancel := context.WithTimeout(context.Background(), 210*time.Second)
	defer cancel()

	var events []StreamEvent
	result, err := a.SendMessage(ctx, Request{
		ConversationID: "claude-live-subagent-routing",
		WorkDir:        t.TempDir(),
		Message: "For an integration test, spawn exactly one sub-agent. Tell the child to run only `pwd`, then reply exactly CLAUDE_CHILD_ONLY. " +
			"Wait for it to finish. Then reply exactly CLAUDE_PARENT_ONLY. Do nothing else.",
	}, func(event StreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if result.SessionID == "" || result.Checkpoint == "" {
		t.Fatalf("missing durable root result: %+v", result)
	}

	var rootText strings.Builder
	agentToolID := ""
	nestedCommand := false
	childResult := ""
	for _, event := range events {
		switch event.Type {
		case EventTextDelta:
			rootText.WriteString(event.Content)
		case EventToolStart:
			if (event.ToolName == "Agent" || event.ToolName == "Task") && event.ParentToolID == "" {
				agentToolID = event.ToolID
			}
			if event.ToolName == "Bash" && event.ParentToolID != "" {
				nestedCommand = true
			}
		case EventSubTaskDone:
			if strings.Contains(event.Content, "CLAUDE_CHILD_ONLY") {
				childResult = event.Content
			}
		}
	}
	root := rootText.String()
	if strings.Contains(root, "CLAUDE_CHILD_ONLY") {
		t.Fatalf("child answer leaked into root transcript: %q", root)
	}
	if !strings.Contains(root, "CLAUDE_PARENT_ONLY") {
		t.Fatalf("root transcript missing parent answer: %q", root)
	}
	if agentToolID == "" {
		t.Fatal("live protocol did not produce a top-level Agent card")
	}
	if !nestedCommand {
		t.Fatalf("live child command was not nested under Agent; events=%+v", events)
	}
	if childResult == "" {
		t.Fatalf("live child final result was not routed to subtask_done; events=%+v", events)
	}
}
