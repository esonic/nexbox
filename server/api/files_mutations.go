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

// UploadProjectFile saves a single file under the given target directory.
// POST /api/projects/{projectId}/file-upload?path=<relative_dir>&overwrite=true&session_id=...
// Form field: "file" (the uploaded file)
func (s *Server) UploadProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}
	full, err := resolveSafePath(root, rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
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

	name := filepath.Base(full)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	http.ServeFile(w, r, full)
}

// ServeProjectFile streams a file inline with a Content-Type guessed from its extension.
// Used by the "open HTML in new tab" feature. The CSP sandbox allows scripts and
// user-initiated top-level navigation so shared HTML behaves like a normal page;
// acceptable because the platform is closed to a small trusted user set.
// GET /api/projects/{projectId}/file-serve?path=relative/path&session_id=...
func (s *Server) ServeProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}
	full, err := resolveSafePath(root, rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
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
	w.Header().Set("Content-Security-Policy", "sandbox allow-same-origin allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-top-navigation-by-user-activation allow-modals")
	http.ServeFile(w, r, full)
}

// DeleteProjectFile removes a single file (not a directory).
// DELETE /api/projects/{projectId}/file-content?path=relative/path&session_id=...
func (s *Server) DeleteProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}
	full, err := resolveSafePath(root, rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	absRoot, _ := filepath.Abs(root)
	if full == absRoot {
		writeError(w, http.StatusBadRequest, "cannot delete project root")
		return
	}
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
		writeError(w, http.StatusBadRequest, "directories cannot be deleted via this endpoint")
		return
	}
	if err := os.Remove(full); err != nil {
		log.Printf("[api] DeleteProjectFile %s error: %v", full, err)
		writeError(w, http.StatusInternalServerError, "failed to delete file")
		return
	}
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
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}
	full, err := resolveSafePath(root, rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxUploadSize)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if info, err := os.Stat(full); err == nil && info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory")
		return
	}

	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create parent directory")
		return
	}

	if err := os.WriteFile(full, []byte(body.Content), 0644); err != nil {
		log.Printf("[api] SaveProjectFile %s error: %v", full, err)
		writeError(w, http.StatusInternalServerError, "failed to write file")
		return
	}

	info, _ := os.Stat(full)
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
	projectID := chi.URLParam(r, "projectId")
	root, err := s.resolveFilesRoot(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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
	writeJSON(w, http.StatusOK, map[string]interface{}{"from": relFrom, "to": relTo})
}
