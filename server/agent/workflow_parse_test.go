package agent

import (
	"encoding/json"
	"testing"
)

// Verifies the StreamMessage struct tags retain the dynamic-workflow fields
// from a real `claude` task_progress line (shape captured from a live run).
// These fields used to be dropped silently by json.Unmarshal.
func TestStreamMessageParsesWorkflowFields(t *testing.T) {
	line := `{"type":"system","subtype":"task_progress","task_id":"wxx86l56h",` +
		`"tool_use_id":"toolu_013Yyz692U42wK2Zris1Aae8","task_type":"local_workflow",` +
		`"workflow_name":"wf-probe","description":"Alpha: a2","last_tool_name":"a2",` +
		`"workflow_progress":[{"type":"workflow_phase","index":1,"title":"Alpha"},` +
		`{"type":"workflow_agent","index":2,"label":"a2","phaseIndex":1,"state":"progress","tokens":10567,"toolCalls":0}]}`

	var msg StreamMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Subtype != "task_progress" {
		t.Errorf("subtype: got %q", msg.Subtype)
	}
	if msg.ToolUseID != "toolu_013Yyz692U42wK2Zris1Aae8" {
		t.Errorf("tool_use_id not parsed into ToolUseID: got %q", msg.ToolUseID)
	}
	if msg.TaskType != "local_workflow" {
		t.Errorf("task_type not parsed: got %q", msg.TaskType)
	}
	if msg.WorkflowName != "wf-probe" {
		t.Errorf("workflow_name not parsed: got %q", msg.WorkflowName)
	}
	if len(msg.WorkflowProgress) == 0 {
		t.Fatal("workflow_progress not retained")
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal(msg.WorkflowProgress, &entries); err != nil {
		t.Fatalf("workflow_progress not valid JSON array: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("want 2 workflow_progress entries, got %d", len(entries))
	}
}
