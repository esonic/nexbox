package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	agentpkg "github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/store"
)

var mcpHeaderVariablePattern = regexp.MustCompile(`\$\{([^{}]+)\}`)

// resolveMCPHeaderTemplates replaces ${VAR} placeholders in header values once,
// when an MCP is installed. The returned headers are the complete runtime
// headers and can be forwarded without guessing which value is a token.
func resolveMCPHeaderTemplates(headers, values map[string]string) (map[string]string, map[string]bool, error) {
	if len(headers) == 0 {
		return nil, nil, nil
	}

	resolved := make(map[string]string, len(headers))
	used := make(map[string]bool)
	missing := make(map[string]bool)
	for name, value := range headers {
		resolved[name] = mcpHeaderVariablePattern.ReplaceAllStringFunc(value, func(match string) string {
			variable := match[2 : len(match)-1]
			if replacement, ok := values[variable]; ok {
				used[variable] = true
				return replacement
			}
			missing[variable] = true
			return match
		})
	}
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for name := range missing {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, nil, fmt.Errorf("missing header values: %v", names)
	}
	return resolved, used, nil
}

// resolveMCPOAuthTemplates resolves the clientId field while preserving any
// other OAuth manifest fields. Marketplace manifests commonly express it as
// ${CLIENT_ID}; keeping that placeholder after install would make OAuth send
// the literal template as the client ID.
func resolveMCPOAuthTemplates(raw json.RawMessage, values map[string]string) (json.RawMessage, map[string]bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return raw, nil, nil
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, nil, fmt.Errorf("invalid OAuth configuration: %w", err)
	}
	clientIDRaw, ok := config["clientId"]
	if !ok {
		return raw, nil, nil
	}
	var clientID string
	if err := json.Unmarshal(clientIDRaw, &clientID); err != nil {
		return nil, nil, fmt.Errorf("OAuth clientId must be a string")
	}
	resolved, used, err := resolveMCPHeaderTemplates(map[string]string{"clientId": clientID}, values)
	if err != nil {
		return nil, nil, err
	}
	encodedClientID, _ := json.Marshal(resolved["clientId"])
	config["clientId"] = encodedClientID
	encodedConfig, err := json.Marshal(config)
	if err != nil {
		return nil, nil, fmt.Errorf("encode OAuth configuration: %w", err)
	}
	return encodedConfig, used, nil
}

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
	if s.Store == nil {
		writeJSON(w, http.StatusOK, []store.InstalledMCP{})
		return
	}
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" {
		projectID = "*"
	}
	mcps, err := s.Store.ListInstalledMCPs(projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mcps)
}

func (s *Server) InstallMCP(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
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

	resolvedVariables := make(map[string]bool)
	var mcpServersWrap struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			Headers map[string]string `json:"headers"`
			OAuth   json.RawMessage   `json:"oauth"`
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
			resolvedOAuth, used, err := resolveMCPOAuthTemplates(cfg.OAuth, req.Env)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			entry.OAuth = resolvedOAuth
			for name := range used {
				resolvedVariables[name] = true
			}
			if len(cfg.Headers) > 0 {
				resolved, used, err := resolveMCPHeaderTemplates(cfg.Headers, req.Env)
				if err != nil {
					writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				entry.Headers = resolved
				for name := range used {
					resolvedVariables[name] = true
				}
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
		remainingEnv := make(map[string]string, len(req.Env))
		for name, value := range req.Env {
			if !resolvedVariables[name] {
				remainingEnv[name] = value
			}
		}
		if b, err := json.Marshal(remainingEnv); err == nil {
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
	if err := s.Store.UpsertInstalledMCP(mcp); err != nil {
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
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	name := chi.URLParam(r, "name")
	projectID := r.URL.Query().Get("project_id")
	all := projectID == "" // no project_id means delete all entries for this name
	if all {
		if err := s.Store.DeleteInstalledMCPByName(name); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = s.Store.DeleteMCPOAuthTokenByName(name)
	} else {
		if err := s.Store.DeleteInstalledMCP(name, projectID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = s.Store.DeleteMCPOAuthToken(name, projectID)
	}
	log.Printf("[marketplace] uninstalled MCP: %s (project=%q, all=%v)", name, projectID, all)
	w.WriteHeader(http.StatusNoContent)
}
