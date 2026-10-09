package store

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the sql.DB connection and manages database lifecycle.
// It automatically detects SQLITE_CANTOPEN errors and reconnects.
type DB struct {
	mu    sync.RWMutex
	sqlDB *sql.DB
	dsn   string
}

// SQLDB returns the underlying *sql.DB for callers that need raw access.
func (db *DB) SQLDB() *sql.DB {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.sqlDB
}

// Close closes the database connection pool.
func (db *DB) Close() error {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.sqlDB.Close()
}

// Ping verifies the database connection is alive.
func (db *DB) Ping() error {
	return db.SQLDB().Ping()
}

// Query executes a query and retries on CANTOPEN.
func (db *DB) Query(query string, args ...any) (*sql.Rows, error) {
	rows, err := db.SQLDB().Query(query, args...)
	if err != nil && isCantOpen(err) {
		log.Printf("[db] CANTOPEN on Query, attempting reconnect...")
		if rerr := db.reconnect(); rerr == nil {
			return db.SQLDB().Query(query, args...)
		}
	}
	return rows, err
}

// Exec executes a statement and retries on CANTOPEN.
func (db *DB) Exec(query string, args ...any) (sql.Result, error) {
	res, err := db.SQLDB().Exec(query, args...)
	if err != nil && isCantOpen(err) {
		log.Printf("[db] CANTOPEN on Exec, attempting reconnect...")
		if rerr := db.reconnect(); rerr == nil {
			return db.SQLDB().Exec(query, args...)
		}
	}
	return res, err
}

// Row wraps sql.Row with CANTOPEN retry support.
type Row struct {
	db    *DB
	query string
	args  []any
	row   *sql.Row
}

// Scan delegates to the underlying sql.Row.Scan and retries on CANTOPEN.
func (r *Row) Scan(dest ...any) error {
	err := r.row.Scan(dest...)
	if err != nil && isCantOpen(err) {
		log.Printf("[db] CANTOPEN on Scan, attempting reconnect...")
		if rerr := r.db.reconnect(); rerr == nil {
			return r.db.SQLDB().QueryRow(r.query, r.args...).Scan(dest...)
		}
	}
	return err
}

// Err returns the error from the underlying sql.Row (if any).
func (r *Row) Err() error {
	return r.row.Err()
}

// QueryRow executes a query returning a single row, with CANTOPEN retry on Scan.
func (db *DB) QueryRow(query string, args ...any) *Row {
	return &Row{
		db:    db,
		query: query,
		args:  args,
		row:   db.SQLDB().QueryRow(query, args...),
	}
}

// Begin starts a transaction, with CANTOPEN retry.
func (db *DB) Begin() (*sql.Tx, error) {
	tx, err := db.SQLDB().Begin()
	if err != nil && isCantOpen(err) {
		log.Printf("[db] CANTOPEN on Begin, attempting reconnect...")
		if rerr := db.reconnect(); rerr == nil {
			return db.SQLDB().Begin()
		}
	}
	return tx, err
}

// reconnect closes the current connection pool and opens a fresh one.
func (db *DB) reconnect() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	old := db.sqlDB

	// Brief pause to let the OS release file handles from the old connection.
	time.Sleep(50 * time.Millisecond)

	newDB, err := sql.Open("sqlite", db.dsn)
	if err != nil {
		log.Printf("[db] reconnect: sql.Open failed: %v", err)
		return err
	}
	newDB.SetMaxOpenConns(1)
	newDB.SetMaxIdleConns(1)

	if err := newDB.Ping(); err != nil {
		newDB.Close()
		log.Printf("[db] reconnect: Ping failed: %v", err)
		return err
	}

	// Re-apply PRAGMAs on the fresh connection. busy_timeout matches the New() value.
	newDB.Exec(`PRAGMA busy_timeout=10000`)
	newDB.Exec(`PRAGMA journal_mode=WAL`)
	newDB.Exec(`PRAGMA temp_store=MEMORY`)

	db.sqlDB = newDB
	old.Close() // close old pool after swap
	log.Printf("[db] reconnect: success")
	return nil
}

// isCantOpen detects SQLite CANTOPEN errors (code 14).
func isCantOpen(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "unable to open database file") ||
		strings.Contains(msg, "SQLITE_CANTOPEN") ||
		strings.Contains(msg, "(14)")
}

// New creates a new database connection and runs migrations
func New(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "nexbox.db")
	// modernc.org/sqlite DSN syntax for connection-time PRAGMAs.
	// (mattn/go-sqlite3's "?_journal_mode=...&_busy_timeout=..." form is silently
	// ignored by this driver, leaving busy_timeout=0 and journal=delete during
	// migrations — which is what caused intermittent SQLITE_BUSY on startup.)
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)",
		dbPath,
	)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite allows only one writer at a time. Using a single connection
	// serialises all writes at the Go level, avoiding "database is locked"
	// errors from concurrent goroutines competing for the write lock.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	// Belt-and-suspenders: re-apply PRAGMAs via Exec in case the DSN-level
	// pragma application failed silently for any reason. busy_timeout must be
	// set BEFORE migrations so a transient lock during startup gets retried
	// for up to 10s instead of failing the boot.
	if _, err := sqlDB.Exec(`PRAGMA busy_timeout=10000`); err != nil {
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := sqlDB.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}
	sqlDB.Exec(`PRAGMA temp_store=MEMORY`)

	db := &DB{sqlDB: sqlDB, dsn: dsn}

	// Force temp storage to memory BEFORE running migrations. Large migration
	// statements — notably the fork-checkpoint message backfill, a full-table
	// scan over chat_messages — otherwise spill to an on-disk temp file, which
	// returns SQLITE_CANTOPEN (14) when the temp directory is inaccessible or
	// full and bricks startup. This PRAGMA must be in effect during migrate(),
	// not merely after it.
	sqlDB.Exec(`PRAGMA temp_store=MEMORY`)

	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	// Verify WAL is actually active (catches the case where another process
	// holds the DB in a different mode at open time).
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
			model TEXT NOT NULL DEFAULT '',
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
		// Composite index for paginated message queries (ORDER BY created_at DESC).
		// Without this, SQLite must temp-sort all messages for a session, which can
		// trigger SQLITE_CANTOPEN when temp file creation fails for large sessions.
		`CREATE INDEX IF NOT EXISTS idx_messages_session_time ON chat_messages(session_id, created_at DESC)`,

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
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			worktree_mode TEXT NOT NULL DEFAULT 'local'
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
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			worktree_path TEXT NOT NULL DEFAULT ''
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

		// worktree_mode is intentionally per-machine: it controls how this
		// machine creates a worktree when executing a workflow. Stored locally
		// so cloud-mode users don't depend on the cloud server knowing the
		// field, mirroring how workflow_schedules is stored.
		`CREATE TABLE IF NOT EXISTS workflow_worktree_modes (
			workflow_id TEXT PRIMARY KEY,
			mode        TEXT NOT NULL DEFAULT 'local'
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

		// Marketplace: locally installed plugin directories (Claude --plugin-dir)
		`CREATE TABLE IF NOT EXISTS installed_plugins (
			name         TEXT NOT NULL,
			project_id   TEXT NOT NULL DEFAULT '',
			install_path TEXT NOT NULL DEFAULT '',
			installed_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (name, project_id)
		)`,

		// MCP OAuth tokens (for oauth_discovery / oauth auth types)
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

		// Marketplace sources (user-added marketplace directories / git repos)
		`CREATE TABLE IF NOT EXISTS marketplace_sources (
			id   TEXT PRIMARY KEY,
			type TEXT NOT NULL DEFAULT 'local',
			url  TEXT NOT NULL DEFAULT '',
			path TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			added_at INTEGER NOT NULL DEFAULT 0
		)`,

		// Local user accounts. Populated only via the `nexbox add-user` CLI;
		// there is no self-registration endpoint.
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// Public web shares: maps a random short id to a (project, file) pair
		// for unauthenticated read-only access via /web_share/{id}.
		`CREATE TABLE IF NOT EXISTS web_shares (
			id         TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			path       TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(project_id, path)
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
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='chrome_browser_url'`,
			alter: `ALTER TABLE sessions ADD COLUMN chrome_browser_url TEXT NOT NULL DEFAULT ''`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='model'`,
			alter: `ALTER TABLE sessions ADD COLUMN model TEXT NOT NULL DEFAULT ''`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='worktree_mode'`,
			alter: `ALTER TABLE sessions ADD COLUMN worktree_mode TEXT NOT NULL DEFAULT 'local'`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='worktree_path'`,
			alter: `ALTER TABLE sessions ADD COLUMN worktree_path TEXT NOT NULL DEFAULT ''`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='effort'`,
			alter: `ALTER TABLE sessions ADD COLUMN effort TEXT NOT NULL DEFAULT ''`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='forked_from_session_id'`,
			alter: `ALTER TABLE sessions ADD COLUMN forked_from_session_id TEXT NOT NULL DEFAULT ''`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='forked_from_message_id'`,
			alter: `ALTER TABLE sessions ADD COLUMN forked_from_message_id TEXT NOT NULL DEFAULT ''`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='forked_at_agent_checkpoint'`,
			alter: `ALTER TABLE sessions ADD COLUMN forked_at_agent_checkpoint TEXT NOT NULL DEFAULT ''`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('installed_mcps') WHERE name='auth_type'`,
			alter: `ALTER TABLE installed_mcps ADD COLUMN auth_type TEXT NOT NULL DEFAULT 'none'`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('assets') WHERE name='lark_doc_url'`,
			alter: `ALTER TABLE assets ADD COLUMN lark_doc_url TEXT NOT NULL DEFAULT ''`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('workflows') WHERE name='worktree_mode'`,
			alter: `ALTER TABLE workflows ADD COLUMN worktree_mode TEXT NOT NULL DEFAULT 'local'`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('wf_executions') WHERE name='worktree_path'`,
			alter: `ALTER TABLE wf_executions ADD COLUMN worktree_path TEXT NOT NULL DEFAULT ''`,
		},
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='is_hidden'`,
			alter: `ALTER TABLE sessions ADD COLUMN is_hidden INTEGER NOT NULL DEFAULT 0`,
		},
		// Phase 5: per-user project ownership. Default '' is used during the
		// brief window between this column being added and the first user being
		// created via `nexbox add-user`.
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name='owner_user_id'`,
			alter: `ALTER TABLE projects ADD COLUMN owner_user_id INTEGER NOT NULL DEFAULT 0`,
		},
		// auto_managed_dir is true (1) when the server created the working
		// directory under data-dir/users/<user>/<name>/. False (0) means the
		// user supplied an existing external path on create; we must not
		// rename or delete it. Default 1 for existing rows since the old code
		// always auto-created.
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name='auto_managed_dir'`,
			alter: `ALTER TABLE projects ADD COLUMN auto_managed_dir INTEGER NOT NULL DEFAULT 1`,
		},
		// updated_at lets the scheduler distinguish "schedule existed before
		// today's slot" from "schedule was just saved". Without it, saving a
		// weekly schedule whose day includes today fires immediately if the
		// hour:minute has already passed today. Default 0 (epoch) for existing
		// rows preserves prior behaviour for already-persisted schedules.
		{
			check: `SELECT COUNT(*) FROM pragma_table_info('workflow_schedules') WHERE name='updated_at'`,
			alter: `ALTER TABLE workflow_schedules ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0`,
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

	// At most one live scratch session per project. Partial unique index ensures
	// concurrent get-or-create requests don't produce duplicates.
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_scratch_per_project ON sessions(project_id) WHERE is_hidden = 1 AND deleted_at IS NULL`); err != nil {
		return fmt.Errorf("scratch session index creation failed: %w", err)
	}

	return nil
}
