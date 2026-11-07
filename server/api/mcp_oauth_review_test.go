package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/esonic/nexbox/store"
	"github.com/go-chi/chi/v5"
)

func reviewOAuthUpstream(t *testing.T, token http.HandlerFunc) *httptest.Server {
	t.Helper()
	var origin string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-authorization-server" {
			json.NewEncoder(w).Encode(map[string]string{"authorization_endpoint": origin + "/authorize", "token_endpoint": origin + "/token"})
			return
		}
		if r.URL.Path == "/token" {
			token(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	origin = upstream.URL
	t.Cleanup(upstream.Close)
	return upstream
}

func reviewOAuthMCP(t *testing.T, s *Server, endpoint, project string) store.InstalledMCP {
	t.Helper()
	mcp := testRuntimeMCP(t, s, endpoint, project)
	mcp.AuthType = "none"
	mcp.Manifest = fmt.Sprintf(`{"url":%q,"oauth":{"clientId":"fixture-client"}}`, endpoint)
	if err := s.Store.UpsertInstalledMCP(mcp); err != nil {
		t.Fatal(err)
	}
	return mcp
}

func reviewOAuthCallback(t *testing.T, authURL string) string {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	return "/api/connectors/oauth/callback?" + url.Values{"state": {u.Query().Get("state")}, "code": {"fixture-code"}}.Encode()
}

func TestMCPOAuthFirstCallbackCannotSurviveFinalUninstallReinstall(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	started, release := make(chan struct{}), make(chan struct{})
	upstream := reviewOAuthUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		fmt.Fprint(w, `{"access_token":"fixture-old-flow","expires_in":3600}`)
	})
	a := reviewOAuthMCP(t, s, upstream.URL+"/mcp", "a")
	authURL, err := s.startOAuthFlow(&a)
	if err != nil {
		t.Fatal(err)
	}
	callbackDone := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		s.HandleOAuthCallback(w, httptest.NewRequest("GET", reviewOAuthCallback(t, authURL), nil))
		callbackDone <- w.Code
	}()
	<-started
	removed := make(chan error, 1)
	go func() { removed <- s.uninstallMCPScopes(a.Name, a.ProjectID, false) }()
	reinstall := func() {
		r := chi.NewRouter()
		s.RegisterMarketplaceRoutes(r)
		body, _ := json.Marshal(map[string]any{"name": a.Name, "project_id": a.ProjectID, "manifest": json.RawMessage(a.Manifest), "auth_type": "none"})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/marketplace/installed-mcps", strings.NewReader(string(body))))
		if w.Code != 200 {
			t.Fatalf("reinstall status=%d body=%s", w.Code, w.Body.String())
		}
	}
	// With the original bug, uninstall finishes while the first token exchange
	// is in flight and the identical reinstall receives that old exchange.
	select {
	case err := <-removed:
		if err != nil {
			close(release)
			t.Fatal(err)
		}
		reinstall()
		close(release)
		<-callbackDone
	case <-time.After(100 * time.Millisecond):
		close(release)
		<-callbackDone
		if err := <-removed; err != nil {
			t.Fatal(err)
		}
		reinstall()
	}
	if header, err := s.oauthHeader(&a); err != nil || header != "" {
		t.Fatal("old first authorization crossed final uninstall and reinstall")
	}
}

func TestMCPOAuthUnusedDiscoveryInstallStillRefreshes(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	var calls atomic.Int32
	upstream := reviewOAuthUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"access_token":"fixture-refreshed","refresh_token":"fixture-rotated","expires_in":3600}`)
	})
	a := reviewOAuthMCP(t, s, upstream.URL+"/mcp", "a")
	a.AuthType = "oauth"
	seedLegacyOAuth(t, s, a, "fixture-old")
	c := testOAuthConnection(t, s, &a)
	c.Token.RefreshToken = "fixture-refresh"
	c.Token.TokenEndpoint = upstream.URL + "/token"
	c.Token.ExpiresAt = time.Now().Add(-time.Minute)
	if err := s.Store.SaveMCPOAuthConnection(*c); err != nil {
		t.Fatal(err)
	}
	b := reviewOAuthMCP(t, s, upstream.URL+"/mcp", "b")
	if err := s.uninstallMCPScopes(a.Name, a.ProjectID, false); err != nil {
		t.Fatal(err)
	}
	s.refreshInstalledMCPOAuth(context.Background())
	if calls.Load() != 1 {
		t.Fatal("unused discovery installation was skipped by maintenance")
	}
	if header, err := s.oauthHeader(&b); err != nil || header != "Bearer fixture-refreshed" {
		t.Fatal("remaining installation lost automatic refresh")
	}
}

func TestMCPOAuthLegacyGrantBeforeAuthTypeUpgradeIsMigrated(t *testing.T) {
	for _, withOlder := range []bool{false, true} {
		t.Run(fmt.Sprintf("older_oauth_install=%v", withOlder), func(t *testing.T) {
			s := newMCPRuntimeTestServer(t)
			a := reviewOAuthMCP(t, s, "https://example.com/mcp", "a")
			seedLegacyOAuth(t, s, a, "fixture-newest")
			latest, err := s.Store.GetMCPOAuthToken(a.Name, a.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			latest.UpdatedAt = time.Now().Add(time.Minute)
			if err := s.Store.UpsertMCPOAuthToken(*latest); err != nil {
				t.Fatal(err)
			}
			request := a
			var b store.InstalledMCP
			if withOlder {
				b = reviewOAuthMCP(t, s, "https://example.com/mcp", "b")
				b.AuthType = "oauth"
				seedLegacyOAuth(t, s, b, "fixture-older")
				request = b
			}
			if header, err := s.oauthHeader(&request); err != nil || header != "Bearer fixture-newest" {
				t.Fatal("unmarked legacy grant was skipped during migration")
			}
			for _, mcp := range []store.InstalledMCP{a, request} {
				legacy, err := s.Store.GetMCPOAuthToken(mcp.Name, mcp.ProjectID)
				if err != nil || legacy != nil {
					t.Fatal("compatible legacy grant copy was retained")
				}
			}
			if withOlder {
				if err := s.uninstallMCPScopes(a.Name, a.ProjectID, false); err != nil {
					t.Fatal(err)
				}
				if header, err := s.oauthHeader(&b); err != nil || header != "Bearer fixture-newest" {
					t.Fatal("uninstall lost the migrated latest grant")
				}
			}
		})
	}
}

func TestMCPOAuthEffectiveTemplatesShareGrant(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	a := testRuntimeMCP(t, s, "https://example.com/mcp", "a")
	a.Manifest = `{"url":"https://example.com/mcp","headers":{"X-Tenant":"${TENANT}"},"oauth":{"clientId":"${OAUTH_CLIENT}"}}`
	a.EnvValues = `{"TENANT":"t1","OAUTH_CLIENT":"fixture-client"}`
	seedLegacyOAuth(t, s, a, "fixture-shared")
	b := testRuntimeMCP(t, s, "https://example.com/mcp", "b")
	b.Manifest = `{"url":"https://example.com/mcp","headers":{"x-tenant":"t1"},"oauth":{"clientId":"fixture-client"}}`
	if err := s.Store.UpsertInstalledMCP(b); err != nil {
		t.Fatal(err)
	}
	ka, err := mcpOAuthConfigKey(&a)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := mcpOAuthConfigKey(&b)
	if err != nil {
		t.Fatal(err)
	}
	if ka != kb {
		t.Fatal("effective header/client configuration differs by storage representation")
	}
	if header, err := s.oauthHeader(&b); err != nil || header != "Bearer fixture-shared" {
		t.Fatal("resolved installation cannot reuse templated legacy grant")
	}
	b.Manifest = strings.Replace(b.Manifest, `"t1"`, `"t2"`, 1)
	if key, err := mcpOAuthConfigKey(&b); err != nil || key == ka {
		t.Fatal("different effective tenant reused authorization")
	}
}

func TestMCPOAuthPendingSurvivesInitiatingInstallationChange(t *testing.T) {
	for _, change := range []string{"update", "uninstall"} {
		t.Run(change, func(t *testing.T) {
			s := newMCPRuntimeTestServer(t)
			upstream := reviewOAuthUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"access_token":"fixture-shared","expires_in":3600}`)
			})
			a := reviewOAuthMCP(t, s, upstream.URL+"/mcp", "a")
			b := reviewOAuthMCP(t, s, upstream.URL+"/mcp", "b")
			authURL, err := s.startOAuthFlow(&a)
			if err != nil {
				t.Fatal(err)
			}
			if otherURL, err := s.startOAuthFlow(&b); err != nil || otherURL != authURL {
				t.Fatal("pending flow was not shared")
			}
			if change == "uninstall" {
				if err := s.uninstallMCPScopes(a.Name, a.ProjectID, false); err != nil {
					t.Fatal(err)
				}
			} else {
				r := chi.NewRouter()
				s.RegisterMarketplaceRoutes(r)
				body, _ := json.Marshal(map[string]any{"name": a.Name, "project_id": a.ProjectID, "manifest": map[string]string{"url": upstream.URL + "/other"}, "auth_type": "none"})
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest("POST", "/marketplace/installed-mcps", strings.NewReader(string(body))))
				if w.Code != 200 {
					t.Fatalf("update status=%d", w.Code)
				}
			}
			if remainingURL, err := s.startOAuthFlow(&b); err != nil || remainingURL != authURL {
				t.Fatal("compatible remaining installation lost its pending flow")
			}
			w := httptest.NewRecorder()
			s.HandleOAuthCallback(w, httptest.NewRequest("GET", reviewOAuthCallback(t, authURL), nil))
			if w.Code != 200 {
				t.Fatalf("remaining installation callback status=%d body=%s", w.Code, w.Body.String())
			}
			if header, err := s.oauthHeader(&b); err != nil || header != "Bearer fixture-shared" {
				t.Fatal("pending flow did not complete for remaining installation")
			}
		})
	}
}
