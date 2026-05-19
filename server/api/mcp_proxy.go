package api

import (
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

	"github.com/go-chi/chi/v5"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

// ---------------------------------------------------------------------------
// MCP Reverse Proxy
// ---------------------------------------------------------------------------

// HandleMCPProxy proxies requests from Claude to the real MCP HTTP endpoint,
// injecting Authorization headers as needed. Supports SSE (no buffering).
//
// Route: /mcp-proxy/{name}/{path...}
func (s *Server) HandleMCPProxy(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	if s.LocalStore == nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	// Require a valid session ID for access control.
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sess, err := s.LocalStore.GetSession(sessionID)
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

	// Get Authorization header value (lazy-refresh OAuth tokens).
	authHeader, err := s.mcpAuthHeader(mcp)
	if err != nil {
		http.Error(w, "auth error for MCP "+name+": "+err.Error(), http.StatusBadGateway)
		return
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
		Director: func(req *http.Request) {
			req.URL = fwdURL
			req.Host = fwdURL.Host
			if authHeader != "" {
				req.Header.Set("Authorization", authHeader)
			}
			// Prefer JSON over SSE in the Accept header so that upstream MCP
			// servers return application/json responses. Some servers incorrectly
			// return Content-Type: text/event-stream with a plain JSON body when
			// SSE is preferred, which breaks strict Streamable-HTTP clients like
			// Codex's rmcp.
			req.Header.Set("Accept", "application/json, text/event-stream")
			// Remove hop-by-hop headers that shouldn't be forwarded.
			req.Header.Del("X-Forwarded-For")
		},
		// Disable response buffering so SSE streams through immediately.
		FlushInterval: -1,
		// Detect upstream 401: always prompt the user to authorize.
		// If the upstream supports OAuth discovery (RFC 9728), also upgrade the
		// stored auth_type so future calls inject the token and skip the round-trip.
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode == http.StatusUnauthorized {
				log.Printf("[mcp-proxy] %s: upstream returned 401, prompting auth", name)
				s.broadcastNeedsAuth(name, mcp.ProjectID)
				if mcp.AuthType == "none" {
					origin := fwdURL.Scheme + "://" + fwdURL.Host
					go func() {
						if _, err := discoverOAuthEndpoints(origin); err == nil {
							if s.LocalStore != nil {
								_ = s.LocalStore.UpdateInstalledMCPAuthType(name, mcp.ProjectID, "oauth_discovery")
							}
						}
					}()
				}
				// Replace the 401 with a JSON-RPC error so Claude CLI can relay
				// a useful message to the user instead of silently dropping tools.
				msg := fmt.Sprintf(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"MCP %q requires OAuth authorization. Please click Authorize in the UI notification, then start a new conversation."}}`, name)
				body := io.NopCloser(strings.NewReader(msg))
				resp.Body = body
				resp.ContentLength = int64(len(msg))
				resp.StatusCode = http.StatusOK
				resp.Status = "200 OK"
				resp.Header.Set("Content-Type", "application/json")
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
	if s.LocalStore == nil {
		return nil, fmt.Errorf("local store not available")
	}
	if projectID != "" {
		mcp, err := s.LocalStore.GetInstalledMCP(name, projectID)
		if err == nil && mcp != nil {
			return mcp, nil
		}
	}
	// Fall back to global record.
	mcp, err := s.LocalStore.GetInstalledMCP(name, "")
	if err != nil || mcp == nil {
		return nil, fmt.Errorf("MCP %q not installed", name)
	}
	return mcp, nil
}

// mcpTargetURL extracts the upstream URL from a stored manifest JSON.
func mcpTargetURL(manifestJSON string) (*url.URL, error) {
	var entry struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(manifestJSON), &entry); err != nil || entry.URL == "" {
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
	fwd.RawQuery = rawQuery
	return &fwd, nil
}

// mcpAuthHeader returns the Authorization header value for an installed MCP.
func (s *Server) mcpAuthHeader(mcp *store.InstalledMCP) (string, error) {
	switch mcp.AuthType {
	case "apikey":
		return s.apikeyHeader(mcp)
	case "oauth_discovery", "oauth":
		return s.oauthHeader(mcp)
	default:
		return "", nil
	}
}

func (s *Server) apikeyHeader(mcp *store.InstalledMCP) (string, error) {
	if mcp.EnvValues == "" || mcp.EnvValues == "{}" {
		return "", nil
	}
	var vals map[string]string
	if err := json.Unmarshal([]byte(mcp.EnvValues), &vals); err != nil {
		return "", nil
	}
	// Look for a value with key that suggests it's an API key / token.
	for k, v := range vals {
		ku := strings.ToUpper(k)
		if strings.Contains(ku, "TOKEN") || strings.Contains(ku, "KEY") || strings.Contains(ku, "SECRET") || strings.Contains(ku, "API") {
			return "Bearer " + v, nil
		}
	}
	// Fallback: return first value as Bearer token.
	for _, v := range vals {
		return "Bearer " + v, nil
	}
	return "", nil
}

func (s *Server) oauthHeader(mcp *store.InstalledMCP) (string, error) {
	tok, err := s.LocalStore.GetMCPOAuthToken(mcp.Name, mcp.ProjectID)
	if err != nil {
		return "", fmt.Errorf("get oauth token: %w", err)
	}
	if tok == nil || tok.AccessToken == "" {
		// No token yet — forward the request without auth; upstream will 401
		// which is caught by ModifyResponse and triggers the auth prompt.
		return "", nil
	}

	// Lazy refresh: if token expires within 5 minutes, try to refresh.
	if !tok.ExpiresAt.IsZero() && time.Until(tok.ExpiresAt) < 5*time.Minute {
		if tok.RefreshToken != "" && tok.TokenEndpoint != "" {
			refreshed, err := refreshOAuthToken(tok)
			if err == nil {
				refreshed.MCPName = mcp.Name
				refreshed.ProjectID = mcp.ProjectID
				_ = s.LocalStore.UpsertMCPOAuthToken(*refreshed)
				tok = refreshed
			} else {
				log.Printf("[mcp-proxy] %s: token refresh failed: %v", mcp.Name, err)
				// Forward without auth; upstream will 401 and trigger re-auth.
				return "", nil
			}
		} else {
			// No refresh token; forward without auth so upstream triggers 401.
			return "", nil
		}
	}

	return "Bearer " + tok.AccessToken, nil
}

// broadcastNeedsAuth pushes a connector:needs_auth event to all WS clients,
// prompting the frontend to initiate the OAuth authorization flow.
// projectID is included so the frontend can pass it to oauthStart.
func (s *Server) broadcastNeedsAuth(mcpName, projectID string) {
	if s.Hub == nil {
		return
	}
	data, err := json.Marshal(map[string]any{"type": "connector:needs_auth", "name": mcpName, "project_id": projectID})
	if err == nil {
		s.Hub.BroadcastRaw(data)
	}
}

// broadcastReauth kept for backwards compatibility.
func (s *Server) broadcastReauth(mcpName string) {
	s.broadcastNeedsAuth(mcpName, "")
}

// refreshOAuthToken uses the refresh_token grant to get a new access token.
func refreshOAuthToken(tok *store.MCPOAuthToken) (*store.MCPOAuthToken, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {tok.ClientID},
	}
	resp, err := http.PostForm(tok.TokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh returned %d: %s", resp.StatusCode, body)
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	out := &store.MCPOAuthToken{
		AccessToken:   tr.AccessToken,
		TokenEndpoint: tok.TokenEndpoint,
		ClientID:      tok.ClientID,
		UpdatedAt:     time.Now(),
	}
	if tr.RefreshToken != "" {
		out.RefreshToken = tr.RefreshToken
	} else {
		out.RefreshToken = tok.RefreshToken // keep old refresh token
	}
	if tr.ExpiresIn > 0 {
		out.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return out, nil
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

	if s.LocalStore == nil {
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
	clientID, authServerURL, err := resolveOAuthConfig(mcp)
	if err != nil {
		return "", err
	}

	// If no client_id yet, check a previously stored token record (e.g. from a prior DCR).
	if clientID == "" {
		if existing, _ := s.LocalStore.GetMCPOAuthToken(name, mcp.ProjectID); existing != nil && existing.ClientID != "" {
			clientID = existing.ClientID
		}
	}

	// Build redirect URI (our callback) — needed for DCR registration.
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/api/connectors/oauth/callback", s.ListenPort)

	// Discover authorization endpoint.
	disc, err := discoverOAuthEndpoints(authServerURL)
	if err != nil {
		return "", fmt.Errorf("OAuth discovery failed: %w", err)
	}

	// If still no client_id, try Dynamic Client Registration (RFC 7591).
	if clientID == "" {
		if disc.RegistrationEndpoint == "" {
			return "", fmt.Errorf("OAuth client_id not configured for MCP %s and server does not support Dynamic Client Registration", name)
		}
		clientID, err = dynamicClientRegister(disc.RegistrationEndpoint, callbackURL)
		if err != nil {
			return "", fmt.Errorf("OAuth client registration failed: %w", err)
		}
		log.Printf("[oauth] registered new client via DCR for MCP %s: %s", name, clientID)
	}

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

	// Persist pending OAuth state.
	pending := store.MCPOAuthToken{
		MCPName:       name,
		ProjectID:     mcp.ProjectID,
		TokenEndpoint: disc.TokenEndpoint,
		ClientID:      clientID,
		State:         state,
		CodeVerifier:  verifier,
		UpdatedAt:     time.Now(),
	}
	if err := s.LocalStore.UpsertMCPOAuthToken(pending); err != nil {
		return "", fmt.Errorf("store state: %w", err)
	}

	// Build authorization URL.
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {callbackURL},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	return disc.AuthorizationEndpoint + "?" + params.Encode(), nil
}

// ---------------------------------------------------------------------------
// MCP Probe: detect auth requirement after install
// ---------------------------------------------------------------------------

// HandleMCPProbe sends an MCP initialize request to the upstream server (without
// any auth headers) to detect whether OAuth is required. If the upstream returns
// 401, it broadcasts connector:needs_auth via WebSocket — the same event the
// chat-time proxy emits — so the frontend shows the standard "Authorize" toast.
func (s *Server) HandleMCPProbe(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	projectID := r.URL.Query().Get("project_id")

	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	mcp, err := s.resolveMCPForProxy(name, projectID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	// Only HTTP MCPs can require OAuth.
	targetURL, err := mcpTargetURL(mcp.Manifest)
	if err != nil {
		// Stdio/command MCP — no auth needed.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Send MCP initialize request without auth to detect 401.
	needs401, err := probeMCPNeedsAuth(targetURL.String())
	if err != nil {
		// Connection failure or non-standard response — don't block the install.
		log.Printf("[mcp-probe] %s: probe failed (ignoring): %v", name, err)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if needs401 {
		// Reuse the same WebSocket event as the chat-time 401 path.
		// The frontend's existing connector:needs_auth handler shows the "Authorize" toast.
		s.broadcastNeedsAuth(name, mcp.ProjectID)
	}

	w.WriteHeader(http.StatusNoContent)
}

// probeMCPNeedsAuth sends an MCP initialize request to url and returns true if
// the server responds with HTTP 401 (auth required). A 5-second timeout is used
// so the probe never stalls the install flow.
func probeMCPNeedsAuth(mcpURL string) (bool, error) {
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
	if code == "" || state == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}
	if s.LocalStore == nil {
		http.Error(w, "local store not available", http.StatusServiceUnavailable)
		return
	}

	// Look up pending OAuth flow by state.
	pending, err := s.LocalStore.GetMCPOAuthTokenByState(state)
	if err != nil || pending == nil {
		http.Error(w, "unknown or expired OAuth state", http.StatusBadRequest)
		return
	}

	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/api/connectors/oauth/callback", s.ListenPort)

	// Exchange code for token.
	tok, err := exchangeCode(pending.TokenEndpoint, pending.ClientID, code, pending.CodeVerifier, callbackURL)
	if err != nil {
		http.Error(w, "token exchange failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Store token (overwrites pending state).
	tok.MCPName = pending.MCPName
	tok.ProjectID = pending.ProjectID
	tok.TokenEndpoint = pending.TokenEndpoint
	tok.ClientID = pending.ClientID
	tok.State = ""
	tok.CodeVerifier = ""
	tok.UpdatedAt = time.Now()
	if err := s.LocalStore.UpsertMCPOAuthToken(*tok); err != nil {
		http.Error(w, "store token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Upgrade the MCP's auth_type so the proxy uses the OAuth token on future calls.
	_ = s.LocalStore.UpdateInstalledMCPAuthType(pending.MCPName, pending.ProjectID, "oauth_discovery")

	// Notify frontend via WebSocket.
	if s.Hub != nil {
		if data, err := json.Marshal(map[string]any{"type": "connector:connected", "name": pending.MCPName}); err == nil {
			s.Hub.BroadcastRaw(data)
		}
	}

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
	var entry struct {
		URL   string `json:"url"`
		OAuth *struct {
			ClientID string `json:"clientId"`
		} `json:"oauth"`
	}
	if err := json.Unmarshal([]byte(mcp.Manifest), &entry); err != nil {
		return "", "", fmt.Errorf("parse manifest: %w", err)
	}

	if entry.OAuth != nil && entry.OAuth.ClientID != "" {
		return entry.OAuth.ClientID, entry.URL, nil
	}

	// oauth_discovery: use env_values for client_id if provided.
	if mcp.EnvValues != "" && mcp.EnvValues != "{}" {
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

	// The authorization server is the MCP server's origin.
	u, err := url.Parse(entry.URL)
	if err != nil {
		return "", "", fmt.Errorf("parse MCP URL: %w", err)
	}
	// clientID may be empty — HandleOAuthStart will attempt DCR if the server supports it.
	return clientID, u.Scheme + "://" + u.Host, nil
}

// oauthDiscoveryDoc holds the fields we care about from RFC 9728 discovery.
type oauthDiscoveryDoc struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"` // RFC 7591 DCR
}

// discoverOAuthEndpoints performs RFC 9728 OAuth discovery and returns the full doc.
func discoverOAuthEndpoints(serverBase string) (*oauthDiscoveryDoc, error) {
	discoveryURL := strings.TrimRight(serverBase, "/") + "/.well-known/oauth-authorization-server"
	resp, err := http.Get(discoveryURL) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("discovery request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery returned %d", resp.StatusCode)
	}
	var doc oauthDiscoveryDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse discovery doc: %w", err)
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" {
		return nil, fmt.Errorf("incomplete discovery doc")
	}
	return &doc, nil
}

// dynamicClientRegister performs RFC 7591 Dynamic Client Registration.
// Returns the client_id assigned by the authorization server.
func dynamicClientRegister(registrationEndpoint, redirectURI string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"redirect_uris":            []string{redirectURI},
		"token_endpoint_auth_method": "none", // public client (PKCE)
		"grant_types":              []string{"authorization_code"},
		"response_types":           []string{"code"},
	})
	resp, err := http.Post(registrationEndpoint, "application/json", strings.NewReader(string(body))) //nolint:gosec
	if err != nil {
		return "", fmt.Errorf("registration request: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registration returned %d: %s", resp.StatusCode, rb)
	}
	var reg struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(rb, &reg); err != nil || reg.ClientID == "" {
		return "", fmt.Errorf("no client_id in registration response")
	}
	return reg.ClientID, nil
}

// exchangeCode exchanges an authorization code for tokens using PKCE.
func exchangeCode(tokenEndpoint, clientID, code, verifier, redirectURI string) (*store.MCPOAuthToken, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	resp, err := http.PostForm(tokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, body)
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("no access_token in response")
	}
	tok := &store.MCPOAuthToken{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
	}
	if tr.ExpiresIn > 0 {
		tok.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return tok, nil
}

// tokenResponse is the standard OAuth token endpoint response shape.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
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
