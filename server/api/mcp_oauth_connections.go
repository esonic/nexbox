package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/esonic/nexbox/store"
)

func (s *Server) preserveMCPOAuthConnection(mcp *store.InstalledMCP) error {
	if mcp.AuthType != "oauth" && mcp.AuthType != "oauth_discovery" && mcp.AuthType != "none" {
		return nil
	}
	key, err := mcpOAuthConfigKey(mcp)
	// Invalid/stdio legacy manifests cannot define a reusable HTTP connection.
	if err != nil {
		return nil
	}
	unlock := s.lockMCPOAuth(key)
	defer unlock()
	_, err = s.resolveMCPOAuthConnection(mcp, key)
	return err
}

// The local store is the user/profile boundary. A project only references a
// connection; neither project IDs nor marketplace display names define grants.
// Include the full resource URL and configuration to avoid sharing credentials
// across tenants, resource paths, clients, requested scopes or custom headers.
func mcpOAuthConfigKey(mcp *store.InstalledMCP) (string, error) {
	clientID, _, err := resolveOAuthConfig(mcp)
	if err != nil {
		return "", err
	}
	entry, err := parseInstalledMCPManifest(mcp.Manifest)
	if err != nil {
		return "", err
	}
	manifest := struct {
		URL     string
		OAuth   map[string]any
		Headers map[string]string
	}{URL: entry.URL, Headers: entry.Headers}
	if len(entry.OAuth) > 0 {
		if err := json.Unmarshal(entry.OAuth, &manifest.OAuth); err != nil {
			return "", err
		}
	}
	u, err := url.Parse(manifest.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", fmt.Errorf("invalid OAuth MCP resource URL")
	}
	u.Scheme, u.Host, u.Fragment = strings.ToLower(u.Scheme), strings.ToLower(u.Host), ""
	if u.Path == "" {
		u.Path = "/"
	}
	if manifest.OAuth == nil {
		manifest.OAuth = make(map[string]any)
	}
	delete(manifest.OAuth, "clientId") // represented by the resolved client ID
	for _, field := range []string{"scope", "scopes"} {
		if scope, ok := manifest.OAuth[field].(string); ok {
			parts := strings.Fields(scope)
			sort.Strings(parts)
			manifest.OAuth[field] = strings.Join(parts, " ")
		}
	}
	values := make(map[string]string)
	if mcp.EnvValues != "" {
		if err := json.Unmarshal([]byte(mcp.EnvValues), &values); err != nil {
			return "", err
		}
	}
	resolved, usedHeaders, err := resolveMCPHeaderTemplates(manifest.Headers, values)
	if err != nil {
		return "", err
	}
	_, usedOAuth, err := resolveMCPOAuthTemplates(entry.OAuth, values)
	if err != nil {
		return "", err
	}
	headers := make(map[string]string)
	for name, value := range resolved {
		headers[strings.ToLower(name)] = value
	}
	for name := range usedHeaders {
		delete(values, name)
	}
	for name := range usedOAuth {
		delete(values, name)
	}
	delete(values, "CLIENT_ID") // represented by the resolved client ID
	raw, err := json.Marshal(struct {
		Resource, ClientID string
		OAuth              map[string]any
		Headers, Values    map[string]string
	}{u.String(), clientID, manifest.OAuth, headers, values})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func (s *Server) lockMCPOAuth(configKey string) func() {
	value, _ := s.oauthRefreshLocks.LoadOrStore("connection:"+configKey, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

// Caller holds the configuration lock, shared by migration, callback and refresh.
// One compatible configuration has one authorization for every installation.
func (s *Server) resolveMCPOAuthConnection(mcp *store.InstalledMCP, configKey string) (*store.MCPOAuthConnection, error) {
	generation, err := s.Store.EnsureMCPOAuthLifecycle(configKey)
	if err != nil {
		return nil, err
	}
	connections, err := s.Store.ListMCPOAuthConnections(configKey)
	if err != nil {
		return nil, err
	}
	id := mcpOAuthConnectionID(configKey)
	if len(connections) == 1 && connections[0].ID == id {
		connections[0].Generation = generation
		return &connections[0], nil
	}
	var latest *store.MCPOAuthToken
	choose := func(token store.MCPOAuthToken) {
		if token.AccessToken == "" {
			return
		}
		if latest == nil || token.UpdatedAt.After(latest.UpdatedAt) {
			copy := token
			latest = &copy
		}
	}
	for _, c := range connections {
		choose(c.Token)
	}
	installed, err := s.Store.ListInstalledMCPs("*")
	if err != nil {
		return nil, err
	}
	installed = append(installed, *mcp)
	var legacyScopes []store.InstalledMCP
	seen := make(map[string]bool)
	for _, candidate := range installed {
		if candidate.AuthType != "oauth" && candidate.AuthType != "oauth_discovery" && candidate.AuthType != "none" {
			continue
		}
		key, err := mcpOAuthConfigKey(&candidate)
		if err != nil || key != configKey {
			continue
		}
		scope := candidate.Name + "\x00" + candidate.ProjectID
		if seen[scope] {
			continue
		}
		seen[scope] = true
		legacy, err := s.Store.GetMCPOAuthToken(candidate.Name, candidate.ProjectID)
		if err != nil {
			return nil, err
		}
		if legacy == nil {
			continue
		}
		legacyScopes = append(legacyScopes, candidate)
		choose(*legacy)
	}
	if latest == nil {
		return nil, nil
	}
	connection := store.MCPOAuthConnection{ID: id, ConfigKey: configKey, Generation: generation, Token: *latest}
	// Commit the selected grant and discard every superseded legacy copy
	// together, so later requests cannot revive an old rotating refresh token.
	if err := s.Store.ConsolidateMCPOAuthConnection(connection, legacyScopes); err != nil {
		return nil, err
	}
	return &connection, nil
}

func mcpOAuthConnectionID(configKey string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte("shared:"+configKey)))
}

// Pending authorization belongs to the compatible configuration's lifetime,
// so another project can finish it after the initiating project changes/removes
// its installation. The persisted generation prevents crossing a final uninstall.
func (s *Server) activeMCPOAuthInstallation(configKey string, preferredName, preferredProject string) (*store.InstalledMCP, error) {
	installed, err := s.Store.ListInstalledMCPs("*")
	if err != nil {
		return nil, err
	}
	var fallback *store.InstalledMCP
	for _, mcp := range installed {
		if mcp.AuthType == "apikey" {
			continue
		}
		key, err := mcpOAuthConfigKey(&mcp)
		if err != nil || key != configKey {
			continue
		}
		copy := mcp
		if mcp.Name == preferredName && mcp.ProjectID == preferredProject {
			return &copy, nil
		}
		if fallback == nil {
			fallback = &copy
		}
	}
	return fallback, nil
}

// Caller serializes installation changes using the installations lock. Cleanup
// takes the same configuration lock as refresh, so a refresh already in flight
// finishes before deletion and cannot recreate an authorization afterward.
func (s *Server) cleanupUnusedMCPOAuthConnections() error {
	if err := s.Store.DeleteOrphanedMCPOAuthTokens(); err != nil {
		return err
	}
	installed, err := s.Store.ListInstalledMCPs("*")
	if err != nil {
		return err
	}
	active := make(map[string]bool)
	for _, mcp := range installed {
		if mcp.AuthType == "apikey" {
			continue
		}
		if key, err := mcpOAuthConfigKey(&mcp); err == nil {
			active[key] = true
		}
	}
	keys, err := s.Store.ListMCPOAuthConfigKeys()
	if err != nil {
		return err
	}
	for _, key := range keys {
		if active[key] {
			continue
		}
		unlock := s.lockMCPOAuth(key)
		err := s.Store.DeleteMCPOAuthConfig(key)
		unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// Completing a shared authorization dismisses notices for every installation
// referencing that connection, including other projects and global installs.
func (s *Server) broadcastMCPOAuthConnected(connection *store.MCPOAuthConnection) {
	installed, err := s.Store.ListInstalledMCPs("*")
	if err != nil || s.Hub == nil {
		return
	}
	for _, mcp := range installed {
		key, err := mcpOAuthConfigKey(&mcp)
		if err != nil || key != connection.ConfigKey {
			continue
		}
		if mcp.AuthType == "apikey" {
			continue
		}
		data, err := json.Marshal(map[string]any{"type": "connector:connected", "name": mcp.Name, "project_id": mcp.ProjectID, "connection_id": connection.ID, "config_key": connection.ConfigKey})
		if err == nil {
			s.Hub.BroadcastRaw(data)
		}
	}
}
