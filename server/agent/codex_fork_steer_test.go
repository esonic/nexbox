package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFakeCodexAppServer(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	scriptPath := filepath.Join(dir, "fake-codex.sh")
	script := `#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$FAKE_CODEX_LOG"
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"id":%s,"result":{}}\n' "$id"
      ;;
    *'"method":"thread/fork"'*)
      printf '{"id":%s,"result":{"thread":{"id":"thread-new"}}}\n' "$id"
      ;;
    *'"method":"thread/start"'*)
      printf '{"id":%s,"result":{"thread":{"id":"thread-new"}}}\n' "$id"
      ;;
    *'"method":"thread/resume"'*)
      printf '{"id":%s,"result":{"thread":{"id":"thread-new"}}}\n' "$id"
      ;;
    *'"method":"turn/start"'*)
      printf '{"id":%s,"result":{"turn":{"id":"turn-1"}}}\n' "$id"
      if [ "$FAKE_COMPLETE_ON_TURN_START" = "1" ]; then
        printf '%s\n' '{"method":"turn/completed","params":{"threadId":"thread-new","turn":{"id":"turn-1","status":"completed"}}}'
      fi
      ;;
    *'"method":"turn/steer"'*)
      printf '{"id":%s,"result":{"turnId":"turn-1"}}\n' "$id"
      printf '%s\n' '{"method":"turn/completed","params":{"threadId":"thread-new","turn":{"id":"turn-1","status":"completed"}}}'
      ;;
  esac
done
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	return scriptPath, logPath
}

func TestCodexAgentForksAtTurnBeforeStartingMessage(t *testing.T) {
	cliPath, logPath := writeFakeCodexAppServer(t)
	t.Setenv("FAKE_CODEX_LOG", logPath)
	t.Setenv("FAKE_COMPLETE_ON_TURN_START", "1")

	a := NewCodexAgent()
	a.CLIPath = cliPath
	a.Verbose = false
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	workDir := t.TempDir()

	result, err := a.SendMessage(ctx, Request{
		ConversationID: "beehub-fork",
		WorkDir:        workDir,
		Message:        "continue from fork",
		Fork: &ForkContext{
			SessionID:  "thread-parent",
			Checkpoint: "turn-previous",
		},
	}, func(StreamEvent) {})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if result.SessionID != "thread-new" {
		t.Fatalf("SessionID = %q, want thread-new", result.SessionID)
	}
	if result.Checkpoint != "turn-1" {
		t.Fatalf("checkpoint = %q, want turn-1", result.Checkpoint)
	}

	requests, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	wire := string(requests)
	for _, want := range []string{
		`"method":"thread/fork"`,
		`"threadId":"thread-parent"`,
		`"lastTurnId":"turn-previous"`,
		`"method":"turn/start"`,
		`"threadId":"thread-new"`,
	} {
		if !strings.Contains(wire, want) {
			t.Errorf("request log missing %s:\n%s", want, wire)
		}
	}
	if strings.Contains(wire, `"method":"thread/start"`) {
		t.Errorf("fork path unexpectedly started a fresh thread:\n%s", wire)
	}
}

func TestCodexAgentRegistersTurnSteerInterjector(t *testing.T) {
	cliPath, logPath := writeFakeCodexAppServer(t)
	t.Setenv("FAKE_CODEX_LOG", logPath)
	t.Setenv("FAKE_COMPLETE_ON_TURN_START", "0")

	a := NewCodexAgent()
	a.CLIPath = cliPath
	a.Verbose = false
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	workDir := t.TempDir()

	registered := make(chan func(string) error, 1)
	resultCh := make(chan Result, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := a.SendMessage(ctx, Request{
			ConversationID: "beehub-steer",
			WorkDir:        workDir,
			Message:        "start",
			RegisterInterject: func(inject func(string) error) {
				registered <- inject
			},
		}, func(StreamEvent) {})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	var inject func(string) error
	select {
	case inject = <-registered:
	case err := <-errCh:
		t.Fatalf("SendMessage before registration: %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for Codex interjector")
	}
	if err := inject("steer this turn"); err != nil {
		t.Fatalf("inject: %v", err)
	}

	select {
	case result := <-resultCh:
		if result.Checkpoint != "turn-1" {
			t.Fatalf("checkpoint = %q, want turn-1", result.Checkpoint)
		}
	case err := <-errCh:
		t.Fatalf("SendMessage: %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for steered turn completion")
	}

	requests, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	wire := string(requests)
	for _, want := range []string{
		`"method":"turn/steer"`,
		`"threadId":"thread-new"`,
		`"expectedTurnId":"turn-1"`,
		`"text":"steer this turn"`,
	} {
		if !strings.Contains(wire, want) {
			t.Errorf("request log missing %s:\n%s", want, wire)
		}
	}
}
