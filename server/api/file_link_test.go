package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/esonic/nexbox/store"
)

func TestResolveProjectFileLink(t *testing.T) {
	s := newRelayServer(t)
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "src", "app.go"))
	project, err := s.Store.CreateProject(store.CreateProjectRequest{Name: "files", GitRepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.Store.CreateSession(store.CreateSessionRequest{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Store.CreateProject(store.CreateProjectRequest{Name: "other", GitRepoPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := s.Store.CreateSession(store.CreateSessionRequest{ProjectID: other.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, sid string
		status          int
		want            string
	}{
		{"relative", "src/app.go", session.ID, 200, "src/app.go"},
		{"absolute", filepath.Join(root, "src", "app.go"), session.ID, 200, "src/app.go"},
		{"outside", filepath.Join(t.TempDir(), "app.go"), session.ID, 400, ""},
		{"traversal", "../app.go", session.ID, 400, ""},
		{"missing", "src/missing.go", session.ID, 404, ""},
		{"directory", "src", session.ID, 400, ""},
		{"other session", "src/app.go", otherSession.ID, 400, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{"path": {tc.path}, "session_id": {tc.sid}}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/file-resolve?"+q.Encode(), nil)
			NewRouter(s).ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if tc.want != "" {
				var body struct {
					Path string `json:"path"`
				}
				if json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Path != tc.want {
					t.Fatalf("path %q", body.Path)
				}
			}
		})
	}
}

func TestResolveProjectFileLinkUsesWorktree(t *testing.T) {
	s := newWorktreeServer(t)
	root := t.TempDir()
	initAPIRepo(t, root)
	commitAPIFile(t, root, "src/app.go", "local\n")
	project := createAPIProject(t, s, root)
	session, err := s.Store.CreateSession(store.CreateSessionRequest{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "checkout")
	runAPIGit(t, root, "worktree", "add", "-q", "-b", "file-link-test", worktree)
	if err := s.Store.UpdateSessionWorktree(session.ID, "worktree", worktree); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path   string
		status int
	}{{filepath.Join(worktree, "src/app.go"), 200}, {filepath.Join(root, "src/app.go"), 400}} {
		q := url.Values{"path": {tc.path}, "session_id": {session.ID}}
		rec := httptest.NewRecorder()
		rec = doRequest(t, s, http.MethodGet, "/api/projects/"+project.ID+"/file-resolve?"+q.Encode(), nil)
		if rec.Code != tc.status {
			t.Fatalf("%s: status=%d body=%s", tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestChatFileLinkCannotFollowOutsideSymlink(t *testing.T) {
	s := newRelayServer(t)
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	mustWriteFile(t, outside)
	project, err := s.Store.CreateProject(store.CreateProjectRequest{Name: "files", GitRepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked.txt")
	if err = os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	base := "/api/projects/" + project.ID
	rec := httptest.NewRecorder()
	NewRouter(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+"/file-resolve?path="+url.QueryEscape(linked), nil))
	if rec.Code == 200 {
		t.Fatal("resolved symlink outside workspace")
	}
	// Even if a link was resolved before a symlink was replaced, opening it
	// through the chat-link context must not read the new target.
	rec = httptest.NewRecorder()
	NewRouter(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+"/file-content?path=linked.txt&safe_link=1", nil))
	if strings.Contains(rec.Body.String(), `"content":"x"`) {
		t.Fatalf("read outside file: %s", rec.Body.String())
	}
}

func mustWriteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}
