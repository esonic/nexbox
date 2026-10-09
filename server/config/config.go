package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// MCPToolsConfig controls which groups of built-in MCP tools are exposed to
// AI agents. Every category is opt-in: a missing value means disabled.
type MCPToolsConfig struct {
	TaskManagement *bool `json:"task_management,omitempty"`
}

// TaskManagementEnabled reports whether the task tools are exposed.
func (c MCPToolsConfig) TaskManagementEnabled() bool {
	return c.TaskManagement != nil && *c.TaskManagement
}

// Config holds all configuration persisted to <data-dir>/config.json.
type Config struct {
	DefaultAgent   string `json:"default_agent"`
	ChromeHeadless bool   `json:"chrome_headless"`
	// MCPTools toggles groups of built-in MCP tools. All are opt-in.
	MCPTools MCPToolsConfig `json:"mcp_tools"`
	// SuggestNextActions controls whether new sessions get a system-prompt
	// instruction asking the model to end each reply with a <next-actions> block
	// (rendered as quick-reply buttons). Pointer so "absent" defaults to ON —
	// nil means enabled; only an explicit false disables it.
	SuggestNextActions *bool `json:"suggest_next_actions,omitempty"`
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
			m.cfg = Config{DefaultAgent: "claude", ChromeHeadless: true}
			return m, m.save()
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, &m.cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
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
