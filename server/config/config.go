package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Config holds all configuration persisted to <data-dir>/config.json.
type Config struct {
	DefaultAgent   string `json:"default_agent"`
	ChromeHeadless bool   `json:"chrome_headless"`
	EnableFileEdit bool   `json:"enable_file_edit"`
	// SuggestNextActions controls whether new sessions get a system-prompt
	// instruction asking the model to end each reply with a <next-actions> block
	// (rendered as quick-reply buttons). Pointer so "absent" defaults to ON —
	// nil means enabled; only an explicit false disables it.
	SuggestNextActions *bool `json:"suggest_next_actions,omitempty"`
	// CrushModels is the Nexbox-owned picker for the Crush native adapter.
	// Provider and ID are passed to Crush as its selected model; no provider
	// secret is stored here.
	CrushModels []CrushModel `json:"crush_models,omitempty"`
}

type CrushModel struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Label    string `json:"label,omitempty"`
}

func defaultCrushModels() []CrushModel {
	return []CrushModel{{
		ID:       "minimax/minimax-m3:free",
		Provider: "openrouter",
		Label:    "minimax/minimax-m3:free",
	}}
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
			m.cfg = Config{DefaultAgent: "claude", ChromeHeadless: true, CrushModels: defaultCrushModels()}
			return m, m.save()
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, &m.cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// Backfill installations created before Crush support. A non-nil empty
	// list remains an explicit admin choice to expose no Crush models.
	if m.cfg.CrushModels == nil {
		m.cfg.CrushModels = defaultCrushModels()
		if err := m.save(); err != nil {
			return nil, err
		}
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
