package api

import (
	"fmt"
	"testing"

	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/store"
)

// TestHandleInterject_UnknownOutcomePersistsOnce: when the injector reports
// ErrInterjectOutcomeUnknown, the message may already be folded into the live
// turn — it must be persisted exactly once (no queue/send fallback that could
// deliver it twice, no transcript gap if it did land).
func TestHandleInterject_UnknownOutcomePersistsOnce(t *testing.T) {
	s := newAgentGateServer(t)
	s.SessionManager = agent.NewSessionManager()

	sess, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "codex", Agent: "codex"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	s.SessionManager.SetInterjector(sess.ID, func(string) error {
		return fmt.Errorf("%w: steer codex turn: context deadline exceeded", agent.ErrInterjectOutcomeUnknown)
	})

	s.handleInterject(sess.ID, "mention MANGO too", nil, nil, nil)

	msgs, err := s.Store.ListMessages(sess.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("persisted %d messages, want exactly 1 (no double delivery, no gap)", len(msgs))
	}
	if msgs[0].Role != "user" || !msgs[0].Metadata.Interjected {
		t.Fatalf("unexpected persisted message: role=%s metadata=%+v", msgs[0].Role, msgs[0].Metadata)
	}
}
