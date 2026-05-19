package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/esonic/nexbox/store"
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

	if err := validateGitRepoPath(req.GitRepoPath); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
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

	// Copy global skills and plugins into the new project's .claude/ directory
	if project.GitRepoPath != "" {
		s.copyGlobalSkillsToProject(project.GitRepoPath)
		s.copyGlobalPluginsToProject(project.GitRepoPath)
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
	writeJSON(w, http.StatusOK, project)
}

// GetOrCreateScratchSession returns the project's hidden "scratch" session,
// creating it on first request. Used by the sidebar when the user clicks a
// project row to open a new-chat surface without committing to a real session;
// the session gets promoted (is_hidden -> 0) as soon as a user message is
// persisted to it (see chat.go).
func (s *Server) GetOrCreateScratchSession(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "projectId required")
		return
	}
	project, err := s.Store.GetProject(projectID)
	if err != nil {
		log.Printf("[scratch-session] GetProject(%s) failed: %v", projectID, err)
		writeError(w, http.StatusInternalServerError, "failed to load project")
		return
	}
	if project == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	sess, err := s.Store.GetOrCreateScratchSession(projectID)
	if writeStoreError(w, err, "GetOrCreateScratchSession", "failed to get or create scratch session") {
		return
	}
	if sess.Agent == "" {
		sess.Agent = s.getDefaultAgent()
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) UpdateProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "projectId")
	var req UpdateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.GitRepoPath != nil {
		if err := validateGitRepoPath(*req.GitRepoPath); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
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
	if err := s.Store.DeleteProject(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// LookupProjectByDir checks whether a local directory path is bound to any
// project. Returns the project if found, or 404 if not.
func (s *Server) LookupProjectByDir(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		writeError(w, http.StatusBadRequest, "dir query param is required")
		return
	}
	projects, err := s.Store.ListProjects()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range projects {
		if projects[i].GitRepoPath == dir {
			writeJSON(w, http.StatusOK, projects[i])
			return
		}
	}
	writeError(w, http.StatusNotFound, "no project mapped to this directory")
}

// CheckProjectByGitDir is retained for API compatibility but in local-only mode
// there's no remote registry of GitLab URLs to look up; we always return 404.
func (s *Server) CheckProjectByGitDir(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "no matching project found")
}
