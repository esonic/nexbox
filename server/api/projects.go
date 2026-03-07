package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"gitlab.okg.com/sre_ai/nexbox/config"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

// injectProjectOKOne injects the okone config from config.json into the project (transient field).
func (s *Server) injectProjectOKOne(p *store.Project) {
	if p == nil {
		return
	}
	for _, m := range s.Config.Get().Projects {
		if m.RemoteID == p.ID {
			p.OKOne = m.OKOne
			return
		}
	}
}

func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.Store.ListProjects()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if projects == nil {
		projects = []Project{}
	}

	// Patch local git paths from config (cloud mode only)
	if s.StoreMode == "remote" {
		pathMap := make(map[string]string)
		for _, m := range s.Config.Get().Projects {
			pathMap[m.RemoteID] = m.GitRepoPath
		}
		for i := range projects {
			if lp, ok := pathMap[projects[i].ID]; ok {
				projects[i].GitRepoPath = lp
			}
		}
	}

	for i := range projects {
		s.injectProjectOKOne(&projects[i])
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

	localPath := req.GitRepoPath
	req.GitRepoPath = "" // local path is never sent to the remote

	if s.StoreMode == "remote" && localPath != "" {
		// Best-effort: try to extract GitLab URL from the local repo
		if gitlabURL, err := extractGitLabURL(localPath); err == nil {
			req.GitLabRepoURL = gitlabURL
		}
	}

	project, err := s.Store.CreateProject(req)
	if err != nil {
		if ae, ok := err.(*store.ApiError); ok && ae.StatusCode == http.StatusConflict {
			writeError(w, http.StatusConflict, "project name already exists")
			return
		}
		if errors.Is(err, store.ErrVersionConflict) {
			writeError(w, http.StatusConflict, "project name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Save local git path mapping when in cloud mode
	if s.StoreMode == "remote" && localPath != "" {
		if err := s.Config.Update(func(cfg *config.Config) {
			for _, m := range cfg.Projects {
				if m.RemoteID == project.ID && m.GitRepoPath == localPath {
					return // already mapped
				}
			}
			cfg.Projects = append(cfg.Projects, config.ProjectMapping{
				RemoteID:    project.ID,
				GitRepoPath: localPath,
			})
		}); err != nil {
			log.Printf("[projects] failed to save local config mapping: %v", err)
		}
		project.GitRepoPath = localPath
	}

	// Copy global skills into the new project's .claude/skills/ directory
	if project.GitRepoPath != "" && s.LocalStore != nil {
		s.copyGlobalSkillsToProject(project.GitRepoPath)
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

	s.injectProjectOKOne(project)
	writeJSON(w, http.StatusOK, project)
}

func (s *Server) UpdateProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "projectId")
	var req UpdateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// In cloud mode, git_repo_path is stored in local config (not sent to remote).
	// This allows "Add to Sidebar" (non-empty path) and "Remove from Sidebar" (empty path).
	localPathOnly := false
	if s.StoreMode == "remote" && req.GitRepoPath != nil {
		localPath := *req.GitRepoPath
		if localPath != "" {
			if err := validateGitRepoPath(localPath); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		_ = s.Config.Update(func(cfg *config.Config) {
			filtered := cfg.Projects[:0]
			for _, m := range cfg.Projects {
				if m.RemoteID != id {
					filtered = append(filtered, m)
				}
			}
			cfg.Projects = filtered
			if localPath != "" {
				cfg.Projects = append(cfg.Projects, config.ProjectMapping{
					RemoteID:    id,
					GitRepoPath: localPath,
				})
			}
		})
		req.GitRepoPath = nil // do not forward local path to remote
		// If git_repo_path was the only field, skip the remote API call entirely
		localPathOnly = req.Name == nil && req.Description == nil && req.GitLabRepoURL == nil && req.ExpectedVersion == nil
	} else if req.GitRepoPath != nil {
		if err := validateGitRepoPath(*req.GitRepoPath); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// If only git_repo_path was provided and it was intercepted, fetch and return current project
	if localPathOnly {
		project, err := s.getProjectWithLocalPath(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if project == nil {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		s.injectProjectOKOne(project)
		writeJSON(w, http.StatusOK, project)
		return
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

	// Patch local path back into the response (cloud mode)
	if s.StoreMode == "remote" {
		for _, m := range s.Config.Get().Projects {
			if m.RemoteID == project.ID {
				project.GitRepoPath = m.GitRepoPath
				break
			}
		}
	}

	s.injectProjectOKOne(project)
	writeJSON(w, http.StatusOK, project)
}

func (s *Server) DeleteProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "projectId")

	// Always remove local git path mapping when present
	_ = s.Config.Update(func(cfg *config.Config) {
		filtered := cfg.Projects[:0]
		for _, m := range cfg.Projects {
			if m.RemoteID != id {
				filtered = append(filtered, m)
			}
		}
		cfg.Projects = filtered
	})

	if err := s.Store.DeleteProject(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if ae, ok := err.(*store.ApiError); ok && ae.StatusCode == http.StatusNotFound {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
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

	// In cloud mode, use the dedicated remote query that searches all projects
	// regardless of the caller's membership (Java supports ?gitlabUrl= for this).
	if s.StoreMode == "remote" && s.Cloud != nil {
		p, err := s.Cloud.FindProjectByGitLabURL(gitlabURL)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if p == nil {
			writeError(w, http.StatusNotFound, "no matching project found")
			return
		}
		// Patch local path if already mapped
		for _, m := range s.Config.Get().Projects {
			if m.RemoteID == p.ID {
				p.GitRepoPath = m.GitRepoPath
				break
			}
		}
		writeJSON(w, http.StatusOK, *p)
		return
	}

	// Local mode: list all projects and match by GitLabRepoURL
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

func (s *Server) ListProjectMembers(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeJSON(w, http.StatusOK, []store.ProjectMember{})
		return
	}
	id := chi.URLParam(r, "projectId")
	members, err := s.Cloud.ListProjectMembers(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if members == nil {
		members = []store.ProjectMember{}
	}
	writeJSON(w, http.StatusOK, members)
}

func (s *Server) AddProjectMember(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeError(w, http.StatusBadRequest, "project members require cloud mode")
		return
	}
	id := chi.URLParam(r, "projectId")
	var body struct {
		UserName string `json:"user_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.UserName == "" {
		writeError(w, http.StatusBadRequest, "user_name is required")
		return
	}
	if err := s.Cloud.AddProjectMember(id, body.UserName); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) RemoveProjectMember(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeError(w, http.StatusBadRequest, "project members require cloud mode")
		return
	}
	id := chi.URLParam(r, "projectId")
	userName := chi.URLParam(r, "userName")
	if err := s.Cloud.RemoveProjectMember(id, userName); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// LeaveProject removes the current user from a project's member list (non-owner only)
// and also removes the local config mapping if present.
func (s *Server) LeaveProject(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeError(w, http.StatusBadRequest, "leaving a project requires cloud mode")
		return
	}
	id := chi.URLParam(r, "projectId")
	if err := s.Cloud.LeaveProject(id); err != nil {
		if ae, ok := err.(*store.ApiError); ok {
			writeError(w, ae.StatusCode, ae.Message)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Remove local config mapping if present
	_ = s.Config.Update(func(cfg *config.Config) {
		filtered := cfg.Projects[:0]
		for _, m := range cfg.Projects {
			if m.RemoteID != id {
				filtered = append(filtered, m)
			}
		}
		cfg.Projects = filtered
	})
	w.WriteHeader(http.StatusNoContent)
}
