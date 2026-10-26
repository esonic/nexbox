package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// User is a project owner: either the internal passwordless workspace owner
// or a login account created through `nexbox add-user`. There is no self-serve
// registration endpoint.
type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// ErrUserNotFound is returned when GetUserByUsername / GetUserByID find no row.
var ErrUserNotFound = errors.New("user not found")

// ErrUserExists is returned by CreateUser when the username is already taken.
var ErrUserExists = errors.New("user already exists")

// LocalUsername is reserved for the passwordless workspace owner. It is not a
// login account and is never shown by ListUsers.
const LocalUsername = "__local__"

// HasLoginUsers reports whether password authentication is enabled.
func (s *SQLiteStore) HasLoginUsers() (bool, error) {
	var enabled bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE password_hash != '')`).Scan(&enabled)
	return enabled, err
}

// EnsureLocalUser creates the internal workspace owner on first use. Existing
// projects from an account-free installation become part of this workspace.
func (s *SQLiteStore) EnsureLocalUser() (*User, error) {
	if u, err := s.GetUserByUsername(LocalUsername); err == nil {
		return u, nil
	} else if !errors.Is(err, ErrUserNotFound) {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES (?, '', ?) ON CONFLICT(username) DO NOTHING`, LocalUsername, time.Now()); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE projects SET owner_user_id = (SELECT id FROM users WHERE username = ?)
		WHERE NOT EXISTS (SELECT 1 FROM users WHERE password_hash != '')`, LocalUsername); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetUserByUsername(LocalUsername)
}

// CreateUser inserts a new user row. Caller is responsible for hashing the
// password before calling.
func (s *SQLiteStore) CreateUser(username, passwordHash string) (*User, error) {
	if username == LocalUsername || passwordHash == "" {
		return nil, fmt.Errorf("a login account requires a non-reserved username and password")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var first bool
	if err := tx.QueryRow(`SELECT NOT EXISTS(SELECT 1 FROM users WHERE password_hash != '')`).Scan(&first); err != nil {
		return nil, err
	}
	res, err := tx.Exec(
		`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
		username, passwordHash, time.Now(),
	)
	if err != nil {
		// modernc sqlite returns this string for UNIQUE constraint violations.
		if isUniqueConstraintErr(err) {
			return nil, ErrUserExists
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("user last insert id: %w", err)
	}
	if first {
		// Preserve the workspace when enabling login for the first time.
		if _, err := tx.Exec(`UPDATE projects SET owner_user_id = ? WHERE owner_user_id = 0
			OR owner_user_id IN (SELECT id FROM users WHERE username = ? AND password_hash = '')`, id, LocalUsername); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetUserByID(id)
}

// GetUserByUsername returns the user with the given username or ErrUserNotFound.
func (s *SQLiteStore) GetUserByUsername(username string) (*User, error) {
	row := s.db.QueryRow(
		`SELECT id, username, password_hash, created_at FROM users WHERE username = ?`,
		username,
	)
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("get user by username: %w", err)
	}
	return &u, nil
}

// GetUserByID returns the user with the given id or ErrUserNotFound.
func (s *SQLiteStore) GetUserByID(id int64) (*User, error) {
	row := s.db.QueryRow(
		`SELECT id, username, password_hash, created_at FROM users WHERE id = ?`,
		id,
	)
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return &u, nil
}

// ListUsers returns login accounts ordered by id ascending. Used by the
// `nexbox list-users` CLI subcommand.
func (s *SQLiteStore) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT id, username, password_hash, created_at FROM users WHERE password_hash != '' ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser removes the user with the given id. Used by the `nexbox del-user`
// CLI subcommand.
func (s *SQLiteStore) DeleteUser(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM users WHERE id = ? AND password_hash != ''`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUserNotFound
	}
	var remaining bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE password_hash != '')`).Scan(&remaining); err != nil {
		return err
	}
	if !remaining {
		if _, err := tx.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES (?, '', ?) ON CONFLICT(username) DO NOTHING`, LocalUsername, time.Now()); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE projects SET owner_user_id = (SELECT id FROM users WHERE username = ?)`, LocalUsername); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpdateUserPassword resets a user's password hash by username.
func (s *SQLiteStore) UpdateUserPassword(username, passwordHash string) error {
	res, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE username = ? AND password_hash != ''`, passwordHash, username)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// isUniqueConstraintErr detects SQLite UNIQUE constraint violations across the
// modernc.org/sqlite driver. The driver returns errors like
// "constraint failed: UNIQUE constraint failed: users.username (2067)".
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsAny(msg, "UNIQUE constraint failed", "SQLITE_CONSTRAINT_UNIQUE", "(2067)")
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if len(n) > 0 && indexOf(s, n) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	n, m := len(s), len(sub)
	if m == 0 {
		return 0
	}
	for i := 0; i+m <= n; i++ {
		if s[i:i+m] == sub {
			return i
		}
	}
	return -1
}
