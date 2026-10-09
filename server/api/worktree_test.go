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

	"github.com/esonic/nexbox/store"
)

// newWorktreeServer creates a minimal Server backed by an in-memory SQLite store
// with auth pre-seeded for HTTP-level tests.
func newWorktreeServer(t *testing.T) *Server {
	t.Helper()
	db, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{
		Store:   store.NewSQLiteStore(db),
		Auth:    NewAuthenticator(),
		Hub:     NewChatHub(),
		DataDir: t.TempDir(),
	}
	if _, err := s.Store.CreateUser("tester", "ignored"); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	return s
}

// testAuthCookie issues a JWT for the seeded test user and returns it as an
// http.Cookie that can be attached to a httptest request.
func testAuthCookie(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	u, err := s.Store.GetUserByUsername("tester")
	if err != nil {
		t.Fatalf("get test user: %v", err)
	}
	tok, err := s.Auth.issue(u)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return &http.Cookie{Name: authCookieName, Value: tok}
}

// createTestSession inserts a session and returns its ID.
func createTestSession(t *testing.T, s *Server, name string) string {
	t.Helper()
	sess, err := s.Store.CreateSession(store.CreateSessionRequest{Name: name})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return sess.ID
}

// doRequest performs a request against the Chi router and returns the response.
// Requests are auto-authenticated with the seeded test user.
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
	req.AddCookie(testAuthCookie(t, s))
	rr := httptest.NewRecorder()
	NewRouter(s).ServeHTTP(rr, req)
	return rr
}

// ---------------------------------------------------------------------------
// SetWorktreeMode tests
// ---------------------------------------------------------------------------

func TestSetWorktreeMode_Success(t *testing.T) {
	s := newWorktreeServer(t)

	// Create a temporary git repository so the handler's git validation passes.
	gitDir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = gitDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	// Create a project pointing to the git repo.
	proj, err := s.Store.CreateProject(store.CreateProjectRequest{
		Name:        "wt-proj",
		GitRepoPath: gitDir,
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Create a session bound to that project.
	sess, err := s.Store.CreateSession(store.CreateSessionRequest{
		Name:      "wt-mode-ok",
		ProjectID: proj.ID,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id := sess.ID

	rr := doRequest(t, s, http.MethodPut,
		"/api/sessions/"+id+"/worktree-mode",
		map[string]string{"mode": "worktree"})

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rr.Code, http.StatusOK, rr.Body)
	}
	var respSess store.Session
	if err := json.NewDecoder(rr.Body).Decode(&respSess); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if respSess.WorktreeMode != "worktree" {
		t.Errorf("WorktreeMode = %q, want %q", respSess.WorktreeMode, "worktree")
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
	req.AddCookie(testAuthCookie(t, s))
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

func TestCloneWorktreeForForkPreservesAncestorProjectDirectory(t *testing.T) {
	s := newWorktreeServer(t)
	repository := t.TempDir()
	initAPIRepo(t, repository)
	commitAPIFile(t, repository, "module/tracked.txt", "tracked\n")
	project, err := s.Store.CreateProject(store.CreateProjectRequest{Name: "fork-subdirectory", GitRepoPath: filepath.Join(repository, "module")})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "parent", ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Store.UpdateSessionWorktree(parent.ID, "worktree", ""); err != nil {
		t.Fatal(err)
	}
	parent, err = s.Store.GetSession(parent.ID)
	if err != nil || parent == nil {
		t.Fatalf("load parent: %v", err)
	}
	parentDirectory := s.ensureWorktree(parent.ID, parent, filepath.Join(repository, "module"))

	child, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "child", ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	s.cloneWorktreeForFork(child, parent)
	persistedChild, err := s.Store.GetSession(child.ID)
	if err != nil || persistedChild == nil {
		t.Fatalf("load child: %v", err)
	}
	if persistedChild.WorktreeMode != "worktree" || persistedChild.WorktreePath == "" {
		t.Fatalf("forked child worktree = %#v", persistedChild)
	}
	childRoot, err := gitWorktreeRoot(persistedChild.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(childRoot, "module"); persistedChild.WorktreePath != want {
		t.Fatalf("forked child work directory = %q, want %q", persistedChild.WorktreePath, want)
	}
	if prefix := runAPIGit(t, persistedChild.WorktreePath, "rev-parse", "--show-prefix"); prefix != "module/\n" {
		t.Fatalf("forked child prefix = %q, want module/", prefix)
	}

	if err := RemoveWorktree(persistedChild.WorktreePath); err != nil {
		t.Fatalf("remove child worktree: %v", err)
	}
	if err := RemoveWorktree(parentDirectory); err != nil {
		t.Fatalf("remove parent worktree: %v", err)
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
