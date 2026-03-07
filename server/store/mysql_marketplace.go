package store

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// MCP – MySQL implementation
// ---------------------------------------------------------------------------

func (s *MySQLStore) UpsertInstalledMCP(mcp InstalledMCP) error {
	ts := mcp.InstalledAt.Unix()
	if ts == 0 {
		ts = time.Now().Unix()
	}
	envValues := mcp.EnvValues
	if envValues == "" {
		envValues = "{}"
	}
	_, err := s.db.Exec(`
		INSERT INTO installed_mcps (name, project_id, package_id, version, mcp_key, manifest, env_values, installed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			package_id   = VALUES(package_id),
			version      = VALUES(version),
			mcp_key      = VALUES(mcp_key),
			manifest     = VALUES(manifest),
			env_values   = VALUES(env_values),
			installed_at = VALUES(installed_at)
	`, mcp.Name, mcp.ProjectID, mcp.PackageID, mcp.Version, mcp.MCPKey, mcp.Manifest, envValues, ts)
	return err
}

func (s *MySQLStore) DeleteInstalledMCP(name, projectID string) error {
	_, err := s.db.Exec(`DELETE FROM installed_mcps WHERE name = ? AND project_id = ?`, name, projectID)
	return err
}

func (s *MySQLStore) GetInstalledMCP(name, projectID string) (*InstalledMCP, error) {
	row := s.db.QueryRow(
		`SELECT name, project_id, package_id, version, mcp_key, manifest, env_values, installed_at
		 FROM installed_mcps WHERE name = ? AND project_id = ?`,
		name, projectID)
	return mysqlScanInstalledMCP(row)
}

// ListInstalledMCPs returns installed MCPs.
//   - projectID = "*"  → all records (global + all project-specific)
//   - projectID = ""   → global only (project_id='')
//   - projectID = "<id>" → global + project-specific for that project
func (s *MySQLStore) ListInstalledMCPs(projectID string) ([]InstalledMCP, error) {
	var (
		rows *sql.Rows
		err  error
	)
	switch projectID {
	case "*":
		rows, err = s.db.Query(
			`SELECT name, project_id, package_id, version, mcp_key, manifest, env_values, installed_at
			 FROM installed_mcps ORDER BY name, project_id`)
	case "":
		rows, err = s.db.Query(
			`SELECT name, project_id, package_id, version, mcp_key, manifest, env_values, installed_at
			 FROM installed_mcps WHERE project_id = '' ORDER BY name`)
	default:
		rows, err = s.db.Query(
			`SELECT name, project_id, package_id, version, mcp_key, manifest, env_values, installed_at
			 FROM installed_mcps WHERE project_id IN ('', ?) ORDER BY name, project_id`,
			projectID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InstalledMCP{}
	for rows.Next() {
		m, err := mysqlScanInstalledMCP(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func mysqlScanInstalledMCP(s interface{ Scan(dest ...any) error }) (*InstalledMCP, error) {
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

// ---------------------------------------------------------------------------
// Skills – MySQL implementation
// ---------------------------------------------------------------------------

func (s *MySQLStore) UpsertInstalledSkill(skill InstalledSkill) error {
	ts := skill.InstalledAt.Unix()
	if ts == 0 {
		ts = time.Now().Unix()
	}
	_, err := s.db.Exec(`
		INSERT INTO installed_skills (name, project_id, version, install_path, installed_at)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			version      = VALUES(version),
			install_path = VALUES(install_path),
			installed_at = VALUES(installed_at)
	`, skill.Name, skill.ProjectID, skill.Version, skill.InstallPath, ts)
	return err
}

func (s *MySQLStore) DeleteInstalledSkill(name, projectID string) error {
	_, err := s.db.Exec(`DELETE FROM installed_skills WHERE name = ? AND project_id = ?`, name, projectID)
	return err
}

func (s *MySQLStore) GetInstalledSkill(name, projectID string) (*InstalledSkill, error) {
	row := s.db.QueryRow(
		`SELECT name, project_id, version, install_path, installed_at
		 FROM installed_skills WHERE name = ? AND project_id = ?`,
		name, projectID)
	return mysqlScanInstalledSkill(row)
}

// ListInstalledSkills returns installed skills.
//   - projectID="*"  → all records
//   - projectID=""   → global only (project_id='')
//   - projectID="<id>" → global + project-specific for that project
func (s *MySQLStore) ListInstalledSkills(projectID string) ([]InstalledSkill, error) {
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
	// Collect all rows first, then release the connection before doing filesystem I/O.
	out := []InstalledSkill{}
	for rows.Next() {
		sk, err := mysqlScanInstalledSkill(rows)
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
	// Check if install directories still exist on disk.
	for i := range out {
		if out[i].InstallPath != "" {
			if _, statErr := os.Stat(out[i].InstallPath); os.IsNotExist(statErr) {
				out[i].Missing = true
			}
		}
	}
	return out, nil
}

func mysqlScanInstalledSkill(s interface{ Scan(dest ...any) error }) (*InstalledSkill, error) {
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

// ---------------------------------------------------------------------------
// Installed Plugins – MySQL implementation
// ---------------------------------------------------------------------------

func (s *MySQLStore) ListInstalledPlugins(userID string) ([]InstalledPlugin, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, plugin_id, config, installed_at FROM installed_plugins WHERE user_id = ? ORDER BY installed_at DESC`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InstalledPlugin{}
	for rows.Next() {
		p, err := mysqlScanInstalledPlugin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *MySQLStore) InstallPlugin(userID, pluginID, config string) (*InstalledPlugin, error) {
	if config == "" {
		config = "{}"
	}
	ts := time.Now().Unix()
	_, err := s.db.Exec(`
		INSERT INTO installed_plugins (name, project_id, id, user_id, plugin_id, config, installed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			id           = VALUES(id),
			config       = VALUES(config),
			installed_at = VALUES(installed_at)
	`, pluginID, userID, uuid.New().String(), userID, pluginID, config, ts)
	if err != nil {
		return nil, fmt.Errorf("install plugin: %w", err)
	}
	return s.GetInstalledPlugin(userID, pluginID)
}

func (s *MySQLStore) UninstallPlugin(userID, pluginID string) error {
	_, err := s.db.Exec(
		`DELETE FROM installed_plugins WHERE name = ? AND project_id = ?`,
		pluginID, userID)
	return err
}

func (s *MySQLStore) GetInstalledPlugin(userID, pluginID string) (*InstalledPlugin, error) {
	row := s.db.QueryRow(
		`SELECT id, user_id, plugin_id, config, installed_at FROM installed_plugins WHERE name = ? AND project_id = ?`,
		pluginID, userID)
	return mysqlScanInstalledPlugin(row)
}

func mysqlScanInstalledPlugin(s interface{ Scan(dest ...any) error }) (*InstalledPlugin, error) {
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
