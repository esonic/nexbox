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
	// interjectMap holds, per active session, a function that injects an extra
	// user message into the running agent turn (queue-jump / "interjection").
	// Only backends that keep a live stdin during the turn register one (Claude);
	// Codex never does, so its sessions fall back to ordinary queueing.
	interjectMap map[string]func(content string) error
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
		sessions:     make(map[string]*SessionInfo),
		cancelMap:    make(map[string]context.CancelFunc),
		interjectMap: make(map[string]func(content string) error),
	}
}

// SetInterjector registers the live message-injection function for a session.
// Called by the agent backend once its stdin is ready to accept additional
// user messages mid-turn.
func (sm *SessionManager) SetInterjector(sessionID string, fn func(content string) error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.interjectMap[sessionID] = fn
}

// RemoveInterjector clears a session's injection function (turn finished).
func (sm *SessionManager) RemoveInterjector(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.interjectMap, sessionID)
}

// Interject delivers content to a session's running turn. It returns
// (false, nil) when no live injector is registered (idle session, or a backend
// that doesn't support interjection) so the caller can fall back to queueing.
// A non-nil error means an injector existed but the write failed (e.g. the turn
// ended and the pipe closed) — the caller should likewise fall back.
func (sm *SessionManager) Interject(sessionID, content string) (bool, error) {
	sm.mu.RLock()
	fn, ok := sm.interjectMap[sessionID]
	sm.mu.RUnlock()
	if !ok {
		return false, nil
	}
	return true, fn(content)
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
