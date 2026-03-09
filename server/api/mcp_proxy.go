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
	projectID := r.URL.Query().Get("project_id") // optional hint

	mcp, err := s.resolveMCPForProxy(name, projectID)
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
	tail := chi.URLParam(r, "*")
	fwdURL, err := buildForwardURL(targetURL, tail, r.URL.RawQuery)
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
			// Remove hop-by-hop headers that shouldn't be forwarded.
			req.Header.Del("X-Forwarded-For")
		},
		// Disable response buffering so SSE streams through immediately.
		FlushInterval: -1,
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
// For apikey: reads the key from env_values.
// For oauth_discovery/oauth: lazily refreshes the stored token.
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
		return "", fmt.Errorf("MCP %q: OAuth not connected", mcp.Name)
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
				s.broadcastReauth(mcp.Name)
				return "", fmt.Errorf("token expired for MCP %q, please reconnect", mcp.Name)
			}
		} else {
			// No refresh token available — cannot recover, require re-auth.
			s.broadcastReauth(mcp.Name)
			return "", fmt.Errorf("token expired for MCP %q and no refresh_token available, please reconnect", mcp.Name)
		}
	}

	return "Bearer " + tok.AccessToken, nil
}

// broadcastReauth pushes a connector:reauth_required event to all WS clients.
func (s *Server) broadcastReauth(mcpName string) {
	if s.Hub == nil {
		return
	}
	data, err := json.Marshal(map[string]any{"type": "connector:reauth_required", "name": mcpName})
	if err == nil {
		s.Hub.BroadcastRaw(data)
	}
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
// POST /api/connectors/{name}/oauth/start
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

	// Determine OAuth client ID and authorization server URL.
	clientID, authServerURL, err := resolveOAuthConfig(mcp)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Discover authorization endpoint.
	authEndpoint, tokenEndpoint, err := discoverOAuthEndpoints(authServerURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, "OAuth discovery failed: "+err.Error())
		return
	}

	// Generate PKCE verifier + challenge.
	verifier, challenge, err := generatePKCE()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PKCE generation failed")
		return
	}

	// Generate random state.
	state, err := randomBase64(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "state generation failed")
		return
	}

	// Build redirect URI (our callback).
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/api/connectors/oauth/callback", s.ListenPort)

	// Persist pending OAuth state.
	pending := store.MCPOAuthToken{
		MCPName:       name,
		ProjectID:     mcp.ProjectID,
		TokenEndpoint: tokenEndpoint,
		ClientID:      clientID,
		State:         state,
		CodeVerifier:  verifier,
		UpdatedAt:     time.Now(),
	}
	if err := s.LocalStore.UpsertMCPOAuthToken(pending); err != nil {
		writeError(w, http.StatusInternalServerError, "store state: "+err.Error())
		return
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
	authURL := authEndpoint + "?" + params.Encode()

	writeJSON(w, http.StatusOK, OAuthStartResponse{AuthURL: authURL})
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

	// Notify frontend via WebSocket.
	if data, err := json.Marshal(map[string]any{"type": "connector:connected", "name": pending.MCPName}); err == nil {
		s.Hub.BroadcastRaw(data)
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
// For oauth (explicit): reads clientId from the manifest's oauth field.
// For oauth_discovery: uses the MCP's URL as the authorization server base.
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
		// Slack-style: explicit OAuth config with clientId and the URL is the server base.
		return entry.OAuth.ClientID, entry.URL, nil
	}

	// oauth_discovery: use env_values for client_id if provided, else empty (public client).
	if mcp.EnvValues != "" && mcp.EnvValues != "{}" {
		var vals map[string]string
		if json.Unmarshal([]byte(mcp.EnvValues), &vals) == nil {
			if cid, ok := vals["CLIENT_ID"]; ok && cid != "" {
				clientID = cid
			}
		}
	}

	if entry.URL == "" {
		return "", "", fmt.Errorf("MCP has no URL for OAuth discovery")
	}

	// The authorization server is the origin of the MCP URL.
	u, err := url.Parse(entry.URL)
	if err != nil {
		return "", "", fmt.Errorf("parse MCP URL: %w", err)
	}
	return clientID, u.Scheme + "://" + u.Host, nil
}

// discoverOAuthEndpoints performs RFC 9728 OAuth discovery.
func discoverOAuthEndpoints(serverBase string) (authEndpoint, tokenEndpoint string, err error) {
	discoveryURL := strings.TrimRight(serverBase, "/") + "/.well-known/oauth-authorization-server"
	resp, err := http.Get(discoveryURL) //nolint:gosec // serverBase is from our own config
	if err != nil {
		return "", "", fmt.Errorf("discovery request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("discovery returned %d", resp.StatusCode)
	}
	var doc struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", "", fmt.Errorf("parse discovery doc: %w", err)
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" {
		return "", "", fmt.Errorf("incomplete discovery doc")
	}
	return doc.AuthorizationEndpoint, doc.TokenEndpoint, nil
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
