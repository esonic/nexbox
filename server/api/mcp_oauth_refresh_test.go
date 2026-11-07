package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/esonic/nexbox/store"
)

func TestMCPOAuthBackgroundAndConcurrentRefresh(t *testing.T) {
	db, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Server{Store: store.NewSQLiteStore(db)}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Error("missing refresh grant")
		}
		fmt.Fprint(w, `{"access_token":"renewed","refresh_token":"rotated","expires_in":3600}`)
	}))
	defer upstream.Close()
	mcp := store.InstalledMCP{Name: "example", ProjectID: "project", AuthType: "oauth", Manifest: `{"url":"https://example.com/mcp"}`}
	if err := s.Store.UpsertInstalledMCP(mcp); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.UpsertMCPOAuthToken(store.MCPOAuthToken{MCPName: mcp.Name, ProjectID: mcp.ProjectID, AccessToken: "old", RefreshToken: "refresh", TokenEndpoint: upstream.URL, ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.refreshInstalledMCPOAuth(context.Background()) }()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d", calls.Load())
	}
	connection := testOAuthConnection(t, s, &mcp)
	tok := &connection.Token
	if tok.AccessToken != "renewed" || tok.RefreshToken != "rotated" {
		t.Fatal("rotation not persisted")
	}
}

func TestMCPOAuthTemporaryFailureAnd401Retry(t *testing.T) {
	db, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Server{Store: store.NewSQLiteStore(db)}
	var fail atomic.Bool
	fail.Store(true)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"access_token":"renewed","expires_in":3600}`)
	}))
	defer tokenServer.Close()
	mcp := store.InstalledMCP{Name: "example", AuthType: "oauth", Manifest: `{"url":"https://example.com/mcp"}`}
	tok := store.MCPOAuthToken{MCPName: mcp.Name, AccessToken: "old", RefreshToken: "refresh", TokenEndpoint: tokenServer.URL, ExpiresAt: time.Now().Add(time.Minute)}
	save := func() {
		t.Helper()
		key, err := mcpOAuthConfigKey(&mcp)
		if err != nil {
			t.Fatal(err)
		}
		generation, err := s.Store.EnsureMCPOAuthLifecycle(key)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Store.SaveMCPOAuthConnection(store.MCPOAuthConnection{ID: mcpOAuthConnectionID(key), ConfigKey: key, Generation: generation, Token: tok}); err != nil {
			t.Fatal(err)
		}
	}
	save()
	if h, err := s.oauthHeader(&mcp); err != nil || h != "Bearer old" {
		t.Fatalf("usable token discarded: %q %v", h, err)
	}
	tok.ExpiresAt = time.Now().Add(-time.Hour)
	save()
	if _, err := s.oauthHeader(&mcp); err == nil {
		t.Fatal("expired token transient failure must return an error")
	}
	fail.Store(false)
	var attempts atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if r.Header.Get("Authorization") != "Bearer renewed" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(200)
	}))
	defer target.Close()
	req, _ := http.NewRequest("POST", target.URL, strings.NewReader(`{"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer old")
	client := http.Client{Transport: &mcpOAuthTransport{server: s, mcp: &mcp}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || attempts.Load() != 2 {
		t.Fatalf("retry: status=%d attempts=%d", resp.StatusCode, attempts.Load())
	}
	stored := &testOAuthConnection(t, s, &mcp).Token
	if stored.RefreshToken != "refresh" {
		t.Fatal("omitted refresh token was lost")
	}
}

func TestMCPOAuthPermanentFailureRequiresAuthorization(t *testing.T) {
	db, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Server{Store: store.NewSQLiteStore(db)}
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	defer tokenServer.Close()
	mcp := store.InstalledMCP{Name: "expired", AuthType: "oauth", Manifest: `{"url":"https://example.com/mcp"}`}
	if err := s.Store.UpsertMCPOAuthToken(store.MCPOAuthToken{MCPName: mcp.Name, AccessToken: "old", RefreshToken: "refresh", TokenEndpoint: tokenServer.URL, ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if header, err := s.oauthHeader(&mcp); err != nil || header != "" {
		t.Fatalf("permanent failure: header=%q err=%v", header, err)
	}
}
