package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/esonic/nexbox/gitrepo"
	"github.com/go-chi/chi/v5"
)

const maxUploadSize = 50 << 20 // 50 MB

// resolveSafePath joins root + relPath and verifies the result stays inside root.
// Returns the absolute path, or an error suitable for a 400 response.
func resolveSafePath(root, relPath string) (string, error) {
	cleanPath := filepath.Clean(relPath)
	if cleanPath == "." {
		cleanPath = ""
	}
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		return "", errors.New("invalid path")
	}
	full := root
	if cleanPath != "" {
		full = filepath.Join(root, cleanPath)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", errors.New("invalid root")
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", errors.New("invalid path")
	}
	if absFull != absRoot && !strings.HasPrefix(absFull, absRoot+string(filepath.Separator)) {
		return "", errors.New("path outside project directory")
	}
	return absFull, nil
}

// requestFile uses os.Root for repository-scoped operations. os.Root resolves
// each path component relative to an already-open directory handle and rejects
// symlink traversal outside it, avoiding the check-then-open race inherent in
// validating an absolute path before a separate filesystem operation.
type requestFile struct {
	fullPath   string
	relative   string
	root       *os.Root
	repository bool
}

func openRequestFile(r *http.Request, root, relPath string) (*requestFile, error) {
	if r.URL.Query().Get("repository_id") == "" {
		fullPath, err := resolveSafePath(root, relPath)
		if err != nil {
			return nil, err
		}
		return &requestFile{fullPath: fullPath}, nil
	}
	clean, err := gitrepo.ValidateRelativePath(relPath)
	if err != nil {
		return nil, err
	}
	if _, validationErr := gitrepo.ResolveFilePath(gitrepo.Repository{CanonicalRoot: root}, clean); validationErr != nil && !errors.Is(validationErr, os.ErrNotExist) {
		return nil, validationErr
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, &gitrepo.Error{Code: gitrepo.CodeStale, Err: fmt.Errorf("open repository root: %w", err)}
	}
	return &requestFile{fullPath: filepath.Join(root, filepath.FromSlash(clean)), relative: filepath.FromSlash(clean), root: rootHandle, repository: true}, nil
}

func (f *requestFile) Close() {
	if f.root != nil {
		_ = f.root.Close()
	}
}

func (f *requestFile) Stat() (os.FileInfo, error) {
	if f.repository {
		return f.root.Lstat(f.relative)
	}
	return os.Stat(f.fullPath)
}

func (f *requestFile) Readlink() (string, error) {
	if f.repository {
		return readlinkAtRoot(f.root, f.relative)
	}
	return os.Readlink(f.fullPath)
}

func (f *requestFile) ReadFile() ([]byte, error) {
	if f.repository {
		opened, err := f.root.Open(f.relative)
		if err != nil {
			return nil, err
		}
		defer opened.Close()
		return io.ReadAll(opened)
	}
	return os.ReadFile(f.fullPath)
}

func (f *requestFile) Open() (*os.File, error) {
	if f.repository {
		return f.root.Open(f.relative)
	}
	return os.Open(f.fullPath)
}

func (f *requestFile) Remove() error {
	if f.repository {
		return f.root.Remove(f.relative)
	}
	return os.Remove(f.fullPath)
}

func (f *requestFile) WriteFile(data []byte, perm os.FileMode) error {
	if f.repository {
		opened, err := f.root.OpenFile(f.relative, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
		if err != nil {
			return err
		}
		if _, err := opened.Write(data); err != nil {
			_ = opened.Close()
			return err
		}
		return opened.Close()
	}
	return os.WriteFile(f.fullPath, data, perm)
}

func (f *requestFile) MkdirParent(perm os.FileMode) error {
	if f.repository {
		parent := filepath.Clean(filepath.Dir(f.relative))
		if parent == "." {
			return nil
		}
		current := ""
		for _, component := range strings.Split(parent, string(filepath.Separator)) {
			if component == "" || component == "." {
				continue
			}
			current = filepath.Join(current, component)
			if err := f.root.Mkdir(current, perm); err != nil {
				if !errors.Is(err, os.ErrExist) {
					return err
				}
				info, statErr := f.root.Stat(current)
				if statErr != nil {
					return statErr
				}
				if !info.IsDir() {
					return fmt.Errorf("parent path is not a directory: %s", current)
				}
			}
		}
		return nil
	}
	return os.MkdirAll(filepath.Dir(f.fullPath), perm)
}

func isBinaryRequestFile(file *requestFile) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	if !info.Mode().IsRegular() {
		return true
	}
	opened, err := file.Open()
	if err != nil {
		return false
	}
	defer opened.Close()
	buffer := make([]byte, 8192)
	count, _ := opened.Read(buffer)
	return count > 0 && strings.IndexByte(string(buffer[:count]), 0) >= 0
}

func (s *Server) invalidateFileGitStatus(r *http.Request, projectID, path, resolvedRoot string, file *requestFile) {
	if file.repository {
		s.repositoryResolver().InvalidateStatus(resolvedRoot)
		return
	}
	repositoryContext, _, err := s.resolveRepositoryFileContext(r, projectID, filepath.ToSlash(path))
	if err == nil {
		s.repositoryResolver().InvalidateStatus(repositoryContext.Repository.CanonicalRoot)
	}
}

// UploadProjectFile saves a single file under the given target directory.
// POST /api/projects/{projectId}/file-upload?path=<relative_dir>&overwrite=true&session_id=...
// Form field: "file" (the uploaded file)
func (s *Server) UploadProjectFile(w http.ResponseWriter, r *http.Request) {
	if rejectRepositoryDirectoryOperation(w, r) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}

	dirRel := r.URL.Query().Get("path")
	overwrite := r.URL.Query().Get("overwrite") == "true"

	targetDir, err := resolveSafePath(root, dirRel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(targetDir)
	if err != nil || !info.IsDir() {
		writeError(w, http.StatusBadRequest, "target is not a directory")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeError(w, http.StatusBadRequest, "file too large (max 50MB)")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	name := filepath.Base(header.Filename)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	dst := filepath.Join(targetDir, name)
	if !overwrite {
		if _, err := os.Stat(dst); err == nil {
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"error":     "file already exists",
				"conflict":  true,
				"name":      name,
				"directory": dirRel,
			})
			return
		}
	}

	out, err := os.Create(dst)
	if err != nil {
		log.Printf("[api] UploadProjectFile create %s error: %v", dst, err)
		writeError(w, http.StatusInternalServerError, "failed to create file")
		return
	}
	defer out.Close()

	if _, err := io.Copy(out, file); err != nil {
		log.Printf("[api] UploadProjectFile write %s error: %v", dst, err)
		writeError(w, http.StatusInternalServerError, "failed to save file")
		return
	}

	rel, _ := filepath.Rel(root, dst)
	s.invalidateFileGitStatus(r, projectID, rel, root, &requestFile{})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"path": rel,
		"name": name,
		"size": header.Size,
	})
}

// DownloadProjectFile streams a single file with a Content-Disposition: attachment header.
// GET /api/projects/{projectId}/file-download?path=relative/path&session_id=...
func (s *Server) DownloadProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}

	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}
	file, err := openRequestFile(r, root, rel)
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
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(file.fullPath)}))
		_, _ = w.Write([]byte(target))
		return
	}
	if !info.Mode().IsRegular() {
		writeRepositoryOperationUnsupported(w, fmt.Errorf("downloading non-regular files is not supported"))
		return
	}

	name := filepath.Base(file.fullPath)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	opened, err := file.Open()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to open file")
		return
	}
	defer opened.Close()
	http.ServeContent(w, r, name, info.ModTime(), opened)
}

// ServeProjectFile streams a file inline with a Content-Type guessed from its extension.
// Used by the "open HTML in new tab" feature.
// GET /api/projects/{projectId}/file-serve?path=relative/path&session_id=...
func (s *Server) ServeProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}

	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}
	file, err := openRequestFile(r, root, rel)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	defer file.Close()
	if !file.repository {
		serveSandboxedFile(w, r, file.fullPath)
		return
	}
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
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(target))
		return
	}
	if !info.Mode().IsRegular() {
		writeRepositoryOperationUnsupported(w, fmt.Errorf("serving non-regular files is not supported"))
		return
	}
	ct := mime.TypeByExtension(filepath.Ext(file.fullPath))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", sandboxCSP)
	opened, err := file.Open()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to open file")
		return
	}
	defer opened.Close()
	http.ServeContent(w, r, filepath.Base(file.fullPath), info.ModTime(), opened)
}

const sandboxCSP = "sandbox allow-same-origin allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-top-navigation-by-user-activation allow-modals"

// serveSandboxedFile streams the file at `full` inline, with a sandboxed CSP
// header so untrusted HTML can render without breaking out of its iframe-like
// context. allow-scripts lets JS run; allow-top-navigation-by-user-activation
// lets user-clicked <a href> links navigate. Returns 404 if missing, 400 if
// it's a directory.
func serveSandboxedFile(w http.ResponseWriter, r *http.Request, full string) {
	info, err := os.Stat(full)
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

	ct := mime.TypeByExtension(filepath.Ext(full))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", sandboxCSP)
	http.ServeFile(w, r, full)
}

// DeleteProjectFile removes a single file (not a directory).
// DELETE /api/projects/{projectId}/file-content?path=relative/path&session_id=...
func (s *Server) DeleteProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}

	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}
	file, err := openRequestFile(r, root, rel)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	defer file.Close()
	absRoot, _ := filepath.Abs(root)
	if file.fullPath == absRoot {
		writeError(w, http.StatusBadRequest, "cannot delete project root")
		return
	}
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
		writeError(w, http.StatusBadRequest, "directories cannot be deleted via this endpoint")
		return
	}
	if err := file.Remove(); err != nil {
		log.Printf("[api] DeleteProjectFile %s error: %v", file.fullPath, err)
		writeError(w, http.StatusInternalServerError, "failed to delete file")
		return
	}
	s.invalidateFileGitStatus(r, projectID, rel, root, file)
	w.WriteHeader(http.StatusNoContent)
}

// SaveProjectFile writes text content to a file.
// Gated by the global enable_file_edit config flag.
// PUT /api/projects/{projectId}/file-content?path=relative/path&session_id=...
// Body: { "content": "<utf-8 string>" }
func (s *Server) SaveProjectFile(w http.ResponseWriter, r *http.Request) {
	if !s.Config.Get().EnableFileEdit {
		writeError(w, http.StatusForbidden, "file editing is disabled (set enable_file_edit=true in config.json)")
		return
	}

	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}

	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}
	file, err := openRequestFile(r, root, rel)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	defer file.Close()

	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxUploadSize)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if info, statErr := file.Stat(); statErr == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0 && r.URL.Query().Get("repository_id") != "":
			writeError(w, http.StatusUnprocessableEntity, "editing symlinks is not supported")
			return
		case info.IsDir():
			writeError(w, http.StatusBadRequest, "path is a directory")
			return
		case !info.Mode().IsRegular():
			writeRepositoryOperationUnsupported(w, fmt.Errorf("editing non-regular files is not supported"))
			return
		}
	}

	if err := file.MkdirParent(0755); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create parent directory")
		return
	}

	if err := file.WriteFile([]byte(body.Content), 0644); err != nil {
		log.Printf("[api] SaveProjectFile %s error: %v", file.fullPath, err)
		writeError(w, http.StatusInternalServerError, "failed to write file")
		return
	}
	s.invalidateFileGitStatus(r, projectID, rel, root, file)

	info, _ := file.Stat()
	resp := map[string]interface{}{"path": rel}
	if info != nil {
		resp["size"] = info.Size()
	}
	writeJSON(w, http.StatusOK, resp)
}

// MoveProjectFile renames/moves a file or directory inside the project root.
// POST /api/projects/{projectId}/file-move?session_id=...
// Body: { "from": "relative/source", "to": "relative/dest" }
func (s *Server) MoveProjectFile(w http.ResponseWriter, r *http.Request) {
	if rejectRepositoryDirectoryOperation(w, r) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}

	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.From == "" || body.To == "" {
		writeError(w, http.StatusBadRequest, "from and to are required")
		return
	}

	src, err := resolveSafePath(root, body.From)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid source: "+err.Error())
		return
	}
	dst, err := resolveSafePath(root, body.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid destination: "+err.Error())
		return
	}
	absRoot, _ := filepath.Abs(root)
	if src == absRoot || dst == absRoot {
		writeError(w, http.StatusBadRequest, "cannot move project root")
		return
	}
	if src == dst {
		writeJSON(w, http.StatusOK, map[string]interface{}{"from": body.From, "to": body.To})
		return
	}
	// Prevent moving a directory into itself or a descendant.
	if strings.HasPrefix(dst, src+string(filepath.Separator)) {
		writeError(w, http.StatusBadRequest, "cannot move a directory into its own descendant")
		return
	}
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "source not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := os.Stat(dst); err == nil {
		writeError(w, http.StatusConflict, "destination already exists")
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create parent directory")
		return
	}
	if err := os.Rename(src, dst); err != nil {
		log.Printf("[api] MoveProjectFile %s -> %s error: %v", src, dst, err)
		writeError(w, http.StatusInternalServerError, "failed to move: "+err.Error())
		return
	}
	relFrom, _ := filepath.Rel(root, src)
	relTo, _ := filepath.Rel(root, dst)
	s.invalidateFileGitStatus(r, projectID, relFrom, root, &requestFile{})
	if relTo != relFrom {
		s.invalidateFileGitStatus(r, projectID, relTo, root, &requestFile{})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"from": relFrom, "to": relTo})
}
