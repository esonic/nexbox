package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/esonic/nexbox/store"
)

// createWebShareRequest is the POST body for CreateWebShare.
type createWebShareRequest struct {
	Path string `json:"path"`
}

// createWebShareResponse is what the frontend uses to open the new tab.
type createWebShareResponse struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// CreateWebShare upserts a public-share row for the given (project, html file)
// and returns its short id + path. Authenticated; only .html files are
// shareable today since the UI button is gated to HTML previews — the
// extension check here is a backstop against direct API misuse.
// POST /api/projects/{projectId}/web-shares  body: {"path": "foo/bar.html"}
func (s *Server) CreateWebShare(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")

	var req createWebShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	if !strings.EqualFold(filepath.Ext(req.Path), ".html") {
		writeError(w, http.StatusBadRequest, "only .html files can be shared")
		return
	}

	// Resolve against the project's main working directory (no worktree).
	// The shared link must remain valid after any single session is gone.
	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if project.GitRepoPath == "" {
		writeError(w, http.StatusBadRequest, "project has no working directory")
		return
	}
	// Validate via resolveSafePath (rejects traversal, absolute paths) but
	// store the cleaned relative form so future lookups match exactly.
	full, err := resolveSafePath(project.GitRepoPath, req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Fail fast if the file isn't visible from the project's main working
	// directory — e.g. user is in worktree mode and the file only exists in
	// the worktree. Without this, the share row would be created but every
	// access to the link would 404, which is confusing UX.
	info, statErr := os.Stat(full)
	if os.IsNotExist(statErr) {
		writeError(w, http.StatusNotFound, "file not found in project working directory")
		return
	}
	if statErr != nil {
		log.Printf("[api] CreateWebShare stat error: %v", statErr)
		writeError(w, http.StatusInternalServerError, "failed to check file")
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory")
		return
	}
	rel := filepath.Clean(req.Path)

	id, err := s.Store.CreateOrGetWebShare(projectID, rel)
	if err != nil {
		writeStoreError(w, err, "CreateOrGetWebShare", "failed to create share")
		return
	}
	writeJSON(w, http.StatusOK, createWebShareResponse{
		ID:  id,
		URL: "/web_share/" + id,
	})
}

// ServeWebShare is the public (no-auth) endpoint for shared HTML files.
// 404s when the share id is unknown or the underlying file has been removed.
// GET /web_share/{id}
func (s *Server) ServeWebShare(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	ws, err := s.Store.GetWebShare(id)
	if err != nil {
		if errors.Is(err, store.ErrWebShareNotFound) {
			http.NotFound(w, r)
			return
		}
		log.Printf("[api] ServeWebShare GetWebShare(%s): %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to load share")
		return
	}
	project, err := s.getProjectWithLocalPath(ws.ProjectID)
	if err != nil {
		log.Printf("[api] ServeWebShare getProject(%s): %v", ws.ProjectID, err)
		http.NotFound(w, r)
		return
	}
	if project == nil || project.GitRepoPath == "" {
		http.NotFound(w, r)
		return
	}
	full, err := resolveSafePath(project.GitRepoPath, ws.Path)
	if err != nil {
		log.Printf("[api] ServeWebShare resolveSafePath(share=%s, path=%q): %v", id, ws.Path, err)
		http.NotFound(w, r)
		return
	}

	// HTML shares are rewritten on the fly to carry a correct set of Open Graph
	// tags so the link renders as a rich card in WeChat/Lark/QQ. Non-HTML (or a
	// read failure) falls back to the plain sandboxed file server.
	if strings.EqualFold(filepath.Ext(full), ".html") {
		if content, readErr := os.ReadFile(full); readErr == nil {
			shareURL := publicBaseURL(r) + "/web_share/" + id
			defaultImage := publicBaseURL(r) + "/icons/icon-512.png"
			out := injectOpenGraph(string(content), shareURL, defaultImage)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Security-Policy", "sandbox allow-same-origin allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-top-navigation-by-user-activation allow-modals")
			_, _ = w.Write([]byte(out))
			return
		}
	}
	serveSandboxedFile(w, r, full)
}
