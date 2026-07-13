package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/esonic/nexbox/agent"
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
	st := store.NewSQLiteStore(db)
	// nexbox is multi-user: doRequest attaches an auth cookie for the seeded
	// "tester" user, so the gate tests need an Authenticator and that user.
	if _, err := st.CreateUser("tester", "ignored"); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	cfg, err := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	claude := agent.NewClaudeAgent()
	codex := agent.NewCodexAgent()
	cursor := agent.NewCursorAgent()
	return &Server{
		Store:  st,
		Auth:   NewAuthenticator(),
		Hub:    NewChatHub(),
		Config: cfg,
		Agent:  claude,
		Agents: map[string]agent.Agent{
			"claude": claude,
			"codex":  codex,
			"cursor": cursor,
		},
		AgentName: "claude",
		DataDir:   t.TempDir(),
	}
}

func TestForkSession_UsesMessageIDAndOpaqueCheckpoint(t *testing.T) {
	s := newAgentGateServer(t)
	parent, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "codex", Agent: "codex"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.Store.UpdateSessionAgentID(parent.ID, "thread-parent"); err != nil {
		t.Fatalf("UpdateSessionAgentID: %v", err)
	}
	message, err := s.Store.CreateMessage(parent.ID, "assistant",
		[]store.MessagePart{{Type: "text", Content: "done"}},
		store.MessageMetadata{AgentCheckpoint: "opaque-turn-2"})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	rr := doRequest(t, s, http.MethodPost, "/api/sessions/"+parent.ID+"/fork", map[string]string{
		"message_id": message.ID,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var forked store.Session
	if err := json.NewDecoder(rr.Body).Decode(&forked); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if forked.ForkedFromMessageID != message.ID || forked.ForkedAtAgentCheckpoint != "opaque-turn-2" {
		t.Fatalf("unexpected fork lineage: %+v", forked)
	}
}

func TestForkSession_RequiresCheckpoint(t *testing.T) {
	s := newAgentGateServer(t)
	parent, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "codex", Agent: "codex"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	message, err := s.Store.CreateMessage(parent.ID, "assistant",
		[]store.MessagePart{{Type: "text", Content: "done"}}, store.MessageMetadata{})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	rr := doRequest(t, s, http.MethodPost, "/api/sessions/"+parent.ID+"/fork", map[string]string{"message_id": message.ID})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestForkSession_UsesCapabilityInsteadOfAgentName(t *testing.T) {
	s := newAgentGateServer(t)
	s.Agents["future-agent"] = agent.NewCodexAgent()
	parent, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "future", Agent: "future-agent"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	message, _ := s.Store.CreateMessage(parent.ID, "assistant",
		[]store.MessagePart{{Type: "text", Content: "done"}},
		store.MessageMetadata{AgentCheckpoint: "opaque"})
	rr := doRequest(t, s, http.MethodPost, "/api/sessions/"+parent.ID+"/fork", map[string]string{"message_id": message.ID})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
}

func TestForkSession_RejectsAgentWithoutForkCapability(t *testing.T) {
	s := newAgentGateServer(t)
	parent, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "cursor", Agent: "cursor"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	message, _ := s.Store.CreateMessage(parent.ID, "assistant",
		[]store.MessagePart{{Type: "text", Content: "done"}},
		store.MessageMetadata{AgentCheckpoint: "opaque"})
	rr := doRequest(t, s, http.MethodPost, "/api/sessions/"+parent.ID+"/fork", map[string]string{"message_id": message.ID})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestListAvailableAgents_IncludesCapabilities(t *testing.T) {
	s := newAgentGateServer(t)
	rr := doRequest(t, s, http.MethodGet, "/api/available-agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Capabilities map[string]agent.Capabilities `json:"capabilities"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Capabilities["codex"].Fork || !body.Capabilities["codex"].Interject {
		t.Fatalf("codex capabilities = %+v", body.Capabilities["codex"])
	}
	if body.Capabilities["cursor"].Fork || body.Capabilities["cursor"].Interject {
		t.Fatalf("cursor capabilities = %+v", body.Capabilities["cursor"])
	}
}

// TestPersistSessionAgent_StampsEmptyAgent guards the whole-session fork-button
// regression: a session created without an agent (as scratch sessions are) must
// get the resolved agent written to its row the first time it is used, so the
// frontend can resolve the selected agent's capabilities. This is the
// source-of-truth fix; there is deliberately no read-time fallback for legacy rows.
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
