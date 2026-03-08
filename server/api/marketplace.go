package api

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	agentpkg "gitlab.okg.com/sre_ai/nexbox/agent"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

// ---------------------------------------------------------------------------
// Chat integration: inject installed MCPs into agent requests
// ---------------------------------------------------------------------------

// resolveInstalledMCPsAndMap returns both the MCPServerEntry list and the key→package
// name map using a single store query. Use this instead of calling resolveInstalledMCPs
// and buildMCPKeyToPackageMap separately.
func (s *Server) resolveInstalledMCPsAndMap(projectID string) ([]agentpkg.MCPServerEntry, map[string]string) {
	if s.LocalStore == nil {
		return nil, nil
	}
	mcps, err := s.LocalStore.ListInstalledMCPs(projectID)
	if err != nil {
		log.Printf("[marketplace] failed to load installed MCPs: %v", err)
		return nil, nil
	}
	// Build key→package map from raw rows (before deduplication).
	keyToPkg := make(map[string]string, len(mcps))
	for _, mcp := range mcps {
		keyToPkg[mcp.MCPKey] = mcp.Name
	}
	// Deduplicate by name: project-specific takes precedence over global.
	byName := make(map[string]store.InstalledMCP, len(mcps))
	for _, mcp := range mcps {
		byName[mcp.Name] = mcp
	}
	var entries []agentpkg.MCPServerEntry
	for _, mcp := range byName {
		manifestJSON := mcp.Manifest
		// Unwrap double-encoded manifests (legacy rows stored before the fix).
		if len(manifestJSON) >= 2 && manifestJSON[0] == '"' {
			var inner string
			if json.Unmarshal([]byte(manifestJSON), &inner) == nil {
				manifestJSON = inner
			}
		}
		// Also handle legacy rows stored in mcpServers wrapper format.
		var mcpWrap struct {
			MCPServers map[string]struct {
				URL     string            `json:"url"`
				Command string            `json:"command"`
				Args    []string          `json:"args"`
				Env     map[string]string `json:"env"`
				Headers map[string]string `json:"headers"`
			} `json:"mcpServers"`
		}
		if json.Unmarshal([]byte(manifestJSON), &mcpWrap) == nil && len(mcpWrap.MCPServers) > 0 {
			for k, cfg := range mcpWrap.MCPServers {
				entry := agentpkg.MCPServerEntry{Key: k}
				if cfg.URL != "" {
					entry.Type = "http"
					entry.URL = cfg.URL
					entry.Headers = cfg.Headers
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
		if mcp.EnvValues != "" && mcp.EnvValues != "{}" {
			var envVals map[string]string
			if json.Unmarshal([]byte(mcp.EnvValues), &envVals) == nil && len(envVals) > 0 {
				for k, v := range envVals {
					if strings.HasPrefix(k, "header:") {
						// "header:Authorization" → entry.Headers["Authorization"]
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

// ---------------------------------------------------------------------------
// Stats buffer — pure in-memory, flushed via pollChanges every few seconds.
// Data loss on crash is acceptable (a few seconds of stats).
// ---------------------------------------------------------------------------

// pkgKey is the map key for stats buffers, combining package name and type.
type pkgKey struct {
	Name    string
	PkgType string
}

// MarketplaceStatsBuffer accumulates install/call/session/message counts in memory.
type MarketplaceStatsBuffer struct {
	mu       sync.Mutex
	installs map[pkgKey]int64
	calls    map[pkgKey]int64
	sessions int64
	messages int64
}

func NewMarketplaceStatsBuffer() *MarketplaceStatsBuffer {
	return &MarketplaceStatsBuffer{
		installs: make(map[pkgKey]int64),
		calls:    make(map[pkgKey]int64),
	}
}

func (b *MarketplaceStatsBuffer) RecordInstall(name, pkgType string) {
	b.mu.Lock()
	b.installs[pkgKey{name, pkgType}]++
	b.mu.Unlock()
}

func (b *MarketplaceStatsBuffer) RecordCall(name, pkgType string) {
	b.mu.Lock()
	b.calls[pkgKey{name, pkgType}]++
	b.mu.Unlock()
}

func (b *MarketplaceStatsBuffer) RecordSession() {
	b.mu.Lock()
	b.sessions++
	b.mu.Unlock()
}

func (b *MarketplaceStatsBuffer) RecordMessage() {
	b.mu.Lock()
	b.messages++
	b.mu.Unlock()
}

// Flush atomically swaps out the accumulated counters and returns them.
// Returns nil maps and zero counts if there is nothing to report.
func (b *MarketplaceStatsBuffer) Flush() (installs, calls map[pkgKey]int64, sessions, messages int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.installs) == 0 && len(b.calls) == 0 && b.sessions == 0 && b.messages == 0 {
		return nil, nil, 0, 0
	}
	installs, calls = b.installs, b.calls
	sessions, messages = b.sessions, b.messages
	b.installs = make(map[pkgKey]int64)
	b.calls = make(map[pkgKey]int64)
	b.sessions = 0
	b.messages = 0
	return
}

// FlushMarketplaceStats sends accumulated stats to the remote server, if any.
// Non-fatal: logs errors and discards stats rather than blocking.
func (s *Server) FlushMarketplaceStats() {
	if s.MarketplaceStats == nil || s.Cloud == nil {
		return
	}
	installs, calls, sessions, messages := s.MarketplaceStats.Flush()
	if installs == nil && calls == nil && sessions == 0 && messages == 0 {
		return
	}
	report := store.MarketplaceStatsReport{
		User:     s.Cloud.Info.UserName,
		Sessions: sessions,
		Messages: messages,
	}
	for k, count := range installs {
		report.Installs = append(report.Installs, store.NameCount{Name: k.Name, Type: k.PkgType, Count: count})
	}
	for k, count := range calls {
		report.Calls = append(report.Calls, store.NameCount{Name: k.Name, Type: k.PkgType, Count: count})
	}
	if err := s.Cloud.ReportMarketplaceStats(report); err != nil {
		log.Printf("[marketplace] stats flush error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Route registration
// ---------------------------------------------------------------------------

func (s *Server) RegisterMarketplaceRoutes(r chi.Router) {
	r.Route("/marketplace", func(r chi.Router) {
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

		// Local directory-based plugin registry
		r.Get("/plugins", s.ListLocalPlugins)
		r.Get("/plugins/{id}", s.GetLocalPlugin)
		r.Get("/installed", s.ListInstalledPlugins)
		r.Post("/installed", s.HandleInstallPlugin)
		r.Delete("/installed/{id}", s.HandleUninstallPlugin)
	})
}

// ---------------------------------------------------------------------------
// Local directory-based plugin registry
// ---------------------------------------------------------------------------

// loadMarketplacePlugins recursively scans dir for plugins in three formats:
//  1. Custom JSON files (*.json) — legacy format
//  2. Anthropic skills (dirs containing SKILL.md with YAML frontmatter)
//  3. Anthropic plugins (dirs containing .claude-plugin/plugin.json + optional .mcp.json)
//
// Returns an empty slice (not an error) if dir is empty or does not exist.
func loadMarketplacePlugins(dir string) ([]store.MarketplacePlugin, error) {
	if dir == "" {
		return []store.MarketplacePlugin{}, nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return []store.MarketplacePlugin{}, nil
	}

	var plugins []store.MarketplacePlugin
	seen := make(map[string]bool) // deduplicate by ID

	// Walk the entire directory tree looking for recognizable formats.
	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries
		}
		// Skip hidden dirs (like .git) except .claude-plugin which we need.
		if info.IsDir() && strings.HasPrefix(info.Name(), ".") && info.Name() != ".claude-plugin" {
			return filepath.SkipDir
		}

		// --- Format 1a: .claude-plugin/plugin.json (Anthropic plugins/connectors) ---
		// Check this BEFORE generic *.json to avoid treating plugin.json as custom JSON.
		if !info.IsDir() && info.Name() == "plugin.json" {
			parentDir := filepath.Dir(path)
			if filepath.Base(parentDir) != ".claude-plugin" {
				return nil
			}
			pluginRoot := filepath.Dir(parentDir) // the actual plugin directory
			p, err := loadClaudePlugin(pluginRoot, path)
			if err != nil {
				log.Printf("[marketplace] skip plugin %s: %v", path, err)
				return nil
			}
			if !seen[p.ID] {
				seen[p.ID] = true
				plugins = append(plugins, *p)
			}
			return nil
		}

		// --- Format 1b: .claude-plugin/marketplace.json (third-party plugin registries) ---
		// Some third-party repos use marketplace.json instead of plugin.json, with a
		// "plugins" array listing multiple plugins in a single file.
		if !info.IsDir() && info.Name() == "marketplace.json" {
			parentDir := filepath.Dir(path)
			if filepath.Base(parentDir) != ".claude-plugin" {
				return nil
			}
			pluginRoot := filepath.Dir(parentDir)
			items, err := loadMarketplaceJSON(pluginRoot, path)
			if err != nil {
				log.Printf("[marketplace] skip marketplace.json %s: %v", path, err)
				return nil
			}
			for _, p := range items {
				if !seen[p.ID] {
					seen[p.ID] = true
					plugins = append(plugins, p)
				}
			}
			return nil
		}

		// --- Format 2: SKILL.md (Anthropic skills format) ---
		if !info.IsDir() && info.Name() == "SKILL.md" {
			p, err := loadSkillMD(path)
			if err != nil {
				log.Printf("[marketplace] skip skill %s: %v", path, err)
				return nil
			}
			if !seen[p.ID] {
				seen[p.ID] = true
				plugins = append(plugins, *p)
			}
			return nil
		}

		// --- Format 3: Custom JSON files (*.json at any level) ---
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".json") {
			// Skip files inside .claude-plugin dirs (plugin metadata, not custom JSON).
			if strings.Contains(path, "/.claude-plugin/") || strings.Contains(path, "\\.claude-plugin\\") {
				return nil
			}
			// Skip .mcp.json and marketplace.json files.
			if info.Name() == ".mcp.json" || info.Name() == "marketplace.json" {
				return nil
			}
			p, err := loadCustomJSON(path)
			if err != nil {
				log.Printf("[marketplace] skip %s: %v", path, err)
				return nil
			}
			if !seen[p.ID] {
				seen[p.ID] = true
				plugins = append(plugins, *p)
			}
			return nil
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk marketplace dir: %w", err)
	}

	if plugins == nil {
		plugins = []store.MarketplacePlugin{}
	}
	return plugins, nil
}

// loadCustomJSON parses a legacy *.json plugin definition.
func loadCustomJSON(path string) (*store.MarketplacePlugin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var p store.MarketplacePlugin
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if p.ID == "" {
		p.ID = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	if p.Source == "" {
		p.Source = "custom"
	}
	return &p, nil
}

// loadSkillMD parses an Anthropic-format SKILL.md with YAML frontmatter.
// Expected format:
//
//	---
//	name: skill-name
//	description: what it does
//	---
//	<markdown body>
func loadSkillMD(path string) (*store.MarketplacePlugin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	content := string(data)

	// Parse YAML frontmatter.
	name, desc := parseSkillFrontmatter(content)
	if name == "" {
		// Fallback: use parent directory name.
		name = filepath.Base(filepath.Dir(path))
	}

	p := &store.MarketplacePlugin{
		ID:          "skill-" + name,
		Name:        name,
		Description: desc,
		Type:        "skill",
		Source:      "skill",
		InstallPath: filepath.Dir(path),
	}
	return p, nil
}

// parseSkillFrontmatter extracts name and description from YAML frontmatter.
// Simple parser — no dependency on a YAML library.
func parseSkillFrontmatter(content string) (name, description string) {
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return "", ""
	}
	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return "", ""
	}
	frontmatter := parts[1]
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			name = strings.Trim(name, `"'`)
		} else if strings.HasPrefix(line, "description:") {
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			description = strings.Trim(description, `"'`)
		}
	}
	return name, description
}

// claudePluginJSON is the schema for .claude-plugin/plugin.json.
type claudePluginJSON struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Author      struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"author"`
}

// loadClaudePlugin parses a .claude-plugin/plugin.json and optional .mcp.json.
func loadClaudePlugin(pluginRoot, pluginJSONPath string) (*store.MarketplacePlugin, error) {
	data, err := os.ReadFile(pluginJSONPath)
	if err != nil {
		return nil, fmt.Errorf("read plugin.json: %w", err)
	}
	var meta claudePluginJSON
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse plugin.json: %w", err)
	}
	if meta.Name == "" {
		meta.Name = filepath.Base(pluginRoot)
	}

	p := &store.MarketplacePlugin{
		ID:          meta.Name,
		Name:        meta.Name,
		Description: meta.Description,
		Version:     meta.Version,
		Author:      meta.Author.Name,
		Type:        "plugin",
		Source:      "plugin",
		InstallPath: pluginRoot,
	}

	// Check for .mcp.json — if present, this is an MCP plugin or connector.
	mcpPath := filepath.Join(pluginRoot, ".mcp.json")
	mcpData, err := os.ReadFile(mcpPath)
	if err == nil {
		p.MCPConfig = string(mcpData)
		p.Type = "mcp"

		// Determine source: connectors live under external_plugins/ dirs.
		relFromRoot := pluginRoot
		if strings.Contains(relFromRoot, "external_plugins") {
			p.Source = "connector"
		}

		// Extract env_vars from template variables ${VAR_NAME} and detect auth type.
		envVars, authType := parseMCPConfig(mcpData)
		if len(envVars) > 0 {
			p.EnvVars = envVars
		}
		p.AuthType = authType
	}

	return p, nil
}

// marketplaceJSONFile is the schema for .claude-plugin/marketplace.json used by
// third-party plugin repositories.
type marketplaceJSONFile struct {
	Name  string `json:"name"`
	Owner struct {
		Name string `json:"name"`
	} `json:"owner"`
	Metadata struct {
		Description string `json:"description"`
		Version     string `json:"version"`
	} `json:"metadata"`
	Plugins []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Version     string `json:"version"`
		Source      string `json:"source"` // relative path to command/skill file
		Author      struct {
			Name string `json:"name"`
		} `json:"author"`
		Tags []string `json:"tags"`
	} `json:"plugins"`
}

// loadMarketplaceJSON parses a .claude-plugin/marketplace.json (third-party format).
// Returns one MarketplacePlugin per entry in the "plugins" array.
func loadMarketplaceJSON(pluginRoot, path string) ([]store.MarketplacePlugin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var mf marketplaceJSONFile
	if err := json.Unmarshal(data, &mf); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if len(mf.Plugins) == 0 {
		return nil, nil
	}

	repoName := filepath.Base(pluginRoot)
	var result []store.MarketplacePlugin
	for _, entry := range mf.Plugins {
		name := entry.Name
		if name == "" {
			continue
		}
		author := entry.Author.Name
		if author == "" {
			author = mf.Owner.Name
		}
		p := store.MarketplacePlugin{
			ID:          repoName + "-" + name,
			Name:        name,
			Description: entry.Description,
			Version:     entry.Version,
			Author:      author,
			Type:        "plugin",
			Source:      "plugin",
		}
		result = append(result, p)
	}
	return result, nil
}

// parseMCPConfig parses a .mcp.json file and extracts:
//   - env_vars: template variable names from ${VAR_NAME} patterns in URLs and headers
//   - authType: "oauth" if oauth config is present, "apikey" if env vars required, "none" otherwise
func parseMCPConfig(data []byte) (envVars []string, authType string) {
	// Find all ${VAR_NAME} template variables.
	varSet := make(map[string]bool)
	content := string(data)
	for i := 0; i < len(content); i++ {
		if i+1 < len(content) && content[i] == '$' && content[i+1] == '{' {
			end := strings.Index(content[i:], "}")
			if end > 0 {
				varName := content[i+2 : i+end]
				if varName != "" {
					varSet[varName] = true
				}
			}
		}
	}
	for v := range varSet {
		envVars = append(envVars, v)
	}
	sort.Strings(envVars)

	// Detect OAuth config.
	authType = "none"
	if strings.Contains(content, `"oauth"`) {
		authType = "oauth"
	} else if len(envVars) > 0 {
		authType = "apikey"
	}

	return envVars, authType
}

// ListLocalPlugins handles GET /api/marketplace/plugins
// marketplaceDir returns the configured marketplace directory, falling back
// to {DataDir}/marketplace if not explicitly set.
func (s *Server) marketplaceDir() string {
	if s.Config != nil {
		if dir := s.Config.Get().MarketplaceDir; dir != "" {
			return dir
		}
	}
	if s.DataDir != "" {
		return filepath.Join(s.DataDir, "marketplace")
	}
	return ""
}

func (s *Server) ListLocalPlugins(w http.ResponseWriter, r *http.Request) {
	plugins, err := loadMarketplacePlugins(s.marketplaceDir())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plugins)
}

// GetLocalPlugin handles GET /api/marketplace/plugins/{id}
func (s *Server) GetLocalPlugin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dir := s.marketplaceDir()
	plugins, err := loadMarketplacePlugins(dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, p := range plugins {
		if p.ID == id {
			writeJSON(w, http.StatusOK, p)
			return
		}
	}
	writeError(w, http.StatusNotFound, "plugin not found")
}

// ListInstalledPlugins handles GET /api/marketplace/installed
func (s *Server) ListInstalledPlugins(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeJSON(w, http.StatusOK, []store.InstalledPlugin{})
		return
	}
	user := GetUserID(r)
	plugins, err := s.LocalStore.ListInstalledPlugins(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plugins)
}

// HandleInstallPlugin handles POST /api/marketplace/installed
func (s *Server) HandleInstallPlugin(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	var req struct {
		PluginID string `json:"plugin_id"`
		Config   string `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.PluginID == "" {
		writeError(w, http.StatusBadRequest, "plugin_id is required")
		return
	}
	user := GetUserID(r)
	installed, err := s.LocalStore.InstallPlugin(user, req.PluginID, req.Config)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("[marketplace] user %s installed plugin %s", user, req.PluginID)
	writeJSON(w, http.StatusCreated, installed)
}

// HandleUninstallPlugin handles DELETE /api/marketplace/installed/{id}
func (s *Server) HandleUninstallPlugin(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	pluginID := chi.URLParam(r, "id")
	user := GetUserID(r)
	if err := s.LocalStore.UninstallPlugin(user, pluginID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("[marketplace] user %s uninstalled plugin %s", user, pluginID)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Remote package registry handlers
// ---------------------------------------------------------------------------

func (s *Server) ListMarketplacePackages(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}
	pkgType := r.URL.Query().Get("type")
	q := r.URL.Query().Get("q")
	tag := r.URL.Query().Get("tag")
	sortBy := r.URL.Query().Get("sort")
	limit := parseIntParam(r, "limit", 20)
	offset := parseIntParam(r, "offset", 0)
	pkgs, err := s.Cloud.ListMarketplacePackages(pkgType, q, tag, sortBy, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pkgs)
}

func parseIntParam(r *http.Request, key string, def int) int {
	s := r.URL.Query().Get(key)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return def
	}
	return v
}

func (s *Server) GetMarketplacePackage(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeError(w, http.StatusServiceUnavailable, "marketplace requires cloud mode")
		return
	}
	id := chi.URLParam(r, "packageId")
	detail, err := s.Cloud.GetMarketplacePackage(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) UpdateMarketplacePackage(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeError(w, http.StatusServiceUnavailable, "marketplace requires cloud mode")
		return
	}
	id := chi.URLParam(r, "packageId")
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	pkg, err := s.Cloud.UpdateMarketplacePackage(id, body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pkg)
}

func (s *Server) DeleteMarketplacePackage(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeError(w, http.StatusServiceUnavailable, "marketplace requires cloud mode")
		return
	}
	id := chi.URLParam(r, "packageId")
	if err := s.Cloud.DeletePackage(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) GetPackageStarStatus(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeError(w, http.StatusServiceUnavailable, "marketplace requires cloud mode")
		return
	}
	id := chi.URLParam(r, "packageId")
	status, err := s.Cloud.GetPackageStarStatus(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) StarPackage(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeError(w, http.StatusServiceUnavailable, "marketplace requires cloud mode")
		return
	}
	id := chi.URLParam(r, "packageId")
	status, err := s.Cloud.StarPackage(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) UnstarPackage(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeError(w, http.StatusServiceUnavailable, "marketplace requires cloud mode")
		return
	}
	id := chi.URLParam(r, "packageId")
	status, err := s.Cloud.UnstarPackage(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) ListPackageReviews(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeError(w, http.StatusServiceUnavailable, "marketplace requires cloud mode")
		return
	}
	id := chi.URLParam(r, "packageId")
	reviews, err := s.Cloud.ListPackageReviews(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if reviews == nil {
		reviews = []store.PackageReview{}
	}
	writeJSON(w, http.StatusOK, reviews)
}

func (s *Server) AddPackageReview(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeError(w, http.StatusServiceUnavailable, "marketplace requires cloud mode")
		return
	}
	id := chi.URLParam(r, "packageId")
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	review, err := s.Cloud.AddPackageReview(id, body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, review)
}

func (s *Server) DeletePackageReview(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeError(w, http.StatusServiceUnavailable, "marketplace requires cloud mode")
		return
	}
	id := chi.URLParam(r, "packageId")
	reviewID := chi.URLParam(r, "reviewId")
	if err := s.Cloud.DeletePackageReview(id, reviewID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) PublishPackage(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeError(w, http.StatusServiceUnavailable, "marketplace requires cloud mode")
		return
	}
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// manifest may arrive as a JSON object; re-serialize to string for the Java backend
	if m, ok := body["manifest"]; ok {
		switch v := m.(type) {
		case string:
			// already a string, leave as-is
		default:
			b, err := json.Marshal(v)
			if err == nil {
				body["manifest"] = string(b)
			}
		}
	}
	// PublishPackage returns a MarketplacePackageDetail (package + versions) directly
	detail, err := s.Cloud.PublishPackage(body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

func (s *Server) PublishPackageVersion(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeError(w, http.StatusServiceUnavailable, "marketplace requires cloud mode")
		return
	}
	packageID := chi.URLParam(r, "packageId")
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ver, err := s.Cloud.PublishPackageVersion(packageID, body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, ver)
}

// ---------------------------------------------------------------------------
// Local MCP install/uninstall
// ---------------------------------------------------------------------------

// InstallMCPRequest is the body for POST /api/marketplace/installed-mcps.
type InstallMCPRequest struct {
	Name      string            `json:"name"`       // display name / package name
	PackageID string            `json:"package_id"` // remote package ID; used to record install count and update checks
	Version   string            `json:"version"`    // installed version string (for update detection)
	MCPKey    string            `json:"mcp_key"`    // server key (falls back to name)
	Manifest  json.RawMessage   `json:"manifest"`   // MCPServerEntry as JSON object or string
	Env       map[string]string `json:"env"`        // user-provided env secrets (e.g. API tokens)
	ProjectID string            `json:"project_id"` // "" = global, else project-scoped
}

func (s *Server) ListInstalledMCPs(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeJSON(w, http.StatusOK, []store.InstalledMCP{})
		return
	}
	// ?project_id= → filter; omit or "*" → all
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" {
		projectID = "*"
	}
	mcps, err := s.LocalStore.ListInstalledMCPs(projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mcps)
}

func (s *Server) InstallMCP(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	var req InstallMCPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	// Normalize manifest to a JSON string for storage.
	// The frontend pre-serializes the manifest object to a string before embedding it in the
	// request body JSON, so req.Manifest arrives as a JSON string token (with outer quotes).
	// Unwrap it here so we always work with a raw JSON object string.
	manifestStr := "{}"
	if len(req.Manifest) > 0 && string(req.Manifest) != "null" {
		raw := string(req.Manifest)
		if len(raw) >= 2 && raw[0] == '"' {
			// Double-encoded: a JSON string whose value is the manifest JSON.
			var inner string
			if json.Unmarshal(req.Manifest, &inner) == nil {
				raw = inner
			}
		}
		manifestStr = raw
	}
	// If manifest is in standard mcpServers format, extract the first server entry
	// into the flat MCPServerEntry format that resolveInstalledMCPs expects.
	var mcpServersWrap struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal([]byte(manifestStr), &mcpServersWrap) == nil && len(mcpServersWrap.MCPServers) > 0 {
		// Sort keys for deterministic selection when multiple servers are present.
		serverKeys := make([]string, 0, len(mcpServersWrap.MCPServers))
		for k := range mcpServersWrap.MCPServers {
			serverKeys = append(serverKeys, k)
		}
		sort.Strings(serverKeys)
		key := serverKeys[0]
		cfg := mcpServersWrap.MCPServers[key]
		entry := agentpkg.MCPServerEntry{Key: key}
		if cfg.URL != "" {
			entry.Type = "http"
			entry.URL = cfg.URL
			// Template headers (empty value = user-supplied at install time via env_values).
			// Store them as placeholders so the key names are preserved; runtime values
			// come from env_values (header: prefix) merged in resolveInstalledMCPsAndMap.
			if len(cfg.Headers) > 0 {
				entry.Headers = cfg.Headers
			}
		} else {
			entry.Type = "stdio"
			entry.Cmd = cfg.Command
			entry.Args = cfg.Args
			// Preserve non-secret default env vars from the manifest.
			// User-provided secrets (req.Env) are stored separately in env_values
			// and will be merged at runtime by resolveInstalledMCPs.
			if len(cfg.Env) > 0 {
				entry.Env = cfg.Env
			}
		}
		if b, err := json.Marshal(entry); err == nil {
			manifestStr = string(b)
		}
		if req.MCPKey == "" {
			req.MCPKey = key
		}
	}
	// Derive mcp_key from explicit field or manifest "key" field
	mcpKey := req.MCPKey
	if mcpKey == "" {
		var m struct {
			Key string `json:"key"`
		}
		if json.Unmarshal([]byte(manifestStr), &m) == nil && m.Key != "" {
			mcpKey = m.Key
		} else {
			mcpKey = req.Name
		}
	}

	// Serialize user-provided env secrets
	envValues := "{}"
	if len(req.Env) > 0 {
		if b, err := json.Marshal(req.Env); err == nil {
			envValues = string(b)
		}
	}

	mcp := store.InstalledMCP{
		Name:        req.Name,
		ProjectID:   req.ProjectID,
		PackageID:   req.PackageID,
		Version:     req.Version,
		MCPKey:      mcpKey,
		Manifest:    manifestStr,
		EnvValues:   envValues,
		InstalledAt: time.Now(),
	}
	if err := s.LocalStore.UpsertInstalledMCP(mcp); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if s.MarketplaceStats != nil {
		s.MarketplaceStats.RecordInstall(req.Name, "mcp")
	}
	if s.Cloud != nil && req.PackageID != "" {
		go func() {
			if err := s.Cloud.RecordPackageInstall(req.PackageID); err != nil {
				log.Printf("[marketplace] record install count for %s: %v", req.PackageID, err)
			}
		}()
	}
	log.Printf("[marketplace] installed MCP: %s (key=%s)", req.Name, mcpKey)
	writeJSON(w, http.StatusOK, mcp)
}

func (s *Server) UninstallMCP(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	name := chi.URLParam(r, "name")
	projectID := r.URL.Query().Get("project_id")
	if err := s.LocalStore.DeleteInstalledMCP(name, projectID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("[marketplace] uninstalled MCP: %s (project=%q)", name, projectID)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Local skill install/uninstall
// ---------------------------------------------------------------------------

// InstallSkillRequest is the body for POST /api/marketplace/installed-skills.
type InstallSkillRequest struct {
	PackageID   string `json:"package_id"`              // remote package ID (used for cloud download)
	Name        string `json:"name"`                    // package name (used as skill directory name)
	Version     string `json:"version"`
	ProjectID   string `json:"project_id"`              // "" = global
	InstallPath string `json:"install_path,omitempty"`  // local source path (for marketplace_dir skills; skips cloud download)
}

func (s *Server) ListInstalledSkills(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeJSON(w, http.StatusOK, []store.InstalledSkill{})
		return
	}
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" {
		projectID = "*" // return all
	}
	skills, err := s.LocalStore.ListInstalledSkills(projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, skills)
}

func (s *Server) InstallSkill(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	var req InstallSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Version == "" {
		req.Version = "0.0.0"
	}

	isLocal := req.InstallPath != ""

	if !isLocal {
		// Cloud install: requires cloud mode and package_id
		if s.Cloud == nil {
			writeError(w, http.StatusServiceUnavailable, "skill install requires cloud mode (or provide install_path for local install)")
			return
		}
		if req.PackageID == "" {
			writeError(w, http.StatusBadRequest, "package_id is required for cloud install")
			return
		}
	}

	// Determine install path
	installDir, err := s.skillInstallDir(req.Name, req.ProjectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if isLocal {
		// Local install: copy skill files from marketplace_dir source to install dir
		if err := copySkillDir(req.InstallPath, installDir); err != nil {
			log.Printf("[marketplace] failed to install local skill %s: %v", req.Name, err)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to install skill: %v", err))
			return
		}
	} else {
		// Cloud install: fetch detail and download tar
		detail, err := s.Cloud.GetMarketplacePackage(req.PackageID)
		if err != nil {
			log.Printf("[marketplace] failed to fetch package detail for skill %s: %v", req.Name, err)
		} else {
			s.autoInstallMCPDeps(detail, req.Version, req.ProjectID)
		}
		if err := s.downloadAndExtractSkill(req.PackageID, req.Version, installDir); err != nil {
			log.Printf("[marketplace] failed to install skill %s: %v", req.Name, err)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to install skill: %v", err))
			return
		}
	}

	skill := store.InstalledSkill{
		Name:        req.Name,
		ProjectID:   req.ProjectID,
		Version:     req.Version,
		InstallPath: installDir,
		InstalledAt: time.Now(),
	}
	if err := s.LocalStore.UpsertInstalledSkill(skill); err != nil {
		// DB write failed after extraction — clean up to avoid orphaned files.
		_ = os.RemoveAll(installDir)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// If global install, also copy into all existing projects; otherwise ensure .agents symlink.
	if req.ProjectID == "" {
		s.copyGlobalSkillToProjects(req.Name, installDir)
	} else {
		project, _ := s.getProjectWithLocalPath(req.ProjectID)
		if project != nil && project.GitRepoPath != "" {
			ensureAgentsSymlink(project.GitRepoPath)
		}
	}

	if s.MarketplaceStats != nil {
		s.MarketplaceStats.RecordInstall(req.Name, "skill")
	}
	if s.Cloud != nil && req.PackageID != "" {
		go func() {
			if err := s.Cloud.RecordPackageInstall(req.PackageID); err != nil {
				log.Printf("[marketplace] record install count for %s: %v", req.PackageID, err)
			}
		}()
	}
	log.Printf("[marketplace] installed skill: %s v%s → %s", req.Name, req.Version, installDir)
	writeJSON(w, http.StatusOK, skill)
}

func (s *Server) UninstallSkill(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	name := chi.URLParam(r, "name")
	projectID := r.URL.Query().Get("project_id")

	// Resolve the install path before deleting from SQLite (we need the path)
	installDir, dirErr := s.skillInstallDir(name, projectID)

	// Remove from SQLite
	if err := s.LocalStore.DeleteInstalledSkill(name, projectID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Remove the installed directory if we were able to determine it
	if dirErr == nil && installDir != "" {
		if err := os.RemoveAll(installDir); err != nil {
			log.Printf("[marketplace] warning: failed to remove skill dir %s: %v", installDir, err)
		}
	}

	log.Printf("[marketplace] uninstalled skill: %s (project=%q)", name, projectID)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Local plugin install/uninstall (project-scoped, for --plugin-dir injection)
// ---------------------------------------------------------------------------

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

// InstallPluginEntryRequest is the body for POST /api/marketplace/installed-plugins.
type InstallPluginEntryRequest struct {
	Name        string `json:"name"`
	InstallPath string `json:"install_path"` // absolute path to the plugin dir (in marketplace_dir)
	ProjectID   string `json:"project_id"`   // "" = global
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
	if req.Name == "" || req.InstallPath == "" {
		writeError(w, http.StatusBadRequest, "name and install_path are required")
		return
	}
	// Verify the install_path exists on disk
	if _, err := os.Stat(req.InstallPath); os.IsNotExist(err) {
		writeError(w, http.StatusBadRequest, "install_path does not exist: "+req.InstallPath)
		return
	}
	entry := store.InstalledPluginEntry{
		Name:        req.Name,
		ProjectID:   req.ProjectID,
		InstallPath: req.InstallPath,
		InstalledAt: time.Now(),
	}
	if err := s.LocalStore.UpsertInstalledPluginEntry(entry); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("[marketplace] installed plugin: %s → %s (project=%q)", req.Name, req.InstallPath, req.ProjectID)
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) UninstallPluginEntry(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	name := chi.URLParam(r, "name")
	projectID := r.URL.Query().Get("project_id")
	if err := s.LocalStore.DeleteInstalledPluginEntry(name, projectID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("[marketplace] uninstalled plugin: %s (project=%q)", name, projectID)
	w.WriteHeader(http.StatusNoContent)
}

// resolveInstalledPluginDirs returns the list of plugin directory paths for a project.
func (s *Server) resolveInstalledPluginDirs(projectID string) []string {
	if s.LocalStore == nil {
		return nil
	}
	entries, err := s.LocalStore.ListInstalledPluginEntries(projectID)
	if err != nil {
		log.Printf("[marketplace] failed to load installed plugins: %v", err)
		return nil
	}
	var dirs []string
	seen := make(map[string]bool)
	for _, e := range entries {
		if e.InstallPath != "" && !seen[e.InstallPath] {
			seen[e.InstallPath] = true
			dirs = append(dirs, e.InstallPath)
		}
	}
	return dirs
}

// ---------------------------------------------------------------------------
// Skill helpers
// ---------------------------------------------------------------------------

// skillInstallDir returns the absolute path where a skill should be installed.
// For project-specific: {project.GitRepoPath}/.claude/skills/{name}/
// For global: {dataDir}/global-skills/{name}/
func (s *Server) skillInstallDir(name, projectID string) (string, error) {
	if projectID == "" {
		// Global: stored under the data directory
		return filepath.Join(s.DataDir, "global-skills", name), nil
	}
	// Project-specific: under the project's git repo
	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		return "", fmt.Errorf("project not found: %s", projectID)
	}
	if project.GitRepoPath == "" {
		return "", fmt.Errorf("project has no git repo path")
	}
	return filepath.Join(project.GitRepoPath, ".claude", "skills", name), nil
}

// copySkillDir copies a skill directory from src to dst. Any existing content in
// dst is removed first.
func copySkillDir(src, dst string) error {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return fmt.Errorf("source path does not exist: %s", src)
	}
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("clean install dir: %w", err)
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return fmt.Errorf("create install dir: %w", err)
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

// downloadAndExtractSkill downloads the skill tar.gz from the remote marketplace
// and extracts it into destDir. Any existing content in destDir is removed first
// to prevent stale files from a previous version persisting after reinstall.
func (s *Server) downloadAndExtractSkill(packageID, version, destDir string) error {
	// Remove existing content so reinstalls don't leave stale files from prior versions.
	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("clean install dir: %w", err)
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("create install dir: %w", err)
	}

	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		defer pw.Close()
		_, err := s.Cloud.DownloadSkillTar(packageID, version, pw)
		errCh <- err
	}()

	if err := extractTarGz(pr, destDir); err != nil {
		pr.CloseWithError(err)
		return fmt.Errorf("extract tar.gz: %w", err)
	}
	if err := <-errCh; err != nil {
		return fmt.Errorf("download: %w", err)
	}
	return nil
}

// extractTarGz extracts a .tar.gz stream into destDir, stripping the top-level directory.
func extractTarGz(r io.Reader, destDir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		// Strip leading directory component (e.g. "skill-name-1.0.0/file" → "file").
		// Use strings.SplitN on the slash-separated path, NOT filepath.SplitList
		// which splits on the OS path-list separator (colon on Unix).
		cleanName := filepath.ToSlash(hdr.Name)
		cleanName = strings.TrimPrefix(cleanName, "./")
		parts := strings.SplitN(cleanName, "/", 2)
		var relPath string
		if len(parts) == 2 && parts[1] != "" {
			relPath = filepath.FromSlash(parts[1])
		} else {
			// top-level directory entry itself — skip
			continue
		}
		relPath = filepath.Clean(relPath)
		if relPath == "." || relPath == "" {
			continue
		}
		target := filepath.Join(destDir, relPath)
		// Guard against path traversal
		if !isSubPath(destDir, target) {
			log.Printf("[marketplace] skip suspicious tar entry: %s", hdr.Name)
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

func isSubPath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	// Accept "." (parent == child) and any relative path that doesn't escape parent
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// copyGlobalSkillsToProject copies all globally installed skills into a project on creation.
func (s *Server) copyGlobalSkillsToProject(gitRepoPath string) {
	globalSkills, err := s.LocalStore.ListInstalledSkills("")
	if err != nil {
		log.Printf("[marketplace] failed to load global skills for new project: %v", err)
		return
	}
	for _, skill := range globalSkills {
		if skill.InstallPath == "" {
			continue
		}
		dest := filepath.Join(gitRepoPath, ".claude", "skills", skill.Name)
		if err := copyDir(skill.InstallPath, dest); err != nil {
			log.Printf("[marketplace] failed to copy global skill %s to new project: %v", skill.Name, err)
		}
	}
}

// ensureAgentsSymlink creates a .agents -> .claude symlink at the repo root so that
// agents like Codex (which look for .agents/) can find skills installed under .claude/.
// If .agents already exists as a correct symlink the call is a no-op.
func ensureAgentsSymlink(repoPath string) {
	link := filepath.Join(repoPath, ".agents")
	info, err := os.Lstat(link)
	if err == nil {
		// Already exists — verify it's the right symlink, otherwise leave it alone.
		if info.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(link)
			if target == ".claude" {
				return // already correct
			}
		}
		// Something else lives here; don't overwrite.
		return
	}
	if !os.IsNotExist(err) {
		log.Printf("[marketplace] ensureAgentsSymlink: lstat %s: %v", link, err)
		return
	}
	if err := os.Symlink(".claude", link); err != nil {
		log.Printf("[marketplace] ensureAgentsSymlink: symlink %s -> .claude: %v", link, err)
	}
}

// copyGlobalSkillToProjects copies a global skill into all projects that have a git repo path.
func (s *Server) copyGlobalSkillToProjects(skillName, srcDir string) {
	projects, err := s.Store.ListProjects()
	if err != nil {
		log.Printf("[marketplace] failed to list projects for global skill copy: %v", err)
		return
	}
	// In cloud/remote mode, git_repo_path is stored in local config (not persisted to the remote store).
	// Patch it in here, mirroring what the ListProjects API handler does.
	if s.StoreMode == "remote" && s.Config != nil {
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
	var wg sync.WaitGroup
	for _, p := range projects {
		if p.GitRepoPath == "" {
			continue
		}
		wg.Add(1)
		go func(p store.Project) {
			defer wg.Done()
			dest := filepath.Join(p.GitRepoPath, ".claude", "skills", skillName)
			if err := copyDir(srcDir, dest); err != nil {
				log.Printf("[marketplace] failed to copy global skill %s to project %s: %v", skillName, p.ID, err)
			} else {
				log.Printf("[marketplace] copied global skill %s to %s", skillName, dest)
				ensureAgentsSymlink(p.GitRepoPath)
			}
		}(p)
	}
	wg.Wait()
}

// autoInstallMCPDeps parses mcp_dependencies from the skill manifest and auto-installs
// any MCP packages that are not already installed. The MCP is installed into the same
// project scope as the skill (projectID). Non-fatal: errors are only logged.
func (s *Server) autoInstallMCPDeps(detail *store.MarketplacePackageDetail, version string, projectID string) {
	if s.Cloud == nil || s.LocalStore == nil {
		return
	}
	// Find the matching version manifest
	var manifest string
	for _, v := range detail.Versions {
		if v.Version == version {
			manifest = v.Manifest
			break
		}
	}
	if manifest == "" {
		return
	}

	// Parse mcp_dependencies: ["pkg-name-1", "pkg-name-2"]
	var parsed struct {
		MCPDeps []string `json:"mcp_dependencies"`
	}
	if err := json.Unmarshal([]byte(manifest), &parsed); err != nil || len(parsed.MCPDeps) == 0 {
		return
	}

	// A dep is satisfied if it is installed in the target project scope OR globally (project_id="").
	// Using name-only dedup would incorrectly skip deps that are only installed in a different project.
	installed, _ := s.LocalStore.ListInstalledMCPs("*")
	installedForScope := make(map[string]bool, len(installed))
	for _, m := range installed {
		if m.ProjectID == projectID || m.ProjectID == "" {
			installedForScope[m.Name] = true
		}
	}

	for _, depName := range parsed.MCPDeps {
		if installedForScope[depName] {
			continue // already installed in this scope or globally
		}
		// Find the dependency package in the marketplace
		pkgs, err := s.Cloud.ListMarketplacePackages("mcp", depName, "", "", 5, 0)
		if err != nil {
			log.Printf("[marketplace] dep auto-install: search %q failed: %v", depName, err)
			continue
		}
		var depPkg *store.MarketplacePackage
		for i := range pkgs {
			if pkgs[i].Name == depName {
				depPkg = &pkgs[i]
				break
			}
		}
		if depPkg == nil {
			log.Printf("[marketplace] dep auto-install: package %q not found in marketplace", depName)
			continue
		}
		depDetail, err := s.Cloud.GetMarketplacePackage(depPkg.ID)
		if err != nil || len(depDetail.Versions) == 0 {
			log.Printf("[marketplace] dep auto-install: fetch detail for %q failed: %v", depName, err)
			continue
		}
		// Use the latest version (first in list, sorted desc by server)
		latestVer := depDetail.Versions[0]
		var mcpEntry agentpkg.MCPServerEntry
		if err := json.Unmarshal([]byte(latestVer.Manifest), &mcpEntry); err != nil {
			log.Printf("[marketplace] dep auto-install: invalid manifest for %q: %v", depName, err)
			continue
		}
		mcpKey := mcpEntry.Key
		if mcpKey == "" {
			mcpKey = depName
		}
		mcp := store.InstalledMCP{
			Name:        depName,
			ProjectID:   projectID,
			MCPKey:      mcpKey,
			Manifest:    latestVer.Manifest,
			EnvValues:   "{}",
			InstalledAt: time.Now(),
		}
		if err := s.LocalStore.UpsertInstalledMCP(mcp); err != nil {
			log.Printf("[marketplace] dep auto-install: store %q failed: %v", depName, err)
			continue
		}
		if s.MarketplaceStats != nil {
			s.MarketplaceStats.RecordInstall(depName, "mcp")
		}
		log.Printf("[marketplace] dep auto-install: installed MCP %q (key=%s) as dependency", depName, mcpKey)
	}
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
