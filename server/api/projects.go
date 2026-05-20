package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/esonic/nexbox/store"
)

// projectNamePattern enforces a filesystem-friendly slug so we can mount the
// project name directly under data-dir/users/<user>/<name>/ without quoting.
var projectNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validProjectName(name string) error {
	if !projectNamePattern.MatchString(name) {
		return errors.New("project name must be 1-64 chars from [A-Za-z0-9_-]")
	}
	return nil
}

// projectWorkDir returns the canonical filesystem path for a project under the
// given user. The directory may or may not exist yet.
func (s *Server) projectWorkDir(username, projectName string) string {
	return filepath.Join(s.DataDir, "users", username, projectName)
}

// markGitRepoStatus stamps IsGitRepo on a project by checking for .git in its
// working dir. Cheap stat, suitable to call on every project we return.
func markGitRepoStatus(p *store.Project) {
	if p == nil || p.GitRepoPath == "" {
		return
	}
	if info, err := os.Stat(filepath.Join(p.GitRepoPath, ".git")); err == nil && (info.IsDir() || info.Mode().IsRegular()) {
		p.IsGitRepo = true
	}
}

// ListProjects returns the projects owned by the authenticated user.
func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request) {
	a := s.requireAuthUser(w, r)
	if a == nil {
		return
	}
	projects, err := s.Store.ListProjectsByOwner(a.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if projects == nil {
		projects = []Project{}
	}
	for i := range projects {
		markGitRepoStatus(&projects[i])
	}
	writeJSON(w, http.StatusOK, projects)
}

// createProjectBody is the JSON shape accepted by POST /projects. Distinct
// from store.CreateProjectRequest so that AutoCreateDir can be a pointer (to
// detect "field omitted" → default true) without leaking into the storage layer.
type createProjectBody struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	AutoCreateDir  *bool  `json:"auto_create_dir,omitempty"`
	GitRepoPath    string `json:"git_repo_path,omitempty"`
}

// CreateProject creates a new project owned by the authenticated user. By
// default the working directory is derived and created under
// data-dir/users/<user>/<name>/. When auto_create_dir is explicitly false,
// the caller must supply an existing absolute git_repo_path, and the server
// will adopt it without modification (and never rename/delete it later).
func (s *Server) CreateProject(w http.ResponseWriter, r *http.Request) {
	a := s.requireAuthUser(w, r)
	if a == nil {
		return
	}
	var req createProjectBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validProjectName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Default behaviour: auto-create when the flag is omitted.
	autoCreate := true
	if req.AutoCreateDir != nil {
		autoCreate = *req.AutoCreateDir
	}

	var workDir string
	var createdDir bool
	if autoCreate {
		workDir = s.projectWorkDir(a.Username, req.Name)
		if err := os.MkdirAll(workDir, 0755); err != nil {
			writeError(w, http.StatusInternalServerError, "create project dir: "+err.Error())
			return
		}
		createdDir = true
	} else {
		workDir = strings.TrimSpace(req.GitRepoPath)
		if workDir == "" {
			writeError(w, http.StatusBadRequest, "git_repo_path is required when auto_create_dir is false")
			return
		}
		if !filepath.IsAbs(workDir) {
			writeError(w, http.StatusBadRequest, "git_repo_path must be an absolute path")
			return
		}
		workDir = filepath.Clean(workDir)
		info, err := os.Stat(workDir)
		if err != nil {
			if os.IsNotExist(err) {
				writeError(w, http.StatusBadRequest, "git_repo_path does not exist: "+workDir)
				return
			}
			writeError(w, http.StatusBadRequest, "git_repo_path stat: "+err.Error())
			return
		}
		if !info.IsDir() {
			writeError(w, http.StatusBadRequest, "git_repo_path is not a directory")
			return
		}
	}

	// Per-user uniqueness check on name. Done after path validation so the
	// user sees the more actionable error first.
	if existing, err := s.Store.GetProjectByNameForOwner(a.UserID, req.Name); err == nil && existing != nil {
		if createdDir {
			_ = os.RemoveAll(workDir)
		}
		writeError(w, http.StatusConflict, "you already have a project named "+req.Name)
		return
	}

	storeReq := store.CreateProjectRequest{
		OwnerUserID:    a.UserID,
		CreatedBy:      a.Username,
		Name:           req.Name,
		Description:    req.Description,
		GitRepoPath:    workDir,
		AutoManagedDir: autoCreate,
	}
	project, err := s.Store.CreateProject(storeReq)
	if err != nil {
		if createdDir {
			// Roll back the directory we just created so subsequent retries succeed.
			_ = os.RemoveAll(workDir)
		}
		if errors.Is(err, store.ErrVersionConflict) {
			writeError(w, http.StatusConflict, "you already have a project named "+req.Name)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Copy global skills and plugins into the new project's .claude/ dir.
	s.copyGlobalSkillsToProject(project.GitRepoPath)
	s.copyGlobalPluginsToProject(project.GitRepoPath)

	writeJSON(w, http.StatusCreated, project)
}

// GetProject returns a single project, only if the caller owns it.
func (s *Server) GetProject(w http.ResponseWriter, r *http.Request) {
	a := s.requireAuthUser(w, r)
	if a == nil {
		return
	}
	id := chi.URLParam(r, "projectId")
	project, err := s.Store.GetProject(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if project == nil || project.OwnerUserID != a.UserID {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	markGitRepoStatus(project)
	writeJSON(w, http.StatusOK, project)
}

// GetOrCreateScratchSession returns the project's hidden "scratch" session,
// creating it on first request.
func (s *Server) GetOrCreateScratchSession(w http.ResponseWriter, r *http.Request) {
	a := s.requireAuthUser(w, r)
	if a == nil {
		return
	}
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
	if project == nil || project.OwnerUserID != a.UserID {
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

// UpdateProject lets a user rename or re-describe a project they own. The
// git_repo_path is server-managed and cannot be set from the request body — a
// rename triggers an os.Rename of the working directory.
func (s *Server) UpdateProject(w http.ResponseWriter, r *http.Request) {
	a := s.requireAuthUser(w, r)
	if a == nil {
		return
	}
	id := chi.URLParam(r, "projectId")
	current, err := s.Store.GetProject(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if current == nil || current.OwnerUserID != a.UserID {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	var req UpdateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Working directory is never accepted from the request body. For
	// auto-managed projects we re-derive it from the new name; for
	// user-supplied paths we leave it alone.
	req.GitRepoPath = nil

	if req.Name != nil && *req.Name != current.Name {
		if err := validProjectName(*req.Name); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if conflict, _ := s.Store.GetProjectByNameForOwner(a.UserID, *req.Name); conflict != nil && conflict.ID != id {
			writeError(w, http.StatusConflict, "you already have a project named "+*req.Name)
			return
		}
		if current.AutoManagedDir {
			newPath := s.projectWorkDir(a.Username, *req.Name)
			if current.GitRepoPath != "" {
				if err := os.Rename(current.GitRepoPath, newPath); err != nil {
					writeError(w, http.StatusInternalServerError, "rename project dir: "+err.Error())
					return
				}
			} else {
				if err := os.MkdirAll(newPath, 0755); err != nil {
					writeError(w, http.StatusInternalServerError, "create project dir: "+err.Error())
					return
				}
			}
			req.GitRepoPath = &newPath
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

// DeleteProject removes the project, its working directory, and all child
// rows (sessions, tasks, etc.).
func (s *Server) DeleteProject(w http.ResponseWriter, r *http.Request) {
	a := s.requireAuthUser(w, r)
	if a == nil {
		return
	}
	id := chi.URLParam(r, "projectId")
	current, err := s.Store.GetProject(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if current == nil || current.OwnerUserID != a.UserID {
		writeError(w, http.StatusNotFound, "project not found")
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
	// Best effort: wipe the working directory only when the server originally
	// created it. User-supplied paths are left untouched on delete.
	if current.AutoManagedDir && current.GitRepoPath != "" {
		if err := os.RemoveAll(current.GitRepoPath); err != nil {
			log.Printf("[projects] remove dir %s: %v", current.GitRepoPath, err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// LookupProjectByDir looks up by working directory path. Filtered to the
// caller's owned projects.
func (s *Server) LookupProjectByDir(w http.ResponseWriter, r *http.Request) {
	a := s.requireAuthUser(w, r)
	if a == nil {
		return
	}
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		writeError(w, http.StatusBadRequest, "dir query param is required")
		return
	}
	projects, err := s.Store.ListProjectsByOwner(a.UserID)
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

// CheckProjectByGitDir is retained for API compatibility but local-only mode
// has no remote registry to look up; we always return 404.
func (s *Server) CheckProjectByGitDir(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "no matching project found")
}

// ensureDefaultProject creates a "Default" project for the user when they have
// none. Called on each successful login. If the user has at least one project
// (even renamed/recreated) we do not seed.
func (s *Server) ensureDefaultProject(u *store.User) error {
	existing, err := s.Store.ListProjectsByOwner(u.ID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	workDir := s.projectWorkDir(u.Username, "Default")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return err
	}
	_, err = s.Store.CreateProject(store.CreateProjectRequest{
		OwnerUserID:    u.ID,
		CreatedBy:      u.Username,
		Name:           "Default",
		GitRepoPath:    workDir,
		AutoManagedDir: true,
	})
	return err
}
