package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/esonic/nexbox/store"
	"github.com/go-chi/chi/v5"
)

func testOAuthConnection(t *testing.T, s *Server, mcp *store.InstalledMCP) *store.MCPOAuthConnection {
	t.Helper()
	key, err := mcpOAuthConfigKey(mcp)
	if err != nil {
		t.Fatal(err)
	}
	unlock := s.lockMCPOAuth(key)
	defer unlock()
	c, err := s.resolveMCPOAuthConnection(mcp, key)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatal("missing connection")
	}
	return c
}

func TestMCPOAuthManagementRoutesAreNotExposed(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	r := chi.NewRouter()
	s.RegisterMarketplaceRoutes(r)
	for _, row := range []struct{ method, path string }{
		{"GET", "/connectors/example/oauth/connections"},
		{"POST", "/connectors/example/oauth/connection"},
		{"DELETE", "/connectors/example/oauth/connections/id"},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(row.method, row.path, nil))
		if w.Code != 404 {
			t.Fatalf("unnecessary management route still exposed: %s %s", row.method, row.path)
		}
	}
}

func TestMCPOAuthLastUninstallDeletesUnmigratedGrant(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	a := testRuntimeMCP(t, s, "https://example.com/mcp", "a")
	seedLegacyOAuth(t, s, a, "fixture-access")
	if err := s.uninstallMCPScopes(a.Name, a.ProjectID, false); err != nil {
		t.Fatal(err)
	}
	legacy, err := s.Store.GetMCPOAuthToken(a.Name, a.ProjectID)
	if err != nil || legacy != nil {
		t.Fatal("last uninstall retained legacy grant")
	}
	b := testRuntimeMCP(t, s, "https://example.com/mcp", "b")
	if header, err := s.oauthHeader(&b); err != nil || header != "" {
		t.Fatal("reinstall revived old authorization")
	}
}

func TestMCPOAuthInterruptedUninstallIsCleanedBeforeReuse(t *testing.T) {
	for _, trigger := range []string{"maintenance", "install"} {
		t.Run(trigger, func(t *testing.T) {
			s := newMCPRuntimeTestServer(t)
			a := testRuntimeMCP(t, s, "https://example.com/mcp", "a")
			seedLegacyOAuth(t, s, a, "fixture-old")
			c := testOAuthConnection(t, s, &a)
			// Simulate a shutdown after the installation disappeared but before
			// authorization cleanup finished.
			if err := s.Store.DeleteInstalledMCP(a.Name, a.ProjectID); err != nil {
				t.Fatal(err)
			}
			if trigger == "maintenance" {
				s.refreshInstalledMCPOAuth(context.Background())
			} else {
				r := chi.NewRouter()
				s.RegisterMarketplaceRoutes(r)
				body, _ := json.Marshal(map[string]any{"name": a.Name, "project_id": a.ProjectID, "manifest": json.RawMessage(a.Manifest), "auth_type": "oauth"})
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest("POST", "/marketplace/installed-mcps", strings.NewReader(string(body))))
				if w.Code != 200 {
					t.Fatalf("reinstall status=%d", w.Code)
				}
			}
			connections, err := s.Store.ListMCPOAuthConnections(c.ConfigKey)
			if err != nil || len(connections) != 0 {
				t.Fatal("ended authorization survived interrupted uninstall")
			}
		})
	}
}

func TestMCPOAuthGlobalInstallKeepsGrantUntilLastUninstall(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	global := testRuntimeMCP(t, s, "https://example.com/mcp", "")
	seedLegacyOAuth(t, s, global, "fixture-shared")
	project := testRuntimeMCP(t, s, "https://example.com/mcp", "project")
	if _, err := s.oauthHeader(&project); err != nil {
		t.Fatal(err)
	}
	if err := s.uninstallMCPScopes(project.Name, project.ProjectID, false); err != nil {
		t.Fatal(err)
	}
	if header, err := s.oauthHeader(&global); err != nil || header != "Bearer fixture-shared" {
		t.Fatal("global installation lost shared authorization")
	}
	key, err := mcpOAuthConfigKey(&global)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := s.Store.GetMCPOAuthLifecycle(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Store.SaveMCPOAuthPending(store.MCPOAuthConnection{ID: mcpOAuthConnectionID(key), ConfigKey: key, Generation: generation, Token: store.MCPOAuthToken{MCPName: global.Name, State: "fixture-pending", UpdatedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := s.uninstallMCPScopes(global.Name, "", false); err != nil {
		t.Fatal(err)
	}
	connections, err := s.Store.ListMCPOAuthConnections(key)
	if err != nil || len(connections) != 0 {
		t.Fatal("last global uninstall retained credentials")
	}
	w := httptest.NewRecorder()
	s.HandleOAuthCallback(w, httptest.NewRequest("GET", "/api/connectors/oauth/callback?state=fixture-pending&code=fixture-code", nil))
	if w.Code != 400 {
		t.Fatal("callback survived last uninstall")
	}
}

func TestMCPOAuthLastUninstallWaitsForRefreshAndDoesNotResurrect(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(started)
		<-release
		fmt.Fprint(w, `{"access_token":"fixture-renewed","refresh_token":"fixture-rotated","expires_in":3600}`)
	}))
	defer upstream.Close()
	a := testRuntimeMCP(t, s, upstream.URL+"/mcp", "a")
	seedLegacyOAuth(t, s, a, "fixture-old")
	c := testOAuthConnection(t, s, &a)
	c.Token.RefreshToken = "fixture-refresh"
	c.Token.TokenEndpoint = upstream.URL
	c.Token.ExpiresAt = time.Now().Add(-time.Minute)
	if err := s.Store.SaveMCPOAuthConnection(*c); err != nil {
		t.Fatal(err)
	}
	refreshed := make(chan error, 1)
	go func() { _, err := s.oauthHeader(&a); refreshed <- err }()
	<-started
	removed := make(chan error, 1)
	go func() { removed <- s.uninstallMCPScopes(a.Name, a.ProjectID, false) }()
	close(release)
	if err := <-refreshed; err != nil {
		t.Fatal(err)
	}
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	s.refreshInstalledMCPOAuth(context.Background())
	connections, err := s.Store.ListMCPOAuthConnections(c.ConfigKey)
	if err != nil || len(connections) != 0 || calls.Load() != 1 {
		t.Fatal("in-flight refresh revived authorization after uninstall")
	}
	b := testRuntimeMCP(t, s, upstream.URL+"/mcp", "b")
	if header, err := s.oauthHeader(&b); err != nil || header != "" {
		t.Fatal("new install reused ended authorization")
	}
}

func TestMCPOAuthIdenticalLegacyGrantsCoalesce(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	a := testRuntimeMCP(t, s, "https://example.com/mcp", "a")
	b := testRuntimeMCP(t, s, "https://example.com/mcp", "b")
	seedLegacyOAuth(t, s, a, "fixture-shared")
	seedLegacyOAuth(t, s, b, "fixture-shared")
	if testOAuthConnection(t, s, &a).ID != testOAuthConnection(t, s, &b).ID {
		t.Fatal("identical grants kept separate rotating tokens")
	}
}

func TestMCPOAuthConfigUpdateCleansUnusedGrant(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	a := testRuntimeMCP(t, s, "https://example.com/mcp", "a")
	seedLegacyOAuth(t, s, a, "fixture-original")
	r := chi.NewRouter()
	s.RegisterMarketplaceRoutes(r)
	body, _ := json.Marshal(map[string]any{"name": a.Name, "project_id": a.ProjectID, "manifest": map[string]string{"url": "https://other.example/mcp"}, "auth_type": "oauth"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/marketplace/installed-mcps", strings.NewReader(string(body))))
	if w.Code != 200 {
		t.Fatalf("update status=%d body=%s", w.Code, w.Body.String())
	}
	updated, err := s.Store.GetInstalledMCP(a.Name, a.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if header, err := s.oauthHeader(updated); err != nil || header != "" {
		t.Fatal("old grant attached to changed resource")
	}
	b := testRuntimeMCP(t, s, "https://example.com/mcp", "b")
	if header, err := s.oauthHeader(&b); err != nil || header != "" {
		t.Fatal("configuration update retained an unused connection")
	}
}

func TestMCPOAuthConcurrentStartsShareFlowAndDCR(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	var registrations atomic.Int32
	var origin string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]string{"authorization_endpoint": origin + "/authorize", "token_endpoint": origin + "/token", "registration_endpoint": origin + "/register"})
		case "/register":
			registrations.Add(1)
			fmt.Fprint(w, `{"client_id":"fixture-client"}`)
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("resource") != origin+"/mcp" {
				t.Error("resource indicator missing from code exchange")
			}
			fmt.Fprint(w, `{"access_token":"fixture-granted","refresh_token":"fixture-refresh","expires_in":3600}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	origin = upstream.URL
	a := testRuntimeMCP(t, s, origin+"/mcp", "a")
	a.Manifest = fmt.Sprintf(`{"url":%q,"oauth":{"scope":"read tools","resource":%q}}`, origin+"/mcp", origin+"/mcp")
	if err := s.Store.UpsertInstalledMCP(a); err != nil {
		t.Fatal(err)
	}
	b := a
	b.ProjectID = "b"
	if err := s.Store.UpsertInstalledMCP(b); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	urls := make(chan string, 2)
	for _, mcp := range []store.InstalledMCP{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			authURL, err := s.startOAuthFlow(&mcp)
			if err != nil {
				t.Error(err)
			}
			urls <- authURL
		}()
	}
	wg.Wait()
	close(urls)
	first, second := <-urls, <-urls
	if first == "" || first != second || registrations.Load() != 1 {
		t.Fatal("duplicate PKCE/DCR flows created")
	}
	u, _ := url.Parse(first)
	if u.Query().Get("scope") != "read tools" || u.Query().Get("resource") != origin+"/mcp" {
		t.Fatal("requested permissions missing")
	}
	w := httptest.NewRecorder()
	s.HandleOAuthCallback(w, httptest.NewRequest("GET", "/api/connectors/oauth/callback?"+url.Values{"state": {u.Query().Get("state")}, "code": {"fixture-code"}}.Encode(), nil))
	if w.Code != 200 {
		t.Fatalf("callback status=%d body=%s", w.Code, w.Body.String())
	}
	for _, mcp := range []store.InstalledMCP{a, b} {
		if header, err := s.oauthHeader(&mcp); err != nil || header != "Bearer fixture-granted" {
			t.Fatal("shared authorization failed")
		}
	}

}

func seedLegacyOAuth(t *testing.T, s *Server, mcp store.InstalledMCP, access string) {
	t.Helper()
	if err := s.Store.UpsertInstalledMCP(mcp); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.UpsertMCPOAuthToken(store.MCPOAuthToken{MCPName: mcp.Name, ProjectID: mcp.ProjectID, AccessToken: access, ClientID: "fixture-client", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func TestMCPOAuthCrossProjectMigrationAndUninstall(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	a := testRuntimeMCP(t, s, "https://example.com/mcp", "a")
	seedLegacyOAuth(t, s, a, "fixture-access")
	b := testRuntimeMCP(t, s, "https://example.com/mcp", "b")
	b.AuthType = "none" // Discovery-only installs also reuse existing grants.
	if err := s.Store.UpdateInstalledMCPAuthType(b.Name, b.ProjectID, "none"); err != nil {
		t.Fatal(err)
	}
	headers, err := s.mcpRequestHeaders(&b)
	if err != nil || headers["Authorization"] != "Bearer fixture-access" {
		t.Fatalf("new project did not reuse grant: %v", err)
	}
	ca, cb := testOAuthConnection(t, s, &a), testOAuthConnection(t, s, &b)
	if ca.ID != cb.ID {
		t.Fatal("project connections differ")
	}
	legacy, err := s.Store.GetMCPOAuthToken(a.Name, a.ProjectID)
	if err != nil || legacy != nil {
		t.Fatal("legacy rotating token was retained")
	}
	if err := s.uninstallMCPScopes(a.Name, a.ProjectID, false); err != nil {
		t.Fatal(err)
	}
	if header, err := s.oauthHeader(&b); err != nil || header != "Bearer fixture-access" {
		t.Fatal("uninstall broke shared grant")
	}
	if err := s.uninstallMCPScopes(b.Name, b.ProjectID, false); err != nil {
		t.Fatal(err)
	}
	c := testRuntimeMCP(t, s, "https://example.com/mcp", "c")
	if header, err := s.oauthHeader(&c); err != nil || header != "" {
		t.Fatal("reinstall reused authorization after last uninstall")
	}
}

func TestMCPOAuthConfigIsolation(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	a := testRuntimeMCP(t, s, "https://example.com/mcp", "a")
	seedLegacyOAuth(t, s, a, "fixture-access")
	for _, manifest := range []string{
		`{"url":"https://other.example/mcp"}`,
		`{"url":"https://example.com/other"}`,
		`{"url":"https://example.com/mcp?tenant=other"}`,
		`{"url":"https://example.com/mcp","oauth":{"clientId":"other"}}`,
		`{"url":"https://example.com/mcp","oauth":{"scope":"admin"}}`,
		`{"url":"https://example.com/mcp","headers":{"X-Tenant":"other"}}`,
	} {
		b := a
		b.ProjectID = "b"
		b.Manifest = manifest
		if header, err := s.oauthHeader(&b); err != nil || header != "" {
			t.Fatalf("configuration crossed credential boundary: %s, %v", manifest, err)
		}
	}
	b := a
	b.ProjectID = "b"
	b.Name = "Different display name"
	if header, err := s.oauthHeader(&b); err != nil || header != "Bearer fixture-access" {
		t.Fatal("display name blocked reuse")
	}
}

func TestMCPOAuthLatestLegacyGrantIsSharedAutomatically(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	a := testRuntimeMCP(t, s, "https://example.com/mcp", "a")
	b := testRuntimeMCP(t, s, "https://example.com/mcp", "b")
	seedLegacyOAuth(t, s, a, "fixture-old")
	seedLegacyOAuth(t, s, b, "fixture-new")
	newer, err := s.Store.GetMCPOAuthToken(b.Name, b.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	newer.UpdatedAt = time.Now().Add(time.Minute)
	if err := s.Store.UpsertMCPOAuthToken(*newer); err != nil {
		t.Fatal(err)
	}
	c := testRuntimeMCP(t, s, "https://example.com/mcp", "c")
	for _, mcp := range []store.InstalledMCP{a, b, c} {
		if header, err := s.oauthHeader(&mcp); err != nil || header != "Bearer fixture-new" {
			t.Fatal("latest legacy grant was not shared automatically")
		}
		legacy, err := s.Store.GetMCPOAuthToken(mcp.Name, mcp.ProjectID)
		if err != nil || legacy != nil {
			t.Fatal("superseded legacy credential retained")
		}
	}
}

func TestMCPOAuthCrossProjectConcurrentRefresh(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("refresh_token") != "fixture-refresh" {
			t.Error("wrong refresh grant")
		}
		fmt.Fprint(w, `{"access_token":"fixture-renewed","refresh_token":"fixture-rotated","expires_in":3600}`)
	}))
	defer upstream.Close()
	a := testRuntimeMCP(t, s, upstream.URL+"/mcp", "a")
	seedLegacyOAuth(t, s, a, "fixture-access")
	c := testOAuthConnection(t, s, &a)
	c.Token.RefreshToken = "fixture-refresh"
	c.Token.TokenEndpoint = upstream.URL
	c.Token.ExpiresAt = time.Now().Add(-time.Hour)
	if err := s.Store.SaveMCPOAuthConnection(*c); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		b := testRuntimeMCP(t, s, upstream.URL+"/mcp", fmt.Sprint(i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			if header, err := s.oauthHeader(&b); err != nil || header != "Bearer fixture-renewed" {
				t.Errorf("refresh: %v", err)
			}
		}()
	}
	wg.Wait()
	s.refreshInstalledMCPOAuth(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("refreshes=%d", calls.Load())
	}
	if testOAuthConnection(t, s, &a).Token.RefreshToken != "fixture-rotated" {
		t.Fatal("rotation lost")
	}
}

func TestMCPOAuthPendingDoesNotEraseActiveGrant(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	var origin string
	var exchanges atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]string{"authorization_endpoint": origin + "/authorize", "token_endpoint": origin + "/token"})
		case "/token":
			exchanges.Add(1)
			fmt.Fprint(w, `{"access_token":"fixture-new","refresh_token":"fixture-refresh","expires_in":3600}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	origin = upstream.URL
	a := testRuntimeMCP(t, s, origin+"/mcp", "a")
	seedLegacyOAuth(t, s, a, "fixture-old")
	b := testRuntimeMCP(t, s, origin+"/mcp", "b")
	if _, err := s.oauthHeader(&b); err != nil {
		t.Fatal(err)
	}
	authURL, err := s.startOAuthFlow(&a)
	if err != nil {
		t.Fatal(err)
	}
	if header, err := s.oauthHeader(&b); err != nil || header != "Bearer fixture-old" {
		t.Fatal("pending flow erased active grant")
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	callback := "/api/connectors/oauth/callback?" + url.Values{"state": {u.Query().Get("state")}, "code": {"fixture-code"}}.Encode()
	w := httptest.NewRecorder()
	s.HandleOAuthCallback(w, httptest.NewRequest("GET", callback, nil))
	if w.Code != 200 {
		t.Fatalf("callback status=%d body=%s", w.Code, w.Body.String())
	}
	if header, err := s.oauthHeader(&b); err != nil || header != "Bearer fixture-new" {
		t.Fatal("callback did not update shared grant")
	}
	w = httptest.NewRecorder()
	s.HandleOAuthCallback(w, httptest.NewRequest("GET", callback, nil))
	if w.Code != 400 || exchanges.Load() != 1 {
		t.Fatal("state replay accepted")
	}
	// Starting another flow cannot invalidate the current credential, and a
	// callback must not attach after every compatible installation changes.
	authURL, err = s.startOAuthFlow(&a)
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(authURL)
	a.Manifest = strings.Replace(a.Manifest, "/mcp", "/changed", 1)
	if err := s.Store.UpsertInstalledMCP(a); err != nil {
		t.Fatal(err)
	}
	b.Manifest = a.Manifest
	if err := s.Store.UpsertInstalledMCP(b); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.HandleOAuthCallback(w, httptest.NewRequest("GET", "/api/connectors/oauth/callback?"+url.Values{"state": {u.Query().Get("state")}, "code": {"fixture-code"}}.Encode(), nil))
	if w.Code != 400 || exchanges.Load() != 1 {
		t.Fatal("callback accepted after config change")
	}
}
