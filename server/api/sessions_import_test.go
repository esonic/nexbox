package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/store"
)

func TestImportSession_ClaudeEndToEnd(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	repo := t.TempDir()
	s := newAgentGateServer(t)
	user, err := s.Store.GetUserByUsername("tester")
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.Store.CreateProject(store.CreateProjectRequest{Name: "p", GitRepoPath: repo, OwnerUserID: user.ID})
	if err != nil {
		t.Fatal(err)
	}

	enc := []rune{}
	for _, r := range repo {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			enc = append(enc, r)
		} else {
			enc = append(enc, '-')
		}
	}
	dir := filepath.Join(cfg, "projects", string(enc))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "aaaaaaaa-0000-0000-0000-000000000001"
	body := `{"type":"user","uuid":"u1","cwd":"` + repo + `","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"hello"}}` + "\n" +
		`{"type":"assistant","uuid":"a1","timestamp":"2026-01-01T00:00:01Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	list := func() []agent.ImportableSession {
		rr := doRequest(t, s, http.MethodGet, "/api/sessions/importable?agent=claude&project_id="+project.ID, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("list status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var out struct {
			Sessions []agent.ImportableSession `json:"sessions"`
		}
		if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out.Sessions
	}
	if got := list(); len(got) != 1 || got[0].ID != id {
		t.Fatalf("importable = %+v", got)
	}

	req := map[string]string{"project_id": project.ID, "agent": "claude", "agent_session_id": id}
	rr := doRequest(t, s, http.MethodPost, "/api/sessions/import", req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("import status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var sess store.Session
	if err := json.NewDecoder(rr.Body).Decode(&sess); err != nil {
		t.Fatal(err)
	}
	if sess.AgentSessionID != id || sess.Agent != "claude" || sess.ProjectID != project.ID || sess.Name != "hello" {
		t.Fatalf("session = %+v", sess)
	}
	msgs, _ := s.Store.ListMessages(sess.ID)
	if len(msgs) != 2 || msgs[1].Metadata.AgentCheckpoint != "a1" {
		t.Fatalf("messages = %+v", msgs)
	}

	// Already in BeeHub: hidden from the list and rejected on re-import.
	if got := list(); len(got) != 0 {
		t.Fatalf("imported session still listed: %+v", got)
	}
	if rr := doRequest(t, s, http.MethodPost, "/api/sessions/import", req); rr.Code != http.StatusConflict {
		t.Fatalf("re-import status = %d", rr.Code)
	}
	// Trashed sessions still count as known.
	if err := s.Store.DeleteSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("trashed session re-offered: %+v", got)
	}

	// Unsupported agent.
	req["agent"] = "cursor"
	if rr := doRequest(t, s, http.MethodPost, "/api/sessions/import", req); rr.Code != http.StatusBadRequest {
		t.Fatalf("unsupported agent status = %d", rr.Code)
	}
}

func TestImportSessionRejectsOtherOwnersProject(t *testing.T) {
	s := newAgentGateServer(t)
	other, err := s.Store.CreateUser("other", "unused-test-hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.Store.CreateProject(store.CreateProjectRequest{Name: "other", GitRepoPath: t.TempDir(), OwnerUserID: other.ID})
	if err != nil {
		t.Fatal(err)
	}
	if rr := doRequest(t, s, http.MethodGet, "/api/sessions/importable?agent=claude&project_id="+project.ID, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("list status = %d: %s", rr.Code, rr.Body.String())
	}
	req := map[string]string{"project_id": project.ID, "agent": "claude", "agent_session_id": "native-id"}
	if rr := doRequest(t, s, http.MethodPost, "/api/sessions/import", req); rr.Code != http.StatusNotFound {
		t.Fatalf("import status = %d: %s", rr.Code, rr.Body.String())
	}
}
