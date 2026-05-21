package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
)

// ErrWebShareNotFound is returned when a share id has no matching row.
var ErrWebShareNotFound = errors.New("web share not found")

// WebShare maps a short public id to a (project_id, relative path) pair.
// File existence is checked at serve time — rows are not eagerly cleaned up
// when the underlying file is deleted (the serve endpoint returns 404).
type WebShare struct {
	ID        string
	ProjectID string
	Path      string
}

// CreateOrGetWebShare returns the existing share id for (projectID, path) if
// one exists, otherwise inserts a new row with a fresh random id and returns
// that. The UNIQUE(project_id, path) constraint guarantees idempotency under
// concurrent callers.
func (s *SQLiteStore) CreateOrGetWebShare(projectID, path string) (string, error) {
	var id string
	err := s.db.QueryRow(
		`SELECT id FROM web_shares WHERE project_id = ? AND path = ?`,
		projectID, path,
	).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("lookup web share: %w", err)
	}

	newID, err := newWebShareID()
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(
		`INSERT INTO web_shares (id, project_id, path) VALUES (?, ?, ?)`,
		newID, projectID, path,
	)
	if err != nil {
		// Lost a race with a concurrent creator — read back the row.
		if isUniqueConstraintErr(err) {
			if err2 := s.db.QueryRow(
				`SELECT id FROM web_shares WHERE project_id = ? AND path = ?`,
				projectID, path,
			).Scan(&id); err2 == nil {
				return id, nil
			}
		}
		return "", fmt.Errorf("insert web share: %w", err)
	}
	return newID, nil
}

// GetWebShare looks up a share by its public id.
func (s *SQLiteStore) GetWebShare(id string) (*WebShare, error) {
	var ws WebShare
	err := s.db.QueryRow(
		`SELECT id, project_id, path FROM web_shares WHERE id = ?`,
		id,
	).Scan(&ws.ID, &ws.ProjectID, &ws.Path)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrWebShareNotFound
		}
		return nil, fmt.Errorf("get web share: %w", err)
	}
	return &ws, nil
}

// newWebShareID returns a 12-character base62 id from crypto/rand.
// 62^12 ≈ 3.2e21 — collision risk is negligible at the scale this serves.
func newWebShareID() (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	const n = 12
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}
