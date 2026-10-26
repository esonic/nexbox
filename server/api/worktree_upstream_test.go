package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/esonic/nexbox/store"
)

func TestWorktreeWithoutUpstreamReportsUnpublishedCommits(t *testing.T) {
	s := newWorktreeServer(t)
	repo := t.TempDir()
	initAPIRepo(t, repo)
	commitAPIFile(t, repo, "tracked.txt", "initial\n")
	remote := t.TempDir()
	runAPIGit(t, remote, "init", "--bare", "-q")
	runAPIGit(t, repo, "remote", "add", "origin", remote)
	runAPIGit(t, repo, "push", "origin", "HEAD:refs/heads/main")
	project := createAPIProject(t, s, repo)
	session, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "worktree", ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "worktree")
	runAPIGit(t, repo, "worktree", "add", "-b", "session-branch", wt)
	if err := s.Store.UpdateSessionWorktree(session.ID, "worktree", wt); err != nil {
		t.Fatal(err)
	}
	if isWorktreeDirty(wt) {
		t.Fatal("published base must not count as unpublished session work")
	}
	commitAPIFile(t, wt, "tracked.txt", "session commit\n")
	response := doRequest(t, s, http.MethodGet, "/api/sessions/"+session.ID+"/worktree-status", nil)
	var status struct {
		Dirty bool `json:"dirty"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &status) != nil || !status.Dirty {
		t.Fatalf("unpublished session commit reported clean: %d %s", response.Code, response.Body.String())
	}
	gitStatus := doRequest(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-status?session_id=%s", project.ID, session.ID), nil)
	var panelStatus GitStatusResponse
	if gitStatus.Code != http.StatusOK || json.Unmarshal(gitStatus.Body.Bytes(), &panelStatus) != nil || panelStatus.Ahead != 1 {
		t.Fatalf("panel ahead count = %d: %s", gitStatus.Code, gitStatus.Body.String())
	}
	branches, err := parseGitBranches(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, branch := range branches.Branches {
		if branch.IsCurrent {
			found = true
			if branch.Upstream != "" || branch.Ahead != 1 {
				t.Fatalf("branch count without upstream: %+v", branch)
			}
		}
	}
	if !found {
		t.Fatal("current branch missing")
	}
	runAPIGit(t, wt, "push", "origin", "HEAD:refs/heads/session-branch")
	if isWorktreeDirty(wt) {
		t.Fatal("published commits still reported unpublished")
	}
	// A nested repository must still count as local data before deletion,
	// although the Git panel omits nested directories from its file list.
	nested := filepath.Join(wt, "nested")
	initAPIRepo(t, nested)
	if err := os.WriteFile(filepath.Join(nested, "new.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isWorktreeDirty(wt) {
		t.Fatal("nested repository data was reported clean")
	}
}
