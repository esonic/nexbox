package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStripPlatformContext(t *testing.T) {
	cases := map[string]string{
		"[Platform Context]\nctx\n[User Message]\nhello":        "hello",
		"[NexBox Platform Context]\nctx\n[User Message]\nhello": "hello",
		"[BeeHub Platform Context]\nx\n[User Message]\nhello":   "hello",
		"[Project: a | ID: 1 | Dir: /d]\n\n[User Message]\nhi":  "hi",
		"[User Message]\nSummarize this":                        "Summarize this",
		"plain prompt":                                          "plain prompt",
		"mentions [User Message]\n in the middle":               "mentions [User Message]\n in the middle",
	}
	for in, want := range cases {
		if got := stripPlatformContext(in); got != want {
			t.Errorf("stripPlatformContext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClaudeImporter(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	workDir := "/work/my.proj"
	dir := filepath.Join(cfg, "projects", "-work-my-proj")
	id := "11111111-1111-1111-1111-111111111111"

	writeLines(t, filepath.Join(dir, id+".jsonl"),
		`{"type":"user","uuid":"u1","cwd":"/work/my.proj","timestamp":"2026-01-01T00:00:00.000Z","message":{"role":"user","content":"[BeeHub Platform Context]\nctx\n[User Message]\nfix the bug"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-01-01T00:00:01.000Z","message":{"id":"m1","model":"claude-opus-4-8","role":"assistant","content":[{"type":"thinking","thinking":"hmm"}],"usage":{"input_tokens":5,"output_tokens":1}}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-01-01T00:00:02.000Z","message":{"id":"m1","model":"claude-opus-4-8","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":5,"output_tokens":9}}}`,
		`{"type":"assistant","uuid":"s1","isSidechain":true,"timestamp":"2026-01-01T00:00:02.500Z","message":{"id":"mx","role":"assistant","content":[{"type":"text","text":"subagent noise"}]}}`,
		`{"type":"user","uuid":"r1","timestamp":"2026-01-01T00:00:03.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a.go","is_error":false}]}}`,
		`{"type":"assistant","uuid":"a3","timestamp":"2026-01-01T00:00:04.000Z","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":7,"output_tokens":2}}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-01-01T00:00:10.000Z","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}`,
		`{"type":"user","uuid":"u3","timestamp":"2026-01-01T00:00:11.000Z","message":{"role":"user","content":"thanks"}}`,
		`{"type":"ai-title","aiTitle":"Bug fix chat"}`,
	)
	// Helper-call transcript and a cwd-collision transcript must not be listed.
	writeLines(t, filepath.Join(dir, "22222222-2222-2222-2222-222222222222.jsonl"),
		`{"type":"user","uuid":"x","cwd":"/work/my.proj","message":{"role":"user","content":"[User Message]\nSummarize the following"}}`)
	writeLines(t, filepath.Join(dir, "33333333-3333-3333-3333-333333333333.jsonl"),
		`{"type":"user","uuid":"y","cwd":"/work/my/proj","message":{"role":"user","content":"other project"}}`)

	imp := &ClaudeSessionImporter{}
	list, _, err := imp.ListSessions(workDir, ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != id || list[0].Title != "fix the bug" {
		t.Fatalf("unexpected listing: %+v", list)
	}

	got, err := imp.LoadSession(workDir, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Bug fix chat" || got.Model != "opus" {
		t.Fatalf("title/model = %q/%q", got.Title, got.Model)
	}
	// user, assistant (whole turn), user
	if len(got.Messages) != 3 {
		t.Fatalf("got %d messages, want 3: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Parts[0].Content != "fix the bug" || got.Messages[2].Parts[0].Content != "thanks" {
		t.Fatalf("user prompts not stripped/filtered: %+v / %+v", got.Messages[0].Parts, got.Messages[2].Parts)
	}
	turn := got.Messages[1]
	var kinds []string
	for _, p := range turn.Parts {
		kinds = append(kinds, p.Type)
	}
	if strings.Join(kinds, ",") != "thinking,tool_use,tool_result,text" {
		t.Fatalf("assistant parts = %v", kinds)
	}
	if turn.Parts[1].ToolName != "Bash" || turn.Parts[1].Input != `{"command":"ls"}` || turn.Parts[1].State != "complete" {
		t.Fatalf("tool_use = %+v", turn.Parts[1])
	}
	if turn.Metadata.AgentCheckpoint != "a3" || turn.Metadata.SessionID != id || turn.Metadata.ToolCalls != 1 {
		t.Fatalf("metadata = %+v", turn.Metadata)
	}
	// m1 usage is the last repeat (5/9), plus m2 (7/2)
	if turn.Metadata.InputTokens != 12 || turn.Metadata.OutputTokens != 11 {
		t.Fatalf("tokens = %d/%d", turn.Metadata.InputTokens, turn.Metadata.OutputTokens)
	}

	if _, err := imp.LoadSession(workDir, "../etc/passwd"); err != ErrImportNotFound {
		t.Fatalf("path traversal id: %v", err)
	}
}

func TestCodexImporter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	workDir := "/work/proj"
	id := "01a00000-0000-7000-8000-000000000001"
	path := filepath.Join(home, "sessions", "2026", "01", "01", "rollout-2026-01-01T00-00-00-"+id+".jsonl")

	writeLines(t, path,
		`{"timestamp":"2026-01-01T00:00:00.000Z","type":"session_meta","payload":{"id":"`+id+`","cwd":"/work/proj","source":"cli"}}`,
		`{"timestamp":"2026-01-01T00:00:01.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-01-01T00:00:01.100Z","type":"turn_context","payload":{"model":"gpt-5.6-luna"}}`,
		`{"timestamp":"2026-01-01T00:00:01.200Z","type":"event_msg","payload":{"type":"user_message","message":"[Project: p | ID: 1 | Dir: /work/proj]\n\n[User Message]\nlist files"}}`,
		`{"timestamp":"2026-01-01T00:00:02.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"ls\"}","call_id":"c1"}}`,
		`{"timestamp":"2026-01-01T00:00:03.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"a.go"}}`,
		`{"timestamp":"2026-01-01T00:00:03.500Z","type":"response_item","payload":{"type":"function_call","name":"wait","arguments":"{}","call_id":"c2"}}`,
		`{"timestamp":"2026-01-01T00:00:03.600Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c2","output":"noise"}}`,
		`{"timestamp":"2026-01-01T00:00:04.000Z","type":"event_msg","payload":{"type":"patch_apply_end","call_id":"p1","success":true,"changes":{"/work/proj/a.go":{"type":"update","unified_diff":"@@ -1,1 +1,1 @@\n-old\n+new\n"}}}}`,
		`{"timestamp":"2026-01-01T00:00:05.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"all done"}]}}`,
		`{"timestamp":"2026-01-01T00:00:05.100Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":60,"output_tokens":7}}}}`,
		`{"timestamp":"2026-01-01T00:00:06.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1"}}`,
		// Newer rollouts record the prompt only as a UserMessage item.
		`{"timestamp":"2026-01-01T00:01:00.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-2"}}`,
		`{"timestamp":"2026-01-01T00:01:00.100Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"and again"}]}}}`,
		`{"timestamp":"2026-01-01T00:01:01.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"interrupted"}]}}`,
		`{"timestamp":"2026-01-01T00:01:02.000Z","type":"event_msg","payload":{"type":"turn_aborted"}}`,
	)
	// Sub-agent threads and other projects must not be listed.
	writeLines(t, filepath.Join(home, "sessions", "2026", "01", "01", "rollout-2026-01-01T00-00-00-01a00000-0000-7000-8000-000000000002.jsonl"),
		`{"timestamp":"2026-01-01T00:00:00.000Z","type":"session_meta","payload":{"id":"01a00000-0000-7000-8000-000000000002","cwd":"/work/proj","source":{"subagent":{}}}}`,
		`{"timestamp":"2026-01-01T00:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"child"}}`)

	imp := &CodexSessionImporter{}
	list, _, err := imp.ListSessions(workDir, ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != id || list[0].Title != "list files" {
		t.Fatalf("unexpected listing: %+v", list)
	}

	got, err := imp.LoadSession(workDir, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "gpt-5.6-luna" || got.Title != "list files" {
		t.Fatalf("model/title = %q/%q", got.Model, got.Title)
	}
	if len(got.Messages) != 4 {
		t.Fatalf("got %d messages, want 4: %+v", len(got.Messages), got.Messages)
	}
	turn := got.Messages[1]
	var kinds []string
	for _, p := range turn.Parts {
		kinds = append(kinds, p.Type+":"+p.ToolName)
	}
	want := "tool_use:Bash,tool_result:,tool_use:FileChange,tool_result:,text:"
	if strings.Join(kinds, ",") != want {
		t.Fatalf("parts = %v, want %s", kinds, want)
	}
	if turn.Parts[0].Input != `{"command":"ls"}` {
		t.Fatalf("bash input = %v", turn.Parts[0].Input)
	}
	if !strings.Contains(turn.Parts[3].Diff, "+new") {
		t.Fatalf("file change diff = %q", turn.Parts[3].Diff)
	}
	if turn.Metadata.AgentCheckpoint != "turn-1" || turn.Metadata.InputTokens != 100 || turn.Metadata.CacheReadInputTokens != 60 || turn.Metadata.OutputTokens != 7 {
		t.Fatalf("metadata = %+v", turn.Metadata)
	}
	if got.Messages[2].Parts[0].Content != "and again" {
		t.Fatalf("UserMessage item prompt = %+v", got.Messages[2].Parts)
	}
	if got.Messages[3].Metadata.AgentCheckpoint != "" {
		t.Fatalf("aborted turn must not be forkable, got checkpoint %q", got.Messages[3].Metadata.AgentCheckpoint)
	}
}

func TestListSessionsPaging(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	dir := filepath.Join(cfg, "projects", "-work-p")
	ids := []string{"s1", "s2", "s3", "s4", "s5"}
	for i, id := range ids {
		path := filepath.Join(dir, id+".jsonl")
		writeLines(t, path, `{"type":"user","uuid":"`+id+`","cwd":"/work/p","message":{"role":"user","content":"prompt `+id+`"}}`)
		mod := time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	imp := &ClaudeSessionImporter{}
	skip := func(id string) bool { return id == "s4" } // already in BeeHub

	var got []string
	opts := ListOptions{Limit: 2, Skip: skip}
	for pages := 0; pages < 5; pages++ {
		page, more, err := imp.ListSessions("/work/p", opts)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range page {
			got = append(got, s.ID)
		}
		if !more {
			break
		}
		opts.Before = page[len(page)-1].UpdatedAt
	}
	if strings.Join(got, ",") != "s5,s3,s2,s1" {
		t.Fatalf("paged ids = %v", got)
	}
}

func TestCodexListSessions_UserMessageItemPrompt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	id := "01a00000-0000-7000-8000-000000000009"
	writeLines(t, filepath.Join(home, "sessions", "2026", "01", "01", "rollout-2026-01-01T00-00-00-"+id+".jsonl"),
		`{"timestamp":"2026-01-01T00:00:00.000Z","type":"session_meta","payload":{"id":"`+id+`","cwd":"/work/proj","source":"vscode"}}`,
		`{"timestamp":"2026-01-01T00:00:01.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"[Project: p | ID: 1 | Dir: /work/proj]\n\n[User Message]\nitem-only prompt"}]}}}`)
	list, _, err := (&CodexSessionImporter{}).ListSessions("/work/proj", ListOptions{Limit: 5})
	if err != nil || len(list) != 1 || list[0].ID != id || list[0].Title != "item-only prompt" {
		t.Fatalf("list = %+v err=%v", list, err)
	}
}

func TestIsCodexActiveWriterError(t *testing.T) {
	busy := errors.New("thread/resume failed (-32600): thread 01a1 already has an active writer")
	if !isCodexActiveWriterError(busy) || isCodexSessionExpiredError(busy) {
		t.Fatal("active-writer failure must be detected and must not look like an expired session")
	}
	if isCodexActiveWriterError(nil) || isCodexActiveWriterError(errors.New("boom")) {
		t.Fatal("unrelated errors must not match")
	}
}
