package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	agentpkg "gitlab.okg.com/sre_ai/boxnex/agent"
	"gitlab.okg.com/sre_ai/boxnex/store"
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
		// Skip entries with empty key — these are malformed (e.g. LSP configs
		// accidentally stored as MCP manifests).
		if entry.Key == "" {
			log.Printf("[marketplace] skip MCP %s: manifest has empty key (not a valid MCP entry)", mcp.Name)
			continue
		}

		// For HTTP MCPs that require auth injection (apikey, oauth_discovery, oauth),
		// route through the local MCP proxy so Claude never handles tokens directly.
		if entry.Type == "http" && (mcp.AuthType == "apikey" || mcp.AuthType == "oauth_discovery" || mcp.AuthType == "oauth") && s.ListenPort > 0 {
			entry.URL = fmt.Sprintf("http://127.0.0.1:%d/mcp-proxy/%s", s.ListenPort, mcp.Name)
			entry.Headers = nil // proxy injects auth; don't send stored headers to Claude
		} else if mcp.EnvValues != "" && mcp.EnvValues != "{}" {
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
	// Resolve symlinks so filepath.Walk can traverse the directory.
	// filepath.Walk uses os.Lstat internally and won't follow a symlinked root.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
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

	// Deduplicate by (name, type): later entries (from direct plugin.json) take
	// precedence over earlier entries from parent marketplace.json, which may use
	// the repo owner as a fallback author rather than the plugin's actual author.
	// Keying on type ensures a "frontend-design" skill and a "frontend-design" plugin
	// are treated as distinct entries that appear in different tabs.
	byKey := make(map[string]int, len(plugins)) // "name:type" → index in result
	deduped := plugins[:0:0]
	for _, p := range plugins {
		key := p.Name + ":" + p.Type
		if idx, exists := byKey[key]; exists {
			deduped[idx] = p // overwrite with later (more specific) entry
		} else {
			byKey[key] = len(deduped)
			deduped = append(deduped, p)
		}
	}
	return deduped, nil
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

// loadClaudePlugin parses a .claude-plugin/plugin.json and enriches from the plugin dir.
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
	}
	enrichFromPluginDir(p, pluginRoot)
	return p, nil
}

// marketplaceJSONFile is the schema for .claude-plugin/marketplace.json.
// The source field is polymorphic: a local path string or a remote-git object.
type marketplaceJSONFile struct {
	Name  string `json:"name"`
	Owner struct {
		Name string `json:"name"`
	} `json:"owner"`
	Metadata struct {
		Description string `json:"description"`
		Version     string `json:"version"`
	} `json:"metadata"`
	Plugins []marketplaceJSONEntry `json:"plugins"`
}

type marketplaceJSONEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Version     string          `json:"version"`
	Source      json.RawMessage `json:"source"` // string (local path) or {"source":"url","url":"..."}
	Category    string          `json:"category"`
	Homepage    string          `json:"homepage"`
	Author      struct {
		Name string `json:"name"`
	} `json:"author"`
	Tags       []string        `json:"tags"`
	Skills     []string        `json:"skills"`      // skills-repo: list of relative skill paths
	LSPServers json.RawMessage `json:"lspServers"`  // inline LSP server config
}

// loadMarketplaceJSON parses a .claude-plugin/marketplace.json and returns
// one MarketplacePlugin per entry, with type/components/auth fully detected.
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
		if entry.Name == "" {
			continue
		}
		author := entry.Author.Name
		if author == "" {
			author = mf.Owner.Name
		}
		p := store.MarketplacePlugin{
			ID:          repoName + "-" + entry.Name,
			Name:        entry.Name,
			Description: entry.Description,
			Version:     entry.Version,
			Author:      author,
			Category:    entry.Category,
			Homepage:    entry.Homepage,
			Type:        "plugin",
			Source:      "plugin",
		}

		// LSP servers inline → type=lsp
		if entry.LSPServers != nil && string(entry.LSPServers) != "null" {
			p.Type = "lsp"
			p.LSPConfig = string(entry.LSPServers)
			p.Components = []string{"lsp"}
			result = append(result, p)
			continue
		}

		// Skills-repo format: skills array with local paths — these are install bundles,
		// not individual skills. The walker discovers each SKILL.md individually, so
		// we skip the bundle entry to avoid confusing non-installable aggregate entries.
		if len(entry.Skills) > 0 {
			continue
		}

		// Resolve source field
		if entry.Source == nil || string(entry.Source) == "null" || string(entry.Source) == `""` {
			result = append(result, p)
			continue
		}

		// Try string source first
		var srcStr string
		if json.Unmarshal(entry.Source, &srcStr) == nil {
			if srcStr == "./" || srcStr == "." {
				// Skills repo self-reference already handled above via Skills field
				result = append(result, p)
				continue
			}
			srcPath := filepath.Join(pluginRoot, filepath.FromSlash(strings.TrimPrefix(srcStr, "./")))
			info, err := os.Stat(srcPath)
			if err != nil {
				// Path doesn't exist; keep as plugin without install path
				result = append(result, p)
				continue
			}
			if info.IsDir() {
				enrichFromPluginDir(&p, srcPath)
			} else if strings.HasSuffix(srcPath, ".md") {
				// Direct command file
				p.Type = "plugin"
				p.Components = []string{"commands"}
				p.InstallPath = filepath.Dir(srcPath)
			}
			result = append(result, p)
			continue
		}

		// Try object source {"source": "url", "url": "..."}
		var srcObj struct {
			SrcType string `json:"source"`
			URL     string `json:"url"`
		}
		if json.Unmarshal(entry.Source, &srcObj) == nil && srcObj.SrcType == "url" {
			p.Type = "remote_git"
			p.Source = "remote_git"
			result = append(result, p)
			continue
		}

		result = append(result, p)
	}
	return result, nil
}

// enrichFromPluginDir reads a plugin directory and fills in type, auth, and components.
func enrichFromPluginDir(p *store.MarketplacePlugin, dir string) {
	p.InstallPath = dir
	components := detectPluginComponents(dir)
	p.Components = components

	// Check for .mcp.json
	mcpPath := filepath.Join(dir, ".mcp.json")
	if mcpData, err := os.ReadFile(mcpPath); err == nil {
		p.MCPConfig = string(mcpData)
		p.Type = "mcp"
		p.Source = "connector"
		envVars, authType := parseMCPConfig(mcpData)
		p.EnvVars = envVars
		p.AuthType = authType
		return
	}

	// Check for .lsp.json
	if lspData, err := os.ReadFile(filepath.Join(dir, ".lsp.json")); err == nil {
		p.Type = "lsp"
		p.LSPConfig = string(lspData)
		return
	}

	// Default: plugin (commands/agents/hooks)
	p.Type = "plugin"
	p.Source = "plugin"
}

// detectPluginComponents inspects a plugin directory for its components.
func detectPluginComponents(dir string) []string {
	var components []string
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); err == nil {
		components = append(components, "mcp")
	}
	if _, err := os.Stat(filepath.Join(dir, ".lsp.json")); err == nil {
		components = append(components, "lsp")
	}
	if hasMarkdownFiles(filepath.Join(dir, "commands")) {
		components = append(components, "commands")
	}
	if hasMarkdownFiles(filepath.Join(dir, "agents")) {
		components = append(components, "agents")
	}
	if _, err := os.Stat(filepath.Join(dir, "hooks", "hooks.json")); err == nil {
		components = append(components, "hooks")
	}
	if hasSkillDirs(filepath.Join(dir, "skills")) {
		components = append(components, "skills")
	}
	return components
}

func hasMarkdownFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			return true
		}
	}
	return false
}

func hasSkillDirs(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); err == nil {
				return true
			}
		}
	}
	return false
}

// parseMCPConfig parses a .mcp.json byte slice and extracts:
//   - env_vars: template variable names from ${VAR_NAME} patterns
//   - authType: "oauth" | "apikey" | "oauth_discovery" | "none"
//
// Auth type rules:
//
//	"oauth"           – explicit oauth field (e.g. Slack with clientId)
//	"apikey"          – Authorization header or env var with ${VAR} placeholder
//	"oauth_discovery" – HTTP/SSE URL with no auth headers (server advertises OAuth via RFC 9728)
//	"none"            – stdio subprocess or URL with no auth needed
func parseMCPConfig(data []byte) (envVars []string, authType string) {
	content := string(data)
	varSet := make(map[string]bool)
	for i := 0; i+1 < len(content); i++ {
		if content[i] == '$' && content[i+1] == '{' {
			end := strings.Index(content[i:], "}")
			if end > 0 {
				name := content[i+2 : i+end]
				if name != "" && !strings.ContainsAny(name, " \t\n") {
					varSet[name] = true
				}
			}
		}
	}
	for v := range varSet {
		envVars = append(envVars, v)
	}
	sort.Strings(envVars)

	// Parse JSON to classify per server.
	// Support both {name: cfg} and {mcpServers: {name: cfg}} wrapper.
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		if len(envVars) > 0 {
			return envVars, "apikey"
		}
		return envVars, "none"
	}
	servers := rawMap
	if inner, ok := rawMap["mcpServers"]; ok {
		var innerMap map[string]json.RawMessage
		if json.Unmarshal(inner, &innerMap) == nil {
			servers = innerMap
		}
	}

	for _, raw := range servers {
		var cfg struct {
			Command string                     `json:"command"`
			URL     string                     `json:"url"`
			Headers map[string]json.RawMessage `json:"headers"`
			OAuth   json.RawMessage            `json:"oauth"`
		}
		if json.Unmarshal(raw, &cfg) != nil {
			continue
		}
		if cfg.OAuth != nil && string(cfg.OAuth) != "null" {
			return envVars, "oauth"
		}
		if cfg.URL != "" {
			hasAuthHeader := false
			for h := range cfg.Headers {
				if strings.EqualFold(h, "authorization") || strings.EqualFold(h, "api-key") {
					hasAuthHeader = true
					break
				}
			}
			if hasAuthHeader {
				return envVars, "apikey"
			}
			return envVars, "oauth_discovery"
		}
		// stdio
		if len(envVars) > 0 {
			return envVars, "apikey"
		}
		return envVars, "none"
	}

	if len(envVars) > 0 {
		return envVars, "apikey"
	}
	return envVars, "none"
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
// Local MCP install/uninstall
// ---------------------------------------------------------------------------

// InstallMCPRequest is the body for POST /api/marketplace/installed-mcps.
type InstallMCPRequest struct {
	Name      string            `json:"name"`       // display name / package name
	Version   string            `json:"version"`    // installed version string
	MCPKey    string            `json:"mcp_key"`    // server key (falls back to name)
	Manifest  json.RawMessage   `json:"manifest"`   // MCPServerEntry as JSON object or string
	Env       map[string]string `json:"env"`        // user-provided env secrets (e.g. API tokens)
	ProjectID string            `json:"project_id"` // "" = global, else project-scoped
	AuthType  string            `json:"auth_type"`  // "none","apikey","oauth_discovery","oauth"
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
		Version:     req.Version,
		MCPKey:      mcpKey,
		Manifest:    manifestStr,
		EnvValues:   envValues,
		AuthType:    req.AuthType,
		InstalledAt: time.Now(),
	}
	if err := s.LocalStore.UpsertInstalledMCP(mcp); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
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
	Name        string `json:"name"`                   // package name (used as skill directory name)
	Version     string `json:"version"`
	ProjectID   string `json:"project_id"`             // "" = global
	InstallPath string `json:"install_path"`           // local source path in marketplace_dir
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
	if req.InstallPath == "" {
		writeError(w, http.StatusBadRequest, "install_path is required")
		return
	}

	if req.ProjectID == "" {
		// "All Projects": copy to every project that has a git repo path.
		// One DB record per project is stored; no global record is kept.
		skills := s.installSkillToAllProjects(req.Name, req.Version, req.InstallPath)
		log.Printf("[marketplace] installed skill %s v%s to %d projects", req.Name, req.Version, len(skills))
		writeJSON(w, http.StatusOK, map[string]interface{}{"scope": "all_projects", "installed": len(skills)})
		return
	}

	// Single-project install.
	skill, err := s.installSkillToProject(req.Name, req.Version, req.InstallPath, req.ProjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("[marketplace] installed skill: %s v%s → %s", req.Name, req.Version, skill.InstallPath)
	writeJSON(w, http.StatusOK, skill)
}

func (s *Server) UninstallSkill(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	name := chi.URLParam(r, "name")
	projectID := r.URL.Query().Get("project_id")

	if projectID == "" {
		// "All Projects": remove from every project that has this skill installed.
		all, err := s.LocalStore.ListInstalledSkills("*")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, sk := range all {
			if sk.Name != name {
				continue
			}
			if sk.InstallPath != "" {
				if err := os.RemoveAll(sk.InstallPath); err != nil {
					log.Printf("[marketplace] warning: failed to remove skill dir %s: %v", sk.InstallPath, err)
				}
			}
			if err := s.LocalStore.DeleteInstalledSkill(name, sk.ProjectID); err != nil {
				log.Printf("[marketplace] warning: failed to delete skill record %s/%s: %v", name, sk.ProjectID, err)
			}
		}
		log.Printf("[marketplace] uninstalled skill %s from all projects", name)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Single-project uninstall: look up stored path then delete.
	sk, _ := s.LocalStore.GetInstalledSkill(name, projectID)
	if err := s.LocalStore.DeleteInstalledSkill(name, projectID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sk != nil && sk.InstallPath != "" {
		if err := os.RemoveAll(sk.InstallPath); err != nil {
			log.Printf("[marketplace] warning: failed to remove skill dir %s: %v", sk.InstallPath, err)
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
	LSPConfig   string `json:"lsp_config"`   // inline LSP config JSON (when install_path is not available)
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
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.InstallPath == "" && req.LSPConfig == "" {
		writeError(w, http.StatusBadRequest, "install_path or lsp_config is required")
		return
	}

	if req.ProjectID == "" {
		// "All Projects": install into every project's workspace.
		entries := s.installPluginToAllProjects(req.Name, req.InstallPath, req.LSPConfig)
		log.Printf("[marketplace] installed plugin %s to %d projects", req.Name, len(entries))
		writeJSON(w, http.StatusOK, map[string]interface{}{"scope": "all_projects", "installed": len(entries)})
		return
	}

	// Single-project install: copy files into the project's workspace.
	entry, err := s.installPluginToProject(req.Name, req.InstallPath, req.LSPConfig, req.ProjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("[marketplace] installed plugin: %s → %s (project=%q)", req.Name, entry.InstallPath, req.ProjectID)
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) UninstallPluginEntry(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	name := chi.URLParam(r, "name")
	projectID := r.URL.Query().Get("project_id")

	if projectID == "" {
		// "All Projects": remove from every project that has this plugin installed.
		all, err := s.LocalStore.ListInstalledPluginEntries("*")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, e := range all {
			if e.Name != name {
				continue
			}
			if e.InstallPath != "" {
				if err := os.RemoveAll(e.InstallPath); err != nil {
					log.Printf("[marketplace] warning: failed to remove plugin dir %s: %v", e.InstallPath, err)
				}
			}
			if err := s.LocalStore.DeleteInstalledPluginEntry(name, e.ProjectID); err != nil {
				log.Printf("[marketplace] warning: failed to delete plugin record %s/%s: %v", name, e.ProjectID, err)
			}
		}
		log.Printf("[marketplace] uninstalled plugin %s from all projects", name)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Single-project uninstall: look up stored path then delete.
	entries, _ := s.LocalStore.ListInstalledPluginEntries(projectID)
	for _, e := range entries {
		if e.Name == name && e.ProjectID == projectID {
			if e.InstallPath != "" {
				if err := os.RemoveAll(e.InstallPath); err != nil {
					log.Printf("[marketplace] warning: failed to remove plugin dir %s: %v", e.InstallPath, err)
				}
			}
			break
		}
	}
	if err := s.LocalStore.DeleteInstalledPluginEntry(name, projectID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("[marketplace] uninstalled plugin: %s (project=%q)", name, projectID)
	w.WriteHeader(http.StatusNoContent)
}

// resolveInstalledPluginDirs returns the list of plugin directory paths for a project.
// repoPath is the project's host-side git repo path; when a ContainerMgr is active it is
// used to translate host paths to the container-internal /workspace/... equivalents.
func (s *Server) resolveInstalledPluginDirs(projectID, repoPath string) []string {
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
		if e.InstallPath == "" {
			continue
		}
		p := e.InstallPath
		// In container mode the plugin lives inside the repo which is mounted at
		// /workspace, so translate the host path to the container-visible path.
		if s.ContainerMgr != nil && repoPath != "" && strings.HasPrefix(p, repoPath+"/") {
			p = "/workspace" + p[len(repoPath):]
		}
		if !seen[p] {
			seen[p] = true
			dirs = append(dirs, p)
		}
	}
	return dirs
}

// ---------------------------------------------------------------------------
// Skill / Plugin install helpers
// ---------------------------------------------------------------------------

// installSkillToProject copies the skill from srcPath into the project's
// .claude/skills/{name}/ directory and records the installation in the DB.
func (s *Server) installSkillToProject(name, version, srcPath, projectID string) (store.InstalledSkill, error) {
	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		return store.InstalledSkill{}, fmt.Errorf("project not found: %s", projectID)
	}
	if project.GitRepoPath == "" {
		return store.InstalledSkill{}, fmt.Errorf("project %s has no git repo path", projectID)
	}
	installDir := filepath.Join(project.GitRepoPath, ".claude", "skills", name)
	if err := copySkillDir(srcPath, installDir); err != nil {
		return store.InstalledSkill{}, fmt.Errorf("copy skill: %w", err)
	}
	ensureAgentsSymlink(project.GitRepoPath)
	skill := store.InstalledSkill{
		Name:        name,
		ProjectID:   projectID,
		Version:     version,
		InstallPath: installDir,
		InstalledAt: time.Now(),
	}
	if err := s.LocalStore.UpsertInstalledSkill(skill); err != nil {
		_ = os.RemoveAll(installDir)
		return store.InstalledSkill{}, err
	}
	return skill, nil
}

// installSkillToAllProjects installs a skill into every project that has a git
// repo path.  A separate DB record is created for each project; no global
// record is stored.  Returns the list of successfully installed records.
func (s *Server) installSkillToAllProjects(name, version, srcPath string) []store.InstalledSkill {
	projects, err := s.Store.ListProjects()
	if err != nil {
		log.Printf("[marketplace] installSkillToAllProjects: list projects: %v", err)
		return nil
	}
	var mu sync.Mutex
	var results []store.InstalledSkill
	var wg sync.WaitGroup
	for _, p := range projects {
		if p.GitRepoPath == "" {
			continue
		}
		wg.Add(1)
		go func(p store.Project) {
			defer wg.Done()
			sk, err := s.installSkillToProject(name, version, srcPath, p.ID)
			if err != nil {
				log.Printf("[marketplace] installSkillToAllProjects: project %s: %v", p.ID, err)
				return
			}
			mu.Lock()
			results = append(results, sk)
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	return results
}

// installPluginToProject copies the plugin from srcPath (or writes lspConfig as
// .lsp.json) into the project's .claude/plugins/{name}/ directory and records it.
func (s *Server) installPluginToProject(name, srcPath, lspConfig, projectID string) (store.InstalledPluginEntry, error) {
	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		return store.InstalledPluginEntry{}, fmt.Errorf("project not found: %s", projectID)
	}
	if project.GitRepoPath == "" {
		return store.InstalledPluginEntry{}, fmt.Errorf("project %s has no git repo path", projectID)
	}
	installDir := filepath.Join(project.GitRepoPath, ".claude", "plugins", name)
	if srcPath != "" {
		if err := copyDir(srcPath, installDir); err != nil {
			return store.InstalledPluginEntry{}, fmt.Errorf("copy plugin: %w", err)
		}
	} else if lspConfig != "" {
		if err := os.MkdirAll(installDir, 0755); err != nil {
			return store.InstalledPluginEntry{}, fmt.Errorf("create plugin dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(installDir, ".lsp.json"), []byte(lspConfig), 0644); err != nil {
			return store.InstalledPluginEntry{}, fmt.Errorf("write .lsp.json: %w", err)
		}
	}
	entry := store.InstalledPluginEntry{
		Name:        name,
		ProjectID:   projectID,
		InstallPath: installDir,
		InstalledAt: time.Now(),
	}
	if err := s.LocalStore.UpsertInstalledPluginEntry(entry); err != nil {
		_ = os.RemoveAll(installDir)
		return store.InstalledPluginEntry{}, err
	}
	return entry, nil
}

// installPluginToAllProjects installs a plugin into every project that has a
// git repo path.  Returns the list of successfully installed records.
func (s *Server) installPluginToAllProjects(name, srcPath, lspConfig string) []store.InstalledPluginEntry {
	projects, err := s.Store.ListProjects()
	if err != nil {
		log.Printf("[marketplace] installPluginToAllProjects: list projects: %v", err)
		return nil
	}
	var mu sync.Mutex
	var results []store.InstalledPluginEntry
	var wg sync.WaitGroup
	for _, p := range projects {
		if p.GitRepoPath == "" {
			continue
		}
		wg.Add(1)
		go func(p store.Project) {
			defer wg.Done()
			e, err := s.installPluginToProject(name, srcPath, lspConfig, p.ID)
			if err != nil {
				log.Printf("[marketplace] installPluginToAllProjects: project %s: %v", p.ID, err)
				return
			}
			mu.Lock()
			results = append(results, e)
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	return results
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
