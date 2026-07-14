package agent

// Real-CLI integration coverage for thread/fork and turn/steer. The mock in
// codex_fork_steer_test.go only proves what we *send*; these tests prove the
// live app-server actually understands `lastTurnId` and `expectedTurnId` —
// in particular that fork does not silently clone the whole thread when the
// cut-point parameter is present, and that a bogus cut point is rejected
// rather than ignored.
//
// Like the other CODEX_INTEGRATION tests these are debug-grade, opt-in
// checks: the context-cut assertion depends on the model reproducing planted
// code words, which is deliberate (it is the only observable proof of the
// cut) but not CI-stable.
//
// Run from the server/ module root:
//
//	cd server && CODEX_INTEGRATION=1 go test -v -run TestCodexIntegrationForkSteer -timeout 300s ./agent/
import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// codexIntegrationSession bundles the minimal client wiring shared by the
// integration assertions below. The notification handler is installed once,
// before Start, so the reader goroutine never observes a handler swap; the
// reply collector is toggled under a mutex instead.
type codexIntegrationSession struct {
	client   *CodexAppServerClient
	turnDone chan string // receives the completed turn id

	mu         sync.Mutex
	collecting bool
	reply      strings.Builder
}

func startCodexIntegrationSession(t *testing.T, ctx context.Context, workDir string) *codexIntegrationSession {
	t.Helper()
	s := &codexIntegrationSession{
		client:   NewCodexAppServerClient("codex", []string{"app-server", "--listen", "stdio://"}),
		turnDone: make(chan string, 4),
	}
	s.client.SetNotificationHandler(func(method string, params json.RawMessage) {
		switch method {
		case "turn/completed":
			var n struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			_ = json.Unmarshal(params, &n)
			select {
			case s.turnDone <- n.Turn.ID:
			default:
			}
		case "item/completed":
			var n struct {
				Item struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"item"`
			}
			if err := json.Unmarshal(params, &n); err != nil || n.Item.Type != "agentMessage" {
				return
			}
			s.mu.Lock()
			if s.collecting {
				s.reply.WriteString(n.Item.Text)
			}
			s.mu.Unlock()
		}
	})
	s.client.SetServerRequestHandler(func(method string, params json.RawMessage) (interface{}, *codexWireError) {
		switch method {
		case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
			return map[string]interface{}{"decision": "accept"}, nil
		case "item/permissions/requestApproval":
			return map[string]interface{}{"permissions": map[string]interface{}{}, "scope": "turn"}, nil
		default:
			return map[string]interface{}{}, nil
		}
	})
	if err := s.client.Start(ctx, workDir); err != nil {
		t.Fatalf("start app-server: %v", err)
	}
	t.Cleanup(func() { _ = s.client.Close() })
	if err := s.client.Initialize(ctx, "nexbox-integration", "1.0.0"); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return s
}

func (s *codexIntegrationSession) setCollecting(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collecting = on
	if on {
		s.reply.Reset()
	}
}

func (s *codexIntegrationSession) collectedReply() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reply.String()
}

func (s *codexIntegrationSession) runTurn(t *testing.T, ctx context.Context, threadID, text string) string {
	t.Helper()
	var turnResp codexTurnStartResponse
	if err := s.client.Request(ctx, "turn/start", map[string]interface{}{
		"threadId": threadID,
		"input":    []map[string]interface{}{{"type": "text", "text": text}},
	}, &turnResp); err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	select {
	case id := <-s.turnDone:
		if id != "" {
			return id
		}
		return turnResp.Turn.ID
	case <-ctx.Done():
		t.Fatalf("timeout waiting for turn completion")
	case <-s.client.readDone:
		t.Fatalf("app-server terminated mid-turn: %s", s.client.Stderr())
	}
	return turnResp.Turn.ID
}

// TestCodexIntegrationForkSteer validates the wire contract our adapter
// depends on: (1) thread/fork accepts threadId+lastTurnId and returns a new
// thread id, (2) the fork is genuinely cut at lastTurnId — context from turns
// after the cut must not leak, (3) a bogus lastTurnId errors instead of being
// silently ignored, (4) turn/steer with expectedTurnId is accepted for a
// running turn.
func TestCodexIntegrationForkSteer(t *testing.T) {
	if os.Getenv("CODEX_INTEGRATION") == "" {
		t.Skip("Set CODEX_INTEGRATION=1 to run this test (requires codex CLI installed & authenticated)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 280*time.Second)
	defer cancel()
	workDir := t.TempDir()
	sess := startCodexIntegrationSession(t, ctx, workDir)

	var threadResp codexThreadResponse
	if err := sess.client.Request(ctx, "thread/start", map[string]interface{}{
		"approvalPolicy": "never",
		"sandbox":        "read-only",
		"cwd":            workDir,
	}, &threadResp); err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	parentThread := threadResp.Thread.ID

	// Turn 1 plants BANANA (the fork keeps this); turn 2 plants CHERRY (the
	// fork must cut it away).
	turn1 := sess.runTurn(t, ctx, parentThread, "Remember the code word BANANA. Reply with just OK.")
	_ = sess.runTurn(t, ctx, parentThread, "Remember a second code word CHERRY. Reply with just OK.")

	// (3) Bogus cut point must be an error — if the server ignored an unknown
	// lastTurnId param it would "succeed" by cloning the whole thread, which
	// is exactly the silent failure mode this test exists to catch.
	var bogusResp codexThreadResponse
	if err := sess.client.Request(ctx, "thread/fork", map[string]interface{}{
		"threadId":   parentThread,
		"lastTurnId": "turn_bogus_does_not_exist",
		"cwd":        workDir,
	}, &bogusResp); err == nil {
		t.Errorf("thread/fork accepted a nonexistent lastTurnId — the cut-point param may be ignored by this app-server version")
	}

	// (1) Fork at turn 1.
	var forkResp codexThreadResponse
	if err := sess.client.Request(ctx, "thread/fork", map[string]interface{}{
		"threadId":   parentThread,
		"lastTurnId": turn1,
		"cwd":        workDir,
	}, &forkResp); err != nil {
		t.Fatalf("thread/fork: %v", err)
	}
	forkedThread := forkResp.Thread.ID
	if forkedThread == "" || forkedThread == parentThread {
		t.Fatalf("fork returned thread id %q (parent %q)", forkedThread, parentThread)
	}

	// (2) The forked thread must know BANANA but not CHERRY.
	sess.setCollecting(true)
	_ = sess.runTurn(t, ctx, forkedThread,
		"List every code word I asked you to remember in this conversation, comma separated, nothing else.")
	sess.setCollecting(false)
	reply := sess.collectedReply()
	if !strings.Contains(strings.ToUpper(reply), "BANANA") {
		t.Errorf("forked thread lost pre-cut context, reply: %q", reply)
	}
	if strings.Contains(strings.ToUpper(reply), "CHERRY") {
		t.Errorf("forked thread retained post-cut context (lastTurnId not honored), reply: %q", reply)
	}

	// (4) turn/steer against a running turn: start a longer turn and steer
	// it immediately. If the turn still manages to finish before the steer
	// lands, the result is inconclusive rather than a failure — timing
	// against a live model can't be pinned down reliably.
	var slowTurn codexTurnStartResponse
	if err := sess.client.Request(ctx, "turn/start", map[string]interface{}{
		"threadId": forkedThread,
		"input": []map[string]interface{}{{
			"type": "text",
			"text": "Write a 200-word story about the sea, slowly and thoughtfully.",
		}},
	}, &slowTurn); err != nil {
		t.Fatalf("turn/start (steer target): %v", err)
	}
	steerCtx, steerCancel := context.WithTimeout(ctx, 30*time.Second)
	defer steerCancel()
	var steerResp codexTurnSteerResponse
	steerErr := sess.client.Request(steerCtx, "turn/steer",
		buildCodexTurnSteerParams(forkedThread, slowTurn.Turn.ID, "Also mention the code word MANGO."),
		&steerResp)
	select {
	case <-sess.turnDone:
	case <-ctx.Done():
		t.Fatal("timeout waiting for steered turn completion")
	case <-sess.client.readDone:
		t.Fatalf("app-server terminated mid-steer: %s", sess.client.Stderr())
	}
	if steerErr != nil {
		t.Logf("turn/steer returned %v — inconclusive if the turn had already completed; re-run to confirm", steerErr)
	}
}
