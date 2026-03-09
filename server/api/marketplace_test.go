package api

import (
	"os"
	"path/filepath"
	"testing"
)

// testTempDir creates a temporary directory that works in the sandbox environment.
func testTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "boxnex-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// ---------------------------------------------------------------------------
// isSubPath tests
// ---------------------------------------------------------------------------

func TestIsSubPath(t *testing.T) {
	cases := []struct {
		parent string
		child  string
		want   bool
	}{
		{"/tmp/dest", "/tmp/dest/file.txt", true},
		{"/tmp/dest", "/tmp/dest/sub/dir/file.txt", true},
		{"/tmp/dest", "/tmp/dest/a", true},             // single-char filename
		{"/tmp/dest", "/tmp/dest", true},               // same dir
		{"/tmp/dest", "/tmp/dest/../etc/passwd", false}, // path traversal via Clean
		{"/tmp/dest", "/tmp/other/file.txt", false},
		{"/tmp/dest", "/tmp/destother/file.txt", false}, // prefix but not sub-path
	}

	for _, c := range cases {
		// filepath.Clean normalises the path
		child := filepath.Clean(c.child)
		got := isSubPath(c.parent, child)
		if got != c.want {
			t.Errorf("isSubPath(%q, %q) = %v, want %v", c.parent, child, got, c.want)
		}
	}
}

func TestLoadMarketplacePlugins_CustomJSON(t *testing.T) {
	dir := testTempDir(t)
	content := `{"id":"my-mcp","name":"My MCP","description":"test","type":"mcp","version":"1.0"}`
	os.WriteFile(filepath.Join(dir, "my-mcp.json"), []byte(content), 0644)

	plugins, err := loadMarketplacePlugins(dir)
	if err != nil {
		t.Fatalf("loadMarketplacePlugins: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("got %d plugins, want 1", len(plugins))
	}
	if plugins[0].ID != "my-mcp" || plugins[0].Source != "custom" {
		t.Errorf("got id=%q source=%q, want id=my-mcp source=custom", plugins[0].ID, plugins[0].Source)
	}
}

func TestLoadMarketplacePlugins_SkillMD(t *testing.T) {
	dir := testTempDir(t)
	skillDir := filepath.Join(dir, "skills", "my-skill")
	os.MkdirAll(skillDir, 0755)
	skillContent := "---\nname: my-skill\ndescription: A test skill\n---\n# My Skill\nInstructions here."
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0644)

	plugins, err := loadMarketplacePlugins(dir)
	if err != nil {
		t.Fatalf("loadMarketplacePlugins: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("got %d plugins, want 1", len(plugins))
	}
	p := plugins[0]
	if p.ID != "skill-my-skill" {
		t.Errorf("id = %q, want skill-my-skill", p.ID)
	}
	if p.Type != "skill" || p.Source != "skill" {
		t.Errorf("type=%q source=%q, want skill/skill", p.Type, p.Source)
	}
	if p.Description != "A test skill" {
		t.Errorf("description = %q, want 'A test skill'", p.Description)
	}
}

func TestLoadMarketplacePlugins_ClaudePlugin(t *testing.T) {
	dir := testTempDir(t)
	pluginDir := filepath.Join(dir, "plugins", "my-plugin", ".claude-plugin")
	os.MkdirAll(pluginDir, 0755)
	pluginJSON := `{"name":"my-plugin","description":"A test plugin","version":"1.0.0","author":{"name":"Test Author"}}`
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(pluginJSON), 0644)

	plugins, err := loadMarketplacePlugins(dir)
	if err != nil {
		t.Fatalf("loadMarketplacePlugins: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("got %d plugins, want 1", len(plugins))
	}
	p := plugins[0]
	if p.Name != "my-plugin" || p.Author != "Test Author" {
		t.Errorf("name=%q author=%q, want my-plugin/Test Author", p.Name, p.Author)
	}
	if p.Type != "plugin" {
		t.Errorf("type=%q, want plugin (no .mcp.json)", p.Type)
	}
}

func TestLoadMarketplacePlugins_Connector(t *testing.T) {
	dir := testTempDir(t)
	connDir := filepath.Join(dir, "external_plugins", "github", ".claude-plugin")
	os.MkdirAll(connDir, 0755)
	pluginJSON := `{"name":"github","description":"GitHub MCP","author":{"name":"GitHub"}}`
	os.WriteFile(filepath.Join(connDir, "plugin.json"), []byte(pluginJSON), 0644)

	mcpJSON := `{"github":{"type":"http","url":"https://api.githubcopilot.com/mcp/","headers":{"Authorization":"Bearer ${GITHUB_PERSONAL_ACCESS_TOKEN}"}}}`
	os.WriteFile(filepath.Join(dir, "external_plugins", "github", ".mcp.json"), []byte(mcpJSON), 0644)

	plugins, err := loadMarketplacePlugins(dir)
	if err != nil {
		t.Fatalf("loadMarketplacePlugins: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("got %d plugins, want 1", len(plugins))
	}
	p := plugins[0]
	if p.Source != "connector" {
		t.Errorf("source=%q, want connector", p.Source)
	}
	if p.Type != "mcp" {
		t.Errorf("type=%q, want mcp", p.Type)
	}
	if p.AuthType != "apikey" {
		t.Errorf("auth_type=%q, want apikey", p.AuthType)
	}
	if len(p.EnvVars) != 1 || p.EnvVars[0] != "GITHUB_PERSONAL_ACCESS_TOKEN" {
		t.Errorf("env_vars=%v, want [GITHUB_PERSONAL_ACCESS_TOKEN]", p.EnvVars)
	}
}

func TestLoadMarketplacePlugins_OAuthDetection(t *testing.T) {
	dir := testTempDir(t)
	connDir := filepath.Join(dir, "external_plugins", "slack", ".claude-plugin")
	os.MkdirAll(connDir, 0755)
	os.WriteFile(filepath.Join(connDir, "plugin.json"), []byte(`{"name":"slack","description":"Slack"}`), 0644)
	mcpJSON := `{"slack":{"type":"http","url":"https://mcp.slack.com/mcp","oauth":{"clientId":"123","callbackPort":3118}}}`
	os.WriteFile(filepath.Join(dir, "external_plugins", "slack", ".mcp.json"), []byte(mcpJSON), 0644)

	plugins, err := loadMarketplacePlugins(dir)
	if err != nil {
		t.Fatalf("loadMarketplacePlugins: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("got %d plugins, want 1", len(plugins))
	}
	if plugins[0].AuthType != "oauth" {
		t.Errorf("auth_type=%q, want oauth", plugins[0].AuthType)
	}
}

func TestLoadMarketplacePlugins_MixedFormats(t *testing.T) {
	dir := testTempDir(t)

	// Custom JSON
	os.WriteFile(filepath.Join(dir, "custom.json"), []byte(`{"id":"custom-mcp","name":"Custom","type":"mcp"}`), 0644)

	// Skill
	skillDir := filepath.Join(dir, "skills", "test-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: test-skill\ndescription: test\n---\n"), 0644)

	// Plugin
	pluginDir := filepath.Join(dir, "plugins", "test-plugin", ".claude-plugin")
	os.MkdirAll(pluginDir, 0755)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"name":"test-plugin","description":"test"}`), 0644)

	plugins, err := loadMarketplacePlugins(dir)
	if err != nil {
		t.Fatalf("loadMarketplacePlugins: %v", err)
	}
	if len(plugins) != 3 {
		t.Fatalf("got %d plugins, want 3", len(plugins))
	}
}

func TestLoadMarketplacePlugins_EmptyDir(t *testing.T) {
	dir := testTempDir(t)
	plugins, err := loadMarketplacePlugins(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("got %d plugins, want 0", len(plugins))
	}
}

func TestLoadMarketplacePlugins_NonexistentDir(t *testing.T) {
	plugins, err := loadMarketplacePlugins("/nonexistent/path")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("got %d plugins, want 0", len(plugins))
	}
}

func TestParseSkillFrontmatter(t *testing.T) {
	cases := []struct {
		content  string
		wantName string
		wantDesc string
	}{
		{"---\nname: my-skill\ndescription: does things\n---\nbody", "my-skill", "does things"},
		{"---\nname: \"quoted\"\ndescription: 'single'\n---\n", "quoted", "single"},
		{"no frontmatter", "", ""},
		{"---\nonly: start\n", "", ""},
	}
	for _, c := range cases {
		name, desc := parseSkillFrontmatter(c.content)
		if name != c.wantName || desc != c.wantDesc {
			t.Errorf("parseSkillFrontmatter(%q) = (%q, %q), want (%q, %q)", c.content[:min(len(c.content), 30)], name, desc, c.wantName, c.wantDesc)
		}
	}
}

func TestLoadMarketplacePlugins_MarketplaceJSON(t *testing.T) {
	dir := testTempDir(t)
	repoDir := filepath.Join(dir, "my-third-party", ".claude-plugin")
	os.MkdirAll(repoDir, 0755)
	content := `{
		"name": "my-registry",
		"owner": {"name": "some-author"},
		"metadata": {"description": "test", "version": "1.0.0"},
		"plugins": [
			{"name": "tool-a", "description": "Tool A desc", "version": "1.0.0", "author": {"name": "Author A"}},
			{"name": "tool-b", "description": "Tool B desc", "version": "2.0.0"}
		]
	}`
	os.WriteFile(filepath.Join(repoDir, "marketplace.json"), []byte(content), 0644)

	plugins, err := loadMarketplacePlugins(dir)
	if err != nil {
		t.Fatalf("loadMarketplacePlugins: %v", err)
	}
	if len(plugins) != 2 {
		t.Fatalf("got %d plugins, want 2", len(plugins))
	}
	// tool-a should have its own author, tool-b should inherit owner
	for _, p := range plugins {
		if p.Name == "tool-a" && p.Author != "Author A" {
			t.Errorf("tool-a author=%q, want 'Author A'", p.Author)
		}
		if p.Name == "tool-b" && p.Author != "some-author" {
			t.Errorf("tool-b author=%q, want 'some-author' (inherited from owner)", p.Author)
		}
	}
}

func TestParseMCPConfig(t *testing.T) {
	data := []byte(`{"github":{"type":"http","url":"https://api.github.com/mcp/","headers":{"Authorization":"Bearer ${GITHUB_TOKEN}","X-Custom":"${CUSTOM_HEADER}"}}}`)
	vars, authType := parseMCPConfig(data)
	if authType != "apikey" {
		t.Errorf("authType = %q, want apikey", authType)
	}
	if len(vars) != 2 {
		t.Fatalf("got %d vars, want 2: %v", len(vars), vars)
	}
	// vars should be sorted
	if vars[0] != "CUSTOM_HEADER" || vars[1] != "GITHUB_TOKEN" {
		t.Errorf("vars = %v, want [CUSTOM_HEADER, GITHUB_TOKEN]", vars)
	}
}

func TestParseMCPConfig_OAuth(t *testing.T) {
	data := []byte(`{"slack":{"type":"http","url":"https://mcp.slack.com","oauth":{"clientId":"123"}}}`)
	_, authType := parseMCPConfig(data)
	if authType != "oauth" {
		t.Errorf("authType = %q, want oauth", authType)
	}
}

func TestParseMCPConfig_OAuthDiscovery(t *testing.T) {
	// Linear: HTTP URL with no auth headers → server advertises OAuth via discovery
	data := []byte(`{"linear":{"type":"http","url":"https://mcp.linear.app/mcp"}}`)
	vars, authType := parseMCPConfig(data)
	if authType != "oauth_discovery" {
		t.Errorf("authType = %q, want oauth_discovery", authType)
	}
	if len(vars) != 0 {
		t.Errorf("vars = %v, want empty", vars)
	}
}

func TestParseMCPConfig_Stdio(t *testing.T) {
	// context7: stdio subprocess with no env vars → none
	data := []byte(`{"context7":{"command":"npx","args":["-y","@upstash/context7-mcp@latest"]}}`)
	vars, authType := parseMCPConfig(data)
	if authType != "none" {
		t.Errorf("authType = %q, want none", authType)
	}
	if len(vars) != 0 {
		t.Errorf("vars = %v, want empty", vars)
	}
}

func TestParseMCPConfig_StdioWithEnv(t *testing.T) {
	// greptile: stdio with env var → apikey
	data := []byte(`{"greptile":{"command":"node","args":["greptile-mcp"],"env":{"GREPTILE_API_KEY":"${GREPTILE_API_KEY}"}}}`)
	vars, authType := parseMCPConfig(data)
	if authType != "apikey" {
		t.Errorf("authType = %q, want apikey", authType)
	}
	if len(vars) != 1 || vars[0] != "GREPTILE_API_KEY" {
		t.Errorf("vars = %v, want [GREPTILE_API_KEY]", vars)
	}
}

