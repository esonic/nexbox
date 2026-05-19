package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const DefaultServerURL = "https://sre-admin.okg.com/rca/api/v1/nexbox"

// OKOneConfig holds per-project OKOne change-order integration settings.
// Stored under "project_okone" in config.json, keyed by project ID.
type OKOneConfig struct {
	BaseURL        string `json:"base_url,omitempty"`
	ApplicationID  int    `json:"application_id,omitempty"`
	ServiceID      int    `json:"service_id,omitempty"`
	Service        string `json:"service,omitempty"`
	ServiceEnvID   int    `json:"service_env_id,omitempty"`
	ServiceEnvName string `json:"service_env_name,omitempty"`
	Owner          string `json:"owner,omitempty"`
}

// Config holds all local-only configuration persisted to ~/.nexbox/config.json.
type Config struct {
	DefaultAgent   string           `json:"default_agent"`
	ServerURL      string           `json:"server_url,omitempty"`
	Token          string           `json:"token,omitempty"`
	GitLabToken    string           `json:"gitlab_token,omitempty"`
	DisableCloud   bool             `json:"disable_cloud"`
	ChromeHeadless bool             `json:"chrome_headless"`
	EnableFileEdit bool             `json:"enable_file_edit"`
	Projects       []ProjectMapping `json:"projects,omitempty"`
}

// ProjectMapping links a remote project to a local Git repository path,
// and optionally holds per-project settings like OKOne integration.
type ProjectMapping struct {
	RemoteID    string       `json:"remote_id"`
	GitRepoPath string       `json:"git_repo_path"`
	OKOne       *OKOneConfig `json:"okone,omitempty"`
}

// Manager provides thread-safe load/save operations for Config.
type Manager struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

// NewManager creates a Manager that reads/writes the given file path.
// If the file does not exist, it initializes an empty config with defaults.
func NewManager(path string) (*Manager, error) {
	m := &Manager{path: path}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			m.cfg = Config{DefaultAgent: "claude", ServerURL: DefaultServerURL}
			return m, m.save()
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, &m.cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if m.cfg.ServerURL == "" {
		m.cfg.ServerURL = DefaultServerURL
	}
	return m, nil
}

// Path returns the filesystem path of the config file.
func (m *Manager) Path() string {
	return m.path
}

// Get returns a snapshot of the current config.
func (m *Manager) Get() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// Update applies a mutation function to the config and persists the result.
func (m *Manager) Update(fn func(cfg *Config)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&m.cfg)
	return m.save()
}

func (m *Manager) save() error {
	data, err := json.MarshalIndent(m.cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(m.path), ".config-*.json")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}
	tmp.Close()

	if err := os.Rename(tmpPath, m.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}
