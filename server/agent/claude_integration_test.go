//go:build integration

package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// clearClaudeEnv unsets the CLAUDECODE env var that prevents nested Claude sessions.
// Claude Code sets this to stop recursive invocations; tests must bypass it.
func clearClaudeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CLAUDECODE", "") // t.Setenv restores on cleanup automatically
	os.Unsetenv("CLAUDECODE")  //nolint:errcheck
}

// Run with:
//   go test -v -tags integration -run TestTokenReporting ./agent/
//   go test -v -tags integration -timeout 120s -run TestTokenReporting ./agent/

func printSection(t *testing.T, title string) {
	t.Helper()
	t.Logf("\n%s\n%s\n%s", strings.Repeat("=", 60), title, strings.Repeat("=", 60))
}

func newTestAgent() *ClaudeAgent {
	a := NewClaudeAgent()
	a.Verbose = true
	return a
}

// TestTokenReporting_NormalCompletion: simple fast reply, should always report tokens.
func TestTokenReporting_NormalCompletion(t *testing.T) {
	clearClaudeEnv(t)
	printSection(t, "SCENARIO 1: Normal Completion")

	a := newTestAgent()
	workDir := t.TempDir()
	ctx := context.Background()

	var gotDone bool
	var doneMeta map[string]interface{}
	var events []string

	_, err := a.SendMessage(ctx, Request{
		ConversationID: "integ-normal-1",
		WorkDir:        workDir,
		Model:          "haiku",
		Message:        `Reply with only the single word "pong". No punctuation, no explanation.`,
	}, func(e StreamEvent) {
		events = append(events, e.Type)
		t.Logf("  [event] %-15s content=%q", e.Type, truncate(e.Content, 60))
		if e.Type == EventDone {
			gotDone = true
			doneMeta = e.Metadata
		}
	})

	t.Logf("\n--- Result ---")
	t.Logf("err        : %v", err)
	t.Logf("gotDone    : %v", gotDone)
	t.Logf("events     : %v", events)
	logMeta(t, doneMeta)
}

// TestTokenReporting_CancelAfter3s: cancel mid-stream, fairly generous window.
func TestTokenReporting_CancelAfter3s(t *testing.T) {
	clearClaudeEnv(t)
	printSection(t, "SCENARIO 2: Cancel after 3 seconds")

	a := newTestAgent()
	workDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var gotDone bool
	var doneMeta map[string]interface{}
	var events []string

	time.AfterFunc(3*time.Second, func() {
		t.Log("  [test] >>> Cancelling context now <<<")
		cancel()
	})

	start := time.Now()
	_, err := a.SendMessage(ctx, Request{
		ConversationID: "integ-cancel-3s",
		WorkDir:        workDir,
		Model:          "haiku",
		Message:        "Count from 1 to 500, each number on its own line. Go slowly and include a sentence describing that number.",
	}, func(e StreamEvent) {
		events = append(events, e.Type)
		t.Logf("  [event] %-15s content=%q elapsed=%.1fs", e.Type, truncate(e.Content, 40), time.Since(start).Seconds())
		if e.Type == EventDone {
			gotDone = true
			doneMeta = e.Metadata
		}
	})

	t.Logf("\n--- Result ---")
	t.Logf("elapsed    : %.1fs", time.Since(start).Seconds())
	t.Logf("err        : %v", err)
	t.Logf("ctx.Err    : %v", ctx.Err())
	t.Logf("gotDone    : %v", gotDone)
	t.Logf("events     : %v", events)
	logMeta(t, doneMeta)
}

// TestTokenReporting_CancelAfter500ms: cancel very quickly, stress tests timing.
func TestTokenReporting_CancelAfter500ms(t *testing.T) {
	clearClaudeEnv(t)
	printSection(t, "SCENARIO 3: Cancel after 500ms (immediate-ish)")

	a := newTestAgent()
	workDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var gotDone bool
	var doneMeta map[string]interface{}
	var events []string

	time.AfterFunc(500*time.Millisecond, func() {
		t.Log("  [test] >>> Cancelling context now <<<")
		cancel()
	})

	start := time.Now()
	_, err := a.SendMessage(ctx, Request{
		ConversationID: "integ-cancel-500ms",
		WorkDir:        workDir,
		Model:          "haiku",
		Message:        "Write a 3000-word essay about the history of programming languages from FORTRAN to today.",
	}, func(e StreamEvent) {
		events = append(events, e.Type)
		t.Logf("  [event] %-15s content=%q elapsed=%.2fs", e.Type, truncate(e.Content, 40), time.Since(start).Seconds())
		if e.Type == EventDone {
			gotDone = true
			doneMeta = e.Metadata
		}
	})

	t.Logf("\n--- Result ---")
	t.Logf("elapsed    : %.2fs", time.Since(start).Seconds())
	t.Logf("err        : %v", err)
	t.Logf("ctx.Err    : %v", ctx.Err())
	t.Logf("gotDone    : %v", gotDone)
	t.Logf("events     : %v", events)
	logMeta(t, doneMeta)
}

// TestTokenReporting_CancelBeforeResponse: cancel before Claude can even start.
func TestTokenReporting_CancelBeforeResponse(t *testing.T) {
	clearClaudeEnv(t)
	printSection(t, "SCENARIO 4: Cancel pre-emptively (already cancelled ctx)")

	a := newTestAgent()
	workDir := t.TempDir()

	// Already cancelled before we even call SendMessage
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	var gotDone bool
	var doneMeta map[string]interface{}
	var events []string

	start := time.Now()
	_, err := a.SendMessage(ctx, Request{
		ConversationID: "integ-cancel-pre",
		WorkDir:        workDir,
		Model:          "haiku",
		Message:        "What is 1+1?",
	}, func(e StreamEvent) {
		events = append(events, e.Type)
		t.Logf("  [event] %-15s content=%q", e.Type, truncate(e.Content, 60))
		if e.Type == EventDone {
			gotDone = true
			doneMeta = e.Metadata
		}
	})

	t.Logf("\n--- Result ---")
	t.Logf("elapsed    : %.2fs", time.Since(start).Seconds())
	t.Logf("err        : %v", err)
	t.Logf("ctx.Err    : %v", ctx.Err())
	t.Logf("gotDone    : %v", gotDone)
	t.Logf("events     : %v", events)
	logMeta(t, doneMeta)
}

// TestTokenReporting_SIGTERMCancel: verifies SIGTERM allows CLI to emit final result.
// After the SIGTERM change, cancel should now give Claude time to write the result message.
func TestTokenReporting_SIGTERMCancel(t *testing.T) {
	clearClaudeEnv(t)
	printSection(t, "SCENARIO 5: SIGTERM cancel (3s) — expect tokens now")

	a := newTestAgent()
	workDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var gotDone bool
	var doneMeta map[string]interface{}
	var events []string

	time.AfterFunc(3*time.Second, func() {
		t.Log("  [test] >>> Cancelling context (SIGTERM path) <<<")
		cancel()
	})

	start := time.Now()
	_, err := a.SendMessage(ctx, Request{
		ConversationID: "integ-sigterm-3s",
		WorkDir:        workDir,
		Model:          "haiku",
		Message:        "Count from 1 to 500, each number on its own line. Include a sentence per number.",
	}, func(e StreamEvent) {
		events = append(events, e.Type)
		t.Logf("  [event] %-15s content=%q elapsed=%.1fs", e.Type, truncate(e.Content, 40), time.Since(start).Seconds())
		if e.Type == EventDone {
			gotDone = true
			doneMeta = e.Metadata
		}
	})

	t.Logf("\n--- Result ---")
	t.Logf("elapsed    : %.1fs", time.Since(start).Seconds())
	t.Logf("err        : %v", err)
	t.Logf("ctx.Err    : %v", ctx.Err())
	t.Logf("gotDone    : %v", gotDone)
	t.Logf("events     : %v", events)
	logMeta(t, doneMeta)
}

// TestTokenReporting_TaskProgressFallback: triggers tool use (multi-step task) then
// cancels mid-way. Verifies that task_progress messages carry token data that can
// be used as a fallback when the process is cancelled before a result is emitted.
//
// Run with:
//
//	go test -v -tags integration -timeout 120s -run TestTokenReporting_TaskProgressFallback ./agent/
func TestTokenReporting_TaskProgressFallback(t *testing.T) {
	clearClaudeEnv(t)
	printSection(t, "SCENARIO 6: task_progress token fallback (cancel after 5s, tool-using task)")

	a := newTestAgent()
	workDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var gotDone bool
	var doneMeta map[string]interface{}
	var events []string
	var progressTokensSeen []int

	time.AfterFunc(15*time.Second, func() {
		t.Log("  [test] >>> Cancelling context now <<<")
		cancel()
	})

	start := time.Now()
	_, err := a.SendMessage(ctx, Request{
		ConversationID: "integ-progress-fallback",
		WorkDir:        workDir,
		Model:          "haiku",
		// A multi-step task that forces Claude to use tools (Bash) many times,
		// generating task_progress messages with running total_tokens.
		Message: "Use the Bash tool to run these 20 commands one by one, waiting for each result: " +
			"echo step1, echo step2, echo step3, echo step4, echo step5, " +
			"echo step6, echo step7, echo step8, echo step9, echo step10, " +
			"echo step11, echo step12, echo step13, echo step14, echo step15, " +
			"echo step16, echo step17, echo step18, echo step19, echo step20. " +
			"After each command, write a paragraph explaining what you observed.",
	}, func(e StreamEvent) {
		events = append(events, e.Type)
		t.Logf("  [event] %-15s content=%q elapsed=%.1fs", e.Type, truncate(e.Content, 40), time.Since(start).Seconds())
		if e.Type == EventDone {
			gotDone = true
			doneMeta = e.Metadata
			if fallback, ok := e.Metadata["totalTokensFallback"]; ok {
				if v, ok2 := fallback.(int); ok2 {
					progressTokensSeen = append(progressTokensSeen, v)
				}
			}
		}
	})

	t.Logf("\n--- Result ---")
	t.Logf("elapsed              : %.1fs", time.Since(start).Seconds())
	t.Logf("err                  : %v", err)
	t.Logf("ctx.Err              : %v", ctx.Err())
	t.Logf("gotDone              : %v", gotDone)
	t.Logf("events               : %v", events)
	t.Logf("progressTokensFallback: %v", progressTokensSeen)
	logMeta(t, doneMeta)

	if gotDone && doneMeta != nil {
		if doneMeta["totalTokensFallback"] != nil {
			t.Logf("✓ task_progress fallback tokens captured: %v", doneMeta["totalTokensFallback"])
		} else if doneMeta["inputTokens"] != nil {
			t.Logf("✓ result message tokens captured normally: input=%v output=%v", doneMeta["inputTokens"], doneMeta["outputTokens"])
		} else {
			t.Logf("✗ no token data available (neither result nor task_progress tokens)")
		}
	}
}


// helpers

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
	inputTokens := meta["inputTokens"]
	outputTokens := meta["outputTokens"]
	cost := meta["totalCostUsd"]
	durationMs := meta["durationMs"]
	t.Logf("metadata:")
	t.Logf("  inputTokens  : %v  (type: %T)", inputTokens, inputTokens)
	t.Logf("  outputTokens : %v  (type: %T)", outputTokens, outputTokens)
	t.Logf("  totalCostUsd : %v  (type: %T)", cost, cost)
	t.Logf("  durationMs   : %v", durationMs)
	if fallback := meta["totalTokensFallback"]; fallback != nil {
		t.Logf("  totalTokensFallback: %v  (task_progress fallback)", fallback)
	}
	if inputTokens == nil && outputTokens == nil && meta["totalTokensFallback"] == nil {
		t.Logf("  *** WARNING: token counts missing — billing will be inaccurate ***")
	}
	if v, ok := inputTokens.(int); ok && v > 0 {
		if o, ok2 := outputTokens.(int); ok2 {
			t.Logf("  total tokens : %d", v+o)
			if c, ok3 := cost.(float64); ok3 && c > 0 {
				t.Logf("  cost/token   : $%.8f", c/float64(v+o))
			}
		}
	}
	fmt.Println() // blank line for readability in raw go test output
}
