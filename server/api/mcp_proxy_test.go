package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"gitlab.okg.com/sre_ai/boxnex/store"
)

// ---------------------------------------------------------------------------
// PKCE helpers
// ---------------------------------------------------------------------------

func TestGeneratePKCE(t *testing.T) {
	v1, c1, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE: %v", err)
	}
	v2, c2, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE: %v", err)
	}
	if v1 == "" || c1 == "" {
		t.Error("verifier/challenge must not be empty")
	}
	if v1 == v2 || c1 == c2 {
		t.Error("each call should produce different values")
	}
}

func TestRandomBase64(t *testing.T) {
	a, err := randomBase64(16)
	if err != nil {
		t.Fatalf("randomBase64: %v", err)
	}
	b, err := randomBase64(16)
	if err != nil {
		t.Fatalf("randomBase64: %v", err)
	}
	if a == b {
		t.Error("expected different random values")
	}
}

// ---------------------------------------------------------------------------
// buildForwardURL
// ---------------------------------------------------------------------------

func TestBuildForwardURL(t *testing.T) {
	base, _ := url.Parse("https://mcp.linear.app/mcp")

	fwd, err := buildForwardURL(base, "tools/list", "session=abc")
	if err != nil {
		t.Fatalf("buildForwardURL: %v", err)
	}
	if fwd.Host != "mcp.linear.app" {
		t.Errorf("Host = %q", fwd.Host)
	}
	if fwd.Path != "/mcp/tools/list" {
		t.Errorf("Path = %q, want /mcp/tools/list", fwd.Path)
	}
	if fwd.RawQuery != "session=abc" {
		t.Errorf("Query = %q", fwd.RawQuery)
	}
}

func TestBuildForwardURL_NoTail(t *testing.T) {
	base, _ := url.Parse("https://mcp.linear.app/mcp")
	fwd, err := buildForwardURL(base, "", "")
	if err != nil {
		t.Fatalf("buildForwardURL: %v", err)
	}
	if fwd.Path != "/mcp" {
		t.Errorf("Path = %q, want /mcp", fwd.Path)
	}
}

// ---------------------------------------------------------------------------
// mcpTargetURL
// ---------------------------------------------------------------------------

func TestMcpTargetURL(t *testing.T) {
	manifest := `{"key":"linear","type":"http","url":"https://mcp.linear.app/mcp"}`
	u, err := mcpTargetURL(manifest)
	if err != nil {
		t.Fatalf("mcpTargetURL: %v", err)
	}
	if u.Host != "mcp.linear.app" {
		t.Errorf("Host = %q", u.Host)
	}
}

func TestMcpTargetURL_NoURL(t *testing.T) {
	manifest := `{"key":"stdio-thing","type":"stdio","cmd":"npx"}`
	_, err := mcpTargetURL(manifest)
	if err == nil {
		t.Error("expected error for stdio manifest (no url)")
	}
}

// ---------------------------------------------------------------------------
// apikeyHeader
// ---------------------------------------------------------------------------

func TestApikeyHeader(t *testing.T) {
	s := &Server{}
	mcp := &store.InstalledMCP{
		AuthType:  "apikey",
		EnvValues: `{"GITHUB_PERSONAL_ACCESS_TOKEN":"ghp_test123"}`,
	}
	hdr, err := s.apikeyHeader(mcp)
	if err != nil {
		t.Fatalf("apikeyHeader: %v", err)
	}
	if hdr != "Bearer ghp_test123" {
		t.Errorf("header = %q, want 'Bearer ghp_test123'", hdr)
	}
}

func TestApikeyHeader_Empty(t *testing.T) {
	s := &Server{}
	mcp := &store.InstalledMCP{AuthType: "apikey", EnvValues: "{}"}
	hdr, err := s.apikeyHeader(mcp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hdr != "" {
		t.Errorf("expected empty header for empty env_values, got %q", hdr)
	}
}

// ---------------------------------------------------------------------------
// discoverOAuthEndpoints (integration-style, uses httptest server)
// ---------------------------------------------------------------------------

func TestDiscoverOAuthEndpoints(t *testing.T) {
	discovery := map[string]string{
		"authorization_endpoint": "https://auth.example.com/oauth/authorize",
		"token_endpoint":         "https://auth.example.com/oauth/token",
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(discovery)
	}))
	defer ts.Close()

	authEP, tokEP, err := discoverOAuthEndpoints(ts.URL)
	if err != nil {
		t.Fatalf("discoverOAuthEndpoints: %v", err)
	}
	if authEP != discovery["authorization_endpoint"] {
		t.Errorf("authEndpoint = %q", authEP)
	}
	if tokEP != discovery["token_endpoint"] {
		t.Errorf("tokenEndpoint = %q", tokEP)
	}
}

func TestDiscoverOAuthEndpoints_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()

	_, _, err := discoverOAuthEndpoints(ts.URL)
	if err == nil {
		t.Error("expected error for 404 discovery")
	}
}

// ---------------------------------------------------------------------------
// resolveOAuthConfig
// ---------------------------------------------------------------------------

func TestResolveOAuthConfig_Explicit(t *testing.T) {
	// Slack-style: explicit clientId in manifest
	mcp := &store.InstalledMCP{
		AuthType:  "oauth",
		Manifest:  `{"type":"http","url":"https://mcp.slack.com/mcp","oauth":{"clientId":"slack-client-id","callbackPort":3118}}`,
		EnvValues: "{}",
	}
	clientID, serverBase, err := resolveOAuthConfig(mcp)
	if err != nil {
		t.Fatalf("resolveOAuthConfig: %v", err)
	}
	if clientID != "slack-client-id" {
		t.Errorf("clientID = %q, want slack-client-id", clientID)
	}
	if serverBase != "https://mcp.slack.com/mcp" {
		t.Errorf("serverBase = %q", serverBase)
	}
}

func TestResolveOAuthConfig_Discovery(t *testing.T) {
	// GitLab-style: no explicit oauth, derive server base from URL origin
	mcp := &store.InstalledMCP{
		AuthType:  "oauth_discovery",
		Manifest:  `{"type":"http","url":"https://gitlab.com/api/v4/mcp"}`,
		EnvValues: "{}",
	}
	_, serverBase, err := resolveOAuthConfig(mcp)
	if err != nil {
		t.Fatalf("resolveOAuthConfig: %v", err)
	}
	if serverBase != "https://gitlab.com" {
		t.Errorf("serverBase = %q, want https://gitlab.com", serverBase)
	}
}

// ---------------------------------------------------------------------------
// oauthHeader — with expired token triggers refresh attempt
// ---------------------------------------------------------------------------

func TestOAuthHeader_ExpiredNoRefresh(t *testing.T) {
	// Build a minimal server with test store + expired token, no refresh token.
	db, _ := store.New(t.TempDir())
	defer db.Close()
	ls := store.NewSQLiteStore(db.DB)

	expiredTok := store.MCPOAuthToken{
		MCPName:     "stripe",
		ProjectID:   "",
		AccessToken: "expired-access",
		ExpiresAt:   time.Now().Add(-1 * time.Minute), // already expired
		UpdatedAt:   time.Now(),
	}
	ls.UpsertMCPOAuthToken(expiredTok)

	srv := &Server{LocalStore: ls, Hub: NewChatHub()}
	mcp := &store.InstalledMCP{Name: "stripe", ProjectID: "", AuthType: "oauth_discovery"}
	_, err := srv.oauthHeader(mcp)
	// Should fail because expired and no refresh token
	if err == nil {
		t.Error("expected error for expired token with no refresh_token")
	}
}

func TestOAuthHeader_Valid(t *testing.T) {
	db, _ := store.New(t.TempDir())
	defer db.Close()
	ls := store.NewSQLiteStore(db.DB)

	validTok := store.MCPOAuthToken{
		MCPName:     "linear",
		ProjectID:   "",
		AccessToken: "valid-access-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour), // far in the future
		UpdatedAt:   time.Now(),
	}
	ls.UpsertMCPOAuthToken(validTok)

	srv := &Server{LocalStore: ls, Hub: NewChatHub()}
	mcp := &store.InstalledMCP{Name: "linear", ProjectID: "", AuthType: "oauth_discovery"}
	hdr, err := srv.oauthHeader(mcp)
	if err != nil {
		t.Fatalf("oauthHeader: %v", err)
	}
	if hdr != "Bearer valid-access-token" {
		t.Errorf("header = %q", hdr)
	}
}
