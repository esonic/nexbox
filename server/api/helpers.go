package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gitlab.okg.com/sre_ai/boxnex/config"
	"gitlab.okg.com/sre_ai/boxnex/store"
)

// getDefaultAgent reads the persisted default agent from config, falling back to in-memory value.
func (s *Server) getDefaultAgent() string {
	if val := s.Config.Get().DefaultAgent; val != "" {
		return val
	}
	return s.AgentName
}

// GetServerInfo returns basic server configuration info.
// GET /api/info → {"agent": "claude", "mode": "local"|"needs_login"}
func (s *Server) GetServerInfo(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config.Get()
	mode := "local"
	if cfg.JWTSecret != "" {
		mode = "needs_login"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent":   s.getDefaultAgent(),
		"mode":    mode,
		"version": s.Version,
	})
}

// ListAvailableAgents returns all registered agent backends.
// GET /api/available-agents → {"agents": ["claude","codex"], "default": "claude"}
func (s *Server) ListAvailableAgents(w http.ResponseWriter, r *http.Request) {
	names := make([]string, 0, len(s.Agents))
	for name := range s.Agents {
		names = append(names, name)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"agents":  names,
		"default": s.getDefaultAgent(),
	})
}

// SetDefaultAgent changes the runtime default agent.
// PUT /api/default-agent  { "agent": "claude" }
func (s *Server) SetDefaultAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agent string `json:"agent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	a, ok := s.Agents[req.Agent]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown agent: %q", req.Agent))
		return
	}

	s.Agent = a
	s.AgentName = req.Agent

	if err := s.Config.Update(func(cfg *config.Config) {
		cfg.DefaultAgent = req.Agent
	}); err != nil {
		log.Printf("[api] SetDefaultAgent persist error: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to persist setting")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"default": s.AgentName,
	})
}

// joinedProjectIDs returns nil; filtering is no longer done client-side.
// The cloud backend already returns only changes for projects the user belongs to.
func (s *Server) joinedProjectIDs() map[string]bool {
	return nil
}

// isVisibleChange checks if a change record belongs to a joined project
// using the stored project_id field. No entity lookups needed.
func isVisibleChange(c store.Change, joined map[string]bool) bool {
	if c.ProjectID == "" {
		return true
	}
	return joined[c.ProjectID]
}

// getProjectWithLocalPath fetches a project by ID.
func (s *Server) getProjectWithLocalPath(id string) (*store.Project, error) {
	return s.Store.GetProject(id)
}

// isSubPath reports whether child is a sub-path of parent.
func isSubPath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	if status >= 500 {
		log.Printf("[api] %d error: %s", status, message)
	}
	writeJSON(w, status, map[string]string{"error": message})
}

// writeStoreError logs the original error and writes a generic error response.
// Returns true when an error response has been written.
func writeStoreError(w http.ResponseWriter, err error, op, fallback string) bool {
	if err == nil {
		return false
	}
	log.Printf("[api] %s error: %v", op, err)
	writeError(w, http.StatusInternalServerError, fallback)
	return true
}

// validateGitRepoPath checks that the given path is a valid, existing directory.
// Returns nil if valid, or an error describing the problem.
func validateGitRepoPath(path string) error {
	if path == "" {
		return nil // empty is allowed (optional field)
	}

	// Must be an absolute path
	if !filepath.IsAbs(path) {
		return fmt.Errorf("git_repo_path must be an absolute path, got: %s", path)
	}

	// Reject obvious non-path strings (e.g. commands pasted by mistake)
	if strings.ContainsAny(path, "|;&$`\"'\\") {
		return fmt.Errorf("git_repo_path contains invalid characters: %s", path)
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("directory does not exist: %s", path)
		}
		return fmt.Errorf("cannot access path: %s (%v)", path, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("path is not a directory: %s", path)
	}

	return nil
}

// BrowseDir lists directories under a given path for the directory picker.
// GET /api/browse-dir?path=/some/path
func (s *Server) BrowseDir(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = home
	}

	// Clean and resolve the path
	dir = filepath.Clean(dir)

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		writeError(w, http.StatusBadRequest, "not a valid directory")
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot read directory")
		return
	}

	type DirEntry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	var dirs []DirEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip hidden directories
		if strings.HasPrefix(name, ".") {
			continue
		}
		dirs = append(dirs, DirEntry{Name: name, Path: filepath.Join(dir, name)})
	}

	parent := filepath.Dir(dir)
	if parent == dir {
		parent = "" // at root
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"current": dir,
		"parent":  parent,
		"dirs":    dirs,
	})
}

// ValidatePath is an API handler that validates a filesystem path.
// POST /api/validate-path  { "path": "/some/path" }
func (s *Server) ValidatePath(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"valid": false, "error": "invalid request"})
		return
	}

	if err := validateGitRepoPath(req.Path); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"valid": false, "error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"valid": true})
}

// ListChanges returns recent change records from the store.
// GET /api/changes?limit=20&since=0&order=asc
func (s *Server) ListChanges(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	var sinceID int64
	if v := r.URL.Query().Get("since"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			sinceID = n
		}
	}
	order := r.URL.Query().Get("order")
	if order != "desc" {
		order = "asc"
	}

	changes, err := s.Store.ListChanges(sinceID, limit, order)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if changes == nil {
		changes = []store.Change{}
	}

	if joined := s.joinedProjectIDs(); joined != nil {
		filtered := changes[:0]
		for _, c := range changes {
			if isVisibleChange(c, joined) {
				filtered = append(filtered, c)
			}
		}
		changes = filtered
	}

	writeJSON(w, http.StatusOK, changes)
}

// GetSettings returns all settings.
// GET /api/settings
func (s *Server) GetSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config.Get()
	maskedToken := ""
	if cfg.Token != "" {
		if len(cfg.Token) > 10 {
			maskedToken = cfg.Token[:6] + "..." + cfg.Token[len(cfg.Token)-4:]
		} else {
			maskedToken = "***"
		}
	}
	maskedGitLabToken := ""
	if cfg.GitLabToken != "" {
		if len(cfg.GitLabToken) > 10 {
			maskedGitLabToken = cfg.GitLabToken[:6] + "..." + cfg.GitLabToken[len(cfg.GitLabToken)-4:]
		} else {
			maskedGitLabToken = "***"
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"default_agent":       s.getDefaultAgent(),
		"has_token":           cfg.Token != "",
		"masked_token":        maskedToken,
		"has_gitlab_token":    cfg.GitLabToken != "",
		"masked_gitlab_token": maskedGitLabToken,
		"disable_cloud":       cfg.DisableCloud,
	})
}

// UpdateSettings updates settings.
// PUT /api/settings
func (s *Server) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DefaultAgent *string `json:"default_agent,omitempty"`
		Token        *string `json:"token,omitempty"`
		GitLabToken  *string `json:"gitlab_token,omitempty"`
		DisableCloud *bool   `json:"disable_cloud,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.DefaultAgent != nil {
		if a, ok := s.Agents[*req.DefaultAgent]; ok {
			s.Agent = a
			s.AgentName = *req.DefaultAgent
		} else {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown agent: %q", *req.DefaultAgent))
			return
		}
	}

	if err := s.Config.Update(func(cfg *config.Config) {
		if req.DefaultAgent != nil {
			cfg.DefaultAgent = *req.DefaultAgent
		}
		if req.Token != nil {
			cfg.Token = *req.Token
		}
		if req.GitLabToken != nil {
			cfg.GitLabToken = *req.GitLabToken
		}
		if req.DisableCloud != nil {
			cfg.DisableCloud = *req.DisableCloud
		}
	}); err != nil {
		log.Printf("[api] UpdateSettings persist error: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to persist settings")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
