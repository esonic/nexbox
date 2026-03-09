package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// SessionManager
// ---------------------------------------------------------------------------

func TestSessionManager_GetOrCreate_New(t *testing.T) {
	sm := NewSessionManager()

	info := sm.GetOrCreate("proj-1", "session-1", "/tmp/repo")
	if info == nil {
		t.Fatal("expected non-nil SessionInfo")
	}
	if info.ProjectID != "proj-1" {
		t.Errorf("ProjectID = %q, want %q", info.ProjectID, "proj-1")
	}
	if info.SessionID != "session-1" {
		t.Errorf("SessionID = %q, want %q", info.SessionID, "session-1")
	}
	if info.WorkDir != "/tmp/repo" {
		t.Errorf("WorkDir = %q, want %q", info.WorkDir, "/tmp/repo")
	}
	if info.IsActive {
		t.Error("new session should not be active")
	}
}

func TestSessionManager_GetOrCreate_Existing(t *testing.T) {
	sm := NewSessionManager()

	// Create first
	first := sm.GetOrCreate("proj-1", "session-1", "/tmp/repo")
	first.IsActive = true

	// Get same project - should return existing
	second := sm.GetOrCreate("proj-1", "session-different", "/different/path")
	if second != first {
		t.Error("expected same SessionInfo instance for same project")
	}
	if second.SessionID != "session-1" {
		t.Error("existing session should not be overwritten")
	}
}

func TestSessionManager_SetActive(t *testing.T) {
	sm := NewSessionManager()
	sm.GetOrCreate("proj-1", "s1", "/tmp")

	sm.SetActive("proj-1", true)
	if !sm.IsActive("proj-1") {
		t.Error("expected active after SetActive(true)")
	}

	sm.SetActive("proj-1", false)
	if sm.IsActive("proj-1") {
		t.Error("expected inactive after SetActive(false)")
	}
}

func TestSessionManager_SetActive_Nonexistent(t *testing.T) {
	sm := NewSessionManager()
	// Should auto-create entry when setting active=true
	sm.SetActive("nonexistent", true)
	if !sm.IsActive("nonexistent") {
		t.Error("expected auto-created session to be active")
	}
	sm.SetActive("nonexistent", false)
	if sm.IsActive("nonexistent") {
		t.Error("expected session to be cleaned up after SetActive(false)")
	}
}

func TestSessionManager_IsActive_Default(t *testing.T) {
	sm := NewSessionManager()
	if sm.IsActive("nonexistent") {
		t.Error("non-existent session should not be active")
	}
}

func TestSessionManager_Remove(t *testing.T) {
	sm := NewSessionManager()
	sm.GetOrCreate("proj-1", "s1", "/tmp")
	sm.SetActive("proj-1", true)

	sm.Remove("proj-1")

	if sm.IsActive("proj-1") {
		t.Error("removed session should not be active")
	}

	// GetOrCreate should create new
	info := sm.GetOrCreate("proj-1", "new-session", "/new/path")
	if info.SessionID != "new-session" {
		t.Errorf("after remove, should create new session, got %q", info.SessionID)
	}
}

func TestSessionManager_MultipleProjects(t *testing.T) {
	sm := NewSessionManager()

	sm.GetOrCreate("proj-1", "s1", "/repo1")
	sm.GetOrCreate("proj-2", "s2", "/repo2")

	sm.SetActive("proj-1", true)

	if !sm.IsActive("proj-1") {
		t.Error("proj-1 should be active")
	}
	if sm.IsActive("proj-2") {
		t.Error("proj-2 should not be active")
	}
}

// ---------------------------------------------------------------------------
// ClaudeAgent
// ---------------------------------------------------------------------------

func TestClaudeAgent_New(t *testing.T) {
	a := NewClaudeAgent()
	if a == nil {
		t.Fatal("expected non-nil ClaudeAgent")
	}
	if a.CLIPath != "claude" {
		t.Errorf("CLIPath = %q, want %q", a.CLIPath, "claude")
	}
}

func TestClaudeAgent_ImplementsAgent(t *testing.T) {
	var _ Agent = (*ClaudeAgent)(nil)
}

func TestClaudeAgent_SendMessage_InvalidWorkDir(t *testing.T) {
	a := NewClaudeAgent()
	ctx := context.Background()

	_, err := a.SendMessage(ctx, Request{
		ConversationID: "conv-1",
		SessionID:      "sess-1",
		WorkDir:        "/nonexistent/dir/that/does/not/exist",
		Message:        "hello",
	}, func(StreamEvent) {})
	if err == nil {
		t.Fatal("expected error for non-existent work dir")
	}
	if !strings.Contains(err.Error(), "work directory does not exist") {
		t.Errorf("error = %q, want to contain 'work directory does not exist'", err.Error())
	}
}

func TestClaudeAgent_SendMessage_WorkDirIsFile(t *testing.T) {
	a := NewClaudeAgent()
	ctx := context.Background()

	// Create a temp file (not a directory)
	tmpFile := filepath.Join(t.TempDir(), "not-a-dir.txt")
	if err := os.WriteFile(tmpFile, []byte("hello"), 0644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	_, err := a.SendMessage(ctx, Request{
		ConversationID: "conv-2",
		SessionID:      "sess-2",
		WorkDir:        tmpFile,
		Message:        "hello",
	}, func(StreamEvent) {})
	if err == nil {
		t.Fatal("expected error for file as work dir")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error = %q, want to contain 'not a directory'", err.Error())
	}
}

func TestClaudeAgent_HasMCP(t *testing.T) {
	a := NewClaudeAgent()
	if a.HasMCP() {
		t.Error("expected no MCP by default")
	}
	a.MCPServerURL = "http://localhost:9090/mcp"
	if !a.HasMCP() {
		t.Error("expected MCP when URL is set")
	}
}

// ---------------------------------------------------------------------------
// ClaudeClient.buildArgs
// ---------------------------------------------------------------------------

func TestBuildArgs_FirstTurn(t *testing.T) {
	c := NewClaudeClient()
	args := c.buildArgs(QueryOptions{
		SessionID: "sess-123",
	})

	expected := map[string]bool{
		"-p":              false,
		"--output-format": false,
		"stream-json":     false,
		"--session-id":    false,
		"sess-123":        false,
	}

	for _, arg := range args {
		if _, ok := expected[arg]; ok {
			expected[arg] = true
		}
	}

	for key, found := range expected {
		if !found {
			t.Errorf("expected arg %q not found in args: %v", key, args)
		}
	}

	// Should not have --resume
	for _, arg := range args {
		if arg == "--resume" {
			t.Error("first turn should not have --resume")
		}
	}
}

func TestBuildArgs_ResumeTurn(t *testing.T) {
	c := NewClaudeClient()
	args := c.buildArgs(QueryOptions{
		Resume: "sess-123",
	})

	hasResume := false
	hasSessionID := false
	for i, arg := range args {
		if arg == "--resume" {
			hasResume = true
			// Next arg should be session ID
			if i+1 < len(args) && args[i+1] != "sess-123" {
				t.Errorf("resume arg = %q, want %q", args[i+1], "sess-123")
			}
		}
		if arg == "--session-id" {
			hasSessionID = true
		}
	}

	if !hasResume {
		t.Error("resume turn should have --resume")
	}
	if hasSessionID {
		t.Error("resume turn should not have --session-id")
	}
}

func TestBuildArgs_AllowedTools(t *testing.T) {
	c := NewClaudeClient()
	args := c.buildArgs(QueryOptions{
		SessionID:    "sess-123",
		AllowedTools: []string{"Bash", "Read", "Write", "Edit", "Grep", "Glob"},
	})

	found := false
	for i, arg := range args {
		if arg == "--allowed-tools" {
			found = true
			// Remaining args after --allowed-tools should be the tools
			tools := args[i+1:]
			toolSet := make(map[string]bool)
			for _, tool := range tools {
				toolSet[tool] = true
			}
			for _, tool := range []string{"Bash", "Read", "Write", "Edit", "Grep", "Glob"} {
				if !toolSet[tool] {
					t.Errorf("missing tool %q in allowed-tools args: %v", tool, tools)
				}
			}
			break
		}
	}
	if !found {
		t.Error("expected --allowed-tools in args")
	}
}

func TestBuildArgs_WithMCPConfig(t *testing.T) {
	c := NewClaudeClient()
	args := c.buildArgs(QueryOptions{
		SessionID:     "sess-123",
		MCPConfigPath: "/tmp/mcp-config.json",
	})

	hasMCPConfig := false
	for i, arg := range args {
		if arg == "--mcp-config" {
			hasMCPConfig = true
			if i+1 < len(args) && args[i+1] != "/tmp/mcp-config.json" {
				t.Errorf("mcp-config path = %q, want %q", args[i+1], "/tmp/mcp-config.json")
			}
		}
	}
	if !hasMCPConfig {
		t.Error("expected --mcp-config in args")
	}
}

// helper function to check if a string contains a substring
func containsString(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// ---------------------------------------------------------------------------
// BuildStaticContext
// ---------------------------------------------------------------------------

func TestBuildStaticContext_Full(t *testing.T) {
	project := ProjectInfo{
		ID:          "proj-001",
		Name:        "Order Service",
		Description: "E-commerce order system",
	}

	ctx := BuildStaticContext(project, "sess-001")

	mustContain := []string{
		"BoxNex Platform Context",
		"You are working within the BoxNex development platform.",
		"Project: Order Service",
		"Project Description: E-commerce order system",
		"Project ID: proj-001",
		"Session ID: sess-001",
		"Working Directory: /home/dev/order-service",
		"[Guidelines]",
		"Use BoxNex MCP tools for asset/task management (project_id: proj-001)",
		"Respond in user's language",
		"Work within the project directory",
	}

	for _, s := range mustContain {
		if !containsString(ctx, s) {
			t.Errorf("context missing: %q", s)
		}
	}
}

func TestBuildStaticContext_NoDescription(t *testing.T) {
	project := ProjectInfo{
		ID:   "proj-002",
		Name: "Simple App",
	}

	ctx := BuildStaticContext(project, "")

	if containsString(ctx, "Project Description:") {
		t.Error("should not include Project Description when empty")
	}
	if !containsString(ctx, "Project: Simple App") {
		t.Error("should include project name")
	}
}

// ---------------------------------------------------------------------------
// BuildDynamicContext
// ---------------------------------------------------------------------------

func TestBuildDynamicContext_Empty(t *testing.T) {
	ctx := BuildDynamicContext(nil, nil, nil, nil)
	if ctx != "" {
		t.Errorf("expected empty string for no refs, got %q", ctx)
	}
}

func TestBuildDynamicContext_ProjectOnly(t *testing.T) {
	proj := &ProjectInfo{ID: "proj-1", Name: "My Project"}
	ctx := BuildDynamicContext(proj, nil, nil, nil)

	mustContain := []string{
		"[Project: My Project | ID: proj-1 | Dir: /tmp/proj]",
	}
	for _, s := range mustContain {
		if !containsString(ctx, s) {
			t.Errorf("context missing: %q", s)
		}
	}
	if containsString(ctx, "[Session References]") {
		t.Error("should not include Session References when no refs")
	}
}

func TestBuildDynamicContext_TasksOnly(t *testing.T) {
	tasks := []TaskRef{
		{ID: "task-1", Title: "Fix bug", Priority: "high", Status: "open"},
		{ID: "task-2", Title: "Add feature", Priority: "medium", Status: "in_progress"},
	}

	ctx := BuildDynamicContext(nil, tasks, nil, nil)

	mustContain := []string{
		"[Session References]",
		"Tasks:",
		"[ID: task-1] Fix bug (priority: high, status: open)",
		"[ID: task-2] Add feature (priority: medium, status: in_progress)",
	}

	for _, s := range mustContain {
		if !containsString(ctx, s) {
			t.Errorf("context missing: %q", s)
		}
	}

	if containsString(ctx, "Assets:") {
		t.Error("should not include Assets section when no assets")
	}
}

func TestBuildDynamicContext_AssetsOnly(t *testing.T) {
	assets := []AssetRef{
		{ID: "a-1", Type: "design_doc", Title: "API Design", Status: "draft"},
		{ID: "a-2", Type: "code_change", Title: "auth.go", Status: "approved"},
	}

	ctx := BuildDynamicContext(nil, nil, assets, nil)

	mustContain := []string{
		"[Session References]",
		"Assets:",
		"[ID: a-1, Type: design_doc] API Design (status: draft)",
		"[ID: a-2, Type: code_change] auth.go (status: approved)",
	}

	for _, s := range mustContain {
		if !containsString(ctx, s) {
			t.Errorf("context missing: %q", s)
		}
	}

	if containsString(ctx, "Tasks:") {
		t.Error("should not include Tasks section when no tasks")
	}
}

func TestBuildDynamicContext_Both(t *testing.T) {
	tasks := []TaskRef{
		{ID: "task-1", Title: "Fix bug", Priority: "high", Status: "open"},
	}
	assets := []AssetRef{
		{ID: "a-1", Type: "design_doc", Title: "API Design", Status: "draft"},
	}

	ctx := BuildDynamicContext(nil, tasks, assets, nil)

	mustContain := []string{
		"[Session References]",
		"Tasks:",
		"[ID: task-1] Fix bug",
		"Assets:",
		"[ID: a-1, Type: design_doc] API Design",
	}

	for _, s := range mustContain {
		if !containsString(ctx, s) {
			t.Errorf("context missing: %q", s)
		}
	}
}

// ---------------------------------------------------------------------------
// BuildPrompt
// ---------------------------------------------------------------------------

func TestBuildPrompt_NewSession(t *testing.T) {
	static := "[Static Context]\nYou are a helpful assistant."
	dynamic := "[Dynamic Context]\nTask: Fix bug"
	msg := "Hello, can you help?"

	prompt := BuildPrompt(static, dynamic, msg, true, nil)

	// Should contain all three parts
	if !containsString(prompt, static) {
		t.Error("new session prompt should contain static context")
	}
	if !containsString(prompt, dynamic) {
		t.Error("new session prompt should contain dynamic context")
	}
	if !containsString(prompt, msg) {
		t.Error("prompt should contain message")
	}
}

func TestBuildPrompt_ResumedSession(t *testing.T) {
	static := "[Static Context]\nYou are a helpful assistant."
	dynamic := "[Dynamic Context]\nTask: Fix bug"
	msg := "Hello again"

	prompt := BuildPrompt(static, dynamic, msg, false, nil)

	// Should NOT contain static context on resume
	if containsString(prompt, static) {
		t.Error("resumed session prompt should NOT contain static context")
	}
	// Should contain dynamic context
	if !containsString(prompt, dynamic) {
		t.Error("resumed session prompt should contain dynamic context")
	}
	if !containsString(prompt, msg) {
		t.Error("prompt should contain message")
	}
}

func TestBuildPrompt_NoDynamicContext(t *testing.T) {
	static := "[Static Context]\nYou are a helpful assistant."
	msg := "Just a message"

	prompt := BuildPrompt(static, "", msg, true, nil)

	if !containsString(prompt, static) {
		t.Error("should contain static context")
	}
	if !containsString(prompt, msg) {
		t.Error("should contain message")
	}
	// Check no double newlines from empty dynamic
	if strings.Contains(prompt, "\n\n\n\n") {
		t.Error("should not have excessive newlines when dynamic is empty")
	}
}

func TestBuildPrompt_EmptyStatic(t *testing.T) {
	dynamic := "[Session Refs]"
	msg := "Hello"

	prompt := BuildPrompt("", dynamic, msg, true, nil)

	if !containsString(prompt, dynamic) {
		t.Error("should contain dynamic context")
	}
	if !containsString(prompt, msg) {
		t.Error("should contain message")
	}
}

// ---------------------------------------------------------------------------
// CodexAgent
// ---------------------------------------------------------------------------

func TestCodexAgent_New(t *testing.T) {
	a := NewCodexAgent()
	if a == nil {
		t.Fatal("expected non-nil CodexAgent")
	}
	if a.CLIPath != "codex" {
		t.Errorf("CLIPath = %q, want %q", a.CLIPath, "codex")
	}
}

func TestCodexAgent_ImplementsAgent(t *testing.T) {
	var _ Agent = (*CodexAgent)(nil)
}

func TestCodexAgent_SendMessage_InvalidWorkDir(t *testing.T) {
	a := NewCodexAgent()
	ctx := context.Background()

	_, err := a.SendMessage(ctx, Request{
		ConversationID: "conv-1",
		WorkDir:        "/nonexistent/path/does/not/exist",
		Message:        "test",
	}, func(StreamEvent) {})

	if err == nil {
		t.Fatal("expected error for non-existent work dir")
	}
	if !strings.Contains(err.Error(), "work directory does not exist") {
		t.Errorf("error = %q, want to contain 'work directory does not exist'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// StreamEvent JSON
// ---------------------------------------------------------------------------

func TestStreamEvent_JSONRoundTrip(t *testing.T) {
	original := StreamEvent{
		Type:     EventToolStart,
		ToolID:   "call_123",
		ToolName: "Bash",
		Input:    `{"command": "ls -la"}`,
	}

	bytes, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded StreamEvent
	if err := json.Unmarshal(bytes, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Type != original.Type {
		t.Errorf("Type = %q, want %q", decoded.Type, original.Type)
	}
	if decoded.ToolID != original.ToolID {
		t.Errorf("ToolID = %q, want %q", decoded.ToolID, original.ToolID)
	}
	if decoded.ToolName != original.ToolName {
		t.Errorf("ToolName = %q, want %q", decoded.ToolName, original.ToolName)
	}
	if decoded.Input != original.Input {
		t.Errorf("Input = %q, want %q", decoded.Input, original.Input)
	}
}

func TestStreamEvent_Constants(t *testing.T) {
	// Verify constant values for documentation/API stability
	tests := map[string]string{
		EventTextDelta:  "text_delta",
		EventToolStart:  "tool_start",
		EventToolResult: "tool_result",
		EventThinking:   "thinking",
		EventError:      "error",
		EventDone:       "done",
		EventSystem:     "system",
	}

	for got, want := range tests {
		if got != want {
			t.Errorf("constant %q != %q", got, want)
		}
	}
}
