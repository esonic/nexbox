package store

import (
	"errors"
	"time"
)

// ErrVersionConflict is returned when an update operation fails due to
// an optimistic-locking version mismatch (the entity was modified by
// another client since it was last read).
var ErrVersionConflict = errors.New("version conflict: entity was modified by another client")

// Store defines the unified data access interface for all NexBox entities.
// Implementations: SQLiteStore (local), MySQLStore (remote DB), RemoteStore (HTTP client).
type Store interface {
	ProjectStore
	TaskStore
	TaskCommentStore
	LabelStore
	ReactionStore
	SessionStore
	AssetStore
	MessageStore
	QueueStore
	ReviewCommentStore
	ChangeStore
	WorkflowStore
	UserStore
	BalanceStore
}

type ProjectStore interface {
	CreateProject(req CreateProjectRequest) (*Project, error)
	ListProjects() ([]Project, error)
	GetProjectsByIDs(ids []string) ([]Project, error)
	GetProject(id string) (*Project, error)
	UpdateProject(id string, req UpdateProjectRequest) (*Project, error)
	DeleteProject(id string) error
}

type TaskStore interface {
	CreateTask(req CreateTaskRequest) (*Task, error)
	ListTasks(limit, offset int, projectIDs []string, q string) ([]Task, error)
	GetTask(id string) (*Task, error)
	UpdateTask(id string, req UpdateTaskRequest) (*Task, error)
	IncrementTaskRefCount(id string) error
	DeleteTask(id string) error
	LinkTaskAsset(taskID, assetID string) (*TaskAsset, error)
	ListTaskAssets(taskID string) ([]Asset, error)
	UnlinkTaskAsset(taskID, assetID string) error
}

type TaskCommentStore interface {
	ListTaskComments(taskID string) ([]TaskComment, error)
	CreateTaskComment(taskID string, req CreateTaskCommentRequest, author string) (*TaskComment, error)
	UpdateTaskComment(id string, req UpdateTaskCommentRequest) (*TaskComment, error)
	DeleteTaskComment(id string) error
}

type LabelStore interface {
	ListLabels(projectID string) ([]Label, error)
	CreateLabel(projectID string, req CreateLabelRequest) (*Label, error)
	DeleteLabel(projectID string, id string) error
	AddLabelToTask(taskID, labelName string) error
	RemoveLabelFromTask(taskID, labelName string) error
}

type ReactionStore interface {
	GetReactionSummary(targetType, targetID, currentUser string) ([]ReactionSummary, error)
	AddReaction(targetType, targetID, author, emoji string) error
	RemoveReaction(targetType, targetID, author, emoji string) error
}

type SessionStore interface {
	CreateSession(req CreateSessionRequest) (*Session, error)
	ListSessions() ([]Session, error)
	ListTrashedSessions() ([]Session, error)
	GetSession(id string) (*Session, error)
	UpdateSession(id string, req UpdateSessionRequest) (*Session, error)
	UpdateSessionAgentID(id, agentSessionID string) error
	UpdateSessionStreamID(id, streamID string) error
	UpdateSessionFeatures(id, features string) error
	UpdateSessionWorktree(id, mode, path string) error
	GetExpiredSessionWorktrees(olderThan time.Time) ([]string, error) // returns worktree_paths for soon-to-be-purged sessions
	DeleteSession(id string) error   // soft delete (move to trash)
	RestoreSession(id string) error  // restore from trash
	PurgeExpiredSessions(olderThan time.Time) ([]string, error) // hard delete, returns session IDs
	ReorderSessions(sessionIDs []string) error
}

type AssetStore interface {
	CreateAsset(projectID string, req CreateAssetRequest) (*Asset, error)
	ListAssets(projectID string, limit, offset int, q string) ([]Asset, error)
	ListAssetsByType(projectID, assetType string, limit, offset int, q string) ([]Asset, error)
	GetAsset(id string) (*Asset, error)
	UpdateAsset(id string, req UpdateAssetRequest) (*Asset, error)
	DeleteAsset(id string) error
	GetAssetVersions(assetID string) ([]AssetVersion, error)
	GetAssetVersion(assetID string, version int) (*AssetVersion, error)
	FindAssetByTitle(projectID, title string) (*Asset, error)
	GetLatestAssetContent(assetID string) (string, error)
}

type MessageStore interface {
	CreateMessage(sessionID, role string, parts []MessagePart, metadata MessageMetadata) (*ChatMessage, error)
	UpdateMessageParts(messageID string, parts []MessagePart, metadata MessageMetadata) error
	ListMessages(sessionID string) ([]ChatMessage, error)
	ListMessagesPaginated(sessionID string, limit int, beforeID string) ([]ChatMessage, bool, error)
	DeleteMessagesBySession(sessionID string) error
	CreateUserMessage(sessionID, content string) (*ChatMessage, error)
}

type QueueStore interface {
	ListQueuedMessages(sessionID string) ([]QueuedMessage, error)
	CreateQueuedMessage(sessionID string, req CreateQueuedMessageRequest) (*QueuedMessage, error)
	UpdateQueuedMessage(sessionID, id, text string) error
	DeleteQueuedMessage(sessionID, id string) error
	PopQueuedMessage(sessionID string) (*QueuedMessage, error)
	PopQueuedMessageByID(sessionID, id string) (*QueuedMessage, error)
	SessionsWithQueuedMessages() ([]string, error)
}

type ReviewCommentStore interface {
	CreateReviewComment(assetID string, req CreateReviewCommentRequest) (*ReviewComment, error)
	ListReviewComments(assetID string) ([]ReviewComment, error)
	GetReviewComment(id string) (*ReviewComment, error)
	UpdateReviewComment(id string, req UpdateReviewCommentRequest) (*ReviewComment, error)
	DeleteReviewComment(id string) error
	ResolveAllReviewComments(assetID string) (int, error)
}

type ChangeStore interface {
	ListChanges(sinceID int64, limit int, order string) ([]Change, error)
}

type WorkflowStore interface {
	// Workflow CRUD
	CreateWorkflow(projectID, name, description, createdBy string, definition []byte) (*Workflow, error)
	GetWorkflow(id string) (*Workflow, error)
	ListWorkflows(projectID string) ([]Workflow, error)
	ListAllWorkflows() ([]Workflow, error)
	UpdateWorkflow(id string, req UpdateWorkflowRequest) (*Workflow, error)
	DeleteWorkflow(id string) error

	// Execution
	CreateExecution(req CreateExecutionRequest) (*WorkflowExecution, error)
	GetExecution(id string) (*WorkflowExecution, error)
	ListExecutions(workflowID string, limit int, offset int) ([]WorkflowExecution, error)
	UpdateExecutionStatus(id string, status string, errorMsg string, durationMs *int64) error
	ResetStaleExecutions() error

	// Node Logs
	CreateNodeLog(log *WorkflowNodeLog) error
	GetNodeLogs(executionID string) ([]WorkflowNodeLog, error)
	UpdateNodeLog(id string, status string, input string, output string, errorMsg string, durationMs int64) error
	SetNodeHookInfo(id string, retries int, result string) error
	// FailStaleNodeLogs sets any node logs still in "running" state for the
	// given execution to the provided terminal status. Used as a safety net when
	// finishExecution is called to prevent nodes being stuck in "running" forever.
	FailStaleNodeLogs(executionID, status string) error
}

type UserStore interface {
	CreateUser(req CreateUserRequest) (*User, error)
	GetUser(id string) (*User, error)
	GetUserByUsername(username string) (*User, error)
	ListUsers(limit, offset int) ([]User, error)
	UpdateUserFeatures(id string, features int64) error
	UpdateUserPassword(id string, passwordHash string) error
	UpdateUserRole(id string, role string) error
}

type BalanceStore interface {
	GetBalance(userID string) (*UserBalance, error)
	// RechargeBalance adds tokens to the user's balance (admin operation).
	RechargeBalance(userID string, tokens int64, adminID string) error
	// ReserveTokens atomically moves `estimated` tokens from balance to reserved.
	// Returns error if balance is insufficient.
	ReserveTokens(userID string, estimated int64) error
	// SettleTokens finalizes a task: releases the reservation and charges actual usage.
	// If actual > estimated, additional tokens are deducted from balance.
	// If actual < estimated, the difference is returned to balance.
	SettleTokens(userID string, sessionID string, estimated, actual int64) error
	ListTransactions(userID string, limit, offset int) ([]TokenTransaction, error)
}

// MarketplaceStore manages user-installed plugins from the local marketplace directory.
type MarketplaceStore interface {
	ListInstalledPlugins(userID string) ([]InstalledPlugin, error)
	InstallPlugin(userID, pluginID, config string) (*InstalledPlugin, error)
	UninstallPlugin(userID, pluginID string) error
	GetInstalledPlugin(userID, pluginID string) (*InstalledPlugin, error)
}
