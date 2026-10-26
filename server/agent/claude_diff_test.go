package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestClaudeStreamMessageAcceptsSDKAndTranscriptToolResults(t *testing.T) {
	patch := `{"filePath":"/workspace/file.txt","structuredPatch":[{"oldStart":1,"oldLines":1,"newStart":1,"newLines":1,"lines":["-old line","+new line"]}]}`
	for _, field := range []string{"tool_use_result", "toolUseResult"} {
		t.Run(field, func(t *testing.T) {
			var message StreamMessage
			if err := json.Unmarshal([]byte(`{"type":"user","`+field+`":`+patch+`}`), &message); err != nil {
				t.Fatal(err)
			}
			got := claudeToolResultDiff(message.ToolUseResult)
			if !strings.Contains(got, "--- a//workspace/file.txt\n+++ b//workspace/file.txt\n") || !strings.Contains(got, "-old line\n+new line\n") {
				t.Fatalf("lost structured patch in %s: %q", field, got)
			}
		})
	}
	var both StreamMessage
	if err := json.Unmarshal([]byte(`{"type":"user","tool_use_result":`+patch+`,"toolUseResult":{"filePath":"wrong"}}`), &both); err != nil {
		t.Fatal(err)
	}
	if string(both.ToolUseResult) != patch {
		t.Fatalf("legacy spelling replaced SDK payload: %s", both.ToolUseResult)
	}
}

func TestClaudeAgentEmitsEditAndCreateDiffs(t *testing.T) {
	for _, parent := range []string{"", "parent-agent"} {
		t.Run("parent_"+parent, func(t *testing.T) {
			lines := []string{
				`{"type":"assistant","session_id":"diff-session","parent_tool_use_id":"` + parent + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"edit","name":"Edit","input":{"file_path":"/workspace/existing.txt","old_string":"old line","new_string":"new line"}}]}}`,
				`{"type":"user","session_id":"diff-session","parent_tool_use_id":"` + parent + `","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"edit","content":"updated"}]},"tool_use_result":{"filePath":"/workspace/existing.txt","structuredPatch":[{"oldStart":1,"oldLines":1,"newStart":1,"newLines":1,"lines":["-old line","+new line"]}]}}`,
				`{"type":"assistant","session_id":"diff-session","parent_tool_use_id":"` + parent + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"write","name":"Write","input":{"file_path":"/workspace/new.txt","content":"new file\n"}}]}}`,
				`{"type":"user","session_id":"diff-session","parent_tool_use_id":"` + parent + `","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"write","content":"created"}]},"tool_use_result":{"type":"create","filePath":"/workspace/new.txt","content":"new file\n","structuredPatch":[],"originalFile":null}}`,
				`{"type":"result","subtype":"success","num_turns":1,"session_id":"diff-session"}`,
			}
			a := &ClaudeAgent{CLIPath: writeFakeCLI(t, lines)}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			results := map[string]StreamEvent{}
			_, err := a.SendMessage(ctx, Request{WorkDir: t.TempDir(), Message: "modify files"}, func(event StreamEvent) {
				if event.Type == EventToolResult {
					results[event.ToolID] = event
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			for id, want := range map[string]string{"edit": "-old line\n+new line\n", "write": "+new file\n"} {
				got, ok := results[id]
				if !ok || got.IsError || got.ParentToolID != parent || !strings.Contains(got.Diff, want) || !looksLikeUnifiedDiff(got.Diff) {
					t.Fatalf("tool=%s result=%+v; want attributed unified diff", id, got)
				}
			}
			if !strings.HasPrefix(results["write"].Diff, "--- /dev/null\n") {
				t.Fatalf("new file must have an empty old side: %q", results["write"].Diff)
			}
		})
	}
}

func TestClaudeToolResultDiffDoesNotInventOverwriteDiff(t *testing.T) {
	for _, raw := range []string{
		`{"type":"update","filePath":"file.txt","content":"replacement\n","structuredPatch":[]}`,
		`{"filePath":"file.txt","content":"replacement\n"}`,
	} {
		if got := claudeToolResultDiff(json.RawMessage(raw)); got != "" {
			t.Fatalf("overwrite without before-image/patch was rendered as a new file: %q", got)
		}
	}
}
