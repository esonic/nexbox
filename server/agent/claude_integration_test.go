//go:build integration

package agent

// Run all tests:
//
//	go test -v -tags integration -timeout 180s ./agent/
//
// Run a single scenario:
//
//	go test -v -tags integration -timeout 60s -run TestStreamingPartialMessages ./agent/

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func clearClaudeEnv(t *testing.T) {
	t.Helper()
	// Claude Code sets CLAUDECODE to prevent recursive invocations; unset it
	// so integration tests can spawn nested claude processes.
	t.Setenv("CLAUDECODE", "")
}

func newTestAgent() *ClaudeAgent {
	a := NewClaudeAgent()
	a.Verbose = true
	return a
}

func printSection(t *testing.T, title string) {
	t.Helper()
	t.Logf("\n%s\n%s\n%s", strings.Repeat("=", 60), title, strings.Repeat("=", 60))
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func logMeta(t *testing.T, meta map[string]interface{}) {
	t.Helper()
	if meta == nil {
		t.Logf("metadata   : nil  ← EventDone never fired or no metadata")
		return
	}
	t.Logf("metadata:")
	for _, key := range []string{
		"inputTokens", "outputTokens", "totalCostUsd",
		"durationMs", "durationApiMs", "stopReason",
	} {
		if v := meta[key]; v != nil {
			t.Logf("  %-16s: %v  (%T)", key, v, v)
		}
	}
	if in, ok := meta["inputTokens"].(int); ok {
		if out, ok2 := meta["outputTokens"].(int); ok2 {
			t.Logf("  %-16s: %d", "totalTokens", in+out)
		}
	}
	if meta["inputTokens"] == nil && meta["outputTokens"] == nil {
		t.Logf("  *** WARNING: token counts missing ***")
	}
	fmt.Println()
}

// collectEvents runs SendMessage and returns all collected events plus the error.
func collectEvents(t *testing.T, ctx context.Context, req Request) ([]StreamEvent, error) {
	t.Helper()
	a := newTestAgent()
	var events []StreamEvent
	start := time.Now()
	_, err := a.SendMessage(ctx, req, func(e StreamEvent) {
		events = append(events, e)
		t.Logf("  [event] %-16s content=%q elapsed=%.2fs",
			e.Type, truncate(e.Content, 50), time.Since(start).Seconds())
	})
	return events, err
}

func doneEvent(events []StreamEvent) *StreamEvent {
	for i := range events {
		if events[i].Type == EventDone {
			return &events[i]
		}
	}
	return nil
}

func countType(events []StreamEvent, typ string) int {
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func allTextDelta(events []StreamEvent) string {
	var sb strings.Builder
	for _, e := range events {
		if e.Type == EventTextDelta {
			sb.WriteString(e.Content)
		}
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// T1: Normal completion — verify tokens, stop_reason, duration_api_ms
// ---------------------------------------------------------------------------

// TestNormalCompletion verifies a simple exchange: tokens present, stop_reason = "end_turn",
// duration_api_ms present.
func TestNormalCompletion(t *testing.T) {
	clearClaudeEnv(t)
	printSection(t, "T1: Normal completion — tokens + stop_reason + duration_api_ms")

	ctx := context.Background()
	events, err := collectEvents(t, ctx, Request{
		ConversationID: "integ-normal-1",
		WorkDir:        t.TempDir(),
		Model:          "claude-haiku-4-5-20251001",
		Message:        `Reply with only the single word "pong". No punctuation, no explanation.`,
	})

	done := doneEvent(events)

	t.Logf("\n--- Result ---")
	t.Logf("err        : %v", err)
	if done != nil {
		logMeta(t, done.Metadata)
	}

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if done == nil {
		t.Fatal("EventDone not received")
	}

	meta := done.Metadata
	if meta["inputTokens"] == nil {
		t.Error("inputTokens missing from metadata")
	}
	if meta["outputTokens"] == nil {
		t.Error("outputTokens missing from metadata")
	}
	if sr, _ := meta["stopReason"].(string); sr == "" {
		t.Error("stopReason missing from metadata")
	} else {
		t.Logf("✓ stopReason = %q", sr)
	}
	if meta["durationApiMs"] == nil {
		// Not a hard failure — CLI version might not emit it yet
		t.Logf("  (durationApiMs not present — may need newer CLI)")
	} else {
		t.Logf("✓ durationApiMs present: %v", meta["durationApiMs"])
	}
}

// ---------------------------------------------------------------------------
// T2: Partial message streaming — stream_event text deltas + deduplication
// ---------------------------------------------------------------------------

// TestStreamingPartialMessages verifies that:
//  1. Multiple EventTextDelta events arrive incrementally (stream_event → text_delta)
//  2. The concatenated text is NOT duplicated (hasPartialText dedup logic works)
//  3. Total text matches the expected response
func TestStreamingPartialMessages(t *testing.T) {
	clearClaudeEnv(t)
	printSection(t, "T2: Partial message streaming — incremental deltas, no duplication")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	events, err := collectEvents(t, ctx, Request{
		ConversationID: "integ-streaming-1",
		WorkDir:        t.TempDir(),
		Model:          "claude-haiku-4-5-20251001",
		// Predictable short response — easy to check for duplication.
		Message: `Reply with exactly these words in order: "alpha beta gamma delta epsilon". Nothing else.`,
	})

	totalText := allTextDelta(events)
	deltaCount := countType(events, EventTextDelta)

	t.Logf("\n--- Result ---")
	t.Logf("err          : %v", err)
	t.Logf("text_delta   : %d events", deltaCount)
	t.Logf("total text   : %q", totalText)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Streaming should produce more than one delta for a multi-word response.
	if deltaCount <= 1 {
		t.Logf("  (only %d text_delta events — stream_event may not be supported by this CLI version)", deltaCount)
	} else {
		t.Logf("✓ %d incremental text_delta events (streaming active)", deltaCount)
	}

	// The most important invariant: text must not be duplicated.
	// e.g. "alpha beta gamma delta epsilon" should appear once, not twice.
	lower := strings.ToLower(totalText)
	if strings.Count(lower, "alpha") > 1 {
		t.Errorf("text duplication detected: %q", totalText)
	} else {
		t.Logf("✓ no text duplication")
	}

	// Verify expected words appear at least once.
	for _, word := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(lower, word) {
			t.Errorf("expected word %q not found in output %q", word, totalText)
		}
	}
}

// ---------------------------------------------------------------------------
// T3: Graceful interrupt — interrupt control_request replaces SIGTERM
// ---------------------------------------------------------------------------

// TestGracefulInterrupt cancels mid-task and verifies:
//   - EventDone fires (either from real result after interrupt, or synthetic fallback)
//   - If interrupt worked → metadata has durationMs (from real result message)
//   - If SIGTERM fallback → metadata has only tokens (synthetic)
//
// Either path is acceptable; the test documents which happened.
func TestGracefulInterrupt(t *testing.T) {
	clearClaudeEnv(t)
	printSection(t, "T3: Graceful interrupt — control_request before SIGTERM")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel after 3s to give Claude time to start generating but not finish.
	time.AfterFunc(3*time.Second, func() {
		t.Log("  [test] >>> cancelling context <<<")
		cancel()
	})

	start := time.Now()
	events, err := collectEvents(t, ctx, Request{
		ConversationID: "integ-interrupt-1",
		WorkDir:        t.TempDir(),
		Model:          "claude-haiku-4-5-20251001",
		Message:        "Count from 1 to 500, one number per line, each followed by a short sentence.",
	})

	done := doneEvent(events)

	t.Logf("\n--- Result ---")
	t.Logf("elapsed    : %.1fs", time.Since(start).Seconds())
	t.Logf("err        : %v", err)
	t.Logf("ctx.Err    : %v", ctx.Err())
	if done != nil {
		logMeta(t, done.Metadata)
	}

	// EventDone MUST fire even on cancel (for billing).
	if done == nil {
		t.Error("EventDone not received — billing will be inaccurate")
	} else if done.Metadata["inputTokens"] == nil {
		t.Error("inputTokens missing after cancel — billing inaccurate")
	} else {
		t.Logf("✓ EventDone fired with token data")

		if done.Metadata["durationMs"] != nil {
			t.Logf("✓ durationMs present → interrupt control_request worked (real result message received)")
		} else {
			t.Logf("  durationMs absent → SIGTERM fallback path (synthetic EventDone)")
		}
	}

	// err should wrap context.Canceled (not nil, not some other error).
	if ctx.Err() == nil {
		t.Error("expected ctx to be cancelled")
	}
}

// TestGracefulInterruptFast cancels very quickly (500ms) to stress-test the
// interrupt path when Claude may not have started its API call yet.
func TestGracefulInterruptFast(t *testing.T) {
	clearClaudeEnv(t)
	printSection(t, "T3b: Fast interrupt (500ms cancel)")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	time.AfterFunc(500*time.Millisecond, func() {
		t.Log("  [test] >>> cancelling context (fast) <<<")
		cancel()
	})

	start := time.Now()
	events, err := collectEvents(t, ctx, Request{
		ConversationID: "integ-interrupt-fast",
		WorkDir:        t.TempDir(),
		Model:          "claude-haiku-4-5-20251001",
		Message:        "Write a 3000-word essay about the history of programming languages.",
	})

	done := doneEvent(events)

	t.Logf("\n--- Result ---")
	t.Logf("elapsed    : %.2fs", time.Since(start).Seconds())
	t.Logf("err        : %v", err)
	if done != nil {
		logMeta(t, done.Metadata)
		t.Logf("✓ EventDone fired (tokens present: %v)", done.Metadata["inputTokens"] != nil)
	} else {
		t.Logf("  EventDone not fired (cancelled before any tokens were generated)")
	}
	_ = err // cancellation errors are expected
}

// ---------------------------------------------------------------------------
// T4: stop_reason field
// ---------------------------------------------------------------------------

// TestStopReason verifies that stop_reason is included in EventDone metadata.
// Normal completion should report "end_turn".
func TestStopReason(t *testing.T) {
	clearClaudeEnv(t)
	printSection(t, "T4: stop_reason in EventDone metadata")

	ctx := context.Background()
	events, err := collectEvents(t, ctx, Request{
		ConversationID: "integ-stopreason-1",
		WorkDir:        t.TempDir(),
		Model:          "claude-haiku-4-5-20251001",
		Message:        `Reply with only the word "done".`,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	done := doneEvent(events)
	if done == nil {
		t.Fatal("EventDone not received")
	}

	sr, _ := done.Metadata["stopReason"].(string)
	t.Logf("stopReason = %q", sr)

	if sr == "" {
		t.Error("stopReason missing from metadata — check StreamMessage.StopReason and result handler")
	} else if sr != "end_turn" {
		t.Logf("  (stopReason=%q; expected 'end_turn' but another value is also valid)", sr)
	} else {
		t.Logf("✓ stopReason = 'end_turn'")
	}
}

// ---------------------------------------------------------------------------
// T5: Session resume — new session → resume → verify session continuity
// ---------------------------------------------------------------------------

// TestSessionResume verifies multi-turn session continuity:
//  1. First turn establishes a session (no Resume).
//  2. Second turn resumes with the returned session ID.
//  3. Claude retains context from turn 1 in turn 2.
func TestSessionResume(t *testing.T) {
	clearClaudeEnv(t)
	printSection(t, "T5: Session resume — multi-turn context")

	a := newTestAgent()
	workDir := t.TempDir()
	ctx := context.Background()
	start := time.Now()

	// Turn 1: establish a fact.
	var sessionID1 string
	result1, err := a.SendMessage(ctx, Request{
		ConversationID: "integ-resume-1",
		WorkDir:        workDir,
		Model:          "claude-haiku-4-5-20251001",
		Message:        `Remember this number: 42. Reply only with "stored".`,
	}, func(e StreamEvent) {
		t.Logf("  [turn1/%s] %q", e.Type, truncate(e.Content, 50))
	})

	if err != nil {
		t.Fatalf("turn 1 error: %v", err)
	}
	sessionID1 = result1.SessionID
	t.Logf("Turn 1 done: session=%s  elapsed=%.1fs", sessionID1, time.Since(start).Seconds())

	if sessionID1 == "" {
		t.Fatal("no session ID returned from turn 1")
	}

	// Turn 2: ask about the fact from turn 1.
	var turn2Text strings.Builder
	_, err = a.SendMessage(ctx, Request{
		ConversationID: "integ-resume-1",
		SessionID:      sessionID1,
		WorkDir:        workDir,
		Model:          "claude-haiku-4-5-20251001",
		Message:        "What number did I ask you to remember?",
	}, func(e StreamEvent) {
		t.Logf("  [turn2/%s] %q", e.Type, truncate(e.Content, 50))
		if e.Type == EventTextDelta {
			turn2Text.WriteString(e.Content)
		}
	})

	if err != nil {
		t.Errorf("turn 2 error: %v", err)
	}

	reply := turn2Text.String()
	t.Logf("Turn 2 reply: %q  elapsed=%.1fs", reply, time.Since(start).Seconds())

	if !strings.Contains(reply, "42") {
		t.Errorf("expected '42' in turn 2 reply, got: %q", reply)
	} else {
		t.Logf("✓ session context preserved across turns (found '42' in reply)")
	}
}

// ---------------------------------------------------------------------------
// T6: Tool use — verify EventToolStart / EventToolResult
// ---------------------------------------------------------------------------

// TestToolUse asks Claude to use the Bash tool and verifies the event sequence:
// EventToolStart → EventToolResult → EventTextDelta → EventDone.
func TestToolUse(t *testing.T) {
	clearClaudeEnv(t)
	printSection(t, "T6: Tool use — EventToolStart + EventToolResult events")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	events, err := collectEvents(t, ctx, Request{
		ConversationID: "integ-tools-1",
		WorkDir:        t.TempDir(),
		Model:          "claude-haiku-4-5-20251001",
		Message:        "Use the Bash tool to run: echo hello_integration_test. Then tell me the output.",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	toolStarts := countType(events, EventToolStart)
	toolResults := countType(events, EventToolResult)

	t.Logf("\n--- Result ---")
	t.Logf("tool_start  : %d events", toolStarts)
	t.Logf("tool_result : %d events", toolResults)

	if toolStarts == 0 {
		t.Error("no EventToolStart events — Bash tool was not used")
	} else {
		t.Logf("✓ %d tool calls initiated", toolStarts)
	}
	if toolResults == 0 {
		t.Error("no EventToolResult events")
	} else {
		t.Logf("✓ %d tool results received", toolResults)
	}

	// Verify the output contains "hello_integration_test".
	totalText := allTextDelta(events)
	if !strings.Contains(totalText, "hello_integration_test") {
		t.Errorf("expected 'hello_integration_test' in final reply, got: %q", truncate(totalText, 200))
	} else {
		t.Logf("✓ tool output appears in final reply")
	}
}

// ---------------------------------------------------------------------------
// T7: No duplicate EventDone after graceful interrupt
// ---------------------------------------------------------------------------

// TestNoDuplicateEventDone verifies that when interrupt works and a real result
// message is received, we do NOT emit a second synthetic EventDone.
func TestNoDuplicateEventDone(t *testing.T) {
	clearClaudeEnv(t)
	printSection(t, "T7: No duplicate EventDone after graceful interrupt")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	time.AfterFunc(2*time.Second, func() {
		cancel()
	})

	events, _ := collectEvents(t, ctx, Request{
		ConversationID: "integ-nodupe-1",
		WorkDir:        t.TempDir(),
		Model:          "claude-haiku-4-5-20251001",
		Message:        "Count slowly from 1 to 1000, one number per line.",
	})

	doneCount := countType(events, EventDone)
	t.Logf("EventDone count: %d", doneCount)

	if doneCount > 1 {
		t.Errorf("duplicate EventDone: got %d, want ≤ 1", doneCount)
	} else {
		t.Logf("✓ EventDone fired exactly %d time(s)", doneCount)
	}
}
