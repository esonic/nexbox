package api

import (
	"encoding/json"

	"github.com/esonic/nexbox/store"
)

// Type aliases pointing to the canonical definitions in the store package.
// Existing handler code can continue using api.Project, api.Session, etc.
type (
	Project         = store.Project
	Session         = store.Session
	ReviewComment   = store.ReviewComment
	MessagePart     = store.MessagePart
	MessageMetadata = store.MessageMetadata
	ChatMessage     = store.ChatMessage
	Task  = store.Task
	Asset = store.Asset
	AssetVersion    = store.AssetVersion
	TaskInfo        = store.TaskInfo
	AssetInfo       = store.AssetInfo

	CreateProjectRequest   = store.CreateProjectRequest
	UpdateProjectRequest   = store.UpdateProjectRequest
	CreateSessionRequest   = store.CreateSessionRequest
	UpdateSessionRequest   = store.UpdateSessionRequest
	ReorderSessionsRequest = store.ReorderSessionsRequest

	CreateReviewCommentRequest = store.CreateReviewCommentRequest
	UpdateReviewCommentRequest = store.UpdateReviewCommentRequest

	CreateTaskRequest  = store.CreateTaskRequest
	UpdateTaskRequest  = store.UpdateTaskRequest
	CreateAssetRequest = store.CreateAssetRequest
	UpdateAssetRequest = store.UpdateAssetRequest

	TaskComment              = store.TaskComment
	CreateTaskCommentRequest = store.CreateTaskCommentRequest
	UpdateTaskCommentRequest = store.UpdateTaskCommentRequest
	Label                    = store.Label
	CreateLabelRequest       = store.CreateLabelRequest
	ReactionSummary          = store.ReactionSummary

	QueuedMessage              = store.QueuedMessage
	CreateQueuedMessageRequest = store.CreateQueuedMessageRequest
	UpdateQueuedMessageRequest = store.UpdateQueuedMessageRequest
)

// ---------------------------------------------------------------------------
// WebSocket / chat types — api-specific, not part of Store
// ---------------------------------------------------------------------------

type WSMessage struct {
	Type    string      `json:"type"`
	Content interface{} `json:"content,omitempty"`
}

type ChatRequest struct {
	Type      string        `json:"type"`
	SessionID string        `json:"session_id,omitempty"`
	Content   string        `json:"content,omitempty"`
	Parts     []MessagePart `json:"parts,omitempty"`
	Images    []string      `json:"images,omitempty"`
	Features  []string      `json:"features,omitempty"`

	// For tool_response messages (interactive tool replies, e.g. AskUserQuestion)
	ToolUseID    string `json:"tool_use_id,omitempty"`
	ToolResponse string `json:"tool_response,omitempty"`
	// Skip marks an interactive tool reply as a deliberate skip: the
	// UserInputHandler returns an error so the tool is denied (same outcome as
	// the input timeout), rather than passing the text through as an answer.
	Skip bool `json:"skip,omitempty"`
}

type ChatEvent struct {
	Type      string `json:"type"`
	Content   string `json:"content,omitempty"`
	Tool      string `json:"tool,omitempty"`
	ToolID    string `json:"tool_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	// ParentToolID, on tool_start/tool_result events, is the tool_use_id
	// of the parent Task tool when the call came from a sub-agent.
	// Lets the UI nest sub-agent tool calls under the correct Agent card,
	// even with multiple sub-agents running in parallel.
	ParentToolID string `json:"parent_tool_id,omitempty"`
	Input        string `json:"input,omitempty"`
	Output       string `json:"output,omitempty"`
	IsError      bool   `json:"is_error,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	Message      string `json:"message,omitempty"`

	// SDKMessageUUID is the CLI-side message uuid for the assistant turn just
	// finished. Sent on the "done" event so the frontend can stamp the in-
	// memory message with it (the WebSocket-driven message in the React
	// store otherwise stays without this field until a page refresh
	// re-fetches from the API). Required by the fork button visibility check.
	SDKMessageUUID string `json:"sdk_message_uuid,omitempty"`

	// Widget (visualize_show_widget)
	WidgetTitle     string   `json:"widget_title,omitempty"`
	WidgetCode      string   `json:"widget_code,omitempty"`
	LoadingMessages []string `json:"loading_messages,omitempty"`

	Stats        *ExecutionStats `json:"stats,omitempty"`
	ContextUsage *ContextUsage   `json:"context_usage,omitempty"`
	SessionInfo  *SessionInfo    `json:"session_info,omitempty"`

	// WorkflowProgress carries the merged dynamic-workflow snapshot
	// ({phases, agents, status, summary}) on subtask_progress / subtask_done
	// events. ToolID is set to the Workflow tool_use_id so the frontend can
	// attach the tree to the right tool card.
	WorkflowProgress json.RawMessage `json:"workflow_progress,omitempty"`
}

type ExecutionStats struct {
	DurationMs               int64   `json:"duration_ms"`
	ToolCalls                int     `json:"tool_calls"`
	InputTokens              int     `json:"input_tokens,omitempty"`
	OutputTokens             int     `json:"output_tokens,omitempty"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens,omitempty"`
	TotalCostUSD             float64 `json:"total_cost_usd,omitempty"`
}

// ContextUsage is emitted to the frontend each time the agent reports an
// updated token count so the UI can render a live context-window indicator.
type ContextUsage struct {
	Model                 string `json:"model,omitempty"`
	UsedTokens            int    `json:"used_tokens"`
	InputTokens           int    `json:"input_tokens,omitempty"`
	OutputTokens          int    `json:"output_tokens,omitempty"`
	CachedInputTokens     int    `json:"cached_input_tokens,omitempty"`
	ReasoningOutputTokens int    `json:"reasoning_output_tokens,omitempty"`
	ContextWindow         int    `json:"context_window,omitempty"`
	MaxOutputTokens       int    `json:"max_output_tokens,omitempty"`
}

// SessionInfo carries the init-time metadata exposed by the agent (model
// resolved by the CLI, permission mode, CLI version, etc.).
type SessionInfo struct {
	Model          string `json:"model,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	Cwd            string `json:"cwd,omitempty"`
	Version        string `json:"version,omitempty"`
	OutputStyle    string `json:"output_style,omitempty"`
}
