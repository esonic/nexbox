package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/esonic/nexbox/gitrepo"
	"github.com/go-chi/chi/v5"
)

// GitDiffFile represents a single changed file with stats
type GitDiffFile struct {
	Path          string `json:"path"`
	OriginalPath  string `json:"original_path,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	Status        string `json:"status"`
	Insertions    int    `json:"insertions"`
	Deletions     int    `json:"deletions"`
	Binary        bool   `json:"binary,omitempty"`
}

// GitDiffResponse represents the full diff response
type GitDiffResponse struct {
	RepositoryID string        `json:"repository_id,omitempty"`
	Files        []GitDiffFile `json:"files"`
	TotalFiles   int           `json:"total_files"`
	Insertions   int           `json:"insertions"`
	Deletions    int           `json:"deletions"`
	Diff         string        `json:"diff"`
}

// GetProjectDiff returns the git diff for a project's directory.
func (s *Server) GetProjectDiff(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		if gitrepo.ErrorCode(err) == gitrepo.CodeNotFound && r.URL.Query().Get("repository_id") == "" {
			writeJSON(w, http.StatusOK, GitDiffResponse{Files: []GitDiffFile{}})
			return
		}
		writeRepositoryError(w, err)
		return
	}

	workDir := repositoryContext.Repository.CanonicalRoot

	diff, err := runGitDiff(r.Context(), workDir)
	if err != nil {
		log.Printf("[diff] git diff error in %s: %v", workDir, err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git diff failed: %v", err))
		return
	}

	files, err := runGitDiffStats(r.Context(), workDir)
	if err != nil {
		log.Printf("[diff] git diff stats error in %s: %v", workDir, err)
		files = []GitDiffFile{}
	}
	addWorkspacePaths(repositoryContext, files)

	totalIns := 0
	totalDel := 0
	for _, f := range files {
		totalIns += f.Insertions
		totalDel += f.Deletions
	}

	writeJSON(w, http.StatusOK, GitDiffResponse{
		RepositoryID: repositoryContext.Repository.ID,
		Files:        files,
		TotalFiles:   len(files),
		Insertions:   totalIns,
		Deletions:    totalDel,
		Diff:         diff,
	})
}

// GetProjectDiffStats returns only the diff stats.
func (s *Server) GetProjectDiffStats(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		if gitrepo.ErrorCode(err) != gitrepo.CodeNotFound || r.URL.Query().Get("repository_id") != "" {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, GitDiffResponse{
			Files: []GitDiffFile{}, TotalFiles: 0, Insertions: 0, Deletions: 0,
		})
		return
	}
	workDir := repositoryContext.Repository.CanonicalRoot

	files, err := runGitDiffStats(r.Context(), workDir)
	if err != nil {
		log.Printf("[diff] git diff stats error in %s: %v", workDir, err)
		files = []GitDiffFile{}
	}
	addWorkspacePaths(repositoryContext, files)

	totalIns := 0
	totalDel := 0
	for _, f := range files {
		totalIns += f.Insertions
		totalDel += f.Deletions
	}

	writeJSON(w, http.StatusOK, GitDiffResponse{
		RepositoryID: repositoryContext.Repository.ID,
		Files:        files, TotalFiles: len(files), Insertions: totalIns, Deletions: totalDel,
	})
}

// GetProjectFileDiff returns the diff for a single file.
func (s *Server) GetProjectFileDiff(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}

	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	repositoryContext, err := s.resolveRepositoryContext(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	workDir := repositoryContext.Repository.CanonicalRoot

	fullContext := r.URL.Query().Get("full") == "true"
	staged := r.URL.Query().Get("staged") == "true"
	diff, err := getFileDiff(r.Context(), workDir, filePath, fullContext, staged)
	if err != nil {
		if gitrepo.ErrorCode(err) != "" {
			writeRepositoryError(w, err)
			return
		}
		log.Printf("[diff] file diff error for %s in %s: %v", filePath, workDir, err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("file diff failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"repository_id": repositoryContext.Repository.ID, "diff": diff})
}

// GetProjectFileHead returns the committed (HEAD) version of a single file.
// The editor uses it as the baseline for VSCode-style dirty-diff gutter markers.
// Untracked/new files (no HEAD blob) return exists=false with empty content, so
// the editor renders the whole file as added — matching VSCode.
// GET /api/projects/{projectId}/git-file-head?path=...[&session_id=xxx]
func (s *Server) GetProjectFileHead(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")

	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}
	repositoryContext, cleanPath, err := s.resolveRepositoryFileContext(r, projectID, relPath)
	if err != nil {
		if gitrepo.ErrorCode(err) == gitrepo.CodeNotFound && r.URL.Query().Get("repository_id") == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{"path": relPath, "in_repo": false, "exists": false, "content": ""})
			return
		}
		writeRepositoryError(w, err)
		return
	}
	cmd := exec.CommandContext(r.Context(), "git", "show", "--end-of-options", "HEAD:"+cleanPath)
	cmd.Dir = repositoryContext.Repository.CanonicalRoot
	out, err := cmd.Output()
	if err != nil {
		// Non-zero exit means the file does not exist at HEAD (new/untracked).
		writeJSON(w, http.StatusOK, map[string]interface{}{"repository_id": repositoryContext.Repository.ID, "path": cleanPath, "in_repo": true, "exists": false, "content": ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"repository_id": repositoryContext.Repository.ID, "path": cleanPath, "in_repo": true, "exists": true, "content": string(out)})
}

// --- Helper functions ---

func getFileDiff(ctx context.Context, dir string, filePath string, fullContext bool, staged bool) (string, error) {
	cleanPath, err := gitrepo.ValidateRelativePath(filePath)
	if err != nil {
		return "", err
	}
	filePath = cleanPath
	diffPaths, err := expandRenamePaths(ctx, dir, []string{filePath})
	if err != nil {
		return "", err
	}
	baseline, err := resolveDiffBaseline(ctx, dir)
	if err != nil {
		return "", err
	}
	args := []string{"-c", "core.quotePath=false", "--literal-pathspecs", "diff"}
	if fullContext {
		args = append(args, "-U99999999")
	}
	if staged {
		// Show only staged changes: index vs HEAD
		args = append(args, "--cached", baseline, "--")
	} else {
		// Show all working tree changes vs HEAD (default behavior)
		args = append(args, baseline, "--")
	}
	args = append(args, diffPaths...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git diff for %s: %w: %s", filePath, err, strings.TrimSpace(stderr.String()))
	}

	if stdout.Len() > 0 {
		return stdout.String(), nil
	}
	// A staged-only request has no representation for an untracked file. More
	// importantly, an empty tracked-file diff must stay empty rather than being
	// misrepresented as a full-file addition against /dev/null.
	if staged {
		return "", nil
	}
	untracked, err := isUntrackedFile(ctx, dir, filePath)
	if err != nil {
		return "", err
	}
	if !untracked {
		return "", nil
	}

	return generateNoIndexDiff(ctx, dir, filePath)
}

func isUntrackedFile(ctx context.Context, dir, filePath string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "--literal-pathspecs", "ls-files", "--others", "--exclude-standard", "-z", "--", filePath)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("git ls-files for %s: %w: %s", filePath, err, strings.TrimSpace(stderr.String()))
	}
	for _, record := range bytes.Split(stdout.Bytes(), []byte{0}) {
		if string(record) == filePath {
			return true, nil
		}
	}
	return false, nil
}

func runGitDiff(ctx context.Context, dir string) (string, error) {
	baseline, err := resolveDiffBaseline(ctx, dir)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "core.quotePath=false", "diff", baseline)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, stderr.String())
	}

	trackedDiff := stdout.String()

	untrackedDiff := generateUntrackedDiff(ctx, dir)
	if untrackedDiff != "" {
		if trackedDiff != "" && !strings.HasSuffix(trackedDiff, "\n") {
			trackedDiff += "\n"
		}
		trackedDiff += untrackedDiff
	}

	return trackedDiff, nil
}

func runGitDiffStats(ctx context.Context, dir string) ([]GitDiffFile, error) {
	return runGitDiffStatsWithStatus(ctx, dir, nil)
}

func runGitDiffStatsWithStatus(ctx context.Context, dir string, status *gitrepo.Status) ([]GitDiffFile, error) {
	baseline, err := resolveDiffBaseline(ctx, dir)
	if err != nil {
		return nil, err
	}
	numstatCmd := exec.CommandContext(ctx, "git", "diff", baseline, "--numstat", "-z")
	numstatCmd.Dir = dir
	var numstatOut, numstatErr bytes.Buffer
	numstatCmd.Stdout = &numstatOut
	numstatCmd.Stderr = &numstatErr

	if err := numstatCmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff numstat: %w: %s", err, numstatErr.String())
	}

	fileMap, orderedPaths := parseNumstatZ(numstatOut.Bytes())
	if status == nil {
		var statusErr error
		status, statusErr = gitrepo.ReadStatus(ctx, dir)
		if statusErr != nil {
			return nil, statusErr
		}
	}
	statusByPath := make(map[string]gitrepo.FileStatus)
	for _, entries := range [][]gitrepo.FileStatus{status.Staged, status.Unstaged} {
		for _, entry := range entries {
			if current, exists := statusByPath[entry.Path]; !exists || diffStatusPriority(entry.Status) > diffStatusPriority(current.Status) {
				statusByPath[entry.Path] = entry
			}
			if _, ok := fileMap[entry.Path]; !ok {
				fileMap[entry.Path] = &GitDiffFile{Path: entry.Path, OriginalPath: entry.OriginalPath, Status: entry.Status}
				orderedPaths = append(orderedPaths, entry.Path)
			}
		}
	}
	for path, entry := range statusByPath {
		fileMap[path].Status = entry.Status
		fileMap[path].OriginalPath = entry.OriginalPath
	}
	for _, entry := range status.Untracked {
		fullPath := filepath.Join(dir, filepath.FromSlash(entry.Path))
		binary := isBinaryFile(fullPath)
		insertions := 0
		if !binary {
			insertions = countFileLines(ctx, fullPath)
		}
		fileMap[entry.Path] = &GitDiffFile{Path: entry.Path, Status: "added", Insertions: insertions, Binary: binary}
		orderedPaths = append(orderedPaths, entry.Path)
	}
	seen := make(map[string]bool)
	result := make([]GitDiffFile, 0, len(orderedPaths))
	for _, path := range orderedPaths {
		if seen[path] {
			continue
		}
		seen[path] = true
		if file := fileMap[path]; file != nil {
			result = append(result, *file)
		}
	}
	return result, nil
}

func diffStatusPriority(status string) int {
	switch status {
	case "renamed", "copied":
		return 4
	case "added", "deleted", "conflicted":
		return 3
	case "modified":
		return 2
	default:
		return 1
	}
}

func getUntrackedFiles(ctx context.Context, dir string) []string {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--others", "--exclude-standard", "-z")
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return nil
	}

	var files []string
	for _, record := range bytes.Split(stdout.Bytes(), []byte{0}) {
		if len(record) > 0 {
			path := string(record)
			info, statErr := os.Lstat(filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(path, "/"))))
			if statErr == nil && info.IsDir() {
				continue
			}
			files = append(files, path)
		}
	}
	return files
}

func parseNumstatZ(data []byte) (map[string]*GitDiffFile, []string) {
	files := make(map[string]*GitDiffFile)
	ordered := []string{}
	for offset := 0; offset < len(data); {
		firstTab := bytes.IndexByte(data[offset:], '\t')
		if firstTab < 0 {
			break
		}
		firstTab += offset
		secondTab := bytes.IndexByte(data[firstTab+1:], '\t')
		if secondTab < 0 {
			break
		}
		secondTab += firstTab + 1
		end := bytes.IndexByte(data[secondTab+1:], 0)
		if end < 0 {
			break
		}
		end += secondTab + 1
		insertionsRaw := string(data[offset:firstTab])
		deletionsRaw := string(data[firstTab+1 : secondTab])
		path := string(data[secondTab+1 : end])
		offset = end + 1
		originalPath := ""
		if path == "" {
			oldEnd := bytes.IndexByte(data[offset:], 0)
			if oldEnd < 0 {
				break
			}
			oldEnd += offset
			originalPath = string(data[offset:oldEnd])
			offset = oldEnd + 1
			newEnd := bytes.IndexByte(data[offset:], 0)
			if newEnd < 0 {
				break
			}
			newEnd += offset
			path = string(data[offset:newEnd])
			offset = newEnd + 1
		}
		insertions, insertErr := strconv.Atoi(insertionsRaw)
		deletions, deleteErr := strconv.Atoi(deletionsRaw)
		binary := insertErr != nil || deleteErr != nil
		files[path] = &GitDiffFile{Path: path, OriginalPath: originalPath, Status: "modified", Insertions: insertions, Deletions: deletions, Binary: binary}
		ordered = append(ordered, path)
	}
	return files, ordered
}

func isBinaryFile(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	// Opening a FIFO can block indefinitely and devices/sockets are not useful
	// text diffs. Keep such entries visible in the Changes list as binary-like
	// 0/0 entries without trying to read them.
	if !info.Mode().IsRegular() {
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	if n == 0 {
		return false
	}
	return bytes.Contains(buf[:n], []byte{0})
}

func countFileLines(ctx context.Context, path string) int {
	info, err := os.Lstat(path)
	if err != nil {
		return 0
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil || target == "" {
			return 0
		}
		count := strings.Count(target, "\n")
		if target[len(target)-1] != '\n' {
			count++
		}
		return count
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return 0
	}
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	buffer := make([]byte, 64*1024)
	count := 0
	hasData := false
	lastByte := byte(0)
	for {
		if ctx.Err() != nil {
			return 0
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			hasData = true
			lastByte = buffer[read-1]
			count += bytes.Count(buffer[:read], []byte{'\n'})
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0
		}
	}
	if hasData && lastByte != '\n' {
		count++
	}
	return count
}

func generateUntrackedDiff(ctx context.Context, dir string) string {
	files := getUntrackedFiles(ctx, dir)
	if len(files) == 0 {
		return ""
	}

	var buf strings.Builder
	for _, filePath := range files {
		diff, err := generateNoIndexDiff(ctx, dir, filePath)
		if err == nil {
			buf.WriteString(diff)
		}
	}

	return buf.String()
}

func generateNoIndexDiff(ctx context.Context, dir, filePath string) (string, error) {
	cleanPath, err := gitrepo.ValidateRelativePath(filePath)
	if err != nil {
		return "", err
	}
	fullPath, err := gitrepo.ResolveFilePath(gitrepo.Repository{CanonicalRoot: dir}, cleanPath)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(fullPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 && !info.Mode().IsRegular() {
		return "", nil
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "core.quotePath=false", "--literal-pathspecs", "diff", "--no-index", "--", "/dev/null", cleanPath)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return "", fmt.Errorf("git diff --no-index: %w: %s", err, stderr.String())
		}
	}
	return stdout.String(), nil
}

// resolveDiffBaseline returns HEAD when it exists, otherwise Git's empty-tree
// object for this repository's configured object format (SHA-1 or SHA-256).
func resolveDiffBaseline(ctx context.Context, dir string) (string, error) {
	head := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	head.Dir = dir
	if output, err := head.Output(); err == nil {
		return strings.TrimSpace(string(output)), nil
	}
	emptyTree := exec.CommandContext(ctx, "git", "hash-object", "-t", "tree", "--stdin")
	emptyTree.Dir = dir
	output, err := emptyTree.Output()
	if err != nil {
		return "", fmt.Errorf("resolve empty-tree baseline: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func addWorkspacePaths(repositoryContext gitrepo.Context, files []GitDiffFile) {
	for index := range files {
		if workspacePath, ok := gitrepo.WorkspacePath(repositoryContext.WorkspaceRoot, repositoryContext.Repository, files[index].Path); ok {
			files[index].WorkspacePath = workspacePath
		}
	}
}
