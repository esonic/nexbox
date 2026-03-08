package agent

import (
	"context"
	"errors"
)

// ErrSessionExpired is returned when a --resume attempt fails because the
// Claude CLI session no longer exists or is corrupted. Callers should clear
// the stored AgentSessionID and retry as a new session.
var ErrSessionExpired = errors.New("agent session expired or not found")

// Agent is the interface that all coding agent backends must implement.
// Each backend (Claude Code, Codex CLI, etc.) provides its own implementation
// that translates between this unified protocol and the backend-specific CLI/API.
type Agent interface {
	// SendMessage sends a user message to the agent and streams events back via callback.
	// Returns the result (including session ID for future resumption) or an error.
	SendMessage(ctx context.Context, req Request, cb EventCallback) (Result, error)
}

// MCPServerEntry describes an extra MCP server to inject into the agent runtime.
// These come from locally installed MCP packages (stored in SQLite installed_mcps).
type MCPServerEntry struct {
	Key       string            `json:"key"`               // server name in mcpServers config (e.g. "github")
	Type      string            `json:"type"`              // "http" or "stdio"
	URL       string            `json:"url,omitempty"`     // for http transport
	Headers   map[string]string `json:"headers,omitempty"` // HTTP headers for http transport; user-provided values stored with "header:" prefix in env_values
	Cmd       string            `json:"cmd,omitempty"`     // for stdio transport
	Args      []string          `json:"args,omitempty"`    // for stdio transport
	Env       map[string]string `json:"env,omitempty"`     // env vars for stdio transport
	ToolNames []string          `json:"tool_names,omitempty"` // allowed tool names (mcp__key__name)
}

// Request contains all parameters needed to send a message to an agent.
type Request struct {
	ConversationID string   // Unique conversation identifier
	SessionID      string   // Session ID for resuming previous sessions (empty for new)
	WorkDir        string   // Working directory (typically the git repo root)
	Model          string   // Model to use (e.g. "claude-sonnet-4-6", "claude-haiku-4-5")
	StaticContext  string   // Fixed context (project info, guidelines) - only for new sessions
	DynamicContext string   // Variable context (session refs) - every message
	Message        string   // The user's message
	Images         []string // Absolute file paths to images for vision analysis
	Features       []string // Active features for this session, e.g. ["chrome_devtools"]
	ChromeMCPURL       string   // Per-request Chrome DevTools MCP URL (set by chat.go when E2E is active)
	ChromeMCPToolNames []string // Per-request Chrome MCP tool names
	ExtraMCPServers    []MCPServerEntry // Extra MCP servers from installed marketplace packages
	PluginDirs         []string          // Directories to pass via --plugin-dir to Claude CLI
	UserInputHandler   UserInputHandler  // Optional: handle interactive tool calls (e.g., AskUserQuestion)
	UserFeatures       int64             // Bitmask of enabled features (store.Feature* constants)
}

// Result contains the outcome of an agent message exchange.
type Result struct {
	SessionID string                 // Session ID for future resumption
	Metadata  map[string]interface{} // Optional metadata (tokens, cost, duration, etc.)
}

// ---------------------------------------------------------------------------
// Unified streaming event types
//
// These are the canonical event types that form the contract between any Agent
// implementation and the API layer (chat.go). Each Agent must map its native
// events (e.g. Claude's "assistant"/"user"/"result", or Codex's "item.started"/
// "item.completed"/"turn.completed") to these unified types.
//
// The API/WebSocket layer only speaks this protocol — it never sees backend-
// specific event formats.
// ---------------------------------------------------------------------------

const (
	// EventTextDelta is an incremental text chunk from the agent's response.
	EventTextDelta = "text_delta"

	// EventToolStart indicates a tool call has been initiated.
	// Fields: ToolID, ToolName, Input
	EventToolStart = "tool_start"

	// EventToolResult indicates a tool call has completed.
	// Fields: ToolID, Output, IsError
	EventToolResult = "tool_result"

	// EventThinking is an extended thinking / reasoning chunk.
	// Fields: Content
	EventThinking = "thinking"

	// EventError indicates an error occurred during execution.
	// Fields: Content (error message), IsError
	EventError = "error"

	// EventDone signals the agent has finished processing the request.
	// Fields: SessionID, Metadata (tokens, cost, duration)
	EventDone = "done"

	// EventSystem is an informational system message (e.g. context compaction).
	// Fields: Content
	EventSystem = "system"
)

// StreamEvent represents a unified streaming event from any agent backend.
// All agent implementations must map their native events to the Event* constants above.
type StreamEvent struct {
	Type    string `json:"type"` // One of the Event* constants
	Content string `json:"content,omitempty"`

	// Tool-related fields (used by EventToolStart, EventToolResult)
	ToolID   string `json:"tool_id,omitempty"`
	ToolName string `json:"tool_name,omitempty"`
	Input    string `json:"input,omitempty"`
	Output   string `json:"output,omitempty"`
	IsError  bool   `json:"is_error,omitempty"`

	// Session and metadata (used by EventDone)
	SessionID string                 `json:"session_id,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// EventCallback is called for each streaming event from the agent.
type EventCallback func(event StreamEvent)

// UserInputHandler is called when the agent encounters an interactive tool
// (e.g., Claude CLI's AskUserQuestion) that requires user input via the
// control protocol's can_use_tool mechanism. It blocks until the user
// responds or the context is cancelled.
//
// Parameters:
//   - requestID: the control_request ID (for correlating the response)
//   - toolName: the tool name (e.g., "AskUserQuestion")
//   - input: JSON-serialized tool input (contains question, options, etc.)
//
// Returns the user's response text, or error if cancelled/timed out.
type UserInputHandler func(requestID, toolName, input string) (string, error)
