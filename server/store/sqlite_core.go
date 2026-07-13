package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Projects
// ---------------------------------------------------------------------------

const projectColumns = `id, owner_user_id, namespace_id, name, description, git_repo_path, gitlab_repo_url, gitlab_token, extra, created_by, version, created_at, updated_at, auto_managed_dir`

func scanProject(row interface{ Scan(...interface{}) error }) (*Project, error) {
	var p Project
	var ignoredGitLabURL, ignoredToken, ignoredExtra string
	var autoManaged int
	err := row.Scan(&p.ID, &p.OwnerUserID, &p.NamespaceID, &p.Name, &p.Description, &p.GitRepoPath, &ignoredGitLabURL, &ignoredToken, &ignoredExtra, &p.CreatedBy, &p.Version, &p.CreatedAt, &p.UpdatedAt, &autoManaged)
	if err != nil {
		return nil, err
	}
	p.AutoManagedDir = autoManaged != 0
	return &p, nil
}

func (s *SQLiteStore) CreateProject(req CreateProjectRequest) (*Project, error) {
	now := time.Now()
	createdBy := req.CreatedBy
	if createdBy == "" {
		createdBy = "local"
	}
	p := &Project{
		ID:             uuid.New().String(),
		OwnerUserID:    req.OwnerUserID,
		NamespaceID:    "local",
		Name:           req.Name,
		Description:    req.Description,
		GitRepoPath:    req.GitRepoPath,
		AutoManagedDir: req.AutoManagedDir,
		CreatedBy:      createdBy,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	autoManagedInt := 0
	if p.AutoManagedDir {
		autoManagedInt = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO projects (id, owner_user_id, namespace_id, name, description, git_repo_path, created_by, version, created_at, updated_at, auto_managed_dir)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.OwnerUserID, p.NamespaceID, p.Name, p.Description, p.GitRepoPath, p.CreatedBy, p.Version, p.CreatedAt, p.UpdatedAt, autoManagedInt,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrVersionConflict
		}
		return nil, fmt.Errorf("insert project: %w", err)
	}
	return p, nil
}

func (s *SQLiteStore) ListProjects() ([]Project, error) {
	rows, err := s.db.Query(`SELECT ` + projectColumns + ` FROM projects ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query projects: %w", err)
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	return projects, nil
}

// ListProjectsByOwner returns projects owned by the given user.
func (s *SQLiteStore) ListProjectsByOwner(ownerUserID int64) ([]Project, error) {
	rows, err := s.db.Query(`SELECT `+projectColumns+` FROM projects WHERE owner_user_id = ? ORDER BY updated_at DESC`, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("query projects by owner: %w", err)
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects by owner: %w", err)
	}
	return projects, nil
}

// GetProjectByNameForOwner returns the project named name owned by ownerUserID,
// or (nil, nil) when there is no match. Used by the create handler to enforce
// per-user uniqueness before attempting the insert.
func (s *SQLiteStore) GetProjectByNameForOwner(ownerUserID int64, name string) (*Project, error) {
	p, err := scanProject(s.db.QueryRow(
		`SELECT `+projectColumns+` FROM projects WHERE owner_user_id = ? AND name = ? LIMIT 1`,
		ownerUserID, name,
	))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get project by owner+name: %w", err)
	}
	return p, nil
}

func (s *SQLiteStore) GetProject(id string) (*Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT `+projectColumns+` FROM projects WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}

func (s *SQLiteStore) UpdateProject(id string, req UpdateProjectRequest) (*Project, error) {
	p, err := s.GetProject(id)
	if err != nil || p == nil {
		return nil, err
	}

	if req.Name != nil {
		p.Name = *req.Name
	}
	if req.Description != nil {
		p.Description = *req.Description
	}
	if req.GitRepoPath != nil {
		p.GitRepoPath = *req.GitRepoPath
	}
	newVersion := p.Version + 1
	p.UpdatedAt = time.Now()

	query := `UPDATE projects SET name=?, description=?, git_repo_path=?, version=?, updated_at=? WHERE id=?`
	args := []interface{}{p.Name, p.Description, p.GitRepoPath, newVersion, p.UpdatedAt, p.ID}

	if req.ExpectedVersion != nil {
		query += ` AND version=?`
		args = append(args, *req.ExpectedVersion)
	}

	res, err := s.db.Exec(query, args...)
	if err != nil {
		return nil, fmt.Errorf("update project: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 && req.ExpectedVersion != nil {
		return nil, ErrVersionConflict
	}
	p.Version = newVersion
	return p, nil
}

func (s *SQLiteStore) GetProjectsByIDs(ids []string) ([]Project, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := `SELECT ` + projectColumns + ` FROM projects WHERE id IN (` + strings.Join(placeholders, ",") + `) ORDER BY updated_at DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query projects by ids: %w", err)
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects by ids: %w", err)
	}
	return projects, nil
}

func (s *SQLiteStore) DeleteProject(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Cascade-delete dependent rows to avoid orphans.
	cascadeQueries := []struct{ sql, label string }{
		{`DELETE FROM review_comments WHERE asset_id IN (SELECT id FROM assets WHERE project_id = ?)`, "review_comments"},
		{`DELETE FROM asset_versions WHERE asset_id IN (SELECT id FROM assets WHERE project_id = ?)`, "asset_versions"},
		{`DELETE FROM task_comments WHERE task_id IN (SELECT id FROM tasks WHERE project_id = ?)`, "task_comments"},
		{`DELETE FROM assets WHERE project_id = ?`, "assets"},
		{`DELETE FROM tasks WHERE project_id = ?`, "tasks"},
		{`DELETE FROM wf_node_logs WHERE execution_id IN (SELECT id FROM wf_executions WHERE workflow_id IN (SELECT id FROM workflows WHERE project_id = ?))`, "wf_node_logs"},
		{`DELETE FROM wf_executions WHERE workflow_id IN (SELECT id FROM workflows WHERE project_id = ?)`, "wf_executions"},
		{`DELETE FROM workflow_schedules WHERE workflow_id IN (SELECT id FROM workflows WHERE project_id = ?)`, "workflow_schedules"},
		{`DELETE FROM workflows WHERE project_id = ?`, "workflows"},
		{`DELETE FROM sessions WHERE project_id = ?`, "sessions"},
		{`DELETE FROM web_shares WHERE project_id = ?`, "web_shares"},
	}
	for _, cq := range cascadeQueries {
		if _, err := tx.Exec(cq.sql, id); err != nil {
			return fmt.Errorf("delete project %s: %w", cq.label, err)
		}
	}

	result, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func (s *SQLiteStore) CreateSession(req CreateSessionRequest) (*Session, error) {
	now := time.Now()
	sess := &Session{
		ID:          uuid.New().String(),
		NamespaceID: "local",
		Name:        req.Name,
		ProjectID:   req.ProjectID,
		Agent:       req.Agent,
		Model:       req.Model,
		Effort:      req.Effort,
		CreatedBy:   "local",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	_, err := s.db.Exec(
		`INSERT INTO sessions (id, namespace_id, name, project_id, agent, model, effort, agent_session_id, stream_id, features, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '', '', '', ?, ?, ?)`,
		sess.ID, sess.NamespaceID, sess.Name, sess.ProjectID, sess.Agent, sess.Model, sess.Effort, sess.CreatedBy, sess.CreatedAt, sess.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	return sess, nil
}

// GetOrCreateScratchSession returns the existing hidden ("scratch") session for
// the given project, or creates one if none exists. There is at most one live
// scratch session per project (enforced by a partial unique index), so the
// session acts as a stable per-project input buffer: the user fills in
// agent/model/worktree/draft, and the session is promoted to a visible one as
// soon as a user message is persisted to it.
func (s *SQLiteStore) GetOrCreateScratchSession(projectID string) (*Session, error) {
	if projectID == "" {
		return nil, fmt.Errorf("GetOrCreateScratchSession: projectID required")
	}

	// Fast path: existing scratch session.
	row := s.db.QueryRow(
		`SELECT `+sessionColumns+` FROM sessions WHERE project_id = ? AND is_hidden = 1 AND deleted_at IS NULL LIMIT 1`,
		projectID,
	)
	if sess, err := scanSession(row); err == nil {
		return sess, nil
	} else if err != sql.ErrNoRows {
		return nil, fmt.Errorf("query scratch session: %w", err)
	}

	// Create one. The partial unique index on (project_id) WHERE is_hidden=1
	// guarantees no duplicates under concurrent callers; if a racing insert
	// wins, our INSERT fails and we re-read.
	now := time.Now()
	sess := &Session{
		ID:          uuid.New().String(),
		NamespaceID: "local",
		// "New Session" matches the placeholder-name list checked by the
		// frontend autoRenameSession helper, so the LLM-generated title
		// kicks in on first send just like for sessions created via the
		// "+" button.
		Name:      "New Session",
		ProjectID: projectID,
		CreatedBy: "local",
		IsHidden:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, namespace_id, name, project_id, agent, model, effort, agent_session_id, stream_id, features, is_hidden, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, '', '', '', '', '', '', 1, ?, ?, ?)`,
		sess.ID, sess.NamespaceID, sess.Name, sess.ProjectID, sess.CreatedBy, sess.CreatedAt, sess.UpdatedAt,
	)
	if err != nil {
		// Race: another caller created one between our SELECT and INSERT.
		// Re-read and return that.
		row := s.db.QueryRow(
			`SELECT `+sessionColumns+` FROM sessions WHERE project_id = ? AND is_hidden = 1 AND deleted_at IS NULL LIMIT 1`,
			projectID,
		)
		if existing, scanErr := scanSession(row); scanErr == nil {
			return existing, nil
		}
		return nil, fmt.Errorf("insert scratch session: %w", err)
	}
	return sess, nil
}

// UnhideSession flips a hidden ("scratch") session into a visible one. Used
// when a user sends the first message into the scratch session — it just gets
// promoted in place, no copy needed. Returns true iff a row was actually
// flipped (i.e. the session existed and was previously hidden), so the caller
// can avoid spamming change events for the common already-visible case.
func (s *SQLiteStore) UnhideSession(id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	res, err := s.db.Exec(`UPDATE sessions SET is_hidden = 0 WHERE id = ? AND is_hidden = 1`, id)
	if err != nil {
		return false, fmt.Errorf("unhide session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("unhide session rows affected: %w", err)
	}
	return n > 0, nil
}

// sessionColumns is the canonical column list for SELECTing a Session row.
// Keep this in sync with scanSession.
const sessionColumns = `id, namespace_id, name, project_id, agent, model, effort, agent_session_id, stream_id, sort_order, features, worktree_mode, worktree_path, forked_from_session_id, forked_from_message_id, forked_at_agent_checkpoint, is_hidden, created_by, created_at, updated_at, deleted_at`

func scanSession(row interface{ Scan(...interface{}) error }) (*Session, error) {
	var sess Session
	var deletedAt sql.NullTime
	var isHidden int
	if err := row.Scan(&sess.ID, &sess.NamespaceID, &sess.Name, &sess.ProjectID, &sess.Agent,
		&sess.Model, &sess.Effort, &sess.AgentSessionID, &sess.StreamID, &sess.SortOrder, &sess.Features,
		&sess.WorktreeMode, &sess.WorktreePath,
		&sess.ForkedFromSessionID, &sess.ForkedFromMessageID, &sess.ForkedAtAgentCheckpoint,
		&isHidden,
		&sess.CreatedBy, &sess.CreatedAt, &sess.UpdatedAt, &deletedAt); err != nil {
		return nil, err
	}
	if sess.WorktreeMode == "" {
		sess.WorktreeMode = "local"
	}
	sess.IsHidden = isHidden != 0
	if deletedAt.Valid {
		sess.DeletedAt = &deletedAt.Time
	}
	return &sess, nil
}

func (s *SQLiteStore) ListSessions() ([]Session, error) {
	rows, err := s.db.Query(
		`SELECT ` + sessionColumns + ` FROM sessions WHERE deleted_at IS NULL AND is_hidden = 0 ORDER BY sort_order ASC, created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var items []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		items = append(items, *sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}
	return items, nil
}

func (s *SQLiteStore) ListTrashedSessions() ([]Session, error) {
	rows, err := s.db.Query(
		`SELECT ` + sessionColumns + ` FROM sessions WHERE deleted_at IS NOT NULL AND is_hidden = 0 ORDER BY deleted_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query trashed sessions: %w", err)
	}
	defer rows.Close()

	var items []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan trashed session: %w", err)
		}
		items = append(items, *sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate trashed sessions: %w", err)
	}
	return items, nil
}

func (s *SQLiteStore) GetSession(id string) (*Session, error) {
	row := s.db.QueryRow(
		`SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id,
	)
	sess, err := scanSession(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	return sess, nil
}

func (s *SQLiteStore) UpdateSession(id string, req UpdateSessionRequest) (*Session, error) {
	sess, err := s.GetSession(id)
	if err != nil || sess == nil {
		return nil, err
	}

	if req.Name != nil {
		sess.Name = *req.Name
	}
	if req.ProjectID != nil {
		sess.ProjectID = *req.ProjectID
	}
	if req.Agent != nil {
		sess.Agent = *req.Agent
	}
	if req.Model != nil {
		sess.Model = *req.Model
	}
	if req.Effort != nil {
		sess.Effort = *req.Effort
	}
	sess.UpdatedAt = time.Now()

	_, err = s.db.Exec(
		`UPDATE sessions SET name = ?, project_id = ?, agent = ?, model = ?, effort = ?, updated_at = ? WHERE id = ?`,
		sess.Name, sess.ProjectID, sess.Agent, sess.Model, sess.Effort, sess.UpdatedAt, sess.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update session: %w", err)
	}
	return sess, nil
}

// ForkSession creates a new session whose history is a prefix of an existing
// (parent) session ending with the message identified by atMessageID. The new
// session inherits agent/model/effort/project/namespace/worktree_mode/features
// from its parent, has its own ID, and starts with `agent_session_id` empty
// so the next chat turn triggers the backend fork. The new session also
// records its lineage and opaque agent checkpoint.
//
// Messages are copied in chronological order until the row whose id matches
// atMessageID is hit (inclusive), then the iteration stops. Each copy gets a
// fresh Nexbox message id but keeps role/parts/metadata/created_at, so the
// UI renders the forked prefix immediately and the parent is left untouched.
//
// Returns the newly created Session.
func (s *SQLiteStore) ForkSession(parentID, name, atMessageID, checkpoint string) (*Session, error) {
	if parentID == "" || atMessageID == "" {
		return nil, fmt.Errorf("ForkSession: parentID and atMessageID required")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Load parent session.
	var parent Session
	var deletedAt sql.NullTime
	var parentIsHidden int
	err = tx.QueryRow(
		`SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, parentID,
	).Scan(&parent.ID, &parent.NamespaceID, &parent.Name, &parent.ProjectID, &parent.Agent,
		&parent.Model, &parent.Effort, &parent.AgentSessionID, &parent.StreamID, &parent.SortOrder, &parent.Features,
		&parent.WorktreeMode, &parent.WorktreePath,
		&parent.ForkedFromSessionID, &parent.ForkedFromMessageID, &parent.ForkedAtAgentCheckpoint,
		&parentIsHidden,
		&parent.CreatedBy, &parent.CreatedAt, &parent.UpdatedAt, &deletedAt)
	_ = parentIsHidden // forks always produce a visible session, parent's hidden flag is irrelevant
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("parent session not found: %s", parentID)
	}
	if err != nil {
		return nil, fmt.Errorf("load parent session: %w", err)
	}

	// Confirm the cut-point message belongs to the parent.
	var atSessionID string
	err = tx.QueryRow(`SELECT session_id FROM chat_messages WHERE id = ?`, atMessageID).Scan(&atSessionID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("fork-at message not found: %s", atMessageID)
	}
	if err != nil {
		return nil, fmt.Errorf("load fork-at message: %w", err)
	}
	if atSessionID != parentID {
		return nil, fmt.Errorf("fork-at message does not belong to parent session")
	}

	now := time.Now()
	newSess := &Session{
		ID:                      uuid.New().String(),
		NamespaceID:             parent.NamespaceID,
		Name:                    name,
		ProjectID:               parent.ProjectID,
		Agent:                   parent.Agent,
		Model:                   parent.Model,
		Effort:                  parent.Effort,
		Features:                parent.Features, // inherit per-session UI features (e.g. chrome_devtools)
		WorktreeMode:            parent.WorktreeMode,
		ForkedFromSessionID:     parent.ID,
		ForkedFromMessageID:     atMessageID,
		ForkedAtAgentCheckpoint: checkpoint,
		CreatedBy:               parent.CreatedBy,
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if newSess.Name == "" {
		base := parent.Name
		if base == "" {
			base = "Session"
		}
		newSess.Name = base + " (fork)"
	}

	if _, err := tx.Exec(
		`INSERT INTO sessions
			(id, namespace_id, name, project_id, agent, model, effort,
			 agent_session_id, stream_id, sort_order, features,
			 worktree_mode, worktree_path,
			 forked_from_session_id, forked_from_message_id, forked_at_agent_checkpoint,
			 created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '', '', 0, ?, ?, '', ?, ?, ?, ?, ?, ?)`,
		newSess.ID, newSess.NamespaceID, newSess.Name, newSess.ProjectID, newSess.Agent,
		newSess.Model, newSess.Effort,
		newSess.Features,
		newSess.WorktreeMode,
		newSess.ForkedFromSessionID, newSess.ForkedFromMessageID, newSess.ForkedAtAgentCheckpoint,
		newSess.CreatedBy, newSess.CreatedAt, newSess.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("insert forked session: %w", err)
	}

	// Copy messages up to (and including) the cut point. We load the parent's
	// full message list in chronological order and stop once we've copied
	// the message whose id matches atMessageID. We do this in Go (rather
	// than SQL with `created_at <=` + tie-break) because the SQLite driver
	// can round-trip time.Time values with subtly different precision when
	// comparing against bound parameters, which made the inclusive boundary
	// unreliable in tests.
	rows, err := tx.Query(
		`SELECT id, role, parts, metadata, created_at FROM chat_messages
		 WHERE session_id = ?
		 ORDER BY created_at ASC, id ASC`,
		parentID,
	)
	if err != nil {
		return nil, fmt.Errorf("read parent messages: %w", err)
	}
	type rowCopy struct {
		role, parts, metadata string
		createdAt             time.Time
	}
	var toCopy []rowCopy
	foundCut := false
	for rows.Next() {
		var rc rowCopy
		var id string
		if err := rows.Scan(&id, &rc.role, &rc.parts, &rc.metadata, &rc.createdAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan parent message: %w", err)
		}
		toCopy = append(toCopy, rc)
		if id == atMessageID {
			foundCut = true
			break
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate parent messages: %w", err)
	}
	if !foundCut {
		// Belt-and-braces: the cut-message id was validated above to belong
		// to this session, but if a concurrent delete races us we'd otherwise
		// end up copying the entire history. Refuse instead.
		return nil, fmt.Errorf("fork-at message not found in parent's message stream: %s", atMessageID)
	}

	for _, rc := range toCopy {
		if _, err := tx.Exec(
			`INSERT INTO chat_messages (id, session_id, role, parts, metadata, created_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			uuid.New().String(), newSess.ID, rc.role, rc.parts, rc.metadata, rc.createdAt,
		); err != nil {
			return nil, fmt.Errorf("copy message: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit fork tx: %w", err)
	}

	return newSess, nil
}

func (s *SQLiteStore) UpdateSessionAgentID(id, agentSessionID string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET agent_session_id = ?, updated_at = ? WHERE id = ?`,
		agentSessionID, time.Now(), id,
	)
	return err
}

func (s *SQLiteStore) UpdateSessionStreamID(id, streamID string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET stream_id = ?, updated_at = ? WHERE id = ?`,
		streamID, time.Now(), id,
	)
	return err
}

func (s *SQLiteStore) UpdateSessionFeatures(id, features string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET features = ?, updated_at = ? WHERE id = ?`,
		features, time.Now(), id,
	)
	return err
}

func (s *SQLiteStore) UpdateSessionWorktree(id, mode, path string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET worktree_mode = ?, worktree_path = ?, updated_at = ? WHERE id = ?`,
		mode, path, time.Now(), id,
	)
	return err
}

// GetExpiredSessionWorktrees returns worktree_paths for sessions that are about to be purged.
func (s *SQLiteStore) GetExpiredSessionWorktrees(olderThan time.Time) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT worktree_path FROM sessions WHERE deleted_at IS NOT NULL AND deleted_at < ? AND worktree_path != ''`,
		olderThan,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// DeleteSession soft-deletes a session by setting deleted_at.
func (s *SQLiteStore) DeleteSession(id string) error {
	_, err := s.db.Exec(`UPDATE sessions SET deleted_at = ? WHERE id = ?`, time.Now(), id)
	return err
}

// RestoreSession clears deleted_at, moving the session back to active.
func (s *SQLiteStore) RestoreSession(id string) error {
	_, err := s.db.Exec(`UPDATE sessions SET deleted_at = NULL WHERE id = ?`, id)
	return err
}

// PurgeExpiredSessions hard-deletes sessions trashed before olderThan.
// Returns the IDs of the deleted sessions so the caller can clean up on-disk data.
// Uses RETURNING to atomically delete and collect IDs (eliminates TOCTOU race).
// All related rows are removed in the same transaction.
func (s *SQLiteStore) PurgeExpiredSessions(olderThan time.Time) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Atomically delete expired sessions and collect their IDs in one statement.
	rows, err := tx.Query(
		`DELETE FROM sessions WHERE deleted_at IS NOT NULL AND deleted_at < ? RETURNING id`,
		olderThan,
	)
	if err != nil {
		return nil, fmt.Errorf("delete expired sessions: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan purged session id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate purged sessions: %w", err)
	}
	rows.Close()

	if len(ids) == 0 {
		return nil, tx.Commit()
	}

	// Clean up related rows for each purged session.
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM chat_messages WHERE session_id = ?`, id); err != nil {
			return nil, fmt.Errorf("delete messages for session %s: %w", id, err)
		}
		if _, err := tx.Exec(`DELETE FROM queued_messages WHERE session_id = ?`, id); err != nil {
			return nil, fmt.Errorf("delete queued messages for session %s: %w", id, err)
		}
		if _, err := tx.Exec(`DELETE FROM message_part_details WHERE session_id = ?`, id); err != nil {
			return nil, fmt.Errorf("delete message details for session %s: %w", id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit purge tx: %w", err)
	}
	return ids, nil
}

// Vacuum checkpoints the WAL and then rebuilds the database to reclaim disk space.
func (s *SQLiteStore) Vacuum() error {
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("wal_checkpoint: %w", err)
	}
	if _, err := s.db.Exec(`VACUUM`); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ReorderSessions(sessionIDs []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`UPDATE sessions SET sort_order = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for i, id := range sessionIDs {
		if _, err := stmt.Exec(i+1, id); err != nil {
			return fmt.Errorf("update sort_order for %s: %w", id, err)
		}
	}

	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

func (s *SQLiteStore) CreateMessage(sessionID, role string, parts []MessagePart, metadata MessageMetadata) (*ChatMessage, error) {
	partsJSON, err := json.Marshal(parts)
	if err != nil {
		return nil, fmt.Errorf("marshal parts: %w", err)
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}

	m := &ChatMessage{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		Role:      role,
		Parts:     parts,
		Metadata:  metadata,
		CreatedAt: time.Now(),
	}

	_, err = s.db.Exec(
		`INSERT INTO chat_messages (id, session_id, role, parts, metadata, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		m.ID, m.SessionID, m.Role, string(partsJSON), string(metadataJSON), m.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert message: %w", err)
	}

	if _, err := s.db.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, time.Now(), sessionID); err != nil {
		log.Printf("[sqlite] failed to update session updated_at (id=%s): %v", sessionID, err)
	}

	return m, nil
}

// GetMessage loads a single message by ID. Returns (nil, nil) when not found.
func (s *SQLiteStore) GetMessage(id string) (*ChatMessage, error) {
	var m ChatMessage
	var partsJSON, metadataJSON string
	err := s.db.QueryRow(
		`SELECT id, session_id, role, parts, metadata, created_at FROM chat_messages WHERE id = ?`, id,
	).Scan(&m.ID, &m.SessionID, &m.Role, &partsJSON, &metadataJSON, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}
	if partsJSON != "" && partsJSON != "[]" {
		if err := json.Unmarshal([]byte(partsJSON), &m.Parts); err != nil {
			return nil, fmt.Errorf("unmarshal parts: %w", err)
		}
	}
	if metadataJSON != "" && metadataJSON != "{}" {
		if err := json.Unmarshal([]byte(metadataJSON), &m.Metadata); err != nil {
			return nil, fmt.Errorf("unmarshal metadata: %w", err)
		}
	}
	return &m, nil
}

func (s *SQLiteStore) ListMessages(sessionID string) ([]ChatMessage, error) {
	rows, err := s.db.Query(
		`SELECT id, session_id, role, parts, metadata, created_at
		 FROM chat_messages WHERE session_id = ? ORDER BY created_at ASC`, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var items []ChatMessage
	for rows.Next() {
		var m ChatMessage
		var partsJSON, metadataJSON string
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &partsJSON, &metadataJSON, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}

		if partsJSON != "" && partsJSON != "[]" {
			if err := json.Unmarshal([]byte(partsJSON), &m.Parts); err != nil {
				return nil, fmt.Errorf("unmarshal parts: %w", err)
			}
		}

		if metadataJSON != "" && metadataJSON != "{}" {
			if err := json.Unmarshal([]byte(metadataJSON), &m.Metadata); err != nil {
				return nil, fmt.Errorf("unmarshal metadata: %w", err)
			}
		}

		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return items, nil
}

func (s *SQLiteStore) ListMessagesPaginated(sessionID string, limit int, beforeID string) ([]ChatMessage, bool, error) {
	if limit <= 0 {
		limit = 20
	}

	var query string
	var args []interface{}

	if beforeID == "" {
		// Initial load: get the most recent `limit` messages
		query = `SELECT id, session_id, role, parts, metadata, created_at
		         FROM chat_messages
		         WHERE session_id = ?
		         ORDER BY created_at DESC
		         LIMIT ?`
		args = []interface{}{sessionID, limit + 1}
	} else {
		// Load older messages: get `limit` messages before the specified message
		query = `SELECT id, session_id, role, parts, metadata, created_at
		         FROM chat_messages
		         WHERE session_id = ?
		         AND created_at < (SELECT created_at FROM chat_messages WHERE id = ?)
		         ORDER BY created_at DESC
		         LIMIT ?`
		args = []interface{}{sessionID, beforeID, limit + 1}
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var items []ChatMessage
	for rows.Next() {
		var m ChatMessage
		var partsJSON, metadataJSON string
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &partsJSON, &metadataJSON, &m.CreatedAt); err != nil {
			return nil, false, fmt.Errorf("scan message: %w", err)
		}

		if partsJSON != "" && partsJSON != "[]" {
			if err := json.Unmarshal([]byte(partsJSON), &m.Parts); err != nil {
				return nil, false, fmt.Errorf("unmarshal parts: %w", err)
			}
		}

		if metadataJSON != "" && metadataJSON != "{}" {
			if err := json.Unmarshal([]byte(metadataJSON), &m.Metadata); err != nil {
				return nil, false, fmt.Errorf("unmarshal metadata: %w", err)
			}
		}

		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate messages paginated: %w", err)
	}

	// Check if there are more messages
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}

	// Reverse to get chronological order (oldest to newest)
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}

	return items, hasMore, nil
}

func (s *SQLiteStore) UpdateMessageParts(messageID string, parts []MessagePart, metadata MessageMetadata) error {
	partsJSON, err := json.Marshal(parts)
	if err != nil {
		return fmt.Errorf("marshal parts: %w", err)
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	_, err = s.db.Exec(`UPDATE chat_messages SET parts = ?, metadata = ? WHERE id = ?`,
		string(partsJSON), string(metadataJSON), messageID)
	return err
}

func (s *SQLiteStore) DeleteMessagesBySession(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM chat_messages WHERE session_id = ?`, sessionID)
	return err
}

func (s *SQLiteStore) CreateUserMessage(sessionID, content string) (*ChatMessage, error) {
	parts := []MessagePart{
		{Type: "text", Content: content},
	}
	return s.CreateMessage(sessionID, "user", parts, MessageMetadata{})
}

// ---------------------------------------------------------------------------
// Queued Messages
// ---------------------------------------------------------------------------

func (s *SQLiteStore) ListQueuedMessages(sessionID string) ([]QueuedMessage, error) {
	rows, err := s.db.Query(
		`SELECT id, session_id, text, images, created_at
		 FROM queued_messages WHERE session_id = ? ORDER BY created_at ASC`, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query queued messages: %w", err)
	}
	defer rows.Close()

	var items []QueuedMessage
	for rows.Next() {
		var m QueuedMessage
		var imagesJSON string
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Text, &imagesJSON, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan queued message: %w", err)
		}
		if imagesJSON != "" && imagesJSON != "[]" {
			if err := json.Unmarshal([]byte(imagesJSON), &m.Images); err != nil {
				log.Printf("[sqlite] failed to decode queued message images (id=%s): %v", m.ID, err)
			}
		}
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate queued messages: %w", err)
	}
	return items, nil
}

func (s *SQLiteStore) CreateQueuedMessage(sessionID string, req CreateQueuedMessageRequest) (*QueuedMessage, error) {
	imagesJSON, _ := json.Marshal(req.Images)
	if req.Images == nil {
		imagesJSON = []byte("[]")
	}
	m := &QueuedMessage{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		Text:      req.Text,
		Images:    req.Images,
		CreatedAt: time.Now(),
	}
	_, err := s.db.Exec(
		`INSERT INTO queued_messages (id, session_id, text, images, created_at) VALUES (?, ?, ?, ?, ?)`,
		m.ID, m.SessionID, m.Text, string(imagesJSON), m.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert queued message: %w", err)
	}
	return m, nil
}

func (s *SQLiteStore) UpdateQueuedMessage(sessionID, id, text string) error {
	_, err := s.db.Exec(
		`UPDATE queued_messages SET text = ? WHERE id = ? AND session_id = ?`,
		text, id, sessionID,
	)
	return err
}

func (s *SQLiteStore) DeleteQueuedMessage(sessionID, id string) error {
	_, err := s.db.Exec(
		`DELETE FROM queued_messages WHERE id = ? AND session_id = ?`, id, sessionID,
	)
	return err
}

func (s *SQLiteStore) PopQueuedMessage(sessionID string) (*QueuedMessage, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var m QueuedMessage
	var imagesJSON string
	err = tx.QueryRow(
		`SELECT id, session_id, text, images, created_at
		 FROM queued_messages WHERE session_id = ? ORDER BY created_at ASC LIMIT 1`, sessionID,
	).Scan(&m.ID, &m.SessionID, &m.Text, &imagesJSON, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pop queued select: %w", err)
	}
	if imagesJSON != "" && imagesJSON != "[]" {
		_ = json.Unmarshal([]byte(imagesJSON), &m.Images)
	}

	if _, err := tx.Exec(`DELETE FROM queued_messages WHERE id = ?`, m.ID); err != nil {
		return nil, fmt.Errorf("pop queued delete: %w", err)
	}
	return &m, tx.Commit()
}

func (s *SQLiteStore) PopQueuedMessageByID(sessionID, id string) (*QueuedMessage, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var m QueuedMessage
	var imagesJSON string
	err = tx.QueryRow(
		`SELECT id, session_id, text, images, created_at
		 FROM queued_messages WHERE id = ? AND session_id = ?`, id, sessionID,
	).Scan(&m.ID, &m.SessionID, &m.Text, &imagesJSON, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pop queued by id select: %w", err)
	}
	if imagesJSON != "" && imagesJSON != "[]" {
		_ = json.Unmarshal([]byte(imagesJSON), &m.Images)
	}

	if _, err := tx.Exec(`DELETE FROM queued_messages WHERE id = ?`, m.ID); err != nil {
		return nil, fmt.Errorf("pop queued by id delete: %w", err)
	}
	return &m, tx.Commit()
}

func (s *SQLiteStore) SessionsWithQueuedMessages() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT session_id FROM queued_messages`)
	if err != nil {
		return nil, fmt.Errorf("query sessions with queued messages: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan session id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions with queued messages: %w", err)
	}
	return ids, nil
}

// ---------------------------------------------------------------------------
// Review Comments
// ---------------------------------------------------------------------------

func (s *SQLiteStore) CreateReviewComment(assetID string, req CreateReviewCommentRequest) (*ReviewComment, error) {
	c := &ReviewComment{
		ID:           uuid.New().String(),
		AssetID:      assetID,
		AssetVersion: req.AssetVersion,
		LineNumber:   req.LineNumber,
		Content:      req.Content,
		Author:       req.Author,
		Status:       "open",
		CreatedAt:    time.Now(),
	}
	if c.Author == "" {
		c.Author = "user"
	}

	_, err := s.db.Exec(
		`INSERT INTO review_comments (id, asset_id, asset_version, line_number, content, author, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.AssetID, c.AssetVersion, c.LineNumber, c.Content, c.Author, c.Status, c.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert review comment: %w", err)
	}
	return c, nil
}

func (s *SQLiteStore) ListReviewComments(assetID string) ([]ReviewComment, error) {
	rows, err := s.db.Query(
		`SELECT id, asset_id, asset_version, line_number, content, author, status, created_at
		 FROM review_comments WHERE asset_id = ? ORDER BY created_at ASC`, assetID,
	)
	if err != nil {
		return nil, fmt.Errorf("query review comments: %w", err)
	}
	defer rows.Close()

	var items []ReviewComment
	for rows.Next() {
		var c ReviewComment
		if err := rows.Scan(&c.ID, &c.AssetID, &c.AssetVersion, &c.LineNumber, &c.Content, &c.Author, &c.Status, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan review comment: %w", err)
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate review comments: %w", err)
	}
	return items, nil
}

func (s *SQLiteStore) GetReviewComment(id string) (*ReviewComment, error) {
	var c ReviewComment
	err := s.db.QueryRow(
		`SELECT id, asset_id, asset_version, line_number, content, author, status, created_at
		 FROM review_comments WHERE id = ?`, id,
	).Scan(&c.ID, &c.AssetID, &c.AssetVersion, &c.LineNumber, &c.Content, &c.Author, &c.Status, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get review comment: %w", err)
	}
	return &c, nil
}

func (s *SQLiteStore) UpdateReviewComment(id string, req UpdateReviewCommentRequest) (*ReviewComment, error) {
	c, err := s.GetReviewComment(id)
	if err != nil || c == nil {
		return nil, err
	}

	if req.Content != nil {
		c.Content = *req.Content
	}
	if req.Status != nil {
		c.Status = *req.Status
	}

	_, err = s.db.Exec(
		`UPDATE review_comments SET content = ?, status = ? WHERE id = ?`,
		c.Content, c.Status, c.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update review comment: %w", err)
	}
	return c, nil
}

func (s *SQLiteStore) DeleteReviewComment(id string) error {
	_, err := s.db.Exec(`DELETE FROM review_comments WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete review comment: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ResolveAllReviewComments(assetID string) (int, error) {
	result, err := s.db.Exec(
		`UPDATE review_comments SET status = 'resolved' WHERE asset_id = ? AND status = 'open'`,
		assetID,
	)
	if err != nil {
		return 0, fmt.Errorf("resolve review comments: %w", err)
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

const taskColumns = `id, project_id, parent_id, title, description, status, priority, assigner, ref_count, comment_count, created_by, version, labels, created_at, updated_at, closed_at`

func scanTask(row interface{ Scan(...interface{}) error }) (*Task, error) {
	var t Task
	var closedAt sql.NullTime
	var commentCount sql.NullInt64
	var labelsRaw sql.NullString
	err := row.Scan(&t.ID, &t.ProjectID, &t.ParentID, &t.Title, &t.Description,
		&t.Status, &t.Priority, &t.Assigner, &t.RefCount, &commentCount, &t.CreatedBy, &t.Version,
		&labelsRaw, &t.CreatedAt, &t.UpdatedAt, &closedAt)
	if err != nil {
		return nil, err
	}
	if closedAt.Valid {
		t.ClosedAt = &closedAt.Time
	}
	if commentCount.Valid {
		t.CommentCount = int(commentCount.Int64)
	}
	if labelsRaw.Valid {
		t.Labels = splitLabels(labelsRaw.String)
	} else {
		t.Labels = []string{}
	}
	return &t, nil
}

func splitLabels(raw string) []string {
	if raw == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{}
	}
	return out
}

func joinLabels(labels []string) string {
	return strings.Join(labels, ",")
}

func (s *SQLiteStore) CreateTask(req CreateTaskRequest) (*Task, error) {
	priority := req.Priority
	if priority == "" {
		priority = "medium"
	}
	now := time.Now()

	t := &Task{
		ID:          uuid.New().String(),
		ProjectID:   req.ProjectID,
		ParentID:    req.ParentID,
		Title:       req.Title,
		Description: req.Description,
		Status:      "open",
		Priority:    priority,
		Assigner:    req.Assigner,
		CreatedBy:   "local",
		Version:     1,
		Labels:      []string{},
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	_, err := s.db.Exec(
		`INSERT INTO tasks (id, project_id, parent_id, title, description, status, priority, assigner, ref_count, comment_count, created_by, version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?, ?)`,
		t.ID, t.ProjectID, t.ParentID, t.Title, t.Description, t.Status, t.Priority, t.Assigner, t.CreatedBy, t.Version, t.CreatedAt, t.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert task: %w", err)
	}
	return t, nil
}

func (s *SQLiteStore) ListTasks(limit, offset int, projectIDs []string, search string) ([]Task, error) {
	if limit <= 0 {
		limit = 200
	}

	q := `SELECT ` + taskColumns + ` FROM tasks`
	var args []interface{}
	var conditions []string
	if projectIDs != nil {
		placeholders := strings.Repeat("?,", len(projectIDs)+1)
		placeholders = placeholders[:len(placeholders)-1]
		conditions = append(conditions, `project_id IN (`+placeholders+`)`)
		for _, id := range projectIDs {
			args = append(args, id)
		}
		args = append(args, "")
	}
	if search != "" {
		conditions = append(conditions, `title LIKE ?`)
		args = append(args, "%"+search+"%")
	}
	if len(conditions) > 0 {
		q += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	q += ` ORDER BY updated_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query tasks: %w", err)
	}
	defer rows.Close()

	var items []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		items = append(items, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tasks: %w", err)
	}

	return items, nil
}

func (s *SQLiteStore) GetTask(id string) (*Task, error) {
	t, err := scanTask(s.db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	return t, nil
}

func (s *SQLiteStore) UpdateTask(id string, req UpdateTaskRequest) (*Task, error) {
	t, err := s.GetTask(id)
	if err != nil || t == nil {
		return nil, err
	}

	oldStatus := t.Status
	if req.ParentID != nil {
		t.ParentID = *req.ParentID
	}
	if req.ProjectID != nil {
		t.ProjectID = *req.ProjectID
	}
	if req.Title != nil {
		t.Title = *req.Title
	}
	if req.Description != nil {
		t.Description = *req.Description
	}
	if req.Status != nil {
		t.Status = *req.Status
	}
	if req.Priority != nil {
		t.Priority = *req.Priority
	}
	if req.Assigner != nil {
		t.Assigner = *req.Assigner
	}

	// Track closed_at: set when transitioning to done/closed, clear when leaving
	isClosed := t.Status == "done" || t.Status == "closed"
	wasClosed := oldStatus == "done" || oldStatus == "closed"
	if isClosed && !wasClosed {
		now := time.Now()
		t.ClosedAt = &now
	} else if !isClosed && wasClosed {
		t.ClosedAt = nil
	}

	newVersion := t.Version + 1
	t.UpdatedAt = time.Now()

	query := `UPDATE tasks SET parent_id=?, project_id=?, title=?, description=?, status=?, priority=?, assigner=?, closed_at=?, version=?, updated_at=? WHERE id=?`
	args := []interface{}{t.ParentID, t.ProjectID, t.Title, t.Description, t.Status, t.Priority, t.Assigner, t.ClosedAt, newVersion, t.UpdatedAt, t.ID}

	if req.ExpectedVersion != nil {
		query += ` AND version=?`
		args = append(args, *req.ExpectedVersion)
	}

	res, err := s.db.Exec(query, args...)
	if err != nil {
		return nil, fmt.Errorf("update task: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 && req.ExpectedVersion != nil {
		return nil, ErrVersionConflict
	}
	t.Version = newVersion
	return t, nil
}

func (s *SQLiteStore) IncrementTaskRefCount(id string) error {
	_, err := s.db.Exec(`UPDATE tasks SET ref_count = ref_count + 1 WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) SetParent(taskID, parentID string) error {
	_, err := s.db.Exec(`UPDATE tasks SET parent_id = ?, updated_at = ? WHERE id = ?`, parentID, time.Now(), taskID)
	return err
}

func (s *SQLiteStore) RemoveParent(taskID string) error {
	_, err := s.db.Exec(`UPDATE tasks SET parent_id = '', updated_at = ? WHERE id = ?`, time.Now(), taskID)
	return err
}

func (s *SQLiteStore) DeleteTask(id string) error {
	rows, err := s.db.Query(`SELECT id FROM tasks WHERE id = ? OR parent_id = ?`, id, id)
	if err != nil {
		return fmt.Errorf("list tasks to delete: %w", err)
	}
	var taskIDs []string
	for rows.Next() {
		var tid string
		if err := rows.Scan(&tid); err == nil {
			taskIDs = append(taskIDs, tid)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate tasks to delete: %w", err)
	}
	rows.Close()
	if len(taskIDs) == 0 {
		return nil
	}

	ph := make([]string, len(taskIDs))
	args := make([]interface{}, len(taskIDs))
	for i, tid := range taskIDs {
		ph[i] = "?"
		args[i] = tid
	}
	joined := strings.Join(ph, ",")

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Clean up comment reactions
	commentIDs := []string{}
	cRows, _ := tx.Query(`SELECT id FROM task_comments WHERE task_id IN (`+joined+`)`, args...)
	if cRows != nil {
		for cRows.Next() {
			var cid string
			if cRows.Scan(&cid) == nil {
				commentIDs = append(commentIDs, cid)
			}
		}
		cRows.Close()
	}
	if len(commentIDs) > 0 {
		cph := make([]string, len(commentIDs))
		cargs := make([]interface{}, len(commentIDs)+1)
		cargs[0] = "task_comment"
		for i, cid := range commentIDs {
			cph[i] = "?"
			cargs[i+1] = cid
		}
		if _, err := tx.Exec(`DELETE FROM reactions WHERE target_type = ? AND target_id IN (`+strings.Join(cph, ",")+`)`, cargs...); err != nil {
			return fmt.Errorf("delete comment reactions: %w", err)
		}
	}

	// Clean up task reactions
	targs := make([]interface{}, len(taskIDs)+1)
	targs[0] = "task"
	for i, tid := range taskIDs {
		targs[i+1] = tid
	}
	if _, err := tx.Exec(`DELETE FROM reactions WHERE target_type = ? AND target_id IN (`+joined+`)`, targs...); err != nil {
		return fmt.Errorf("delete task reactions: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM task_comments WHERE task_id IN (`+joined+`)`, args...); err != nil {
		return fmt.Errorf("delete task comments: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM tasks WHERE id IN (`+joined+`)`, args...); err != nil {
		return fmt.Errorf("delete tasks: %w", err)
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Assets
// ---------------------------------------------------------------------------

func (s *SQLiteStore) CreateAsset(projectID string, req CreateAssetRequest) (*Asset, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	asset := &Asset{
		ID:             uuid.New().String(),
		ProjectID:      projectID,
		Type:           req.Type,
		Title:          req.Title,
		CurrentVersion: 1,
		Status:         "draft",
		CreatedBy:      "local",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	_, err = tx.Exec(
		`INSERT INTO assets (id, project_id, type, title, current_version, status, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		asset.ID, asset.ProjectID, asset.Type, asset.Title, asset.CurrentVersion, asset.Status, asset.CreatedBy, asset.CreatedAt, asset.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert asset: %w", err)
	}

	versionID := uuid.New().String()
	_, err = tx.Exec(
		`INSERT INTO asset_versions (id, asset_id, version, content, created_at) VALUES (?, ?, ?, ?, ?)`,
		versionID, asset.ID, 1, req.Content, time.Now(),
	)
	if err != nil {
		return nil, fmt.Errorf("insert asset version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return asset, nil
}

func (s *SQLiteStore) queryAssets(projectID, assetType string, limit, offset int, q string) ([]Asset, error) {
	if limit <= 0 {
		limit = 20
	}
	var conditions []string
	var args []interface{}
	conditions = append(conditions, `project_id = ?`)
	args = append(args, projectID)
	if assetType != "" {
		conditions = append(conditions, `type = ?`)
		args = append(args, assetType)
	}
	if q != "" {
		conditions = append(conditions, `title LIKE ?`)
		args = append(args, "%"+q+"%")
	}
	query := `SELECT id, project_id, type, title, current_version, status, created_by, created_at, updated_at, lark_doc_url FROM assets WHERE ` +
		strings.Join(conditions, ` AND `) + ` ORDER BY updated_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query assets: %w", err)
	}
	defer rows.Close()

	var items []Asset
	for rows.Next() {
		var a Asset
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Type, &a.Title, &a.CurrentVersion, &a.Status, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt, &a.LarkDocURL); err != nil {
			return nil, fmt.Errorf("scan asset: %w", err)
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assets: %w", err)
	}
	return items, nil
}

func (s *SQLiteStore) ListAssets(projectID string, limit, offset int, q string) ([]Asset, error) {
	return s.queryAssets(projectID, "", limit, offset, q)
}

func (s *SQLiteStore) ListAssetsByType(projectID, assetType string, limit, offset int, q string) ([]Asset, error) {
	return s.queryAssets(projectID, assetType, limit, offset, q)
}

func (s *SQLiteStore) GetAsset(id string) (*Asset, error) {
	var a Asset
	err := s.db.QueryRow(
		`SELECT id, project_id, type, title, current_version, status, created_by, created_at, updated_at, lark_doc_url
		 FROM assets WHERE id = ?`, id,
	).Scan(&a.ID, &a.ProjectID, &a.Type, &a.Title, &a.CurrentVersion, &a.Status, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt, &a.LarkDocURL)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get asset: %w", err)
	}
	return &a, nil
}

func (s *SQLiteStore) SetAssetLarkDoc(id, url string) error {
	_, err := s.db.Exec(
		`UPDATE assets SET lark_doc_url = ?, updated_at = ? WHERE id = ?`,
		url, time.Now(), id,
	)
	return err
}

func (s *SQLiteStore) UpdateAsset(id string, req UpdateAssetRequest) (*Asset, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Read inside the transaction so the version check at the UPDATE is consistent.
	var asset Asset
	err = tx.QueryRow(
		`SELECT id, project_id, type, title, current_version, status, created_by, created_at, updated_at, lark_doc_url
		 FROM assets WHERE id = ?`, id,
	).Scan(&asset.ID, &asset.ProjectID, &asset.Type, &asset.Title, &asset.CurrentVersion,
		&asset.Status, &asset.CreatedBy, &asset.CreatedAt, &asset.UpdatedAt, &asset.LarkDocURL)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get asset: %w", err)
	}

	if req.Title != nil {
		asset.Title = *req.Title
	}
	if req.Status != nil {
		asset.Status = *req.Status
	}

	if req.Content != nil {
		asset.CurrentVersion++
		versionID := uuid.New().String()
		_, err = tx.Exec(
			`INSERT INTO asset_versions (id, asset_id, version, content, created_at) VALUES (?, ?, ?, ?, ?)`,
			versionID, asset.ID, asset.CurrentVersion, *req.Content, time.Now(),
		)
		if err != nil {
			return nil, fmt.Errorf("insert new version: %w", err)
		}

		_, err = tx.Exec(
			`UPDATE review_comments SET status = 'resolved' WHERE asset_id = ? AND status = 'open'`,
			asset.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("resolve comments: %w", err)
		}
	}

	asset.UpdatedAt = time.Now()

	query := `UPDATE assets SET title = ?, current_version = ?, status = ?, updated_at = ? WHERE id = ?`
	args := []interface{}{asset.Title, asset.CurrentVersion, asset.Status, asset.UpdatedAt, asset.ID}

	if req.ExpectedVersion != nil {
		query += ` AND current_version = ?`
		args = append(args, *req.ExpectedVersion)
	}

	res, err := tx.Exec(query, args...)
	if err != nil {
		return nil, fmt.Errorf("update asset: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 && req.ExpectedVersion != nil {
		return nil, ErrVersionConflict
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &asset, nil
}

func (s *SQLiteStore) DeleteAsset(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM review_comments WHERE asset_id = ?`, id); err != nil {
		return fmt.Errorf("delete asset review_comments: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM asset_versions WHERE asset_id = ?`, id); err != nil {
		return fmt.Errorf("delete asset_versions: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM assets WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete asset: %w", err)
	}
	return tx.Commit()
}

func (s *SQLiteStore) GetAssetVersions(assetID string) ([]AssetVersion, error) {
	rows, err := s.db.Query(
		`SELECT id, asset_id, version, content, created_at FROM asset_versions WHERE asset_id = ? ORDER BY version DESC`,
		assetID,
	)
	if err != nil {
		return nil, fmt.Errorf("query versions: %w", err)
	}
	defer rows.Close()

	var items []AssetVersion
	for rows.Next() {
		var v AssetVersion
		if err := rows.Scan(&v.ID, &v.AssetID, &v.Version, &v.Content, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan version: %w", err)
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate asset versions: %w", err)
	}
	return items, nil
}

func (s *SQLiteStore) GetAssetVersion(assetID string, version int) (*AssetVersion, error) {
	var v AssetVersion
	err := s.db.QueryRow(
		`SELECT id, asset_id, version, content, created_at FROM asset_versions WHERE asset_id = ? AND version = ?`,
		assetID, version,
	).Scan(&v.ID, &v.AssetID, &v.Version, &v.Content, &v.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get version: %w", err)
	}
	return &v, nil
}

func (s *SQLiteStore) FindAssetByTitle(projectID, title string) (*Asset, error) {
	var a Asset
	err := s.db.QueryRow(
		`SELECT id, project_id, type, title, current_version, status, created_by, created_at, updated_at
		 FROM assets WHERE project_id = ? AND title = ? LIMIT 1`, projectID, title,
	).Scan(&a.ID, &a.ProjectID, &a.Type, &a.Title, &a.CurrentVersion, &a.Status, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find asset by title: %w", err)
	}
	return &a, nil
}

func (s *SQLiteStore) GetLatestAssetContent(assetID string) (string, error) {
	var content string
	err := s.db.QueryRow(
		`SELECT content FROM asset_versions WHERE asset_id = ? ORDER BY version DESC LIMIT 1`, assetID,
	).Scan(&content)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get latest content: %w", err)
	}
	return content, nil
}

// ---------------------------------------------------------------------------
// Changes (local mode: minimal implementation)
// ---------------------------------------------------------------------------

func (s *SQLiteStore) ListChanges(sinceID int64, limit int, order string) ([]Change, error) {
	if limit <= 0 {
		limit = 100
	}
	orderSQL := "ASC"
	whereOp := ">"
	if order == "desc" {
		orderSQL = "DESC"
		whereOp = "<"
	}
	q := `SELECT id, COALESCE(project_id,''), entity_type, entity_id, action, summary, COALESCE(detail,''), user_name, entity_version, created_at
		 FROM changes`
	var args []interface{}
	if sinceID > 0 {
		q += ` WHERE id ` + whereOp + ` ?`
		args = append(args, sinceID)
	}
	q += ` ORDER BY id ` + orderSQL + ` LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query changes: %w", err)
	}
	defer rows.Close()

	var items []Change
	for rows.Next() {
		var c Change
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.EntityType, &c.EntityID, &c.Action, &c.Summary, &c.Detail, &c.UserName, &c.EntityVersion, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan change: %w", err)
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate changes: %w", err)
	}
	return items, nil
}
