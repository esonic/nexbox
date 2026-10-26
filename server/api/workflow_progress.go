package api

import (
	"encoding/json"
	"sort"
	"sync"
)

// wfSnapshot accumulates a dynamic Workflow run's progress on the server.
//
// Claude streams `workflow_progress` as DELTAS over the stream-json protocol:
// the first task_progress carries a full snapshot (all phases + all agents),
// and later ones carry only the phase/agent entries that changed. We merge
// them by index into a complete snapshot so the copy we persist on the
// Workflow tool_use part — and the copy we push over the WebSocket — is always
// the full tree. That makes the UI robust to WebSocket reconnects and browser
// refreshes (a reconnecting client gets the whole tree, not a partial delta).
//
// Entries are kept as generic maps so we stay forward-compatible if Claude adds
// fields to the workflow_progress schema.
type wfSnapshot struct {
	phases  map[int]map[string]interface{}
	agents  map[int]map[string]interface{}
	status  string
	summary string
}

func newWFSnapshot() *wfSnapshot {
	return &wfSnapshot{
		phases: map[int]map[string]interface{}{},
		agents: map[int]map[string]interface{}{},
	}
}

// merge applies a workflow_progress delta array onto the snapshot. Each entry
// is upserted by its index (last write wins, field-level), which correctly
// handles both the initial full snapshot and subsequent partial deltas.
func (s *wfSnapshot) merge(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return
	}
	for _, e := range entries {
		idxF, ok := e["index"].(float64)
		if !ok {
			continue
		}
		idx := int(idxF)

		var target map[int]map[string]interface{}
		switch e["type"] {
		case "workflow_phase":
			target = s.phases
		case "workflow_agent":
			target = s.agents
		default:
			continue
		}

		cur := target[idx]
		if cur == nil {
			cur = map[string]interface{}{}
			target[idx] = cur
		}
		for k, v := range e {
			cur[k] = v
		}
	}
}

// snapshotJSON serializes the merged snapshot into the shape the frontend
// consumes: {phases:[...sorted by index], agents:[...sorted by index],
// status, summary}.
func (s *wfSnapshot) snapshotJSON() json.RawMessage {
	out := map[string]interface{}{
		"phases": sortMapByIndex(s.phases),
		"agents": sortMapByIndex(s.agents),
	}
	if s.status != "" {
		out["status"] = s.status
	}
	if s.summary != "" {
		out["summary"] = s.summary
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return b
}

func sortMapByIndex(m map[int]map[string]interface{}) []map[string]interface{} {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	out := make([]map[string]interface{}, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

// attachWorkflowProgress stores the merged snapshot on the matching Workflow
// tool_use part (by tool_use_id) so it is flushed to the DB and replayed by
// GET /messages on refresh. Called from the (serial) streaming event handler;
// takes mu because the flush goroutine reads msgParts concurrently.
func attachWorkflowProgress(mu *sync.Mutex, parts []MessagePart, toolUseID string, snapshot json.RawMessage, dirty *bool) {
	if toolUseID == "" || len(snapshot) == 0 {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for i := range parts {
		if parts[i].Type == "tool_use" && parts[i].ToolCallID == toolUseID {
			parts[i].WorkflowProgress = snapshot
			*dirty = true
			return
		}
	}
}
