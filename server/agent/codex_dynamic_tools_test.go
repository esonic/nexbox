package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCodexDynamicToolCall(t *testing.T) {
	raw := json.RawMessage(`{
		"itemId":"itm_1",
		"toolName":"Read",
		"arguments":{"path":"a.txt","start_line":1}
	}`)
	call, err := parseCodexDynamicToolCall(raw)
	if err != nil {
		t.Fatalf("parseCodexDynamicToolCall failed: %v", err)
	}
	if call.ItemID != "itm_1" {
		t.Fatalf("unexpected item id: %s", call.ItemID)
	}
	if call.ToolName != "Read" {
		t.Fatalf("unexpected tool name: %s", call.ToolName)
	}
	if !strings.Contains(string(call.Arguments), `"path":"a.txt"`) {
		t.Fatalf("unexpected arguments: %s", string(call.Arguments))
	}
}

func TestRunCodexDynamicToolCall_FileOps(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "a.txt")
	if err := os.WriteFile(target, []byte("hello world\n"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	ctx := context.Background()

	out, err := runCodexDynamicToolCall(ctx, workDir, "Read", json.RawMessage(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !strings.Contains(out, "hello world") {
		t.Fatalf("unexpected read output: %q", out)
	}

	_, err = runCodexDynamicToolCall(ctx, workDir, "Edit", json.RawMessage(`{"path":"a.txt","old_str":"world","new_str":"nexbox"}`))
	if err != nil {
		t.Fatalf("Edit failed: %v", err)
	}
	edited, _ := os.ReadFile(target)
	if !strings.Contains(string(edited), "hello nexbox") {
		t.Fatalf("edit not applied: %q", string(edited))
	}

	_, err = runCodexDynamicToolCall(ctx, workDir, "MultiEdit", json.RawMessage(`{
		"path":"a.txt",
		"edits":[
			{"old_str":"hello","new_str":"hi"},
			{"old_str":"nexbox","new_str":"team"}
		]
	}`))
	if err != nil {
		t.Fatalf("MultiEdit failed: %v", err)
	}
	edited, _ = os.ReadFile(target)
	if !strings.Contains(string(edited), "hi team") {
		t.Fatalf("multiedit not applied: %q", string(edited))
	}

	_, err = runCodexDynamicToolCall(ctx, workDir, "Write", json.RawMessage(`{"path":"b.txt","content":"new file"}`))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "b.txt")); err != nil {
		t.Fatalf("written file missing: %v", err)
	}

	_, err = runCodexDynamicToolCall(ctx, workDir, "Delete", json.RawMessage(`{"path":"b.txt"}`))
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file still exists")
	}
}

func TestCodexPatchToolStart_FromApplyPatchText(t *testing.T) {
	payload := map[string]interface{}{
		"diff": "*** Begin Patch\n*** Update File: build.sh\n@@\n-echo hi\n+echo hey\n*** End Patch\n",
	}
	toolName, input, path := codexPatchToolStart(payload, nil)
	if toolName != "Edit" {
		t.Fatalf("expected tool=Edit, got %q", toolName)
	}
	if path != "build.sh" {
		t.Fatalf("expected path=build.sh, got %q", path)
	}
	if !strings.Contains(input, `"path":"build.sh"`) {
		t.Fatalf("unexpected input: %s", input)
	}
}

func TestFirstJSONMapStringDeep(t *testing.T) {
	payload := map[string]interface{}{
		"event": map[string]interface{}{
			"payload": map[string]interface{}{
				"callId":  "call_123",
				"command": "echo hello",
			},
		},
	}
	callID := firstJSONMapStringDeep(payload, "call_id", "callId")
	if callID != "call_123" {
		t.Fatalf("expected call_123, got %q", callID)
	}
	cmd := firstJSONMapStringDeep(payload, "command")
	if cmd != "echo hello" {
		t.Fatalf("expected command echo hello, got %q", cmd)
	}
}

func TestExtractPatchPathFromResultText(t *testing.T) {
	msg := "Success. Updated the following files:\nM /Users/demo/project/build.sh\n"
	path := extractPatchPathFromResultText(msg)
	if path != "/Users/demo/project/build.sh" {
		t.Fatalf("unexpected path: %q", path)
	}
}

func TestNormalizeExecCommandText(t *testing.T) {
	in := `["/bin/bash","-lc","gofmt -w server/agent/codex_cli.go"]`
	got := normalizeExecCommandText(in)
	want := "/bin/bash -lc 'gofmt -w server/agent/codex_cli.go'"
	if got != want {
		t.Fatalf("unexpected normalized command: %q", got)
	}
}

func TestCodexWebSearchQuery_FromArguments(t *testing.T) {
	item := codexThreadItem{
		Arguments: json.RawMessage(`{"query":"site:claude.ai code review"}`),
	}
	got := codexWebSearchQuery(item)
	want := "site:claude.ai code review"
	if got != want {
		t.Fatalf("unexpected query: %q", got)
	}
}
