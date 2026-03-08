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

func (c *CompositeStore) TrimSessionMessages(sessionID string, keepCount int) (int64, error) {
	return c.local.TrimSessionMessages(sessionID, keepCount)
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

func (c *CompositeStore) CreateWorkflow(projectID, name, description, createdBy string, definition []byte) (*Workflow, error) {
	return c.Store.CreateWorkflow(projectID, name, description, createdBy, definition)
}

func (c *CompositeStore) GetWorkflow(id string) (*Workflow, error) {
	wf, err := c.Store.GetWorkflow(id)
	if err == nil && c.isCloud() {
		c.mergeLocalSchedule(wf)
	}
	return wf, err
}

func (c *CompositeStore) ListWorkflows(projectID string) ([]Workflow, error) {
	workflows, err := c.Store.ListWorkflows(projectID)
	if err == nil && c.isCloud() {
		c.mergeLocalSchedules(workflows)
	}
	return workflows, err
}

func (c *CompositeStore) UpdateWorkflow(id string, req UpdateWorkflowRequest) (*Workflow, error) {
	if req.Schedule != nil {
		// Persist schedule locally; never send to remote.
		if err := c.local.SetLocalSchedule(id, *req.Schedule); err != nil {
			return nil, err
		}
		req.Schedule = nil
		// If nothing else changed, skip the remote round-trip entirely.
		if req.Name == nil && req.Description == nil && req.Definition == nil && req.Status == nil && req.ExpectedVersion == nil {
			wf, err := c.Store.GetWorkflow(id)
			if err == nil && c.isCloud() {
				c.mergeLocalSchedule(wf)
			}
			return wf, err
		}
	}
	wf, err := c.Store.UpdateWorkflow(id, req)
	if err == nil && c.isCloud() {
		c.mergeLocalSchedule(wf)
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
	if err == nil && c.isCloud() {
		c.mergeLocalSchedules(workflows)
	}
	return workflows, err
}

func (c *CompositeStore) ResetStaleExecutions() error {
	return c.local.ResetStaleExecutions()
}
