package store

import (
	"testing"
	"time"
)

func TestMCPOAuthConnectionsSurviveRestartAndPendingExpires(t *testing.T) {
	dir := t.TempDir()
	db, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSQLiteStore(db)
	c := MCPOAuthConnection{ID: "fixture-connection", ConfigKey: "fixture-config", Token: MCPOAuthToken{MCPName: "example", ProjectID: "a", AccessToken: "fixture-access", RefreshToken: "fixture-refresh", UpdatedAt: time.Now()}}
	c.Generation, err = s.EnsureMCPOAuthLifecycle(c.ConfigKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertMCPOAuthToken(c.Token); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsolidateMCPOAuthConnection(c, []InstalledMCP{{Name: "example", ProjectID: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = NewSQLiteStore(db)
	connections, err := s.ListMCPOAuthConnections(c.ConfigKey)
	if err != nil || len(connections) != 1 || connections[0].Token.RefreshToken != "fixture-refresh" {
		t.Fatal("credential lost after restart")
	}
	legacy, err := s.GetMCPOAuthToken("example", "a")
	if err != nil || legacy != nil {
		t.Fatal("legacy credential resurrected")
	}
	c.Token.State = "fixture-expired-state"
	c.Token.UpdatedAt = time.Now().Add(-16 * time.Minute)
	if err := s.SaveMCPOAuthPending(c); err != nil {
		t.Fatal(err)
	}
	pending, err := s.TakeMCPOAuthPending(c.Token.State)
	if err != nil || pending != nil {
		t.Fatal("expired flow accepted")
	}
	c.Token.State = "fixture-current-state"
	c.Token.UpdatedAt = time.Now()
	c.PendingAuthURL = "https://example.com/authorize"
	if err := s.SaveMCPOAuthPending(c); err != nil {
		t.Fatal(err)
	}
	pending, err = s.TakeMCPOAuthPending(c.Token.State)
	if err != nil || pending == nil {
		t.Fatal("valid flow rejected")
	}
	pending, err = s.TakeMCPOAuthPending(c.Token.State)
	if err != nil || pending != nil {
		t.Fatal("callback replay accepted")
	}
}

func TestMCPOAuthOldGenerationCannotWriteAfterReinstall(t *testing.T) {
	s := newTestDB(t)
	mcp := InstalledMCP{Name: "example", ProjectID: "a", AuthType: "none", Manifest: `{"url":"https://example.com/mcp"}`, EnvValues: "{}"}
	if err := s.UpsertInstalledMCP(mcp); err != nil {
		t.Fatal(err)
	}
	old, err := s.EnsureMCPOAuthLifecycle("fixture-config")
	if err != nil {
		t.Fatal(err)
	}
	c := MCPOAuthConnection{ID: "fixture-id", ConfigKey: "fixture-config", Generation: old, Token: MCPOAuthToken{AccessToken: "fixture-old"}}
	if err := s.DeleteMCPOAuthConfig(c.ConfigKey); err != nil {
		t.Fatal(err)
	}
	current, err := s.EnsureMCPOAuthLifecycle(c.ConfigKey)
	if err != nil {
		t.Fatal(err)
	}
	if old == current {
		t.Fatal("authorization lifetime identity reused")
	}
	if err := s.CompleteMCPOAuthConnection(c, mcp); err == nil {
		t.Fatal("old callback committed in new lifetime")
	}
	if err := s.SaveMCPOAuthConnection(c); err == nil {
		t.Fatal("old refresh committed in new lifetime")
	}
	if err := s.SaveMCPOAuthPending(c); err == nil {
		t.Fatal("old pending flow committed in new lifetime")
	}
	rows, err := s.ListMCPOAuthConnections(c.ConfigKey)
	if err != nil || len(rows) != 0 {
		t.Fatal("old generation recreated authorization")
	}
}
