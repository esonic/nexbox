package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/esonic/nexbox/store"
)

// ---------------------------------------------------------------------------
// Plugin install/uninstall (--plugin-dir)
// ---------------------------------------------------------------------------

type InstallPluginEntryRequest struct {
	Name        string `json:"name"`
	InstallPath string `json:"install_path"`
	LSPConfig   string `json:"lsp_config"`
	ProjectID   string `json:"project_id"`
}

func (s *Server) ListInstalledPluginEntries(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeJSON(w, http.StatusOK, []store.InstalledPluginEntry{})
		return
	}
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" {
		projectID = "*"
	}
	entries, err := s.LocalStore.ListInstalledPluginEntries(projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) InstallPluginEntry(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	var req InstallPluginEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	srcPath := req.InstallPath
	lspConfig := req.LSPConfig

	// If no explicit install_path, try to resolve from marketplace directory.
	if srcPath == "" && lspConfig == "" {
		plugins, _ := loadMarketplacePlugins(s.marketplaceDir(), sourcesByPath(s.LocalStore))
		for _, p := range plugins {
			if p.Name == req.Name {
				srcPath = p.InstallPath
				lspConfig = p.LSPConfig
				break
			}
		}
	}

	entry, err := s.installPluginToProject(req.Name, srcPath, lspConfig, req.ProjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// If global install, propagate to all projects with a git repo path.
	if req.ProjectID == "" {
		s.copyGlobalPluginToProjects(req.Name, entry.InstallPath)
	}

	if s.MarketplaceStats != nil {
		s.MarketplaceStats.RecordInstall(req.Name, "plugin")
	}
	log.Printf("[marketplace] installed plugin: %s → %s (project=%q)", req.Name, entry.InstallPath, req.ProjectID)
	writeJSON(w, http.StatusOK, entry)
}

// writePluginToDisk writes the plugin files into installDir.
// srcPath and lspConfig follow the same semantics as installPluginToProject.
func writePluginToDisk(srcPath, lspConfig, installDir string) error {
	if srcPath != "" {
		// Source directory has a proper .claude-plugin/plugin.json — link it.
		if _, err := os.Stat(filepath.Join(srcPath, ".claude-plugin", "plugin.json")); err == nil {
			return symlinkDir(srcPath, installDir)
		} else if lspConfig != "" {
			// Source directory exists but has no plugin structure; write .lsp.json instead.
			if err := os.MkdirAll(installDir, 0755); err != nil {
				return fmt.Errorf("create plugin dir: %w", err)
			}
			return os.WriteFile(filepath.Join(installDir, ".lsp.json"), []byte(lspConfig), 0644)
		}
		// Regular plugin directory — link as-is.
		return symlinkDir(srcPath, installDir)
	} else if lspConfig != "" {
		// No source directory — create a minimal plugin with just .lsp.json.
		if err := os.MkdirAll(installDir, 0755); err != nil {
			return fmt.Errorf("create plugin dir: %w", err)
		}
		return os.WriteFile(filepath.Join(installDir, ".lsp.json"), []byte(lspConfig), 0644)
	}
	return fmt.Errorf("install_path or lsp_config is required")
}

// installPluginToProject writes the plugin to disk and records the install in the DB.
// For global installs (projectID == ""):
//   - LSP plugins are stored under DataDir/global-plugins/<name>/ (real files needed for propagation).
//   - Regular plugins store the original srcPath directly — no intermediate symlink needed.
func (s *Server) installPluginToProject(name, srcPath, lspConfig, projectID string) (store.InstalledPluginEntry, error) {
	var installDir string
	if projectID == "" {
		if lspConfig != "" {
			// LSP plugin: store real files in global-plugins so propagation can read the config.
			installDir = filepath.Join(s.DataDir, "global-plugins", name)
		} else {
			// Regular plugin: record the source path directly; no intermediate copy needed.
			installDir = srcPath
		}
	} else {
		project, err := s.getProjectWithLocalPath(projectID)
		if err != nil || project == nil {
			return store.InstalledPluginEntry{}, fmt.Errorf("project not found: %s", projectID)
		}
		if project.GitRepoPath == "" {
			return store.InstalledPluginEntry{}, fmt.Errorf("project %s has no git repo path", projectID)
		}
		installDir = filepath.Join(project.GitRepoPath, ".claude", "plugins", name)
	}
	if projectID != "" || lspConfig != "" {
		// Only write to disk for per-project installs and LSP global installs.
		// Regular global installs have no installDir to write to — propagation handles each project.
		if err := writePluginToDisk(srcPath, lspConfig, installDir); err != nil {
			return store.InstalledPluginEntry{}, err
		}
	}

	entry := store.InstalledPluginEntry{
		Name:        name,
		ProjectID:   projectID,
		InstallPath: installDir,
		InstalledAt: store.UnixTime(time.Now()),
	}
	if err := s.LocalStore.UpsertInstalledPluginEntry(entry); err != nil {
		_ = removeInstallDir(installDir)
		return store.InstalledPluginEntry{}, err
	}
	return entry, nil
}

func (s *Server) UninstallPluginEntry(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	name := chi.URLParam(r, "name")
	projectID := r.URL.Query().Get("project_id")

	// Collect install paths before deleting DB records so we can clean up the filesystem.
	globalPluginsPrefix := filepath.Join(s.DataDir, "global-plugins") + string(filepath.Separator)
	var pathsToRemove []string
	if entries, err := s.LocalStore.ListInstalledPluginEntries("*"); err == nil {
		for _, e := range entries {
			if e.Name != name {
				continue
			}
			if projectID != "" && e.ProjectID != projectID {
				continue
			}
			if e.InstallPath == "" {
				continue
			}
			if e.ProjectID == "" && !strings.HasPrefix(e.InstallPath, globalPluginsPrefix) {
				// Regular global plugin: InstallPath is the marketplace source — don't delete it.
				continue
			}
			pathsToRemove = append(pathsToRemove, e.InstallPath)
		}
	}
	// For global uninstall, also remove the per-project symlinks/dirs propagated to each project.
	if projectID == "" {
		for _, repoPath := range s.allGitRepoPaths() {
			pathsToRemove = append(pathsToRemove, filepath.Join(repoPath, ".claude", "plugins", name))
		}
	}

	var delErr error
	if projectID == "" {
		delErr = s.LocalStore.DeleteInstalledPluginEntryByName(name)
	} else {
		delErr = s.LocalStore.DeleteInstalledPluginEntry(name, projectID)
	}
	if delErr != nil {
		writeError(w, http.StatusInternalServerError, delErr.Error())
		return
	}

	// Remove installed directories / symlinks from disk.
	for _, p := range pathsToRemove {
		if err := removeInstallDir(p); err != nil {
			log.Printf("[marketplace] warning: failed to remove plugin dir %s: %v", p, err)
		}
	}

	log.Printf("[marketplace] uninstalled plugin: %s (project=%q)", name, projectID)
	w.WriteHeader(http.StatusNoContent)
}

// propagatePluginToProject writes a globally-installed plugin into a specific project's
// .claude/plugins directory (filesystem only, no DB record — the global entry covers it).
// It reuses writePluginToDisk: for LSP plugins the .lsp.json content is read from the
// global install dir; for regular plugins the global dir is used as srcPath.
func (s *Server) propagatePluginToProject(name, globalInstallDir, gitRepoPath string) error {
	srcPath := globalInstallDir
	lspConfig := ""
	if data, err := os.ReadFile(filepath.Join(globalInstallDir, ".lsp.json")); err == nil {
		// LSP plugin: globalInstallDir is the global-plugins dir holding the real .lsp.json.
		srcPath = ""
		lspConfig = string(data)
	}
	// Regular plugin: globalInstallDir IS the original source path (stored directly in DB).
	installDir := filepath.Join(gitRepoPath, ".claude", "plugins", name)
	return writePluginToDisk(srcPath, lspConfig, installDir)
}

func (s *Server) copyGlobalPluginToProjects(pluginName, srcDir string) {
	var wg sync.WaitGroup
	for _, repoPath := range s.allGitRepoPaths() {
		wg.Add(1)
		go func(rp string) {
			defer wg.Done()
			if err := s.propagatePluginToProject(pluginName, srcDir, rp); err != nil {
				log.Printf("[marketplace] failed to propagate global plugin %s to %s: %v", pluginName, rp, err)
			}
		}(repoPath)
	}
	wg.Wait()
}

func (s *Server) copyGlobalPluginsToProject(gitRepoPath string) {
	globalPlugins, err := s.LocalStore.ListInstalledPluginEntries("")
	if err != nil {
		log.Printf("[marketplace] failed to load global plugins for new project: %v", err)
		return
	}
	for _, plugin := range globalPlugins {
		if plugin.InstallPath == "" {
			continue
		}
		if err := s.propagatePluginToProject(plugin.Name, plugin.InstallPath, gitRepoPath); err != nil {
			log.Printf("[marketplace] failed to propagate global plugin %s to new project: %v", plugin.Name, err)
		}
	}
}
