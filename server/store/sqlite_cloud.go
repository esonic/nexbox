package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// UserStore – SQLite implementation
// ---------------------------------------------------------------------------

func (s *SQLiteStore) CreateUser(req CreateUserRequest) (*User, error) {
	now := time.Now()
	role := req.Role
	if role == "" {
		role = "user"
	}
	u := &User{
		ID:           uuid.New().String(),
		Username:     req.Username,
		PasswordHash: req.Password, // caller passes already-hashed value
		Email:        req.Email,
		Role:         role,
		Features:     0,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	_, err := s.db.Exec(
		`INSERT INTO users (id, username, password_hash, email, role, features, created_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, u.Email, u.Role, u.Features, u.CreatedAt, u.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	// Create an empty balance row for the new user.
	_, err = s.db.Exec(
		`INSERT INTO user_balances (user_id, balance_tokens, reserved_tokens, total_recharged, updated_at)
         VALUES (?, 0, 0, 0, ?)`,
		u.ID, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create user balance: %w", err)
	}
	return u, nil
}

func scanUser(row interface{ Scan(...interface{}) error }) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Email, &u.Role, &u.Features, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *SQLiteStore) GetUser(id string) (*User, error) {
	row := s.db.QueryRow(
		`SELECT id, username, password_hash, email, role, features, created_at, updated_at FROM users WHERE id = ?`, id,
	)
	u, err := scanUser(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

func (s *SQLiteStore) GetUserByUsername(username string) (*User, error) {
	row := s.db.QueryRow(
		`SELECT id, username, password_hash, email, role, features, created_at, updated_at FROM users WHERE username = ?`, username,
	)
	u, err := scanUser(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

func (s *SQLiteStore) ListUsers(limit, offset int) ([]User, error) {
	rows, err := s.db.Query(
		`SELECT id, username, password_hash, email, role, features, created_at, updated_at FROM users ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	return users, rows.Err()
}

func (s *SQLiteStore) UpdateUserFeatures(id string, features int64) error {
	_, err := s.db.Exec(
		`UPDATE users SET features = ?, updated_at = ? WHERE id = ?`,
		features, time.Now(), id,
	)
	return err
}

func (s *SQLiteStore) UpdateUserPassword(id string, passwordHash string) error {
	_, err := s.db.Exec(
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, time.Now(), id,
	)
	return err
}

// ---------------------------------------------------------------------------
// BalanceStore – SQLite implementation
// ---------------------------------------------------------------------------

func (s *SQLiteStore) GetBalance(userID string) (*UserBalance, error) {
	var b UserBalance
	err := s.db.QueryRow(
		`SELECT user_id, balance_tokens, reserved_tokens, total_recharged, updated_at FROM user_balances WHERE user_id = ?`,
		userID,
	).Scan(&b.UserID, &b.BalanceTokens, &b.ReservedTokens, &b.TotalRecharged, &b.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get balance: %w", err)
	}
	return &b, nil
}

func (s *SQLiteStore) RechargeBalance(userID string, tokens int64, adminID string) error {
	now := time.Now()
	// Upsert balance row (in case it was somehow missing).
	_, err := s.db.Exec(`
        INSERT INTO user_balances (user_id, balance_tokens, reserved_tokens, total_recharged, updated_at)
        VALUES (?, ?, 0, ?, ?)
        ON CONFLICT(user_id) DO UPDATE SET
            balance_tokens  = balance_tokens + excluded.balance_tokens,
            total_recharged = total_recharged + excluded.balance_tokens,
            updated_at      = excluded.updated_at`,
		userID, tokens, tokens, now,
	)
	if err != nil {
		return fmt.Errorf("recharge balance: %w", err)
	}
	// Write transaction record.
	_, err = s.db.Exec(
		`INSERT INTO token_transactions (id, user_id, session_id, delta, reason, created_at) VALUES (?, ?, '', ?, 'recharge', ?)`,
		uuid.New().String(), userID, tokens, now,
	)
	return err
}

func (s *SQLiteStore) ReserveTokens(userID string, estimated int64) error {
	res, err := s.db.Exec(`
        UPDATE user_balances
        SET balance_tokens  = balance_tokens - ?,
            reserved_tokens = reserved_tokens + ?,
            updated_at      = ?
        WHERE user_id = ? AND balance_tokens >= ?`,
		estimated, estimated, time.Now(), userID, estimated,
	)
	if err != nil {
		return fmt.Errorf("reserve tokens: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("insufficient balance")
	}
	return nil
}

func (s *SQLiteStore) SettleTokens(userID string, sessionID string, estimated, actual int64) error {
	// diff > 0: used more than reserved (deduct extra from balance)
	// diff < 0: used less than reserved (return surplus to balance)
	diff := actual - estimated
	now := time.Now()
	_, err := s.db.Exec(`
        UPDATE user_balances
        SET balance_tokens  = balance_tokens - ?,
            reserved_tokens = reserved_tokens - ?,
            updated_at      = ?
        WHERE user_id = ?`,
		diff, estimated, now, userID,
	)
	if err != nil {
		return fmt.Errorf("settle tokens: %w", err)
	}
	// Record actual consumption (negative delta = consumed).
	_, err = s.db.Exec(
		`INSERT INTO token_transactions (id, user_id, session_id, delta, reason, created_at) VALUES (?, ?, ?, ?, 'settle', ?)`,
		uuid.New().String(), userID, sessionID, -actual, now,
	)
	return err
}

func (s *SQLiteStore) ListTransactions(userID string, limit, offset int) ([]TokenTransaction, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, session_id, delta, reason, created_at FROM token_transactions WHERE user_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	defer rows.Close()
	var txs []TokenTransaction
	for rows.Next() {
		var tx TokenTransaction
		if err := rows.Scan(&tx.ID, &tx.UserID, &tx.SessionID, &tx.Delta, &tx.Reason, &tx.CreatedAt); err != nil {
			return nil, err
		}
		txs = append(txs, tx)
	}
	return txs, rows.Err()
}
