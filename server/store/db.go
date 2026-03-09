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

	dbPath := filepath.Join(dataDir, "boxnex.db")
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
		// ── Core tables ──────────────────────────────────────────────────

		`CREATE TABLE IF NOT EXISTS projects (
			id              TEXT PRIMARY KEY,
			namespace_id    TEXT NOT NULL DEFAULT 'local',
			name            TEXT NOT NULL,
			description     TEXT DEFAULT '',
			git_repo_path   TEXT DEFAULT '',
			gitlab_repo_url TEXT DEFAULT '',
			created_by      TEXT NOT NULL DEFAULT '',
			owner_id        TEXT NOT NULL DEFAULT '',
			version         INTEGER DEFAULT 1,
			created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_projects_owner ON projects(owner_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_projects_owner_name ON projects(owner_id, name)`,

		`CREATE TABLE IF NOT EXISTS sessions (
			id               TEXT PRIMARY KEY,
			namespace_id     TEXT NOT NULL DEFAULT 'local',
			name             TEXT DEFAULT '',
			project_id       TEXT DEFAULT '',
			agent            TEXT DEFAULT '',
			model            TEXT NOT NULL DEFAULT '',
			agent_session_id TEXT DEFAULT '',
			stream_id        TEXT DEFAULT '',
			sort_order       INTEGER DEFAULT 0,
			features         TEXT DEFAULT '',
			worktree_mode    TEXT NOT NULL DEFAULT 'local',
			worktree_path    TEXT NOT NULL DEFAULT '',
			created_by       TEXT NOT NULL DEFAULT '',
			user_id          TEXT NOT NULL DEFAULT '',
			created_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at       DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_id)`,

		// ── Task / Asset tables (no user isolation yet — admin-only) ──

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

		// ── Chat / Queue ─────────────────────────────────────────────────

		`CREATE TABLE IF NOT EXISTS chat_messages (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			parts TEXT NOT NULL DEFAULT '[]',
			metadata TEXT DEFAULT '{}',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_session ON chat_messages(session_id)`,

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

		// ── Audit log ────────────────────────────────────────────────────

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

		// ── Workflow ─────────────────────────────────────────────────────

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

		`CREATE TABLE IF NOT EXISTS workflow_schedules (
			workflow_id TEXT PRIMARY KEY,
			schedule    TEXT NOT NULL DEFAULT ''
		)`,

		// ── Marketplace ──────────────────────────────────────────────────

		`CREATE TABLE IF NOT EXISTS installed_mcps (
			name         TEXT NOT NULL,
			project_id   TEXT NOT NULL DEFAULT '',
			mcp_key      TEXT NOT NULL,
			manifest     TEXT NOT NULL DEFAULT '{}',
			installed_at INTEGER NOT NULL DEFAULT 0,
			env_values   TEXT NOT NULL DEFAULT '{}',
			package_id   TEXT NOT NULL DEFAULT '',
			version      TEXT NOT NULL DEFAULT '',
			auth_type    TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (name, project_id)
		)`,

		`CREATE TABLE IF NOT EXISTS mcp_oauth_tokens (
			mcp_name       TEXT NOT NULL,
			project_id     TEXT NOT NULL DEFAULT '',
			access_token   TEXT NOT NULL DEFAULT '',
			refresh_token  TEXT NOT NULL DEFAULT '',
			expires_at     INTEGER NOT NULL DEFAULT 0,
			token_endpoint TEXT NOT NULL DEFAULT '',
			client_id      TEXT NOT NULL DEFAULT '',
			state          TEXT NOT NULL DEFAULT '',
			code_verifier  TEXT NOT NULL DEFAULT '',
			updated_at     INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (mcp_name, project_id)
		)`,

		`CREATE TABLE IF NOT EXISTS installed_skills (
			name         TEXT NOT NULL,
			project_id   TEXT NOT NULL,
			version      TEXT NOT NULL DEFAULT '',
			install_path TEXT NOT NULL DEFAULT '',
			installed_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (name, project_id)
		)`,

		`CREATE TABLE IF NOT EXISTS installed_plugins (
			name         TEXT NOT NULL,
			project_id   TEXT NOT NULL DEFAULT '',
			install_path TEXT NOT NULL DEFAULT '',
			installed_at INTEGER NOT NULL DEFAULT 0,
			id           TEXT NOT NULL DEFAULT '',
			user_id      TEXT NOT NULL DEFAULT '',
			plugin_id    TEXT NOT NULL DEFAULT '',
			config       TEXT NOT NULL DEFAULT '{}',
			PRIMARY KEY (name, project_id)
		)`,

		// ── Users & Billing ──────────────────────────────────────────────

		`CREATE TABLE IF NOT EXISTS users (
			id            TEXT PRIMARY KEY,
			username      TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			email         TEXT NOT NULL DEFAULT '',
			role          TEXT NOT NULL DEFAULT 'user',
			features      INTEGER NOT NULL DEFAULT 0,
			created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at    DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS user_balances (
			user_id         TEXT PRIMARY KEY,
			balance_tokens  INTEGER NOT NULL DEFAULT 0,
			reserved_tokens INTEGER NOT NULL DEFAULT 0,
			total_recharged INTEGER NOT NULL DEFAULT 0,
			updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS token_transactions (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			delta      INTEGER NOT NULL,
			reason     TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_token_tx_user ON token_transactions(user_id, created_at DESC)`,
	}

	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, m)
		}
	}

	return nil
}
