package api

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

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
func (s *Server) resolveFilesRoot(r *http.Request, projectID string) (string, error) {
	if sid := r.URL.Query().Get("session_id"); sid != "" {
		sess, err := s.Store.GetSession(sid)
		if err == nil && sess != nil && sess.WorktreeMode == "worktree" && sess.WorktreePath != "" {
			return sess.WorktreePath, nil
		}
	}
	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		return "", fmt.Errorf("project not found")
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
		writeError(w, http.StatusBadRequest, err.Error())
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
		writeError(w, http.StatusBadRequest, err.Error())
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

	// Verify the resolved path is still within the root directory
	absProject, _ := filepath.Abs(root)
	absFile, _ := filepath.Abs(fullPath)
	if !strings.HasPrefix(absFile, absProject) {
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

// SearchProjectFiles searches for files by name or content.
// GET /api/projects/{projectId}/file-search?q=query&type=name|content[&session_id=xxx]
func (s *Server) SearchProjectFiles(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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
					Content: fmt.Sprintf("%d: %s", lineNum, strings.TrimSpace(preview)),
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
