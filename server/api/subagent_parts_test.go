package api

import "testing"

func TestRecordSubAgentLifecycle(t *testing.T) {
	parts := []MessagePart{
		{Type: "tool_use", ToolCallID: "agent-1", ToolName: "Agent", Input: `{}`, State: "running"},
		{Type: "tool_result", ToolCallID: "agent-1", Output: "agent launched"},
	}

	var changed bool
	parts, changed = recordSubAgentDescription(parts, "agent-1", "inspect protocol")
	if !changed || parts[0].Summary != "inspect protocol" {
		t.Fatalf("description was not attached: %+v", parts)
	}
	parts, changed = recordSubAgentDescription(parts, "agent-1", "inspect the full protocol prompt")
	if !changed || parts[0].Summary != "inspect the full protocol prompt" {
		t.Fatalf("later full prompt did not replace the initial Agent name: %+v", parts)
	}

	parts, changed = recordSubAgentCompletion(parts, "agent-1", "SUB_AGENT_OK", false)
	if !changed || parts[0].State != "complete" {
		t.Fatalf("Agent card was not completed: %+v", parts)
	}
	if len(parts) != 2 || parts[1].Output != "SUB_AGENT_OK" || parts[1].IsError {
		t.Fatalf("launch receipt was not replaced by final result: %+v", parts)
	}

	parts, changed = recordSubAgentCompletion(parts, "agent-1", "SUB_AGENT_OK", false)
	if changed || len(parts) != 2 {
		t.Fatalf("duplicate completion was not idempotent: changed=%v parts=%+v", changed, parts)
	}
}

func TestRecordSubAgentFailureWithoutLaunchReceipt(t *testing.T) {
	parts := []MessagePart{
		{Type: "tool_use", ToolCallID: "agent-2", ToolName: "Task", State: "running"},
		{Type: "text", Content: "main agent continued while child was running"},
	}
	parts, changed := recordSubAgentCompletion(parts, "agent-2", "child failed", true)
	if !changed || len(parts) != 3 {
		t.Fatalf("failure result not added: changed=%v parts=%+v", changed, parts)
	}
	if parts[0].State != "error" || parts[1].Type != "tool_result" || !parts[1].IsError || parts[1].Output != "child failed" {
		t.Fatalf("failure state mismatch: %+v", parts)
	}
	if parts[2].Type != "text" {
		t.Fatalf("result was not kept adjacent to Agent card: %+v", parts)
	}
}

func TestSubAgentStatusIsError(t *testing.T) {
	for _, status := range []string{"failed", "stopped", "killed"} {
		if !subAgentStatusIsError(status) {
			t.Errorf("status %q should be an error", status)
		}
	}
	for _, status := range []string{"", "completed", "running"} {
		if subAgentStatusIsError(status) {
			t.Errorf("status %q should not be an error", status)
		}
	}
}
