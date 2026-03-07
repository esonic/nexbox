package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps the sql.DB connection and manages database lifecycle.
type DB struct {
	*sql.DB
}

// New creates a new database connection and runs migrations
func New(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "beehub.db")
	sqlDB, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite allows only one writer at a time. Using a single connection
	// serialises all writes at the Go level, avoiding "database is locked"
	// errors from concurrent goroutines competing for the write lock.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	db := &DB{sqlDB}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	// Verify WAL mode is active. The DSN parameter _journal_mode=WAL should
	// have set it, but we confirm here to catch silent failures (e.g. the pure
	// Go modernc driver silently ignoring the DSN param).
	var journalMode string
	if err := sqlDB.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		return nil, fmt.Errorf("query journal_mode: %w", err)
	}
	if journalMode != "wal" {
		if _, err := sqlDB.Exec(`PRAGMA journal_mode=WAL`); err != nil {
			return nil, fmt.Errorf("enable WAL mode: %w", err)
		}
	}

	return db, nil
}

func (db *DB) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT DEFAULT '',
			git_repo_path TEXT DEFAULT '',
			gitlab_token TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			extra TEXT DEFAULT '{}',
			namespace_id TEXT DEFAULT 'local',
			gitlab_repo_url TEXT DEFAULT '',
			created_by TEXT DEFAULT 'local',
			version INTEGER DEFAULT 1
		)`,

		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY,
			namespace_id TEXT DEFAULT 'local',
			project_id TEXT DEFAULT '',
			parent_id TEXT DEFAULT '',
			title TEXT NOT NULL,
			description TEXT DEFAULT '',
			status TEXT DEFAULT 'open',
			priority TEXT DEFAULT 'medium',
			assigner TEXT DEFAULT '',
			ref_count INTEGER DEFAULT 0,
			comment_count INTEGER DEFAULT 0,
			created_by TEXT DEFAULT 'local',
			version INTEGER DEFAULT 1,
			labels TEXT DEFAULT '',
			closed_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			namespace_id TEXT DEFAULT 'local',
			name TEXT DEFAULT '',
			project_id TEXT DEFAULT '',
			agent TEXT DEFAULT '',
			agent_session_id TEXT DEFAULT '',
			stream_id TEXT DEFAULT '',
			sort_order INTEGER DEFAULT 0,
			created_by TEXT DEFAULT 'local',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			features TEXT DEFAULT '',
			deleted_at DATETIME
		)`,

		`CREATE TABLE IF NOT EXISTS assets (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			type TEXT NOT NULL,
			title TEXT NOT NULL,
			current_version INTEGER DEFAULT 1,
			status TEXT DEFAULT 'draft',
			created_by TEXT DEFAULT 'local',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_assets_project ON assets(project_id)`,

		`CREATE TABLE IF NOT EXISTS asset_versions (
			id TEXT PRIMARY KEY,
			asset_id TEXT NOT NULL,
			version INTEGER NOT NULL,
			content TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS review_comments (
			id TEXT PRIMARY KEY,
			asset_id TEXT NOT NULL,
			asset_version INTEGER NOT NULL,
			line_number INTEGER,
			content TEXT NOT NULL,
			author TEXT DEFAULT 'user',
			status TEXT DEFAULT 'open',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS task_assets (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			asset_id TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(task_id, asset_id)
		)`,

		`CREATE TABLE IF NOT EXISTS chat_messages (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			parts TEXT NOT NULL DEFAULT '[]',
			metadata TEXT DEFAULT '{}',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_session ON chat_messages(session_id)`,

		`CREATE TABLE IF NOT EXISTS task_comments (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			author TEXT DEFAULT 'local',
			body TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_task_comments_task ON task_comments(task_id)`,

		`CREATE TABLE IF NOT EXISTS labels (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			color TEXT NOT NULL DEFAULT '#6b7280',
			description TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS task_label_links (
			task_id TEXT NOT NULL,
			label_id TEXT NOT NULL,
			PRIMARY KEY(task_id, label_id)
		)`,

		`CREATE TABLE IF NOT EXISTS reactions (
			id TEXT PRIMARY KEY,
			target_type TEXT NOT NULL,
			target_id TEXT NOT NULL,
			author TEXT DEFAULT 'local',
			emoji TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(target_type, target_id, author, emoji)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_reactions_target ON reactions(target_type, target_id)`,

		`CREATE TABLE IF NOT EXISTS changes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			namespace_id TEXT NOT NULL DEFAULT 'local',
			project_id TEXT DEFAULT '',
			entity_type TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			action TEXT NOT NULL,
			summary TEXT DEFAULT '',
			detail TEXT DEFAULT '',
			user_name TEXT DEFAULT 'local',
			entity_version INTEGER,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_changes_ns_id ON changes(namespace_id, id)`,

		`CREATE TABLE IF NOT EXISTS queued_messages (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			text TEXT NOT NULL DEFAULT '',
			images TEXT NOT NULL DEFAULT '[]',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_queued_messages_session ON queued_messages(session_id)`,

		`CREATE TABLE IF NOT EXISTS message_part_details (
			message_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			parts TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_part_details_session ON message_part_details(session_id)`,

		`CREATE TABLE IF NOT EXISTS workflows (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			namespace_id TEXT NOT NULL,
			name TEXT NOT NULL,
			description TEXT DEFAULT '',
			definition TEXT NOT NULL,
			version INTEGER NOT NULL DEFAULT 1,
			status TEXT DEFAULT 'draft',
			created_by TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_workflows_project ON workflows(project_id)`,
		`CREATE INDEX IF NOT EXISTS idx_workflows_namespace ON workflows(namespace_id)`,

		`CREATE TABLE IF NOT EXISTS wf_executions (
			id TEXT PRIMARY KEY,
			workflow_id TEXT NOT NULL,
			workflow_version INTEGER NOT NULL,
			definition_snapshot TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			triggered_by TEXT NOT NULL DEFAULT '',
			user_context TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			started_at DATETIME,
			completed_at DATETIME,
			duration_ms INTEGER,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wf_exec_workflow ON wf_executions(workflow_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_wf_exec_status ON wf_executions(status)`,

		`CREATE TABLE IF NOT EXISTS wf_node_logs (
			id TEXT PRIMARY KEY,
			execution_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			node_type TEXT NOT NULL,
			node_label TEXT NOT NULL DEFAULT '',
			level INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'pending',
			input TEXT NOT NULL DEFAULT '',
			output TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			started_at DATETIME,
			completed_at DATETIME,
			duration_ms INTEGER,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			hook_retries INTEGER NOT NULL DEFAULT 0,
			hook_result TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wf_node_exec ON wf_node_logs(execution_id, level ASC, node_id)`,

		`CREATE TABLE IF NOT EXISTS ralph_runs (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			workflow_id TEXT DEFAULT '',
			prompt TEXT NOT NULL,
			workers INTEGER NOT NULL DEFAULT 1,
			status TEXT NOT NULL DEFAULT 'running',
			total_tasks INTEGER DEFAULT 0,
			total_completed INTEGER DEFAULT 0,
			total_failed INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ralph_runs_project ON ralph_runs(project_id)`,

		`CREATE TABLE IF NOT EXISTS ralph_worker_logs (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			worker_index INTEGER NOT NULL,
			task_id TEXT NOT NULL,
			task_title TEXT DEFAULT '',
			session_id TEXT DEFAULT '',
			status TEXT NOT NULL DEFAULT 'running',
			started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ralph_worker_logs_run ON ralph_worker_logs(run_id)`,

		`CREATE TABLE IF NOT EXISTS workflow_schedules (
			workflow_id TEXT PRIMARY KEY,
			schedule    TEXT NOT NULL DEFAULT ''
		)`,

		// Marketplace: locally installed MCP servers
		// manifest = JSON runtime config (key, type, cmd/url, args, env, tool_names)
		// project_id = '' means global (used in all sessions)
		`CREATE TABLE IF NOT EXISTS installed_mcps (
			name         TEXT NOT NULL,
			project_id   TEXT NOT NULL DEFAULT '',
			mcp_key      TEXT NOT NULL,
			manifest     TEXT NOT NULL DEFAULT '{}',
			installed_at INTEGER NOT NULL DEFAULT 0,
			env_values TEXT NOT NULL DEFAULT '{}',
			package_id TEXT NOT NULL DEFAULT '',
			version TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (name, project_id)
		)`,

		// Marketplace: locally installed skills
		// project_id = '' means global (copies to all projects on creation)
		`CREATE TABLE IF NOT EXISTS installed_skills (
			name         TEXT NOT NULL,
			project_id   TEXT NOT NULL,
			version      TEXT NOT NULL DEFAULT '',
			install_path TEXT NOT NULL DEFAULT '',
			installed_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (name, project_id)
		)`,

		// Users
		`CREATE TABLE IF NOT EXISTS users (
			id          TEXT PRIMARY KEY,
			username    TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			email       TEXT NOT NULL DEFAULT '',
			role        TEXT NOT NULL DEFAULT 'user',
			features    INTEGER NOT NULL DEFAULT 0,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// User balances (one row per user)
		`CREATE TABLE IF NOT EXISTS user_balances (
			user_id         TEXT PRIMARY KEY,
			balance_tokens  INTEGER NOT NULL DEFAULT 0,
			reserved_tokens INTEGER NOT NULL DEFAULT 0,
			total_recharged INTEGER NOT NULL DEFAULT 0,
			updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// Token transaction log
		`CREATE TABLE IF NOT EXISTS token_transactions (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			delta      INTEGER NOT NULL,
			reason     TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_token_tx_user ON token_transactions(user_id, created_at DESC)`,

		// Marketplace: locally installed plugins
		`CREATE TABLE IF NOT EXISTS installed_plugins (
			name         TEXT NOT NULL,
			project_id   TEXT NOT NULL DEFAULT '',
			install_path TEXT NOT NULL DEFAULT '',
			installed_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (name, project_id)
		)`,
	}

	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, m)
		}
	}

	// Additive column migrations (idempotent: skip if column already exists).
	type colMigration struct{ check, alter string }
	colMigrations := []colMigration{
		// installed_plugins: new user-centric columns (Phase 6 marketplace)
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('installed_plugins') WHERE name='id'`,
			alter: `ALTER TABLE installed_plugins ADD COLUMN id TEXT NOT NULL DEFAULT ''`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('installed_plugins') WHERE name='user_id'`,
			alter: `ALTER TABLE installed_plugins ADD COLUMN user_id TEXT NOT NULL DEFAULT ''`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('installed_plugins') WHERE name='plugin_id'`,
			alter: `ALTER TABLE installed_plugins ADD COLUMN plugin_id TEXT NOT NULL DEFAULT ''`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('installed_plugins') WHERE name='config'`,
			alter: `ALTER TABLE installed_plugins ADD COLUMN config TEXT NOT NULL DEFAULT '{}'`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='worktree_mode'`,
			alter: `ALTER TABLE sessions ADD COLUMN worktree_mode TEXT NOT NULL DEFAULT 'local'`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='worktree_path'`,
			alter: `ALTER TABLE sessions ADD COLUMN worktree_path TEXT NOT NULL DEFAULT ''`,
		},
		// owner_id on projects (for multi-user isolation)
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name='owner_id'`,
			alter: `ALTER TABLE projects ADD COLUMN owner_id TEXT NOT NULL DEFAULT ''`,
		},
		// user_id on sessions (for multi-user isolation)
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='user_id'`,
			alter: `ALTER TABLE sessions ADD COLUMN user_id TEXT NOT NULL DEFAULT ''`,
		},
	}
	for _, cm := range colMigrations {
		var count int
		if err := db.QueryRow(cm.check).Scan(&count); err != nil || count == 0 {
			if _, err := db.Exec(cm.alter); err != nil {
				return fmt.Errorf("column migration failed: %w\nSQL: %s", err, cm.alter)
			}
		}
	}

	return nil
}
