package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	agentpkg "gitlab.okg.com/sre_ai/nexbox/agent"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

// ---------------------------------------------------------------------------
// Local MCP install/uninstall
// ---------------------------------------------------------------------------

type InstallMCPRequest struct {
	Name      string            `json:"name"`
	PackageID string            `json:"package_id"`
	Version   string            `json:"version"`
	MCPKey    string            `json:"mcp_key"`
	Manifest  json.RawMessage   `json:"manifest"`
	Env       map[string]string `json:"env"`
	ProjectID string            `json:"project_id"`
	AuthType  string            `json:"auth_type"`
}

func (s *Server) ListInstalledMCPs(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeJSON(w, http.StatusOK, []store.InstalledMCP{})
		return
	}
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

	manifestStr := "{}"
	if len(req.Manifest) > 0 && string(req.Manifest) != "null" {
		raw := string(req.Manifest)
		if len(raw) >= 2 && raw[0] == '"' {
			var inner string
			if json.Unmarshal(req.Manifest, &inner) == nil {
				raw = inner
			}
		}
		manifestStr = raw
	}

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
			if len(cfg.Headers) > 0 {
				entry.Headers = cfg.Headers
			}
		} else {
			entry.Type = "stdio"
			entry.Cmd = cfg.Command
			entry.Args = cfg.Args
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

	envValues := "{}"
	if len(req.Env) > 0 {
		if b, err := json.Marshal(req.Env); err == nil {
			envValues = string(b)
		}
	}

	authType := req.AuthType
	if authType == "" {
		authType = "none"
	}

	mcp := store.InstalledMCP{
		Name:        req.Name,
		ProjectID:   req.ProjectID,
		PackageID:   req.PackageID,
		Version:     req.Version,
		MCPKey:      mcpKey,
		Manifest:    manifestStr,
		EnvValues:   envValues,
		AuthType:    authType,
		InstalledAt: store.UnixTime(time.Now()),
	}
	if err := s.LocalStore.UpsertInstalledMCP(mcp); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if s.MarketplaceStats != nil {
		s.MarketplaceStats.RecordInstall(req.Name, "mcp")
	}
	log.Printf("[marketplace] installed MCP: %s (key=%s, auth=%s)", req.Name, mcpKey, authType)
	writeJSON(w, http.StatusOK, mcp)
}

func (s *Server) UninstallMCP(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	name := chi.URLParam(r, "name")
	projectID := r.URL.Query().Get("project_id")
	all := projectID == "" // no project_id means delete all entries for this name
	if all {
		if err := s.LocalStore.DeleteInstalledMCPByName(name); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = s.LocalStore.DeleteMCPOAuthTokenByName(name)
	} else {
		if err := s.LocalStore.DeleteInstalledMCP(name, projectID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = s.LocalStore.DeleteMCPOAuthToken(name, projectID)
	}
	log.Printf("[marketplace] uninstalled MCP: %s (project=%q, all=%v)", name, projectID, all)
	w.WriteHeader(http.StatusNoContent)
}
