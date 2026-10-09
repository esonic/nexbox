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
	Name      string      `json:"name"`
	Path      string      `json:"path"` // relative to project root
	IsDir     bool        `json:"is_dir"`
	IsSymlink bool        `json:"is_symlink,omitempty"`
	Size      int64       `json:"size,omitempty"`
	Children  []FileEntry `json:"children,omitempty"`
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
	workspace, err := s.resolveEffectiveWorkspace(r, projectID)
	if err != nil {
		return "", err
	}
	if r.URL.Query().Get("repository_id") == "" {
		return workspace.Root, nil
	}
	repositoryContext, err := s.repositoryResolver().Resolve(r.Context(), workspace.Root, r.URL.Query().Get("repository_id"))
	if err != nil {
		return "", err
	}
	return repositoryContext.Repository.CanonicalRoot, nil
}

func rejectRepositoryDirectoryOperation(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Query().Get("repository_id") == "" {
		return false
	}
	writeRepositoryOperationUnsupported(w, fmt.Errorf("repository-scoped directory operations are not supported"))
	return true
}

// ListProjectFiles returns the immediate children of a directory in the project.
// Lazy loading: callers pass ?path=relative/dir to expand a specific folder; omit for root.
// GET /api/projects/{projectId}/files[?path=...&session_id=xxx]
func (s *Server) ListProjectFiles(w http.ResponseWriter, r *http.Request) {
	if rejectRepositoryDirectoryOperation(w, r) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}

	relPath := r.URL.Query().Get("path")
	cleanPath := filepath.Clean(relPath)
	if cleanPath == "." || cleanPath == "/" {
		cleanPath = ""
	}
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}

	target := root
	if cleanPath != "" {
		target = filepath.Join(root, cleanPath)
	}

	entries, err := listDirLevel(root, target)
	if err != nil {
		log.Printf("[api] ListProjectFiles(%s, %q) error: %v", projectID, cleanPath, err)
		writeError(w, http.StatusInternalServerError, "failed to read directory: "+err.Error())
		return
	}

	if entries == nil {
		entries = []FileEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// listDirLevel returns the immediate children of currentPath (no recursion).
// Symlinks pointing to directories are followed for IsDir detection and recursable expansion.
func listDirLevel(basePath, currentPath string) ([]FileEntry, error) {
	dirents, err := os.ReadDir(currentPath)
	if err != nil {
		return nil, err
	}

	result := make([]FileEntry, 0, len(dirents))
	for _, e := range dirents {
		name := e.Name()

		if strings.HasPrefix(name, ".") && name != ".env.example" {
			continue
		}

		fullPath := filepath.Join(currentPath, name)
		isSymlink := e.Type()&os.ModeSymlink != 0

		var isDir bool
		var size int64
		if isSymlink {
			// Follow the symlink to determine if it points to a directory.
			info, err := os.Stat(fullPath)
			if err != nil {
				continue // broken symlink — skip
			}
			isDir = info.IsDir()
			if !isDir {
				size = info.Size()
			}
		} else {
			isDir = e.IsDir()
			if !isDir {
				if info, err := e.Info(); err == nil {
					size = info.Size()
				}
			}
		}

		if isDir && skipDir(name) {
			continue
		}

		relPath, _ := filepath.Rel(basePath, fullPath)
		result = append(result, FileEntry{
			Name:      name,
			Path:      relPath,
			IsDir:     isDir,
			IsSymlink: isSymlink,
			Size:      size,
		})
	}

	sortFileEntries(result)
	return result, nil
}

// ListProjectFilesFlat returns a flat list of files (not directories) within the project,
// with symlink cycle detection. Used by the chat file picker.
// GET /api/projects/{projectId}/files-flat[?session_id=xxx]
func (s *Server) ListProjectFilesFlat(w http.ResponseWriter, r *http.Request) {
	if rejectRepositoryDirectoryOperation(w, r) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}

	const maxResults = 20000
	results := make([]FileEntry, 0, 256)
	visited := make(map[string]bool)

	var walk func(dirPath string)
	walk = func(dirPath string) {
		if len(results) >= maxResults {
			return
		}
		// Canonicalize to detect symlink cycles. EvalSymlinks resolves all symlinks
		// in the path; visiting the same canonical path twice means a cycle.
		canon, err := filepath.EvalSymlinks(dirPath)
		if err != nil {
			return
		}
		if visited[canon] {
			return
		}
		visited[canon] = true

		dirents, err := os.ReadDir(dirPath)
		if err != nil {
			return
		}
		for _, e := range dirents {
			if len(results) >= maxResults {
				return
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") && name != ".env.example" {
				continue
			}

			full := filepath.Join(dirPath, name)
			isSymlink := e.Type()&os.ModeSymlink != 0

			var isDir bool
			var size int64
			if isSymlink {
				info, err := os.Stat(full)
				if err != nil {
					continue
				}
				isDir = info.IsDir()
				if !isDir {
					size = info.Size()
				}
			} else {
				isDir = e.IsDir()
				if !isDir {
					if info, err := e.Info(); err == nil {
						size = info.Size()
					}
				}
			}

			if isDir && skipDir(name) {
				continue
			}

			if isDir {
				walk(full)
			} else {
				rel, _ := filepath.Rel(root, full)
				results = append(results, FileEntry{
					Name:      name,
					Path:      rel,
					IsDir:     false,
					IsSymlink: isSymlink,
					Size:      size,
				})
			}
		}
	}
	walk(root)

	writeJSON(w, http.StatusOK, results)
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
		writeRepositoryError(w, err)
		return
	}

	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}

	cleanPath := filepath.ToSlash(filepath.Clean(relPath))
	file, err := openRequestFile(r, root, relPath)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	defer file.Close()

	info, err := file.Stat()
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
	if info.Mode()&os.ModeSymlink != 0 {
		target, readErr := file.Readlink()
		if readErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to read symlink")
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"path": cleanPath, "binary": false, "size": len(target), "content": target, "symlink": true,
		})
		return
	}

	// Limit file size to 2MB
	const maxSize = 2 * 1024 * 1024
	if info.Size() > maxSize {
		writeError(w, http.StatusBadRequest, "file too large (max 2MB)")
		return
	}

	// Check if file is likely binary
	if isBinaryRequestFile(file) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"path":    cleanPath,
			"binary":  true,
			"size":    info.Size(),
			"content": "",
		})
		return
	}

	content, err := file.ReadFile()
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
	if rejectRepositoryDirectoryOperation(w, r) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
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
