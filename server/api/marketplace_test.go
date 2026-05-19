package api

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"gitlab.okg.com/sre_ai/nexbox/store"
)

// testTempDir creates a temporary directory that works in the sandbox environment.
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

// ---------------------------------------------------------------------------
// parseSkillFrontmatter tests
// ---------------------------------------------------------------------------

func TestParseSkillFrontmatter(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantDesc string
	}{
		{
			input:    "---\nname: my-skill\ndescription: does stuff\n---\nbody",
			wantName: "my-skill",
			wantDesc: "does stuff",
		},
		{
			input:    "---\nname: \"quoted-name\"\ndescription: 'quoted desc'\n---\n",
			wantName: "quoted-name",
			wantDesc: "quoted desc",
		},
		{
			input:    "no frontmatter here",
			wantName: "",
			wantDesc: "",
		},
		{
			input:    "---\nonly one delimiter",
			wantName: "",
			wantDesc: "",
		},
	}
	for _, tc := range tests {
		name, desc := parseSkillFrontmatter(tc.input)
		if name != tc.wantName {
			t.Errorf("parseSkillFrontmatter(%q) name = %q, want %q", tc.input[:min(len(tc.input), 30)], name, tc.wantName)
		}
		if desc != tc.wantDesc {
			t.Errorf("parseSkillFrontmatter(%q) desc = %q, want %q", tc.input[:min(len(tc.input), 30)], desc, tc.wantDesc)
		}
	}
}

// ---------------------------------------------------------------------------
// parseMCPConfig tests
// ---------------------------------------------------------------------------

func TestParseMCPConfig_EnvVars(t *testing.T) {
	config := `{"mcpServers":{"my-server":{"command":"npx","args":["-y","server"],"env":{"TOKEN":"${MY_TOKEN}","API_KEY":"${MY_API_KEY}"}}}}`
	envVars, authType := parseMCPConfig([]byte(config))

	if len(envVars) != 2 {
		t.Fatalf("envVars count = %d, want 2; got %v", len(envVars), envVars)
	}
	// envVars should be sorted
	if envVars[0] != "MY_API_KEY" || envVars[1] != "MY_TOKEN" {
		t.Errorf("envVars = %v, want [MY_API_KEY MY_TOKEN]", envVars)
	}
	if authType != "apikey" {
		t.Errorf("authType = %q, want %q", authType, "apikey")
	}
}

func TestParseMCPConfig_OAuth(t *testing.T) {
	config := `{"mcpServers":{"slack":{"url":"https://slack.mcp.example.com","oauth":{"clientId":"abc123"}}}}`
	envVars, authType := parseMCPConfig([]byte(config))

	if authType != "oauth" {
		t.Errorf("authType = %q, want %q", authType, "oauth")
	}
	if len(envVars) != 0 {
		t.Errorf("envVars = %v, want empty", envVars)
	}
}

func TestParseMCPConfig_PlainHTTP(t *testing.T) {
	// Plain HTTP MCP without auth should default to "none", not "oauth_discovery".
	config := `{"mcpServers":{"simple":{"url":"https://example.com/mcp"}}}`
	_, authType := parseMCPConfig([]byte(config))

	if authType != "none" {
		t.Errorf("authType = %q, want %q", authType, "none")
	}
}

func TestParseMCPConfig_NoAuth(t *testing.T) {
	config := `{"mcpServers":{"simple":{"command":"node","args":["server.js"]}}}`
	envVars, authType := parseMCPConfig([]byte(config))

	if authType != "none" {
		t.Errorf("authType = %q, want %q", authType, "none")
	}
	if len(envVars) != 0 {
		t.Errorf("envVars = %v, want empty", envVars)
	}
}

// ---------------------------------------------------------------------------
// detectPluginComponents tests
// ---------------------------------------------------------------------------

func TestDetectPluginComponents(t *testing.T) {
	dir := testTempDir(t)

	// Create .mcp.json
	os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{}`), 0644)

	// Create commands/ with a .md file
	os.MkdirAll(filepath.Join(dir, "commands"), 0755)
	os.WriteFile(filepath.Join(dir, "commands", "test.md"), []byte("# cmd"), 0644)

	// Create hooks/hooks.json
	os.MkdirAll(filepath.Join(dir, "hooks"), 0755)
	os.WriteFile(filepath.Join(dir, "hooks", "hooks.json"), []byte(`{}`), 0644)

	components := detectPluginComponents(dir)

	found := map[string]bool{}
	for _, c := range components {
		found[c] = true
	}

	if !found["mcp"] {
		t.Error("expected 'mcp' component")
	}
	if !found["commands"] {
		t.Error("expected 'commands' component")
	}
	if !found["hooks"] {
		t.Error("expected 'hooks' component")
	}
	if found["lsp"] {
		t.Error("did not expect 'lsp' component")
	}
}

// ---------------------------------------------------------------------------
// loadSkillMD tests
// ---------------------------------------------------------------------------

func TestLoadSkillMD(t *testing.T) {
	dir := testTempDir(t)
	skillDir := filepath.Join(dir, "my-cool-skill")
	os.MkdirAll(skillDir, 0755)

	content := "---\nname: cool-skill\ndescription: A cool agent skill\n---\nDo something cool."
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644)

	p, err := loadSkillMD(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("loadSkillMD: %v", err)
	}
	if p.Name != "cool-skill" {
		t.Errorf("Name = %q, want %q", p.Name, "cool-skill")
	}
	if p.Description != "A cool agent skill" {
		t.Errorf("Description = %q, want %q", p.Description, "A cool agent skill")
	}
	if p.Type != "skill" {
		t.Errorf("Type = %q, want %q", p.Type, "skill")
	}
	if p.InstallPath != skillDir {
		t.Errorf("InstallPath = %q, want %q", p.InstallPath, skillDir)
	}
}

func TestLoadSkillMD_NoFrontmatter(t *testing.T) {
	dir := testTempDir(t)
	skillDir := filepath.Join(dir, "fallback-name")
	os.MkdirAll(skillDir, 0755)

	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("No frontmatter here"), 0644)

	p, err := loadSkillMD(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("loadSkillMD: %v", err)
	}
	// Name should fall back to directory name
	if p.Name != "fallback-name" {
		t.Errorf("Name = %q, want %q", p.Name, "fallback-name")
	}
}

// ---------------------------------------------------------------------------
// loadClaudePlugin tests
// ---------------------------------------------------------------------------

func TestLoadClaudePlugin(t *testing.T) {
	dir := testTempDir(t)
	pluginDir := filepath.Join(dir, "test-plugin")
	os.MkdirAll(filepath.Join(pluginDir, ".claude-plugin"), 0755)

	pluginJSON := `{"name":"my-plugin","description":"A test plugin","version":"1.2.3","author":{"name":"Test Author"}}`
	pluginJSONPath := filepath.Join(pluginDir, ".claude-plugin", "plugin.json")
	os.WriteFile(pluginJSONPath, []byte(pluginJSON), 0644)

	p, err := loadClaudePlugin(pluginDir, pluginJSONPath)
	if err != nil {
		t.Fatalf("loadClaudePlugin: %v", err)
	}
	if p.Name != "my-plugin" {
		t.Errorf("Name = %q, want %q", p.Name, "my-plugin")
	}
	if p.Description != "A test plugin" {
		t.Errorf("Description = %q, want %q", p.Description, "A test plugin")
	}
	if p.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", p.Version, "1.2.3")
	}
	if p.Author != "Test Author" {
		t.Errorf("Author = %q, want %q", p.Author, "Test Author")
	}
}

func TestLoadClaudePlugin_FallbackName(t *testing.T) {
	dir := testTempDir(t)
	pluginDir := filepath.Join(dir, "dir-name-plugin")
	os.MkdirAll(filepath.Join(pluginDir, ".claude-plugin"), 0755)

	pluginJSON := `{"description":"No name field"}`
	pluginJSONPath := filepath.Join(pluginDir, ".claude-plugin", "plugin.json")
	os.WriteFile(pluginJSONPath, []byte(pluginJSON), 0644)

	p, err := loadClaudePlugin(pluginDir, pluginJSONPath)
	if err != nil {
		t.Fatalf("loadClaudePlugin: %v", err)
	}
	if p.Name != "dir-name-plugin" {
		t.Errorf("Name = %q, want %q (directory fallback)", p.Name, "dir-name-plugin")
	}
}

// ---------------------------------------------------------------------------
// loadMarketplaceJSON tests
// ---------------------------------------------------------------------------

func TestLoadMarketplaceJSON(t *testing.T) {
	dir := testTempDir(t)
	repoDir := filepath.Join(dir, "my-repo")
	os.MkdirAll(filepath.Join(repoDir, ".claude-plugin"), 0755)

	mJSON := `{
		"name":"my-marketplace",
		"owner":{"name":"OwnerCo"},
		"plugins":[
			{"name":"plugin-a","description":"First plugin","version":"0.1.0"},
			{"name":"plugin-b","description":"Second plugin","version":"0.2.0","author":{"name":"Bob"}}
		]
	}`
	path := filepath.Join(repoDir, ".claude-plugin", "marketplace.json")
	os.WriteFile(path, []byte(mJSON), 0644)

	plugins, err := loadMarketplaceJSON(repoDir, path)
	if err != nil {
		t.Fatalf("loadMarketplaceJSON: %v", err)
	}
	if len(plugins) != 2 {
		t.Fatalf("len = %d, want 2", len(plugins))
	}
	if plugins[0].Name != "plugin-a" {
		t.Errorf("plugins[0].Name = %q, want %q", plugins[0].Name, "plugin-a")
	}
	if plugins[0].Author != "OwnerCo" {
		t.Errorf("plugins[0].Author = %q, want %q (owner fallback)", plugins[0].Author, "OwnerCo")
	}
	if plugins[1].Author != "Bob" {
		t.Errorf("plugins[1].Author = %q, want %q", plugins[1].Author, "Bob")
	}
}

func TestLoadMarketplaceJSON_Empty(t *testing.T) {
	dir := testTempDir(t)
	path := filepath.Join(dir, "marketplace.json")
	os.WriteFile(path, []byte(`{"name":"empty","plugins":[]}`), 0644)

	plugins, err := loadMarketplaceJSON(dir, path)
	if err != nil {
		t.Fatalf("loadMarketplaceJSON: %v", err)
	}
	if plugins != nil {
		t.Errorf("expected nil for empty plugins, got %d", len(plugins))
	}
}

// ---------------------------------------------------------------------------
// loadMarketplacePlugins integration test
// ---------------------------------------------------------------------------

func TestLoadMarketplacePlugins(t *testing.T) {
	dir := testTempDir(t)

	// Create a skill directory with SKILL.md
	skillDir := filepath.Join(dir, "skills-repo", "git-helper")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: git-helper\ndescription: Helps with git\n---\n"), 0644)

	// Create a Claude plugin directory
	pluginDir := filepath.Join(dir, "my-plugin")
	os.MkdirAll(filepath.Join(pluginDir, ".claude-plugin"), 0755)
	os.WriteFile(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"), []byte(`{"name":"test-plugin","description":"A test"}`), 0644)

	plugins, err := loadMarketplacePlugins(dir, nil)
	if err != nil {
		t.Fatalf("loadMarketplacePlugins: %v", err)
	}

	// Should find at least the skill and the plugin
	byName := map[string]*MarketplacePlugin{}
	for i := range plugins {
		byName[plugins[i].Name] = &plugins[i]
	}

	if _, ok := byName["git-helper"]; !ok {
		t.Error("expected to find skill 'git-helper'")
	}
	if _, ok := byName["test-plugin"]; !ok {
		t.Error("expected to find plugin 'test-plugin'")
	}
}

func TestLoadMarketplacePlugins_EmptyDir(t *testing.T) {
	dir := testTempDir(t)
	plugins, err := loadMarketplacePlugins(dir, nil)
	if err != nil {
		t.Fatalf("loadMarketplacePlugins: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins for empty dir, got %d", len(plugins))
	}
}

func TestLoadMarketplacePlugins_NonExistentDir(t *testing.T) {
	plugins, err := loadMarketplacePlugins("/nonexistent/path/should/not/exist", nil)
	if err != nil {
		t.Fatalf("expected no error for missing dir, got: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins, got %d", len(plugins))
	}
}

func TestLoadMarketplacePlugins_Symlink(t *testing.T) {
	// Create a real plugin directory
	realDir := t.TempDir()
	cpDir := filepath.Join(realDir, ".claude-plugin")
	os.Mkdir(cpDir, 0755)
	os.WriteFile(filepath.Join(cpDir, "plugin.json"), []byte(`{"name":"sym-test","description":"symlink test"}`), 0644)

	// Create marketplace dir with a symlink pointing to the real dir
	mpDir := t.TempDir()
	os.Symlink(realDir, filepath.Join(mpDir, "my-plugin"))

	plugins, err := loadMarketplacePlugins(mpDir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, p := range plugins {
		if p.Name == "sym-test" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find plugin 'sym-test' via symlink, got %d plugins: %v", len(plugins), plugins)
	}
}

// ---------------------------------------------------------------------------
// enrichFromPluginDir tests
// ---------------------------------------------------------------------------

func TestEnrichFromPluginDir_MCP(t *testing.T) {
	dir := testTempDir(t)
	os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers":{"test":{"command":"node","env":{"KEY":"${API_KEY}"}}}}`), 0644)

	p := &MarketplacePlugin{Name: "test-mcp"}
	enrichFromPluginDir(p, dir)

	if p.Type != "mcp" {
		t.Errorf("Type = %q, want %q", p.Type, "mcp")
	}
	if p.Source != "connector" {
		t.Errorf("Source = %q, want %q", p.Source, "connector")
	}
	if len(p.EnvVars) != 1 || p.EnvVars[0] != "API_KEY" {
		t.Errorf("EnvVars = %v, want [API_KEY]", p.EnvVars)
	}
}

func TestEnrichFromPluginDir_LSP(t *testing.T) {
	dir := testTempDir(t)
	os.WriteFile(filepath.Join(dir, ".lsp.json"), []byte(`{"servers":{}}`), 0644)

	p := &MarketplacePlugin{Name: "test-lsp"}
	enrichFromPluginDir(p, dir)

	if p.Type != "lsp" {
		t.Errorf("Type = %q, want %q", p.Type, "lsp")
	}
}

func TestEnrichFromPluginDir_Plain(t *testing.T) {
	dir := testTempDir(t)

	p := &MarketplacePlugin{Name: "test-plain"}
	enrichFromPluginDir(p, dir)

	if p.Type != "plugin" {
		t.Errorf("Type = %q, want %q", p.Type, "plugin")
	}
}

// ---------------------------------------------------------------------------
// MCP proxy helper tests
// ---------------------------------------------------------------------------

func TestBuildForwardURL(t *testing.T) {
	base, _ := buildForwardURL(mustParseURL("https://api.example.com/v1"), "messages", "limit=10")
	if base.String() != "https://api.example.com/v1/messages?limit=10" {
		t.Errorf("got %q", base.String())
	}

	// No tail
	base2, _ := buildForwardURL(mustParseURL("https://api.example.com/v1/"), "", "")
	if base2.String() != "https://api.example.com/v1/" {
		t.Errorf("got %q", base2.String())
	}
}

func TestMCPTargetURL(t *testing.T) {
	u, err := mcpTargetURL(`{"url":"https://mcp.example.com","type":"http"}`)
	if err != nil {
		t.Fatalf("mcpTargetURL: %v", err)
	}
	if u.String() != "https://mcp.example.com" {
		t.Errorf("got %q", u.String())
	}

	_, err = mcpTargetURL(`{"type":"stdio","command":"node"}`)
	if err == nil {
		t.Error("expected error for manifest without url")
	}
}

func TestResolveOAuthConfig_ExplicitOAuth(t *testing.T) {
	manifest := `{"url":"https://slack.mcp.com","oauth":{"clientId":"my-client-123"}}`
	mcp := &storeInstalledMCPShim{manifest: manifest, authType: "oauth"}

	clientID, serverURL, err := resolveOAuthConfig(mcp.toStoreMCP())
	if err != nil {
		t.Fatalf("resolveOAuthConfig: %v", err)
	}
	if clientID != "my-client-123" {
		t.Errorf("clientID = %q, want %q", clientID, "my-client-123")
	}
	if serverURL != "https://slack.mcp.com" {
		t.Errorf("serverURL = %q, want %q", serverURL, "https://slack.mcp.com")
	}
}

func TestResolveOAuthConfig_Discovery(t *testing.T) {
	manifest := `{"url":"https://api.example.com/mcp/v1"}`
	mcp := &storeInstalledMCPShim{manifest: manifest, authType: "oauth_discovery", envValues: `{"CLIENT_ID":"disc-123"}`}

	clientID, serverURL, err := resolveOAuthConfig(mcp.toStoreMCP())
	if err != nil {
		t.Fatalf("resolveOAuthConfig: %v", err)
	}
	if clientID != "disc-123" {
		t.Errorf("clientID = %q, want %q", clientID, "disc-123")
	}
	if serverURL != "https://api.example.com" {
		t.Errorf("serverURL = %q, want %q", serverURL, "https://api.example.com")
	}
}

func TestResolveOAuthConfig_NoDCR(t *testing.T) {
	// When no clientId and no CLIENT_ID env var, resolveOAuthConfig returns empty clientID (not an error).
	manifest := `{"url":"https://api.example.com/mcp/v1","oauth":{}}`
	mcp := &storeInstalledMCPShim{manifest: manifest, authType: "oauth"}

	clientID, serverURL, err := resolveOAuthConfig(mcp.toStoreMCP())
	if err != nil {
		t.Fatalf("resolveOAuthConfig: %v", err)
	}
	if clientID != "" {
		t.Errorf("clientID = %q, want empty (DCR will register)", clientID)
	}
	if serverURL != "https://api.example.com" {
		t.Errorf("serverURL = %q, want %q", serverURL, "https://api.example.com")
	}
}

// ---------------------------------------------------------------------------
// PKCE tests
// ---------------------------------------------------------------------------

func TestGeneratePKCE(t *testing.T) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE: %v", err)
	}
	if len(verifier) < 40 {
		t.Errorf("verifier too short: %d", len(verifier))
	}
	if len(challenge) < 40 {
		t.Errorf("challenge too short: %d", len(challenge))
	}
	if verifier == challenge {
		t.Error("verifier and challenge should differ")
	}
}

func TestRandomBase64(t *testing.T) {
	a, err := randomBase64(16)
	if err != nil {
		t.Fatalf("randomBase64: %v", err)
	}
	b, _ := randomBase64(16)
	if a == b {
		t.Error("two random values should not be equal")
	}
}

// ---------------------------------------------------------------------------
// MarketplaceStatsBuffer tests
// ---------------------------------------------------------------------------

func TestMarketplaceStatsBuffer_RecordAndFlush(t *testing.T) {
	buf := NewMarketplaceStatsBuffer()

	buf.RecordInstall("pkg-a", "mcp")
	buf.RecordInstall("pkg-a", "mcp")
	buf.RecordInstall("pkg-b", "skill")
	buf.RecordCall("pkg-a", "mcp")

	installs, calls, _, _ := buf.Flush()

	if installs[pkgKey{"pkg-a", "mcp"}] != 2 {
		t.Errorf("installs[pkg-a/mcp] = %d, want 2", installs[pkgKey{"pkg-a", "mcp"}])
	}
	if installs[pkgKey{"pkg-b", "skill"}] != 1 {
		t.Errorf("installs[pkg-b/skill] = %d, want 1", installs[pkgKey{"pkg-b", "skill"}])
	}
	if calls[pkgKey{"pkg-a", "mcp"}] != 1 {
		t.Errorf("calls[pkg-a/mcp] = %d, want 1", calls[pkgKey{"pkg-a", "mcp"}])
	}
}

func TestMarketplaceStatsBuffer_SessionsAndMessages(t *testing.T) {
	buf := NewMarketplaceStatsBuffer()
	buf.RecordSession()
	buf.RecordSession()
	buf.RecordMessage()
	buf.RecordMessage()
	buf.RecordMessage()

	_, _, sessions, messages := buf.Flush()
	if sessions != 2 {
		t.Errorf("sessions = %d, want 2", sessions)
	}
	if messages != 3 {
		t.Errorf("messages = %d, want 3", messages)
	}
}

func TestMarketplaceStatsBuffer_FlushResetsCounters(t *testing.T) {
	buf := NewMarketplaceStatsBuffer()
	buf.RecordInstall("x", "mcp")
	buf.Flush()

	installs, calls, sessions, messages := buf.Flush()
	if installs != nil || calls != nil || sessions != 0 || messages != 0 {
		t.Errorf("expected nil/0 after empty flush, got installs=%v calls=%v sessions=%d messages=%d", installs, calls, sessions, messages)
	}
}

func TestMarketplaceStatsBuffer_Concurrent(t *testing.T) {
	buf := NewMarketplaceStatsBuffer()
	done := make(chan struct{})

	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				buf.RecordInstall("pkg", "mcp")
				buf.RecordCall("pkg", "mcp")
				buf.RecordSession()
				buf.RecordMessage()
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	installs, calls, sessions, messages := buf.Flush()
	if installs[pkgKey{"pkg", "mcp"}] != 1000 {
		t.Errorf("installs[pkg/mcp] = %d, want 1000", installs[pkgKey{"pkg", "mcp"}])
	}
	if calls[pkgKey{"pkg", "mcp"}] != 1000 {
		t.Errorf("calls[pkg/mcp] = %d, want 1000", calls[pkgKey{"pkg", "mcp"}])
	}
	if sessions != 1000 {
		t.Errorf("sessions = %d, want 1000", sessions)
	}
	if messages != 1000 {
		t.Errorf("messages = %d, want 1000", messages)
	}
}

// ---------------------------------------------------------------------------
// helpers for tests
// ---------------------------------------------------------------------------

func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// storeInstalledMCPShim creates a store.InstalledMCP for testing without importing store in a circular way.
type storeInstalledMCPShim struct {
	manifest  string
	authType  string
	envValues string
}

func (s *storeInstalledMCPShim) toStoreMCP() *store.InstalledMCP {
	return &store.InstalledMCP{
		Name:      "test-mcp",
		Manifest:  s.manifest,
		AuthType:  s.authType,
		EnvValues: s.envValues,
	}
}

// Ensure json import is used
var _ = json.Marshal
