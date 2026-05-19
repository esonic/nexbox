package store

import "time"

// CompositeStore routes session and message operations to a local SQLiteStore
// while delegating all other operations to the primary (remote) store.
// This keeps chat data local for privacy while sharing projects/tasks/assets remotely.
type CompositeStore struct {
	Store
	local *SQLiteStore
}

func NewCompositeStore(primary Store, local *SQLiteStore) *CompositeStore {
	return &CompositeStore{Store: primary, local: local}
}

// --- SessionStore → local ---

func (c *CompositeStore) CreateSession(req CreateSessionRequest) (*Session, error) {
	return c.local.CreateSession(req)
}

func (c *CompositeStore) GetOrCreateScratchSession(projectID string) (*Session, error) {
	return c.local.GetOrCreateScratchSession(projectID)
}

func (c *CompositeStore) UnhideSession(id string) (bool, error) {
	return c.local.UnhideSession(id)
}

func (c *CompositeStore) ListSessions() ([]Session, error) {
	return c.local.ListSessions()
}

func (c *CompositeStore) GetSession(id string) (*Session, error) {
	return c.local.GetSession(id)
}

func (c *CompositeStore) UpdateSession(id string, req UpdateSessionRequest) (*Session, error) {
	return c.local.UpdateSession(id, req)
}

func (c *CompositeStore) UpdateSessionAgentID(id, agentSessionID string) error {
	return c.local.UpdateSessionAgentID(id, agentSessionID)
}

func (c *CompositeStore) UpdateSessionStreamID(id, streamID string) error {
	return c.local.UpdateSessionStreamID(id, streamID)
}

func (c *CompositeStore) UpdateSessionFeatures(id, features string) error {
	return c.local.UpdateSessionFeatures(id, features)
}

func (c *CompositeStore) UpdateSessionWorktree(id, mode, path string) error {
	return c.local.UpdateSessionWorktree(id, mode, path)
}

func (c *CompositeStore) ForkSession(parentID, name, atMessageID, forkedAtAgentMsgUUID string) (*Session, error) {
	return c.local.ForkSession(parentID, name, atMessageID, forkedAtAgentMsgUUID)
}

func (c *CompositeStore) GetExpiredSessionWorktrees(olderThan time.Time) ([]string, error) {
	return c.local.GetExpiredSessionWorktrees(olderThan)
}

func (c *CompositeStore) ListTrashedSessions() ([]Session, error) {
	return c.local.ListTrashedSessions()
}

func (c *CompositeStore) DeleteSession(id string) error {
	return c.local.DeleteSession(id)
}

func (c *CompositeStore) RestoreSession(id string) error {
	return c.local.RestoreSession(id)
}

func (c *CompositeStore) PurgeExpiredSessions(olderThan time.Time) ([]string, error) {
	return c.local.PurgeExpiredSessions(olderThan)
}

func (c *CompositeStore) Vacuum() error {
	return c.local.Vacuum()
}

func (c *CompositeStore) ReorderSessions(sessionIDs []string) error {
	return c.local.ReorderSessions(sessionIDs)
}

// --- MessageStore → local ---

func (c *CompositeStore) CreateMessage(sessionID, role string, parts []MessagePart, metadata MessageMetadata) (*ChatMessage, error) {
	return c.local.CreateMessage(sessionID, role, parts, metadata)
}

func (c *CompositeStore) UpdateMessageParts(messageID string, parts []MessagePart, metadata MessageMetadata) error {
	return c.local.UpdateMessageParts(messageID, parts, metadata)
}

func (c *CompositeStore) GetMessage(id string) (*ChatMessage, error) {
	return c.local.GetMessage(id)
}

func (c *CompositeStore) GetMessageBySDKUUID(sessionID, sdkUUID string) (*ChatMessage, error) {
	return c.local.GetMessageBySDKUUID(sessionID, sdkUUID)
}

func (c *CompositeStore) ListMessages(sessionID string) ([]ChatMessage, error) {
	return c.local.ListMessages(sessionID)
}

func (c *CompositeStore) ListMessagesPaginated(sessionID string, limit int, beforeID string) ([]ChatMessage, bool, error) {
	return c.local.ListMessagesPaginated(sessionID, limit, beforeID)
}

func (c *CompositeStore) DeleteMessagesBySession(sessionID string) error {
	return c.local.DeleteMessagesBySession(sessionID)
}

func (c *CompositeStore) CreateUserMessage(sessionID, content string) (*ChatMessage, error) {
	return c.local.CreateUserMessage(sessionID, content)
}

// --- QueueStore → local ---

func (c *CompositeStore) ListQueuedMessages(sessionID string) ([]QueuedMessage, error) {
	return c.local.ListQueuedMessages(sessionID)
}

func (c *CompositeStore) CreateQueuedMessage(sessionID string, req CreateQueuedMessageRequest) (*QueuedMessage, error) {
	return c.local.CreateQueuedMessage(sessionID, req)
}

func (c *CompositeStore) UpdateQueuedMessage(sessionID, id, text string) error {
	return c.local.UpdateQueuedMessage(sessionID, id, text)
}

func (c *CompositeStore) DeleteQueuedMessage(sessionID, id string) error {
	return c.local.DeleteQueuedMessage(sessionID, id)
}

func (c *CompositeStore) PopQueuedMessage(sessionID string) (*QueuedMessage, error) {
	return c.local.PopQueuedMessage(sessionID)
}

func (c *CompositeStore) PopQueuedMessageByID(sessionID, id string) (*QueuedMessage, error) {
	return c.local.PopQueuedMessageByID(sessionID, id)
}

func (c *CompositeStore) SessionsWithQueuedMessages() ([]string, error) {
	return c.local.SessionsWithQueuedMessages()
}

// --- WorkflowStore: definitions → primary (remote in cloud mode), executions/logs → local ---
// Schedule is always stored locally (workflow_schedules table) and never synced to remote.
// This is intentional: a schedule like "run at 3am daily" is a per-machine execution config,
// not part of the shared workflow definition.

// isCloud returns true when the primary store is a RemoteStore (cloud mode).
func (c *CompositeStore) isCloud() bool {
	_, ok := c.Store.(*RemoteStore)
	return ok
}

// mergeLocalSchedule overlays the locally-stored schedule onto a workflow returned from remote.
func (c *CompositeStore) mergeLocalSchedule(wf *Workflow) {
	if wf != nil {
		wf.Schedule = c.local.GetLocalSchedule(wf.ID)
	}
}

// mergeLocalSchedules overlays locally-stored schedules onto a slice of remote workflows.
func (c *CompositeStore) mergeLocalSchedules(workflows []Workflow) {
	schedules := c.local.GetAllLocalSchedules()
	for i := range workflows {
		if s, ok := schedules[workflows[i].ID]; ok {
			workflows[i].Schedule = s
		}
	}
}

// mergeLocalWorktreeMode overlays the locally-stored worktree_mode. Always
// applied (in both local and cloud mode) because worktree_mode is per-machine
// and the workflows-table column is treated as legacy/unused.
func (c *CompositeStore) mergeLocalWorktreeMode(wf *Workflow) {
	if wf == nil {
		return
	}
	if m := c.local.GetLocalWorktreeMode(wf.ID); m != "" {
		wf.WorktreeMode = m
	} else if wf.WorktreeMode == "" {
		wf.WorktreeMode = "local"
	}
}

func (c *CompositeStore) mergeLocalWorktreeModes(workflows []Workflow) {
	modes := c.local.GetAllLocalWorktreeModes()
	for i := range workflows {
		if m, ok := modes[workflows[i].ID]; ok && m != "" {
			workflows[i].WorktreeMode = m
		} else if workflows[i].WorktreeMode == "" {
			workflows[i].WorktreeMode = "local"
		}
	}
}

func (c *CompositeStore) CreateWorkflow(projectID, name, description, createdBy string, definition []byte) (*Workflow, error) {
	return c.Store.CreateWorkflow(projectID, name, description, createdBy, definition)
}

func (c *CompositeStore) GetWorkflow(id string) (*Workflow, error) {
	wf, err := c.Store.GetWorkflow(id)
	if err == nil {
		c.mergeLocalWorktreeMode(wf)
		if c.isCloud() {
			c.mergeLocalSchedule(wf)
		}
	}
	return wf, err
}

func (c *CompositeStore) ListWorkflows(projectID string) ([]Workflow, error) {
	workflows, err := c.Store.ListWorkflows(projectID)
	if err == nil {
		c.mergeLocalWorktreeModes(workflows)
		if c.isCloud() {
			c.mergeLocalSchedules(workflows)
		}
	}
	return workflows, err
}

func (c *CompositeStore) UpdateWorkflow(id string, req UpdateWorkflowRequest) (*Workflow, error) {
	scheduleOnly := req.Schedule != nil
	worktreeOnly := req.WorktreeMode != nil

	if req.Schedule != nil {
		// Persist schedule locally; never send to remote.
		if err := c.local.SetLocalSchedule(id, *req.Schedule); err != nil {
			return nil, err
		}
		req.Schedule = nil
	}

	if req.WorktreeMode != nil {
		// Persist worktree_mode locally; never send to remote (worktree is a
		// per-machine concept). Validation matches SQLiteStore.UpdateWorkflow.
		mode := *req.WorktreeMode
		if mode != "local" && mode != "worktree" {
			return nil, NewValidationError("worktree_mode must be 'local' or 'worktree'")
		}
		if err := c.local.SetLocalWorktreeMode(id, mode); err != nil {
			return nil, err
		}
		req.WorktreeMode = nil
	}

	// If the update was purely local-overlay fields and nothing else, skip
	// the remote round-trip entirely.
	if (scheduleOnly || worktreeOnly) &&
		req.Name == nil && req.Description == nil && req.Definition == nil &&
		req.Status == nil && req.ExpectedVersion == nil {
		wf, err := c.Store.GetWorkflow(id)
		if err == nil {
			c.mergeLocalWorktreeMode(wf)
			if c.isCloud() {
				c.mergeLocalSchedule(wf)
			}
		}
		return wf, err
	}

	wf, err := c.Store.UpdateWorkflow(id, req)
	if err == nil {
		c.mergeLocalWorktreeMode(wf)
		if c.isCloud() {
			c.mergeLocalSchedule(wf)
		}
	}
	return wf, err
}

func (c *CompositeStore) DeleteWorkflow(id string) error {
	return c.Store.DeleteWorkflow(id)
}

func (c *CompositeStore) CreateExecution(req CreateExecutionRequest) (*WorkflowExecution, error) {
	return c.local.CreateExecution(req)
}

func (c *CompositeStore) GetExecution(id string) (*WorkflowExecution, error) {
	return c.local.GetExecution(id)
}

func (c *CompositeStore) ListExecutions(workflowID string, limit int, offset int) ([]WorkflowExecution, error) {
	return c.local.ListExecutions(workflowID, limit, offset)
}

func (c *CompositeStore) UpdateExecutionStatus(id string, status string, errorMsg string, durationMs *int64) error {
	return c.local.UpdateExecutionStatus(id, status, errorMsg, durationMs)
}

func (c *CompositeStore) UpdateExecutionWorktreePath(id, path string) error {
	return c.local.UpdateExecutionWorktreePath(id, path)
}

func (c *CompositeStore) CreateNodeLog(log *WorkflowNodeLog) error {
	return c.local.CreateNodeLog(log)
}

func (c *CompositeStore) GetNodeLogs(executionID string) ([]WorkflowNodeLog, error) {
	return c.local.GetNodeLogs(executionID)
}

func (c *CompositeStore) UpdateNodeLog(id string, status string, input string, output string, errorMsg string, durationMs int64) error {
	return c.local.UpdateNodeLog(id, status, input, output, errorMsg, durationMs)
}

func (c *CompositeStore) SetNodeHookInfo(id string, retries int, result string) error {
	return c.local.SetNodeHookInfo(id, retries, result)
}

func (c *CompositeStore) FailStaleNodeLogs(executionID, status string) error {
	return c.local.FailStaleNodeLogs(executionID, status)
}

func (c *CompositeStore) ListAllWorkflows() ([]Workflow, error) {
	// Always query the primary store: in local mode c.Store == c.local;
	// in cloud mode c.Store is the RemoteStore which fetches all projects' workflows.
	workflows, err := c.Store.ListAllWorkflows()
	if err == nil {
		c.mergeLocalWorktreeModes(workflows)
		if c.isCloud() {
			c.mergeLocalSchedules(workflows)
		}
	}
	return workflows, err
}

func (c *CompositeStore) ResetStaleExecutions() error {
	return c.local.ResetStaleExecutions()
}
