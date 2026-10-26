package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestIsSteerOutcomeUnknown pins the classification contract: only
// "never written" and "explicitly rejected" may fall back to the ordinary
// send path; every post-write failure must be treated as possibly delivered.
func TestIsSteerOutcomeUnknown(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		unknown bool
	}{
		{"request never written", fmt.Errorf("%w: write request turn/steer: broken pipe", errCodexRequestNotSent), false},
		{"rpc rejection", &codexRPCError{Method: "turn/steer", Code: -32600, Message: "turn not in progress"}, false},
		{"wrapped rpc rejection", fmt.Errorf("request failed: %w", &codexRPCError{Code: 1}), false},
		{"context deadline", context.DeadlineExceeded, true},
		{"context cancel", context.Canceled, true},
		{"connection closed while waiting", errors.New("codex connection closed while waiting for turn/steer: EOF"), true},
		{"response decode failure", errors.New("decode turn/steer result: unexpected end of JSON input"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSteerOutcomeUnknown(tc.err); got != tc.unknown {
				t.Fatalf("isSteerOutcomeUnknown(%v) = %v, want %v", tc.err, got, tc.unknown)
			}
		})
	}
}

// TestCodexRequest_CancelledContextNeverWrites: after Stop, an interjection
// queued behind the session lock holds an already-cancelled context. The
// request must be classified as not-sent and must never reach the app-server.
func TestCodexRequest_CancelledContextNeverWrites(t *testing.T) {
	cliPath, logPath := writeFakeCodexAppServer(t)
	t.Setenv("FAKE_CODEX_LOG", logPath)
	t.Setenv("FAKE_COMPLETE_ON_TURN_START", "0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewCodexAppServerClient(cliPath, nil)
	if err := client.Start(ctx, t.TempDir()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = client.Close() }()

	cancelledCtx, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	err := client.Request(cancelledCtx, "turn/steer", map[string]interface{}{"threadId": "t"}, nil)
	if err == nil {
		t.Fatal("Request with cancelled context succeeded")
	}
	if !errors.Is(err, errCodexRequestNotSent) {
		t.Fatalf("error = %v, want errCodexRequestNotSent", err)
	}
	if isSteerOutcomeUnknown(err) {
		t.Fatalf("cancelled-before-write must not be classified unknown: %v", err)
	}
	// Nothing may have reached the fake server's stdin.
	requests, _ := os.ReadFile(logPath)
	if strings.Contains(string(requests), "turn/steer") {
		t.Fatalf("cancelled request was written to the wire:\n%s", requests)
	}
}

// TestCodexAgent_StoppedTurnKeepsCheckpointEmpty: a turn interrupted by Stop
// never reaches a terminal state on the codex side, so its Result must not
// advertise a fork checkpoint (the UI hides the fork button instead of
// offering a fork that would fail).
func TestCodexAgent_StoppedTurnKeepsCheckpointEmpty(t *testing.T) {
	cliPath, logPath := writeFakeCodexAppServer(t)
	t.Setenv("FAKE_CODEX_LOG", logPath)
	t.Setenv("FAKE_COMPLETE_ON_TURN_START", "0") // turn never completes

	a := NewCodexAgent()
	a.CLIPath = cliPath
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registered := make(chan struct{}, 1)
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := a.SendMessage(ctx, Request{
			ConversationID: "nexbox-stop",
			WorkDir:        t.TempDir(),
			Message:        "start",
			RegisterInterject: func(func(string) error) {
				select {
				case registered <- struct{}{}:
				default:
				}
			},
		}, func(StreamEvent) {})
		done <- outcome{result, err}
	}()

	select {
	case <-registered: // the turn is running
	case o := <-done:
		t.Fatalf("SendMessage returned before turn start: %+v, err=%v", o.result, o.err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for turn start")
	}
	cancel() // user hits Stop

	select {
	case o := <-done:
		if !errors.Is(o.err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", o.err)
		}
		if o.result.Checkpoint != "" {
			t.Fatalf("stopped turn advertised checkpoint %q, want empty", o.result.Checkpoint)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SendMessage to return after cancel")
	}
}
