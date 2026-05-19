package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/gitlab"
)

// --- Types ---

// GitFileEntry represents a file in git status output
type GitFileEntry struct {
	Path   string `json:"path"`
	Status string `json:"status"` // modified, added, deleted, renamed, untracked
}

// GitStatusResponse represents the structured git status
type GitStatusResponse struct {
	Staged    []GitFileEntry `json:"staged"`
	Unstaged  []GitFileEntry `json:"unstaged"`
	Untracked []GitFileEntry `json:"untracked"`
	Ahead     int            `json:"ahead,omitempty"`
}

// GitCommitRequest represents a commit request
type GitCommitRequest struct {
	FilePaths []string `json:"file_paths"`
	Message   string   `json:"message"`
}

// GitCommitResponse represents the commit result
type GitCommitResponse struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"short_hash"`
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
	Path   string `json:"path"`
	Status string `json:"status"` // added, modified, deleted, renamed
}

// GitBranchInfo represents a branch
type GitBranchInfo struct {
	Name      string `json:"name"`
	IsCurrent bool   `json:"is_current"`
	IsRemote  bool   `json:"is_remote"`
	Upstream  string `json:"upstream,omitempty"`
	Ahead     int    `json:"ahead,omitempty"`
	Behind    int    `json:"behind,omitempty"`
}

// GitBranchResponse is the response for listing branches
type GitBranchResponse struct {
	Current  string          `json:"current"`
	Branches []GitBranchInfo `json:"branches"`
}

// GitCheckoutRequest represents a branch checkout request
type GitCheckoutRequest struct {
	Branch string `json:"branch"`
	Create bool   `json:"create"`
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

// resolveWorkDir resolves the working directory from a project ID.
func (s *Server) resolveWorkDir(projectID string) (string, error) {
	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		return "", fmt.Errorf("project not found")
	}
	if project.GitRepoPath == "" {
		return "", fmt.Errorf("project has no git repo path configured")
	}
	return project.GitRepoPath, nil
}

// resolveRequestWorkDir resolves the working directory for a request.
// If the request has a ?session_id= query parameter and the session is in worktree mode
// with a created worktree, the worktree path is used instead of the project path.
func (s *Server) resolveRequestWorkDir(r *http.Request, projectID string) (string, error) {
	if sid := r.URL.Query().Get("session_id"); sid != "" {
		sess, err := s.Store.GetSession(sid)
		if err == nil && sess != nil && sess.WorktreeMode == "worktree" && sess.WorktreePath != "" {
			return sess.WorktreePath, nil
		}
	}
	return s.resolveWorkDir(projectID)
}

// --- Handlers ---

// GetGitStatus returns staged, unstaged, and untracked files
func (s *Server) GetGitStatus(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	workDir, err := s.resolveRequestWorkDir(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := parseGitStatus(workDir)
	if err != nil {
		log.Printf("[git] status error in %s: %v", workDir, err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git status failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// GitCommit performs a selective commit of specified files
func (s *Server) GitCommit(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	workDir, err := s.resolveRequestWorkDir(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

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

	// Atomic commit: reset -> add selected -> commit
	// Step 1: Reset staging area
	if err := runGitCommand(workDir, "reset", "HEAD"); err != nil {
		// For fresh repos, reset may fail - that's ok
		log.Printf("[git] reset HEAD warning (may be fresh repo): %v", err)
	}

	// Step 2: Stage only selected files
	addArgs := append([]string{"add", "--"}, req.FilePaths...)
	if err := runGitCommand(workDir, addArgs...); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git add failed: %v", err))
		return
	}

	// Step 3: Commit
	cmd := exec.Command("git", "commit", "-m", req.Message)
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git commit failed: %s", stderr.String()))
		return
	}

	// Get the commit hash
	hash, shortHash := getLastCommitHash(workDir)

	log.Printf("[git] Committed %d files: %s (%s)", len(req.FilePaths), shortHash, req.Message)
	writeJSON(w, http.StatusOK, GitCommitResponse{
		Hash:      hash,
		ShortHash: shortHash,
	})
}

// GitDiscard discards changes to a single file
func (s *Server) GitDiscard(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	workDir, err := s.resolveRequestWorkDir(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req GitDiscardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.FilePath == "" {
		writeError(w, http.StatusBadRequest, "file_path is required")
		return
	}

	// Check if the file is tracked or untracked
	status, err := parseGitStatus(workDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git status failed: %v", err))
		return
	}

	isUntracked := false
	for _, f := range status.Untracked {
		if f.Path == req.FilePath || strings.HasSuffix(f.Path, "/") && strings.HasPrefix(req.FilePath, f.Path) {
			isUntracked = true
			break
		}
	}

	if isUntracked {
		// Delete untracked file
		fullPath := filepath.Join(workDir, req.FilePath)
		if err := os.Remove(fullPath); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete file: %v", err))
			return
		}
	} else {
		// Checkout from HEAD to discard changes
		if err := runGitCommand(workDir, "checkout", "HEAD", "--", req.FilePath); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("git checkout failed: %v", err))
			return
		}
	}

	log.Printf("[git] Discarded changes to %s", req.FilePath)
	w.WriteHeader(http.StatusNoContent)
}

// GitLog returns recent commit history
func (s *Server) GitLog(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	workDir, err := s.resolveRequestWorkDir(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

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

	commits, err := getGitLog(workDir, limit, skip)
	if err != nil {
		log.Printf("[git] log error in %s: %v", workDir, err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git log failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, commits)
}

// GitCommitFiles returns the files changed in a specific commit
func (s *Server) GitCommitFiles(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	workDir, err := s.resolveRequestWorkDir(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	hash := r.URL.Query().Get("hash")
	if hash == "" {
		writeError(w, http.StatusBadRequest, "hash parameter is required")
		return
	}

	files, err := getCommitFiles(workDir, hash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get commit files: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, files)
}

// GitCommitDiff returns the full diff of a specific commit
func (s *Server) GitCommitDiff(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	workDir, err := s.resolveRequestWorkDir(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	hash := r.URL.Query().Get("hash")
	if hash == "" {
		writeError(w, http.StatusBadRequest, "hash parameter is required")
		return
	}

	diff, err := getCommitDiff(workDir, hash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get commit diff: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"diff": diff})
}

// GenerateCommitMessage generates a commit message using AI based on the
// actual diff content. Falls back to a heuristic when the agent is unavailable
// or returns an error.
func (s *Server) GenerateCommitMessage(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	workDir, err := s.resolveRequestWorkDir(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req GenerateCommitMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	diff := collectFileDiffs(workDir, req.FilePaths)
	if diff == "" {
		writeJSON(w, http.StatusOK, map[string]string{"message": generateCommitMessage(workDir, req.FilePaths)})
		return
	}

	message, err := s.generateAICommitMessage(r.Context(), workDir, diff)
	if err != nil {
		log.Printf("[git] AI commit message generation failed: %v, using heuristic", err)
		message = generateCommitMessage(workDir, req.FilePaths)
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": message})
}

// GetGitBranches returns local and remote branches with tracking info
func (s *Server) GetGitBranches(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	workDir, err := s.resolveRequestWorkDir(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := parseGitBranches(workDir)
	if err != nil {
		log.Printf("[git] branches error in %s: %v", workDir, err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git branch failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// GitCheckout switches to or creates a branch
func (s *Server) GitCheckout(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	workDir, err := s.resolveRequestWorkDir(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req GitCheckoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Branch == "" {
		writeError(w, http.StatusBadRequest, "branch name is required")
		return
	}

	var args []string
	if req.Create {
		args = []string{"checkout", "-b", req.Branch}
	} else {
		args = []string{"checkout", req.Branch}
	}

	if err := runGitCommand(workDir, args...); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git checkout failed: %v", err))
		return
	}

	log.Printf("[git] Checked out branch %s (create=%v)", req.Branch, req.Create)
	writeJSON(w, http.StatusOK, map[string]string{"branch": req.Branch})
}

// GitStage stages specified files
func (s *Server) GitStage(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	workDir, err := s.resolveRequestWorkDir(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req GitStageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.FilePaths) == 0 {
		writeError(w, http.StatusBadRequest, "at least one file path is required")
		return
	}

	addArgs := append([]string{"add", "--"}, req.FilePaths...)
	if err := runGitCommand(workDir, addArgs...); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git add failed: %v", err))
		return
	}

	log.Printf("[git] Staged %d files", len(req.FilePaths))
	w.WriteHeader(http.StatusNoContent)
}

// GitUnstage unstages specified files
func (s *Server) GitUnstage(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	workDir, err := s.resolveRequestWorkDir(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req GitStageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.FilePaths) == 0 {
		writeError(w, http.StatusBadRequest, "at least one file path is required")
		return
	}

	resetArgs := append([]string{"reset", "HEAD", "--"}, req.FilePaths...)
	if err := runGitCommand(workDir, resetArgs...); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git reset failed: %v", err))
		return
	}

	log.Printf("[git] Unstaged %d files", len(req.FilePaths))
	w.WriteHeader(http.StatusNoContent)
}

// GitPush pushes to remote
func (s *Server) GitPush(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	workDir, err := s.resolveRequestWorkDir(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	cmd := exec.Command("git", "push")
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// If no upstream, try push -u origin <current-branch>
		if strings.Contains(stderr.String(), "no upstream") || strings.Contains(stderr.String(), "has no upstream") {
			branch, _ := gitlab.GetCurrentBranch(workDir)
			if branch != "" {
				cmd2 := exec.Command("git", "push", "-u", "origin", branch)
				cmd2.Dir = workDir
				stdout.Reset()
				stderr.Reset()
				cmd2.Stdout = &stdout
				cmd2.Stderr = &stderr
				if err2 := cmd2.Run(); err2 != nil {
					writeError(w, http.StatusInternalServerError, fmt.Sprintf("git push failed: %s", stderr.String()))
					return
				}
				log.Printf("[git] Pushed branch %s (set upstream)", branch)
				writeJSON(w, http.StatusOK, GitPushPullResponse{Output: stdout.String() + stderr.String()})
				return
			}
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git push failed: %s", stderr.String()))
		return
	}

	log.Printf("[git] Pushed to remote")
	writeJSON(w, http.StatusOK, GitPushPullResponse{Output: stdout.String() + stderr.String()})
}

// GitPull pulls from remote
func (s *Server) GitPull(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	workDir, err := s.resolveRequestWorkDir(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	cmd := exec.Command("git", "pull")
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git pull failed: %s", stderr.String()))
		return
	}

	log.Printf("[git] Pulled from remote")
	writeJSON(w, http.StatusOK, GitPushPullResponse{Output: stdout.String() + stderr.String()})
}

// --- Git helper functions ---

// gitRelPrefix returns the path prefix that git appends to file paths when
// running from a subdirectory of a repo. Commands like "git diff" and
// "git status --porcelain" return paths relative to the git root, not the CWD.
// If dir is already the git root (or on error), returns "".
func gitRelPrefix(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return ""
	}
	toplevel := strings.TrimSpace(stdout.String())
	if toplevel == dir {
		return ""
	}
	rel, err := filepath.Rel(toplevel, dir)
	if err != nil || rel == "." || rel == "" {
		return ""
	}
	return rel + "/"
}

// parseGitStatus parses `git status -b --porcelain=v1` output
func parseGitStatus(dir string) (*GitStatusResponse, error) {
	prefix := gitRelPrefix(dir)

	cmd := exec.Command("git", "status", "-b", "--porcelain=v1")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}

	result := &GitStatusResponse{
		Staged:    []GitFileEntry{},
		Unstaged:  []GitFileEntry{},
		Untracked: []GitFileEntry{},
	}

	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		line := scanner.Text()
		// Parse branch tracking line: ## main...origin/main [ahead 2]
		if strings.HasPrefix(line, "## ") {
			if idx := strings.Index(line, "[ahead "); idx != -1 {
				rest := line[idx+7:]
				if end := strings.IndexAny(rest, ",]"); end != -1 {
					fmt.Sscanf(rest[:end], "%d", &result.Ahead)
				}
			}
			continue
		}
		if len(line) < 4 {
			continue
		}

		indexStatus := line[0] // staging area status
		workStatus := line[1]  // working tree status
		filePath := strings.TrimSpace(line[3:])

		// Handle renamed files: "R  old -> new"
		if strings.Contains(filePath, " -> ") {
			parts := strings.SplitN(filePath, " -> ", 2)
			filePath = parts[1]
		}

		// Strip the git-root-relative prefix so paths are relative to dir.
		// git status --porcelain always outputs paths relative to the repo root,
		// but we want paths relative to the project working directory.
		filePath = strings.TrimPrefix(filePath, prefix)

		// Untracked files
		if indexStatus == '?' && workStatus == '?' {
			result.Untracked = append(result.Untracked, GitFileEntry{
				Path:   filePath,
				Status: "untracked",
			})
			continue
		}

		// Staged changes (index status is not space or ?)
		if indexStatus != ' ' && indexStatus != '?' {
			result.Staged = append(result.Staged, GitFileEntry{
				Path:   filePath,
				Status: porcelainStatusToString(indexStatus),
			})
		}

		// Unstaged changes (work tree status is not space or ?)
		if workStatus != ' ' && workStatus != '?' {
			result.Unstaged = append(result.Unstaged, GitFileEntry{
				Path:   filePath,
				Status: porcelainStatusToString(workStatus),
			})
		}
	}

	return result, nil
}

func porcelainStatusToString(s byte) string {
	switch s {
	case 'M':
		return "modified"
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	default:
		return "modified"
	}
}

// runGitCommand runs a git command in the given directory
func runGitCommand(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

// getLastCommitHash returns the hash and short hash of the last commit
func getLastCommitHash(dir string) (string, string) {
	cmd := exec.Command("git", "log", "-1", "--format=%H|%h")
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
func getGitLog(dir string, limit, skip int) ([]GitCommitInfo, error) {
	args := []string{"log", fmt.Sprintf("-%d", limit), "--format=%H%x00%h%x00%s%x00%an%x00%aI"}
	if skip > 0 {
		args = append(args, fmt.Sprintf("--skip=%d", skip))
	}
	cmd := exec.Command("git", args...)
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
func getCommitFiles(dir string, hash string) ([]GitCommitFileEntry, error) {
	cmd := exec.Command("git", "diff-tree", "--no-commit-id", "--name-status", "-r", hash)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}

	var files []GitCommitFileEntry
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		status := "modified"
		switch {
		case parts[0] == "A":
			status = "added"
		case parts[0] == "D":
			status = "deleted"
		case strings.HasPrefix(parts[0], "R"):
			status = "renamed"
		}
		files = append(files, GitCommitFileEntry{
			Path:   parts[1],
			Status: status,
		})
	}

	if files == nil {
		files = []GitCommitFileEntry{}
	}
	return files, nil
}

// getCommitDiff returns the full diff for a specific commit
func getCommitDiff(dir string, hash string) (string, error) {
	// Try parent diff first
	cmd := exec.Command("git", "diff", hash+"^", hash)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Might be the root commit, use --root
		cmd2 := exec.Command("git", "diff-tree", "-p", "--root", hash)
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

// parseGitBranches lists branches with tracking info
func parseGitBranches(dir string) (*GitBranchResponse, error) {
	cmd := exec.Command("git", "branch", "-a", "--format=%(HEAD)|%(refname:short)|%(upstream:short)|%(upstream:track)")
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
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}

		isCurrent := strings.TrimSpace(parts[0]) == "*"
		name := strings.TrimSpace(parts[1])
		upstream := strings.TrimSpace(parts[2])
		trackInfo := strings.TrimSpace(parts[3])

		isRemote := strings.HasPrefix(name, "origin/")
		if isRemote {
			// Skip HEAD pointer
			if name == "origin/HEAD" {
				continue
			}
		}

		branch := GitBranchInfo{
			Name:      name,
			IsCurrent: isCurrent,
			IsRemote:  isRemote,
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
func collectFileDiffs(dir string, filePaths []string) string {
	if len(filePaths) == 0 {
		return ""
	}

	const maxSize = 20000
	var buf strings.Builder
	for _, fp := range filePaths {
		diff, err := getFileDiff(dir, fp, false, false)
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
	if s.Agent == nil {
		return "", fmt.Errorf("no agent configured")
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

	ctx, cancel := context.WithTimeout(parentCtx, 60*time.Second)
	defer cancel()

	var textBuf strings.Builder
	_, err := s.Agent.SendMessage(ctx, agent.Request{
		ConversationID: fmt.Sprintf("commit-msg-%d", time.Now().UnixNano()),
		WorkDir:        workDir,
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
func generateCommitMessage(dir string, filePaths []string) string {
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
	status, _ := parseGitStatus(dir)
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
