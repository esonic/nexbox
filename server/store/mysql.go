package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

var _ Store = (*MySQLStore)(nil)

// MySQLStore implements the Store interface using MySQL.
type MySQLStore struct {
	db *sql.DB
}

// NewMySQLStore creates a new MySQLStore.
// dsn example: "user:pass@tcp(127.0.0.1:3306)/boxnex?parseTime=true&charset=utf8mb4"
func NewMySQLStore(dsn string) (*MySQLStore, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	s := &MySQLStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("mysql migrate: %w", err)
	}
	return s, nil
}

// Close closes the underlying database connection.
func (s *MySQLStore) Close() error {
	return s.db.Close()
}

func (s *MySQLStore) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS projects (
			id              VARCHAR(36) PRIMARY KEY,
			namespace_id    VARCHAR(255) NOT NULL DEFAULT 'local',
			name            VARCHAR(255) NOT NULL,
			description     TEXT DEFAULT '',
			git_repo_path   TEXT DEFAULT '',
			gitlab_repo_url TEXT DEFAULT '',
			created_by      VARCHAR(255) NOT NULL DEFAULT '',
			owner_id        VARCHAR(36) NOT NULL DEFAULT '',
			version         INT DEFAULT 1,
			created_at      DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			updated_at      DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
			INDEX idx_projects_owner (owner_id),
			UNIQUE KEY uq_projects_owner_name (owner_id, name)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS tasks (
			id            VARCHAR(36) PRIMARY KEY,
			namespace_id  VARCHAR(255) DEFAULT 'local',
			project_id    VARCHAR(36) DEFAULT '',
			parent_id     VARCHAR(36) DEFAULT '',
			title         TEXT NOT NULL,
			description   MEDIUMTEXT DEFAULT '',
			status        VARCHAR(50) DEFAULT 'open',
			priority      VARCHAR(50) DEFAULT 'medium',
			assigner      VARCHAR(255) DEFAULT '',
			ref_count     INT DEFAULT 0,
			comment_count INT DEFAULT 0,
			created_by    VARCHAR(255) DEFAULT 'local',
			version       INT DEFAULT 1,
			labels        TEXT DEFAULT '',
			closed_at     DATETIME(3),
			created_at    DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			updated_at    DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
			INDEX idx_tasks_project (project_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS sessions (
			id               VARCHAR(36) PRIMARY KEY,
			namespace_id     VARCHAR(255) NOT NULL DEFAULT 'local',
			name             TEXT DEFAULT '',
			project_id       VARCHAR(36) DEFAULT '',
			agent            VARCHAR(100) DEFAULT '',
			model            VARCHAR(100) NOT NULL DEFAULT '',
			agent_session_id VARCHAR(255) DEFAULT '',
			stream_id        VARCHAR(255) DEFAULT '',
			sort_order       INT DEFAULT 0,
			features         TEXT DEFAULT '',
			worktree_mode    VARCHAR(50) NOT NULL DEFAULT 'local',
			worktree_path    TEXT NOT NULL DEFAULT '',
			created_by       VARCHAR(255) NOT NULL DEFAULT '',
			user_id          VARCHAR(36) NOT NULL DEFAULT '',
			created_at       DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			updated_at       DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
			deleted_at       DATETIME(3),
			INDEX idx_sessions_user (user_id),
			INDEX idx_sessions_project (project_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS assets (
			id              VARCHAR(36) PRIMARY KEY,
			project_id      VARCHAR(36) NOT NULL,
			type            VARCHAR(100) NOT NULL,
			title           TEXT NOT NULL,
			current_version INT DEFAULT 1,
			status          VARCHAR(50) DEFAULT 'draft',
			created_by      VARCHAR(255) DEFAULT 'local',
			created_at      DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			updated_at      DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
			INDEX idx_assets_project (project_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS asset_versions (
			id         VARCHAR(36) PRIMARY KEY,
			asset_id   VARCHAR(36) NOT NULL,
			version    INT NOT NULL,
			content    MEDIUMTEXT NOT NULL,
			created_at DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			INDEX idx_asset_versions_asset (asset_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS review_comments (
			id            VARCHAR(36) PRIMARY KEY,
			asset_id      VARCHAR(36) NOT NULL,
			asset_version INT NOT NULL,
			line_number   INT,
			content       MEDIUMTEXT NOT NULL,
			author        VARCHAR(255) DEFAULT 'user',
			status        VARCHAR(50) DEFAULT 'open',
			created_at    DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			INDEX idx_review_comments_asset (asset_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS task_assets (
			id         VARCHAR(36) PRIMARY KEY,
			task_id    VARCHAR(36) NOT NULL,
			asset_id   VARCHAR(36) NOT NULL,
			created_at DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			UNIQUE KEY uq_task_asset (task_id, asset_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS chat_messages (
			id         VARCHAR(36) PRIMARY KEY,
			session_id VARCHAR(36) NOT NULL,
			role       VARCHAR(50) NOT NULL,
			parts      MEDIUMTEXT NOT NULL DEFAULT '[]',
			metadata   TEXT DEFAULT '{}',
			created_at DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			INDEX idx_messages_session (session_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS task_comments (
			id         VARCHAR(36) PRIMARY KEY,
			task_id    VARCHAR(36) NOT NULL,
			author     VARCHAR(255) DEFAULT 'local',
			body       MEDIUMTEXT NOT NULL,
			created_at DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			updated_at DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
			INDEX idx_task_comments_task (task_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS labels (
			id          VARCHAR(36) PRIMARY KEY,
			name        VARCHAR(255) NOT NULL,
			color       VARCHAR(50) NOT NULL DEFAULT '#6b7280',
			description TEXT DEFAULT '',
			created_at  DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS task_label_links (
			task_id  VARCHAR(36) NOT NULL,
			label_id VARCHAR(36) NOT NULL,
			PRIMARY KEY (task_id, label_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS reactions (
			id          VARCHAR(36) PRIMARY KEY,
			target_type VARCHAR(100) NOT NULL,
			target_id   VARCHAR(36) NOT NULL,
			author      VARCHAR(255) DEFAULT 'local',
			emoji       VARCHAR(50) NOT NULL,
			created_at  DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			UNIQUE KEY uq_reaction (target_type, target_id, author, emoji),
			INDEX idx_reactions_target (target_type, target_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS ` + "`changes`" + ` (
			id             BIGINT AUTO_INCREMENT PRIMARY KEY,
			namespace_id   VARCHAR(255) NOT NULL DEFAULT 'local',
			project_id     VARCHAR(36) DEFAULT '',
			entity_type    VARCHAR(100) NOT NULL,
			entity_id      VARCHAR(36) NOT NULL,
			action         VARCHAR(100) NOT NULL,
			summary        TEXT DEFAULT '',
			detail         MEDIUMTEXT DEFAULT '',
			user_name      VARCHAR(255) DEFAULT 'local',
			entity_version INT,
			created_at     DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			INDEX idx_changes_ns_id (namespace_id, id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS queued_messages (
			id         VARCHAR(36) PRIMARY KEY,
			session_id VARCHAR(36) NOT NULL,
			text       MEDIUMTEXT NOT NULL DEFAULT '',
			images     TEXT NOT NULL DEFAULT '[]',
			created_at DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			INDEX idx_queued_messages_session (session_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS message_part_details (
			message_id VARCHAR(36) PRIMARY KEY,
			session_id VARCHAR(36) NOT NULL,
			parts      MEDIUMTEXT NOT NULL,
			created_at DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			INDEX idx_part_details_session (session_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS workflows (
			id           VARCHAR(36) PRIMARY KEY,
			project_id   VARCHAR(36) NOT NULL,
			namespace_id VARCHAR(255) NOT NULL,
			name         VARCHAR(255) NOT NULL,
			description  TEXT DEFAULT '',
			definition   MEDIUMTEXT NOT NULL,
			version      INT NOT NULL DEFAULT 1,
			status       VARCHAR(50) DEFAULT 'draft',
			schedule     TEXT DEFAULT '',
			created_by   VARCHAR(255) NOT NULL,
			created_at   DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			updated_at   DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
			INDEX idx_workflows_project (project_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS wf_executions (
			id                  VARCHAR(36) PRIMARY KEY,
			workflow_id         VARCHAR(36) NOT NULL,
			workflow_version    INT NOT NULL,
			definition_snapshot MEDIUMTEXT NOT NULL DEFAULT '',
			status              VARCHAR(50) NOT NULL DEFAULT 'pending',
			triggered_by        VARCHAR(255) NOT NULL DEFAULT '',
			user_context        TEXT NOT NULL DEFAULT '',
			error               TEXT NOT NULL DEFAULT '',
			started_at          DATETIME(3),
			completed_at        DATETIME(3),
			duration_ms         BIGINT,
			created_at          DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			INDEX idx_wf_exec_workflow (workflow_id),
			INDEX idx_wf_exec_status (status)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS wf_node_logs (
			id           VARCHAR(36) PRIMARY KEY,
			execution_id VARCHAR(36) NOT NULL,
			node_id      VARCHAR(255) NOT NULL,
			node_type    VARCHAR(100) NOT NULL,
			node_label   VARCHAR(255) NOT NULL DEFAULT '',
			level        INT NOT NULL DEFAULT 0,
			status       VARCHAR(50) NOT NULL DEFAULT 'pending',
			input        MEDIUMTEXT NOT NULL DEFAULT '',
			output       MEDIUMTEXT NOT NULL DEFAULT '',
			error        TEXT NOT NULL DEFAULT '',
			hook_retries INT NOT NULL DEFAULT 0,
			hook_result  VARCHAR(100) NOT NULL DEFAULT '',
			started_at   DATETIME(3),
			completed_at DATETIME(3),
			duration_ms  BIGINT,
			created_at   DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			INDEX idx_wf_node_exec (execution_id, level, node_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS workflow_schedules (
			workflow_id VARCHAR(36) PRIMARY KEY,
			schedule    TEXT NOT NULL DEFAULT ''
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS installed_mcps (
			name         VARCHAR(255) NOT NULL,
			project_id   VARCHAR(36) NOT NULL DEFAULT '',
			mcp_key      VARCHAR(255) NOT NULL,
			manifest     MEDIUMTEXT NOT NULL DEFAULT '{}',
			installed_at BIGINT NOT NULL DEFAULT 0,
			env_values   TEXT NOT NULL DEFAULT '{}',
			package_id   VARCHAR(255) NOT NULL DEFAULT '',
			version      VARCHAR(100) NOT NULL DEFAULT '',
			PRIMARY KEY (name, project_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS installed_skills (
			name         VARCHAR(255) NOT NULL,
			project_id   VARCHAR(36) NOT NULL,
			version      VARCHAR(100) NOT NULL DEFAULT '',
			install_path TEXT NOT NULL DEFAULT '',
			installed_at BIGINT NOT NULL DEFAULT 0,
			PRIMARY KEY (name, project_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS installed_plugins (
			name         VARCHAR(255) NOT NULL,
			project_id   VARCHAR(36) NOT NULL DEFAULT '',
			install_path TEXT NOT NULL DEFAULT '',
			installed_at BIGINT NOT NULL DEFAULT 0,
			id           VARCHAR(36) NOT NULL DEFAULT '',
			user_id      VARCHAR(36) NOT NULL DEFAULT '',
			plugin_id    VARCHAR(255) NOT NULL DEFAULT '',
			config       TEXT NOT NULL,
			PRIMARY KEY (name, project_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS users (
			id            VARCHAR(36) PRIMARY KEY,
			username      VARCHAR(255) NOT NULL,
			password_hash TEXT NOT NULL,
			email         VARCHAR(255) NOT NULL DEFAULT '',
			role          VARCHAR(50) NOT NULL DEFAULT 'user',
			features      BIGINT NOT NULL DEFAULT 0,
			created_at    DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			updated_at    DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
			UNIQUE KEY uq_users_username (username)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS user_balances (
			user_id         VARCHAR(36) PRIMARY KEY,
			balance_tokens  BIGINT NOT NULL DEFAULT 0,
			reserved_tokens BIGINT NOT NULL DEFAULT 0,
			total_recharged BIGINT NOT NULL DEFAULT 0,
			updated_at      DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE IF NOT EXISTS token_transactions (
			id         VARCHAR(36) PRIMARY KEY,
			user_id    VARCHAR(36) NOT NULL,
			session_id VARCHAR(36) NOT NULL DEFAULT '',
			delta      BIGINT NOT NULL,
			reason     VARCHAR(50) NOT NULL,
			created_at DATETIME(3) DEFAULT CURRENT_TIMESTAMP(3),
			INDEX idx_token_tx_user (user_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}

	for _, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, m)
		}
	}
	return nil
}
