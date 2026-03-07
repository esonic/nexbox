package store

import "fmt"

// ---------------------------------------------------------------------------
// UserStore – stub implementations for RemoteStore (real implementation in Phase 1B)
// ---------------------------------------------------------------------------

func (r *RemoteStore) CreateUser(req CreateUserRequest) (*User, error) {
	return nil, fmt.Errorf("CreateUser: not yet implemented")
}

func (r *RemoteStore) GetUser(id string) (*User, error) {
	return nil, fmt.Errorf("GetUser: not yet implemented")
}

func (r *RemoteStore) GetUserByUsername(username string) (*User, error) {
	return nil, fmt.Errorf("GetUserByUsername: not yet implemented")
}

func (r *RemoteStore) ListUsers(limit, offset int) ([]User, error) {
	return nil, fmt.Errorf("ListUsers: not yet implemented")
}

func (r *RemoteStore) UpdateUserFeatures(id string, features int64) error {
	return fmt.Errorf("UpdateUserFeatures: not yet implemented")
}

func (r *RemoteStore) UpdateUserPassword(id string, passwordHash string) error {
	return fmt.Errorf("UpdateUserPassword: not yet implemented")
}

func (r *RemoteStore) UpdateUserRole(id string, role string) error {
	return fmt.Errorf("UpdateUserRole: not yet implemented")
}

// ---------------------------------------------------------------------------
// BalanceStore – stub implementations for RemoteStore (real implementation in Phase 1B)
// ---------------------------------------------------------------------------

func (r *RemoteStore) GetBalance(userID string) (*UserBalance, error) {
	return nil, fmt.Errorf("GetBalance: not yet implemented")
}

func (r *RemoteStore) RechargeBalance(userID string, tokens int64, adminID string) error {
	return fmt.Errorf("RechargeBalance: not yet implemented")
}

func (r *RemoteStore) ReserveTokens(userID string, estimated int64) error {
	return fmt.Errorf("ReserveTokens: not yet implemented")
}

func (r *RemoteStore) SettleTokens(userID string, sessionID string, estimated, actual int64) error {
	return fmt.Errorf("SettleTokens: not yet implemented")
}

func (r *RemoteStore) ListTransactions(userID string, limit, offset int) ([]TokenTransaction, error) {
	return nil, fmt.Errorf("ListTransactions: not yet implemented")
}
