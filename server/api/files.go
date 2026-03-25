package api

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

var errForbidden = errors.New("access denied")

const defaultDiskQuota int64 = 256 << 20 // 256 MB

// dirSize returns the total byte size of a directory using `du -sb`.
// Consistent with workspaceSize in agent/container.go so both quota checks
// (pre-task in chat.go and mid-task in WatchWorkspace) use the same measurement.
func dirSize(root string) (int64, error) {
	out, err := exec.Command("du", "-sb", root).Output()
	if err != nil {
		return 0, err
	}
	// du output: "<bytes>\t<path>"
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) == 0 {
		return 0, fmt.Errorf("unexpected du output: %q", string(out))
	}
	var size int64
	_, err = fmt.Sscanf(parts[0], "%d", &size)
	return size, err
}

// diskQuotaBytes returns the configured disk quota, falling back to defaultDiskQuota.
func (s *Server) diskQuotaBytes() int64 {
	if q := s.Config.Get().DiskQuotaBytes; q > 0 {
		return q
	}
	return defaultDiskQuota
}

// checkDiskQuota checks if the project directory exceeds the disk quota.
// Returns (currentSize, quotaBytes, error). Error is non-nil when over quota.
// In local mode (no JWT secret), disk quota is not enforced.
func (s *Server) checkDiskQuota(root string) (int64, int64, error) {
	if s.Config.Get().JWTSecret == "" {
		return 0, 0, nil
	}
	quota := s.diskQuotaBytes()
	used, err := dirSize(root)
	if err != nil {
		return 0, quota, err
	}
	if used >= quota {
		return used, quota, fmt.Errorf("disk quota exceeded: used %dMB / %dMB", used>>20, quota>>20)
	}
	return used, quota, nil
}

// FileEntry represents a file or directory in the project tree.
type FileEntry struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"` // relative to project root
	IsDir    bool        `json:"is_dir"`
	Size     int64       `json:"size,omitempty"`
	Children []FileEntry `json:"children,omitempty"`
}

// skipDir returns true for directories that should be excluded from the file tree.
func skipDir(name string) bool {
	skip := map[string]bool{
		".git":         true,
		"node_modules": true,
		".next":        true,
		"__pycache__":  true,
		".venv":        true,
		"vendor":       true,
		"dist":         true,
		"build":        true,
		".idea":        true,
		".vscode":      true,
	}
	return skip[name]
}

// resolveFilesRoot resolves the root directory for file operations.
// Prefers the session's worktree_path when session_id query param is provided.
// Returns errForbidden when the caller does not own the resource.
func (s *Server) resolveFilesRoot(r *http.Request, projectID string) (string, error) {
	if sid := r.URL.Query().Get("session_id"); sid != "" {
		sess, err := s.Store.GetSession(sid)
		if err != nil || sess == nil {
			return "", fmt.Errorf("session not found")
		}
		if GetUserRole(r) != "admin" && sess.UserID != GetUserID(r) {
			return "", errForbidden
		}
		if sess.WorktreeMode == "worktree" && sess.WorktreePath != "" {
			return sess.WorktreePath, nil
		}
	}
	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		return "", fmt.Errorf("project not found")
	}
	if GetUserRole(r) != "admin" && project.OwnerID != GetUserID(r) {
		return "", errForbidden
	}
	if project.GitRepoPath == "" {
		return "", fmt.Errorf("project has no working directory")
	}
	return project.GitRepoPath, nil
}

// ListProjectFiles returns the file tree of a project's git_repo_path (or worktree path).
// GET /api/projects/{projectId}/files[?session_id=xxx]
func (s *Server) ListProjectFiles(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		if errors.Is(err, errForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	// Build file tree (max 3 levels deep to avoid huge responses)
	maxDepth := 10
	tree, err := buildFileTree(root, root, 0, maxDepth)
	if err != nil {
		log.Printf("[api] ListProjectFiles(%s) read dir error: %v", projectID, err)
		writeError(w, http.StatusInternalServerError, "failed to read directory: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, tree)
}

func buildFileTree(basePath, currentPath string, depth, maxDepth int) ([]FileEntry, error) {
	if depth >= maxDepth {
		return nil, nil
	}

	entries, err := os.ReadDir(currentPath)
	if err != nil {
		return nil, err
	}

	var result []FileEntry
	for _, e := range entries {
		name := e.Name()

		// Skip hidden files/dirs (except some useful ones)
		if strings.HasPrefix(name, ".") && name != ".env.example" {
			continue
		}

		if e.IsDir() && skipDir(name) {
			continue
		}

		relPath, _ := filepath.Rel(basePath, filepath.Join(currentPath, name))

		entry := FileEntry{
			Name:  name,
			Path:  relPath,
			IsDir: e.IsDir(),
		}

		if e.IsDir() {
			children, err := buildFileTree(basePath, filepath.Join(currentPath, name), depth+1, maxDepth)
			if err == nil {
				entry.Children = children
			}
		} else {
			info, err := e.Info()
			if err == nil {
				entry.Size = info.Size()
			}
		}

		result = append(result, entry)
	}

	// Sort: directories first, then files, alphabetical within each group
	sortFileEntries(result)

	return result, nil
}

func sortFileEntries(entries []FileEntry) {
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			// Directories before files
			if entries[j].IsDir && !entries[i].IsDir {
				entries[i], entries[j] = entries[j], entries[i]
			} else if entries[i].IsDir == entries[j].IsDir && strings.ToLower(entries[i].Name) > strings.ToLower(entries[j].Name) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
}

// ReadProjectFile reads the content of a single file from the project directory.
// GET /api/projects/{projectId}/file-content?path=relative/path/to/file[&session_id=xxx]
func (s *Server) ReadProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		if errors.Is(err, errForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}

	// Security: prevent path traversal
	cleanPath := filepath.Clean(relPath)
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}

	fullPath := filepath.Join(root, cleanPath)

	// Verify the resolved path is still within the root directory (resolve symlinks)
	absProject, err := filepath.EvalSymlinks(root)
	if err != nil {
		absProject, _ = filepath.Abs(root)
	}
	// For the file path: use EvalSymlinks if the file exists, otherwise use Abs
	absFile, evalErr := filepath.EvalSymlinks(fullPath)
	if evalErr != nil {
		absFile, _ = filepath.Abs(fullPath)
	}
	if !strings.HasPrefix(absFile, absProject+string(filepath.Separator)) && absFile != absProject {
		writeError(w, http.StatusBadRequest, "path outside project directory")
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "file not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory")
		return
	}

	// Limit file size to 2MB
	const maxSize = 2 * 1024 * 1024
	if info.Size() > maxSize {
		writeError(w, http.StatusBadRequest, "file too large (max 2MB)")
		return
	}

	// Check if file is likely binary
	if isBinaryFile(fullPath) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"path":    cleanPath,
			"binary":  true,
			"size":    info.Size(),
			"content": "",
		})
		return
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		log.Printf("[api] ReadProjectFile(%s,%s) read file error: %v", projectID, cleanPath, err)
		writeError(w, http.StatusInternalServerError, "failed to read file")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"path":    cleanPath,
		"binary":  false,
		"size":    info.Size(),
		"content": string(content),
	})
}

// WriteProjectFile writes (creates or updates) a text file in the project directory.
// PUT /api/projects/{projectId}/file-content[?session_id=xxx]
// Body: {"path": "relative/path", "content": "file content"}
func (s *Server) WriteProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		if errors.Is(err, errForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	// Enforce 2MB content limit
	const maxSize = 2 * 1024 * 1024
	if len(req.Content) > maxSize {
		writeError(w, http.StatusBadRequest, "content too large (max 2MB)")
		return
	}

	fullPath, err := resolveSecurePath(root, req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Atomic write: write to a temp file in the same directory, then rename.
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create directory")
		return
	}
	tmp, err := os.CreateTemp(dir, ".nexbox-tmp-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create temp file")
		return
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op if already renamed
	}()
	if _, err := tmp.WriteString(req.Content); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write content")
		return
	}
	if err := tmp.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to flush content")
		return
	}
	if err := os.Rename(tmpName, fullPath); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save file")
		return
	}

	rel, _ := filepath.Rel(root, fullPath)
	log.Printf("[api] WriteProjectFile(%s) wrote %s (%d bytes)", projectID, rel, len(req.Content))

	if s.ContainerMgr != nil {
		s.ContainerMgr.TouchActivity(projectID)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"path": rel,
		"size": len(req.Content),
	})
}

// SearchProjectFiles searches for files by name or content.
// GET /api/projects/{projectId}/file-search?q=query&type=name|content[&session_id=xxx]
func (s *Server) SearchProjectFiles(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		if errors.Is(err, errForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}
	searchType := r.URL.Query().Get("type")
	if searchType == "" {
		searchType = "name"
	}
	queryLower := strings.ToLower(query)
	const maxResults = 50

	if searchType == "content" {
		results := searchFileContents(root, root, queryLower, maxResults)
		writeJSON(w, http.StatusOK, results)
		return
	}

	// Default: name search
	var results []map[string]interface{}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || len(results) >= maxResults {
			return filepath.SkipDir
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && skipDir(name) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if strings.Contains(strings.ToLower(name), queryLower) {
			rel, _ := filepath.Rel(root, path)
			results = append(results, map[string]interface{}{
				"path": rel,
				"name": name,
			})
		}
		return nil
	})
	if results == nil {
		results = []map[string]interface{}{}
	}
	writeJSON(w, http.StatusOK, results)
}

type contentMatch struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

func searchFileContents(root, current, query string, maxResults int) []contentMatch {
	var results []contentMatch
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || len(results) >= maxResults {
			return filepath.SkipDir
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && skipDir(name) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 1024*1024 {
			return nil
		}
		if isBinaryFile(path) {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if strings.Contains(strings.ToLower(line), query) {
				rel, _ := filepath.Rel(root, path)
				preview := line
				if len(preview) > 200 {
					preview = preview[:200] + "..."
				}
				results = append(results, contentMatch{
					Path:    rel,
					Line:    lineNum,
					Content: strings.TrimSpace(preview),
				})
				if len(results) >= maxResults {
					return filepath.SkipDir
				}
			}
		}
		return nil
	})
	return results
}

// isBinaryFile is defined in diff.go

// resolveSecurePath validates a relative path and returns the full path within root.
// Returns an error if the path is invalid or escapes the root directory.
func resolveSecurePath(root, relPath string) (string, error) {
	cleanPath := filepath.Clean(relPath)
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		return "", fmt.Errorf("invalid path")
	}
	fullPath := filepath.Join(root, cleanPath)

	absProject, err := filepath.EvalSymlinks(root)
	if err != nil {
		absProject, _ = filepath.Abs(root)
	}
	absFile, evalErr := filepath.EvalSymlinks(fullPath)
	if evalErr != nil {
		absFile, _ = filepath.Abs(fullPath)
	}
	if !strings.HasPrefix(absFile, absProject+string(filepath.Separator)) && absFile != absProject {
		return "", fmt.Errorf("path outside project directory")
	}
	return fullPath, nil
}

// UploadProjectFile uploads a file into the project directory.
// POST /api/projects/{projectId}/files/upload
// multipart/form-data with fields: file (the file), path (relative directory)
func (s *Server) UploadProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		if errors.Is(err, errForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	// Check disk quota before accepting upload
	if _, _, qErr := s.checkDiskQuota(root); qErr != nil {
		writeError(w, http.StatusForbidden, qErr.Error())
		return
	}

	// 10 MB max upload
	const maxUpload = 10 << 20
	if err := r.ParseMultipartForm(maxUpload); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form: "+err.Error())
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file field is required")
		return
	}
	defer file.Close()

	// Destination relative path
	destDir := r.FormValue("path")
	if destDir == "" {
		destDir = "."
	}

	destRelPath := filepath.Join(destDir, header.Filename)
	fullPath, err := resolveSecurePath(root, destRelPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Create parent directories if needed
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create directory")
		return
	}

	dst, err := os.Create(fullPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create file: "+err.Error())
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, file)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write file: "+err.Error())
		return
	}

	rel, _ := filepath.Rel(root, fullPath)
	log.Printf("[api] UploadProjectFile(%s) wrote %s (%d bytes)", projectID, rel, written)

	// Touch container activity so idle reaper knows the user is active.
	if s.ContainerMgr != nil {
		s.ContainerMgr.TouchActivity(projectID)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"path": rel,
		"size": written,
	})
}

// DownloadProjectFile serves a file from the project directory as a download.
// GET /api/projects/{projectId}/files/download?path=relative/path/to/file[&session_id=xxx]
func (s *Server) DownloadProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		if errors.Is(err, errForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}

	fullPath, err := resolveSecurePath(root, relPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "file not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory")
		return
	}

	// 50 MB max download
	const maxDownload = 50 << 20
	if info.Size() > maxDownload {
		writeError(w, http.StatusBadRequest, "file too large for download (max 50MB)")
		return
	}

	// Touch container activity so idle reaper knows the user is active.
	if s.ContainerMgr != nil {
		s.ContainerMgr.TouchActivity(projectID)
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(fullPath)))
	http.ServeFile(w, r, fullPath)
}

// DeleteProjectFile deletes a file from the project directory.
// DELETE /api/projects/{projectId}/files/delete?path=relative/path/to/file[&session_id=xxx]
func (s *Server) DeleteProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		if errors.Is(err, errForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}

	fullPath, err := resolveSecurePath(root, relPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "file not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory, not a file")
		return
	}

	if err := os.Remove(fullPath); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete file: "+err.Error())
		return
	}

	log.Printf("[api] DeleteProjectFile(%s) deleted %s", projectID, relPath)

	// Touch container activity so idle reaper knows the user is active.
	if s.ContainerMgr != nil {
		s.ContainerMgr.TouchActivity(projectID)
	}

	writeJSON(w, http.StatusOK, map[string]string{"deleted": relPath})
}
