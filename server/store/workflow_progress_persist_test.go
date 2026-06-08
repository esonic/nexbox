package store

import (
	"encoding/json"
	"testing"
)

// Dynamic-workflow progress must survive a browser refresh: it is persisted on
// the Workflow tool_use part and replayed by ListMessages (which backs the
// GET /messages endpoint the frontend calls on load). This exercises the real
// SQLite round-trip in the same shape chat.go uses (upfront empty assistant
// message -> UpdateMessageParts with the merged snapshot).
func TestWorkflowProgressPersistsAcrossReload(t *testing.T) {
	s := newTestDB(t)

	sess, err := s.CreateSession(CreateSessionRequest{Name: "wf", Agent: "claude", Model: "sonnet"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	msg, err := s.CreateMessage(sess.ID, "assistant", []MessagePart{}, MessageMetadata{})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	snapshot := json.RawMessage(`{"phases":[{"index":1,"title":"Alpha"}],"agents":[{"index":1,"label":"a1","state":"done","tokens":10567}]}`)
	parts := []MessagePart{
		{Type: "tool_use", ToolCallID: "toolu_wf", ToolName: "Workflow", State: "complete", WorkflowProgress: snapshot},
	}
	if err := s.UpdateMessageParts(msg.ID, parts, MessageMetadata{}); err != nil {
		t.Fatalf("UpdateMessageParts: %v", err)
	}

	// Simulate a refresh: re-read through the same path the API uses on load.
	msgs, err := s.ListMessages(sess.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}

	var found *MessagePart
	for i := range msgs {
		for j := range msgs[i].Parts {
			if msgs[i].Parts[j].ToolCallID == "toolu_wf" {
				found = &msgs[i].Parts[j]
			}
		}
	}
	if found == nil {
		t.Fatal("Workflow part not found after reload")
	}
	if len(found.WorkflowProgress) == 0 {
		t.Fatal("WorkflowProgress not persisted/replayed across reload")
	}

	var out struct {
		Phases []map[string]interface{} `json:"phases"`
		Agents []map[string]interface{} `json:"agents"`
	}
	if err := json.Unmarshal(found.WorkflowProgress, &out); err != nil {
		t.Fatalf("persisted WorkflowProgress is not valid JSON: %v", err)
	}
	if len(out.Phases) != 1 || len(out.Agents) != 1 {
		t.Errorf("snapshot shape lost across reload: %+v", out)
	}
}
