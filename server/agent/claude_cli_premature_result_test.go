package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeFakeCLI writes an executable shell script that emits the given stdout
// lines (one JSON stream-json message per line) and exits. It ignores stdin,
// which is fine: ClaudeClient.Query writes a small initialize + user message
// that fits in the pipe buffer. Returns the script path.
func writeFakeCLI(t *testing.T, lines []string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI shell script is POSIX-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-claude.sh")
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	for _, ln := range lines {
		// Single-quote the JSON payload; our fixtures contain no single quotes.
		b.WriteString("printf '%s\\n' '")
		b.WriteString(ln)
		b.WriteString("'\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	return path
}

// TestQuery_SkipsPrematureCatchUpResult reproduces the resume-with-orphaned-
// background-task case: on --resume the CLI flushes a pending task_notification
// and emits a no-op result (num_turns=0, cost=0) BEFORE the real turn. The read
// loop must not break on that premature result — doing so drops the whole
// assistant turn and persists an empty message. See the guard in
// claude_cli.go's scanner loop.
func TestQuery_SkipsPrematureCatchUpResult(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"task_notification","session_id":"S","task_id":"t1","status":"stopped","summary":"No completion record was found for background workflow","output_file":"","uuid":"u0"}`,
		`{"type":"system","subtype":"init","session_id":"S","model":"claude-opus-4-8"}`,
		// Premature catch-up result: MUST be skipped, not treated as end-of-turn.
		`{"type":"result","subtype":"success","is_error":false,"num_turns":0,"total_cost_usd":0,"session_id":"S","duration_ms":1}`,
		// The real turn streams on the SAME stdout afterwards.
		`{"type":"assistant","session_id":"S","uuid":"u1","message":{"role":"assistant","id":"msg_1","content":[{"type":"text","text":"HELLO_REAL_TURN"}]}}`,
		// The real result ends the query.
		`{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":0.5,"session_id":"S","duration_ms":100}`,
	}
	client := &ClaudeClient{CLIPath: writeFakeCLI(t, lines)}

	var gotText strings.Builder
	var resultNumTurns []int
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sessionID, err := client.Query(ctx, "hi", QueryOptions{}, func(msg StreamMessage, parsed *AssistantMessage) {
		if msg.Type == "result" {
			resultNumTurns = append(resultNumTurns, msg.NumTurns)
		}
		if parsed != nil {
			for _, c := range parsed.Content {
				if c.Type == "text" {
					gotText.WriteString(c.Text)
				}
			}
		}
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if sessionID != "S" {
		t.Errorf("sessionID = %q, want %q", sessionID, "S")
	}
	// The real turn's text must have been delivered — the whole point of the fix.
	if !strings.Contains(gotText.String(), "HELLO_REAL_TURN") {
		t.Errorf("real turn text was dropped; got %q", gotText.String())
	}
	// Exactly one result should reach the callback: the real one (num_turns=1).
	// The premature num_turns=0 result must be swallowed before the callback.
	if len(resultNumTurns) != 1 || resultNumTurns[0] != 1 {
		t.Errorf("results delivered to callback = %v, want exactly [1]", resultNumTurns)
	}
}

// TestQuery_KeepsStreamOpenWhileWorkflowRuns reproduces the detached-Workflow
// arc: the agent launches a Workflow (task_started, task_type=local_workflow),
// the foreground turn goes idle and emits a `result` WHILE the Workflow is still
// running, then the Workflow completes (terminal task_notification) and the
// agent auto-continues before the true final result. The read loop must NOT
// break on the interim result — doing so would tear down the process, kill the
// Workflow, and drop the continuation.
func TestQuery_KeepsStreamOpenWhileWorkflowRuns(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"S3","model":"claude-opus-4-8"}`,
		`{"type":"assistant","session_id":"S3","uuid":"u1","message":{"role":"assistant","id":"m1","content":[{"type":"text","text":"LAUNCHING_WORKFLOW"}]}}`,
		`{"type":"assistant","session_id":"S3","uuid":"u2","message":{"role":"assistant","id":"m1","content":[{"type":"tool_use","id":"toolu_wf","name":"Workflow","input":{}}]}}`,
		`{"type":"system","subtype":"task_started","session_id":"S3","task_id":"w1","task_type":"local_workflow","tool_use_id":"toolu_wf","description":"wf","uuid":"u3"}`,
		// Workflow tool returns immediately.
		`{"type":"user","session_id":"S3","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_wf","content":"launched"}]}}`,
		// Foreground idle result — MUST be swallowed, Workflow w1 is still running.
		`{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":0.5,"session_id":"S3","duration_ms":100}`,
		`{"type":"system","subtype":"task_progress","session_id":"S3","task_id":"w1","task_type":"local_workflow","tool_use_id":"toolu_wf","description":"running","uuid":"u4"}`,
		// Workflow completes.
		`{"type":"system","subtype":"task_notification","session_id":"S3","task_id":"w1","status":"completed","summary":"done","uuid":"u5"}`,
		// Agent auto-continues on the same stream.
		`{"type":"assistant","session_id":"S3","uuid":"u6","message":{"role":"assistant","id":"m2","content":[{"type":"text","text":"WORKFLOW_DONE_CONTINUATION"}]}}`,
		// True final result — no Workflow outstanding, ends the exchange.
		`{"type":"result","subtype":"success","is_error":false,"num_turns":2,"total_cost_usd":0.8,"session_id":"S3","duration_ms":200}`,
		`{"type":"assistant","session_id":"S3","uuid":"u7","message":{"role":"assistant","id":"m3","content":[{"type":"text","text":"SHOULD_NOT_APPEAR"}]}}`,
	}
	client := &ClaudeClient{CLIPath: writeFakeCLI(t, lines)}

	var gotText strings.Builder
	var resultNumTurns []int
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := client.Query(ctx, "run the audit", QueryOptions{}, func(msg StreamMessage, parsed *AssistantMessage) {
		if msg.Type == "result" {
			resultNumTurns = append(resultNumTurns, msg.NumTurns)
		}
		if parsed != nil {
			for _, c := range parsed.Content {
				if c.Type == "text" {
					gotText.WriteString(c.Text + "|")
				}
			}
		}
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	got := gotText.String()
	if !strings.Contains(got, "LAUNCHING_WORKFLOW") {
		t.Errorf("missing launch text; got %q", got)
	}
	// The whole point: the post-Workflow continuation was captured.
	if !strings.Contains(got, "WORKFLOW_DONE_CONTINUATION") {
		t.Errorf("continuation after Workflow completion was dropped; got %q", got)
	}
	if strings.Contains(got, "SHOULD_NOT_APPEAR") {
		t.Error("loop kept reading past the final result")
	}
	// Only the final result (num_turns=2) reaches the callback; the interim
	// foreground-idle result (num_turns=1) is swallowed while the Workflow runs.
	if len(resultNumTurns) != 1 || resultNumTurns[0] != 2 {
		t.Errorf("results delivered to callback = %v, want exactly [2]", resultNumTurns)
	}
}

// TestQuery_ClearsWorkflowViaUserTaskNotification covers the delivery path that
// hung in production: the interim foreground result is swallowed while a
// Workflow runs, and the Workflow's completion arrives ONLY as a
// `<task-notification>` user message (no system task_notification/task_updated).
// Without clearing the running set on that user message, every later result is
// swallowed forever and the turn never ends.
func TestQuery_ClearsWorkflowViaUserTaskNotification(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"S4","model":"claude-opus-4-8"}`,
		`{"type":"assistant","session_id":"S4","uuid":"u1","message":{"role":"assistant","id":"m1","content":[{"type":"text","text":"LAUNCHING"}]}}`,
		`{"type":"assistant","session_id":"S4","uuid":"u2","message":{"role":"assistant","id":"m1","content":[{"type":"tool_use","id":"toolu_wf","name":"Workflow","input":{}}]}}`,
		`{"type":"system","subtype":"task_started","session_id":"S4","task_id":"wabc123","task_type":"local_workflow","tool_use_id":"toolu_wf","description":"wf","uuid":"u3"}`,
		`{"type":"user","session_id":"S4","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_wf","content":"launched"}]}}`,
		// Interim foreground result — swallowed, workflow wabc123 still running.
		`{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":0.5,"session_id":"S4","duration_ms":100}`,
		// Completion arrives ONLY as a user <task-notification> re-invocation.
		`{"type":"user","session_id":"S4","message":{"role":"user","content":[{"type":"text","text":"<task-notification>\n<task-id>wabc123</task-id>\n<status>completed</status>\n</task-notification>"}]}}`,
		`{"type":"assistant","session_id":"S4","uuid":"u6","message":{"role":"assistant","id":"m2","content":[{"type":"text","text":"CONTINUATION_DONE"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"num_turns":2,"total_cost_usd":0.8,"session_id":"S4","duration_ms":200}`,
	}
	client := &ClaudeClient{CLIPath: writeFakeCLI(t, lines)}

	var gotText strings.Builder
	var resultNumTurns []int
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := client.Query(ctx, "run it", QueryOptions{}, func(msg StreamMessage, parsed *AssistantMessage) {
		if msg.Type == "result" {
			resultNumTurns = append(resultNumTurns, msg.NumTurns)
		}
		if parsed != nil {
			for _, c := range parsed.Content {
				if c.Type == "text" {
					gotText.WriteString(c.Text)
				}
			}
		}
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if !strings.Contains(gotText.String(), "CONTINUATION_DONE") {
		t.Errorf("continuation dropped (workflow not cleared via user task-notification); got %q", gotText.String())
	}
	if len(resultNumTurns) != 1 || resultNumTurns[0] != 2 {
		t.Errorf("results delivered to callback = %v, want exactly [2] (interim swallowed)", resultNumTurns)
	}
}

// TestQuery_BreaksOnRealResult is the control case: a normal turn (no premature
// result, no Workflow) ends at its result exactly once, confirming the guards
// don't change ordinary behavior.
func TestQuery_BreaksOnRealResult(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"S2","model":"claude-opus-4-8"}`,
		`{"type":"assistant","session_id":"S2","uuid":"u1","message":{"role":"assistant","id":"msg_1","content":[{"type":"text","text":"ANSWER"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":0.25,"session_id":"S2","duration_ms":100}`,
		// A trailing line that must never be read (loop breaks on the result above).
		`{"type":"assistant","session_id":"S2","uuid":"u2","message":{"role":"assistant","id":"msg_2","content":[{"type":"text","text":"SHOULD_NOT_APPEAR"}]}}`,
	}
	client := &ClaudeClient{CLIPath: writeFakeCLI(t, lines)}

	var gotText strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := client.Query(ctx, "hi", QueryOptions{}, func(msg StreamMessage, parsed *AssistantMessage) {
		if parsed != nil {
			for _, c := range parsed.Content {
				if c.Type == "text" {
					gotText.WriteString(c.Text)
				}
			}
		}
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if !strings.Contains(gotText.String(), "ANSWER") {
		t.Errorf("missing real answer; got %q", gotText.String())
	}
	if strings.Contains(gotText.String(), "SHOULD_NOT_APPEAR") {
		t.Error("loop kept reading past the real result; break-on-result is broken")
	}
}
