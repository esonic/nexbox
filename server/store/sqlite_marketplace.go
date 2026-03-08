package store

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
)

// InstalledMCP holds the runtime config for a locally installed MCP package.
type InstalledMCP struct {
	Name        string    `json:"name"`         // human-readable package name (PK component)
	ProjectID   string    `json:"project_id"`   // project scope: "" = global (all sessions), else project-specific
	PackageID   string    `json:"package_id"`   // remote marketplace package ID (for update checks)
	Version     string    `json:"version"`      // installed version string
	MCPKey      string    `json:"mcp_key"`      // server key used in mcpServers config
	Manifest    string    `json:"manifest"`     // JSON: MCPServerEntry (key, type, cmd/url, args, env, tool_names)
	EnvValues   string    `json:"env_values"`   // JSON: user-provided env secrets {"KEY": "value"} merged at runtime
	InstalledAt time.Time `json:"installed_at"`
}

// InstalledSkill holds install info for a locally installed skill package.
type InstalledSkill struct {
	Name        string    `json:"name"`         // package name (primary key component)
	ProjectID   string    `json:"project_id"`   // project ID, or "" for global
	Version     string    `json:"version"`
	InstallPath string    `json:"install_path"` // absolute path to the extracted skill directory
	InstalledAt time.Time `json:"installed_at"`
	Missing     bool      `json:"missing"`      // true if install_path no longer exists on disk
}

// ── MCP ──────────────────────────────────────────────────────────────────────

func (s *SQLiteStore) UpsertInstalledMCP(mcp InstalledMCP) error {
	ts := mcp.InstalledAt.Unix()
	if ts == 0 {
		ts = time.Now().Unix()
	}
	envValues := mcp.EnvValues
	if envValues == "" {
		envValues = "{}"
	}
	_, err := s.db.Exec(
		`INSERT INTO installed_mcps (name, project_id, package_id, version, mcp_key, manifest, env_values, installed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name, project_id) DO UPDATE SET package_id=excluded.package_id, version=excluded.version, mcp_key=excluded.mcp_key, manifest=excluded.manifest, env_values=excluded.env_values, installed_at=excluded.installed_at`,
		mcp.Name, mcp.ProjectID, mcp.PackageID, mcp.Version, mcp.MCPKey, mcp.Manifest, envValues, ts,
	)
	return err
}

func (s *SQLiteStore) DeleteInstalledMCP(name, projectID string) error {
	_, err := s.db.Exec(`DELETE FROM installed_mcps WHERE name = ? AND project_id = ?`, name, projectID)
	return err
}

func (s *SQLiteStore) GetInstalledMCP(name, projectID string) (*InstalledMCP, error) {
	row := s.db.QueryRow(
		`SELECT name, project_id, package_id, version, mcp_key, manifest, env_values, installed_at FROM installed_mcps WHERE name = ? AND project_id = ?`,
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
	switch projectID {
	case "*":
		rows, err = s.db.Query(
			`SELECT name, project_id, package_id, version, mcp_key, manifest, env_values, installed_at FROM installed_mcps ORDER BY name, project_id`)
	case "":
		rows, err = s.db.Query(
			`SELECT name, project_id, package_id, version, mcp_key, manifest, env_values, installed_at FROM installed_mcps WHERE project_id = '' ORDER BY name`)
	default:
		rows, err = s.db.Query(
			`SELECT name, project_id, package_id, version, mcp_key, manifest, env_values, installed_at FROM installed_mcps WHERE project_id IN ('', ?) ORDER BY name, project_id`,
			projectID)
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
	if err := s.Scan(&m.Name, &m.ProjectID, &m.PackageID, &m.Version, &m.MCPKey, &m.Manifest, &m.EnvValues, &ts); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if m.EnvValues == "" {
		m.EnvValues = "{}"
	}
	m.InstalledAt = time.Unix(ts, 0)
	return &m, nil
}

// ── Skills ───────────────────────────────────────────────────────────────────

func (s *SQLiteStore) UpsertInstalledSkill(skill InstalledSkill) error {
	ts := skill.InstalledAt.Unix()
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

func (s *SQLiteStore) GetInstalledSkill(name, projectID string) (*InstalledSkill, error) {
	row := s.db.QueryRow(
		`SELECT name, project_id, version, install_path, installed_at FROM installed_skills WHERE name = ? AND project_id = ?`,
		name, projectID)
	return scanInstalledSkill(row)
}

// ListInstalledSkills returns installed skills.
//   - projectID="*"  → all records (global + all project-specific)
//   - projectID=""   → global only (project_id='')
//   - projectID="<id>" → global + project-specific for that project (mirrors ListInstalledMCPs behaviour)
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
	// Collect all rows first, then release the SQLite connection before doing
	// filesystem I/O. os.Stat on a stale network mount can block for minutes,
	// and holding the single SQLite connection during that time freezes every
	// other goroutine that needs the DB (e.g. ListSessions → /api/sessions).
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
	rows.Close() // release connection before filesystem I/O
	if rowsErr != nil {
		return nil, rowsErr
	}
	// Check if install directories still exist on disk (after releasing SQLite connection).
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
	sk.InstalledAt = time.Unix(ts, 0)
	return &sk, nil
}

// ── Installed Plugins (project-scoped, for --plugin-dir injection) ─────────

// InstalledPluginEntry mirrors InstalledSkill but for plugins: name + project scope + disk path.
type InstalledPluginEntry struct {
	Name        string    `json:"name"`
	ProjectID   string    `json:"project_id"`
	InstallPath string    `json:"install_path"` // absolute path to the plugin directory (in marketplace_dir)
	InstalledAt time.Time `json:"installed_at"`
}

func (s *SQLiteStore) UpsertInstalledPluginEntry(p InstalledPluginEntry) error {
	ts := p.InstalledAt.Unix()
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

// ListInstalledPluginEntries returns installed plugin entries for a project.
//   - projectID="*"    → all
//   - projectID=""     → global only
//   - projectID="<id>" → global + project-specific
func (s *SQLiteStore) ListInstalledPluginEntries(projectID string) ([]InstalledPluginEntry, error) {
	var (
		rows *sql.Rows
		err  error
	)
	switch projectID {
	case "*":
		rows, err = s.db.Query(
			`SELECT name, project_id, install_path, installed_at FROM installed_plugins ORDER BY name`)
	case "":
		rows, err = s.db.Query(
			`SELECT name, project_id, install_path, installed_at FROM installed_plugins WHERE project_id = '' ORDER BY name`)
	default:
		rows, err = s.db.Query(
			`SELECT name, project_id, install_path, installed_at FROM installed_plugins WHERE project_id IN ('', ?) ORDER BY name`,
			projectID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InstalledPluginEntry
	for rows.Next() {
		var p InstalledPluginEntry
		var ts int64
		if err := rows.Scan(&p.Name, &p.ProjectID, &p.InstallPath, &ts); err != nil {
			return nil, err
		}
		p.InstalledAt = time.Unix(ts, 0)
		out = append(out, p)
	}
	if out == nil {
		out = []InstalledPluginEntry{}
	}
	return out, rows.Err()
}

// ── Legacy Installed Plugins (user-centric, kept for backward compatibility) ──

func (s *SQLiteStore) ListInstalledPlugins(userID string) ([]InstalledPlugin, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, plugin_id, config, installed_at FROM installed_plugins WHERE user_id = ? ORDER BY installed_at DESC`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InstalledPlugin{}
	for rows.Next() {
		p, err := scanInstalledPlugin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) InstallPlugin(userID, pluginID, config string) (*InstalledPlugin, error) {
	if config == "" {
		config = "{}"
	}
	ts := time.Now().Unix()
	// Use user_id+plugin_id as the logical primary key, stored in the name+project_id columns
	// for backward compatibility with the existing table schema.
	_, err := s.db.Exec(
		`INSERT INTO installed_plugins (name, project_id, id, user_id, plugin_id, config, installed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name, project_id) DO UPDATE SET
		   id=excluded.id, config=excluded.config, installed_at=excluded.installed_at`,
		pluginID, userID, uuid.New().String(), userID, pluginID, config, ts)
	if err != nil {
		return nil, fmt.Errorf("install plugin: %w", err)
	}
	return s.GetInstalledPlugin(userID, pluginID)
}

func (s *SQLiteStore) UninstallPlugin(userID, pluginID string) error {
	// name=pluginID, project_id=userID is the logical PK mapping used by InstallPlugin.
	_, err := s.db.Exec(
		`DELETE FROM installed_plugins WHERE name = ? AND project_id = ?`,
		pluginID, userID)
	return err
}

func (s *SQLiteStore) GetInstalledPlugin(userID, pluginID string) (*InstalledPlugin, error) {
	row := s.db.QueryRow(
		`SELECT id, user_id, plugin_id, config, installed_at FROM installed_plugins WHERE name = ? AND project_id = ?`,
		pluginID, userID)
	return scanInstalledPlugin(row)
}

type pluginScanner interface {
	Scan(dest ...any) error
}

func scanInstalledPlugin(s pluginScanner) (*InstalledPlugin, error) {
	var p InstalledPlugin
	var ts int64
	if err := s.Scan(&p.ID, &p.UserID, &p.PluginID, &p.Config, &ts); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if p.Config == "" {
		p.Config = "{}"
	}
	p.CreatedAt = time.Unix(ts, 0)
	return &p, nil
}
