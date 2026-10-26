package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/esonic/nexbox/store"
)

func newTrashWorktree(t *testing.T) (*Server, *Session, string) {
	t.Helper()
	s := newWorktreeServer(t)
	repo := t.TempDir()
	initAPIRepo(t, repo)
	commitAPIFile(t, repo, "module/tracked.txt", "initial\n")
	project := createAPIProject(t, s, repo)
	session, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "expired", ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "shared")
	runAPIGit(t, repo, "worktree", "add", "-q", "-b", "shared-test", worktree)
	if err := s.Store.UpdateSessionWorktree(session.ID, "worktree", worktree); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "uncommitted.txt"), []byte("keep this work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return s, session, worktree
}

func TestPurgeTrashPreservesReferencedWorktree(t *testing.T) {
	for _, reference := range []string{"active", "recent-trash", "hidden", "subdirectory", "symlink"} {
		t.Run(reference, func(t *testing.T) {
			s, expired, worktree := newTrashWorktree(t)
			var survivor *Session
			var err error
			if reference == "hidden" {
				survivor, err = s.Store.GetOrCreateScratchSession(expired.ProjectID)
			} else {
				survivor, err = s.Store.CreateSession(store.CreateSessionRequest{Name: "survivor", ProjectID: expired.ProjectID})
			}
			if err != nil {
				t.Fatal(err)
			}
			path := worktree
			switch reference {
			case "subdirectory":
				path = filepath.Join(worktree, "module")
			case "symlink":
				path = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(worktree, path); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(path, "module")
			}
			if err := s.Store.UpdateSessionWorktree(survivor.ID, "worktree", path); err != nil {
				t.Fatal(err)
			}
			if err := s.Store.DeleteSession(expired.ID); err != nil {
				t.Fatal(err)
			}
			cutoff := time.Now()
			if reference == "recent-trash" {
				if err := s.Store.DeleteSession(survivor.ID); err != nil {
					t.Fatal(err)
				}
			}
			if n, err := s.PurgeTrash(cutoff); err != nil || n != 1 {
				t.Fatalf("purge = %d, %v; want one expired session", n, err)
			}
			if content, err := os.ReadFile(filepath.Join(worktree, "uncommitted.txt")); err != nil || string(content) != "keep this work\n" {
				t.Fatalf("shared worktree data lost: %q, %v", content, err)
			}
			if session, err := s.Store.GetSession(survivor.ID); err != nil || session == nil {
				t.Fatalf("surviving session lost: %v", err)
			}
			// The shared directory is still cleaned up once its last session
			// reference is permanently removed, including hidden sessions.
			if err := s.Store.DeleteSession(survivor.ID); err != nil {
				t.Fatal(err)
			}
			if n, err := s.PurgeTrash(time.Now().Add(time.Hour)); err != nil || n != 1 {
				t.Fatalf("last-reference purge = %d, %v", n, err)
			}
			if _, err := os.Stat(worktree); !os.IsNotExist(err) {
				t.Fatalf("unreferenced worktree still exists: %v", err)
			}
		})
	}
}

func TestPurgeTrashPreservesActiveWorkflowWorktree(t *testing.T) {
	for _, status := range []string{"pending", "running"} {
		t.Run(status, func(t *testing.T) {
			s, session, worktree := newTrashWorktree(t)
			wf, err := s.Store.CreateWorkflow(session.ProjectID, "shared workflow", "", "local", []byte(`{"nodes":[],"edges":[]}`))
			if err != nil {
				t.Fatal(err)
			}
			execution, err := s.Store.CreateExecution(store.CreateExecutionRequest{WorkflowID: wf.ID})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Store.UpdateExecutionWorktreePath(execution.ID, worktree); err != nil {
				t.Fatal(err)
			}
			if err := s.Store.UpdateExecutionStatus(execution.ID, status, "", nil); err != nil {
				t.Fatal(err)
			}
			if err := s.Store.DeleteSession(session.ID); err != nil {
				t.Fatal(err)
			}
			if n, err := s.PurgeTrash(time.Now().Add(time.Hour)); err != nil || n != 1 {
				t.Fatalf("purge = %d, %v", n, err)
			}
			if _, err := os.Stat(filepath.Join(worktree, "uncommitted.txt")); err != nil {
				t.Fatalf("active workflow worktree was removed: %v", err)
			}
		})
	}
}

func TestPurgeTrashKeepsWorktreeWhenReferenceCheckFails(t *testing.T) {
	s, session, worktree := newTrashWorktree(t)
	if err := s.Store.DeleteSession(session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.DB().Exec(`ALTER TABLE wf_executions RENAME TO unavailable_executions`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeTrash(time.Now().Add(time.Hour)); err == nil {
		t.Fatal("expected reference query error")
	}
	if _, err := os.Stat(filepath.Join(worktree, "uncommitted.txt")); err != nil {
		t.Fatalf("worktree was removed without a successful reference check: %v", err)
	}
}
