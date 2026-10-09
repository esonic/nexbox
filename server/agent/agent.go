package agent

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrSessionExpired is returned when a backend can no longer resume its stored
// session. Callers should clear AgentSessionID and retry as a new session.
var ErrSessionExpired = errors.New("agent session expired or not found")

// ErrInterjectOutcomeUnknown is returned by an interject injector when the
// message may have reached the running turn but delivery could not be
// confirmed (e.g. a timeout after the request was already written). Callers
// must NOT fall back to re-sending the message — that risks delivering it
// twice.
var ErrInterjectOutcomeUnknown = errors.New("interject outcome unknown")

// Agent is the interface that all coding agent backends must implement.
// Each backend (Claude Code, Codex CLI, etc.) provides its own implementation
// that translates between this unified protocol and the backend-specific CLI/API.
type Agent interface {
	// Capabilities reports optional interaction features without exposing the
	// backend or protocol used to implement them.
	Capabilities() Capabilities
	// SendMessage sends a user message to the agent and streams events back via callback.
	// Returns the result (including session ID for future resumption) or an error.
	SendMessage(ctx context.Context, req Request, cb EventCallback) (Result, error)
}

// Capabilities describes backend features understood by the shared API/UI.
// Callers must branch on these flags, never on concrete agent names.
type Capabilities struct {
	Fork      bool `json:"fork"`
	Interject bool `json:"interject"`
	// ChromeDevTools reports whether the backend can attach the per-session
	// Chrome DevTools MCP server (the chrome_devtools E2E testing feature).
	ChromeDevTools bool `json:"chrome_devtools"`
	// ModelSwitchMode defines when a selected model can be changed. Empty is
	// kept equivalent to current_session for older third-party adapters.
	ModelSwitchMode ModelSwitchMode `json:"model_switch_mode,omitempty"`
}

// ModelSwitchMode describes the lifecycle at which a backend accepts a model
// change. It is deliberately backend-neutral: native and ACP adapters expose
// the same UI contract without the frontend knowing their transport.
type ModelSwitchMode string

const (
	// ModelSwitchCurrentSession means the backend accepts a different model on
	// an existing conversation.
	ModelSwitchCurrentSession ModelSwitchMode = "current_session"
	// ModelSwitchNewSessionOnly means the model is fixed once the first
	// message creates the backend session.
	ModelSwitchNewSessionOnly ModelSwitchMode = "new_session_only"
)

// ModelCatalog is implemented by backends that can expose a model picker.
// The source string identifies where the catalog came from (for example a
// live CLI query or Nexbox configuration).
type ModelCatalog interface {
	ListSupportedModels(context.Context) ([]ModelInfo, string)
}

// ForkContext identifies a durable point in an existing agent session. The
// checkpoint is opaque outside the backend adapter that produced it.
type ForkContext struct {
	SessionID  string
	Checkpoint string
}

// MCPServerEntry describes an extra MCP server to inject into the agent runtime.
// These come from locally installed MCP packages (stored in SQLite installed_mcps).
type MCPServerEntry struct {
	Key              string            `json:"key"`                          // server name in mcpServers config (e.g. "github")
	Type             string            `json:"type"`                         // "http" or "stdio"
	URL              string            `json:"url,omitempty"`                // for http transport
	Headers          map[string]string `json:"headers,omitempty"`            // complete HTTP headers forwarded to the MCP server
	OAuth            json.RawMessage   `json:"oauth,omitempty"`              // optional marketplace OAuth client configuration; not passed to agent runtimes
	Cmd              string            `json:"cmd,omitempty"`                // for stdio transport
	Args             []string          `json:"args,omitempty"`               // for stdio transport
	Env              map[string]string `json:"env,omitempty"`                // env vars for stdio transport
	ToolNames        []string          `json:"tool_names,omitempty"`         // allowed tool names (mcp__key__name)
	AppendSessionURL bool              `json:"append_session_url,omitempty"` // if true, append ?session=<conversationID> to URL at runtime
}

// UserInputHandler is called when an agent needs interactive user input
// (e.g., Claude CLI's AskUserQuestion tool). The handler should present the
// question to the user, block until they respond, and return the response string.
// Parameters: requestID (control protocol request ID), toolName, input (JSON).
type UserInputHandler func(requestID, toolName, input string) (string, error)

// Request contains all parameters needed to send a message to an agent.
type Request struct {
	ConversationID string // Unique conversation identifier
	SessionID      string // Session ID for resuming previous sessions (empty for new)
	WorkDir        string // Working directory (typically the git repo root)
	Model          string // Model to use (agent-specific; e.g. "gpt-5.3-codex", "haiku")
	// Effort is the reasoning effort for the current turn. Valid values come
	// from the live per-model catalog — see ModelInfo.SupportedEfforts. Known
	// vocabularies per backend: Claude accepts {low, medium, high, xhigh, max};
	// Codex accepts {none, minimal, low, medium, high, xhigh}. Empty string
	// lets the CLI use its own default. Each agent drops the value if the
	// active model doesn't advertise it (see validateEffort).
	Effort             string
	StaticContext      string           // Fixed context (project info, guidelines) - only for new sessions
	DynamicContext     string           // Variable context (session refs) - every message
	Message            string           // The user's message
	Images             []string         // Absolute file paths to images for vision analysis
	Features           []string         // Active features for this session, e.g. ["chrome_devtools"]
	ChromeMCPURL       string           // Per-request Chrome DevTools MCP URL (set by chat.go when E2E is active)
	ChromeMCPToolNames []string         // Per-request Chrome MCP tool names
	ExtraMCPServers    []MCPServerEntry // Extra MCP servers from installed marketplace packages
	PluginDirs         []string         // Extra plugin directories (--plugin-dir) from installed marketplace plugins
	UserInputHandler   UserInputHandler // Optional: handle interactive tool calls (e.g., AskUserQuestion)

	// RegisterInterject, when set, is invoked by capable backends during an
	// active turn.
	// It receives a function that injects an additional user message into the
	// running turn (queue-jump / interjection); the model picks it up at the
	// next turn boundary without aborting current work. The API layer stores it
	// per session so a later "interject" action can deliver a message mid-turn.
	// Backends without this capability never call it.
	RegisterInterject func(inject func(content string) error)

	// Fork asks the backend to branch from an opaque durable checkpoint. Only
	// agents advertising Capabilities.Fork receive this field.
	Fork *ForkContext
}

// Result contains the outcome of an agent message exchange.
type Result struct {
	SessionID  string                 // Session ID for future resumption
	Checkpoint string                 // Opaque durable checkpoint for this completed turn
	Metadata   map[string]interface{} // Optional metadata (tokens, cost, duration, etc.)
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
	// Fields: ToolID, Output, IsError, Diff
	EventToolResult = "tool_result"

	// EventToolInputReplace replaces a tool_use's full Input in place, unlike
	// EventToolStart's tool_input_delta counterpart (append-only, for
	// streamed partial JSON). Used when a backend's authoritative payload for
	// a tool call (e.g. Codex's per-file diffs on a FileChange item) can
	// legitimately arrive or change after item/started already fired.
	// Fields: ToolID, Input
	EventToolInputReplace = "tool_input_replace"

	// EventThinking is an extended thinking / reasoning chunk.
	// Fields: Content, Metadata["signature"]
	EventThinking = "thinking"

	// EventError indicates an error occurred during execution.
	// Fields: Content (error message), IsError
	EventError = "error"

	// EventDone signals the agent has finished processing the request.
	// Fields: SessionID, Metadata (tokens, cost, duration, stopReason)
	EventDone = "done"

	// EventSystem is an informational system message (e.g. context compaction).
	// Fields: Content, Metadata
	EventSystem = "system"

	// EventSubTaskStart is emitted when Claude delegates a sub-task to a sub-agent (Agent tool).
	// Fields: Content (description), Metadata["task_id"]
	EventSubTaskStart = "subtask_start"

	// EventSubTaskProgress is emitted periodically while a sub-task is running.
	// Fields: Content (description), Metadata["task_id", "last_tool_name"]
	EventSubTaskProgress = "subtask_progress"

	// EventSubTaskDone is emitted when a sub-task completes.
	// Fields: Content (summary), Metadata["task_id", "status"]
	EventSubTaskDone = "subtask_done"

	// EventContextUsage reports current context-window consumption.
	// Fields: Metadata["model", "inputTokens", "cachedInputTokens",
	//                    "outputTokens", "contextWindow", "maxOutputTokens"].
	// Emitted after each turn so the UI can render a live usage indicator.
	EventContextUsage = "context_usage"

	// EventSessionInfo reports session-level metadata discovered at init.
	// Fields: Metadata["model", "permissionMode", "cwd", "version"].
	EventSessionInfo = "session_info"
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

	// Diff carries a unified diff (git-style ---/+++/@@ hunks) describing a
	// file mutation performed by an edit/write-family tool call. Populated on
	// EventToolResult for single-file tools (edit/write/multiedit/...). Empty
	// when the tool made no file change or no diff could be derived.
	Diff string `json:"diff,omitempty"`

	// ParentToolID is set when a tool call was issued from inside a
	// sub-agent (Task tool). It holds the tool_use_id of the parent Task
	// invocation, so the UI can nest the call under the correct Agent
	// card even when several sub-agents run in parallel. Empty for tool
	// calls made by the main agent.
	ParentToolID string `json:"parent_tool_id,omitempty"`

	// Session and metadata (used by EventDone). The turn's durable checkpoint
	// travels only on Result.Checkpoint — one delivery channel, not two.
	SessionID string                 `json:"session_id,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// EventCallback is called for each streaming event from the agent.
type EventCallback func(event StreamEvent)
