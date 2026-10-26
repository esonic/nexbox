package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/config"
	"github.com/esonic/nexbox/store"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
)

type localWorkspaceAgent struct{}

func (localWorkspaceAgent) Capabilities() agent.Capabilities { return agent.Capabilities{Fork: true} }
func (localWorkspaceAgent) SendMessage(_ context.Context, req agent.Request, cb agent.EventCallback) (agent.Result, error) {
	cb(agent.StreamEvent{Type: agent.EventTextDelta, Content: "Local workspace works"})
	return agent.Result{SessionID: "local-checkpoint", Checkpoint: "completed-turn"}, nil
}

func newOptionalAuthServer(t *testing.T) *Server {
	t.Helper()
	s := newRelayServer(t)
	s.Auth = NewAuthenticator()
	var err error
	s.Config, err = config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.Agent, s.AgentName = localWorkspaceAgent{}, "claude"
	s.Agents = map[string]agent.Agent{"claude": s.Agent}
	s.Hub.SessionOwnerLookup = func(id string) int64 {
		session, _ := s.Store.GetSession(id)
		if session != nil {
			project, _ := s.Store.GetProject(session.ProjectID)
			if project != nil {
				return project.OwnerUserID
			}
		}
		return 0
	}
	return s
}

func optionalAuthRequest(t *testing.T, s *Server, method, path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var data bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&data).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &data)
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	NewRouter(s).ServeHTTP(rr, req)
	return rr
}

func TestFreshInstanceWorksWithoutLogin(t *testing.T) {
	s := newOptionalAuthServer(t)
	for range 2 {
		status := optionalAuthRequest(t, s, http.MethodGet, "/api/auth/me", nil, &http.Cookie{Name: authCookieName, Value: "stale-cookie"})
		if status.Code != http.StatusOK || strings.TrimSpace(status.Body.String()) != `{"auth_required":false}` || len(status.Result().Cookies()) != 0 {
			t.Fatalf("local mode exposed an identity or required credentials: %d %s", status.Code, status.Body.String())
		}
	}
	users, _ := s.Store.ListUsers()
	if len(users) != 0 {
		t.Fatal("fresh instance created a login account")
	}
	projects := optionalAuthRequest(t, s, http.MethodGet, "/api/projects/", nil)
	var list []store.Project
	if err := json.Unmarshal(projects.Body.Bytes(), &list); err != nil || projects.Code != 200 || len(list) != 1 {
		t.Fatalf("default workspace: %d %s", projects.Code, projects.Body.String())
	}
	project := list[0]
	if project.Name != "Default" || project.OwnerUserID <= 0 {
		t.Fatalf("default workspace lacks its internal owner: %+v", project)
	}
	if _, err := os.Stat(project.GitRepoPath); err != nil {
		t.Fatal(err)
	}
	session, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "Local session", ProjectID: project.ID, Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewRouter(s))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("chat needs login: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(map[string]string{"type": "user_message", "session_id": session.ID, "content": "hello"}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var event TaggedEvent
		if err := conn.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "done" {
			break
		}
	}
	messages, _ := s.Store.ListMessages(session.ID)
	if len(messages) != 2 || messages[1].Metadata.AgentCheckpoint == "" {
		t.Fatalf("local conversation was not persisted: %+v", messages)
	}
	fork := optionalAuthRequest(t, s, http.MethodPost, "/api/sessions/"+session.ID+"/fork", map[string]string{"message_id": messages[1].ID})
	if fork.Code != http.StatusCreated {
		t.Fatalf("local fork: %d %s", fork.Code, fork.Body.String())
	}
	if _, _, err := s.CreateSiblingSession(session.ID, "sibling", "hello", "", ""); err != nil {
		t.Fatalf("local cross-session collaboration failed: %v", err)
	}
}

func TestAddingAccountEnablesLoginWithoutLosingLocalWork(t *testing.T) {
	s := newOptionalAuthServer(t)
	optionalAuthRequest(t, s, http.MethodGet, "/api/auth/me", nil)
	projects, _ := s.Store.ListProjects()
	project := projects[0]
	file := filepath.Join(project.GitRepoPath, "keep.txt")
	if err := os.WriteFile(file, []byte("existing work"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := s.Store.CreateSession(store.CreateSessionRequest{ProjectID: project.ID, Name: "terminal"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewRouter(s))
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	chat, _, err := websocket.DefaultDialer.Dial(url+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer chat.Close()
	terminal, _, err := websocket.DefaultDialer.Dial(url+"/ws/terminal/sessions/"+session.ID, nil)
	if err != nil {
		t.Fatalf("local terminal needs login: %v", err)
	}
	defer terminal.Close()
	t.Cleanup(func() {
		termRegistryMu.Lock()
		ts := termRegistry[session.ID]
		termRegistryMu.Unlock()
		if ts != nil {
			ts.shutdown()
		}
	})
	hash, err := bcrypt.GenerateFromPassword([]byte(t.Name()), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.Store.CreateUser("owner", string(hash))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/auth/me", "/api/projects/", "/api/sessions/"} {
		if rr := optionalAuthRequest(t, s, http.MethodGet, path, nil); rr.Code != http.StatusUnauthorized {
			t.Fatalf("account did not protect %s: %d", path, rr.Code)
		}
	}
	for _, conn := range []*websocket.Conn{chat, terminal} {
		_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
					t.Fatalf("old local connection not invalidated: %v", err)
				}
				break
			}
		}
	}
	if conn, response, err := websocket.DefaultDialer.Dial(url+"/ws", nil); err == nil {
		conn.Close()
		t.Fatal("new unauthenticated WebSocket was accepted")
	} else if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unexpected WebSocket rejection: %v", err)
	}
	login := optionalAuthRequest(t, s, http.MethodPost, "/api/login", map[string]string{"username": "owner", "password": t.Name()})
	if login.Code != http.StatusOK || len(login.Result().Cookies()) != 1 {
		t.Fatalf("login failed: %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	status := optionalAuthRequest(t, s, http.MethodGet, "/api/auth/me", nil, cookie)
	var info loginResponse
	if err := json.Unmarshal(status.Body.Bytes(), &info); err != nil || !info.AuthRequired || info.UserID != user.ID {
		t.Fatalf("authenticated status: %s", status.Body.String())
	}
	owned, _ := s.Store.ListProjectsByOwner(user.ID)
	if len(owned) != 1 || owned[0].ID != project.ID || owned[0].GitRepoPath != project.GitRepoPath {
		t.Fatal("first login lost or moved the local workspace")
	}
	rename := optionalAuthRequest(t, s, http.MethodPut, "/api/projects/"+project.ID, map[string]string{"name": "renamed"}, cookie)
	if rename.Code != http.StatusOK {
		t.Fatalf("adopted project cannot be renamed: %d %s", rename.Code, rename.Body.String())
	}
	if err := s.Store.DeleteUser(user.ID); err != nil {
		t.Fatal(err)
	}
	if rr := optionalAuthRequest(t, s, http.MethodGet, "/api/auth/me", nil); rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != `{"auth_required":false}` {
		t.Fatalf("removing last account did not restore local access: %s", rr.Body.String())
	}
	local, _ := s.Store.EnsureLocalUser()
	owned, _ = s.Store.ListProjectsByOwner(local.ID)
	if len(owned) != 1 || owned[0].ID != project.ID {
		t.Fatal("returning to local mode lost the project")
	}
	if data, err := os.ReadFile(filepath.Join(owned[0].GitRepoPath, "keep.txt")); err != nil || string(data) != "existing work" {
		t.Fatal("switching access modes lost project files")
	}
}
