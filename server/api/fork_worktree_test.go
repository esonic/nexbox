package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/esonic/nexbox/store"
)

func TestForkSessionSharesWorktreeAndLocksMode(t *testing.T) {
	for _, mode := range []string{"local", "worktree"} {
		t.Run(mode, func(t *testing.T) {
			s := newAgentGateServer(t)
			repo := t.TempDir()
			initAPIRepo(t, repo)
			commitAPIFile(t, repo, "module/tracked.txt", "initial\n")
			project := createAPIProject(t, s, filepath.Join(repo, "module"))
			parent, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "parent", ProjectID: project.ID, Agent: "codex"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Store.UpdateSessionWorktree(parent.ID, mode, ""); err != nil {
				t.Fatal(err)
			}
			parent, err = s.Store.GetSession(parent.ID)
			if err != nil {
				t.Fatal(err)
			}
			dir := s.ensureWorktree(parent.ID, parent, project.GitRepoPath)
			if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("uncommitted\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new file\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := s.Store.UpdateSessionAgentID(parent.ID, "parent-thread"); err != nil {
				t.Fatal(err)
			}
			message, err := s.Store.CreateMessage(parent.ID, "assistant", []store.MessagePart{{Type: "text", Content: "done"}}, store.MessageMetadata{AgentCheckpoint: "checkpoint"})
			if err != nil {
				t.Fatal(err)
			}
			beforeWorktrees := runAPIGit(t, repo, "worktree", "list", "--porcelain")
			beforeBranches := runAPIGit(t, repo, "show-ref", "--heads")
			parentID := parent.ID
			for generation := 0; generation < 2; generation++ {
				response := doRequest(t, s, http.MethodPost, "/api/sessions/"+parentID+"/fork", map[string]string{"message_id": message.ID})
				if response.Code != http.StatusCreated {
					t.Fatalf("fork = %d: %s", response.Code, response.Body.String())
				}
				var child Session
				if err := json.Unmarshal(response.Body.Bytes(), &child); err != nil {
					t.Fatal(err)
				}
				if child.WorktreeMode != mode || child.WorktreePath != parent.WorktreePath || s.sessionWorkDir(&child) != dir {
					t.Fatalf("fork changed workspace: %+v", child)
				}
				persisted, err := s.Store.GetSession(child.ID)
				if err != nil || persisted == nil || persisted.WorktreeMode != mode || persisted.WorktreePath != parent.WorktreePath {
					t.Fatalf("fork workspace not persisted: %+v, %v", persisted, err)
				}
				for _, requestedMode := range []string{"local", "worktree"} {
					locked := doRequest(t, s, http.MethodPut, "/api/sessions/"+child.ID+"/worktree-mode", map[string]string{"mode": requestedMode})
					if locked.Code != http.StatusConflict {
						t.Fatalf("fork mode change accepted: %d: %s", locked.Code, locked.Body.String())
					}
				}
				if content, err := os.ReadFile(filepath.Join(s.sessionWorkDir(&child), "tracked.txt")); err != nil || string(content) != "uncommitted\n" {
					t.Fatalf("uncommitted changes missing: %q, %v", content, err)
				}
				if _, err := os.Stat(filepath.Join(s.sessionWorkDir(&child), "new.txt")); err != nil {
					t.Fatalf("new file missing: %v", err)
				}
				messages, err := s.Store.ListMessages(child.ID)
				if err != nil || len(messages) != 1 {
					t.Fatalf("fork messages: %v", err)
				}
				parentID, message = child.ID, &messages[0]
			}
			if got := runAPIGit(t, repo, "worktree", "list", "--porcelain"); got != beforeWorktrees {
				t.Fatal("fork created or changed a worktree")
			}
			if got := runAPIGit(t, repo, "show-ref", "--heads"); got != beforeBranches {
				t.Fatal("fork created or changed a branch")
			}
		})
	}
}
