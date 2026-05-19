package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// UnixTime wraps time.Time to serialize as a Unix timestamp (int64) in JSON,
// matching frontend expectations (number instead of RFC3339 string).
type UnixTime time.Time

func (t UnixTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).Unix())
}

func (t *UnixTime) UnmarshalJSON(data []byte) error {
	var ts int64
	if err := json.Unmarshal(data, &ts); err != nil {
		// Try parsing as RFC3339 string for backwards compat.
		var s string
		if err2 := json.Unmarshal(data, &s); err2 != nil {
			return fmt.Errorf("UnixTime: expected number or string, got %s", string(data))
		}
		parsed, err3 := time.Parse(time.RFC3339, s)
		if err3 != nil {
			return err3
		}
		*t = UnixTime(parsed)
		return nil
	}
	*t = UnixTime(time.Unix(ts, 0))
	return nil
}

func (t UnixTime) Time() time.Time { return time.Time(t) }

// InstalledMCP holds the runtime config for a locally installed MCP package.
type InstalledMCP struct {
	Name        string    `json:"name"`         // human-readable package name (PK component)
	ProjectID   string    `json:"project_id"`   // project scope: "" = global (all sessions), else project-specific
	PackageID   string    `json:"package_id"`   // remote marketplace package ID (for update checks)
	Version     string    `json:"version"`      // installed version string
	MCPKey      string    `json:"mcp_key"`      // server key used in mcpServers config
	Manifest    string    `json:"manifest"`     // JSON: MCPServerEntry (key, type, cmd/url, args, env, tool_names)
	EnvValues   string    `json:"env_values"`   // JSON: user-provided env secrets {"KEY": "value"} merged at runtime
	AuthType    string   `json:"auth_type"`    // "none", "apikey", "oauth_discovery", "oauth"
	InstalledAt UnixTime `json:"installed_at"`
}

// InstalledSkill holds install info for a locally installed skill package.
type InstalledSkill struct {
	Name        string   `json:"name"`         // package name (primary key component)
	ProjectID   string   `json:"project_id"`   // project ID, or "" for global
	Version     string   `json:"version"`
	InstallPath string   `json:"install_path"` // absolute path to the extracted skill directory
	InstalledAt UnixTime `json:"installed_at"`
	Missing     bool     `json:"missing"`      // true if install_path no longer exists on disk
}

// InstalledPluginEntry holds install info for a locally installed plugin directory (Claude --plugin-dir).
type InstalledPluginEntry struct {
	Name        string   `json:"name"`
	ProjectID   string   `json:"project_id"`
	InstallPath string   `json:"install_path"` // absolute path to the plugin directory
	InstalledAt UnixTime `json:"installed_at"`
}

// MCPOAuthToken stores OAuth tokens for MCP connectors using oauth_discovery or oauth auth types.
type MCPOAuthToken struct {
	MCPName       string    `json:"mcp_name"`
	ProjectID     string    `json:"project_id"`
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token"`
	ExpiresAt     time.Time `json:"expires_at"`      // zero = no expiry
	TokenEndpoint string    `json:"token_endpoint"`  // for refresh
	ClientID      string    `json:"client_id"`
	State         string    `json:"state"`           // transient: random state for OAuth flow
	CodeVerifier  string    `json:"code_verifier"`   // transient: PKCE verifier
	UpdatedAt     time.Time `json:"updated_at"`
}

// MarketplaceSource records a user-added marketplace directory or git repo.
type MarketplaceSource struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"` // "local" or "git"
	URL     string   `json:"url"`  // git URL (for git type)
	Path    string   `json:"path"` // absolute path on disk
	Name    string   `json:"name"` // display name
	AddedAt UnixTime `json:"added_at"`
}

// ── MCP ──────────────────────────────────────────────────────────────────────

func (s *SQLiteStore) UpsertInstalledMCP(mcp InstalledMCP) error {
	ts := mcp.InstalledAt.Time().Unix()
	if ts == 0 {
		ts = time.Now().Unix()
	}
	envValues := mcp.EnvValues
	if envValues == "" {
		envValues = "{}"
	}
	authType := mcp.AuthType
	if authType == "" {
		authType = "none"
	}
	_, err := s.db.Exec(
		`INSERT INTO installed_mcps (name, project_id, package_id, version, mcp_key, manifest, env_values, auth_type, installed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name, project_id) DO UPDATE SET package_id=excluded.package_id, version=excluded.version, mcp_key=excluded.mcp_key, manifest=excluded.manifest, env_values=excluded.env_values, auth_type=excluded.auth_type, installed_at=excluded.installed_at`,
		mcp.Name, mcp.ProjectID, mcp.PackageID, mcp.Version, mcp.MCPKey, mcp.Manifest, envValues, authType, ts,
	)
	return err
}

func (s *SQLiteStore) DeleteInstalledMCP(name, projectID string) error {
	_, err := s.db.Exec(`DELETE FROM installed_mcps WHERE name = ? AND project_id = ?`, name, projectID)
	return err
}

func (s *SQLiteStore) DeleteInstalledMCPByName(name string) error {
	_, err := s.db.Exec(`DELETE FROM installed_mcps WHERE name = ?`, name)
	return err
}

// UpdateInstalledMCPAuthType updates only the auth_type field for an installed MCP.
// Used by the proxy when it auto-discovers OAuth support after a 401 response.
func (s *SQLiteStore) UpdateInstalledMCPAuthType(name, projectID, authType string) error {
	_, err := s.db.Exec(`UPDATE installed_mcps SET auth_type = ? WHERE name = ? AND project_id = ?`,
		authType, name, projectID)
	return err
}

func (s *SQLiteStore) GetInstalledMCP(name, projectID string) (*InstalledMCP, error) {
	row := s.db.QueryRow(
		`SELECT name, project_id, package_id, version, mcp_key, manifest, env_values, COALESCE(auth_type, 'none'), installed_at FROM installed_mcps WHERE name = ? AND project_id = ?`,
		name, projectID)
	return scanInstalledMCP(row)
}

// ListInstalledMCPs returns installed MCPs.
//   - projectID = "*"  → all records (global + all project-specific)
//   - projectID = ""   → global only (project_id='')
//   - projectID = "<id>" → global + project-specific for that project
func (s *SQLiteStore) ListInstalledMCPs(projectID string) ([]InstalledMCP, error) {
	var (
		rows *sql.Rows
		err  error
	)
	q := `SELECT name, project_id, package_id, version, mcp_key, manifest, env_values, COALESCE(auth_type, 'none'), installed_at FROM installed_mcps`
	switch projectID {
	case "*":
		rows, err = s.db.Query(q + ` ORDER BY name, project_id`)
	case "":
		rows, err = s.db.Query(q+` WHERE project_id = '' ORDER BY name`)
	default:
		rows, err = s.db.Query(q+` WHERE project_id IN ('', ?) ORDER BY name, project_id`, projectID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InstalledMCP{}
	for rows.Next() {
		m, err := scanInstalledMCP(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

type mcpScanner interface {
	Scan(dest ...any) error
}

func scanInstalledMCP(s mcpScanner) (*InstalledMCP, error) {
	var m InstalledMCP
	var ts int64
	if err := s.Scan(&m.Name, &m.ProjectID, &m.PackageID, &m.Version, &m.MCPKey, &m.Manifest, &m.EnvValues, &m.AuthType, &ts); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if m.EnvValues == "" {
		m.EnvValues = "{}"
	}
	if m.AuthType == "" {
		m.AuthType = "none"
	}
	m.InstalledAt = UnixTime(time.Unix(ts, 0))
	return &m, nil
}

// ── Skills ───────────────────────────────────────────────────────────────────

func (s *SQLiteStore) UpsertInstalledSkill(skill InstalledSkill) error {
	ts := skill.InstalledAt.Time().Unix()
	if ts == 0 {
		ts = time.Now().Unix()
	}
	_, err := s.db.Exec(
		`INSERT INTO installed_skills (name, project_id, version, install_path, installed_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(name, project_id) DO UPDATE SET version=excluded.version, install_path=excluded.install_path, installed_at=excluded.installed_at`,
		skill.Name, skill.ProjectID, skill.Version, skill.InstallPath, ts,
	)
	return err
}

func (s *SQLiteStore) DeleteInstalledSkill(name, projectID string) error {
	_, err := s.db.Exec(`DELETE FROM installed_skills WHERE name = ? AND project_id = ?`, name, projectID)
	return err
}

func (s *SQLiteStore) DeleteInstalledSkillByName(name string) error {
	_, err := s.db.Exec(`DELETE FROM installed_skills WHERE name = ?`, name)
	return err
}

func (s *SQLiteStore) GetInstalledSkill(name, projectID string) (*InstalledSkill, error) {
	row := s.db.QueryRow(
		`SELECT name, project_id, version, install_path, installed_at FROM installed_skills WHERE name = ? AND project_id = ?`,
		name, projectID)
	return scanInstalledSkill(row)
}

// ListInstalledSkills returns installed skills.
func (s *SQLiteStore) ListInstalledSkills(projectID string) ([]InstalledSkill, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if projectID == "*" {
		rows, err = s.db.Query(
			`SELECT name, project_id, version, install_path, installed_at FROM installed_skills ORDER BY name`)
	} else if projectID == "" {
		rows, err = s.db.Query(
			`SELECT name, project_id, version, install_path, installed_at FROM installed_skills WHERE project_id = '' ORDER BY name`)
	} else {
		rows, err = s.db.Query(
			`SELECT name, project_id, version, install_path, installed_at FROM installed_skills WHERE project_id = '' OR project_id = ? ORDER BY name`,
			projectID)
	}
	if err != nil {
		return nil, err
	}
	out := []InstalledSkill{}
	for rows.Next() {
		sk, err := scanInstalledSkill(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *sk)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return nil, rowsErr
	}
	for i := range out {
		if out[i].InstallPath != "" {
			if _, statErr := os.Stat(out[i].InstallPath); os.IsNotExist(statErr) {
				out[i].Missing = true
			}
		}
	}
	return out, nil
}

type skillScanner interface {
	Scan(dest ...any) error
}

func scanInstalledSkill(s skillScanner) (*InstalledSkill, error) {
	var sk InstalledSkill
	var ts int64
	if err := s.Scan(&sk.Name, &sk.ProjectID, &sk.Version, &sk.InstallPath, &ts); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	sk.InstalledAt = UnixTime(time.Unix(ts, 0))
	return &sk, nil
}

// ── Plugins ──────────────────────────────────────────────────────────────────

func (s *SQLiteStore) UpsertInstalledPluginEntry(p InstalledPluginEntry) error {
	ts := p.InstalledAt.Time().Unix()
	if ts == 0 {
		ts = time.Now().Unix()
	}
	_, err := s.db.Exec(
		`INSERT INTO installed_plugins (name, project_id, install_path, installed_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(name, project_id) DO UPDATE SET install_path=excluded.install_path, installed_at=excluded.installed_at`,
		p.Name, p.ProjectID, p.InstallPath, ts,
	)
	return err
}

func (s *SQLiteStore) DeleteInstalledPluginEntry(name, projectID string) error {
	_, err := s.db.Exec(`DELETE FROM installed_plugins WHERE name = ? AND project_id = ?`, name, projectID)
	return err
}

func (s *SQLiteStore) DeleteInstalledPluginEntryByName(name string) error {
	_, err := s.db.Exec(`DELETE FROM installed_plugins WHERE name = ?`, name)
	return err
}

func (s *SQLiteStore) ListInstalledPluginEntries(projectID string) ([]InstalledPluginEntry, error) {
	var (
		rows *sql.Rows
		err  error
	)
	q := `SELECT name, project_id, install_path, installed_at FROM installed_plugins`
	switch projectID {
	case "*":
		rows, err = s.db.Query(q + ` ORDER BY name, project_id`)
	case "":
		rows, err = s.db.Query(q + ` WHERE project_id = '' ORDER BY name`)
	default:
		rows, err = s.db.Query(q+` WHERE project_id IN ('', ?) ORDER BY name, project_id`, projectID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InstalledPluginEntry{}
	for rows.Next() {
		var p InstalledPluginEntry
		var ts int64
		if err := rows.Scan(&p.Name, &p.ProjectID, &p.InstallPath, &ts); err != nil {
			return nil, err
		}
		p.InstalledAt = UnixTime(time.Unix(ts, 0))
		out = append(out, p)
	}
	return out, rows.Err()
}

// ── MCP OAuth Tokens ─────────────────────────────────────────────────────────

func (s *SQLiteStore) UpsertMCPOAuthToken(tok MCPOAuthToken) error {
	expiresAt := int64(0)
	if !tok.ExpiresAt.IsZero() {
		expiresAt = tok.ExpiresAt.Unix()
	}
	updatedAt := tok.UpdatedAt.Unix()
	if updatedAt == 0 {
		updatedAt = time.Now().Unix()
	}
	_, err := s.db.Exec(
		`INSERT INTO mcp_oauth_tokens (mcp_name, project_id, access_token, refresh_token, expires_at, token_endpoint, client_id, state, code_verifier, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(mcp_name, project_id) DO UPDATE SET
		   access_token=excluded.access_token, refresh_token=excluded.refresh_token,
		   expires_at=excluded.expires_at, token_endpoint=excluded.token_endpoint,
		   client_id=excluded.client_id, state=excluded.state,
		   code_verifier=excluded.code_verifier, updated_at=excluded.updated_at`,
		tok.MCPName, tok.ProjectID, tok.AccessToken, tok.RefreshToken, expiresAt,
		tok.TokenEndpoint, tok.ClientID, tok.State, tok.CodeVerifier, updatedAt,
	)
	return err
}

func (s *SQLiteStore) GetMCPOAuthToken(mcpName, projectID string) (*MCPOAuthToken, error) {
	row := s.db.QueryRow(
		`SELECT mcp_name, project_id, access_token, refresh_token, expires_at, token_endpoint, client_id, state, code_verifier, updated_at
		 FROM mcp_oauth_tokens WHERE mcp_name = ? AND project_id = ?`, mcpName, projectID)
	return scanMCPOAuthToken(row)
}

func (s *SQLiteStore) GetMCPOAuthTokenByState(state string) (*MCPOAuthToken, error) {
	row := s.db.QueryRow(
		`SELECT mcp_name, project_id, access_token, refresh_token, expires_at, token_endpoint, client_id, state, code_verifier, updated_at
		 FROM mcp_oauth_tokens WHERE state = ?`, state)
	return scanMCPOAuthToken(row)
}

func (s *SQLiteStore) DeleteMCPOAuthToken(mcpName, projectID string) error {
	_, err := s.db.Exec(`DELETE FROM mcp_oauth_tokens WHERE mcp_name = ? AND project_id = ?`, mcpName, projectID)
	return err
}

func (s *SQLiteStore) DeleteMCPOAuthTokenByName(mcpName string) error {
	_, err := s.db.Exec(`DELETE FROM mcp_oauth_tokens WHERE mcp_name = ?`, mcpName)
	return err
}

type oauthScanner interface {
	Scan(dest ...any) error
}

func scanMCPOAuthToken(s oauthScanner) (*MCPOAuthToken, error) {
	var tok MCPOAuthToken
	var expiresAt, updatedAt int64
	if err := s.Scan(&tok.MCPName, &tok.ProjectID, &tok.AccessToken, &tok.RefreshToken,
		&expiresAt, &tok.TokenEndpoint, &tok.ClientID, &tok.State, &tok.CodeVerifier, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if expiresAt > 0 {
		tok.ExpiresAt = time.Unix(expiresAt, 0)
	}
	tok.UpdatedAt = time.Unix(updatedAt, 0)
	return &tok, nil
}

// ── Marketplace Sources ──────────────────────────────────────────────────────

func (s *SQLiteStore) UpsertMarketplaceSource(src MarketplaceSource) error {
	ts := src.AddedAt.Time().Unix()
	if ts == 0 {
		ts = time.Now().Unix()
	}
	_, err := s.db.Exec(
		`INSERT INTO marketplace_sources (id, type, url, path, name, added_at) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET type=excluded.type, url=excluded.url, path=excluded.path, name=excluded.name, added_at=excluded.added_at`,
		src.ID, src.Type, src.URL, src.Path, src.Name, ts,
	)
	return err
}

func (s *SQLiteStore) ListMarketplaceSources() ([]MarketplaceSource, error) {
	rows, err := s.db.Query(`SELECT id, type, url, path, name, added_at FROM marketplace_sources ORDER BY added_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MarketplaceSource{}
	for rows.Next() {
		var src MarketplaceSource
		var ts int64
		if err := rows.Scan(&src.ID, &src.Type, &src.URL, &src.Path, &src.Name, &ts); err != nil {
			return nil, err
		}
		src.AddedAt = UnixTime(time.Unix(ts, 0))
		out = append(out, src)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetMarketplaceSource(id string) (*MarketplaceSource, error) {
	row := s.db.QueryRow(`SELECT id, type, url, path, name, added_at FROM marketplace_sources WHERE id = ?`, id)
	var src MarketplaceSource
	var ts int64
	if err := row.Scan(&src.ID, &src.Type, &src.URL, &src.Path, &src.Name, &ts); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	src.AddedAt = UnixTime(time.Unix(ts, 0))
	return &src, nil
}

func (s *SQLiteStore) DeleteMarketplaceSource(id string) error {
	_, err := s.db.Exec(`DELETE FROM marketplace_sources WHERE id = ?`, id)
	return err
}
