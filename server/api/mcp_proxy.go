package api

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/esonic/nexbox/store"
	"github.com/go-chi/chi/v5"
)

var oauthDiscoveryHTTPClient = &http.Client{Timeout: 10 * time.Second}

// ---------------------------------------------------------------------------
// MCP Reverse Proxy
// ---------------------------------------------------------------------------

// HandleMCPProxy proxies requests from Claude to the real MCP HTTP endpoint,
// injecting Authorization headers as needed. Supports SSE (no buffering).
//
// Route: /mcp-proxy/{name}/{path...}
func (s *Server) HandleMCPProxy(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	if s.Store == nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	// Require a valid session ID for access control.
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sess, err := s.Store.GetSession(sessionID)
	if err != nil || sess == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	mcp, err := s.resolveMCPForProxy(name, sess.ProjectID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Determine real target URL from the stored manifest.
	targetURL, err := mcpTargetURL(mcp.Manifest)
	if err != nil {
		http.Error(w, "cannot determine target URL for MCP "+name, http.StatusBadGateway)
		return
	}

	// Resolve the complete header set. Static headers come directly from the
	// installed manifest; OAuth, when configured, only replaces Authorization.
	requestHeaders, err := s.mcpRequestHeaders(mcp)
	if err != nil {
		http.Error(w, "auth error for MCP "+name+": "+err.Error(), http.StatusBadGateway)
		return
	}
	var rpcRequest mcpProxyRequest
	if r.Method == http.MethodPost && r.Body != nil {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
		_ = r.Body.Close()
		if err != nil {
			http.Error(w, "MCP request body is too large or unreadable", http.StatusRequestEntityTooLarge)
			return
		}
		_ = json.Unmarshal(body, &rpcRequest)
		if rpcRequest.Method == "tools/call" {
			body, err = s.translateMCPToolCall(mcp, body, rpcRequest)
			if err != nil {
				http.Error(w, "invalid MCP tool call", http.StatusBadRequest)
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		r.ContentLength = int64(len(body))
	}

	// Build the forwarding URL: targetURL + the tail path after /mcp-proxy/{name}.
	// Strip internal "session" query parameter before forwarding.
	tail := chi.URLParam(r, "*")
	fwdQuery := r.URL.Query()
	fwdQuery.Del("session")
	fwdURL, err := buildForwardURL(targetURL, tail, fwdQuery.Encode())
	if err != nil {
		http.Error(w, "invalid forward URL: "+err.Error(), http.StatusBadGateway)
		return
	}

	proxy := &httputil.ReverseProxy{
		Transport: &mcpOAuthTransport{server: s, mcp: mcp},
		Director: func(req *http.Request) {
			req.URL = fwdURL
			req.Host = fwdURL.Host
			for headerName, headerValue := range requestHeaders {
				req.Header.Set(headerName, headerValue)
			}
			// Prefer JSON over SSE in the Accept header so that upstream MCP
			// servers return application/json responses. Some servers incorrectly
			// return Content-Type: text/event-stream with a plain JSON body when
			// SSE is preferred, which breaks strict Streamable-HTTP clients like
			// Codex's rmcp.
			req.Header.Set("Accept", "application/json, text/event-stream")
			if rpcRequest.Method == "tools/list" {
				// This reply is parsed and rewritten below. Do not forward a
				// client's gzip negotiation and then try to parse compressed JSON.
				req.Header.Set("Accept-Encoding", "identity")
			}
			// Remove hop-by-hop headers that shouldn't be forwarded.
			req.Header.Del("X-Forwarded-For")
		},
		// Disable response buffering so SSE streams through immediately.
		FlushInterval: -1,
		// Detect upstream 401. Prompt only for known OAuth MCPs or after successful
		// discovery; an API-key failure must not be mislabeled as OAuth.
		ModifyResponse: func(resp *http.Response) error {
			normalizeMCPNotificationResponse(rpcRequest, resp)
			if resp.StatusCode == http.StatusUnauthorized {
				log.Printf("[mcp-proxy] %s: upstream returned 401", name)
				shouldPromptOAuth := mcp.AuthType == "oauth" || mcp.AuthType == "oauth_discovery"
				if mcp.AuthType == "none" {
					go func() {
						if s.discoverAndMarkMCPOAuth(mcp, fwdURL.String()) {
							s.broadcastNeedsAuth(name, mcp.ProjectID)
						}
					}()
				}
				if shouldPromptOAuth {
					s.broadcastNeedsAuth(name, mcp.ProjectID)
				}
				// Preserve 401, WWW-Authenticate and the upstream body. Turning
				// this into HTTP 200 with id:null breaks strict JSON-RPC clients.
			}
			if rpcRequest.Method == "tools/list" && resp.StatusCode == http.StatusOK {
				return s.rewriteMCPToolList(resp, mcp, rpcRequest.ID)
			}
			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			log.Printf("[mcp-proxy] %s: upstream error: %v", name, err)
			http.Error(rw, "upstream error: "+err.Error(), http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// resolveMCPForProxy fetches the InstalledMCP record for proxying.
// It tries project-scoped first, then global.
func (s *Server) resolveMCPForProxy(name, projectID string) (*store.InstalledMCP, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("local store not available")
	}
	if projectID != "" {
		mcp, err := s.Store.GetInstalledMCP(name, projectID)
		if err == nil && mcp != nil {
			return mcp, nil
		}
	}
	// Fall back to global record.
	mcp, err := s.Store.GetInstalledMCP(name, "")
	if err != nil || mcp == nil {
		return nil, fmt.Errorf("MCP %q not installed", name)
	}
	return mcp, nil
}

// mcpTargetURL extracts the upstream URL from a stored manifest JSON.
func mcpTargetURL(manifestJSON string) (*url.URL, error) {
	entry, err := parseInstalledMCPManifest(manifestJSON)
	if err != nil || entry.URL == "" {
		return nil, fmt.Errorf("no url in manifest")
	}
	return url.Parse(entry.URL)
}

// buildForwardURL concatenates base + tail path and sets the query string.
func buildForwardURL(base *url.URL, tail, rawQuery string) (*url.URL, error) {
	fwd := *base
	if tail != "" {
		fwd.Path = strings.TrimRight(fwd.Path, "/") + "/" + strings.TrimLeft(tail, "/")
	}
	query := base.Query()
	forwarded, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, err
	}
	for key, values := range forwarded {
		query[key] = values
	}
	fwd.RawQuery = query.Encode()
	return &fwd, nil
}

// mcpRequestHeaders returns the complete headers stored for an MCP. Placeholder
// replacement here keeps MCPs installed by older NexBox versions working; new
// installs are already fully resolved before they are stored.
func (s *Server) mcpRequestHeaders(mcp *store.InstalledMCP) (map[string]string, error) {
	manifest, err := parseInstalledMCPManifest(mcp.Manifest)
	if err != nil {
		return nil, fmt.Errorf("parse MCP manifest: %w", err)
	}

	var vals map[string]string
	if mcp.EnvValues != "" && mcp.EnvValues != "{}" {
		if err := json.Unmarshal([]byte(mcp.EnvValues), &vals); err != nil {
			return nil, fmt.Errorf("parse stored MCP values: %w", err)
		}
	}
	resolved, _, err := resolveMCPHeaderTemplates(manifest.Headers, vals)
	if err != nil {
		return nil, err
	}
	if resolved == nil {
		resolved = make(map[string]string)
	}

	useOAuth := mcp.AuthType == "oauth_discovery" || mcp.AuthType == "oauth"
	if mcp.AuthType == "none" && s.Store != nil {
		_, err := mcpTargetURL(mcp.Manifest)
		useOAuth = err == nil
	}
	if useOAuth {
		authHeader, err := s.oauthHeader(mcp)
		if err != nil {
			return nil, err
		}
		if authHeader != "" {
			if mcp.AuthType == "none" {
				mcp.AuthType = "oauth_discovery"
				if err := s.Store.UpdateInstalledMCPAuthType(mcp.Name, mcp.ProjectID, mcp.AuthType); err != nil {
					return nil, err
				}
			}
			for name := range resolved {
				if strings.EqualFold(name, "Authorization") {
					delete(resolved, name)
				}
			}
			resolved["Authorization"] = authHeader
		}
	}
	return resolved, nil
}

func (s *Server) oauthHeader(mcp *store.InstalledMCP) (string, error) {
	return s.managedOAuthHeader(mcp, "")
}

// broadcastNeedsAuth pushes a connector:needs_auth event to all WS clients,
// prompting the frontend to initiate the OAuth authorization flow.
// projectID is included so the frontend can pass it to oauthStart.
func (s *Server) broadcastNeedsAuth(mcpName, projectID string) {
	if s.Hub == nil {
		return
	}
	event := map[string]any{"type": "connector:needs_auth", "name": mcpName, "project_id": projectID}
	if s.Store != nil {
		if mcp, err := s.Store.GetInstalledMCP(mcpName, projectID); err == nil && mcp != nil {
			if key, err := mcpOAuthConfigKey(mcp); err == nil {
				event["config_key"] = key
			}
		}
	}
	data, err := json.Marshal(event)
	if err == nil {
		s.Hub.BroadcastRaw(data)
	}
}

// broadcastReauth kept for backwards compatibility.
func (s *Server) broadcastReauth(mcpName string) {
	s.broadcastNeedsAuth(mcpName, "")
}

// discoverAndMarkMCPOAuth verifies OAuth discovery for an MCP whose auth type
// is not known yet. A plain 401 is not enough to classify a server as OAuth.
func (s *Server) discoverAndMarkMCPOAuth(mcp *store.InstalledMCP, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	if _, err := discoverInstalledMCPOAuth(mcp); err != nil {
		return false
	}
	if s.Store != nil {
		_ = s.Store.UpdateInstalledMCPAuthType(mcp.Name, mcp.ProjectID, "oauth_discovery")
	}
	return true
}

// ---------------------------------------------------------------------------
// OAuth Authorization Code + PKCE flow
// ---------------------------------------------------------------------------

// OAuthStartResponse is returned by POST /api/connectors/{name}/oauth/start.
type OAuthStartResponse struct {
	AuthURL string `json:"auth_url"`
}

// HandleOAuthStart initiates OAuth Authorization Code + PKCE for an installed MCP.
func (s *Server) HandleOAuthStart(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	projectID := r.URL.Query().Get("project_id")

	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	mcp, err := s.resolveMCPForProxy(name, projectID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	authURL, err := s.startOAuthFlow(mcp)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, OAuthStartResponse{AuthURL: authURL})
}

// startOAuthFlow performs OAuth Authorization Code + PKCE setup for an installed MCP
// and returns the authorization URL to redirect the user to.
func (s *Server) startOAuthFlow(mcp *store.InstalledMCP) (string, error) {
	name := mcp.Name

	// Determine OAuth client ID and authorization server URL.
	clientID, _, err := resolveOAuthConfig(mcp)
	if err != nil {
		return "", err
	}

	configKey, err := mcpOAuthConfigKey(mcp)
	if err != nil {
		return "", err
	}
	unlock := s.lockMCPOAuth(configKey)
	defer unlock()
	active, err := s.activeMCPOAuthInstallation(configKey, mcp.Name, mcp.ProjectID)
	if err != nil {
		return "", err
	}
	if active == nil {
		return "", fmt.Errorf("MCP installation no longer exists")
	}
	mcp = active
	name = active.Name
	connection, err := s.resolveMCPOAuthConnection(mcp, configKey)
	if err != nil {
		return "", err
	}
	if connection == nil {
		id := mcpOAuthConnectionID(configKey)
		generation, err := s.Store.GetMCPOAuthLifecycle(configKey)
		if err != nil {
			return "", err
		}
		connection = &store.MCPOAuthConnection{ID: id, ConfigKey: configKey, Generation: generation}
	}
	configuredClient := clientID != ""
	if clientID == "" {
		clientID = connection.Token.ClientID
	}
	pendingURL, err := s.Store.GetMCPOAuthPendingURL(configKey, connection.ID)
	if err != nil {
		return "", err
	}
	if pendingURL != "" {
		return pendingURL, nil
	}
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/api/connectors/oauth/callback", s.ListenPort)
	disc, err := discoverInstalledMCPOAuth(mcp)
	if err != nil {
		return "", fmt.Errorf("OAuth discovery failed: %w", err)
	}
	var manifest struct {
		OAuth struct {
			Scope    string `json:"scope"`
			Resource string `json:"resource"`
		} `json:"oauth"`
	}
	entry, err := parseInstalledMCPManifest(mcp.Manifest)
	if err != nil {
		return "", err
	}
	if len(entry.OAuth) > 0 {
		if err := json.Unmarshal(entry.OAuth, &manifest.OAuth); err != nil {
			return "", err
		}
	}
	scope, resource := disc.Scope, disc.Resource
	if manifest.OAuth.Scope != "" {
		scope = manifest.OAuth.Scope
	}
	if manifest.OAuth.Resource != "" {
		resource = manifest.OAuth.Resource
	}
	client := connection.Token
	client.ClientID = clientID
	// Older BeeHub DCR clients requested only authorization_code. Reusing their
	// registration can fail refresh even when a token was issued. Upgrade once
	// on explicit authorization; never invalidate a working grant in maintenance.
	if !configuredClient && len(client.RegisteredGrantTypes) == 0 && disc.RegistrationEndpoint != "" {
		client.ClientID = ""
	}
	if client.ClientID == "" {
		if disc.RegistrationEndpoint == "" {
			return "", fmt.Errorf("OAuth client_id not configured for MCP %s and dynamic registration is unavailable", name)
		}
		registered, err := registerMCPOAuthClient(disc, callbackURL, scope)
		if err != nil {
			return "", fmt.Errorf("OAuth client registration failed: %w", err)
		}
		client = *registered
	} else {
		client.AuthMethod, err = chooseMCPOAuthAuthMethod(disc.AuthMethods, client.ClientSecret, client.AuthMethod)
		if err != nil {
			return "", err
		}
	}
	client.Scope, client.Resource = scope, resource

	// Generate PKCE verifier + challenge.
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return "", fmt.Errorf("PKCE generation failed")
	}

	// Generate random state.
	state, err := randomBase64(16)
	if err != nil {
		return "", fmt.Errorf("state generation failed")
	}

	// Pending metadata includes the registration's refresh grant, client auth,
	// resource and scope, so callback and future refreshes use the same contract.
	pending := client
	pending.MCPName, pending.ProjectID = name, mcp.ProjectID
	pending.AccessToken, pending.RefreshToken = "", ""
	pending.TokenEndpoint = disc.TokenEndpoint
	pending.State, pending.CodeVerifier = state, verifier
	pending.UpdatedAt = time.Now()

	// Build authorization URL.
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {client.ClientID},
		"redirect_uri":          {callbackURL},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if scope != "" {
		params.Set("scope", scope)
	}
	if resource != "" {
		params.Set("resource", resource)
	}
	authorizationURL, err := url.Parse(disc.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("invalid OAuth authorization endpoint")
	}
	query := authorizationURL.Query()
	for key, values := range params {
		query[key] = values
	}
	authorizationURL.RawQuery = query.Encode()
	pending.Scope, pending.Resource = query.Get("scope"), query.Get("resource")
	authURL := authorizationURL.String()
	// Pending state never replaces the live grant, and concurrent starts for
	// the same connection share one PKCE flow and one DCR client registration.
	pendingConnection := *connection
	pendingConnection.Token = pending
	pendingConnection.PendingAuthURL = authURL
	if err := s.Store.SaveMCPOAuthPending(pendingConnection); err != nil {
		return "", fmt.Errorf("store OAuth state: %w", err)
	}
	return authURL, nil
}

// ---------------------------------------------------------------------------
// MCP Probe: detect auth requirement after install
// ---------------------------------------------------------------------------

// HandleMCPProbe uses the same discovery and authorization checks as chat.
func (s *Server) HandleMCPProbe(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	projectID := r.URL.Query().Get("project_id")

	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	mcp, err := s.resolveMCPForProxy(name, projectID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	key := installedMCPRuntimeKey(mcp)
	entry, err := parseInstalledMCPManifest(mcp.Manifest)
	if err == nil && entry.Type == "stdio" && entry.Cmd != "" {
		writeJSON(w, http.StatusOK, mcpRuntimeStatus{Name: name, Key: key, State: "runtime_managed", Message: "Local stdio connector; availability is determined by the agent runtime."})
		return
	}
	status := s.probeInstalledMCP(r.Context(), mcp, key)
	if status.State == "ready" && (mcp.AuthType == "oauth" || mcp.AuthType == "oauth_discovery") {
		if configKey, err := mcpOAuthConfigKey(mcp); err == nil {
			unlock := s.lockMCPOAuth(configKey)
			if connection, err := s.resolveMCPOAuthConnection(mcp, configKey); err == nil && connection != nil {
				s.broadcastMCPOAuthConnected(connection)
			}
			unlock()
		}
	}
	writeJSON(w, http.StatusOK, status)
}

// probeMCPNeedsAuth sends an MCP initialize request to url and returns true if
// the server responds with HTTP 401 (auth required). A 5-second timeout is used
// so the probe never stalls the install flow.
func probeMCPNeedsAuth(mcpURL string, headers map[string]string) (bool, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "nexbox", "version": "1.0"},
		},
	})

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPost, mcpURL, strings.NewReader(string(body)))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusUnauthorized, nil
}

// HandleOAuthCallback exchanges the authorization code for tokens.
// GET /api/connectors/oauth/callback?code=...&state=...
func (s *Server) HandleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if state == "" || (code == "" && r.URL.Query().Get("error") == "") {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}
	if s.Store == nil {
		http.Error(w, "local store not available", http.StatusServiceUnavailable)
		return
	}

	connection, err := s.Store.GetMCPOAuthPending(state)
	if err != nil || connection == nil {
		http.Error(w, "unknown or expired OAuth state", http.StatusBadRequest)
		return
	}
	unlock := s.lockMCPOAuth(connection.ConfigKey)
	defer unlock()
	connection, err = s.Store.TakeMCPOAuthPending(state)
	if err != nil || connection == nil {
		http.Error(w, "unknown or expired OAuth state", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("error") != "" {
		http.Error(w, "Authorization was declined. You may close this tab and retry in BeeHub.", http.StatusBadRequest)
		return
	}
	pending := &connection.Token
	generation, err := s.Store.GetMCPOAuthLifecycle(connection.ConfigKey)
	if err != nil || generation == "" || generation != connection.Generation {
		http.Error(w, "MCP authorization lifecycle ended; restart authorization", http.StatusBadRequest)
		return
	}
	installed, err := s.activeMCPOAuthInstallation(connection.ConfigKey, pending.MCPName, pending.ProjectID)
	if err != nil || installed == nil {
		http.Error(w, "MCP installation no longer exists", http.StatusBadRequest)
		return
	}

	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/api/connectors/oauth/callback", s.ListenPort)

	// Exchange code for token.
	tok, err := exchangeMCPOAuthToken(pending, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {pending.CodeVerifier}, "redirect_uri": {callbackURL}})
	if err != nil {
		http.Error(w, "token exchange failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Consent responses may omit a replacement refresh token. Keep the current
	// one only for exactly the same OAuth client/resource/scope contract.
	if tok.RefreshToken == "" {
		current, err := s.Store.ListMCPOAuthConnections(connection.ConfigKey)
		if err != nil {
			http.Error(w, "load existing OAuth connection failed", http.StatusInternalServerError)
			return
		}
		for _, existing := range current {
			old := existing.Token
			if old.ClientID == tok.ClientID && old.ClientSecret == tok.ClientSecret && old.TokenEndpoint == tok.TokenEndpoint && old.Resource == tok.Resource && sameMCPOAuthScope(old.Scope, tok.Scope) {
				tok.RefreshToken = old.RefreshToken
				break
			}
		}
	}
	// The preferred installation may change during the network exchange;
	// retain the grant for any installation still in the same lifetime.
	installed, err = s.activeMCPOAuthInstallation(connection.ConfigKey, pending.MCPName, pending.ProjectID)
	if err != nil || installed == nil {
		http.Error(w, "MCP installation no longer exists", http.StatusBadRequest)
		return
	}
	// Commit the shared grant without retaining pending state.
	tok.MCPName = installed.Name
	tok.ProjectID = installed.ProjectID
	tok.TokenEndpoint = pending.TokenEndpoint
	tok.ClientID = pending.ClientID
	tok.Resource = pending.Resource
	tok.State = ""
	tok.CodeVerifier = ""
	tok.UpdatedAt = time.Now()
	connection.Token = *tok
	if err := s.Store.CompleteMCPOAuthConnection(*connection, *installed); err != nil {
		http.Error(w, "could not save OAuth connection; retry authorization", http.StatusInternalServerError)
		return
	}

	log.Printf("[mcp-oauth] %s authorization saved (refresh_available=%t, expiry_known=%t)", installed.Name, tok.RefreshToken != "", !tok.ExpiresAt.IsZero())
	s.broadcastMCPOAuthConnected(connection)

	// Return a small HTML page that closes the popup.
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!DOCTYPE html><html><body>
<p>Connected! You may close this tab.</p>
<script>window.close();</script>
</body></html>`)
}

// ---------------------------------------------------------------------------
// OAuth helpers
// ---------------------------------------------------------------------------

// resolveOAuthConfig returns (clientID, authServerBaseURL) for an MCP.
// clientID may be empty when the server supports Dynamic Client Registration (RFC 7591).
func resolveOAuthConfig(mcp *store.InstalledMCP) (clientID, authServerURL string, err error) {
	entry, err := parseInstalledMCPManifest(mcp.Manifest)
	if err != nil {
		return "", "", fmt.Errorf("parse manifest: %w", err)
	}
	var oauth struct {
		ClientID string `json:"clientId"`
	}
	var values map[string]string
	if mcp.EnvValues != "" {
		if err := json.Unmarshal([]byte(mcp.EnvValues), &values); err != nil {
			return "", "", err
		}
	}
	resolvedOAuth, _, err := resolveMCPOAuthTemplates(entry.OAuth, values)
	if err != nil {
		return "", "", err
	}
	if len(resolvedOAuth) > 0 {
		if err := json.Unmarshal(resolvedOAuth, &oauth); err != nil {
			return "", "", fmt.Errorf("parse OAuth configuration: %w", err)
		}
	}
	clientID = oauth.ClientID

	// oauth_discovery: use env_values for client_id if provided.
	if clientID == "" && mcp.EnvValues != "" && mcp.EnvValues != "{}" {
		var vals map[string]string
		if json.Unmarshal([]byte(mcp.EnvValues), &vals) == nil {
			if cid, ok := vals["CLIENT_ID"]; ok && cid != "" {
				clientID = cid
			}
		}
	}

	// Also check if a client_id was previously obtained via DCR and stored in the token record.
	// (clientID remains empty here — HandleOAuthStart will look it up.)

	if entry.URL == "" {
		return "", "", fmt.Errorf("MCP has no URL for OAuth")
	}

	// Discovery is rooted at the MCP server's origin. The MCP endpoint itself
	// commonly has a path (for example /mcp), which must not become part of the
	// OAuth well-known URL just because an explicit client ID was configured.
	u, err := url.Parse(entry.URL)
	if err != nil {
		return "", "", fmt.Errorf("parse MCP URL: %w", err)
	}
	// clientID may be empty — HandleOAuthStart will attempt DCR if the server supports it.
	return clientID, u.Scheme + "://" + u.Host, nil
}

// generatePKCE creates a PKCE code verifier and S256 challenge.
func generatePKCE() (verifier, challenge string, err error) {
	raw, err := randomBytes(32)
	if err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// randomBase64 returns n random bytes encoded as URL-safe base64.
func randomBase64(n int) (string, error) {
	b, err := randomBytes(n)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}
