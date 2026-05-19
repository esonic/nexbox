package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// User is a local account that can log in to the nexbox UI. Accounts are
// created exclusively via the `nexbox add-user` CLI; there is no self-serve
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

// CreateUser inserts a new user row. Caller is responsible for hashing the
// password before calling.
func (s *SQLiteStore) CreateUser(username, passwordHash string) (*User, error) {
	res, err := s.db.Exec(
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

// ListUsers returns all users ordered by id ascending. Used by the
// `nexbox list-users` CLI subcommand.
func (s *SQLiteStore) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT id, username, password_hash, created_at FROM users ORDER BY id ASC`)
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
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// UpdateUserPassword resets a user's password hash by username.
func (s *SQLiteStore) UpdateUserPassword(username, passwordHash string) error {
	res, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE username = ?`, passwordHash, username)
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
