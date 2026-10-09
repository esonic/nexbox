package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	agentpkg "github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/store"
)

// ---------------------------------------------------------------------------
// Chat integration: inject installed MCPs into agent requests
// ---------------------------------------------------------------------------

func (s *Server) resolveInstalledMCPsAndMap(projectID string) ([]agentpkg.MCPServerEntry, map[string]string) {
	if s.Store == nil {
		return nil, nil
	}
	mcps, err := s.Store.ListInstalledMCPs(projectID)
	if err != nil {
		log.Printf("[marketplace] failed to load installed MCPs: %v", err)
		return nil, nil
	}
	keyToPkg := make(map[string]string, len(mcps))
	for _, mcp := range mcps {
		keyToPkg[mcp.MCPKey] = mcp.Name
	}
	byName := make(map[string]store.InstalledMCP, len(mcps))
	for _, mcp := range mcps {
		byName[mcp.Name] = mcp
	}
	var entries []agentpkg.MCPServerEntry
	for _, mcp := range byName {
		manifestJSON := mcp.Manifest
		if len(manifestJSON) >= 2 && manifestJSON[0] == '"' {
			var inner string
			if json.Unmarshal([]byte(manifestJSON), &inner) == nil {
				manifestJSON = inner
			}
		}
		var mcpWrap struct {
			MCPServers map[string]struct {
				URL     string            `json:"url"`
				Command string            `json:"command"`
				Args    []string          `json:"args"`
				Env     map[string]string `json:"env"`
				Headers map[string]string `json:"headers"`
				OAuth   json.RawMessage   `json:"oauth"`
			} `json:"mcpServers"`
		}
		if json.Unmarshal([]byte(manifestJSON), &mcpWrap) == nil && len(mcpWrap.MCPServers) > 0 {
			for k, cfg := range mcpWrap.MCPServers {
				entry := agentpkg.MCPServerEntry{Key: k}
				if cfg.URL != "" {
					entry.Type = "http"
					entry.URL = cfg.URL
					entry.Headers = cfg.Headers
					entry.OAuth = cfg.OAuth
				} else {
					entry.Type = "stdio"
					entry.Cmd = cfg.Command
					entry.Args = cfg.Args
					entry.Env = cfg.Env
				}
				manifestJSON, _ = func() (string, error) {
					b, err := json.Marshal(entry)
					return string(b), err
				}()
				break
			}
		}
		var entry agentpkg.MCPServerEntry
		if err := json.Unmarshal([]byte(manifestJSON), &entry); err != nil {
			log.Printf("[marketplace] skip MCP %s: invalid manifest: %v", mcp.Name, err)
			continue
		}
		if entry.Key == "" {
			log.Printf("[marketplace] skip MCP %s: manifest has empty key", mcp.Name)
			continue
		}

		// Always route HTTP MCPs through the local proxy so we can:
		//  - inject auth headers (apikey / OAuth Bearer)
		//  - detect upstream 401 and trigger OAuth flow via WebSocket
		//  - enforce access control via ?session= parameter
		// AppendSessionURL=true tells claude_agent.go to append ?session=<conversationID> at runtime.
		if entry.Type == "http" && s.ListenPort > 0 {
			entry.URL = fmt.Sprintf("http://127.0.0.1:%d/mcp-proxy/%s", s.ListenPort, mcp.Name)
			entry.Headers = nil
			entry.AppendSessionURL = true
		} else if mcp.EnvValues != "" && mcp.EnvValues != "{}" {
			var envVals map[string]string
			if json.Unmarshal([]byte(mcp.EnvValues), &envVals) == nil && len(envVals) > 0 {
				for k, v := range envVals {
					if strings.HasPrefix(k, "header:") {
						if entry.Headers == nil {
							entry.Headers = make(map[string]string)
						}
						entry.Headers[strings.TrimPrefix(k, "header:")] = v
					} else {
						if entry.Env == nil {
							entry.Env = make(map[string]string)
						}
						entry.Env[k] = v
					}
				}
			}
		}
		entries = append(entries, entry)
	}
	return entries, keyToPkg
}

// resolveInstalledPluginDirs returns the install paths for all installed plugins
// (type "plugin" or "lsp") that have an install_path set. These are passed to
// Claude CLI via --plugin-dir.
// If a stored entry has an empty install_path, we attempt to resolve it from
// the marketplace plugin list as a fallback.
func (s *Server) resolveInstalledPluginDirs(projectID string) []string {
	if s.Store == nil {
		return nil
	}
	entries, err := s.Store.ListInstalledPluginEntries(projectID)
	if err != nil {
		log.Printf("[marketplace] failed to load installed plugins: %v", err)
		return nil
	}

	// Build a marketplace name→install_path lookup for fallback resolution.
	var mpPlugins []MarketplacePlugin
	var mpLoaded bool

	var dirs []string
	seen := make(map[string]bool)
	for _, e := range entries {
		path := e.InstallPath

		// Re-resolve if the stored path is empty or doesn't contain a
		// valid Claude plugin structure (.claude-plugin/ directory).
		needsResolve := path == ""
		if !needsResolve {
			if _, err := os.Stat(filepath.Join(path, ".claude-plugin")); err != nil {
				needsResolve = true
			}
		}
		if needsResolve {
			// Lazy-load marketplace plugins on first miss.
			if !mpLoaded {
				mpLoaded = true
				mpPlugins, _ = loadMarketplacePlugins(s.marketplaceDir(), sourcesByPath(s.Store))
			}
			for _, p := range mpPlugins {
				if p.Name == e.Name && p.InstallPath != "" {
					path = p.InstallPath
					// Backfill the DB so future lookups don't need the fallback.
					e.InstallPath = path
					_ = s.Store.UpsertInstalledPluginEntry(e)
					log.Printf("[marketplace] backfilled install_path for plugin %s: %s", e.Name, path)
					break
				}
			}
		}
		if path != "" && !seen[path] {
			seen[path] = true
			dirs = append(dirs, path)
		}
	}
	return dirs
}

// ---------------------------------------------------------------------------
// Route registration
// ---------------------------------------------------------------------------

func (s *Server) RegisterMarketplaceRoutes(r chi.Router) {
	r.Route("/marketplace", func(r chi.Router) {
		// Local directory-based plugin registry
		r.Get("/plugins", s.ListLocalPlugins)
		r.Get("/plugins/{id}", s.GetLocalPlugin)

		// Marketplace sources (Add Marketplace)
		r.Get("/sources", s.ListMarketplaceSources)
		r.Post("/sources", s.AddMarketplaceSource)
		r.Post("/mcp-sources", s.AddManualMCPSource)
		r.Delete("/sources/{id}", s.RemoveMarketplaceSource)

		// Local install/uninstall
		r.Get("/installed-mcps", s.ListInstalledMCPs)
		r.Post("/installed-mcps", s.InstallMCP)
		r.Delete("/installed-mcps/{name}", s.UninstallMCP)

		r.Get("/installed-skills", s.ListInstalledSkills)
		r.Post("/installed-skills", s.InstallSkill)
		r.Delete("/installed-skills/{name}", s.UninstallSkill)

		r.Get("/installed-plugins", s.ListInstalledPluginEntries)
		r.Post("/installed-plugins", s.InstallPluginEntry)
		r.Delete("/installed-plugins/{name}", s.UninstallPluginEntry)
	})

	// MCP OAuth routes
	r.Post("/connectors/{name}/oauth/start", s.HandleOAuthStart)
	r.Get("/connectors/oauth/callback", s.HandleOAuthCallback)
	r.Post("/connectors/{name}/probe", s.HandleMCPProbe)
}

// ---------------------------------------------------------------------------
// Utility functions
// ---------------------------------------------------------------------------

// allGitRepoPaths returns the git repo paths for all known projects.
func (s *Server) allGitRepoPaths() []string {
	projects, err := s.Store.ListProjects()
	if err != nil {
		return nil
	}
	var paths []string
	for _, p := range projects {
		if p.GitRepoPath != "" {
			paths = append(paths, p.GitRepoPath)
		}
	}
	return paths
}

// symlinkDir creates a symlink at dst pointing to src.
// It ensures the parent directory exists and removes any pre-existing entry at dst.
func symlinkDir(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	// Remove any pre-existing entry (old copy or stale symlink).
	_ = os.RemoveAll(dst)
	return os.Symlink(src, dst)
}

// removeInstallDir removes an installed directory or symlink.
func removeInstallDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return os.Remove(dir)
	}
	return os.RemoveAll(dir)
}

// copyDir recursively copies src directory to dst.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
