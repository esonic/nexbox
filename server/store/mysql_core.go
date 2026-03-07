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

func (s *MySQLStore) CreateProject(req CreateProjectRequest) (*Project, error) {
	now := time.Now()
	p := &Project{
		ID:            uuid.New().String(),
		NamespaceID:   "local",
		Name:          req.Name,
		Description:   req.Description,
		GitRepoPath:   req.GitRepoPath,
		GitLabRepoURL: req.GitLabRepoURL,
		CreatedBy:     "local",
		OwnerID:       req.OwnerID,
		Version:       1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	_, err := s.db.Exec(
		`INSERT INTO projects (id, namespace_id, name, description, git_repo_path, gitlab_repo_url, created_by, owner_id, version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.NamespaceID, p.Name, p.Description, p.GitRepoPath, p.GitLabRepoURL, p.CreatedBy, p.OwnerID, p.Version, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert project: %w", err)
	}
	return p, nil
}

func (s *MySQLStore) ListProjects() ([]Project, error) {
	rows, err := s.db.Query(
		`SELECT id, namespace_id, name, description, git_repo_path, gitlab_repo_url, created_by, owner_id, version, created_at, updated_at
		 FROM projects ORDER BY updated_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query projects: %w", err)
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		p, err := mysqlScanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, *p)
	}
	return projects, rows.Err()
}

func (s *MySQLStore) GetProject(id string) (*Project, error) {
	row := s.db.QueryRow(
		`SELECT id, namespace_id, name, description, git_repo_path, gitlab_repo_url, created_by, owner_id, version, created_at, updated_at
		 FROM projects WHERE id = ?`, id,
	)
	p, err := mysqlScanProject(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}

func (s *MySQLStore) GetProjectsByIDs(ids []string) ([]Project, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := `SELECT id, namespace_id, name, description, git_repo_path, gitlab_repo_url, created_by, owner_id, version, created_at, updated_at
	          FROM projects WHERE id IN (` + strings.Join(placeholders, ",") + `) ORDER BY updated_at DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query projects by ids: %w", err)
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		p, err := mysqlScanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, *p)
	}
	return projects, rows.Err()
}

func (s *MySQLStore) UpdateProject(id string, req UpdateProjectRequest) (*Project, error) {
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
	if req.GitLabRepoURL != nil {
		p.GitLabRepoURL = *req.GitLabRepoURL
	}
	newVersion := p.Version + 1
	p.UpdatedAt = time.Now()

	query := `UPDATE projects SET name=?, description=?, git_repo_path=?, gitlab_repo_url=?, version=?, updated_at=? WHERE id=?`
	args := []interface{}{p.Name, p.Description, p.GitRepoPath, p.GitLabRepoURL, newVersion, p.UpdatedAt, p.ID}

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

func (s *MySQLStore) DeleteProject(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM sessions WHERE project_id = ?`, id); err != nil {
		return fmt.Errorf("delete project sessions: %w", err)
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

func mysqlScanProject(row interface{ Scan(...interface{}) error }) (*Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.NamespaceID, &p.Name, &p.Description, &p.GitRepoPath, &p.GitLabRepoURL, &p.CreatedBy, &p.OwnerID, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func (s *MySQLStore) CreateSession(req CreateSessionRequest) (*Session, error) {
	now := time.Now()
	sess := &Session{
		ID:          uuid.New().String(),
		NamespaceID: "local",
		Name:        req.Name,
		ProjectID:   req.ProjectID,
		Agent:       req.Agent,
		CreatedBy:   "local",
		UserID:      req.UserID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	_, err := s.db.Exec(
		`INSERT INTO sessions (id, namespace_id, name, project_id, agent, agent_session_id, stream_id, features, created_by, user_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, '', '', '', ?, ?, ?, ?)`,
		sess.ID, sess.NamespaceID, sess.Name, sess.ProjectID, sess.Agent, sess.CreatedBy, sess.UserID, sess.CreatedAt, sess.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	return sess, nil
}

func (s *MySQLStore) ListSessions() ([]Session, error) {
	rows, err := s.db.Query(
		`SELECT id, namespace_id, name, project_id, agent, agent_session_id, stream_id, sort_order, features, worktree_mode, worktree_path, created_by, user_id, created_at, updated_at, deleted_at
		 FROM sessions WHERE deleted_at IS NULL ORDER BY sort_order ASC, created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var items []Session
	for rows.Next() {
		sess, err := mysqlScanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		items = append(items, *sess)
	}
	return items, rows.Err()
}

func (s *MySQLStore) ListTrashedSessions() ([]Session, error) {
	rows, err := s.db.Query(
		`SELECT id, namespace_id, name, project_id, agent, agent_session_id, stream_id, sort_order, features, worktree_mode, worktree_path, created_by, user_id, created_at, updated_at, deleted_at
		 FROM sessions WHERE deleted_at IS NOT NULL ORDER BY deleted_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query trashed sessions: %w", err)
	}
	defer rows.Close()

	var items []Session
	for rows.Next() {
		sess, err := mysqlScanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan trashed session: %w", err)
		}
		items = append(items, *sess)
	}
	return items, rows.Err()
}

func (s *MySQLStore) GetSession(id string) (*Session, error) {
	row := s.db.QueryRow(
		`SELECT id, namespace_id, name, project_id, agent, agent_session_id, stream_id, sort_order, features, worktree_mode, worktree_path, created_by, user_id, created_at, updated_at, deleted_at
		 FROM sessions WHERE id = ?`, id,
	)
	sess, err := mysqlScanSession(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	return sess, nil
}

func (s *MySQLStore) UpdateSession(id string, req UpdateSessionRequest) (*Session, error) {
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
	sess.UpdatedAt = time.Now()

	_, err = s.db.Exec(
		`UPDATE sessions SET name = ?, project_id = ?, agent = ?, updated_at = ? WHERE id = ?`,
		sess.Name, sess.ProjectID, sess.Agent, sess.UpdatedAt, sess.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update session: %w", err)
	}
	return sess, nil
}

func (s *MySQLStore) UpdateSessionAgentID(id, agentSessionID string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET agent_session_id = ?, updated_at = ? WHERE id = ?`,
		agentSessionID, time.Now(), id,
	)
	return err
}

func (s *MySQLStore) UpdateSessionStreamID(id, streamID string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET stream_id = ?, updated_at = ? WHERE id = ?`,
		streamID, time.Now(), id,
	)
	return err
}

func (s *MySQLStore) UpdateSessionFeatures(id, features string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET features = ?, updated_at = ? WHERE id = ?`,
		features, time.Now(), id,
	)
	return err
}

func (s *MySQLStore) UpdateSessionWorktree(id, mode, path string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET worktree_mode = ?, worktree_path = ?, updated_at = ? WHERE id = ?`,
		mode, path, time.Now(), id,
	)
	return err
}

func (s *MySQLStore) GetExpiredSessionWorktrees(olderThan time.Time) ([]string, error) {
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

func (s *MySQLStore) DeleteSession(id string) error {
	_, err := s.db.Exec(`UPDATE sessions SET deleted_at = ? WHERE id = ?`, time.Now(), id)
	return err
}

func (s *MySQLStore) RestoreSession(id string) error {
	_, err := s.db.Exec(`UPDATE sessions SET deleted_at = NULL WHERE id = ?`, id)
	return err
}

// PurgeExpiredSessions hard-deletes sessions trashed before olderThan.
// MySQL does not support RETURNING, so we select IDs first, then delete in a transaction.
func (s *MySQLStore) PurgeExpiredSessions(olderThan time.Time) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Collect IDs to delete.
	rows, err := tx.Query(
		`SELECT id FROM sessions WHERE deleted_at IS NOT NULL AND deleted_at < ?`,
		olderThan,
	)
	if err != nil {
		return nil, fmt.Errorf("select expired sessions: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan session id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired sessions: %w", err)
	}

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

	// Build placeholders for bulk delete.
	ph := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE id IN (`+strings.Join(ph, ",")+`)`, args...); err != nil {
		return nil, fmt.Errorf("delete expired sessions: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit purge tx: %w", err)
	}
	return ids, nil
}

func (s *MySQLStore) ReorderSessions(sessionIDs []string) error {
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

func mysqlScanSession(row interface{ Scan(...interface{}) error }) (*Session, error) {
	var sess Session
	var deletedAt sql.NullTime
	if err := row.Scan(&sess.ID, &sess.NamespaceID, &sess.Name, &sess.ProjectID, &sess.Agent,
		&sess.AgentSessionID, &sess.StreamID, &sess.SortOrder, &sess.Features,
		&sess.WorktreeMode, &sess.WorktreePath,
		&sess.CreatedBy, &sess.UserID, &sess.CreatedAt, &sess.UpdatedAt, &deletedAt); err != nil {
		return nil, err
	}
	if sess.WorktreeMode == "" {
		sess.WorktreeMode = "local"
	}
	if deletedAt.Valid {
		sess.DeletedAt = &deletedAt.Time
	}
	return &sess, nil
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

func (s *MySQLStore) CreateMessage(sessionID, role string, parts []MessagePart, metadata MessageMetadata) (*ChatMessage, error) {
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
		log.Printf("[mysql] failed to update session updated_at (id=%s): %v", sessionID, err)
	}

	return m, nil
}

func (s *MySQLStore) ListMessages(sessionID string) ([]ChatMessage, error) {
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
		m, err := mysqlScanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		items = append(items, *m)
	}
	return items, rows.Err()
}

func (s *MySQLStore) ListMessagesPaginated(sessionID string, limit int, beforeID string) ([]ChatMessage, bool, error) {
	if limit <= 0 {
		limit = 20
	}

	var query string
	var args []interface{}

	if beforeID == "" {
		query = `SELECT id, session_id, role, parts, metadata, created_at
		         FROM chat_messages
		         WHERE session_id = ?
		         ORDER BY created_at DESC
		         LIMIT ?`
		args = []interface{}{sessionID, limit + 1}
	} else {
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
		m, err := mysqlScanMessage(rows)
		if err != nil {
			return nil, false, fmt.Errorf("scan message: %w", err)
		}
		items = append(items, *m)
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}

	// Reverse to get chronological order.
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}

	return items, hasMore, nil
}

func (s *MySQLStore) UpdateMessageParts(messageID string, parts []MessagePart, metadata MessageMetadata) error {
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

func (s *MySQLStore) DeleteMessagesBySession(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM chat_messages WHERE session_id = ?`, sessionID)
	return err
}

func (s *MySQLStore) CreateUserMessage(sessionID, content string) (*ChatMessage, error) {
	parts := []MessagePart{
		{Type: "text", Content: content},
	}
	return s.CreateMessage(sessionID, "user", parts, MessageMetadata{})
}

func mysqlScanMessage(row interface{ Scan(...interface{}) error }) (*ChatMessage, error) {
	var m ChatMessage
	var partsJSON, metadataJSON string
	if err := row.Scan(&m.ID, &m.SessionID, &m.Role, &partsJSON, &metadataJSON, &m.CreatedAt); err != nil {
		return nil, err
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

// ---------------------------------------------------------------------------
// Queued Messages
// ---------------------------------------------------------------------------

func (s *MySQLStore) ListQueuedMessages(sessionID string) ([]QueuedMessage, error) {
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
				log.Printf("[mysql] failed to decode queued message images (id=%s): %v", m.ID, err)
			}
		}
		items = append(items, m)
	}
	return items, rows.Err()
}

func (s *MySQLStore) CreateQueuedMessage(sessionID string, req CreateQueuedMessageRequest) (*QueuedMessage, error) {
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

func (s *MySQLStore) UpdateQueuedMessage(sessionID, id, text string) error {
	_, err := s.db.Exec(
		`UPDATE queued_messages SET text = ? WHERE id = ? AND session_id = ?`,
		text, id, sessionID,
	)
	return err
}

func (s *MySQLStore) DeleteQueuedMessage(sessionID, id string) error {
	_, err := s.db.Exec(
		`DELETE FROM queued_messages WHERE id = ? AND session_id = ?`, id, sessionID,
	)
	return err
}

func (s *MySQLStore) PopQueuedMessage(sessionID string) (*QueuedMessage, error) {
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

func (s *MySQLStore) PopQueuedMessageByID(sessionID, id string) (*QueuedMessage, error) {
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

func (s *MySQLStore) SessionsWithQueuedMessages() ([]string, error) {
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
	return ids, rows.Err()
}

// ---------------------------------------------------------------------------
// Review Comments
// ---------------------------------------------------------------------------

func (s *MySQLStore) CreateReviewComment(assetID string, req CreateReviewCommentRequest) (*ReviewComment, error) {
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

func (s *MySQLStore) ListReviewComments(assetID string) ([]ReviewComment, error) {
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
	return items, rows.Err()
}

func (s *MySQLStore) GetReviewComment(id string) (*ReviewComment, error) {
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

func (s *MySQLStore) UpdateReviewComment(id string, req UpdateReviewCommentRequest) (*ReviewComment, error) {
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

func (s *MySQLStore) DeleteReviewComment(id string) error {
	_, err := s.db.Exec(`DELETE FROM review_comments WHERE id = ?`, id)
	return err
}

func (s *MySQLStore) ResolveAllReviewComments(assetID string) (int, error) {
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

func (s *MySQLStore) CreateTask(req CreateTaskRequest) (*Task, error) {
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

func (s *MySQLStore) ListTasks(limit, offset int, projectIDs []string, search string) ([]Task, error) {
	if limit <= 0 {
		limit = 200
	}

	q := `SELECT id, project_id, parent_id, title, description, status, priority, assigner, ref_count, comment_count, created_by, version, labels, created_at, updated_at, closed_at FROM tasks`
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
		t, err := mysqlScanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		items = append(items, *t)
	}
	return items, rows.Err()
}

func (s *MySQLStore) GetTask(id string) (*Task, error) {
	t, err := mysqlScanTask(s.db.QueryRow(
		`SELECT id, project_id, parent_id, title, description, status, priority, assigner, ref_count, comment_count, created_by, version, labels, created_at, updated_at, closed_at FROM tasks WHERE id = ?`, id,
	))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	return t, nil
}

func (s *MySQLStore) UpdateTask(id string, req UpdateTaskRequest) (*Task, error) {
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

	// Track closed_at
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

func (s *MySQLStore) IncrementTaskRefCount(id string) error {
	_, err := s.db.Exec(`UPDATE tasks SET ref_count = ref_count + 1 WHERE id = ?`, id)
	return err
}

func (s *MySQLStore) DeleteTask(id string) error {
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

	// Clean up comment reactions.
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

	// Clean up task reactions.
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
	if _, err := tx.Exec(`DELETE FROM task_assets WHERE task_id IN (`+joined+`)`, args...); err != nil {
		return fmt.Errorf("delete task assets: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM tasks WHERE id IN (`+joined+`)`, args...); err != nil {
		return fmt.Errorf("delete tasks: %w", err)
	}
	return tx.Commit()
}

func (s *MySQLStore) LinkTaskAsset(taskID, assetID string) (*TaskAsset, error) {
	ta := &TaskAsset{
		ID:        uuid.New().String(),
		TaskID:    taskID,
		AssetID:   assetID,
		CreatedAt: time.Now(),
	}

	_, err := s.db.Exec(
		`INSERT INTO task_assets (id, task_id, asset_id, created_at) VALUES (?, ?, ?, ?)`,
		ta.ID, ta.TaskID, ta.AssetID, ta.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert task_asset: %w", err)
	}
	return ta, nil
}

func (s *MySQLStore) ListTaskAssets(taskID string) ([]Asset, error) {
	rows, err := s.db.Query(
		`SELECT a.id, a.project_id, a.type, a.title, a.current_version, a.status, a.created_by, a.created_at, a.updated_at
		 FROM assets a
		 JOIN task_assets ta ON ta.asset_id = a.id
		 WHERE ta.task_id = ?
		 ORDER BY a.updated_at DESC`, taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("query task assets: %w", err)
	}
	defer rows.Close()

	var items []Asset
	for rows.Next() {
		var a Asset
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Type, &a.Title, &a.CurrentVersion, &a.Status, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan asset: %w", err)
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

func (s *MySQLStore) UnlinkTaskAsset(taskID, assetID string) error {
	_, err := s.db.Exec(`DELETE FROM task_assets WHERE task_id = ? AND asset_id = ?`, taskID, assetID)
	return err
}

func mysqlScanTask(row interface{ Scan(...interface{}) error }) (*Task, error) {
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

// ---------------------------------------------------------------------------
// Task Comments
// ---------------------------------------------------------------------------

func (s *MySQLStore) ListTaskComments(taskID string) ([]TaskComment, error) {
	rows, err := s.db.Query(
		`SELECT id, task_id, author, body, created_at, updated_at
		 FROM task_comments WHERE task_id = ? ORDER BY created_at ASC`, taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("query task comments: %w", err)
	}
	defer rows.Close()

	var items []TaskComment
	for rows.Next() {
		var c TaskComment
		if err := rows.Scan(&c.ID, &c.TaskID, &c.Author, &c.Body, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan task comment: %w", err)
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

func (s *MySQLStore) CreateTaskComment(taskID string, req CreateTaskCommentRequest, author string) (*TaskComment, error) {
	if author == "" {
		author = "local"
	}
	now := time.Now()
	c := &TaskComment{
		ID:        uuid.New().String(),
		TaskID:    taskID,
		Author:    author,
		Body:      req.Body,
		CreatedAt: now,
		UpdatedAt: now,
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`INSERT INTO task_comments (id, task_id, author, body, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.TaskID, c.Author, c.Body, c.CreatedAt, c.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("insert task comment: %w", err)
	}
	if _, err := tx.Exec(`UPDATE tasks SET comment_count = comment_count + 1, updated_at = ? WHERE id = ?`, now, taskID); err != nil {
		return nil, fmt.Errorf("increment comment_count: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return c, nil
}

func (s *MySQLStore) UpdateTaskComment(id string, req UpdateTaskCommentRequest) (*TaskComment, error) {
	var c TaskComment
	err := s.db.QueryRow(
		`SELECT id, task_id, author, body, created_at, updated_at FROM task_comments WHERE id = ?`, id,
	).Scan(&c.ID, &c.TaskID, &c.Author, &c.Body, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get task comment: %w", err)
	}
	if req.Body != nil {
		c.Body = *req.Body
	}
	c.UpdatedAt = time.Now()
	_, err = s.db.Exec(`UPDATE task_comments SET body = ?, updated_at = ? WHERE id = ?`, c.Body, c.UpdatedAt, c.ID)
	if err != nil {
		return nil, fmt.Errorf("update task comment: %w", err)
	}
	return &c, nil
}

func (s *MySQLStore) DeleteTaskComment(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var taskID string
	if err := tx.QueryRow(`SELECT task_id FROM task_comments WHERE id = ?`, id).Scan(&taskID); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("lookup task comment: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM reactions WHERE target_type = 'task_comment' AND target_id = ?`, id); err != nil {
		return fmt.Errorf("delete comment reactions: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM task_comments WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete task comment: %w", err)
	}
	// MySQL does not have a MAX() guard in UPDATE like SQLite, but GREATEST() works.
	if _, err := tx.Exec(`UPDATE tasks SET comment_count = GREATEST(0, comment_count - 1) WHERE id = ?`, taskID); err != nil {
		return fmt.Errorf("decrement comment_count: %w", err)
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Labels
// ---------------------------------------------------------------------------

func (s *MySQLStore) ListLabels(_ string) ([]Label, error) {
	rows, err := s.db.Query(
		`SELECT id, name, color, description, created_at FROM labels ORDER BY name ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query labels: %w", err)
	}
	defer rows.Close()

	var items []Label
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.ID, &l.Name, &l.Color, &l.Description, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan label: %w", err)
		}
		items = append(items, l)
	}
	return items, rows.Err()
}

func (s *MySQLStore) CreateLabel(_ string, req CreateLabelRequest) (*Label, error) {
	l := &Label{
		ID:        uuid.New().String(),
		Name:      req.Name,
		Color:     "#6b7280",
		CreatedAt: time.Now(),
	}
	_, err := s.db.Exec(
		`INSERT INTO labels (id, name, color, created_at) VALUES (?, ?, ?, ?)`,
		l.ID, l.Name, l.Color, l.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert label: %w", err)
	}
	return l, nil
}

func (s *MySQLStore) DeleteLabel(_ string, id string) error {
	_, err := s.db.Exec(`DELETE FROM labels WHERE id = ?`, id)
	return err
}

func (s *MySQLStore) AddLabelToTask(taskID, labelName string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var labelsRaw sql.NullString
	if err := tx.QueryRow(`SELECT COALESCE(labels, '') FROM tasks WHERE id = ?`, taskID).Scan(&labelsRaw); err != nil {
		return fmt.Errorf("get task labels: %w", err)
	}
	current := splitLabels(labelsRaw.String)
	for _, l := range current {
		if l == labelName {
			return nil
		}
	}
	if len(current) >= 5 {
		return fmt.Errorf("task already has 5 labels (max)")
	}
	current = append(current, labelName)
	if _, err := tx.Exec(`UPDATE tasks SET labels = ?, updated_at = ? WHERE id = ?`,
		joinLabels(current), time.Now(), taskID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *MySQLStore) RemoveLabelFromTask(taskID, labelName string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var labelsRaw sql.NullString
	if err := tx.QueryRow(`SELECT COALESCE(labels, '') FROM tasks WHERE id = ?`, taskID).Scan(&labelsRaw); err != nil {
		return fmt.Errorf("get task labels: %w", err)
	}
	current := splitLabels(labelsRaw.String)
	updated := make([]string, 0, len(current))
	for _, l := range current {
		if l != labelName {
			updated = append(updated, l)
		}
	}
	if _, err := tx.Exec(`UPDATE tasks SET labels = ?, updated_at = ? WHERE id = ?`,
		joinLabels(updated), time.Now(), taskID); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Reactions
// ---------------------------------------------------------------------------

func (s *MySQLStore) GetReactionSummary(targetType, targetID, currentUser string) ([]ReactionSummary, error) {
	rows, err := s.db.Query(
		`SELECT emoji, COUNT(*) as cnt, SUM(CASE WHEN author = ? THEN 1 ELSE 0 END) as user_reacted
		 FROM reactions WHERE target_type = ? AND target_id = ?
		 GROUP BY emoji ORDER BY MIN(created_at) ASC`,
		currentUser, targetType, targetID,
	)
	if err != nil {
		return nil, fmt.Errorf("query reactions: %w", err)
	}
	defer rows.Close()

	var items []ReactionSummary
	for rows.Next() {
		var r ReactionSummary
		var userCount int
		if err := rows.Scan(&r.Emoji, &r.Count, &userCount); err != nil {
			return nil, fmt.Errorf("scan reaction: %w", err)
		}
		r.UserReacted = userCount > 0
		items = append(items, r)
	}
	return items, rows.Err()
}

func (s *MySQLStore) AddReaction(targetType, targetID, author, emoji string) error {
	id := uuid.New().String()
	_, err := s.db.Exec(
		`INSERT IGNORE INTO reactions (id, target_type, target_id, author, emoji, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, targetType, targetID, author, emoji, time.Now(),
	)
	return err
}

func (s *MySQLStore) RemoveReaction(targetType, targetID, author, emoji string) error {
	_, err := s.db.Exec(
		`DELETE FROM reactions WHERE target_type = ? AND target_id = ? AND author = ? AND emoji = ?`,
		targetType, targetID, author, emoji,
	)
	return err
}

// ---------------------------------------------------------------------------
// Assets
// ---------------------------------------------------------------------------

func (s *MySQLStore) CreateAsset(projectID string, req CreateAssetRequest) (*Asset, error) {
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

func (s *MySQLStore) mysqlQueryAssets(projectID, assetType string, limit, offset int, q string) ([]Asset, error) {
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
	query := `SELECT id, project_id, type, title, current_version, status, created_by, created_at, updated_at FROM assets WHERE ` +
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
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Type, &a.Title, &a.CurrentVersion, &a.Status, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan asset: %w", err)
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

func (s *MySQLStore) ListAssets(projectID string, limit, offset int, q string) ([]Asset, error) {
	return s.mysqlQueryAssets(projectID, "", limit, offset, q)
}

func (s *MySQLStore) ListAssetsByType(projectID, assetType string, limit, offset int, q string) ([]Asset, error) {
	return s.mysqlQueryAssets(projectID, assetType, limit, offset, q)
}

func (s *MySQLStore) GetAsset(id string) (*Asset, error) {
	var a Asset
	err := s.db.QueryRow(
		`SELECT id, project_id, type, title, current_version, status, created_by, created_at, updated_at
		 FROM assets WHERE id = ?`, id,
	).Scan(&a.ID, &a.ProjectID, &a.Type, &a.Title, &a.CurrentVersion, &a.Status, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get asset: %w", err)
	}
	return &a, nil
}

func (s *MySQLStore) UpdateAsset(id string, req UpdateAssetRequest) (*Asset, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var asset Asset
	err = tx.QueryRow(
		`SELECT id, project_id, type, title, current_version, status, created_by, created_at, updated_at
		 FROM assets WHERE id = ?`, id,
	).Scan(&asset.ID, &asset.ProjectID, &asset.Type, &asset.Title, &asset.CurrentVersion,
		&asset.Status, &asset.CreatedBy, &asset.CreatedAt, &asset.UpdatedAt)
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

func (s *MySQLStore) DeleteAsset(id string) error {
	_, err := s.db.Exec(`DELETE FROM assets WHERE id = ?`, id)
	return err
}

func (s *MySQLStore) GetAssetVersions(assetID string) ([]AssetVersion, error) {
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
	return items, rows.Err()
}

func (s *MySQLStore) GetAssetVersion(assetID string, version int) (*AssetVersion, error) {
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

func (s *MySQLStore) FindAssetByTitle(projectID, title string) (*Asset, error) {
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

func (s *MySQLStore) GetLatestAssetContent(assetID string) (string, error) {
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
// Changes
// ---------------------------------------------------------------------------

func (s *MySQLStore) ListChanges(sinceID int64, limit int, order string) ([]Change, error) {
	if limit <= 0 {
		limit = 100
	}
	orderSQL := "ASC"
	whereOp := ">"
	if order == "desc" {
		orderSQL = "DESC"
		whereOp = "<"
	}
	q := "SELECT id, COALESCE(project_id,''), entity_type, entity_id, action, summary, COALESCE(detail,''), user_name, entity_version, created_at FROM `changes`"
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
	return items, rows.Err()
}
