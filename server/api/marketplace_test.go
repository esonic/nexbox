package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testTempDir creates a temporary directory that works in the sandbox environment.
func testTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nexbox-test-*")
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

// ---------------------------------------------------------------------------
// extractTarGz tests
// ---------------------------------------------------------------------------

// buildTarGz creates an in-memory .tar.gz containing the given files.
// Each entry in files is {name, content} where name is the tar path
// (may include a leading directory component to simulate real archives).
func buildTarGz(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, content := range files {
		hdr := &tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Mode:     0644,
			Size:     int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writeHeader %q: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write %q: %v", name, err)
		}
	}
	// Also add a top-level directory entry (optional but common in npm tarballs)
	hdr := &tar.Header{
		Name:     "skill-pkg-1.0.0/",
		Typeflag: tar.TypeDir,
		Mode:     0755,
	}
	_ = tw.WriteHeader(hdr) // ignore error if already exists

	if err := tw.Close(); err != nil {
		t.Fatalf("tw.Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz.Close: %v", err)
	}
	return &buf
}

func TestExtractTarGz_BasicExtraction(t *testing.T) {
	files := map[string]string{
		"skill-pkg-1.0.0/skill.md":        "# My Skill",
		"skill-pkg-1.0.0/helper.sh":       "#!/bin/sh\necho hi",
		"skill-pkg-1.0.0/sub/config.json": `{"key":"value"}`,
	}
	buf := buildTarGz(t, files)

	destDir := testTempDir(t)
	if err := extractTarGz(buf, destDir); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}

	// Verify files are extracted (top-level prefix stripped)
	want := map[string]string{
		"skill.md":        "# My Skill",
		"helper.sh":       "#!/bin/sh\necho hi",
		"sub/config.json": `{"key":"value"}`,
	}
	for rel, wantContent := range want {
		path := filepath.Join(destDir, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("file %q not extracted: %v", rel, err)
			continue
		}
		if string(data) != wantContent {
			t.Errorf("file %q content = %q, want %q", rel, data, wantContent)
		}
	}
}

func TestExtractTarGz_PathTraversalRejected(t *testing.T) {
	// Build an archive with a path-traversal entry
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	malicious := &tar.Header{
		Name:     "../../evil.txt",
		Typeflag: tar.TypeReg,
		Mode:     0644,
		Size:     3,
	}
	_ = tw.WriteHeader(malicious)
	_, _ = tw.Write([]byte("bad"))
	_ = tw.Close()
	_ = gz.Close()

	destDir := testTempDir(t)
	// Should not return an error (bad entries are skipped), but file must not exist
	if err := extractTarGz(&buf, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evilPath := filepath.Join(filepath.Dir(destDir), "evil.txt")
	if _, err := os.Stat(evilPath); !os.IsNotExist(err) {
		t.Errorf("path traversal file was extracted to %q", evilPath)
	}
}

func TestExtractTarGz_NoPrefixEntries(t *testing.T) {
	// An archive where all entries have a top-level directory → they should be skipped/stripped
	files := map[string]string{
		"myskill/":          "", // directory-only entry
		"myskill/README.md": "Hello",
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		var typeflag byte
		if strings.HasSuffix(name, "/") {
			typeflag = tar.TypeDir
			hdr := &tar.Header{Name: name, Typeflag: typeflag, Mode: 0755}
			_ = tw.WriteHeader(hdr)
		} else {
			typeflag = tar.TypeReg
			hdr := &tar.Header{Name: name, Typeflag: typeflag, Mode: 0644, Size: int64(len(content))}
			_ = tw.WriteHeader(hdr)
			_, _ = tw.Write([]byte(content))
		}
	}
	_ = tw.Close()
	_ = gz.Close()

	destDir := testTempDir(t)
	if err := extractTarGz(&buf, destDir); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(destDir, "README.md"))
	if err != nil {
		t.Fatalf("README.md not extracted: %v", err)
	}
	if string(data) != "Hello" {
		t.Errorf("content = %q, want %q", data, "Hello")
	}
}

// ---------------------------------------------------------------------------
// loadMarketplacePlugins scanner tests
// ---------------------------------------------------------------------------

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

func TestParseMCPConfig_NoAuth(t *testing.T) {
	data := []byte(`{"linear":{"type":"http","url":"https://mcp.linear.app/mcp"}}`)
	vars, authType := parseMCPConfig(data)
	if authType != "none" {
		t.Errorf("authType = %q, want none", authType)
	}
	if len(vars) != 0 {
		t.Errorf("vars = %v, want empty", vars)
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

	// After flush the counters should be reset
	installs, calls, sessions, messages := buf.Flush()
	if installs != nil || calls != nil || sessions != 0 || messages != 0 {
		t.Errorf("expected nil/0 after empty flush, got installs=%v calls=%v sessions=%d messages=%d", installs, calls, sessions, messages)
	}
}

func TestMarketplaceStatsBuffer_EmptyFlush(t *testing.T) {
	buf := NewMarketplaceStatsBuffer()
	installs, calls, sessions, messages := buf.Flush()
	if installs != nil || calls != nil || sessions != 0 || messages != 0 {
		t.Errorf("empty buffer should return nil, nil, 0, 0; got %v, %v, %d, %d", installs, calls, sessions, messages)
	}
}

func TestMarketplaceStatsBuffer_Concurrent(t *testing.T) {
	buf := NewMarketplaceStatsBuffer()
	done := make(chan struct{})

	// Concurrent writes from multiple goroutines
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
