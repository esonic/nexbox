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

// ListProjectFiles returns the immediate children of a directory in the project.
// Lazy loading: callers pass ?path=relative/dir to expand a specific folder; omit for root.
// GET /api/projects/{projectId}/files[?path=...&session_id=xxx]
func (s *Server) ListProjectFiles(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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

// maxCompactDepth caps how many single-child links compactSingleChildDirs will
// follow, so a symlink cycle or a pathological tree can't spin forever.
const maxCompactDepth = 32

// listDirLevel returns the immediate children of currentPath, with chains of
// single-child directories compacted into one entry (see compactSingleChildDirs).
func listDirLevel(basePath, currentPath string) ([]FileEntry, error) {
	entries, err := readDirLevel(basePath, currentPath)
	if err != nil {
		return nil, err
	}
	compactSingleChildDirs(basePath, entries)
	return entries, nil
}

// compactSingleChildDirs merges a directory whose only visible child is itself a
// directory into that child (VSCode "compact folders"). The merged entry keeps
// the joined name ("com/example/app") and the deepest path, so expanding or
// dropping onto the row acts on the innermost folder. The chain is followed
// server-side because the client loads one level at a time.
func compactSingleChildDirs(basePath string, entries []FileEntry) {
	for i := range entries {
		if !entries[i].IsDir || entries[i].IsSymlink {
			continue
		}
		name, path := entries[i].Name, entries[i].Path
		for depth := 0; depth < maxCompactDepth; depth++ {
			children, err := readDirLevel(basePath, filepath.Join(basePath, path))
			if err != nil || len(children) != 1 || !children[0].IsDir || children[0].IsSymlink {
				break
			}
			name += "/" + children[0].Name
			path = children[0].Path
		}
		entries[i].Name, entries[i].Path = name, path
	}
}

// readDirLevel returns the immediate children of currentPath (no recursion).
// Symlinks pointing to directories are followed for IsDir detection and recursable expansion.
func readDirLevel(basePath, currentPath string) ([]FileEntry, error) {
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
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	const maxResults = 20000
	results := make([]FileEntry, 0, 256)
	visited := make(map[string]bool)
	// Honour .gitignore so build output doesn't crowd out source files in quick open.
	ignore := ignoreFilterForRoot(root)

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

			if excludedFromSearch(ignore, root, full, isDir) {
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
	// case=1 keeps the search case-sensitive; otherwise both the query and the
	// scanned text are folded to lower case (the historical default).
	caseSensitive := r.URL.Query().Get("case") == "1" || r.URL.Query().Get("case") == "true"
	needle := query
	if !caseSensitive {
		needle = strings.ToLower(query)
	}
	const maxResults = 50

	ignore := ignoreFilterForRoot(root)

	if searchType == "content" {
		results := searchFileContents(root, ignore, needle, caseSensitive, maxResults)
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
		if excludedFromSearch(ignore, root, path, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		hay := name
		if !caseSensitive {
			hay = strings.ToLower(name)
		}
		if strings.Contains(hay, needle) {
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

// searchFileContents scans files under root for lines containing needle. The
// caller passes needle already lower-cased when caseSensitive is false, and the
// ignore filter for root (nil outside a git repository).
func searchFileContents(root string, ignore *gitIgnoreFilter, needle string, caseSensitive bool, maxResults int) []contentMatch {
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
		if excludedFromSearch(ignore, root, path, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
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
			hay := line
			if !caseSensitive {
				hay = strings.ToLower(line)
			}
			if strings.Contains(hay, needle) {
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
