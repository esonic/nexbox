package store

import (
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// Project
// ---------------------------------------------------------------------------

type Project struct {
	ID            string    `json:"id"`
	NamespaceID   string    `json:"namespace_id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	GitRepoPath   string    `json:"git_repo_path"`
	GitLabRepoURL string    `json:"gitlab_repo_url,omitempty"`
	CreatedBy     string    `json:"created_by"`
	OwnerID       string    `json:"owner_id"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}


type CreateProjectRequest struct {
	NamespaceID   string `json:"namespace_id,omitempty"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	GitRepoPath   string `json:"git_repo_path"`
	GitLabRepoURL string `json:"gitlab_repo_url"`
	OwnerID       string `json:"owner_id,omitempty"` // set by API layer
}

type UpdateProjectRequest struct {
	Name            *string `json:"name,omitempty"`
	Description     *string `json:"description,omitempty"`
	GitRepoPath     *string `json:"git_repo_path,omitempty"`
	GitLabRepoURL   *string `json:"gitlab_repo_url,omitempty"`
	ExpectedVersion *int    `json:"expected_version,omitempty"`
}

// ---------------------------------------------------------------------------
// Task
// ---------------------------------------------------------------------------

type Task struct {
	ID           string     `json:"id"`
	ProjectID    string     `json:"project_id"`
	ParentID     string     `json:"parent_id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	Status       string     `json:"status"`
	Priority     string     `json:"priority"`
	Assigner     string     `json:"assigner"`
	RefCount     int        `json:"ref_count"`
	CommentCount int        `json:"comment_count"`
	CreatedBy    string     `json:"created_by"`
	Version      int        `json:"version"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ClosedAt     *time.Time `json:"closed_at,omitempty"`
	Labels       []string   `json:"labels"`
}

type TaskAsset struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	AssetID   string    `json:"asset_id"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateTaskRequest struct {
	ProjectID   string `json:"project_id"`
	ParentID    string `json:"parent_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    string `json:"priority"`
	Assigner    string `json:"assigner"`
}

type UpdateTaskRequest struct {
	ParentID        *string `json:"parent_id,omitempty"`
	ProjectID       *string `json:"project_id,omitempty"`
	Title           *string `json:"title,omitempty"`
	Description     *string `json:"description,omitempty"`
	Status          *string `json:"status,omitempty"`
	Priority        *string `json:"priority,omitempty"`
	Assigner        *string `json:"assigner,omitempty"`
	ExpectedVersion *int    `json:"expected_version,omitempty"`
}

// ---------------------------------------------------------------------------
// Task Comment
// ---------------------------------------------------------------------------

type TaskComment struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CreateTaskCommentRequest struct {
	Body string `json:"body"`
}

type UpdateTaskCommentRequest struct {
	Body *string `json:"body,omitempty"`
}

// ---------------------------------------------------------------------------
// Label
// ---------------------------------------------------------------------------

type Label struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Color       string    `json:"color"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

type CreateLabelRequest struct {
	Name string `json:"name"`
}

// ---------------------------------------------------------------------------
// Reaction
// ---------------------------------------------------------------------------

type Reaction struct {
	ID         string    `json:"id"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	Author     string    `json:"author"`
	Emoji      string    `json:"emoji"`
	CreatedAt  time.Time `json:"created_at"`
}

type ReactionSummary struct {
	Emoji       string `json:"emoji"`
	Count       int    `json:"count"`
	UserReacted bool   `json:"user_reacted"`
}

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

type Session struct {
	ID             string     `json:"id"`
	NamespaceID    string     `json:"namespace_id"`
	Name           string     `json:"name"`
	ProjectID      string     `json:"project_id"`
	Agent          string     `json:"agent"`
	Model          string     `json:"model"`
	AgentSessionID string     `json:"agent_session_id"`
	StreamID       string     `json:"stream_id"`
	SortOrder      int        `json:"sort_order"`
	Features       string     `json:"features"`
	WorktreeMode   string     `json:"worktree_mode"`   // "local" | "worktree"
	WorktreePath   string     `json:"worktree_path"`   // empty until worktree is created
	CreatedBy      string     `json:"created_by"`
	UserID         string     `json:"user_id"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
}

type CreateSessionRequest struct {
	Name      string `json:"name"`
	ProjectID string `json:"project_id"`
	Agent     string `json:"agent"`
	Model     string `json:"model"`
	UserID    string `json:"user_id,omitempty"` // set by API layer
}

type UpdateSessionRequest struct {
	Name      *string `json:"name,omitempty"`
	ProjectID *string `json:"project_id,omitempty"`
	Agent     *string `json:"agent,omitempty"`
	Model     *string `json:"model,omitempty"`
}

type ReorderSessionsRequest struct {
	SessionIDs []string `json:"session_ids"`
}

// ---------------------------------------------------------------------------
// Asset
// ---------------------------------------------------------------------------

type AssetType string

const (
	AssetTypeRequirementDoc AssetType = "requirement_doc"
	AssetTypeDesignDoc      AssetType = "design_doc"
	AssetTypeCodeChange     AssetType = "code_change"
	AssetTypeUnitTest       AssetType = "unit_test"
	AssetTypeTestCase       AssetType = "test_case"
)

type AssetStatus string

const (
	AssetStatusDraft     AssetStatus = "draft"
	AssetStatusReviewing AssetStatus = "reviewing"
	AssetStatusApproved  AssetStatus = "approved"
)

type Asset struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"project_id"`
	Type           string    `json:"type"`
	Title          string    `json:"title"`
	CurrentVersion int       `json:"current_version"`
	Status         string    `json:"status"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type AssetWithContent struct {
	Asset
	Content string `json:"content"`
}

type AssetVersion struct {
	ID        string    `json:"id"`
	AssetID   string    `json:"asset_id"`
	Version   int       `json:"version"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateAssetRequest struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

type UpdateAssetRequest struct {
	Title           *string `json:"title,omitempty"`
	Content         *string `json:"content,omitempty"`
	Status          *string `json:"status,omitempty"`
	ExpectedVersion *int    `json:"expected_version,omitempty"`
}

// ---------------------------------------------------------------------------
// Chat Message
// ---------------------------------------------------------------------------

type MessagePart struct {
	Type       string      `json:"type"`
	Content    string      `json:"content,omitempty"`
	ToolCallID string      `json:"toolCallId,omitempty"`
	ToolName   string      `json:"toolName,omitempty"`
	Input      interface{} `json:"input,omitempty"`
	Output     string      `json:"output,omitempty"`
	State      string      `json:"state,omitempty"`
	IsError    bool        `json:"isError,omitempty"`
	Count      int         `json:"count,omitempty"`
	TaskID     string      `json:"taskId,omitempty"`
	TaskData   interface{} `json:"task,omitempty"`
	AssetID    string      `json:"assetId,omitempty"`
	Title      string      `json:"title,omitempty"`
	AssetType  string      `json:"assetType,omitempty"`
	WorkflowID string      `json:"workflowId,omitempty"`
}

type MessageMetadata struct {
	SessionID      string  `json:"sessionId,omitempty"`
	SDKMessageUUID string  `json:"sdkMessageUuid,omitempty"`
	InputTokens    int     `json:"inputTokens,omitempty"`
	OutputTokens   int     `json:"outputTokens,omitempty"`
	TotalCostUSD   float64 `json:"totalCostUsd,omitempty"`
	DurationMs     int64   `json:"durationMs,omitempty"`
	ToolCalls      int     `json:"toolCalls,omitempty"`
}

type ChatMessage struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Role      string          `json:"role"`
	Parts     []MessagePart   `json:"parts"`
	Metadata  MessageMetadata `json:"metadata,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Queued Message (per-session message queue)
// ---------------------------------------------------------------------------

type QueuedMessageImage struct {
	Filename string `json:"filename"`
	URL      string `json:"url"`
}

type QueuedMessage struct {
	ID        string              `json:"id"`
	SessionID string              `json:"session_id"`
	Text      string              `json:"text"`
	Images    []QueuedMessageImage `json:"images,omitempty"`
	CreatedAt time.Time           `json:"created_at"`
}

type CreateQueuedMessageRequest struct {
	Text   string               `json:"text"`
	Images []QueuedMessageImage `json:"images,omitempty"`
}

type UpdateQueuedMessageRequest struct {
	Text string `json:"text"`
}

// ---------------------------------------------------------------------------
// Review Comment
// ---------------------------------------------------------------------------

type ReviewComment struct {
	ID           string    `json:"id"`
	AssetID      string    `json:"asset_id"`
	AssetVersion int       `json:"asset_version"`
	LineNumber   *int      `json:"line_number,omitempty"`
	Content      string    `json:"content"`
	Author       string    `json:"author"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

type CreateReviewCommentRequest struct {
	AssetVersion int    `json:"asset_version"`
	LineNumber   *int   `json:"line_number,omitempty"`
	Content      string `json:"content"`
	Author       string `json:"author"`
}

type UpdateReviewCommentRequest struct {
	Content *string `json:"content,omitempty"`
	Status  *string `json:"status,omitempty"`
}

// ---------------------------------------------------------------------------
// Change (audit trail for shared data)
// ---------------------------------------------------------------------------

type Change struct {
	ID            int64     `json:"id"`
	ProjectID     string    `json:"project_id,omitempty"`
	EntityType    string    `json:"entity_type"`
	EntityID      string    `json:"entity_id"`
	Action        string    `json:"action"`
	Summary       string    `json:"summary,omitempty"`
	Detail        string    `json:"detail,omitempty"`
	UserName      string    `json:"user_name"`
	EntityVersion *int      `json:"entity_version,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	// Parsed from detail JSON for entity_type="notification"
	Recipient      string `json:"recipient,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	AssetID        string `json:"asset_id,omitempty"`
	LineNumber     *int   `json:"line_number,omitempty"`
	CommentID      string `json:"comment_id,omitempty"`
	CommentPreview string `json:"comment_preview,omitempty"`
}

// ---------------------------------------------------------------------------
// Cross-domain helpers (not part of Store interface)
// ---------------------------------------------------------------------------

type TaskInfo struct {
	ID          string
	Title       string
	Description string
	Status      string
	Priority    string
	Assigner    string
}

type AssetInfo struct {
	ID     string
	Type   string
	Title  string
	Status string
}

// ---------------------------------------------------------------------------
// Workflow
// ---------------------------------------------------------------------------

// WorkflowSchedule defines when a workflow runs automatically.
// Type "interval": runs every IntervalHours hours (1–24) after last completion.
// Type "weekly": runs on specific Days (0=Sun,1=Mon...6=Sat) at Hour:Minute (local time).
type WorkflowSchedule struct {
	Type          string `json:"type"`                     // "interval" | "weekly"
	IntervalHours int    `json:"interval_hours,omitempty"` // 1–24
	Days          []int  `json:"days,omitempty"`           // 0=Sun … 6=Sat
	Hour          int    `json:"hour,omitempty"`
	Minute        int    `json:"minute,omitempty"`
}

type Workflow struct {
	ID          string          `json:"id"`
	ProjectID   string          `json:"project_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Definition  json.RawMessage `json:"definition"` // {nodes, edges}
	Version     int             `json:"version"`
	Status      string          `json:"status"` // draft/active/archived
	CreatedBy   string          `json:"created_by"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	Schedule    string          `json:"schedule,omitempty"` // JSON-encoded WorkflowSchedule, empty = no schedule
	IsRunning   bool            `json:"is_running,omitempty"` // computed at query time, not stored
}

type WorkflowDefinition struct {
	Nodes []WorkflowNode `json:"nodes"`
	Edges []WorkflowEdge `json:"edges"`
}

type WorkflowNode struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"` // llm/http/condition/transform/code
	Position map[string]float64     `json:"position"` // {x, y}
	Data     map[string]interface{} `json:"data"` // {label, config}
}

type WorkflowEdge struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Label  string `json:"label,omitempty"` // 用于条件分支标注
}

type WorkflowExecution struct {
	ID                 string     `json:"id"`
	WorkflowID         string     `json:"workflow_id"`
	WorkflowVersion    int        `json:"workflow_version"`
	DefinitionSnapshot string     `json:"definition_snapshot"` // JSON snapshot of definition at execution time
	Status             string     `json:"status"`              // pending|running|completed|failed|cancelled
	TriggeredBy        string     `json:"triggered_by"`
	UserContext        string     `json:"user_context,omitempty"`
	Error              string     `json:"error,omitempty"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
	DurationMs         *int64     `json:"duration_ms,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
}

type WorkflowNodeLog struct {
	ID          string     `json:"id"`
	ExecutionID string     `json:"execution_id"`
	NodeID      string     `json:"node_id"`
	NodeType    string     `json:"node_type"`
	NodeLabel   string     `json:"node_label"`           // display name at execution time
	Level       int        `json:"level"`                // topological DAG level; same level = parallel nodes
	Status      string     `json:"status"`               // pending|running|waiting|completed|failed|cancelled
	Input       string     `json:"input,omitempty"`      // input to this node; for human nodes: the continue input
	Output      string     `json:"output,omitempty"`
	Error       string     `json:"error,omitempty"`
	HookRetries int        `json:"hook_retries,omitempty"` // number of hook retry iterations
	HookResult  string     `json:"hook_result,omitempty"`  // ok|ok_after_retries|agent_error|parse_error|skipped
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	DurationMs  *int64     `json:"duration_ms,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// CreateExecutionRequest is passed to CreateExecution to avoid a long parameter list.
type CreateExecutionRequest struct {
	WorkflowID         string
	WorkflowVersion    int
	DefinitionSnapshot string
	TriggeredBy        string
	UserContext        string
}

type CreateWorkflowRequest struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Definition  json.RawMessage `json:"definition"`
}

type UpdateWorkflowRequest struct {
	Name            *string          `json:"name,omitempty"`
	Description     *string          `json:"description,omitempty"`
	Definition      *json.RawMessage `json:"definition,omitempty"`
	Status          *string          `json:"status,omitempty"`
	ExpectedVersion *int             `json:"expected_version,omitempty"`
	Schedule        *string          `json:"schedule,omitempty"` // JSON-encoded WorkflowSchedule or "" to clear
}

type ExecuteWorkflowRequest struct {
	Input map[string]interface{} `json:"input,omitempty"`
}

// ---------------------------------------------------------------------------
// User
// ---------------------------------------------------------------------------

// Feature flag bitmask constants for User.Features field.
const (
	FeatureTask     int64 = 1 << 0 // can see Task management
	FeatureAsset    int64 = 1 << 1 // can see Asset management
	FeatureWorkflow int64 = 1 << 2 // can see Workflow management
	FeatureTerminal int64 = 1 << 3 // can see Terminal
	FeatureGit      int64 = 1 << 4 // can see Git / Files panels
)

type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Email        string    `json:"email,omitempty"`
	Role         string    `json:"role"` // "admin" | "user"
	Features     int64     `json:"features"` // bitmask of Feature* constants
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type CreateUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"` // plain text, caller must hash before passing to store
	Email    string `json:"email,omitempty"`
	Role     string `json:"role"` // default "user"
}

// ---------------------------------------------------------------------------
// Billing
// ---------------------------------------------------------------------------

type UserBalance struct {
	UserID         string    `json:"user_id"`
	BalanceTokens  int64     `json:"balance_tokens"`
	ReservedTokens int64     `json:"reserved_tokens"`
	TotalRecharged int64     `json:"total_recharged"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type TokenTransaction struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	SessionID string    `json:"session_id,omitempty"`
	Delta     int64     `json:"delta"`   // positive = recharge, negative = consume
	Reason    string    `json:"reason"`  // "recharge" | "reserve" | "settle"
	CreatedAt time.Time `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Marketplace Plugin (local directory listing)
// ---------------------------------------------------------------------------

// MarketplacePlugin represents a plugin available in the admin-maintained
// marketplace directory. Supports three formats:
//   - Custom JSON files (*.json)
//   - Anthropic skills (SKILL.md with YAML frontmatter)
//   - Anthropic plugins (.claude-plugin/plugin.json + optional .mcp.json)
type MarketplacePlugin struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"`              // "mcp", "skill", or "plugin"
	Version     string   `json:"version"`
	Command     string   `json:"command,omitempty"` // for mcp servers (stdio)
	Args        []string `json:"args,omitempty"`
	EnvVars     []string `json:"env_vars,omitempty"` // required env var names
	IconURL     string   `json:"icon_url,omitempty"`
	Author      string   `json:"author,omitempty"`       // from plugin.json author.name or SKILL.md
	Source      string   `json:"source,omitempty"`        // "custom", "skill", "plugin", "connector"
	MCPConfig   string   `json:"mcp_config,omitempty"`   // raw .mcp.json content (JSON string)
	AuthType    string   `json:"auth_type,omitempty"`     // "oauth", "apikey", "none" — derived from .mcp.json
	InstallPath string   `json:"install_path,omitempty"` // absolute path on disk (for plugin --plugin-dir install)
}

// ---------------------------------------------------------------------------
// Installed Plugin (Marketplace)
// ---------------------------------------------------------------------------

// InstalledPlugin represents a plugin installed by a user from the local
// marketplace directory.
type InstalledPlugin struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	PluginID  string    `json:"plugin_id"`
	Config    string    `json:"config"` // JSON: env var values filled in by user
	CreatedAt time.Time `json:"created_at"`
}
