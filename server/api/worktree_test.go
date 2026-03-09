package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"gitlab.okg.com/sre_ai/boxnex/config"
	"gitlab.okg.com/sre_ai/boxnex/store"
)

const testJWTSecret = "boxnex-test-secret"

// makeTestJWT creates a signed JWT token for use in tests.
func makeTestJWT(t *testing.T) string {
	t.Helper()
	claims := jwtClaims{
		UserID:   "test-user-id",
		Username: "testuser",
		Role:     "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	return signed
}

// newWorktreeServer creates a minimal Server backed by an in-memory SQLite store.
func newWorktreeServer(t *testing.T) *Server {
	t.Helper()
	db, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cfgMgr, err := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	if err := cfgMgr.Update(func(cfg *config.Config) {
		cfg.JWTSecret = testJWTSecret
	}); err != nil {
		t.Fatalf("config.Update: %v", err)
	}
	return &Server{
		Store:   store.NewSQLiteStore(db.DB),
		Hub:     NewChatHub(),
		DataDir: t.TempDir(),
		Config:  cfgMgr,
	}
}

// createTestSession creates a project and a session bound to it, returning the session ID.
// SetWorktreeMode requires the session to be bound to a project.
func createTestSession(t *testing.T, s *Server, name string) string {
	t.Helper()
	repoDir := t.TempDir()
	// Init a bare git repo so worktree operations have a valid repo root.
	if out, err2 := exec.Command("git", "init", repoDir).CombinedOutput(); err2 != nil {
		t.Fatalf("git init: %v: %s", err2, out)
	}
	proj, err := s.Store.CreateProject(store.CreateProjectRequest{Name: name + "-proj", GitRepoPath: repoDir})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	sess, err := s.Store.CreateSession(store.CreateSessionRequest{Name: name, ProjectID: proj.ID})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return sess.ID
}

// doRequest performs a request against the Chi router and returns the response.
func doRequest(t *testing.T, s *Server, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+makeTestJWT(t))
	rr := httptest.NewRecorder()
	NewRouter(s).ServeHTTP(rr, req)
	return rr
}

// ---------------------------------------------------------------------------
// SetWorktreeMode tests
// ---------------------------------------------------------------------------

func TestSetWorktreeMode_Success(t *testing.T) {
	s := newWorktreeServer(t)
	id := createTestSession(t, s, "wt-mode-ok")

	rr := doRequest(t, s, http.MethodPut,
		"/api/sessions/"+id+"/worktree-mode",
		map[string]string{"mode": "worktree"})

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rr.Code, http.StatusOK, rr.Body)
	}
	var sess store.Session
	if err := json.NewDecoder(rr.Body).Decode(&sess); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if sess.WorktreeMode != "worktree" {
		t.Errorf("WorktreeMode = %q, want %q", sess.WorktreeMode, "worktree")
	}

	// Verify persisted in DB
	got, _ := s.Store.GetSession(id)
	if got.WorktreeMode != "worktree" {
		t.Errorf("persisted WorktreeMode = %q, want %q", got.WorktreeMode, "worktree")
	}
}

func TestSetWorktreeMode_ResetToLocal(t *testing.T) {
	s := newWorktreeServer(t)
	id := createTestSession(t, s, "wt-mode-reset")
	_ = s.Store.UpdateSessionWorktree(id, "worktree", "")

	rr := doRequest(t, s, http.MethodPut,
		"/api/sessions/"+id+"/worktree-mode",
		map[string]string{"mode": "local"})

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rr.Code, http.StatusOK, rr.Body)
	}
	got, _ := s.Store.GetSession(id)
	if got.WorktreeMode != "local" {
		t.Errorf("WorktreeMode = %q, want %q", got.WorktreeMode, "local")
	}
}

func TestSetWorktreeMode_InvalidMode(t *testing.T) {
	s := newWorktreeServer(t)
	id := createTestSession(t, s, "wt-mode-bad")

	rr := doRequest(t, s, http.MethodPut,
		"/api/sessions/"+id+"/worktree-mode",
		map[string]string{"mode": "invalid"})

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestSetWorktreeMode_NotFound(t *testing.T) {
	s := newWorktreeServer(t)

	rr := doRequest(t, s, http.MethodPut,
		"/api/sessions/nonexistent/worktree-mode",
		map[string]string{"mode": "worktree"})

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestSetWorktreeMode_LockedAfterFirstMessage(t *testing.T) {
	s := newWorktreeServer(t)
	id := createTestSession(t, s, "wt-mode-locked")
	// Simulate first message by setting AgentSessionID
	if err := s.Store.UpdateSessionAgentID(id, "some-agent-session"); err != nil {
		t.Fatalf("UpdateSessionAgentID: %v", err)
	}

	rr := doRequest(t, s, http.MethodPut,
		"/api/sessions/"+id+"/worktree-mode",
		map[string]string{"mode": "worktree"})

	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusConflict)
	}
}

func TestSetWorktreeMode_InvalidJSON(t *testing.T) {
	s := newWorktreeServer(t)
	id := createTestSession(t, s, "wt-mode-badjson")

	req := httptest.NewRequest(http.MethodPut,
		"/api/sessions/"+id+"/worktree-mode",
		bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+makeTestJWT(t))
	rr := httptest.NewRecorder()
	NewRouter(s).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

// ---------------------------------------------------------------------------
// GetWorktreeStatus tests
// ---------------------------------------------------------------------------

func TestGetWorktreeStatus_LocalSession(t *testing.T) {
	s := newWorktreeServer(t)
	id := createTestSession(t, s, "wt-status-local")

	rr := doRequest(t, s, http.MethodGet,
		"/api/sessions/"+id+"/worktree-status", nil)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rr.Code, http.StatusOK, rr.Body)
	}
	var resp map[string]interface{}
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp["dirty"] != false {
		t.Errorf("dirty = %v, want false", resp["dirty"])
	}
}

func TestGetWorktreeStatus_WorktreeModeNoPath(t *testing.T) {
	s := newWorktreeServer(t)
	id := createTestSession(t, s, "wt-status-nopath")
	_ = s.Store.UpdateSessionWorktree(id, "worktree", "") // mode=worktree but path not yet set

	rr := doRequest(t, s, http.MethodGet,
		"/api/sessions/"+id+"/worktree-status", nil)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp map[string]interface{}
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp["dirty"] != false {
		t.Errorf("dirty = %v, want false", resp["dirty"])
	}
}

func TestGetWorktreeStatus_NotFound(t *testing.T) {
	s := newWorktreeServer(t)

	rr := doRequest(t, s, http.MethodGet,
		"/api/sessions/nonexistent/worktree-status", nil)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestGetWorktreeStatus_CleanWorktree(t *testing.T) {
	// Create a real git repo + worktree so isWorktreeDirty can be tested end-to-end.
	base := testTempDir(t)
	repoDir := filepath.Join(base, "repo")
	wtDir := filepath.Join(base, "worktrees", "sess0001")

	mustGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Bare-minimum git repo setup
	mustGit(base, "init", repoDir)
	mustGit(repoDir, "config", "user.email", "test@test.com")
	mustGit(repoDir, "config", "user.name", "Test")
	// Initial commit so we have HEAD
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hi"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustGit(repoDir, "add", ".")
	mustGit(repoDir, "commit", "-m", "init")

	// Create the worktree on a new branch
	if err := os.MkdirAll(filepath.Dir(wtDir), 0755); err != nil {
		t.Fatalf("mkdirall: %v", err)
	}
	mustGit(repoDir, "worktree", "add", "-b", "sess0001", wtDir)

	// Worktree should be clean after creation
	if isWorktreeDirty(wtDir) {
		t.Error("newly created worktree should not be dirty")
	}

	// Add an uncommitted file
	if err := os.WriteFile(filepath.Join(wtDir, "change.txt"), []byte("change"), 0644); err != nil {
		t.Fatalf("write change: %v", err)
	}
	mustGit(wtDir, "add", ".")

	if !isWorktreeDirty(wtDir) {
		t.Error("worktree with staged file should be dirty")
	}
}

// ---------------------------------------------------------------------------
// isWorktreeDirty unit tests
// ---------------------------------------------------------------------------

func TestIsWorktreeDirty_NonExistentPath(t *testing.T) {
	// A path that doesn't exist — git commands will fail, result should be false (not dirty).
	dirty := isWorktreeDirty("/nonexistent/path/that/does/not/exist")
	if dirty {
		t.Error("non-existent path should not be dirty")
	}
}

// ---------------------------------------------------------------------------
// RemoveWorktree unit tests
// ---------------------------------------------------------------------------

func TestRemoveWorktree_Success(t *testing.T) {
	base := testTempDir(t)
	repoDir := filepath.Join(base, "repo")
	wtDir := filepath.Join(base, "worktrees", "rmtest")

	mustGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	mustGit(base, "init", repoDir)
	mustGit(repoDir, "config", "user.email", "test@test.com")
	mustGit(repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hi"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustGit(repoDir, "add", ".")
	mustGit(repoDir, "commit", "-m", "init")
	if err := os.MkdirAll(filepath.Dir(wtDir), 0755); err != nil {
		t.Fatalf("mkdirall: %v", err)
	}
	mustGit(repoDir, "worktree", "add", "-b", "rmtest", wtDir)

	// Verify worktree exists
	if _, err := os.Stat(wtDir); err != nil {
		t.Fatalf("worktree dir should exist: %v", err)
	}

	if err := RemoveWorktree(wtDir); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	// Directory should be gone
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Error("worktree dir should have been removed")
	}
}

func TestRemoveWorktree_NonExistent(t *testing.T) {
	err := RemoveWorktree("/nonexistent/worktree/path")
	if err == nil {
		t.Error("expected error when removing non-existent worktree")
	}
}
