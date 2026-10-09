package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/gitrepo"
	"github.com/go-chi/chi/v5"
)

// getCurrentBranch returns the current branch name from a git working directory.
func getCurrentBranch(dir string) (string, error) {
	return getCurrentBranchContext(context.Background(), dir)
}

func getCurrentBranchContext(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

// --- Types ---

// GitFileEntry represents a file in git status output
type GitFileEntry struct {
	Path         string `json:"path"`
	OriginalPath string `json:"original_path,omitempty"`
	Status       string `json:"status"` // modified, added, deleted, renamed, untracked, conflicted
}

// GitStatusResponse represents the structured git status
type GitStatusResponse struct {
	RepositoryID string         `json:"repository_id,omitempty"`
	Staged       []GitFileEntry `json:"staged"`
	Unstaged     []GitFileEntry `json:"unstaged"`
	Untracked    []GitFileEntry `json:"untracked"`
	Ahead        int            `json:"ahead,omitempty"`
}

// GitCommitRequest represents a commit request
type GitCommitRequest struct {
	FilePaths []string `json:"file_paths"`
	Message   string   `json:"message"`
}

// GitCommitResponse represents the commit result
type GitCommitResponse struct {
	RepositoryID string `json:"repository_id,omitempty"`
	Hash         string `json:"hash"`
	ShortHash    string `json:"short_hash"`
}

// GitDiscardRequest represents a discard request
type GitDiscardRequest struct {
	FilePath string `json:"file_path"`
}

// GitCommitInfo represents a commit in the log
type GitCommitInfo struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"short_hash"`
	Message   string `json:"message"`
	Author    string `json:"author"`
	Date      string `json:"date"`
}

// GitCommitFileEntry represents a file changed in a commit
type GitCommitFileEntry struct {
	Path          string `json:"path"`
	OriginalPath  string `json:"original_path,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	Status        string `json:"status"` // added, modified, deleted, renamed
}

// GitBranchInfo represents a branch
type GitBranchInfo struct {
	Name      string `json:"name"`
	IsCurrent bool   `json:"is_current"`
	IsRemote  bool   `json:"is_remote"`
	// LocalName is the branch name without the remote prefix, set only for
	// remote-tracking branches (origin/feature -> feature).
	LocalName string `json:"local_name,omitempty"`
	Upstream  string `json:"upstream,omitempty"`
	Ahead     int    `json:"ahead,omitempty"`
	Behind    int    `json:"behind,omitempty"`
}

// GitBranchResponse is the response for listing branches
type GitBranchResponse struct {
	RepositoryID string          `json:"repository_id,omitempty"`
	Current      string          `json:"current"`
	Branches     []GitBranchInfo `json:"branches"`
}

// GitCheckoutRequest represents a branch checkout request
type GitCheckoutRequest struct {
	Branch string `json:"branch"`
	Create bool   `json:"create"`
	// StartPoint, when set with Create, is the remote-tracking branch the new
	// branch starts from and tracks (e.g. origin/feature). Only
	// remote-tracking refs are accepted.
	StartPoint string `json:"start_point,omitempty"`
}

// GitStageRequest represents a stage/unstage request
type GitStageRequest struct {
	FilePaths []string `json:"file_paths"`
}

// GitPushPullResponse represents the result of push/pull
type GitPushPullResponse struct {
	Output string `json:"output"`
}

// GenerateCommitMessageRequest represents the request for AI commit message
type GenerateCommitMessageRequest struct {
	FilePaths []string `json:"file_paths"`
}

// --- Helpers ---

func (s *Server) lockRepositoryMutation(r *http.Request, selected gitrepo.Context) (func(), error) {
	return s.repositoryResolver().LockMutation(r.Context(), selected)
}

// --- Handlers ---

// GetGitStatus returns staged, unstaged, and untracked files
func (s *Server) GetGitStatus(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}

	workDir := repositoryContext.Repository.CanonicalRoot
	status, err := s.repositoryResolver().Status(r.Context(), repositoryContext.Repository, false)
	if err != nil {
		log.Printf("[git] status error in %s: %v", workDir, err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git status failed: %v", err))
		return
	}
	result := gitStatusResponse(status)
	result.RepositoryID = repositoryContext.Repository.ID

	writeJSON(w, http.StatusOK, result)
}

// GitCommit performs a selective commit of specified files
func (s *Server) GitCommit(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	workDir := repositoryContext.Repository.CanonicalRoot

	var req GitCommitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Message == "" {
		writeError(w, http.StatusBadRequest, "commit message is required")
		return
	}
	if len(req.FilePaths) == 0 {
		writeError(w, http.StatusBadRequest, "at least one file path is required")
		return
	}
	req.FilePaths, err = validateGitFilePaths(req.FilePaths)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	unlock, err := s.lockRepositoryMutation(r, repositoryContext)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	defer unlock()
	req.FilePaths, err = expandRenamePaths(r.Context(), workDir, req.FilePaths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git status failed: %v", err))
		return
	}

	// Build the index from scratch so the commit contains only the requested
	// paths. An unborn repository has no HEAD for `git reset HEAD`, so clear its
	// index explicitly instead of leaving previously staged paths in place.
	if gitrepo.HasCommit(r.Context(), workDir) {
		if err := runGitCommand(r.Context(), workDir, "reset", "HEAD"); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("git reset failed: %v", err))
			return
		}
	} else if err := runGitCommand(r.Context(), workDir, "read-tree", "--empty"); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git index reset failed: %v", err))
		return
	}

	// Step 2: Stage only selected files
	addArgs := append([]string{"--literal-pathspecs", "add", "--"}, req.FilePaths...)
	if err := runGitCommand(r.Context(), workDir, addArgs...); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git add failed: %v", err))
		return
	}

	// Step 3: Commit
	cmd := exec.CommandContext(r.Context(), "git", "commit", "-m", req.Message)
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git commit failed: %s", stderr.String()))
		return
	}

	// Get the commit hash
	hash, shortHash := getLastCommitHash(r.Context(), workDir)

	log.Printf("[git-operation] project=%s repo=%s op=commit files=%d result=ok", projectID, repositoryContext.Repository.ID, len(req.FilePaths))
	writeJSON(w, http.StatusOK, GitCommitResponse{
		RepositoryID: repositoryContext.Repository.ID,
		Hash:         hash,
		ShortHash:    shortHash,
	})
}

// GitDiscard discards all index and worktree changes associated with a single
// status entry. Rename entries involve two paths, so both the current and
// original path are restored together.
func (s *Server) GitDiscard(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	workDir := repositoryContext.Repository.CanonicalRoot

	var req GitDiscardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.FilePath == "" {
		writeError(w, http.StatusBadRequest, "file_path is required")
		return
	}
	req.FilePath, err = gitrepo.ValidateRelativePath(req.FilePath)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	unlock, err := s.lockRepositoryMutation(r, repositoryContext)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	defer unlock()

	// Read a fresh status snapshot while holding the mutation lock. In
	// particular, a staged add is not reported as untracked and cannot be
	// discarded with checkout alone.
	status, err := parseGitStatus(r.Context(), workDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git status failed: %v", err))
		return
	}

	paths := []string{req.FilePath}
	pathSeen := map[string]bool{req.FilePath: true}
	trackedChange := false
	addRelatedPath := func(entry GitFileEntry) {
		isRename := entry.Status == "renamed" && entry.OriginalPath != ""
		if entry.Path != req.FilePath && (!isRename || entry.OriginalPath != req.FilePath) {
			return
		}
		trackedChange = true
		related := []string{entry.Path}
		if isRename {
			related = append(related, entry.OriginalPath)
		}
		for _, path := range related {
			if path != "" && !pathSeen[path] {
				paths = append(paths, path)
				pathSeen[path] = true
			}
		}
	}
	for _, entry := range status.Staged {
		addRelatedPath(entry)
	}
	for _, entry := range status.Unstaged {
		addRelatedPath(entry)
	}

	isUntracked := false
	for _, f := range status.Untracked {
		if f.Path == req.FilePath {
			isUntracked = true
			break
		}
	}

	if isUntracked && !trackedChange {
		if err := removeRepositoryFiles(workDir, []string{req.FilePath}); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete untracked file: %v", err))
			return
		}
	} else if gitrepo.HasCommit(r.Context(), workDir) {
		paths, err = validateGitFilePaths(paths)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		// restore --staged --worktree resets both the index and working tree.
		// Supplying both sides of a rename also removes the new path and brings
		// the original path back in one operation.
		restoreArgs := append([]string{"--literal-pathspecs", "restore", "--source=HEAD", "--staged", "--worktree", "--"}, paths...)
		if err := runGitCommand(r.Context(), workDir, restoreArgs...); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("git restore failed: %v", err))
			return
		}
	} else {
		// An unborn repository has no source tree to restore from. Its indexed
		// files are staged additions, so discard means remove them from the
		// index and then remove the worktree entries themselves.
		paths, err = validateGitFilePaths(paths)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		rmArgs := append([]string{"--literal-pathspecs", "rm", "--cached", "--ignore-unmatch", "--"}, paths...)
		if err := runGitCommand(r.Context(), workDir, rmArgs...); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("git rm --cached failed: %v", err))
			return
		}
		if err := removeRepositoryFiles(workDir, paths); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete file: %v", err))
			return
		}
	}

	log.Printf("[git-operation] project=%s repo=%s op=discard result=ok", projectID, repositoryContext.Repository.ID)
	w.WriteHeader(http.StatusNoContent)
}

// removeRepositoryFiles removes final path entries through an already-open
// repository root. os.Root prevents a concurrently retargeted parent symlink
// from redirecting deletion outside the selected repository, while final
// symlinks are removed as links rather than followed.
func removeRepositoryFiles(workDir string, paths []string) error {
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, path := range paths {
		clean, err := gitrepo.ValidateRelativePath(path)
		if err != nil {
			return err
		}
		if err := root.Remove(filepath.FromSlash(clean)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %q: %w", clean, err)
		}
	}
	return nil
}

// GitLog returns recent commit history
func (s *Server) GitLog(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	workDir := repositoryContext.Repository.CanonicalRoot

	limitStr := r.URL.Query().Get("limit")
	limit := 20
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 100 {
		limit = 100
	}

	skip := 0
	if s := r.URL.Query().Get("skip"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			skip = n
		}
	}

	commits, err := getGitLog(r.Context(), workDir, limit, skip)
	if err != nil {
		log.Printf("[git] log error in %s: %v", workDir, err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git log failed: %v", err))
		return
	}

	// Preserve the legacy single-repository response shape for callers that do
	// not send repository_id. Explicitly scoped callers receive the repository
	// identity needed to reject stale cross-repository responses.
	if r.URL.Query().Get("repository_id") == "" {
		writeJSON(w, http.StatusOK, commits)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"repository_id": repositoryContext.Repository.ID, "commits": commits})
}

// GitCommitFiles returns the files changed in a specific commit
func (s *Server) GitCommitFiles(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	workDir := repositoryContext.Repository.CanonicalRoot

	hash := r.URL.Query().Get("hash")
	if hash == "" {
		writeError(w, http.StatusBadRequest, "hash parameter is required")
		return
	}
	hash, err = validateCommitRef(r.Context(), workDir, hash)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	files, err := getCommitFiles(r.Context(), workDir, hash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get commit files: %v", err))
		return
	}
	for index := range files {
		if workspacePath, ok := gitrepo.WorkspacePath(repositoryContext.WorkspaceRoot, repositoryContext.Repository, files[index].Path); ok {
			files[index].WorkspacePath = workspacePath
		}
	}

	if r.URL.Query().Get("repository_id") == "" {
		writeJSON(w, http.StatusOK, files)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"repository_id": repositoryContext.Repository.ID, "files": files})
}

// GitCommitDiff returns the full diff of a specific commit
func (s *Server) GitCommitDiff(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	workDir := repositoryContext.Repository.CanonicalRoot

	hash := r.URL.Query().Get("hash")
	if hash == "" {
		writeError(w, http.StatusBadRequest, "hash parameter is required")
		return
	}
	hash, err = validateCommitRef(r.Context(), workDir, hash)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	diff, err := getCommitDiff(r.Context(), workDir, hash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get commit diff: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"repository_id": repositoryContext.Repository.ID, "diff": diff})
}

// GenerateCommitMessage generates a commit message using AI based on the
// actual diff content. Falls back to a heuristic when the agent is unavailable
// or returns an error.
func (s *Server) GenerateCommitMessage(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	workDir := repositoryContext.Repository.CanonicalRoot

	var req GenerateCommitMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.FilePaths) > 0 {
		req.FilePaths, err = validateGitFilePaths(req.FilePaths)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
	}

	diff := collectFileDiffs(r.Context(), workDir, req.FilePaths)
	if diff == "" {
		writeJSON(w, http.StatusOK, map[string]string{"repository_id": repositoryContext.Repository.ID, "message": generateCommitMessage(r.Context(), workDir, req.FilePaths)})
		return
	}

	message, err := s.generateAICommitMessage(r.Context(), workDir, diff)
	if err != nil {
		log.Printf("[git] AI commit message generation failed: %v, using heuristic", err)
		message = generateCommitMessage(r.Context(), workDir, req.FilePaths)
	}

	writeJSON(w, http.StatusOK, map[string]string{"repository_id": repositoryContext.Repository.ID, "message": message})
}

// GetGitBranches returns local and remote branches with tracking info
func (s *Server) GetGitBranches(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	workDir := repositoryContext.Repository.CanonicalRoot

	result, err := parseGitBranches(r.Context(), workDir)
	if err != nil {
		log.Printf("[git] branches error in %s: %v", workDir, err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git branch failed: %v", err))
		return
	}
	result.RepositoryID = repositoryContext.Repository.ID

	writeJSON(w, http.StatusOK, result)
}

// GitCheckout switches to or creates a branch
func (s *Server) GitCheckout(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	workDir := repositoryContext.Repository.CanonicalRoot

	var req GitCheckoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Branch == "" {
		writeError(w, http.StatusBadRequest, "branch name is required")
		return
	}
	if err := runGitCommand(r.Context(), workDir, "check-ref-format", "--branch", req.Branch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid branch name")
		return
	}
	if req.StartPoint != "" {
		if !req.Create {
			writeError(w, http.StatusBadRequest, "start_point requires create")
			return
		}
		if err := validateRemoteTrackingBranch(r.Context(), workDir, req.StartPoint); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	unlock, err := s.lockRepositoryMutation(r, repositoryContext)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	defer unlock()

	// Without create, only an existing local branch is a valid target: git would
	// otherwise DWIM an arbitrary revision into a detached HEAD.
	if !req.Create && !localBranchExists(r.Context(), workDir, req.Branch) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("local branch %q does not exist", req.Branch))
		return
	}

	var args []string
	switch {
	case req.Create && req.StartPoint != "":
		args = []string{"checkout", "-b", req.Branch, "--track", req.StartPoint}
	case req.Create:
		args = []string{"checkout", "-b", req.Branch}
	default:
		args = []string{"checkout", req.Branch}
	}

	if err := runGitCommand(r.Context(), workDir, args...); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git checkout failed: %v", err))
		return
	}

	log.Printf("[git-operation] project=%s repo=%s op=checkout create=%v result=ok", projectID, repositoryContext.Repository.ID, req.Create)
	writeJSON(w, http.StatusOK, map[string]string{"repository_id": repositoryContext.Repository.ID, "branch": req.Branch})
}

// GitStage stages specified files
func (s *Server) GitStage(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	workDir := repositoryContext.Repository.CanonicalRoot

	var req GitStageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.FilePaths) == 0 {
		writeError(w, http.StatusBadRequest, "at least one file path is required")
		return
	}
	req.FilePaths, err = validateGitFilePaths(req.FilePaths)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	unlock, err := s.lockRepositoryMutation(r, repositoryContext)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	defer unlock()
	req.FilePaths, err = expandRenamePaths(r.Context(), workDir, req.FilePaths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git status failed: %v", err))
		return
	}

	addArgs := append([]string{"--literal-pathspecs", "add", "--"}, req.FilePaths...)
	if err := runGitCommand(r.Context(), workDir, addArgs...); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git add failed: %v", err))
		return
	}

	log.Printf("[git-operation] project=%s repo=%s op=stage files=%d result=ok", projectID, repositoryContext.Repository.ID, len(req.FilePaths))
	w.WriteHeader(http.StatusNoContent)
}

// GitUnstage unstages specified files
func (s *Server) GitUnstage(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	workDir := repositoryContext.Repository.CanonicalRoot

	var req GitStageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.FilePaths) == 0 {
		writeError(w, http.StatusBadRequest, "at least one file path is required")
		return
	}
	req.FilePaths, err = validateGitFilePaths(req.FilePaths)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	unlock, err := s.lockRepositoryMutation(r, repositoryContext)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	defer unlock()
	req.FilePaths, err = expandRenamePaths(r.Context(), workDir, req.FilePaths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git status failed: %v", err))
		return
	}

	resetArgs := append([]string{"--literal-pathspecs", "reset", "HEAD", "--"}, req.FilePaths...)
	if err := runGitCommand(r.Context(), workDir, resetArgs...); err != nil {
		// Unborn repositories do not have HEAD. Removing paths from the index is
		// the equivalent unstage operation in that case.
		rmArgs := append([]string{"--literal-pathspecs", "rm", "--cached", "--ignore-unmatch", "--"}, req.FilePaths...)
		if rmErr := runGitCommand(r.Context(), workDir, rmArgs...); rmErr != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("git reset failed: %v", err))
			return
		}
	}

	log.Printf("[git-operation] project=%s repo=%s op=unstage files=%d result=ok", projectID, repositoryContext.Repository.ID, len(req.FilePaths))
	w.WriteHeader(http.StatusNoContent)
}

// GitPush pushes to remote
func (s *Server) GitPush(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	workDir := repositoryContext.Repository.CanonicalRoot
	unlock, err := s.lockRepositoryMutation(r, repositoryContext)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	defer unlock()
	if err := validateRemoteBranchOperation(r.Context(), workDir); err != nil {
		writeRepositoryOperationUnsupported(w, err)
		return
	}

	cmd := exec.CommandContext(r.Context(), "git", "push")
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// If no upstream, try push -u origin <current-branch>
		if strings.Contains(stderr.String(), "no upstream") || strings.Contains(stderr.String(), "has no upstream") {
			branch, _ := getCurrentBranchContext(r.Context(), workDir)
			if branch != "" {
				cmd2 := exec.CommandContext(r.Context(), "git", "push", "-u", "--end-of-options", "origin", branch)
				cmd2.Dir = workDir
				stdout.Reset()
				stderr.Reset()
				cmd2.Stdout = &stdout
				cmd2.Stderr = &stderr
				if err2 := cmd2.Run(); err2 != nil {
					writeError(w, http.StatusInternalServerError, fmt.Sprintf("git push failed: %s", stderr.String()))
					return
				}
				log.Printf("[git-operation] project=%s repo=%s op=push set_upstream=true result=ok", projectID, repositoryContext.Repository.ID)
				writeJSON(w, http.StatusOK, map[string]interface{}{"repository_id": repositoryContext.Repository.ID, "output": stdout.String() + stderr.String()})
				return
			}
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git push failed: %s", stderr.String()))
		return
	}

	log.Printf("[git-operation] project=%s repo=%s op=push result=ok", projectID, repositoryContext.Repository.ID)
	writeJSON(w, http.StatusOK, map[string]interface{}{"repository_id": repositoryContext.Repository.ID, "output": stdout.String() + stderr.String()})
}

// GitPull pulls from remote
func (s *Server) GitPull(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	workDir := repositoryContext.Repository.CanonicalRoot
	unlock, err := s.lockRepositoryMutation(r, repositoryContext)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	defer unlock()
	if err := validateRemoteBranchOperation(r.Context(), workDir); err != nil {
		writeRepositoryOperationUnsupported(w, err)
		return
	}

	cmd := exec.CommandContext(r.Context(), "git", "pull")
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git pull failed: %s", stderr.String()))
		return
	}

	log.Printf("[git-operation] project=%s repo=%s op=pull result=ok", projectID, repositoryContext.Repository.ID)
	writeJSON(w, http.StatusOK, map[string]interface{}{"repository_id": repositoryContext.Repository.ID, "output": stdout.String() + stderr.String()})
}

func validateRemoteBranchOperation(ctx context.Context, workDir string) error {
	if gitrepo.ReadRemoteURL(ctx, workDir) == "" {
		return errors.New("selected repository has no origin remote")
	}
	status, err := gitrepo.ReadStatus(ctx, workDir)
	if err != nil {
		return fmt.Errorf("read repository status: %w", err)
	}
	if status.Detached {
		return errors.New("operation is not supported from detached HEAD")
	}
	return nil
}

// validateRemoteTrackingBranch verifies name refers to an existing
// remote-tracking branch (e.g. origin/feature), so a checkout start point can
// never resolve to an arbitrary revision.
func validateRemoteTrackingBranch(ctx context.Context, workDir, name string) error {
	if err := runGitCommand(ctx, workDir, "check-ref-format", "--branch", name); err != nil {
		return errors.New("invalid start point")
	}
	if err := runGitCommand(ctx, workDir, "rev-parse", "--verify", "--quiet", "--end-of-options", remoteRefPrefix+name+"^{commit}"); err != nil {
		return errors.New("start point is not a remote-tracking branch")
	}
	return nil
}

// localBranchExists reports whether refs/heads/<name> is present.
func localBranchExists(ctx context.Context, workDir, name string) bool {
	return runGitCommand(ctx, workDir, "rev-parse", "--verify", "--quiet", "--end-of-options", "refs/heads/"+name) == nil
}

// --- Git helper functions ---

// parseGitStatus parses NUL-delimited porcelain output from a repository root.
func parseGitStatus(ctx context.Context, dir string) (*GitStatusResponse, error) {
	status, err := gitrepo.ReadStatus(ctx, dir)
	if err != nil {
		return nil, err
	}
	return gitStatusResponse(status), nil
}

func gitStatusResponse(status *gitrepo.Status) *GitStatusResponse {
	result := &GitStatusResponse{
		Staged:    []GitFileEntry{},
		Unstaged:  []GitFileEntry{},
		Untracked: []GitFileEntry{},
		Ahead:     status.Ahead,
	}
	for _, file := range status.Staged {
		result.Staged = append(result.Staged, GitFileEntry{Path: file.Path, OriginalPath: file.OriginalPath, Status: file.Status})
	}
	for _, file := range status.Unstaged {
		result.Unstaged = append(result.Unstaged, GitFileEntry{Path: file.Path, OriginalPath: file.OriginalPath, Status: file.Status})
	}
	for _, file := range status.Untracked {
		result.Untracked = append(result.Untracked, GitFileEntry{Path: file.Path, Status: file.Status})
	}
	return result
}

// runGitCommand runs a git command in the given directory
func runGitCommand(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

func validateGitFilePaths(paths []string) ([]string, error) {
	validated := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		clean, err := gitrepo.ValidateRelativePath(path)
		if err != nil {
			return nil, err
		}
		if !seen[clean] {
			validated = append(validated, clean)
			seen[clean] = true
		}
	}
	return validated, nil
}

// expandRenamePaths turns either side of a detected rename into the complete
// old+new path pair. This is required before reset/add: adding only the new
// path after a reset would commit an addition while silently leaving the old
// path's deletion behind.
func expandRenamePaths(ctx context.Context, workDir string, paths []string) ([]string, error) {
	validated, err := validateGitFilePaths(paths)
	if err != nil {
		return nil, err
	}
	status, err := parseGitStatus(ctx, workDir)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(validated))
	for _, path := range validated {
		seen[path] = true
	}
	for _, entries := range [][]GitFileEntry{status.Staged, status.Unstaged} {
		for _, entry := range entries {
			if entry.Status != "renamed" || entry.OriginalPath == "" || (!seen[entry.Path] && !seen[entry.OriginalPath]) {
				continue
			}
			for _, path := range []string{entry.OriginalPath, entry.Path} {
				if !seen[path] {
					validated = append(validated, path)
					seen[path] = true
				}
			}
		}
	}
	return validateGitFilePaths(validated)
}

func validateCommitRef(ctx context.Context, dir, value string) (string, error) {
	if len(value) < 4 || len(value) > 64 {
		return "", errors.New("invalid commit hash")
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return "", errors.New("invalid commit hash")
		}
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--end-of-options", value+"^{commit}")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", errors.New("commit not found")
	}
	return strings.TrimSpace(string(output)), nil
}

// getLastCommitHash returns the hash and short hash of the last commit
func getLastCommitHash(ctx context.Context, dir string) (string, string) {
	cmd := exec.CommandContext(ctx, "git", "log", "-1", "--format=%H|%h")
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", ""
	}
	parts := strings.SplitN(strings.TrimSpace(stdout.String()), "|", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

// getGitLog returns recent commits
func getGitLog(ctx context.Context, dir string, limit, skip int) ([]GitCommitInfo, error) {
	if !gitrepo.HasCommit(ctx, dir) {
		return []GitCommitInfo{}, nil
	}
	args := []string{"log", fmt.Sprintf("-%d", limit), "--format=%H%x00%h%x00%s%x00%an%x00%aI"}
	if skip > 0 {
		args = append(args, fmt.Sprintf("--skip=%d", skip))
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// No commits yet
		if strings.Contains(stderr.String(), "does not have any commits") {
			return []GitCommitInfo{}, nil
		}
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		return []GitCommitInfo{}, nil
	}

	var commits []GitCommitInfo
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(line, "\x00", 5)
		if len(parts) != 5 {
			continue
		}
		// Parse date
		t, _ := time.Parse(time.RFC3339, parts[4])
		commits = append(commits, GitCommitInfo{
			Hash:      parts[0],
			ShortHash: parts[1],
			Message:   parts[2],
			Author:    parts[3],
			Date:      t.Format(time.RFC3339),
		})
	}

	return commits, nil
}

// getCommitFiles returns files changed in a specific commit
func getCommitFiles(ctx context.Context, dir string, hash string) ([]GitCommitFileEntry, error) {
	cmd := exec.CommandContext(ctx, "git", "diff-tree", "--root", "--no-commit-id", "--name-status", "-r", "-z", "--find-renames", "--end-of-options", hash)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}

	var files []GitCommitFileEntry
	records := bytes.Split(stdout.Bytes(), []byte{0})
	for index := 0; index < len(records); {
		if len(records[index]) == 0 {
			index++
			continue
		}
		statusCode := string(records[index])
		index++
		if index >= len(records) {
			break
		}
		path := string(records[index])
		index++
		originalPath := ""
		if strings.HasPrefix(statusCode, "R") || strings.HasPrefix(statusCode, "C") {
			originalPath = path
			if index >= len(records) {
				break
			}
			path = string(records[index])
			index++
		}
		status := "modified"
		switch {
		case statusCode == "A":
			status = "added"
		case statusCode == "D":
			status = "deleted"
		case strings.HasPrefix(statusCode, "R"):
			status = "renamed"
		case strings.HasPrefix(statusCode, "C"):
			status = "copied"
		}
		files = append(files, GitCommitFileEntry{
			Path: path, OriginalPath: originalPath, Status: status,
		})
	}

	if files == nil {
		files = []GitCommitFileEntry{}
	}
	return files, nil
}

// getCommitDiff returns the full diff for a specific commit
func getCommitDiff(ctx context.Context, dir string, hash string) (string, error) {
	// Try parent diff first
	cmd := exec.CommandContext(ctx, "git", "-c", "core.quotePath=false", "diff", "--end-of-options", hash+"^", hash)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Might be the root commit, use --root
		cmd2 := exec.CommandContext(ctx, "git", "-c", "core.quotePath=false", "diff-tree", "-p", "--root", "--end-of-options", hash)
		cmd2.Dir = dir
		stdout.Reset()
		stderr.Reset()
		cmd2.Stdout = &stdout
		cmd2.Stderr = &stderr
		if err2 := cmd2.Run(); err2 != nil {
			return "", fmt.Errorf("%w: %s", err2, stderr.String())
		}
	}

	return stdout.String(), nil
}

const remoteRefPrefix = "refs/remotes/"

// parseGitBranches lists branches with tracking info
func parseGitBranches(ctx context.Context, dir string) (*GitBranchResponse, error) {
	cmd := exec.CommandContext(ctx, "git", "branch", "-a", "--format=%(HEAD)|%(refname)|%(refname:short)|%(upstream:short)|%(upstream:track)")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}

	result := &GitBranchResponse{
		Branches: []GitBranchInfo{},
	}

	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "|", 5)
		if len(parts) < 5 {
			continue
		}

		isCurrent := strings.TrimSpace(parts[0]) == "*"
		refName := strings.TrimSpace(parts[1])
		name := strings.TrimSpace(parts[2])
		upstream := strings.TrimSpace(parts[3])
		trackInfo := strings.TrimSpace(parts[4])

		// Classify from the full refname: %(refname:short) shortens
		// refs/remotes/<remote>/HEAD all the way down to "<remote>", so the short
		// name alone cannot tell a local branch from a remote one.
		isRemote := strings.HasPrefix(refName, remoteRefPrefix)
		localName := ""
		if isRemote {
			// Skip the remote's default-branch symref (refs/remotes/<remote>/HEAD)
			if strings.HasSuffix(refName, "/HEAD") {
				continue
			}
			_, branchPath, found := strings.Cut(strings.TrimPrefix(refName, remoteRefPrefix), "/")
			if !found || branchPath == "" {
				continue
			}
			localName = branchPath
		}

		branch := GitBranchInfo{
			Name:      name,
			IsCurrent: isCurrent,
			IsRemote:  isRemote,
			LocalName: localName,
			Upstream:  upstream,
		}

		if trackInfo != "" {
			// Parse [ahead N, behind M] or [ahead N] or [behind M]
			if strings.Contains(trackInfo, "ahead") {
				fmt.Sscanf(extractTrackNum(trackInfo, "ahead"), "%d", &branch.Ahead)
			}
			if strings.Contains(trackInfo, "behind") {
				fmt.Sscanf(extractTrackNum(trackInfo, "behind"), "%d", &branch.Behind)
			}
		}

		if isCurrent {
			result.Current = name
		}
		result.Branches = append(result.Branches, branch)
	}
	if result.Current == "" {
		current := exec.CommandContext(ctx, "git", "symbolic-ref", "--short", "-q", "HEAD")
		current.Dir = dir
		if output, err := current.Output(); err == nil && strings.TrimSpace(string(output)) != "" {
			result.Current = strings.TrimSpace(string(output))
			result.Branches = append([]GitBranchInfo{{Name: result.Current, IsCurrent: true}}, result.Branches...)
		} else {
			result.Current = "HEAD"
		}
	}

	return result, nil
}

func extractTrackNum(track, keyword string) string {
	idx := strings.Index(track, keyword)
	if idx < 0 {
		return "0"
	}
	sub := track[idx+len(keyword):]
	sub = strings.TrimLeft(sub, " ")
	var num string
	for _, c := range sub {
		if c >= '0' && c <= '9' {
			num += string(c)
		} else {
			break
		}
	}
	if num == "" {
		return "0"
	}
	return num
}

// collectFileDiffs returns the combined diff text for the given file paths,
// truncated to a reasonable size so it fits in an AI prompt.
func collectFileDiffs(ctx context.Context, dir string, filePaths []string) string {
	if len(filePaths) == 0 {
		return ""
	}

	const maxSize = 20000
	var buf strings.Builder
	for _, fp := range filePaths {
		diff, err := getFileDiff(ctx, dir, fp, false, false)
		if err != nil || diff == "" {
			continue
		}
		buf.WriteString(diff)
		if !strings.HasSuffix(diff, "\n") {
			buf.WriteByte('\n')
		}
		if buf.Len() > maxSize {
			buf.WriteString("...(diff truncated)\n")
			break
		}
	}
	return buf.String()
}

// generateAICommitMessage calls the configured AI agent to produce a commit
// message from the given diff text. The call is synchronous with a 60-second
// timeout; the caller should fall back to the heuristic on error.
func (s *Server) generateAICommitMessage(parentCtx context.Context, workDir, diff string) (string, error) {
	ctx, cancel := context.WithTimeout(parentCtx, 60*time.Second)
	defer cancel()

	a, agentName, model, effort, err := s.resolveUtilityBackend(ctx)
	if err != nil {
		return "", err
	}

	prompt := "Based on the following git diff, generate a concise commit message following the Conventional Commits format.\n\n" +
		"Rules:\n" +
		"- Format: type(scope): description\n" +
		"- Types: feat, fix, refactor, docs, test, chore, style, perf, ci, build\n" +
		"- scope is optional; use it when changes are clearly scoped to one module\n" +
		"- Keep the first line under 72 characters\n" +
		"- For complex changes, add a blank line then a brief body (1-3 lines)\n" +
		"- Output ONLY the raw commit message text — no markdown fences, no quotes, no explanation\n" +
		"- Do NOT use any tools; just respond with the commit message\n\n" +
		"Diff:\n" + diff

	log.Printf("[git] commit-message agent=%s model=%s effort=%s", agentName, model, effort)

	var textBuf strings.Builder
	_, err = a.SendMessage(ctx, agent.Request{
		ConversationID: fmt.Sprintf("commit-msg-%d", time.Now().UnixNano()),
		WorkDir:        workDir,
		Model:          model,
		Effort:         effort,
		Message:        prompt,
	}, func(event agent.StreamEvent) {
		if event.Type == agent.EventTextDelta {
			textBuf.WriteString(event.Content)
		}
	})
	if err != nil {
		return "", fmt.Errorf("agent: %w", err)
	}

	message := strings.TrimSpace(textBuf.String())
	if message == "" {
		return "", fmt.Errorf("agent returned empty response")
	}

	// Strip markdown code fences that some models wrap around the output
	message = strings.TrimPrefix(message, "```")
	message = strings.TrimSuffix(message, "```")
	message = strings.TrimSpace(message)

	return message, nil
}

// generateCommitMessage generates a conventional commit message heuristically
func generateCommitMessage(ctx context.Context, dir string, filePaths []string) string {
	if len(filePaths) == 0 {
		return "chore: update files"
	}

	// Categorize files
	hasTest := false
	hasDoc := false
	hasConfig := false
	hasNew := false
	hasDeleted := false

	// Get status to check new/deleted
	status, _ := parseGitStatus(ctx, dir)
	deletedSet := make(map[string]bool)
	newSet := make(map[string]bool)
	if status != nil {
		for _, f := range status.Staged {
			if f.Status == "added" {
				newSet[f.Path] = true
			}
			if f.Status == "deleted" {
				deletedSet[f.Path] = true
			}
		}
		for _, f := range status.Untracked {
			newSet[f.Path] = true
		}
	}

	var fileNames []string
	for _, p := range filePaths {
		name := filepath.Base(p)
		lower := strings.ToLower(p)

		if newSet[p] {
			hasNew = true
		}
		if deletedSet[p] {
			hasDeleted = true
		}

		if strings.Contains(lower, "test") || strings.Contains(lower, "_test.") || strings.HasSuffix(lower, ".test.ts") || strings.HasSuffix(lower, ".test.tsx") {
			hasTest = true
		}
		if strings.Contains(lower, "readme") || strings.Contains(lower, "doc") || strings.HasSuffix(lower, ".md") {
			hasDoc = true
		}
		if name == "package.json" || name == "go.mod" || name == "go.sum" ||
			strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") ||
			strings.HasSuffix(name, ".toml") || strings.HasSuffix(name, ".json") ||
			name == ".gitignore" || name == "Dockerfile" || name == "Makefile" {
			hasConfig = true
		}

		fileNames = append(fileNames, name)
	}

	// Determine prefix
	prefix := "chore"
	if hasNew && !hasDeleted {
		prefix = "feat"
	} else if hasTest && !hasDoc && !hasConfig {
		prefix = "test"
	} else if hasDoc && !hasTest && !hasConfig {
		prefix = "docs"
	}

	// Deduplicate file names
	seen := make(map[string]bool)
	var unique []string
	for _, n := range fileNames {
		if !seen[n] {
			seen[n] = true
			unique = append(unique, n)
		}
	}

	if len(unique) == 1 {
		return fmt.Sprintf("%s: update %s", prefix, unique[0])
	} else if len(unique) <= 3 {
		return fmt.Sprintf("%s: update %s", prefix, strings.Join(unique, ", "))
	}
	return fmt.Sprintf("%s: update %d files", prefix, len(unique))
}
