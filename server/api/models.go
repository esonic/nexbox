package api

import "gitlab.okg.com/sre_ai/boxnex/store"

// Type aliases pointing to the canonical definitions in the store package.
// Existing handler code can continue using api.Project, api.Session, etc.
type (
	Project  = store.Project
	Session  = store.Session
	ReviewComment = store.ReviewComment
	MessagePart  = store.MessagePart
	MessageMetadata = store.MessageMetadata
	ChatMessage  = store.ChatMessage
	Task         = store.Task
	TaskAsset    = store.TaskAsset
	Asset        = store.Asset
	AssetVersion = store.AssetVersion
	TaskInfo     = store.TaskInfo
	AssetInfo    = store.AssetInfo

	CreateProjectRequest    = store.CreateProjectRequest
	UpdateProjectRequest    = store.UpdateProjectRequest
	CreateSessionRequest    = store.CreateSessionRequest
	UpdateSessionRequest    = store.UpdateSessionRequest
	ReorderSessionsRequest  = store.ReorderSessionsRequest

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
}

type ChatEvent struct {
	Type      string `json:"type"`
	Content   string `json:"content,omitempty"`
	Tool      string `json:"tool,omitempty"`
	ToolID    string `json:"tool_id,omitempty"`
	Input     string `json:"input,omitempty"`
	Output    string `json:"output,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message,omitempty"`

	Stats *ExecutionStats `json:"stats,omitempty"`
}

type ExecutionStats struct {
	DurationMs   int64   `json:"duration_ms"`
	ToolCalls    int     `json:"tool_calls"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	TotalCostUSD float64 `json:"total_cost_usd,omitempty"`
}
