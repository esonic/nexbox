package store

import (
	"database/sql"
	"fmt"
)

var _ Store = (*SQLiteStore)(nil)

// SQLiteStore implements the Store interface using SQLite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore creates a new SQLiteStore with the given database connection.
func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db}
}

// DB exposes the underlying *sql.DB for use by non-Store code (e.g. MCP helpers).
func (s *SQLiteStore) DB() *sql.DB {
	return s.db
}

// Maintain performs periodic SQLite maintenance: WAL checkpoint and VACUUM.
func (s *SQLiteStore) Maintain() error {
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("wal_checkpoint: %w", err)
	}
	if _, err := s.db.Exec(`VACUUM`); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	return nil
}

// GetTaskInfo returns minimal task data for agent context building.
func (s *SQLiteStore) GetTaskInfo(id string) (*TaskInfo, error) {
	var t TaskInfo
	err := s.db.QueryRow(
		`SELECT id, title, COALESCE(description,''), status, priority FROM tasks WHERE id = ?`, id,
	).Scan(&t.ID, &t.Title, &t.Description, &t.Status, &t.Priority)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetAssetInfo returns minimal asset data for agent context building.
func (s *SQLiteStore) GetAssetInfo(id string) (*AssetInfo, error) {
	var a AssetInfo
	err := s.db.QueryRow(
		`SELECT id, type, title, status FROM assets WHERE id = ?`, id,
	).Scan(&a.ID, &a.Type, &a.Title, &a.Status)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// GetDesignDocSummary returns a truncated summary of the project's design document.
func (s *SQLiteStore) GetDesignDocSummary(projectID string) string {
	var content string
	err := s.db.QueryRow(`
		SELECT av.content
		FROM assets a
		JOIN asset_versions av ON av.asset_id = a.id AND av.version = a.current_version
		WHERE a.project_id = ? AND a.type = 'design_doc'
		ORDER BY a.updated_at DESC
		LIMIT 1
	`, projectID).Scan(&content)
	if err != nil {
		return ""
	}
	runes := []rune(content)
	if len(runes) > 100 {
		return string(runes[:100]) + "..."
	}
	return content
}
