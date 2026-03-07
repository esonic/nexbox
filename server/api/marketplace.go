package api

import (
	"archive/tar"
	"bytes"
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
		// Remote package registry (proxied from cloud)
		r.Get("/packages", s.ListMarketplacePackages)
		r.Post("/packages", s.PublishPackage)
		r.Get("/packages/{packageId}", s.GetMarketplacePackage)
		r.Patch("/packages/{packageId}", s.UpdateMarketplacePackage)
		r.Delete("/packages/{packageId}", s.DeleteMarketplacePackage)
		r.Post("/packages/{packageId}/versions", s.PublishPackageVersion)
		r.Post("/packages/{packageId}/skill-dir-upload", s.UploadSkillDir)
		r.Get("/packages/{packageId}/star", s.GetPackageStarStatus)
		r.Post("/packages/{packageId}/star", s.StarPackage)
		r.Delete("/packages/{packageId}/star", s.UnstarPackage)
		r.Get("/packages/{packageId}/reviews", s.ListPackageReviews)
		r.Post("/packages/{packageId}/reviews", s.AddPackageReview)
		r.Delete("/packages/{packageId}/reviews/{reviewId}", s.DeletePackageReview)

		// Local install/uninstall
		r.Get("/installed-mcps", s.ListInstalledMCPs)
		r.Post("/installed-mcps", s.InstallMCP)
		r.Delete("/installed-mcps/{name}", s.UninstallMCP)

		r.Get("/installed-skills", s.ListInstalledSkills)
		r.Post("/installed-skills", s.InstallSkill)
		r.Delete("/installed-skills/{name}", s.UninstallSkill)

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

// loadMarketplacePlugins scans dir for *.json files and parses each as a
// MarketplacePlugin. Returns an empty slice (not an error) if dir is empty
// or does not exist.
func loadMarketplacePlugins(dir string) ([]store.MarketplacePlugin, error) {
	if dir == "" {
		return []store.MarketplacePlugin{}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []store.MarketplacePlugin{}, nil
		}
		return nil, fmt.Errorf("read marketplace dir: %w", err)
	}
	var plugins []store.MarketplacePlugin
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		fPath := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(fPath)
		if err != nil {
			log.Printf("[marketplace] skip %s: read error: %v", entry.Name(), err)
			continue
		}
		var p store.MarketplacePlugin
		if err := json.Unmarshal(data, &p); err != nil {
			log.Printf("[marketplace] skip %s: parse error: %v", entry.Name(), err)
			continue
		}
		// Derive ID from filename if not set in the JSON.
		if p.ID == "" {
			p.ID = strings.TrimSuffix(entry.Name(), ".json")
		}
		plugins = append(plugins, p)
	}
	if plugins == nil {
		plugins = []store.MarketplacePlugin{}
	}
	return plugins, nil
}

// ListLocalPlugins handles GET /api/marketplace/plugins
func (s *Server) ListLocalPlugins(w http.ResponseWriter, r *http.Request) {
	dir := ""
	if s.Config != nil {
		dir = s.Config.Get().MarketplaceDir
	}
	plugins, err := loadMarketplacePlugins(dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plugins)
}

// GetLocalPlugin handles GET /api/marketplace/plugins/{id}
func (s *Server) GetLocalPlugin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dir := ""
	if s.Config != nil {
		dir = s.Config.Get().MarketplaceDir
	}
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
		writeError(w, http.StatusServiceUnavailable, "marketplace requires cloud mode")
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

const (
	maxSkillTarSize        = 1 << 20        // 1 MB (compressed tar.gz)
	maxSkillRawSize        = 20 << 20       // 20 MB (uncompressed total)
	maxSkillFileCount      = 100
)

// UploadSkillDir reads a local directory path from the JSON body, walks the
// directory on the server filesystem, checks for SKILL.md, packs everything
// into a tar.gz in-memory, validates the compressed size is under 1 MB, and
// uploads to OSS via the remote marketplace.
// The request body is { "dir": "/absolute/path/to/dir" }.
func (s *Server) UploadSkillDir(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		writeError(w, http.StatusServiceUnavailable, "marketplace requires cloud mode")
		return
	}
	packageID := chi.URLParam(r, "packageId")

	var req struct {
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	dirPath := filepath.Clean(req.Dir)
	if dirPath == "" || req.Dir == "" {
		writeError(w, http.StatusBadRequest, "dir is required")
		return
	}

	info, err := os.Stat(dirPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "directory not found: "+req.Dir)
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, req.Dir+" is not a directory")
		return
	}

	// Walk directory and collect entries.
	type entry struct {
		abs  string
		rel  string
		size int64
	}
	var entries []entry
	var totalSize int64

	walkErr := filepath.Walk(dirPath, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dirPath, path)
		if err != nil {
			return err
		}
		if len(entries) >= maxSkillFileCount {
			return fmt.Errorf("too many files: max is %d", maxSkillFileCount)
		}
		totalSize += fi.Size()
		entries = append(entries, entry{abs: path, rel: filepath.ToSlash(rel), size: fi.Size()})
		return nil
	})
	if walkErr != nil {
		writeError(w, http.StatusBadRequest, walkErr.Error())
		return
	}
	if len(entries) == 0 {
		writeError(w, http.StatusBadRequest, "directory is empty")
		return
	}
	// A valid skill directory must contain a SKILL.md at the top level.
	// Also read its content to embed in the manifest for display in the detail page.
	var skillMdContent string
	for _, e := range entries {
		if e.rel == "SKILL.md" {
			if b, err := os.ReadFile(e.abs); err == nil {
				skillMdContent = string(b)
			}
			break
		}
	}
	if skillMdContent == "" {
		writeError(w, http.StatusBadRequest, "directory must contain a SKILL.md file at the top level")
		return
	}

	// Reject before allocating the buffer: check uncompressed total size.
	if totalSize > maxSkillRawSize {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("uncompressed size %d MB exceeds %d MB limit", totalSize>>20, maxSkillRawSize>>20))
		return
	}

	// Pack into tar.gz in memory.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	// extractTarGz strips the first path component, so all entries must be nested
	// under a top-level directory (e.g. "inspect/SKILL.md" not "SKILL.md").
	tarPrefix := filepath.Base(dirPath) + "/"
	for _, e := range entries {
		f, err := os.Open(e.abs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to open: "+e.rel)
			return
		}
		hdr := &tar.Header{
			Name:    tarPrefix + e.rel,
			Size:    e.size,
			Mode:    0644,
			ModTime: time.Now(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			f.Close()
			writeError(w, http.StatusInternalServerError, "tar header error")
			return
		}
		if _, err := io.Copy(tw, f); err != nil {
			f.Close()
			writeError(w, http.StatusInternalServerError, "tar write error: "+e.rel)
			return
		}
		f.Close()
	}
	if err := tw.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to finalize tar archive")
		return
	}
	if err := gw.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to finalize gzip stream")
		return
	}

	tarSize := int64(buf.Len())
	if tarSize > maxSkillTarSize {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("compressed archive size %d KB exceeds %d MB limit", tarSize>>10, maxSkillTarSize>>20))
		return
	}
	archiveName := filepath.Base(dirPath)
	ossKey, err := s.Cloud.UploadSkillTar(packageID, archiveName, &buf, tarSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("[marketplace] dir-upload package=%s dir=%s files=%d raw=%d tar=%d oss_key=%s",
		packageID, dirPath, len(entries), totalSize, tarSize, ossKey)
	writeJSON(w, http.StatusOK, map[string]string{"oss_key": ossKey, "skill_md": skillMdContent})
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
	PackageID string `json:"package_id"` // remote package ID (used for download)
	Name      string `json:"name"`       // package name (used as skill directory name)
	Version   string `json:"version"`
	ProjectID string `json:"project_id"` // "" = global
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
	if s.Cloud == nil {
		writeError(w, http.StatusServiceUnavailable, "skill install requires cloud mode")
		return
	}
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	var req InstallSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Version == "" {
		writeError(w, http.StatusBadRequest, "name and version are required")
		return
	}
	if req.PackageID == "" {
		writeError(w, http.StatusBadRequest, "package_id is required")
		return
	}

	// Determine install path
	installDir, err := s.skillInstallDir(req.Name, req.ProjectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Fetch package detail to resolve mcp_dependencies before downloading the tar
	detail, err := s.Cloud.GetMarketplacePackage(req.PackageID)
	if err != nil {
		log.Printf("[marketplace] failed to fetch package detail for skill %s: %v", req.Name, err)
		// Non-fatal: proceed without dependency auto-install
	} else {
		s.autoInstallMCPDeps(detail, req.Version, req.ProjectID)
	}

	// Download tar.gz from remote and extract
	if err := s.downloadAndExtractSkill(req.PackageID, req.Version, installDir); err != nil {
		log.Printf("[marketplace] failed to install skill %s: %v", req.Name, err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to install skill: %v", err))
		return
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
