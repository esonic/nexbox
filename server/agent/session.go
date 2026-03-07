package agent

import (
	"context"
	"sync"
)

// SessionManager tracks active agent sessions keyed by session ID.
type SessionManager struct {
	mu        sync.RWMutex
	sessions  map[string]*SessionInfo
	cancelMap map[string]context.CancelFunc
}

// SessionInfo stores metadata about an active session.
type SessionInfo struct {
	SessionID string
	ProjectID string
	WorkDir   string
	IsActive  bool
}

// NewSessionManager creates a session manager.
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions:  make(map[string]*SessionInfo),
		cancelMap: make(map[string]context.CancelFunc),
	}
}

// SetCancel stores the cancel function for a session (used for stop).
func (sm *SessionManager) SetCancel(sessionID string, cancel context.CancelFunc) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if old, ok := sm.cancelMap[sessionID]; ok {
		old()
	}
	sm.cancelMap[sessionID] = cancel
}

// Cancel stops the active request for a session and removes the cancel func.
func (sm *SessionManager) Cancel(sessionID string) {
	sm.mu.Lock()
	cancel, ok := sm.cancelMap[sessionID]
	if ok {
		delete(sm.cancelMap, sessionID)
	}
	sm.mu.Unlock()
	if ok {
		cancel()
	}
}

// RemoveCancel removes the cancel func without calling it (when goroutine finishes normally).
func (sm *SessionManager) RemoveCancel(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.cancelMap, sessionID)
}

// GetOrCreate returns existing session info or creates new one for a project.
func (sm *SessionManager) GetOrCreate(projectID, sessionID, workDir string) *SessionInfo {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if info, ok := sm.sessions[projectID]; ok {
		return info
	}

	info := &SessionInfo{
		SessionID: sessionID,
		ProjectID: projectID,
		WorkDir:   workDir,
		IsActive:  false,
	}
	sm.sessions[projectID] = info
	return info
}

// SetActive marks a session as active (in use).
// Auto-creates the entry when setting active=true, and removes it when setting active=false.
func (sm *SessionManager) SetActive(sessionID string, active bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if info, ok := sm.sessions[sessionID]; ok {
		info.IsActive = active
		if !active {
			delete(sm.sessions, sessionID)
		}
	} else if active {
		sm.sessions[sessionID] = &SessionInfo{
			SessionID: sessionID,
			IsActive:  true,
		}
	}
}

// IsActive checks if a session is currently active.
func (sm *SessionManager) IsActive(sessionID string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if info, ok := sm.sessions[sessionID]; ok {
		return info.IsActive
	}
	return false
}

// Remove deletes a session.
func (sm *SessionManager) Remove(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.sessions, sessionID)
}
