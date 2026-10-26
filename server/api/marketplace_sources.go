package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/esonic/nexbox/mcpidentity"
	"github.com/esonic/nexbox/store"
	"github.com/go-chi/chi/v5"
)

// ---------------------------------------------------------------------------
// Marketplace sources (Add Marketplace)
// ---------------------------------------------------------------------------

type AddMarketplaceSourceRequest struct {
	Input string `json:"input"` // local path or git URL (auto-detected)
}

type AddManualMCPSourceRequest struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

var (
	manualMCPHeaderNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	manualMCPNamePattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,39}$`)
)

// AddManualMCPSource creates a minimal local marketplace entry for one remote
// MCP. It deliberately does not install the MCP; installation continues through
// the normal marketplace card flow, including project selection.
func (s *Server) AddManualMCPSource(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	var req AddManualMCPSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if !manualMCPNamePattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, "name must be 1-40 characters, start with a letter or number, and contain only letters, numbers, hyphens, or underscores")
		return
	}
	rawURL := strings.TrimSpace(req.URL)
	parsedURL, err := url.Parse(rawURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		writeError(w, http.StatusBadRequest, "url must be a valid http:// or https:// URL")
		return
	}
	if parsedURL.Fragment != "" {
		writeError(w, http.StatusBadRequest, "url must not contain a fragment")
		return
	}
	if parsedURL.User != nil {
		writeError(w, http.StatusBadRequest, "url must not contain credentials; use headers instead")
		return
	}
	parsedURL.Scheme = strings.ToLower(parsedURL.Scheme)
	parsedURL.Host = strings.ToLower(parsedURL.Host)
	rawURL = parsedURL.String()

	headers := make(map[string]string, len(req.Headers))
	seenHeaders := make(map[string]bool, len(req.Headers))
	for name, value := range req.Headers {
		name = strings.TrimSpace(name)
		if !manualMCPHeaderNamePattern.MatchString(name) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid header name %q", name))
			return
		}
		lowerName := strings.ToLower(name)
		if seenHeaders[lowerName] {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("duplicate header name %q", name))
			return
		}
		if strings.ContainsAny(value, "\r\n") {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("header %q must be a single line", name))
			return
		}
		seenHeaders[lowerName] = true
		headers[name] = value
	}

	mpDir := s.marketplaceDir()
	if mpDir == "" {
		writeError(w, http.StatusInternalServerError, "marketplace directory not configured")
		return
	}
	if err := os.MkdirAll(mpDir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "create marketplace dir: "+err.Error())
		return
	}

	hash := sha256.Sum256([]byte(rawURL))
	shortHash := hex.EncodeToString(hash[:6])
	sourceID := "mcp-" + shortHash
	sourceDirName := "manual-" + sourceID
	sourceDir := filepath.Join(mpDir, sourceDirName)
	existingPlugins, err := loadMarketplacePlugins(mpDir, sourcesByPath(s.Store))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load marketplace: "+err.Error())
		return
	}
	currentSourceOwnsName := false
	for _, plugin := range existingPlugins {
		if plugin.Type == "mcp" && strings.EqualFold(plugin.Name, name) {
			if plugin.InstallPath == sourceDir {
				currentSourceOwnsName = true
				continue
			}
			writeError(w, http.StatusConflict, fmt.Sprintf("an MCP named %q already exists", name))
			return
		}
	}
	installedMCPs, err := s.Store.ListInstalledMCPs("*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load installed MCPs: "+err.Error())
		return
	}
	for _, installed := range installedMCPs {
		if strings.EqualFold(installed.Name, name) && !currentSourceOwnsName {
			writeError(w, http.StatusConflict, fmt.Sprintf("an installed MCP named %q already exists", name))
			return
		}
	}
	pluginDir := filepath.Join(sourceDir, ".claude-plugin")
	if err := os.MkdirAll(pluginDir, 0700); err != nil {
		writeError(w, http.StatusInternalServerError, "create MCP source: "+err.Error())
		return
	}
	if err := os.Chmod(sourceDir, 0700); err != nil {
		writeError(w, http.StatusInternalServerError, "secure MCP source: "+err.Error())
		return
	}
	if err := os.Chmod(pluginDir, 0700); err != nil {
		writeError(w, http.StatusInternalServerError, "secure MCP source: "+err.Error())
		return
	}
	// Headers can contain credentials, so both generated files are private.
	pluginData, err := json.MarshalIndent(map[string]string{
		"name":        name,
		"description": rawURL,
	}, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode MCP source: "+err.Error())
		return
	}
	mcpData, err := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{
			mcpidentity.ServerKey(name): map[string]any{
				"url":     rawURL,
				"headers": headers,
			},
		},
	}, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode MCP source: "+err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), pluginData, 0600); err != nil {
		writeError(w, http.StatusInternalServerError, "write MCP source: "+err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(sourceDir, ".mcp.json"), mcpData, 0600); err != nil {
		writeError(w, http.StatusInternalServerError, "write MCP source: "+err.Error())
		return
	}
	if err := os.Chmod(filepath.Join(pluginDir, "plugin.json"), 0600); err != nil {
		writeError(w, http.StatusInternalServerError, "secure MCP source: "+err.Error())
		return
	}
	if err := os.Chmod(filepath.Join(sourceDir, ".mcp.json"), 0600); err != nil {
		writeError(w, http.StatusInternalServerError, "secure MCP source: "+err.Error())
		return
	}

	src := store.MarketplaceSource{
		ID:      sourceID,
		Type:    "mcp",
		URL:     rawURL,
		Path:    sourceDir,
		Name:    sourceDirName,
		AddedAt: store.UnixTime(time.Now()),
	}
	if err := s.Store.UpsertMarketplaceSource(src); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("[marketplace] added manual MCP source: %s", rawURL)
	writeJSON(w, http.StatusCreated, src)
}

// hasValidPluginContent checks whether a directory contains any recognized plugin format.
func hasValidPluginContent(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "marketplace.json")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); err == nil {
		return true
	}
	// Also accept repos containing SKILL.md files (e.g., anthropics/skills).
	found := false
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && info.Name() == "SKILL.md" {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// isGitURL returns true if the input looks like a git repository URL.
func isGitURL(input string) bool {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "git@") {
		return true
	}
	if strings.HasPrefix(input, "https://") || strings.HasPrefix(input, "http://") {
		// Ends with .git, or contains a known git host
		if strings.HasSuffix(input, ".git") {
			return true
		}
		lower := strings.ToLower(input)
		if strings.Contains(lower, "github.com/") || strings.Contains(lower, "gitlab.com/") || strings.Contains(lower, "gitlab.") {
			return true
		}
	}
	return false
}

// repoNameFromURL extracts a directory name from a git URL.
func repoNameFromURL(gitURL string) string {
	gitURL = strings.TrimSpace(gitURL)
	// git@github.com:org/repo.git → repo
	if idx := strings.LastIndex(gitURL, "/"); idx >= 0 {
		name := gitURL[idx+1:]
		name = strings.TrimSuffix(name, ".git")
		return name
	}
	if idx := strings.LastIndex(gitURL, ":"); idx >= 0 {
		name := gitURL[idx+1:]
		name = strings.TrimSuffix(name, ".git")
		if slash := strings.LastIndex(name, "/"); slash >= 0 {
			name = name[slash+1:]
		}
		return name
	}
	return "marketplace-source"
}

func (s *Server) AddMarketplaceSource(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	var req AddMarketplaceSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	input := strings.TrimSpace(req.Input)
	if input == "" {
		writeError(w, http.StatusBadRequest, "input is required")
		return
	}

	mpDir := s.marketplaceDir()
	if mpDir == "" {
		writeError(w, http.StatusInternalServerError, "marketplace directory not configured")
		return
	}
	if err := os.MkdirAll(mpDir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "create marketplace dir: "+err.Error())
		return
	}

	hash := sha256.Sum256([]byte(input))
	srcID := hex.EncodeToString(hash[:8])

	if isGitURL(input) {
		repoName := repoNameFromURL(input)
		destDir := filepath.Join(mpDir, repoName)
		if _, err := os.Stat(destDir); err == nil {
			writeError(w, http.StatusConflict, fmt.Sprintf("directory %s already exists in marketplace", repoName))
			return
		}
		cmd := exec.Command("git", "clone", "--depth", "1", input, destDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			writeError(w, http.StatusBadGateway, fmt.Sprintf("git clone failed: %s\n%s", err, output))
			return
		}

		// Validate: must contain at least one recognized plugin format.
		if !hasValidPluginContent(destDir) {
			_ = os.RemoveAll(destDir)
			writeError(w, http.StatusBadRequest, "repository does not contain any recognized format (.claude-plugin/marketplace.json, .claude-plugin/plugin.json, or subdirectories with SKILL.md)")
			return
		}

		src := store.MarketplaceSource{
			ID:      srcID,
			Type:    "git",
			URL:     input,
			Path:    destDir,
			Name:    repoName,
			AddedAt: store.UnixTime(time.Now()),
		}
		if err := s.Store.UpsertMarketplaceSource(src); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[marketplace] added git source: %s → %s", input, destDir)
		writeJSON(w, http.StatusCreated, src)
	} else {
		// Local directory
		absPath, err := filepath.Abs(input)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid path: "+err.Error())
			return
		}
		info, err := os.Stat(absPath)
		if err != nil || !info.IsDir() {
			writeError(w, http.StatusBadRequest, "path is not a valid directory")
			return
		}

		// Validate: must contain at least one recognized plugin format.
		if !hasValidPluginContent(absPath) {
			writeError(w, http.StatusBadRequest, "directory does not contain any recognized format (.claude-plugin/marketplace.json, .claude-plugin/plugin.json, or subdirectories with SKILL.md)")
			return
		}

		// Symlink into marketplace dir
		linkName := filepath.Base(absPath)
		linkPath := filepath.Join(mpDir, linkName)
		if _, err := os.Lstat(linkPath); err == nil {
			writeError(w, http.StatusConflict, fmt.Sprintf("directory %s already exists in marketplace", linkName))
			return
		}
		if err := os.Symlink(absPath, linkPath); err != nil {
			writeError(w, http.StatusInternalServerError, "symlink: "+err.Error())
			return
		}

		src := store.MarketplaceSource{
			ID:      srcID,
			Type:    "local",
			URL:     "",
			Path:    absPath,
			Name:    linkName,
			AddedAt: store.UnixTime(time.Now()),
		}
		if err := s.Store.UpsertMarketplaceSource(src); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[marketplace] added local source: %s → %s", absPath, linkPath)
		writeJSON(w, http.StatusCreated, src)
	}
}

func (s *Server) ListMarketplaceSources(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusOK, []store.MarketplaceSource{})
		return
	}
	sources, err := s.Store.ListMarketplaceSources()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sources)
}

func (s *Server) RemoveMarketplaceSource(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	id := chi.URLParam(r, "id")

	// Look up source before deleting so we can clean up the filesystem.
	src, _ := s.Store.GetMarketplaceSource(id)

	// Uninstall everything this source provided before its files disappear.
	if src != nil {
		s.uninstallSourceItems(*src)
	}

	if err := s.Store.DeleteMarketplaceSource(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Clean up filesystem: remove symlink (local) or cloned directory (git).
	if src != nil && src.Name != "" {
		mpDir := s.marketplaceDir()
		linkOrDir := filepath.Join(mpDir, src.Name)
		if fi, err := os.Lstat(linkOrDir); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(linkOrDir)
				log.Printf("[marketplace] removed symlink: %s", linkOrDir)
			} else if fi.IsDir() {
				_ = os.RemoveAll(linkOrDir)
				log.Printf("[marketplace] removed directory: %s", linkOrDir)
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// uninstallSourceItems uninstalls every MCP, skill and plugin offered by src,
// skipping names that another directory in the marketplace dir still provides.
func (s *Server) uninstallSourceItems(src store.MarketplaceSource) {
	type key struct{ kind, name string }
	kindOf := func(p MarketplacePlugin) string {
		switch p.Type {
		case "mcp", "skill":
			return p.Type
		default:
			return "plugin"
		}
	}
	scan := func(path string) (map[key]bool, error) {
		plugins, err := loadMarketplacePlugins(path, nil)
		if err != nil {
			return nil, err
		}
		keys := map[key]bool{}
		for _, p := range plugins {
			keys[key{kindOf(p), p.Name}] = true
		}
		return keys, nil
	}

	owned, err := scan(src.Path)
	if err != nil {
		log.Printf("[marketplace] list plugins of source %s: %v", src.Name, err)
		return
	}
	// Protect names that any other directory under the marketplace dir still
	// provides (registered or not). On any error, uninstall nothing.
	mpDir := s.marketplaceDir()
	entries, err := os.ReadDir(mpDir)
	if err != nil {
		log.Printf("[marketplace] read marketplace dir while removing source %s: %v", src.Name, err)
		return
	}
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	self := resolve(src.Path)
	for _, e := range entries {
		other := filepath.Join(mpDir, e.Name())
		if strings.HasPrefix(e.Name(), ".") || resolve(other) == self {
			continue
		}
		keys, err := scan(other)
		if err != nil {
			log.Printf("[marketplace] scan %s while removing source %s: %v", other, src.Name, err)
			return
		}
		for k := range keys {
			delete(owned, k)
		}
	}
	for k := range owned {
		var err error
		switch k.kind {
		case "mcp":
			err = s.uninstallMCP(k.name, "")
		case "skill":
			err = s.uninstallSkill(k.name, "")
		default:
			err = s.uninstallPluginEntry(k.name, "")
		}
		if err != nil {
			log.Printf("[marketplace] uninstall %s %q for removed source %s: %v", k.kind, k.name, src.Name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Startup: clone official marketplace repos
// ---------------------------------------------------------------------------

// CloneOfficialMarketplaceRepos ensures the marketplace directory exists so
// users can add their own sources later. The previous default source pointed
// to a deployment-specific private repository; NexBox now ships with no preconfigured
// marketplace.
func CloneOfficialMarketplaceRepos(dataDir string, ls *store.SQLiteStore) {
	mpDir := filepath.Join(dataDir, "marketplace")
	if err := os.MkdirAll(mpDir, 0755); err != nil {
		log.Printf("[marketplace] failed to create marketplace dir: %v", err)
	}
}
