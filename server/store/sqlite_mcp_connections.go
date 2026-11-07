package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MCPOAuthConnection is a local-profile connection. Tokens never enter API responses.
type MCPOAuthConnection struct {
	ID, ConfigKey  string
	Generation     string
	Token          MCPOAuthToken
	PendingAuthURL string
}

// This is an installation-lifetime identity, not an OAuth credential. It must
// change after the final uninstall even if every installation field is reused.
func (s *SQLiteStore) EnsureMCPOAuthLifecycle(configKey string) (string, error) {
	_, err := s.db.Exec(`INSERT INTO mcp_oauth_lifecycles (config_key,generation) VALUES (?,?) ON CONFLICT(config_key) DO NOTHING`, configKey, uuid.NewString())
	if err != nil {
		return "", err
	}
	return s.GetMCPOAuthLifecycle(configKey)
}

func (s *SQLiteStore) GetMCPOAuthLifecycle(configKey string) (string, error) {
	var generation string
	err := s.db.QueryRow(`SELECT generation FROM mcp_oauth_lifecycles WHERE config_key = ?`, configKey).Scan(&generation)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return generation, err
}

func (s *SQLiteStore) ListMCPOAuthConnections(configKey string) ([]MCPOAuthConnection, error) {
	rows, err := s.db.Query(`SELECT id, token_json FROM mcp_oauth_connections WHERE config_key = ? ORDER BY id`, configKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []MCPOAuthConnection
	for rows.Next() {
		c := MCPOAuthConnection{ConfigKey: configKey}
		var raw string
		if err := rows.Scan(&c.ID, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &c.Token); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (s *SQLiteStore) SaveMCPOAuthConnection(c MCPOAuthConnection) error {
	c.Token.UpdatedAt = time.Now()
	raw, err := json.Marshal(c.Token)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(`INSERT INTO mcp_oauth_connections (id, config_key, token_json)
		SELECT ?,?,? WHERE EXISTS (SELECT 1 FROM mcp_oauth_lifecycles WHERE config_key = ? AND generation = ?)
		ON CONFLICT(id) DO UPDATE SET token_json=excluded.token_json`, c.ID, c.ConfigKey, string(raw), c.ConfigKey, c.Generation)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("MCP OAuth lifecycle ended")
	}
	return nil
}

// CompleteMCPOAuthConnection commits the shared grant and auth type together.
// A configuration update/uninstall during the network exchange must not attach
// the resulting grant to a different installation snapshot.
func (s *SQLiteStore) CompleteMCPOAuthConnection(c MCPOAuthConnection, installed InstalledMCP) error {
	c.Token.UpdatedAt = time.Now()
	raw, err := json.Marshal(c.Token)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE installed_mcps SET auth_type = 'oauth_discovery' WHERE name = ? AND project_id = ? AND manifest = ? AND env_values = ?
		AND EXISTS (SELECT 1 FROM mcp_oauth_lifecycles WHERE config_key = ? AND generation = ?)`, installed.Name, installed.ProjectID, installed.Manifest, installed.EnvValues, c.ConfigKey, c.Generation)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("MCP installation changed during authorization")
	}
	if _, err := tx.Exec(`INSERT INTO mcp_oauth_connections (id,config_key,token_json) VALUES (?,?,?) ON CONFLICT(id) DO UPDATE SET token_json=excluded.token_json`, c.ID, c.ConfigKey, string(raw)); err != nil {
		return err
	}

	return tx.Commit()
}

// ConsolidateMCPOAuthConnection atomically selects one shared grant and removes
// old copies, including unused credentials from previous project installations.
func (s *SQLiteStore) ConsolidateMCPOAuthConnection(c MCPOAuthConnection, legacy []InstalledMCP) error {
	raw, err := json.Marshal(c.Token)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var generation string
	if err := tx.QueryRow(`SELECT generation FROM mcp_oauth_lifecycles WHERE config_key = ?`, c.ConfigKey).Scan(&generation); err != nil {
		return err
	}
	if generation != c.Generation {
		return fmt.Errorf("MCP OAuth lifecycle ended")
	}
	if _, err := tx.Exec(`DELETE FROM mcp_oauth_connections WHERE config_key = ?`, c.ConfigKey); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO mcp_oauth_connections (id,config_key,token_json) VALUES (?,?,?)`, c.ID, c.ConfigKey, string(raw)); err != nil {
		return err
	}
	for _, scope := range legacy {
		if _, err := tx.Exec(`DELETE FROM mcp_oauth_tokens WHERE mcp_name = ? AND project_id = ?`, scope.Name, scope.ProjectID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) ListMCPOAuthConfigKeys() ([]string, error) {
	rows, err := s.db.Query(`SELECT config_key FROM mcp_oauth_connections UNION SELECT config_key FROM mcp_oauth_pending UNION SELECT config_key FROM mcp_oauth_lifecycles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *SQLiteStore) DeleteOrphanedMCPOAuthTokens() error {
	_, err := s.db.Exec(`DELETE FROM mcp_oauth_tokens WHERE NOT EXISTS (
		SELECT 1 FROM installed_mcps WHERE installed_mcps.name = mcp_oauth_tokens.mcp_name
		AND installed_mcps.project_id = mcp_oauth_tokens.project_id)`)
	return err
}

func (s *SQLiteStore) DeleteMCPOAuthConfig(key string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM mcp_oauth_connections WHERE config_key = ?`, key); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM mcp_oauth_pending WHERE config_key = ?`, key); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM mcp_oauth_lifecycles WHERE config_key = ?`, key); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) SaveMCPOAuthPending(c MCPOAuthConnection) error {
	raw, err := json.Marshal(c.Token)
	if err != nil {
		return err
	}
	// Pending flows expire independently of the active credential.
	if _, err = s.db.Exec(`DELETE FROM mcp_oauth_pending WHERE created_at < ?`, time.Now().Add(-15*time.Minute).Unix()); err != nil {
		return err
	}
	result, err := s.db.Exec(`INSERT INTO mcp_oauth_pending (state, connection_id, config_key, generation, auth_url, created_at, token_json)
		SELECT ?,?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM mcp_oauth_lifecycles WHERE config_key = ? AND generation = ?)`, c.Token.State, c.ID, c.ConfigKey, c.Generation, c.PendingAuthURL, time.Now().Unix(), string(raw), c.ConfigKey, c.Generation)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("MCP OAuth lifecycle ended")
	}
	return nil
}

func (s *SQLiteStore) GetMCPOAuthPendingURL(configKey, connectionID string) (string, error) {
	var authURL string
	err := s.db.QueryRow(`SELECT auth_url FROM mcp_oauth_pending AS p WHERE config_key = ? AND connection_id = ? AND created_at > ?
		AND EXISTS (SELECT 1 FROM mcp_oauth_lifecycles AS l WHERE l.config_key = p.config_key AND l.generation = p.generation)
		ORDER BY created_at DESC LIMIT 1`, configKey, connectionID, time.Now().Add(-15*time.Minute).Unix()).Scan(&authURL)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return authURL, err
}

// Read before acquiring the configuration lock; callers consume state only
// after locking, so uninstall can cancel a callback waiting for that lock.
func (s *SQLiteStore) GetMCPOAuthPending(state string) (*MCPOAuthConnection, error) {
	var c MCPOAuthConnection
	var raw string
	err := s.db.QueryRow(`SELECT connection_id,config_key,generation,token_json FROM mcp_oauth_pending WHERE state = ?`, state).Scan(&c.ID, &c.ConfigKey, &c.Generation, &raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(raw), &c.Token); err != nil {
		return nil, err
	}
	if time.Since(c.Token.UpdatedAt) > 15*time.Minute {
		return nil, nil
	}
	return &c, nil
}

// TakeMCPOAuthPending makes callbacks single-use, including failed exchanges.
func (s *SQLiteStore) TakeMCPOAuthPending(state string) (*MCPOAuthConnection, error) {
	var c MCPOAuthConnection
	var raw string
	err := s.db.QueryRow(`DELETE FROM mcp_oauth_pending WHERE state = ? RETURNING connection_id, config_key, generation, token_json`, state).Scan(&c.ID, &c.ConfigKey, &c.Generation, &raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(raw), &c.Token); err != nil {
		return nil, err
	}
	if time.Since(c.Token.UpdatedAt) > 15*time.Minute {
		return nil, nil
	}
	return &c, nil
}
