package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"gitlab.okg.com/sre_ai/boxnex/store"
)

func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.Store.ListProjects()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if projects == nil {
		projects = []Project{}
	}

	// Filter by owner unless admin
	if GetUserRole(r) != "admin" {
		userID := GetUserID(r)
		filtered := projects[:0]
		for _, p := range projects {
			if p.OwnerID == userID {
				filtered = append(filtered, p)
			}
		}
		projects = filtered
	}

	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) CreateProject(w http.ResponseWriter, r *http.Request) {
	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if len([]rune(req.Name)) > 20 {
		writeError(w, http.StatusBadRequest, "name must be less than 20 characters")
		return
	}
	if len([]rune(req.Description)) > 100 {
		writeError(w, http.StatusBadRequest, "description must be less than 100 characters")
		return
	}

	// Set owner from authenticated user
	req.OwnerID = GetUserID(r)

	// If no git_repo_path provided, auto-create workspace directory
	if req.GitRepoPath == "" && s.DataDir != "" {
		// Will be set after project creation (need project ID for directory name)
	} else if req.GitRepoPath != "" {
		if err := validateGitRepoPath(req.GitRepoPath); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	project, err := s.Store.CreateProject(req)
	if err != nil {
		if errors.Is(err, store.ErrVersionConflict) {
			writeError(w, http.StatusConflict, "project name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Auto-create workspace directory if none was provided
	if req.GitRepoPath == "" && s.DataDir != "" {
		shortID := project.ID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		wsDir := filepath.Join(s.DataDir, "workspaces", shortID)
		if err := os.MkdirAll(wsDir, 0755); err != nil {
			log.Printf("[projects] failed to create workspace dir %s: %v", wsDir, err)
		} else {
			// Update project with the workspace path
			gitPath := wsDir
			if _, err := s.Store.UpdateProject(project.ID, store.UpdateProjectRequest{
				GitRepoPath: &gitPath,
			}); err != nil {
				log.Printf("[projects] failed to update project git_repo_path: %v", err)
			}
			project.GitRepoPath = wsDir
		}
	}

	writeJSON(w, http.StatusCreated, project)
}

func (s *Server) GetProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "projectId")
	project, err := s.getProjectWithLocalPath(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if project == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if GetUserRole(r) != "admin" && project.OwnerID != GetUserID(r) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	writeJSON(w, http.StatusOK, project)
}

func (s *Server) UpdateProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "projectId")
	var req UpdateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name != nil && len([]rune(*req.Name)) > 20 {
		writeError(w, http.StatusBadRequest, "name must be less than 20 characters")
		return
	}
	if req.Description != nil && len([]rune(*req.Description)) > 100 {
		writeError(w, http.StatusBadRequest, "description must be less than 100 characters")
		return
	}

	if req.GitRepoPath != nil {
		if err := validateGitRepoPath(*req.GitRepoPath); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Ownership check before update
	if GetUserRole(r) != "admin" {
		existing, err := s.Store.GetProject(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if existing == nil {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if existing.OwnerID != GetUserID(r) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
	}

	project, err := s.Store.UpdateProject(id, req)
	if err != nil {
		if errors.Is(err, store.ErrVersionConflict) {
			writeError(w, http.StatusConflict, "version conflict: project was modified by another client")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if project == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	writeJSON(w, http.StatusOK, project)
}

func (s *Server) DeleteProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "projectId")

	// Fetch project before deletion so we can clean up its workspace directory.
	existing, err := s.Store.GetProject(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Ownership check before delete
	if GetUserRole(r) != "admin" && existing.OwnerID != GetUserID(r) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	if err := s.Store.DeleteProject(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Stop the per-project Docker container if container isolation is enabled.
	if s.ContainerMgr != nil {
		if err := s.ContainerMgr.StopContainer(id); err != nil {
			log.Printf("[projects] failed to stop container for project %s: %v", id, err)
		}
	}

	// T3.4 + T9.4: Clean up the workspace directory on the host.
	// Only remove directories under DataDir/workspaces/ (auto-created workspaces).
	if s.DataDir != "" && existing.GitRepoPath != "" {
		wsRoot := filepath.Join(s.DataDir, "workspaces")
		if isSubPath(wsRoot, existing.GitRepoPath) {
			if err := os.RemoveAll(existing.GitRepoPath); err != nil {
				log.Printf("[projects] failed to remove workspace dir %s: %v", existing.GitRepoPath, err)
			} else {
				log.Printf("[projects] removed workspace dir %s for project %s", existing.GitRepoPath, id)
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── Sidebar / local dir lookup ──

// LookupProjectByDir checks whether a local directory path is already mapped to a
// project in the config (Working Projects).  Returns the project mapping if found,
// or 404 if not.
func (s *Server) LookupProjectByDir(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		writeError(w, http.StatusBadRequest, "dir query param is required")
		return
	}
	for _, m := range s.Config.Get().Projects {
		if m.GitRepoPath == dir {
			writeJSON(w, http.StatusOK, m)
			return
		}
	}
	writeError(w, http.StatusNotFound, "no project mapped to this directory")
}

// CheckProjectByGitDir extracts the GitLab remote URL from the given local directory
// and checks whether any project already uses that URL.
// In cloud mode it queries the remote service directly (which searches all projects,
// not just those the caller is already a member of).
// Returns the matching Project (200) or 404 if none found / dir has no git remote.
func (s *Server) CheckProjectByGitDir(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	gitlabURL, err := extractGitLabURL(path)
	if err != nil || gitlabURL == "" {
		writeError(w, http.StatusNotFound, "no gitlab remote found")
		return
	}

	// List all projects and match by GitLabRepoURL
	projects, err := s.Store.ListProjects()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, p := range projects {
		if p.GitLabRepoURL == gitlabURL {
			writeJSON(w, http.StatusOK, p)
			return
		}
	}

	writeError(w, http.StatusNotFound, "no matching project found")
}

// ── Project Members (cloud mode only) ──

