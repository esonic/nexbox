package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/esonic/nexbox/store"
)

// The fixture issues durable grants only when registration, consent, code
// exchange and every refresh preserve the same OAuth contract.
func TestMCPOAuthDiscoveredDurableGrantRotatesAcrossRestart(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	var origin string
	var registrations, refreshes atomic.Int32
	var omitConsentRefresh atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q, scope="tools:read"`, origin+"/metadata"))
			w.WriteHeader(401)
		case "/metadata":
			json.NewEncoder(w).Encode(map[string]any{"resource": origin + "/mcp", "authorization_servers": []string{origin + "/issuer"}, "scopes_supported": []string{"tools:read"}})
		case "/.well-known/oauth-authorization-server/issuer":
			json.NewEncoder(w).Encode(map[string]any{"authorization_endpoint": origin + "/authorize", "token_endpoint": origin + "/token", "registration_endpoint": origin + "/register", "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"client_secret_basic"}, "scopes_supported": []string{"tools:read", "offline_access"}})
		case "/register":
			registrations.Add(1)
			var metadata struct {
				Grants []string `json:"grant_types"`
				Scope  string   `json:"scope"`
				Method string   `json:"token_endpoint_auth_method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&metadata); err != nil {
				t.Error(err)
			}
			if !slices.Contains(metadata.Grants, "refresh_token") || !slices.Contains(strings.Fields(metadata.Scope), "offline_access") || metadata.Method != "client_secret_basic" {
				t.Error("registration did not request a durable grant")
			}
			json.NewEncoder(w).Encode(map[string]string{"client_id": "fixture-client", "client_secret": "fixture-secret", "token_endpoint_auth_method": "client_secret_basic"})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			id, secret, ok := r.BasicAuth()
			if !ok || id != "fixture-client" || secret != "fixture-secret" || r.Form.Get("client_secret") != "" || r.Form.Get("resource") != origin+"/mcp" {
				t.Error("token exchange lost client authentication/resource")
			}
			if r.Form.Get("grant_type") == "refresh_token" {
				cycle := refreshes.Add(1)
				if r.Form.Get("refresh_token") != fmt.Sprintf("fixture-refresh-%d", cycle-1) || !sameMCPOAuthScope(r.Form.Get("scope"), "tools:read offline_access") {
					t.Error("refresh lost scope or rotated credential")
				}
				json.NewEncoder(w).Encode(map[string]string{"access_token": fmt.Sprintf("fixture-access-%d", cycle), "refresh_token": fmt.Sprintf("fixture-refresh-%d", cycle), "expires_in": "3600"})
			} else {
				response := map[string]string{"access_token": "fixture-access-consent", "expires_in": "3600", "scope": "offline_access tools:read"}
				if !omitConsentRefresh.Load() {
					response["refresh_token"] = "fixture-refresh-0"
				}
				json.NewEncoder(w).Encode(response)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	origin = upstream.URL
	mcp := testRuntimeMCP(t, s, origin+"/mcp", "project")
	authURL, err := s.startOAuthFlow(&mcp)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if !sameMCPOAuthScope(u.Query().Get("scope"), "tools:read offline_access") || u.Query().Get("resource") != origin+"/mcp" {
		t.Fatal("consent URL omitted durable access scope/resource")
	}
	complete := func(authURL string) {
		t.Helper()
		w := httptest.NewRecorder()
		s.HandleOAuthCallback(w, httptest.NewRequest("GET", reviewOAuthCallback(t, authURL), nil))
		if w.Code != 200 {
			t.Fatalf("callback status=%d body=%s", w.Code, w.Body.String())
		}
	}
	complete(authURL)
	// Re-consent omits a refresh token, as providers often do after the first
	// grant. Equivalent reordered scopes must not erase the stored refresh token.
	omitConsentRefresh.Store(true)
	authURL, err = s.startOAuthFlow(&mcp)
	if err != nil {
		t.Fatal(err)
	}
	complete(authURL)
	if testOAuthConnection(t, s, &mcp).Token.RefreshToken != "fixture-refresh-0" {
		t.Fatal("re-consent erased the existing refresh token")
	}
	for cycle := 1; cycle <= 5; cycle++ {
		connection := testOAuthConnection(t, s, &mcp)
		connection.Token.ExpiresAt = time.Now().Add(-time.Minute)
		if err := s.Store.SaveMCPOAuthConnection(*connection); err != nil {
			t.Fatal(err)
		}
		// A new Server discards all in-memory locks/state; client auth, resource,
		// scope and the latest refresh token must come from persistent storage.
		s = &Server{Store: s.Store}
		s.refreshInstalledMCPOAuth(context.Background())
		if header, err := s.oauthHeader(&mcp); err != nil || header != fmt.Sprintf("Bearer fixture-access-%d", cycle) {
			t.Fatalf("automatic refresh cycle %d failed: %v", cycle, err)
		}
	}
	if registrations.Load() != 1 || refreshes.Load() != 5 {
		t.Fatalf("unexpected registration/refresh count: %d/%d", registrations.Load(), refreshes.Load())
	}
}

func TestMCPOAuthUpgradesLegacyDCRWithoutRefreshToken(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	var origin string
	var registered atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]string{"authorization_endpoint": origin + "/authorize", "token_endpoint": origin + "/token", "registration_endpoint": origin + "/register"})
		case "/register":
			registered.Add(1)
			fmt.Fprint(w, `{"client_id":"fixture-upgraded"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	origin = upstream.URL
	mcp := testRuntimeMCP(t, s, origin+"/mcp", "project")
	seedLegacyOAuth(t, s, mcp, "fixture-unexpired")
	authURL, err := s.startOAuthFlow(&mcp)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	if u.Query().Get("client_id") != "fixture-upgraded" || registered.Load() != 1 {
		t.Fatal("reused a legacy authorization_code-only registration forever")
	}
	if header, err := s.oauthHeader(&mcp); err != nil || header != "Bearer fixture-unexpired" {
		t.Fatal("pending registration upgrade invalidated active access")
	}
}

func TestMCPOAuthShortLivedTokensAreNotRefreshedEveryRequest(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"access_token":"fixture-renewed","refresh_token":"fixture-rotated","expires_in":30}`)
	}))
	defer upstream.Close()
	mcp := testRuntimeMCP(t, s, upstream.URL, "project")
	seedLegacyOAuth(t, s, mcp, "fixture-old")
	connection := testOAuthConnection(t, s, &mcp)
	connection.Token.RefreshToken = "fixture-refresh"
	connection.Token.TokenEndpoint = upstream.URL
	connection.Token.ExpiresAt = time.Now().Add(-time.Minute)
	if err := s.Store.SaveMCPOAuthConnection(*connection); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if header, err := s.oauthHeader(&mcp); err != nil || header != "Bearer fixture-renewed" {
			t.Fatal("short-lived token unavailable")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("short-lived token refreshed on every request: %d", calls.Load())
	}
}

func TestMCPOAuthUnknownExpiryIsMaintainedAndTemporaryErrorsStayTemporary(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"access_token":"fixture-renewed"}`)
	}))
	defer upstream.Close()
	mcp := testRuntimeMCP(t, s, upstream.URL, "project")
	seedLegacyOAuth(t, s, mcp, "fixture-old")
	c := testOAuthConnection(t, s, &mcp)
	c.Token.RefreshToken = "fixture-refresh"
	c.Token.TokenEndpoint = upstream.URL
	c.Token.ObtainedAt = time.Now().Add(-2 * time.Hour)
	if err := s.Store.SaveMCPOAuthConnection(*c); err != nil {
		t.Fatal(err)
	}
	s.refreshInstalledMCPOAuth(context.Background())
	s.refreshInstalledMCPOAuth(context.Background())
	if calls.Load() != 1 || testOAuthConnection(t, s, &mcp).Token.RefreshToken != "fixture-refresh" {
		t.Fatal("unknown expiry stopped maintenance or caused repeated refresh")
	}
	for _, status := range []int{429, 500, 502, 503} {
		if (&oauthRefreshError{status: status, code: "invalid_grant"}).requiresAuthorization() {
			t.Fatalf("HTTP %d treated as revoked consent", status)
		}
	}
}

func TestMCPOAuthTokenErrorsNeverExposeResponseBodies(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		fmt.Fprint(w, `{"error":"temporarily_unavailable","debug":"fixture-sensitive-response"}`)
	}))
	defer upstream.Close()
	_, err := refreshOAuthToken(&store.MCPOAuthToken{ClientID: "fixture-client", TokenEndpoint: upstream.URL, RefreshToken: "fixture-refresh"})
	if err == nil || strings.Contains(err.Error(), "fixture-sensitive-response") {
		t.Fatal("token endpoint response body escaped into diagnostics")
	}
}

func TestMCPOAuthOIDCDiscoveryKeepsIssuerPath(t *testing.T) {
	var origin string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tenant/.well-known/openid-configuration" {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"authorization_endpoint": origin + "/authorize", "token_endpoint": origin + "/token"})
	}))
	defer upstream.Close()
	origin = upstream.URL
	if _, err := discoverOAuthIssuer(context.Background(), origin+"/tenant"); err != nil {
		t.Fatalf("OIDC issuer-path metadata was not discovered: %v", err)
	}
}

func TestMCPOAuthDiscoveryRejectsOtherIssuer(t *testing.T) {
	for _, matching := range []bool{false, true} {
		t.Run(fmt.Sprintf("matching=%t", matching), func(t *testing.T) {
			var origin string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				issuer := origin + "/tenant-a"
				if !matching {
					issuer = origin + "/tenant-b"
				}
				json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "authorization_endpoint": origin + "/authorize", "token_endpoint": origin + "/token", "registration_endpoint": origin + "/register"})
			}))
			defer upstream.Close()
			origin = upstream.URL
			_, err := discoverOAuthIssuer(context.Background(), origin+"/tenant-a")
			if matching && err != nil {
				t.Fatal("matching issuer was rejected")
			}
			if !matching && err == nil {
				t.Fatal("different tenant's issuer metadata was accepted")
			}
		})
	}
}

func TestMCPOAuthAuthorizationEndpointRetainsQuery(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	var origin string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"authorization_endpoint": origin + "/authorize?tenant=fixture-tenant", "token_endpoint": origin + "/token"})
	}))
	defer upstream.Close()
	origin = upstream.URL
	mcp := reviewOAuthMCP(t, s, origin+"/mcp", "project")
	authURL, err := s.startOAuthFlow(&mcp)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("tenant") != "fixture-tenant" || u.Query().Get("client_id") != "fixture-client" || u.Query().Get("state") == "" {
		t.Fatal("authorization endpoint query or OAuth parameters were corrupted")
	}
}

func TestMCPOAuthDelayed401DoesNotReuseExpiredReplacement(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"access_token":"fixture-renewed","expires_in":3600}`)
	}))
	defer upstream.Close()
	mcp := testRuntimeMCP(t, s, upstream.URL, "project")
	seedLegacyOAuth(t, s, mcp, "fixture-replacement")
	c := testOAuthConnection(t, s, &mcp)
	c.Token.TokenEndpoint = upstream.URL
	c.Token.RefreshToken = "fixture-refresh"
	c.Token.ExpiresAt = time.Now().Add(-time.Minute)
	if err := s.Store.SaveMCPOAuthConnection(*c); err != nil {
		t.Fatal(err)
	}
	header, err := s.managedOAuthHeader(&mcp, "Bearer fixture-original")
	if err != nil || header != "Bearer fixture-renewed" || calls.Load() != 1 {
		t.Fatalf("delayed 401 reused an expired replacement instead of refreshing: %v", err)
	}
}
