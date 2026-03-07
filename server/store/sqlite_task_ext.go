package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Task Comments
// ---------------------------------------------------------------------------

func (s *SQLiteStore) ListTaskComments(taskID string) ([]TaskComment, error) {
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
	return items, nil
}

func (s *SQLiteStore) CreateTaskComment(taskID string, req CreateTaskCommentRequest, author string) (*TaskComment, error) {
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

func (s *SQLiteStore) UpdateTaskComment(id string, req UpdateTaskCommentRequest) (*TaskComment, error) {
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

func (s *SQLiteStore) DeleteTaskComment(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var taskID string
	if err := tx.QueryRow(`SELECT task_id FROM task_comments WHERE id = ?`, id).Scan(&taskID); err != nil {
		if err == sql.ErrNoRows {
			return nil // already deleted
		}
		return fmt.Errorf("lookup task comment: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM reactions WHERE target_type = 'task_comment' AND target_id = ?`, id); err != nil {
		return fmt.Errorf("delete comment reactions: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM task_comments WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete task comment: %w", err)
	}
	if _, err := tx.Exec(`UPDATE tasks SET comment_count = MAX(0, comment_count - 1) WHERE id = ?`, taskID); err != nil {
		return fmt.Errorf("decrement comment_count: %w", err)
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Labels
// ---------------------------------------------------------------------------

func (s *SQLiteStore) ListLabels(_ string) ([]Label, error) {
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
	return items, nil
}

func (s *SQLiteStore) CreateLabel(_ string, req CreateLabelRequest) (*Label, error) {
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

func (s *SQLiteStore) DeleteLabel(_ string, id string) error {
	_, err := s.db.Exec(`DELETE FROM labels WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) AddLabelToTask(taskID, labelName string) error {
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

func (s *SQLiteStore) RemoveLabelFromTask(taskID, labelName string) error {
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

func (s *SQLiteStore) GetReactionSummary(targetType, targetID, currentUser string) ([]ReactionSummary, error) {
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
	return items, nil
}

func (s *SQLiteStore) AddReaction(targetType, targetID, author, emoji string) error {
	id := uuid.New().String()
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO reactions (id, target_type, target_id, author, emoji, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, targetType, targetID, author, emoji, time.Now(),
	)
	return err
}

func (s *SQLiteStore) RemoveReaction(targetType, targetID, author, emoji string) error {
	_, err := s.db.Exec(
		`DELETE FROM reactions WHERE target_type = ? AND target_id = ? AND author = ? AND emoji = ?`,
		targetType, targetID, author, emoji,
	)
	return err
}
