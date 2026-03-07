package store

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// CreateWorkflow creates a new workflow
func (s *SQLiteStore) CreateWorkflow(projectID, name, description, createdBy string, definition []byte) (*Workflow, error) {
	id := uuid.New().String()
	now := time.Now()

	_, err := s.db.Exec(`
		INSERT INTO workflows (id, namespace_id, project_id, name, description, definition, version, status, created_by, created_at, updated_at)
		VALUES (?, 'local', ?, ?, ?, ?, 1, 'draft', ?, ?, ?)
	`, id, projectID, name, description, string(definition), createdBy, now, now)
	if err != nil {
		return nil, err
	}

	return s.GetWorkflow(id)
}

// GetWorkflow retrieves a workflow by ID
func (s *SQLiteStore) GetWorkflow(id string) (*Workflow, error) {
	var w Workflow
	var defStr string

	err := s.db.QueryRow(`
		SELECT id, project_id, name, description, definition, version, status, created_by, created_at, updated_at
		FROM workflows
		WHERE id = ?
	`, id).Scan(&w.ID, &w.ProjectID, &w.Name, &w.Description, &defStr, &w.Version, &w.Status, &w.CreatedBy, &w.CreatedAt, &w.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	w.Definition = []byte(defStr)
	return &w, nil
}

// ListWorkflows lists workflows for a project
func (s *SQLiteStore) ListWorkflows(projectID string) ([]Workflow, error) {
	rows, err := s.db.Query(`
		SELECT id, project_id, name, description, definition, version, status, created_by, created_at, updated_at
		FROM workflows
		WHERE project_id = ?
		ORDER BY updated_at DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	workflows := make([]Workflow, 0)
	for rows.Next() {
		var w Workflow
		var defStr string
		if err := rows.Scan(&w.ID, &w.ProjectID, &w.Name, &w.Description, &defStr, &w.Version, &w.Status, &w.CreatedBy, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, err
		}
		w.Definition = []byte(defStr)
		workflows = append(workflows, w)
	}

	return workflows, rows.Err()
}

// ListAllWorkflows returns all workflows across all projects (used by the scheduler).
func (s *SQLiteStore) ListAllWorkflows() ([]Workflow, error) {
	// Call GetAllLocalSchedules BEFORE opening rows to avoid self-deadlock.
	// With SetMaxOpenConns(1), opening rows holds the single SQLite connection;
	// calling GetAllLocalSchedules() while rows are open would block forever
	// trying to acquire the same connection.
	schedules := s.GetAllLocalSchedules()

	rows, err := s.db.Query(`
		SELECT id, project_id, name, description, definition, version, status, created_by, created_at, updated_at
		FROM workflows
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	workflows := make([]Workflow, 0)
	for rows.Next() {
		var w Workflow
		var defStr string
		if err := rows.Scan(&w.ID, &w.ProjectID, &w.Name, &w.Description, &defStr, &w.Version, &w.Status, &w.CreatedBy, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, err
		}
		w.Definition = []byte(defStr)
		w.Schedule = schedules[w.ID]
		workflows = append(workflows, w)
	}

	return workflows, rows.Err()
}

// UpdateWorkflow updates a workflow
func (s *SQLiteStore) UpdateWorkflow(id string, req UpdateWorkflowRequest) (*Workflow, error) {
	// Get current workflow for version check
	current, err := s.GetWorkflow(id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, sql.ErrNoRows
	}

	// Version conflict check
	if req.ExpectedVersion != nil && *req.ExpectedVersion != current.Version {
		return nil, ErrVersionConflict
	}

	// Persist schedule separately; never stored in workflows table.
	if req.Schedule != nil {
		if err := s.SetLocalSchedule(id, *req.Schedule); err != nil {
			return nil, err
		}
		req.Schedule = nil
	}

	// Build update query
	query := `UPDATE workflows SET updated_at = ?, version = version + 1`
	args := []interface{}{time.Now()}

	if req.Name != nil {
		query += `, name = ?`
		args = append(args, *req.Name)
	}
	if req.Description != nil {
		query += `, description = ?`
		args = append(args, *req.Description)
	}
	if req.Definition != nil {
		query += `, definition = ?`
		args = append(args, string(*req.Definition))
	}
	if req.Status != nil {
		query += `, status = ?`
		args = append(args, *req.Status)
	}
	query += ` WHERE id = ?`
	args = append(args, id)

	if _, err := s.db.Exec(query, args...); err != nil {
		return nil, err
	}

	return s.GetWorkflow(id)
}

// SetLocalSchedule upserts a schedule string for a workflow in the local workflow_schedules table.
// Used in cloud mode to persist schedule locally without syncing to remote.
func (s *SQLiteStore) SetLocalSchedule(id, schedule string) error {
	_, err := s.db.Exec(`
		INSERT INTO workflow_schedules (workflow_id, schedule) VALUES (?, ?)
		ON CONFLICT(workflow_id) DO UPDATE SET schedule = excluded.schedule
	`, id, schedule)
	return err
}

// GetLocalSchedule returns the locally-stored schedule for a workflow, or "" if none.
func (s *SQLiteStore) GetLocalSchedule(id string) string {
	var schedule string
	_ = s.db.QueryRow(`SELECT schedule FROM workflow_schedules WHERE workflow_id = ?`, id).Scan(&schedule)
	return schedule
}

// GetAllLocalSchedules returns a map of workflow_id → schedule for all workflows
// that have a non-empty local schedule. Used to overlay schedules on remote workflows.
func (s *SQLiteStore) GetAllLocalSchedules() map[string]string {
	rows, err := s.db.Query(`SELECT workflow_id, schedule FROM workflow_schedules WHERE schedule != ''`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var wfID, schedule string
		if err := rows.Scan(&wfID, &schedule); err == nil {
			result[wfID] = schedule
		}
	}
	return result
}

// ResetStaleExecutions marks any running or pending executions as failed.
// Called on startup to clean up executions interrupted by a previous crash/restart.
func (s *SQLiteStore) ResetStaleExecutions() error {
	now := time.Now()
	// Reset stale node logs first (before executions, so we can join on execution status).
	if _, err := s.db.Exec(`
		UPDATE wf_node_logs
		SET status = 'failed', error = 'Interrupted: server restarted', completed_at = ?
		WHERE status IN ('running', 'waiting')
		  AND execution_id IN (
		      SELECT id FROM wf_executions WHERE status IN ('running', 'pending')
		  )
	`, now); err != nil {
		return err
	}
	_, err := s.db.Exec(`
		UPDATE wf_executions
		SET status = 'failed', error = 'Interrupted: server restarted', completed_at = ?
		WHERE status IN ('running', 'pending')
	`, now)
	return err
}

// DeleteWorkflow deletes a workflow
func (s *SQLiteStore) DeleteWorkflow(id string) error {
	_, err := s.db.Exec(`DELETE FROM workflows WHERE id = ?`, id)
	return err
}

// CreateExecution creates a new workflow execution record.
func (s *SQLiteStore) CreateExecution(req CreateExecutionRequest) (*WorkflowExecution, error) {
	id := uuid.New().String()
	now := time.Now()

	_, err := s.db.Exec(`
		INSERT INTO wf_executions
			(id, workflow_id, workflow_version, definition_snapshot, status, triggered_by, user_context, created_at)
		VALUES (?, ?, ?, ?, 'pending', ?, ?, ?)
	`, id, req.WorkflowID, req.WorkflowVersion, req.DefinitionSnapshot, req.TriggeredBy, req.UserContext, now)
	if err != nil {
		return nil, err
	}

	return s.GetExecution(id)
}

// GetExecution retrieves an execution by ID.
func (s *SQLiteStore) GetExecution(id string) (*WorkflowExecution, error) {
	var e WorkflowExecution

	err := s.db.QueryRow(`
		SELECT id, workflow_id, workflow_version, definition_snapshot, status, triggered_by, user_context,
		       error, started_at, completed_at, duration_ms, created_at
		FROM wf_executions
		WHERE id = ?
	`, id).Scan(
		&e.ID, &e.WorkflowID, &e.WorkflowVersion, &e.DefinitionSnapshot,
		&e.Status, &e.TriggeredBy, &e.UserContext,
		&e.Error, &e.StartedAt, &e.CompletedAt, &e.DurationMs, &e.CreatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return &e, nil
}

// ListExecutions lists executions for a workflow, newest first.
func (s *SQLiteStore) ListExecutions(workflowID string, limit int, offset int) ([]WorkflowExecution, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := s.db.Query(`
		SELECT id, workflow_id, workflow_version, definition_snapshot, status, triggered_by, user_context,
		       error, started_at, completed_at, duration_ms, created_at
		FROM wf_executions
		WHERE workflow_id = ?
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?
	`, workflowID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	executions := make([]WorkflowExecution, 0)
	for rows.Next() {
		var e WorkflowExecution
		if err := rows.Scan(
			&e.ID, &e.WorkflowID, &e.WorkflowVersion, &e.DefinitionSnapshot,
			&e.Status, &e.TriggeredBy, &e.UserContext,
			&e.Error, &e.StartedAt, &e.CompletedAt, &e.DurationMs, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		executions = append(executions, e)
	}

	return executions, rows.Err()
}

// UpdateExecutionStatus transitions execution status.
// For "running": sets started_at.
// For terminal states (completed/failed/cancelled): sets completed_at, duration_ms, error.
func (s *SQLiteStore) UpdateExecutionStatus(id string, status string, errorMsg string, durationMs *int64) error {
	now := time.Now()

	if status == "running" {
		_, err := s.db.Exec(`
			UPDATE wf_executions SET status = ?, started_at = ? WHERE id = ?
		`, status, now, id)
		return err
	}

	_, err := s.db.Exec(`
		UPDATE wf_executions SET status = ?, error = ?, completed_at = ?, duration_ms = ? WHERE id = ?
	`, status, errorMsg, now, durationMs, id)
	return err
}

// CreateNodeLog inserts a node execution log entry.
func (s *SQLiteStore) CreateNodeLog(log *WorkflowNodeLog) error {
	if log.ID == "" {
		log.ID = uuid.New().String()
	}
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now()
	}

	_, err := s.db.Exec(`
		INSERT INTO wf_node_logs
			(id, execution_id, node_id, node_type, node_label, level, status, input, output, error,
			 started_at, completed_at, duration_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, log.ID, log.ExecutionID, log.NodeID, log.NodeType, log.NodeLabel, log.Level,
		log.Status, log.Input, log.Output, log.Error,
		log.StartedAt, log.CompletedAt, log.DurationMs, log.CreatedAt)

	return err
}

// GetNodeLogs retrieves all node logs for an execution, ordered by level then creation time.
func (s *SQLiteStore) GetNodeLogs(executionID string) ([]WorkflowNodeLog, error) {
	rows, err := s.db.Query(`
		SELECT id, execution_id, node_id, node_type, node_label, level, status, input, output, error,
		       hook_retries, hook_result, started_at, completed_at, duration_ms, created_at
		FROM wf_node_logs
		WHERE execution_id = ?
		ORDER BY level ASC, created_at ASC
	`, executionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	logs := make([]WorkflowNodeLog, 0)
	for rows.Next() {
		var l WorkflowNodeLog
		if err := rows.Scan(
			&l.ID, &l.ExecutionID, &l.NodeID, &l.NodeType, &l.NodeLabel, &l.Level,
			&l.Status, &l.Input, &l.Output, &l.Error,
			&l.HookRetries, &l.HookResult,
			&l.StartedAt, &l.CompletedAt, &l.DurationMs, &l.CreatedAt,
		); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}

	return logs, rows.Err()
}

// SetNodeHookInfo records hook execution results on a node log.
func (s *SQLiteStore) SetNodeHookInfo(id string, retries int, result string) error {
	_, err := s.db.Exec(`
		UPDATE wf_node_logs SET hook_retries = ?, hook_result = ? WHERE id = ?
	`, retries, result, id)
	return err
}

// FailStaleNodeLogs updates any node logs still in "running" state for the given
// execution to the provided terminal status. This is a safety net called by
// finishExecution to ensure no node is left spinning after the execution ends.
func (s *SQLiteStore) FailStaleNodeLogs(executionID, status string) error {
	_, err := s.db.Exec(`
		UPDATE wf_node_logs SET status = ?, completed_at = ?
		WHERE execution_id = ? AND status = 'running'
	`, status, time.Now(), executionID)
	return err
}

// UpdateNodeLog updates status, input (for human nodes), output, error, and completion time.
func (s *SQLiteStore) UpdateNodeLog(id string, status string, input string, output string, errorMsg string, durationMs int64) error {
	now := time.Now()

	if status == "waiting" {
		// Human node waiting: record the transition time but leave completion fields empty.
		_, err := s.db.Exec(`
			UPDATE wf_node_logs SET status = ? WHERE id = ?
		`, status, id)
		return err
	}

	_, err := s.db.Exec(`
		UPDATE wf_node_logs
		SET status = ?, input = ?, output = ?, error = ?, completed_at = ?, duration_ms = ?
		WHERE id = ?
	`, status, input, output, errorMsg, now, durationMs, id)
	return err
}
