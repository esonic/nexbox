package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testTempDir creates a temporary directory that works in the sandbox environment.
// It uses the system temp dir if available, otherwise the allowed sandbox path.
func testTempDir(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TMPDIR")
	if base == "" {
		base = "/Users/ericdu/meili/eric.du_dacs_at_okg.com/114/.claude-tmp/claude-502"
	}
	dir, err := os.MkdirTemp(base, "nexbox-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// newTestDB creates an in-memory SQLite database for testing.
func newTestDB(t *testing.T) *SQLiteStore {
	t.Helper()
	db, err := New(testTempDir(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewSQLiteStore(db)
}

var _ = filepath.Join // keep import for testTempDir

// ---------------------------------------------------------------------------
// InstalledMCP tests
// ---------------------------------------------------------------------------

func TestUpsertAndGetInstalledMCP(t *testing.T) {
	s := newTestDB(t)

	mcp := InstalledMCP{
		Name:        "github-mcp",
		ProjectID:   "",
		MCPKey:      "github",
		Manifest:    `{"key":"github","type":"stdio","cmd":"npx"}`,
		EnvValues:   `{"GITHUB_TOKEN":"secret123"}`,
		InstalledAt: UnixTime(time.Now().Truncate(time.Second)),
	}
	if err := s.UpsertInstalledMCP(mcp); err != nil {
		t.Fatalf("UpsertInstalledMCP: %v", err)
	}

	got, err := s.GetInstalledMCP("github-mcp", "")
	if err != nil {
		t.Fatalf("GetInstalledMCP: %v", err)
	}
	if got == nil {
		t.Fatal("GetInstalledMCP returned nil")
	}
	if got.Name != mcp.Name {
		t.Errorf("Name = %q, want %q", got.Name, mcp.Name)
	}
	if got.MCPKey != mcp.MCPKey {
		t.Errorf("MCPKey = %q, want %q", got.MCPKey, mcp.MCPKey)
	}
	if got.Manifest != mcp.Manifest {
		t.Errorf("Manifest = %q, want %q", got.Manifest, mcp.Manifest)
	}
	if got.EnvValues != mcp.EnvValues {
		t.Errorf("EnvValues = %q, want %q", got.EnvValues, mcp.EnvValues)
	}
}

func TestUpsertInstalledMCP_Upsert(t *testing.T) {
	s := newTestDB(t)

	mcp := InstalledMCP{Name: "my-mcp", ProjectID: "", MCPKey: "my-key", Manifest: `{}`, EnvValues: `{}`}
	if err := s.UpsertInstalledMCP(mcp); err != nil {
		t.Fatalf("initial insert: %v", err)
	}

	// Update manifest and env values
	mcp.Manifest = `{"updated":true}`
	mcp.EnvValues = `{"TOKEN":"new"}`
	if err := s.UpsertInstalledMCP(mcp); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, _ := s.GetInstalledMCP("my-mcp", "")
	if got.Manifest != `{"updated":true}` {
		t.Errorf("Manifest after upsert = %q", got.Manifest)
	}
	if got.EnvValues != `{"TOKEN":"new"}` {
		t.Errorf("EnvValues after upsert = %q", got.EnvValues)
	}
}

func TestGetInstalledMCP_NotFound(t *testing.T) {
	s := newTestDB(t)
	got, err := s.GetInstalledMCP("does-not-exist", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing MCP, got %+v", got)
	}
}

func TestListInstalledMCPs(t *testing.T) {
	s := newTestDB(t)

	for _, name := range []string{"alpha", "beta", "gamma"} {
		s.UpsertInstalledMCP(InstalledMCP{Name: name, ProjectID: "", MCPKey: name + "-key", Manifest: `{}`, EnvValues: `{}`})
	}

	mcps, err := s.ListInstalledMCPs("*")
	if err != nil {
		t.Fatalf("ListInstalledMCPs: %v", err)
	}
	if len(mcps) != 3 {
		t.Errorf("len = %d, want 3", len(mcps))
	}
	// Results should be ordered by name
	if mcps[0].Name != "alpha" || mcps[1].Name != "beta" || mcps[2].Name != "gamma" {
		t.Errorf("order wrong: %v", []string{mcps[0].Name, mcps[1].Name, mcps[2].Name})
	}
}

func TestListInstalledMCPs_ProjectScoping(t *testing.T) {
	s := newTestDB(t)

	// Global MCP (project_id="")
	s.UpsertInstalledMCP(InstalledMCP{Name: "global-mcp", ProjectID: "", MCPKey: "g", Manifest: `{}`, EnvValues: `{}`})
	// Project-specific MCPs
	s.UpsertInstalledMCP(InstalledMCP{Name: "proj-mcp", ProjectID: "proj-1", MCPKey: "p", Manifest: `{}`, EnvValues: `{}`})
	s.UpsertInstalledMCP(InstalledMCP{Name: "other-mcp", ProjectID: "proj-2", MCPKey: "o", Manifest: `{}`, EnvValues: `{}`})

	// projectID="*" → all 3
	all, err := s.ListInstalledMCPs("*")
	if err != nil {
		t.Fatalf("ListInstalledMCPs(*): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("all = %d, want 3", len(all))
	}

	// projectID="" → global only (1)
	globals, err := s.ListInstalledMCPs("")
	if err != nil {
		t.Fatalf("ListInstalledMCPs(\"\"): %v", err)
	}
	if len(globals) != 1 || globals[0].Name != "global-mcp" {
		t.Errorf("globals = %v", globals)
	}

	// projectID="proj-1" → global + proj-1 (2)
	proj1, err := s.ListInstalledMCPs("proj-1")
	if err != nil {
		t.Fatalf("ListInstalledMCPs(proj-1): %v", err)
	}
	if len(proj1) != 2 {
		t.Errorf("proj1 = %d, want 2", len(proj1))
	}
}

func TestDeleteInstalledMCP(t *testing.T) {
	s := newTestDB(t)
	s.UpsertInstalledMCP(InstalledMCP{Name: "to-delete", ProjectID: "", MCPKey: "k", Manifest: `{}`, EnvValues: `{}`})

	if err := s.DeleteInstalledMCP("to-delete", ""); err != nil {
		t.Fatalf("DeleteInstalledMCP: %v", err)
	}
	got, _ := s.GetInstalledMCP("to-delete", "")
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}

func TestInstalledMCP_EmptyEnvValuesDefault(t *testing.T) {
	s := newTestDB(t)
	// Insert without setting EnvValues (should default to "{}")
	mcp := InstalledMCP{Name: "bare-mcp", ProjectID: "", MCPKey: "bare", Manifest: `{}`}
	if err := s.UpsertInstalledMCP(mcp); err != nil {
		t.Fatalf("UpsertInstalledMCP: %v", err)
	}

	got, _ := s.GetInstalledMCP("bare-mcp", "")
	if got.EnvValues != "{}" {
		t.Errorf("EnvValues = %q, want %q", got.EnvValues, "{}")
	}
}

// ---------------------------------------------------------------------------
// InstalledSkill tests
// ---------------------------------------------------------------------------

func TestUpsertAndGetInstalledSkill(t *testing.T) {
	s := newTestDB(t)

	skill := InstalledSkill{
		Name:        "my-skill",
		ProjectID:   "proj-1",
		Version:     "1.0.0",
		InstallPath: "/tmp/skills/my-skill",
		InstalledAt: UnixTime(time.Now().Truncate(time.Second)),
	}
	if err := s.UpsertInstalledSkill(skill); err != nil {
		t.Fatalf("UpsertInstalledSkill: %v", err)
	}

	got, err := s.GetInstalledSkill("my-skill", "proj-1")
	if err != nil {
		t.Fatalf("GetInstalledSkill: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil skill")
	}
	if got.Version != "1.0.0" || got.InstallPath != "/tmp/skills/my-skill" {
		t.Errorf("got %+v", got)
	}
}

func TestListInstalledSkills_ByProject(t *testing.T) {
	s := newTestDB(t)

	s.UpsertInstalledSkill(InstalledSkill{Name: "s1", ProjectID: "p1", Version: "1.0", InstallPath: ""})
	s.UpsertInstalledSkill(InstalledSkill{Name: "s2", ProjectID: "p1", Version: "1.0", InstallPath: ""})
	s.UpsertInstalledSkill(InstalledSkill{Name: "s3", ProjectID: "p2", Version: "1.0", InstallPath: ""})
	s.UpsertInstalledSkill(InstalledSkill{Name: "s4", ProjectID: "", Version: "1.0", InstallPath: ""}) // global

	// Project-specific: global + p1 skills
	p1Skills, err := s.ListInstalledSkills("p1")
	if err != nil {
		t.Fatalf("ListInstalledSkills: %v", err)
	}
	if len(p1Skills) != 3 {
		t.Errorf("p1 skills = %d, want 3 (global + project-specific)", len(p1Skills))
	}

	// All
	allSkills, _ := s.ListInstalledSkills("*")
	if len(allSkills) != 4 {
		t.Errorf("all skills = %d, want 4", len(allSkills))
	}

	// Global
	globalSkills, _ := s.ListInstalledSkills("")
	if len(globalSkills) != 1 {
		t.Errorf("global skills = %d, want 1", len(globalSkills))
	}
}

func TestDeleteInstalledSkill(t *testing.T) {
	s := newTestDB(t)
	s.UpsertInstalledSkill(InstalledSkill{Name: "del-skill", ProjectID: "proj", Version: "1.0", InstallPath: ""})

	if err := s.DeleteInstalledSkill("del-skill", "proj"); err != nil {
		t.Fatalf("DeleteInstalledSkill: %v", err)
	}
	got, _ := s.GetInstalledSkill("del-skill", "proj")
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}

func TestUpsertInstalledSkill_Upsert(t *testing.T) {
	s := newTestDB(t)

	skill := InstalledSkill{Name: "updatable", ProjectID: "p", Version: "1.0", InstallPath: "/old"}
	s.UpsertInstalledSkill(skill)

	skill.Version = "2.0"
	skill.InstallPath = "/new"
	if err := s.UpsertInstalledSkill(skill); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, _ := s.GetInstalledSkill("updatable", "p")
	if got.Version != "2.0" || got.InstallPath != "/new" {
		t.Errorf("after upsert: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// InstalledMCP auth_type tests
// ---------------------------------------------------------------------------

func TestInstalledMCP_AuthType(t *testing.T) {
	s := newTestDB(t)

	mcp := InstalledMCP{
		Name:     "oauth-mcp",
		MCPKey:   "oauth-key",
		Manifest: `{"url":"https://example.com"}`,
		AuthType: "oauth_discovery",
	}
	if err := s.UpsertInstalledMCP(mcp); err != nil {
		t.Fatalf("UpsertInstalledMCP: %v", err)
	}

	got, err := s.GetInstalledMCP("oauth-mcp", "")
	if err != nil {
		t.Fatalf("GetInstalledMCP: %v", err)
	}
	if got.AuthType != "oauth_discovery" {
		t.Errorf("AuthType = %q, want %q", got.AuthType, "oauth_discovery")
	}
}

func TestInstalledMCP_AuthTypeDefault(t *testing.T) {
	s := newTestDB(t)

	mcp := InstalledMCP{Name: "no-auth", MCPKey: "k", Manifest: `{}`}
	s.UpsertInstalledMCP(mcp)

	got, _ := s.GetInstalledMCP("no-auth", "")
	if got.AuthType != "none" {
		t.Errorf("AuthType = %q, want %q", got.AuthType, "none")
	}
}

// ---------------------------------------------------------------------------
// InstalledPluginEntry tests
// ---------------------------------------------------------------------------

func TestUpsertAndListInstalledPluginEntries(t *testing.T) {
	s := newTestDB(t)

	p := InstalledPluginEntry{
		Name:        "test-plugin",
		ProjectID:   "",
		InstallPath: "/path/to/plugin",
		InstalledAt: UnixTime(time.Now().Truncate(time.Second)),
	}
	if err := s.UpsertInstalledPluginEntry(p); err != nil {
		t.Fatalf("UpsertInstalledPluginEntry: %v", err)
	}

	plugins, err := s.ListInstalledPluginEntries("*")
	if err != nil {
		t.Fatalf("ListInstalledPluginEntries: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("len = %d, want 1", len(plugins))
	}
	if plugins[0].Name != "test-plugin" {
		t.Errorf("Name = %q, want %q", plugins[0].Name, "test-plugin")
	}
	if plugins[0].InstallPath != "/path/to/plugin" {
		t.Errorf("InstallPath = %q, want %q", plugins[0].InstallPath, "/path/to/plugin")
	}
}

func TestDeleteInstalledPluginEntry(t *testing.T) {
	s := newTestDB(t)

	s.UpsertInstalledPluginEntry(InstalledPluginEntry{Name: "del-plugin", ProjectID: ""})
	if err := s.DeleteInstalledPluginEntry("del-plugin", ""); err != nil {
		t.Fatalf("DeleteInstalledPluginEntry: %v", err)
	}

	plugins, _ := s.ListInstalledPluginEntries("*")
	if len(plugins) != 0 {
		t.Errorf("expected 0 after delete, got %d", len(plugins))
	}
}

func TestListInstalledPluginEntries_ProjectScoping(t *testing.T) {
	s := newTestDB(t)

	s.UpsertInstalledPluginEntry(InstalledPluginEntry{Name: "global-p", ProjectID: ""})
	s.UpsertInstalledPluginEntry(InstalledPluginEntry{Name: "proj-p", ProjectID: "p1"})
	s.UpsertInstalledPluginEntry(InstalledPluginEntry{Name: "other-p", ProjectID: "p2"})

	all, _ := s.ListInstalledPluginEntries("*")
	if len(all) != 3 {
		t.Errorf("all = %d, want 3", len(all))
	}

	globals, _ := s.ListInstalledPluginEntries("")
	if len(globals) != 1 {
		t.Errorf("globals = %d, want 1", len(globals))
	}

	p1, _ := s.ListInstalledPluginEntries("p1")
	if len(p1) != 2 {
		t.Errorf("p1 = %d, want 2 (global + project-specific)", len(p1))
	}
}

// ---------------------------------------------------------------------------
// MCPOAuthToken tests
// ---------------------------------------------------------------------------

func TestUpsertAndGetMCPOAuthToken(t *testing.T) {
	s := newTestDB(t)

	tok := MCPOAuthToken{
		MCPName:       "oauth-mcp",
		ProjectID:     "",
		AccessToken:   "access-123",
		RefreshToken:  "refresh-456",
		ExpiresAt:     time.Now().Add(time.Hour).Truncate(time.Second),
		TokenEndpoint: "https://auth.example.com/token",
		ClientID:      "client-789",
		UpdatedAt:     time.Now().Truncate(time.Second),
	}
	if err := s.UpsertMCPOAuthToken(tok); err != nil {
		t.Fatalf("UpsertMCPOAuthToken: %v", err)
	}

	got, err := s.GetMCPOAuthToken("oauth-mcp", "")
	if err != nil {
		t.Fatalf("GetMCPOAuthToken: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil token")
	}
	if got.AccessToken != "access-123" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "access-123")
	}
	if got.RefreshToken != "refresh-456" {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, "refresh-456")
	}
	if got.ClientID != "client-789" {
		t.Errorf("ClientID = %q, want %q", got.ClientID, "client-789")
	}
}

func TestGetMCPOAuthTokenByState(t *testing.T) {
	s := newTestDB(t)

	tok := MCPOAuthToken{
		MCPName:      "state-mcp",
		State:        "random-state-123",
		CodeVerifier: "verifier-abc",
		UpdatedAt:    time.Now(),
	}
	s.UpsertMCPOAuthToken(tok)

	got, err := s.GetMCPOAuthTokenByState("random-state-123")
	if err != nil {
		t.Fatalf("GetMCPOAuthTokenByState: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if got.MCPName != "state-mcp" {
		t.Errorf("MCPName = %q, want %q", got.MCPName, "state-mcp")
	}
	if got.CodeVerifier != "verifier-abc" {
		t.Errorf("CodeVerifier = %q, want %q", got.CodeVerifier, "verifier-abc")
	}
}

func TestGetMCPOAuthTokenByState_NotFound(t *testing.T) {
	s := newTestDB(t)

	got, err := s.GetMCPOAuthTokenByState("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing state, got %+v", got)
	}
}

func TestDeleteMCPOAuthToken(t *testing.T) {
	s := newTestDB(t)

	s.UpsertMCPOAuthToken(MCPOAuthToken{MCPName: "del-tok", UpdatedAt: time.Now()})
	if err := s.DeleteMCPOAuthToken("del-tok", ""); err != nil {
		t.Fatalf("DeleteMCPOAuthToken: %v", err)
	}

	got, _ := s.GetMCPOAuthToken("del-tok", "")
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// MarketplaceSource tests
// ---------------------------------------------------------------------------

func TestUpsertAndListMarketplaceSources(t *testing.T) {
	s := newTestDB(t)

	src := MarketplaceSource{
		ID:      "src-1",
		Type:    "git",
		URL:     "git@github.com:anthropics/skills.git",
		Path:    "/data/marketplace/skills",
		Name:    "skills",
		AddedAt: UnixTime(time.Now().Truncate(time.Second)),
	}
	if err := s.UpsertMarketplaceSource(src); err != nil {
		t.Fatalf("UpsertMarketplaceSource: %v", err)
	}

	sources, err := s.ListMarketplaceSources()
	if err != nil {
		t.Fatalf("ListMarketplaceSources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("len = %d, want 1", len(sources))
	}
	if sources[0].URL != "git@github.com:anthropics/skills.git" {
		t.Errorf("URL = %q", sources[0].URL)
	}
	if sources[0].Type != "git" {
		t.Errorf("Type = %q, want %q", sources[0].Type, "git")
	}
}

func TestDeleteMarketplaceSource(t *testing.T) {
	s := newTestDB(t)

	s.UpsertMarketplaceSource(MarketplaceSource{ID: "del-src", Type: "local", Path: "/tmp", Name: "temp"})
	if err := s.DeleteMarketplaceSource("del-src"); err != nil {
		t.Fatalf("DeleteMarketplaceSource: %v", err)
	}

	sources, _ := s.ListMarketplaceSources()
	if len(sources) != 0 {
		t.Errorf("expected 0 after delete, got %d", len(sources))
	}
}

func TestUpsertMarketplaceSource_Upsert(t *testing.T) {
	s := newTestDB(t)

	src := MarketplaceSource{ID: "upsert-src", Type: "local", Path: "/old", Name: "old-name"}
	s.UpsertMarketplaceSource(src)

	src.Path = "/new"
	src.Name = "new-name"
	if err := s.UpsertMarketplaceSource(src); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	sources, _ := s.ListMarketplaceSources()
	if len(sources) != 1 {
		t.Fatalf("len = %d, want 1", len(sources))
	}
	if sources[0].Path != "/new" || sources[0].Name != "new-name" {
		t.Errorf("after upsert: path=%q name=%q", sources[0].Path, sources[0].Name)
	}
}
