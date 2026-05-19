package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/go-chi/chi/v5"
)

// ensureWorktree creates the git worktree for a session on first message.
// No-op when the session is not in worktree mode, or when the worktree already exists.
// Returns the effective workDir to use.
func (s *Server) ensureWorktree(sessionID string, session *Session, projectWorkDir string) string {
	if session.WorktreeMode != "worktree" {
		return projectWorkDir
	}
	// Already created
	if session.WorktreePath != "" {
		return session.WorktreePath
	}

	shortID := sessionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	worktreeDir := s.DataDir + "/worktrees/" + shortID
	branchName := shortID

	if err := os.MkdirAll(s.DataDir+"/worktrees", 0755); err != nil {
		log.Printf("[worktree] Failed to create worktrees dir: %v", err)
		return projectWorkDir
	}

	cmd := exec.Command("git", "worktree", "add", "-b", branchName, worktreeDir)
	cmd.Dir = projectWorkDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[worktree] git worktree add failed for session %s: %v\n%s", sessionID, err, out)
		// Revert mode to local so the session continues working, and notify the
		// frontend so it can clear its "preparing" spinner.
		_ = s.Store.UpdateSessionWorktree(sessionID, "local", "")
		s.Hub.NotifyChange("session", sessionID, "updated")
		return projectWorkDir
	}

	log.Printf("[worktree] Created worktree %s (branch %s) for session %s", worktreeDir, branchName, sessionID)
	if err := s.Store.UpdateSessionWorktree(sessionID, "worktree", worktreeDir); err != nil {
		log.Printf("[worktree] Failed to update session worktree path: %v", err)
	} else {
		s.Hub.NotifyChange("session", sessionID, "updated")
	}
	return worktreeDir
}

// createWorkflowRunWorktree creates a fresh git worktree for a workflow run.
// Returns the absolute worktree path on success. Caller is expected to persist
// the path (e.g. on the WorkflowExecution row) and pass it to every session
// spawned during the run.
//
// Naming: directory and branch are both `wfrun-{executionID[:8]}`. This lets a
// human reading `git worktree list` immediately tie a worktree back to a run.
func (s *Server) createWorkflowRunWorktree(executionID, projectID string) (string, error) {
	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		return "", fmt.Errorf("project not found")
	}
	if project.GitRepoPath == "" {
		return "", fmt.Errorf("project has no git repo path configured")
	}
	check := exec.Command("git", "rev-parse", "--git-dir")
	check.Dir = project.GitRepoPath
	if err := check.Run(); err != nil {
		return "", fmt.Errorf("project path is not a git repository")
	}

	shortID := executionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	worktreeDir := s.DataDir + "/worktrees/wfrun-" + shortID
	branchName := "wfrun-" + shortID

	if err := os.MkdirAll(s.DataDir+"/worktrees", 0755); err != nil {
		return "", fmt.Errorf("create worktrees dir: %w", err)
	}

	cmd := exec.Command("git", "worktree", "add", "-b", branchName, worktreeDir)
	cmd.Dir = project.GitRepoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git worktree add: %w\n%s", err, out)
	}

	log.Printf("[worktree] Created workflow-run worktree %s (branch %s) for execution %s", worktreeDir, branchName, executionID)
	return worktreeDir, nil
}

// SetWorktreeMode sets the worktree mode for a session before first message.
// PUT /api/sessions/{sessionId}/worktree-mode
func (s *Server) SetWorktreeMode(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	session, err := s.Store.GetSession(sessionID)
	if err != nil || session == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if session.AgentSessionID != "" {
		writeError(w, http.StatusConflict, "cannot change worktree mode after first message")
		return
	}

	var req struct {
		Mode string `json:"mode"` // "local" | "worktree"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Mode != "local" && req.Mode != "worktree" {
		writeError(w, http.StatusBadRequest, "mode must be 'local' or 'worktree'")
		return
	}

	// When enabling worktree mode, verify the project's path is a real git repo
	// so we fail fast here rather than at first-message time.
	if req.Mode == "worktree" {
		if session.ProjectID == "" {
			writeError(w, http.StatusBadRequest, "session is not bound to a project")
			return
		}
		project, err := s.getProjectWithLocalPath(session.ProjectID)
		if err != nil || project == nil {
			writeError(w, http.StatusBadRequest, "project not found")
			return
		}
		gitPath := project.GitRepoPath
		if gitPath == "" {
			writeError(w, http.StatusBadRequest, "project has no git repo path configured")
			return
		}
		check := exec.Command("git", "rev-parse", "--git-dir")
		check.Dir = gitPath
		if err := check.Run(); err != nil {
			writeError(w, http.StatusBadRequest, "project path is not a git repository")
			return
		}
	}

	if err := s.Store.UpdateSessionWorktree(sessionID, req.Mode, ""); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update worktree mode")
		return
	}

	session.WorktreeMode = req.Mode
	session.WorktreePath = ""
	writeJSON(w, http.StatusOK, session)
}

// GetWorktreeStatus checks whether the session's worktree has uncommitted or unpushed changes.
// GET /api/sessions/{sessionId}/worktree-status
func (s *Server) GetWorktreeStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	session, err := s.Store.GetSession(sessionID)
	if err != nil || session == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	if session.WorktreeMode != "worktree" || session.WorktreePath == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"dirty": false})
		return
	}

	dirty := isWorktreeDirty(session.WorktreePath)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"dirty":         dirty,
		"worktree_path": session.WorktreePath,
	})
}

// isWorktreeDirty returns true if the worktree has uncommitted changes or unpushed commits.
func isWorktreeDirty(worktreePath string) bool {
	// Check for uncommitted changes
	statusOut, err := exec.Command("git", "-C", worktreePath, "status", "--porcelain").Output()
	if err == nil && len(strings.TrimSpace(string(statusOut))) > 0 {
		return true
	}

	// Check for unpushed commits
	logOut, err := exec.Command("git", "-C", worktreePath, "log", "@{u}..HEAD", "--oneline").Output()
	if err == nil && len(strings.TrimSpace(string(logOut))) > 0 {
		return true
	}

	return false
}

// RemoveWorktree removes a git worktree directory and unregisters it.
// Exported so main.go can call it during trash cleanup.
// The command runs from within the worktree so git can locate the main repo.
func RemoveWorktree(worktreePath string) error {
	cmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
	cmd.Dir = worktreePath // run from within the worktree so git finds the parent repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove: %w\n%s", err, out)
	}
	return nil
}
