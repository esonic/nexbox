package api

import (
	"encoding/json"
	"sync"
	"testing"
)

// Replays the exact delta sequence captured from a real `claude` dynamic
// workflow run (wf-probe: 2 phases, agents a1/a2 parallel + b1 with one Bash
// call). The first delta is a full snapshot (with duplicate start entries),
// the rest are per-agent deltas — exactly what the stream-json protocol emits.
func TestWFSnapshotMergeRealSequence(t *testing.T) {
	deltas := []string{
		// #0 full snapshot — note a1/a2 appear twice (queued, then started).
		`[{"type":"workflow_phase","index":1,"title":"Alpha"},
		  {"type":"workflow_phase","index":2,"title":"Beta"},
		  {"type":"workflow_agent","index":1,"label":"a1","phaseIndex":1,"phaseTitle":"Alpha","model":"claude-sonnet-4-6","state":"start","promptPreview":"Reply A1"},
		  {"type":"workflow_agent","index":2,"label":"a2","phaseIndex":1,"phaseTitle":"Alpha","model":"claude-sonnet-4-6","state":"start","promptPreview":"Reply A2"},
		  {"type":"workflow_agent","index":1,"label":"a1","phaseIndex":1,"phaseTitle":"Alpha","agentId":"a7","model":"claude-sonnet-4-6","state":"start"},
		  {"type":"workflow_agent","index":2,"label":"a2","phaseIndex":1,"phaseTitle":"Alpha","agentId":"af","model":"claude-sonnet-4-6","state":"start"}]`,
		`[{"type":"workflow_agent","index":2,"label":"a2","phaseIndex":1,"state":"progress","tokens":10567,"toolCalls":0}]`,
		`[{"type":"workflow_agent","index":2,"label":"a2","phaseIndex":1,"state":"done","tokens":10567,"durationMs":1600,"resultPreview":"A2"}]`,
		`[{"type":"workflow_agent","index":1,"label":"a1","phaseIndex":1,"state":"progress","tokens":10560}]`,
		`[{"type":"workflow_agent","index":1,"label":"a1","phaseIndex":1,"state":"done","tokens":10560,"durationMs":1500,"resultPreview":"A1"},
		  {"type":"workflow_agent","index":3,"label":"b1","phaseIndex":2,"phaseTitle":"Beta","model":"claude-sonnet-4-6","state":"start","promptPreview":"Run Bash"},
		  {"type":"workflow_agent","index":3,"label":"b1","phaseIndex":2,"agentId":"a9","state":"start"}]`,
		`[{"type":"workflow_agent","index":3,"label":"b1","phaseIndex":2,"state":"progress","tokens":10632,"toolCalls":1,"lastToolName":"Bash","lastToolSummary":"echo PROBE-OK"}]`,
		`[{"type":"workflow_agent","index":3,"label":"b1","phaseIndex":2,"state":"progress","tokens":10843,"toolCalls":1,"lastToolName":"Bash"}]`,
		`[{"type":"workflow_agent","index":3,"label":"b1","phaseIndex":2,"state":"done","tokens":10843,"toolCalls":1,"lastToolName":"Bash","durationMs":5363,"resultPreview":"B1"}]`,
	}

	snap := newWFSnapshot()
	for i, d := range deltas {
		snap.merge(json.RawMessage(d))
		if snap == nil {
			t.Fatalf("nil snapshot after delta %d", i)
		}
	}

	var out struct {
		Phases []struct {
			Index int    `json:"index"`
			Title string `json:"title"`
		} `json:"phases"`
		Agents []struct {
			Index         int    `json:"index"`
			Label         string `json:"label"`
			State         string `json:"state"`
			Tokens        int    `json:"tokens"`
			ToolCalls     int    `json:"toolCalls"`
			LastToolName  string `json:"lastToolName"`
			PromptPreview string `json:"promptPreview"`
			ResultPreview string `json:"resultPreview"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(snap.snapshotJSON(), &out); err != nil {
		t.Fatalf("snapshotJSON did not unmarshal: %v", err)
	}

	// Phases: 2, sorted by index.
	if len(out.Phases) != 2 {
		t.Fatalf("want 2 phases, got %d", len(out.Phases))
	}
	if out.Phases[0].Index != 1 || out.Phases[0].Title != "Alpha" || out.Phases[1].Index != 2 || out.Phases[1].Title != "Beta" {
		t.Errorf("phases not merged/sorted as expected: %+v", out.Phases)
	}

	// Agents: 3 unique (deduped by index), sorted, all terminal "done".
	if len(out.Agents) != 3 {
		t.Fatalf("want 3 agents, got %d: %+v", len(out.Agents), out.Agents)
	}
	for i, want := range []string{"a1", "a2", "b1"} {
		if out.Agents[i].Index != i+1 || out.Agents[i].Label != want {
			t.Errorf("agent %d: want index=%d label=%s, got index=%d label=%s", i, i+1, want, out.Agents[i].Index, out.Agents[i].Label)
		}
		if out.Agents[i].State != "done" {
			t.Errorf("agent %s: want final state=done, got %q", want, out.Agents[i].State)
		}
	}

	// Field-level merge: a1's promptPreview was set only in the first snapshot
	// and must survive the later progress/done deltas that omit it.
	if out.Agents[0].PromptPreview != "Reply A1" {
		t.Errorf("a1 promptPreview lost across deltas: got %q", out.Agents[0].PromptPreview)
	}
	// b1 accumulated tool activity + final numbers.
	b1 := out.Agents[2]
	if b1.Tokens != 10843 || b1.ToolCalls != 1 || b1.LastToolName != "Bash" || b1.ResultPreview != "B1" {
		t.Errorf("b1 final state wrong: %+v", b1)
	}
}

func TestAttachWorkflowProgress(t *testing.T) {
	parts := []MessagePart{
		{Type: "text", Content: "hi"},
		{Type: "tool_use", ToolCallID: "toolu_wf", ToolName: "Workflow", State: "complete"},
	}
	var mu sync.Mutex
	dirty := false
	snapshot := json.RawMessage(`{"phases":[{"index":1,"title":"Alpha"}],"agents":[]}`)

	attachWorkflowProgress(&mu, parts, "toolu_wf", snapshot, &dirty)

	if !dirty {
		t.Error("dirty flag not set")
	}
	if len(parts[1].WorkflowProgress) == 0 {
		t.Fatal("workflow progress not attached to the Workflow part")
	}
	if string(parts[1].WorkflowProgress) != string(snapshot) {
		t.Errorf("attached snapshot mismatch: %s", parts[1].WorkflowProgress)
	}

	// No matching tool_use_id -> no-op, no panic.
	dirty2 := false
	attachWorkflowProgress(&mu, parts, "toolu_missing", snapshot, &dirty2)
	if dirty2 {
		t.Error("dirty set for a non-matching tool_use_id")
	}
}
