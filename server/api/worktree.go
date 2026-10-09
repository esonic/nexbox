package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
		effectiveWorkDir, err := projectPathInWorktree(session.WorktreePath, projectWorkDir)
		if err != nil {
			log.Printf("[worktree] Failed to normalize existing worktree path for session %s: %v", sessionID, err)
			return session.WorktreePath
		}
		if effectiveWorkDir != session.WorktreePath {
			if err := s.Store.UpdateSessionWorktree(sessionID, "worktree", effectiveWorkDir); err != nil {
				log.Printf("[worktree] Failed to persist normalized worktree path for session %s: %v", sessionID, err)
			} else {
				session.WorktreePath = effectiveWorkDir
				s.Hub.NotifyChange("session", sessionID, "updated")
			}
		}
		return effectiveWorkDir
	}

	shortID := sessionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	worktreeDir := s.DataDir + "/worktrees/" + shortID
	branchName := shortID
	projectPrefix, err := gitRepositoryPrefix(projectWorkDir)
	if err != nil {
		log.Printf("[worktree] Failed to resolve project path inside repository for session %s: %v", sessionID, err)
		_ = s.Store.UpdateSessionWorktree(sessionID, "local", "")
		s.Hub.NotifyChange("session", sessionID, "updated")
		return projectWorkDir
	}

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

	effectiveWorkDir, err := effectiveWorktreePath(worktreeDir, projectPrefix)
	if err != nil {
		log.Printf("[worktree] Failed to prepare project directory in worktree for session %s: %v", sessionID, err)
		if removeErr := RemoveWorktree(worktreeDir); removeErr != nil {
			log.Printf("[worktree] Failed to roll back worktree %s: %v", worktreeDir, removeErr)
		}
		_ = s.Store.UpdateSessionWorktree(sessionID, "local", "")
		s.Hub.NotifyChange("session", sessionID, "updated")
		return projectWorkDir
	}

	log.Printf("[worktree] Created worktree %s (working directory %s, branch %s) for session %s", worktreeDir, effectiveWorkDir, branchName, sessionID)
	if err := s.Store.UpdateSessionWorktree(sessionID, "worktree", effectiveWorkDir); err != nil {
		log.Printf("[worktree] Failed to update session worktree path: %v", err)
	} else {
		session.WorktreePath = effectiveWorkDir
		s.Hub.NotifyChange("session", sessionID, "updated")
	}
	return effectiveWorkDir
}

// createWorkflowRunWorktree creates a fresh git worktree for a workflow run.
// Returns the absolute effective project directory on success (which may be
// below the linked worktree root). Caller is expected to persist the path and
// pass it to every session spawned during the run.
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
	projectPrefix, err := gitRepositoryPrefix(project.GitRepoPath)
	if err != nil {
		return "", fmt.Errorf("project path is not a git repository: %w", err)
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

	effectiveWorkDir, err := effectiveWorktreePath(worktreeDir, projectPrefix)
	if err != nil {
		if removeErr := RemoveWorktree(worktreeDir); removeErr != nil {
			log.Printf("[worktree] Failed to roll back workflow-run worktree %s: %v", worktreeDir, removeErr)
		}
		return "", fmt.Errorf("prepare project directory in worktree: %w", err)
	}

	log.Printf("[worktree] Created workflow-run worktree %s (working directory %s, branch %s) for execution %s", worktreeDir, effectiveWorkDir, branchName, executionID)
	return effectiveWorkDir, nil
}

// gitRepositoryPrefix returns workDir's relative location below the repository
// root. Git is the source of truth here so paths reached through symlinks and
// linked worktrees use the same semantics as Git commands run from workDir.
func gitRepositoryPrefix(workDir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-prefix")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-prefix: %w", err)
	}
	prefix := strings.TrimSuffix(strings.TrimSuffix(string(out), "\n"), "\r")
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" {
		return "", nil
	}
	prefix = filepath.Clean(filepath.FromSlash(prefix))
	if prefix == "." {
		return "", nil
	}
	if filepath.IsAbs(prefix) || prefix == ".." || strings.HasPrefix(prefix, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("Git returned unsafe repository prefix %q", prefix)
	}
	return prefix, nil
}

// effectiveWorktreePath maps the project's repository-relative prefix into a
// newly-created worktree. The directory may be absent when the source project
// directory contains no tracked files, so create it before agents start there.
func effectiveWorktreePath(worktreeRoot, projectPrefix string) (string, error) {
	root, err := filepath.Abs(filepath.Clean(worktreeRoot))
	if err != nil {
		return "", fmt.Errorf("resolve worktree root: %w", err)
	}
	effective := root
	if projectPrefix != "" {
		effective = filepath.Join(root, projectPrefix)
	}
	relative, err := filepath.Rel(root, effective)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("project directory escapes worktree root")
	}
	if err := os.MkdirAll(effective, 0o755); err != nil {
		return "", fmt.Errorf("create worktree project directory: %w", err)
	}
	return effective, nil
}

// projectPathInWorktree returns the project directory inside an existing
// worktree. This upgrades legacy rows that stored only the worktree root while
// the configured project itself was a subdirectory of its repository.
func projectPathInWorktree(worktreePath, projectWorkDir string) (string, error) {
	projectPrefix, err := gitRepositoryPrefix(projectWorkDir)
	if err != nil {
		return "", err
	}
	worktreeRoot, err := gitWorktreeRoot(worktreePath)
	if err != nil {
		return "", err
	}
	return effectiveWorktreePath(worktreeRoot, projectPrefix)
}

// gitWorktreeRoot resolves a stored effective working directory back to the
// linked worktree root. Older rows already store the root itself, so this also
// keeps those sessions compatible.
func gitWorktreeRoot(worktreePath string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve Git worktree root: %w", err)
	}
	root := strings.TrimSuffix(strings.TrimSuffix(string(out), "\n"), "\r")
	if root == "" {
		return "", fmt.Errorf("Git returned an empty worktree root")
	}
	return filepath.Clean(root), nil
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

// RemoveWorktree removes and unregisters the linked worktree containing the
// supplied effective working directory.
// Exported so main.go can call it during trash cleanup.
// The command runs from within the worktree so git can locate the main repo.
func RemoveWorktree(worktreePath string) error {
	worktreeRoot, err := gitWorktreeRoot(worktreePath)
	if err != nil {
		return err
	}
	cmd := exec.Command("git", "worktree", "remove", "--force", worktreeRoot)
	cmd.Dir = worktreeRoot // run from within the worktree so git finds the parent repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove: %w\n%s", err, out)
	}
	return nil
}
