package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCodexFileChangeDiffArrivesOnlyAtCompletion exercises the sequencing
// review flagged: codex-cli can send item/started for a "fileChange" item
// before the patch's diffs are known (empty Changes), then fill them in by
// item/completed. Without EventToolInputReplace, that diff data would never
// reach the tool_use's Input at all. This drives emitCodexItemStarted /
// emitCodexItemCompleted directly (no RPC mock) to assert the emitted event
// sequence and payload.
func TestCodexFileChangeDiffArrivesOnlyAtCompletion(t *testing.T) {
	started := codexThreadItem{
		ID:      "item-1",
		Type:    "fileChange",
		Changes: nil, // not yet known at item/started
	}
	completed := codexThreadItem{
		ID:   "item-1",
		Type: "fileChange",
		Changes: []codexFileChange{
			{Path: "a.txt", KindRaw: json.RawMessage(`"update"`), Diff: "@@ -1,1 +1,1 @@\n-old\n+new\n"},
		},
	}

	var events []StreamEvent
	record := func(e StreamEvent) { events = append(events, e) }

	emitCodexItemStarted(started, record)
	emitCodexItemCompleted(completed, false, false, record)

	// item/started -> tool_start (empty changes); item/completed -> both a
	// tool_input_replace (non-empty Changes) and its own tool_result.
	if len(events) != 3 {
		t.Fatalf("expected 3 events (start, input-replace, result), got %d: %+v", len(events), events)
	}

	if events[0].Type != EventToolStart || events[0].ToolID != "item-1" {
		t.Fatalf("event 0 = %+v, want tool_start for item-1", events[0])
	}
	var startedChanges struct {
		Changes []map[string]string `json:"changes"`
	}
	if err := json.Unmarshal([]byte(events[0].Input), &startedChanges); err != nil {
		t.Fatalf("started input not valid JSON: %v", err)
	}
	if len(startedChanges.Changes) != 0 {
		t.Fatalf("expected no changes at item/started, got %+v", startedChanges.Changes)
	}

	if events[1].Type != EventToolInputReplace {
		t.Fatalf("event 1 = %+v, want tool_input_replace", events[1])
	}
	if events[1].ToolID != "item-1" {
		t.Fatalf("tool_input_replace has wrong ToolID: %+v", events[1])
	}
	var replaced struct {
		Changes []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
			Diff string `json:"diff"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(events[1].Input), &replaced); err != nil {
		t.Fatalf("replace input not valid JSON: %v", err)
	}
	if len(replaced.Changes) != 1 || replaced.Changes[0].Path != "a.txt" {
		t.Fatalf("unexpected replaced changes: %+v", replaced.Changes)
	}
	if !strings.Contains(replaced.Changes[0].Diff, "-old") || !strings.Contains(replaced.Changes[0].Diff, "+new") {
		t.Fatalf("expected diff content in replaced changes, got %+v", replaced.Changes[0])
	}
	if events[2].Type != EventToolResult || events[2].ToolID != "item-1" {
		t.Fatalf("event 2 = %+v, want tool_result for item-1", events[2])
	}
}

// TestCodexFileChangeCompletionSkipsReplaceWhenStillEmpty ensures a
// completion with no Changes at all (some other item type reusing this
// path, or a genuinely empty patch) doesn't emit a pointless/empty replace
// that could clobber a good item/started payload.
func TestCodexFileChangeCompletionSkipsReplaceWhenStillEmpty(t *testing.T) {
	completed := codexThreadItem{ID: "item-2", Type: "fileChange", Changes: nil}
	var events []StreamEvent
	emitCodexItemCompleted(completed, false, false, func(e StreamEvent) { events = append(events, e) })
	for _, e := range events {
		if e.Type == EventToolInputReplace {
			t.Fatalf("did not expect a tool_input_replace when Changes is still empty, got %+v", e)
		}
	}
}
// TestCodexParentedCallbackTagsToolInputReplace verifies the connective
// tissue between emitCodexItemCompleted's re-emit and sub-agent routing:
// codexParentedCallback must stamp ParentToolID on EventToolInputReplace
// (not just EventToolStart/EventToolResult), otherwise a sub-agent's
// FileChange diff update loses its parent association server-side, before
// it even reaches the frontend's nestedToolIdsRef routing.
func TestCodexParentedCallbackTagsToolInputReplace(t *testing.T) {
	var captured StreamEvent
	cb := codexParentedCallback("parent-task-1", func(e StreamEvent) { captured = e })

	cb(StreamEvent{Type: EventToolInputReplace, ToolID: "child-1", Input: "{}"})
	if captured.ParentToolID != "parent-task-1" {
		t.Fatalf("expected ParentToolID set on tool_input_replace, got %+v", captured)
	}

	// With no parent (main-thread call), codexParentedCallback must be a
	// pure passthrough — confirms the "no parent" half of this codepath too.
	var passthrough StreamEvent
	cbNoParent := codexParentedCallback("", func(e StreamEvent) { passthrough = e })
	cbNoParent(StreamEvent{Type: EventToolInputReplace, ToolID: "child-2", Input: "{}"})
	if passthrough.ParentToolID != "" {
		t.Fatalf("expected no ParentToolID without a parent, got %+v", passthrough)
	}
}
