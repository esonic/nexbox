package api

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// TerminalManager tracks active PTYs keyed by session ID for use by the
// run_in_terminal MCP tool. Each session has its own independent PTY.
type TerminalManager struct {
	mu    sync.RWMutex
	ptmxs map[string]*os.File
}

func NewTerminalManager() *TerminalManager {
	return &TerminalManager{ptmxs: make(map[string]*os.File)}
}

// Register associates a PTY with a session ID.
func (m *TerminalManager) Register(sessionID string, ptmx *os.File) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ptmxs[sessionID] = ptmx
}

// Unregister removes the PTY for a session ID.
func (m *TerminalManager) Unregister(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.ptmxs, sessionID)
}

// WriteCommand writes a command string (with trailing newline) to the session's PTY.
func (m *TerminalManager) WriteCommand(sessionID, command string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ptmx := m.ptmxs[sessionID]
	if ptmx == nil {
		return fmt.Errorf("no active terminal for session %s", sessionID)
	}
	_, err := ptmx.Write([]byte(command + "\n"))
	return err
}

// WaitReady blocks until a PTY for the session is registered or context is cancelled.
func (m *TerminalManager) WaitReady(ctx context.Context, sessionID string) error {
	for {
		m.mu.RLock()
		ready := m.ptmxs[sessionID] != nil
		m.mu.RUnlock()
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
