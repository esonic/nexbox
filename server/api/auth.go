package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gitlab.okg.com/sre_ai/nexbox/config"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

// GetAuthLoginURL returns the patrol login URL for the frontend to open in a popup.
// GET /api/auth/login-url → {"url": "https://..."}
func (s *Server) GetAuthLoginURL(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config.Get()
	if cfg.DisableCloud {
		writeError(w, http.StatusForbidden, "cloud disabled")
		return
	}

	patrolBase, err := extractURLBase(cfg.ServerURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid server URL in config")
		return
	}

	// Random CSRF state
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	state := hex.EncodeToString(b)

	// Callback URL points back to nexbox itself
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	callbackURL := fmt.Sprintf("%s://%s/api/auth/callback", scheme, r.Host)

	loginURL := fmt.Sprintf("%s/login/nexbox?state=%s&redirect_uri=%s",
		patrolBase,
		url.QueryEscape(state),
		url.QueryEscape(callbackURL))

	writeJSON(w, http.StatusOK, map[string]string{"url": loginURL})
}

// HandleAuthCallback receives the OAuth callback from patrol with the access token.
// GET /api/auth/callback?token=<token>&state=<state>
func (s *Server) HandleAuthCallback(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	// Validate by pinging the remote store
	cfg := s.Config.Get()
	remoteStore := store.NewRemoteStore(cfg.ServerURL, token)
	if err := remoteStore.Ping(); err != nil {
		log.Printf("[auth] token validation failed: %v", err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html><html><body><p>Login failed. Please try again.</p></body></html>`)
		return
	}

	// Persist token to config
	if err := s.Config.Update(func(c *config.Config) {
		c.Token = token
	}); err != nil {
		log.Printf("[auth] failed to save token: %v", err)
		http.Error(w, "failed to save token", http.StatusInternalServerError)
		return
	}

	// Switch store to cloud mode
	s.ActivateCloud(remoteStore)

	// Auto-create a default project if the user has none yet
	s.ensureDefaultProject(remoteStore.Info.UserName)

	// Notify connected frontend clients
	data, _ := json.Marshal(map[string]interface{}{
		"type":      "auth:success",
		"user_name": remoteStore.Info.UserName,
	})
	s.Hub.BroadcastRaw(data)

	// Return a minimal page that auto-closes; the main window reacts to the WS event
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!DOCTYPE html><html><body><p>Login successful.</p><script>window.close()</script></body></html>`)
}

// HandleAuthLogout removes the stored token and drops back to local-only mode.
// POST /api/auth/logout
func (s *Server) HandleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.Config.Update(func(c *config.Config) {
		c.Token = ""
	}); err != nil {
		log.Printf("[auth] failed to remove token: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to remove token")
		return
	}

	s.DeactivateCloud()

	data, _ := json.Marshal(map[string]interface{}{"type": "auth:logout"})
	s.Hub.BroadcastRaw(data)

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ActivateCloud switches the server to use the given remote store and starts
// cloud-mode background goroutines (change polling, lark chat).
func (s *Server) ActivateCloud(remoteStore *store.RemoteStore) {
	s.Cloud = remoteStore
	s.Store = store.NewCompositeStore(remoteStore, s.LocalStore)
	s.StoreMode = "remote"
	log.Printf("[auth] cloud activated, user=%s", remoteStore.Info.UserName)
	if s.OnCloudActivated != nil {
		s.OnCloudActivated()
	}
}

// DeactivateCloud stops cloud-mode background goroutines and reverts to
// local-only store.
func (s *Server) DeactivateCloud() {
	if s.OnCloudDeactivated != nil {
		s.OnCloudDeactivated()
	}
	s.Cloud = nil
	s.Store = s.LocalStore
	s.StoreMode = "sqlite"
	log.Printf("[auth] cloud deactivated")
}

// ensureDefaultProject creates a default project named after the user if none exist.
// If the project already exists remotely (e.g. config was deleted and re-logged in),
// it finds and associates the existing project instead.
// Called once after a successful cloud login so new users can start a session immediately.
func (s *Server) ensureDefaultProject(userName string) {
	// Check local config: if the user already has project mappings, nothing to do.
	// This mirrors what the sidebar shows — only locally-mapped projects appear.
	if len(s.Config.Get().Projects) > 0 {
		return
	}

	// Create a directory for the user under DataDir (MkdirAll is idempotent).
	localPath := filepath.Join(s.DataDir, userName)
	if err := os.MkdirAll(localPath, 0755); err != nil {
		log.Printf("[auth] ensureDefaultProject: mkdir %s: %v", localPath, err)
		return
	}

	// Create the project on the remote (local path is never sent).
	ns := toNamespaceID(userName)
	project, err := s.Store.CreateProject(CreateProjectRequest{Name: userName, NamespaceID: ns})
	if err != nil {
		if errors.Is(err, store.ErrVersionConflict) {
			// Project already exists remotely (e.g. config was wiped): find and associate it.
			log.Printf("[auth] ensureDefaultProject: project already exists remotely, searching for it...")
			project = s.findProjectByName(userName)
			if project == nil {
				log.Printf("[auth] ensureDefaultProject: could not find existing project for %q", userName)
				return
			}
			log.Printf("[auth] ensureDefaultProject: found existing project %q (id=%s)", project.Name, project.ID)
		} else {
			log.Printf("[auth] ensureDefaultProject: create project: %v", err)
			return
		}
	}

	// Persist the local path mapping in config.json.
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
		log.Printf("[auth] ensureDefaultProject: config update: %v", err)
	}
	project.GitRepoPath = localPath

	// Copy any globally installed skills and plugins into the new project.
	if s.LocalStore != nil {
		s.copyGlobalSkillsToProject(localPath)
		s.copyGlobalPluginsToProject(localPath)
	}

	log.Printf("[auth] ensured default project %q (id=%s) at %s", userName, project.ID, localPath)
}

// findProjectByName lists all remote projects and returns the first one matching name.
func (s *Server) findProjectByName(name string) *store.Project {
	projects, err := s.Store.ListProjects()
	if err != nil {
		log.Printf("[auth] findProjectByName: list projects: %v", err)
		return nil
	}
	for i := range projects {
		if projects[i].Name == name {
			return &projects[i]
		}
	}
	return nil
}

// toNamespaceID derives a valid namespace ID from a username.
// Remote API requires 3–10 chars matching [a-zA-Z0-9_].
func toNamespaceID(userName string) string {
	// Replace every invalid character with underscore
	re := regexp.MustCompile(`[^a-zA-Z0-9_]`)
	ns := re.ReplaceAllString(userName, "_")
	// Collapse consecutive underscores and trim them
	ns = regexp.MustCompile(`_+`).ReplaceAllString(ns, "_")
	ns = strings.Trim(ns, "_")
	// Truncate to 10
	if len(ns) > 10 {
		ns = ns[:10]
	}
	// Pad to at least 3
	for len(ns) < 3 {
		ns += "_"
	}
	return ns
}

// extractURLBase returns the scheme+host of a URL (e.g. "https://sre-admin.okg.com").
func extractURLBase(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("no host in URL: %s", rawURL)
	}
	return fmt.Sprintf("%s://%s", u.Scheme, u.Host), nil
}
