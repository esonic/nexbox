package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/esonic/nexbox/store"
)

// ---------------------------------------------------------------------------
// MarketplacePlugin model (for local directory scanning, NOT persisted)
// ---------------------------------------------------------------------------

// MarketplacePlugin represents a plugin discovered from the marketplace directory.
type MarketplacePlugin struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"`                  // "mcp", "skill", "plugin", "lsp"
	Version     string   `json:"version,omitempty"`
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	EnvVars     []string `json:"env_vars,omitempty"`
	Author      string   `json:"author,omitempty"`
	Source      string   `json:"source,omitempty"`       // "custom", "skill", "plugin", "connector"
	MCPConfig   string   `json:"mcp_config,omitempty"`
	LSPConfig   string   `json:"lsp_config,omitempty"`
	AuthType    string   `json:"auth_type,omitempty"`    // "oauth", "apikey", "oauth_discovery", "none"
	InstallPath string   `json:"install_path,omitempty"`
	Category    string   `json:"category,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
	Components  []string `json:"components,omitempty"`
	SourceLabel string   `json:"source_label,omitempty"` // short display label, e.g. "anthropics/skills"
	SourceFull  string   `json:"source_full,omitempty"`  // full path or git URL for tooltip
}

// ---------------------------------------------------------------------------
// Local directory-based plugin registry
// ---------------------------------------------------------------------------

func (s *Server) marketplaceDir() string {
	if s.DataDir != "" {
		return filepath.Join(s.DataDir, "marketplace")
	}
	return ""
}

func (s *Server) ListLocalPlugins(w http.ResponseWriter, r *http.Request) {
	plugins, err := loadMarketplacePlugins(s.marketplaceDir(), sourcesByPath(s.Store))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plugins)
}

// gitSourceLabel extracts a short human-readable label from a git URL.
// Examples:
//
//	git@github.com:anthropics/skills.git          → anthropics/skills
//	git@gitlab.okg.com:sre/skills/code-review.git → sre/skills/code-review
//	https://github.com/anthropics/skills.git       → anthropics/skills
func gitSourceLabel(url string) string {
	url = strings.TrimSuffix(url, ".git")
	if strings.HasPrefix(url, "git@") {
		// git@host:path/to/repo
		if idx := strings.Index(url, ":"); idx >= 0 {
			return url[idx+1:]
		}
	}
	// https://host/path/to/repo
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		if idx := strings.Index(url[8:], "/"); idx >= 0 {
			return url[8+idx+1:]
		}
	}
	return filepath.Base(url)
}

func (s *Server) GetLocalPlugin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	plugins, err := loadMarketplacePlugins(s.marketplaceDir(), sourcesByPath(s.Store))
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

// sourcesByPath builds a path→source map from the store for use during plugin scanning.
// Returns an empty map (never nil) if the store is unavailable or returns an error.
func sourcesByPath(ls *store.SQLiteStore) map[string]store.MarketplaceSource {
	m := make(map[string]store.MarketplaceSource)
	if ls == nil {
		return m
	}
	sources, err := ls.ListMarketplaceSources()
	if err != nil {
		return m
	}
	for _, src := range sources {
		m[src.Path] = src
	}
	return m
}

// applySourceLabel sets SourceLabel/SourceFull on p based on the sourceRoot directory
// and the registered sources map.
func applySourceLabel(p *MarketplacePlugin, sourceRoot string, byPath map[string]store.MarketplaceSource) {
	if sourceRoot == "" {
		return
	}
	src, ok := byPath[sourceRoot]
	if !ok {
		// Not a registered source (e.g. built-in cloned at startup); use dir name.
		p.SourceLabel = filepath.Base(sourceRoot)
		p.SourceFull = sourceRoot
		return
	}
	if src.Type == "git" && src.URL != "" {
		p.SourceLabel = gitSourceLabel(src.URL)
		p.SourceFull = src.URL
	} else {
		p.SourceLabel = filepath.Base(src.Path)
		p.SourceFull = src.Path
	}
}

// loadMarketplacePlugins scans dir for plugins in three formats:
//  1. .claude-plugin/plugin.json (Anthropic plugins/connectors)
//  2. .claude-plugin/marketplace.json (third-party plugin registries)
//  3. SKILL.md (Anthropic skills)
func loadMarketplacePlugins(dir string, byPath map[string]store.MarketplaceSource) ([]MarketplacePlugin, error) {
	if dir == "" {
		return []MarketplacePlugin{}, nil
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return []MarketplacePlugin{}, nil
	}

	var plugins []MarketplacePlugin
	seen := make(map[string]bool)

	// Collect directories to scan: the root dir plus any symlinked subdirs
	// (filepath.Walk does not follow symlinks, so we resolve them here)
	scanDirs := []string{dir}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			target := filepath.Join(dir, e.Name())
			if resolved, err := filepath.EvalSymlinks(target); err == nil {
				if fi, err := os.Stat(resolved); err == nil && fi.IsDir() {
					scanDirs = append(scanDirs, resolved)
				}
			}
		}
	}

	for _, scanDir := range scanDirs {
	// Determine the source root for plugins found in this scanDir.
	// For symlinked local sources (scanDir != dir), the source root is scanDir itself.
	// For the root marketplace dir (scanDir == dir), each plugin's source root is
	// the immediate child subdirectory of dir that contains it.
	isRootScan := scanDir == dir

	err := filepath.Walk(scanDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() && strings.HasPrefix(info.Name(), ".") && info.Name() != ".claude-plugin" {
			return filepath.SkipDir
		}

		// Compute the source root for this path.
		sourceRoot := scanDir
		if isRootScan {
			// Find the immediate child of dir that contains this path.
			rel, _ := filepath.Rel(dir, path)
			parts := strings.SplitN(rel, string(filepath.Separator), 2)
			if len(parts) >= 1 && parts[0] != "" && parts[0] != "." {
				sourceRoot = filepath.Join(dir, parts[0])
			}
		}

		// .claude-plugin/plugin.json
		if !info.IsDir() && info.Name() == "plugin.json" {
			parentDir := filepath.Dir(path)
			if filepath.Base(parentDir) != ".claude-plugin" {
				return nil
			}
			pluginRoot := filepath.Dir(parentDir)
			p, err := loadClaudePlugin(pluginRoot, path)
			if err != nil {
				log.Printf("[marketplace] skip plugin %s: %v", path, err)
				return nil
			}
			applySourceLabel(p, sourceRoot, byPath)
			if !seen[p.ID] {
				seen[p.ID] = true
				plugins = append(plugins, *p)
			}
			return nil
		}

		// .claude-plugin/marketplace.json
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
			for i := range items {
				applySourceLabel(&items[i], sourceRoot, byPath)
			}
			for _, p := range items {
				if !seen[p.ID] {
					seen[p.ID] = true
					plugins = append(plugins, p)
				}
			}
			return nil
		}

		// SKILL.md
		if !info.IsDir() && info.Name() == "SKILL.md" {
			p, err := loadSkillMD(path)
			if err != nil {
				log.Printf("[marketplace] skip skill %s: %v", path, err)
				return nil
			}
			applySourceLabel(p, sourceRoot, byPath)
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
	} // end scanDirs loop

	if plugins == nil {
		plugins = []MarketplacePlugin{}
	}

	// Deduplicate by (name, type)
	byKey := make(map[string]int, len(plugins))
	deduped := plugins[:0:0]
	for _, p := range plugins {
		key := p.Name + ":" + p.Type
		if idx, exists := byKey[key]; exists {
			deduped[idx] = p
		} else {
			byKey[key] = len(deduped)
			deduped = append(deduped, p)
		}
	}
	return deduped, nil
}

// --- Plugin JSON schemas ---

type claudePluginJSON struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Author      struct {
		Name string `json:"name"`
	} `json:"author"`
}

func loadClaudePlugin(pluginRoot, pluginJSONPath string) (*MarketplacePlugin, error) {
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
	p := &MarketplacePlugin{
		ID:          meta.Name,
		Name:        meta.Name,
		Description: meta.Description,
		Version:     meta.Version,
		Author:      meta.Author.Name,
	}
	enrichFromPluginDir(p, pluginRoot)
	return p, nil
}

type marketplaceJSONFile struct {
	Name  string `json:"name"`
	Owner struct {
		Name string `json:"name"`
	} `json:"owner"`
	Plugins []marketplaceJSONEntry `json:"plugins"`
}

type marketplaceJSONEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Version     string          `json:"version"`
	Source      json.RawMessage `json:"source"`
	Category    string          `json:"category"`
	Homepage    string          `json:"homepage"`
	Author      struct {
		Name string `json:"name"`
	} `json:"author"`
	Skills     []string        `json:"skills"`
	LSPServers json.RawMessage `json:"lspServers"`
}

func loadMarketplaceJSON(pluginRoot, path string) ([]MarketplacePlugin, error) {
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
	var result []MarketplacePlugin
	for _, entry := range mf.Plugins {
		if entry.Name == "" {
			continue
		}
		author := entry.Author.Name
		if author == "" {
			author = mf.Owner.Name
		}
		p := MarketplacePlugin{
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

		if entry.LSPServers != nil && string(entry.LSPServers) != "null" {
			p.Type = "lsp"
			p.LSPConfig = string(entry.LSPServers)
			p.Components = []string{"lsp"}
			// Resolve install_path for --plugin-dir. Only set InstallPath if
			// the source subdirectory has its own .claude-plugin/plugin.json.
			// For pure LSP plugins (no plugin.json), leave InstallPath empty —
			// installPluginToProject will use LSPConfig to write .lsp.json.
			var srcStr string
			if entry.Source != nil && json.Unmarshal(entry.Source, &srcStr) == nil && srcStr != "" && srcStr != "./" && srcStr != "." {
				srcPath := filepath.Join(pluginRoot, filepath.FromSlash(strings.TrimPrefix(srcStr, "./")))
				pluginJSON := filepath.Join(srcPath, ".claude-plugin", "plugin.json")
				if _, err := os.Stat(pluginJSON); err == nil {
					p.InstallPath = srcPath
				}
			}
			result = append(result, p)
			continue
		}

		if len(entry.Skills) > 0 {
			continue
		}

		if entry.Source == nil || string(entry.Source) == "null" || string(entry.Source) == `""` {
			p.InstallPath = pluginRoot
			result = append(result, p)
			continue
		}

		var srcStr string
		if json.Unmarshal(entry.Source, &srcStr) == nil {
			if srcStr == "./" || srcStr == "." {
				p.InstallPath = pluginRoot
				result = append(result, p)
				continue
			}
			srcPath := filepath.Join(pluginRoot, filepath.FromSlash(strings.TrimPrefix(srcStr, "./")))
			info, err := os.Stat(srcPath)
			if err != nil {
				result = append(result, p)
				continue
			}
			if info.IsDir() {
				enrichFromPluginDir(&p, srcPath)
			}
			result = append(result, p)
			continue
		}

		var srcObj struct {
			SrcType string `json:"source"`
			URL     string `json:"url"`
		}
		if json.Unmarshal(entry.Source, &srcObj) == nil && srcObj.SrcType == "url" {
			p.Type = "remote_git"
			p.Source = "remote_git"
		}
		result = append(result, p)
	}
	return result, nil
}

func loadSkillMD(path string) (*MarketplacePlugin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	name, desc := parseSkillFrontmatter(string(data))
	if name == "" {
		name = filepath.Base(filepath.Dir(path))
	}
	p := &MarketplacePlugin{
		ID:          "skill-" + name,
		Name:        name,
		Description: desc,
		Type:        "skill",
		Source:      "skill",
		InstallPath: filepath.Dir(path),
	}
	return p, nil
}

func parseSkillFrontmatter(content string) (name, description string) {
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return "", ""
	}
	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return "", ""
	}
	for _, line := range strings.Split(parts[1], "\n") {
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

func enrichFromPluginDir(p *MarketplacePlugin, dir string) {
	p.InstallPath = dir
	p.Components = detectPluginComponents(dir)

	if mcpData, err := os.ReadFile(filepath.Join(dir, ".mcp.json")); err == nil {
		p.MCPConfig = string(mcpData)
		p.Type = "mcp"
		p.Source = "connector"
		envVars, authType := parseMCPConfig(mcpData)
		p.EnvVars = envVars
		p.AuthType = authType
		return
	}

	if lspData, err := os.ReadFile(filepath.Join(dir, ".lsp.json")); err == nil {
		p.Type = "lsp"
		p.LSPConfig = string(lspData)
		return
	}

	p.Type = "plugin"
	p.Source = "plugin"
}

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
			return envVars, "none"
		}
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
