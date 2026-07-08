package api

import (
	"path/filepath"
	"testing"

	"github.com/esonic/nexbox/config"
	"github.com/esonic/nexbox/store"
)

// newAgentGateServer builds a Server whose ResolveAgent falls back to "claude",
// mirroring a stock local install. A real config.Manager is wired in because
// other Server helpers dereference s.Config.
func newAgentGateServer(t *testing.T) *Server {
	t.Helper()
	db, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cfg, err := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	return &Server{
		Store:     store.NewSQLiteStore(db),
		Hub:       NewChatHub(),
		Config:    cfg,
		AgentName: "claude",
		DataDir:   t.TempDir(),
	}
}

// TestPersistSessionAgent_StampsEmptyAgent guards the whole-session fork-button
// regression: a session created without an agent (as scratch sessions are) must
// get the resolved agent written to its row the first time it is used, so the
// frontend — which gates the fork button on agent==="claude" — sees a concrete
// agent. This is the source-of-truth fix; there is deliberately no read-time
// fallback for legacy rows.
func TestPersistSessionAgent_StampsEmptyAgent(t *testing.T) {
	s := newAgentGateServer(t)

	sess, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "scratch"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Agent != "" {
		t.Fatalf("precondition: expected empty agent, got %q", sess.Agent)
	}

	s.persistSessionAgent(sess)

	if sess.Agent != "claude" {
		t.Errorf("in-memory session.Agent = %q, want %q", sess.Agent, "claude")
	}
	got, err := s.Store.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Agent != "claude" {
		t.Errorf("persisted agent = %q, want %q (fork button stays hidden otherwise)", got.Agent, "claude")
	}
}

// TestPersistSessionAgent_LeavesExistingAgent verifies the helper never
// clobbers an agent the user actually chose.
func TestPersistSessionAgent_LeavesExistingAgent(t *testing.T) {
	s := newAgentGateServer(t)

	sess, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "chosen", Agent: "codex"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	s.persistSessionAgent(sess)

	if sess.Agent != "codex" {
		t.Errorf("session.Agent = %q, want %q (must not overwrite a chosen agent)", sess.Agent, "codex")
	}
}
