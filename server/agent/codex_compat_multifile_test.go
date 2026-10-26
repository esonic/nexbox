package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCodexCompatMultiFilePatchSurvivesOutputDeltaAndEnd exercises the exact
// wiring bug review flagged: a compat-mode (older codex-cli) multi-file
// apply_patch drives patch_apply_begin -> item/fileChange/outputDelta ->
// patch_apply_end, and the outputDelta/end handlers used to always try to
// infer a single `path` and overwrite the tool_use's Input with
// {"path": "..."}, silently discarding the multi-file `changes` array
// codexPatchToolStart had already built at begin. This drives the real
// SendMessage path (via a fake codex-cli app-server) rather than calling
// helpers directly, so it catches wiring bugs the helper-level tests can't.
func TestCodexCompatMultiFilePatchSurvivesOutputDeltaAndEnd(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-codex.sh")

	patch := "*** Begin Patch\n" +
		"*** Update File: a.txt\n@@\n-old a\n+new a\n" +
		"*** Add File: b.txt\n+line one\n" +
		"*** End Patch\n"

	beginLine := mustMarshalJSONRPCNotification(t, "codex/event/patch_apply_begin", map[string]interface{}{
		"call_id": "patch-1",
		"diff":    patch,
	})
	// Deliberately a shape extractPatchPathFromResultText *can* parse into a
	// single path ("M a.txt") — this is what makes the outputDelta/end
	// path-inference logic actually fire and attempt to override the
	// tool_use's input; a delta that doesn't parse into any path wouldn't
	// exercise (and so couldn't catch a regression in) that code at all.
	deltaLine := mustMarshalJSONRPCNotification(t, "item/fileChange/outputDelta", map[string]interface{}{
		"call_id": "patch-1",
		"delta":   "M a.txt",
	})
	endLine := mustMarshalJSONRPCNotification(t, "codex/event/patch_apply_end", map[string]interface{}{
		"call_id": "patch-1",
		"success": true,
		"message": "Applied",
	})
	turnCompletedLine := mustMarshalJSONRPCNotification(t, "turn/completed", map[string]interface{}{
		"threadId": "thread-new",
		"turn":     map[string]interface{}{"id": "turn-1", "status": "completed"},
	})

	script := fmt.Sprintf(`#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%%s' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"id":%%s,"result":{}}\n' "$id"
      ;;
    *'"method":"thread/start"'*)
      printf '{"id":%%s,"result":{"thread":{"id":"thread-new"}}}\n' "$id"
      ;;
    *'"method":"turn/start"'*)
      printf '{"id":%%s,"result":{"turn":{"id":"turn-1"}}}\n' "$id"
      cat <<'BEGIN_EVT'
%s
BEGIN_EVT
      cat <<'DELTA_EVT'
%s
DELTA_EVT
      cat <<'END_EVT'
%s
END_EVT
      cat <<'TURN_EVT'
%s
TURN_EVT
      ;;
  esac
done
`, beginLine, deltaLine, endLine, turnCompletedLine)

	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	a := NewCodexAgent()
	a.CLIPath = scriptPath
	a.Verbose = false
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var events []StreamEvent
	_, err := a.SendMessage(ctx, Request{
		ConversationID: "nexbox-compat-multifile",
		WorkDir:        t.TempDir(),
		Message:        "edit two files",
	}, func(e StreamEvent) { events = append(events, e) })
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	var starts []StreamEvent
	for _, e := range events {
		if e.Type == EventToolStart {
			starts = append(starts, e)
		}
	}
	if len(starts) == 0 {
		t.Fatalf("expected at least one tool_start event, got none (all events: %+v)", events)
	}
	// Every tool_start for this call must be the FileChange shape with both
	// files still present — none of them should have been overwritten with
	// a single {"path": "..."} by the outputDelta/end path-inference logic.
	for _, e := range starts {
		if e.ToolName != "FileChange" {
			t.Fatalf("expected ToolName=FileChange, got %q (input=%s)", e.ToolName, e.Input)
		}
		var decoded struct {
			Changes []struct {
				Path string `json:"path"`
			} `json:"changes"`
		}
		if err := json.Unmarshal([]byte(e.Input), &decoded); err != nil {
			t.Fatalf("tool_start input not valid JSON: %v (%s)", err, e.Input)
		}
		if len(decoded.Changes) != 2 {
			t.Fatalf("expected 2 files preserved in tool_start input, got %d: %s", len(decoded.Changes), e.Input)
		}
	}
}

// TestCodexCompatMultiFilePatchResultHasNoSingleFileDiff guards against a
// second way the multi-file case can regress: even once tool_start carries
// every file, patch_apply_end could still resolve a single `path` from the
// result message (e.g. "M a.txt") and attach *that one file's* diff to the
// call's tool_result.Diff, which ToolCallCard prioritizes over per-entry
// diffs — making an expanded FileChange card look like it only touched one
// file, even though its input correctly lists both.
func TestCodexCompatMultiFilePatchResultHasNoSingleFileDiff(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-codex.sh")

	patch := "*** Begin Patch\n" +
		"*** Update File: a.txt\n@@\n-old a\n+new a\n" +
		"*** Add File: b.txt\n+line one\n" +
		"*** End Patch\n"

	beginLine := mustMarshalJSONRPCNotification(t, "codex/event/patch_apply_begin", map[string]interface{}{
		"call_id": "patch-1",
		"diff":    patch,
	})
	// A result message that DOES resolve to a single path via
	// extractPatchPathFromResultText — this is what makes the buggy
	// single-file Diff attachment fire at all; a message that doesn't parse
	// into any path wouldn't exercise (or catch a regression in) this code.
	endLine := mustMarshalJSONRPCNotification(t, "codex/event/patch_apply_end", map[string]interface{}{
		"call_id": "patch-1",
		"success": true,
		"message": "M a.txt",
	})
	turnCompletedLine := mustMarshalJSONRPCNotification(t, "turn/completed", map[string]interface{}{
		"threadId": "thread-new",
		"turn":     map[string]interface{}{"id": "turn-1", "status": "completed"},
	})

	script := fmt.Sprintf(`#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%%s' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"id":%%s,"result":{}}\n' "$id"
      ;;
    *'"method":"thread/start"'*)
      printf '{"id":%%s,"result":{"thread":{"id":"thread-new"}}}\n' "$id"
      ;;
    *'"method":"turn/start"'*)
      printf '{"id":%%s,"result":{"turn":{"id":"turn-1"}}}\n' "$id"
      cat <<'BEGIN_EVT'
%s
BEGIN_EVT
      cat <<'END_EVT'
%s
END_EVT
      cat <<'TURN_EVT'
%s
TURN_EVT
      ;;
  esac
done
`, beginLine, endLine, turnCompletedLine)

	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	a := NewCodexAgent()
	a.CLIPath = scriptPath
	a.Verbose = false
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var events []StreamEvent
	_, err := a.SendMessage(ctx, Request{
		ConversationID: "nexbox-compat-multifile-result",
		WorkDir:        t.TempDir(),
		Message:        "edit two files",
	}, func(e StreamEvent) { events = append(events, e) })
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	found := false
	for _, e := range events {
		if e.Type != EventToolResult {
			continue
		}
		found = true
		if e.Diff != "" {
			t.Fatalf("expected empty tool_result.Diff for a multi-file FileChange call (per-file diffs live in the input instead), got %q", e.Diff)
		}
	}
	if !found {
		t.Fatalf("expected at least one tool_result event, got none (all events: %+v)", events)
	}
}

func mustMarshalJSONRPCNotification(t *testing.T, method string, params map[string]interface{}) string {
	t.Helper()
	b, err := json.Marshal(map[string]interface{}{"method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal notification %s: %v", method, err)
	}
	return string(b)
}
