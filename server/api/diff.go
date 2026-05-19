package api

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// GitDiffFile represents a single changed file with stats
type GitDiffFile struct {
	Path       string `json:"path"`
	Status     string `json:"status"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
}

// GitDiffResponse represents the full diff response
type GitDiffResponse struct {
	Files      []GitDiffFile `json:"files"`
	TotalFiles int           `json:"total_files"`
	Insertions int           `json:"insertions"`
	Deletions  int           `json:"deletions"`
	Diff       string        `json:"diff"`
}

// resolveDiffWorkDir resolves the working directory for diff operations.
// Prefers the session's worktree_path when session_id query param is provided.
func (s *Server) resolveDiffWorkDir(r *http.Request, projectID string) string {
	if sid := r.URL.Query().Get("session_id"); sid != "" {
		sess, err := s.Store.GetSession(sid)
		if err == nil && sess != nil && sess.WorktreeMode == "worktree" && sess.WorktreePath != "" {
			return sess.WorktreePath
		}
	}
	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		return ""
	}
	return project.GitRepoPath
}

// GetProjectDiff returns the git diff for a project's directory.
func (s *Server) GetProjectDiff(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	workDir := s.resolveDiffWorkDir(r, projectID)
	if workDir == "" {
		writeError(w, http.StatusBadRequest, "project has no git repo path configured")
		return
	}

	if !isGitRepo(workDir) {
		writeJSON(w, http.StatusOK, GitDiffResponse{Files: []GitDiffFile{}})
		return
	}

	diff, err := runGitDiff(workDir)
	if err != nil {
		log.Printf("[diff] git diff error in %s: %v", workDir, err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("git diff failed: %v", err))
		return
	}

	files, err := runGitDiffStats(workDir)
	if err != nil {
		log.Printf("[diff] git diff stats error in %s: %v", workDir, err)
		files = []GitDiffFile{}
	}

	totalIns := 0
	totalDel := 0
	for _, f := range files {
		totalIns += f.Insertions
		totalDel += f.Deletions
	}

	writeJSON(w, http.StatusOK, GitDiffResponse{
		Files:      files,
		TotalFiles: len(files),
		Insertions: totalIns,
		Deletions:  totalDel,
		Diff:       diff,
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

	workDir := s.resolveDiffWorkDir(r, projectID)
	if workDir == "" || !isGitRepo(workDir) {
		writeJSON(w, http.StatusOK, GitDiffResponse{
			Files: []GitDiffFile{}, TotalFiles: 0, Insertions: 0, Deletions: 0,
		})
		return
	}

	files, err := runGitDiffStats(workDir)
	if err != nil {
		log.Printf("[diff] git diff stats error in %s: %v", workDir, err)
		files = []GitDiffFile{}
	}

	totalIns := 0
	totalDel := 0
	for _, f := range files {
		totalIns += f.Insertions
		totalDel += f.Deletions
	}

	writeJSON(w, http.StatusOK, GitDiffResponse{
		Files: files, TotalFiles: len(files), Insertions: totalIns, Deletions: totalDel,
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

	workDir := s.resolveDiffWorkDir(r, projectID)
	if workDir == "" || !isGitRepo(workDir) {
		writeError(w, http.StatusBadRequest, "project has no git repo path configured")
		return
	}

	fullContext := r.URL.Query().Get("full") == "true"
	staged := r.URL.Query().Get("staged") == "true"
	diff, err := getFileDiff(workDir, filePath, fullContext, staged)
	if err != nil {
		log.Printf("[diff] file diff error for %s in %s: %v", filePath, workDir, err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("file diff failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"diff": diff})
}

// --- Helper functions (unchanged) ---

func isGitRepo(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	return cmd.Run() == nil
}

func getFileDiff(dir string, filePath string, fullContext bool, staged bool) (string, error) {
	args := []string{"diff"}
	if fullContext {
		args = append(args, "-U99999999")
	}
	if staged {
		// Show only staged changes: index vs HEAD
		args = append(args, "--cached", "HEAD", "--", filePath)
	} else {
		// Show all working tree changes vs HEAD (default behavior)
		args = append(args, "HEAD", "--", filePath)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil && !staged {
		// Fallback for HEAD diff: try --cached (e.g. newly staged files with no HEAD ref)
		args2 := []string{"diff"}
		if fullContext {
			args2 = append(args2, "-U99999999")
		}
		args2 = append(args2, "--cached", "--", filePath)
		cmd2 := exec.Command("git", args2...)
		cmd2.Dir = dir
		stdout.Reset()
		cmd2.Stdout = &stdout
		if err2 := cmd2.Run(); err2 != nil {
			log.Printf("[diff] git diff --cached failed for %s: %v", filePath, err2)
		}
	}

	if stdout.Len() > 0 {
		return stdout.String(), nil
	}

	fullPath := filepath.Join(dir, filePath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("file not found: %s", filePath)
	}

	checkLen := len(data)
	if checkLen > 8192 {
		checkLen = 8192
	}
	if bytes.Contains(data[:checkLen], []byte{0}) {
		return fmt.Sprintf("diff --git a/%s b/%s\nnew file mode 100644\nBinary files /dev/null and b/%s differ\n",
			filePath, filePath, filePath), nil
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	lineCount := len(lines)

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", filePath, filePath))
	buf.WriteString("new file mode 100644\n")
	buf.WriteString("--- /dev/null\n")
	buf.WriteString(fmt.Sprintf("+++ b/%s\n", filePath))
	buf.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", lineCount))
	for _, line := range lines {
		buf.WriteString("+" + line + "\n")
	}

	return buf.String(), nil
}

func runGitDiff(dir string) (string, error) {
	cmd := exec.Command("git", "diff", "HEAD")
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.String() != "" && (bytes.Contains(stderr.Bytes(), []byte("unknown revision")) ||
			bytes.Contains(stderr.Bytes(), []byte("bad revision"))) {
			return runGitDiffCached(dir)
		}
		return "", fmt.Errorf("%w: %s", err, stderr.String())
	}

	trackedDiff := stdout.String()

	untrackedDiff := generateUntrackedDiff(dir)
	if untrackedDiff != "" {
		if trackedDiff != "" && !strings.HasSuffix(trackedDiff, "\n") {
			trackedDiff += "\n"
		}
		trackedDiff += untrackedDiff
	}

	return trackedDiff, nil
}

func runGitDiffCached(dir string) (string, error) {
	cmd := exec.Command("git", "diff", "--cached")
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

func runGitDiffStats(dir string) ([]GitDiffFile, error) {
	// git diff outputs paths relative to the repo root, not the CWD.
	// Strip this prefix so all returned paths are relative to dir.
	prefix := gitRelPrefix(dir)

	numstatCmd := exec.Command("git", "diff", "HEAD", "--numstat")
	numstatCmd.Dir = dir
	var numstatOut, numstatErr bytes.Buffer
	numstatCmd.Stdout = &numstatOut
	numstatCmd.Stderr = &numstatErr

	useHead := true
	if err := numstatCmd.Run(); err != nil {
		numstatCmd2 := exec.Command("git", "diff", "--cached", "--numstat")
		numstatCmd2.Dir = dir
		numstatOut.Reset()
		numstatCmd2.Stdout = &numstatOut
		if err2 := numstatCmd2.Run(); err2 != nil {
			return nil, fmt.Errorf("git diff numstat: %w", err)
		}
		useHead = false
	}

	fileMap := make(map[string]*GitDiffFile)
	var orderedPaths []string
	scanner := bufio.NewScanner(&numstatOut)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := strings.TrimPrefix(parts[2], prefix)
		if parts[0] == "-" && parts[1] == "-" {
			continue // skip binary files
		}
		ins, _ := strconv.Atoi(parts[0])
		del, _ := strconv.Atoi(parts[1])
		fileMap[path] = &GitDiffFile{
			Path: path, Status: "modified", Insertions: ins, Deletions: del,
		}
		orderedPaths = append(orderedPaths, path)
	}

	var statusArgs []string
	if useHead {
		statusArgs = []string{"diff", "HEAD", "--name-status"}
	} else {
		statusArgs = []string{"diff", "--cached", "--name-status"}
	}
	statusCmd := exec.Command("git", statusArgs...)
	statusCmd.Dir = dir
	var statusOut bytes.Buffer
	statusCmd.Stdout = &statusOut
	if err := statusCmd.Run(); err != nil {
		log.Printf("[diff] git diff --name-status failed: %v", err)
	}

	scanner2 := bufio.NewScanner(&statusOut)
	for scanner2.Scan() {
		line := scanner2.Text()
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		statusCode := parts[0]
		path := strings.TrimPrefix(parts[1], prefix)
		status := "modified"
		switch {
		case statusCode == "A":
			status = "added"
		case statusCode == "D":
			status = "deleted"
		case strings.HasPrefix(statusCode, "R"):
			status = "renamed"
		}
		if f, ok := fileMap[path]; ok {
			f.Status = status
		}
	}

	result := make([]GitDiffFile, 0, len(orderedPaths))
	for _, path := range orderedPaths {
		if f, ok := fileMap[path]; ok {
			result = append(result, *f)
		}
	}

	untrackedFiles := getUntrackedFiles(dir)
	for _, uf := range untrackedFiles {
		fullPath := filepath.Join(dir, uf)
		if isBinaryFile(fullPath) {
			continue
		}
		lineCount := countFileLines(fullPath)
		result = append(result, GitDiffFile{
			Path: uf, Status: "added", Insertions: lineCount, Deletions: 0,
		})
	}

	return result, nil
}

func getUntrackedFiles(dir string) []string {
	cmd := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return nil
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		return nil
	}

	var files []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}

func isBinaryFile(path string) bool {
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

func countFileLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	if len(data) == 0 {
		return 0
	}
	count := bytes.Count(data, []byte("\n"))
	if data[len(data)-1] != '\n' {
		count++
	}
	return count
}

func generateUntrackedDiff(dir string) string {
	files := getUntrackedFiles(dir)
	if len(files) == 0 {
		return ""
	}

	var buf strings.Builder
	for _, filePath := range files {
		fullPath := filepath.Join(dir, filePath)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		checkLen := len(data)
		if checkLen > 8192 {
			checkLen = 8192
		}
		if bytes.Contains(data[:checkLen], []byte{0}) {
			buf.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", filePath, filePath))
			buf.WriteString("new file mode 100644\n")
			buf.WriteString("Binary files /dev/null and b/" + filePath + " differ\n")
			continue
		}

		lines := strings.Split(string(data), "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lineCount := len(lines)

		buf.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", filePath, filePath))
		buf.WriteString("new file mode 100644\n")
		buf.WriteString("--- /dev/null\n")
		buf.WriteString(fmt.Sprintf("+++ b/%s\n", filePath))
		buf.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", lineCount))
		for _, line := range lines {
			buf.WriteString("+" + line + "\n")
		}
	}

	return buf.String()
}
