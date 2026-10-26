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
	// Backends advertising the capability register their own opaque control
	// closure. Others fall back to ordinary queueing. Each registration is
	// stamped with a generation so a finishing turn's cleanup can't remove an
	// injector the NEXT turn already registered (the old turn's deferred
	// removal runs after processNextQueued has started the successor).
	interjectMap     map[string]interjectorEntry
	interjectNextGen uint64
}

type interjectorEntry struct {
	fn  func(content string) error
	gen uint64
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
		interjectMap: make(map[string]interjectorEntry),
	}
}

// SetInterjector registers the live message-injection function for a session
// and returns its generation token. Called by the agent backend once its
// active turn can accept more input. The caller passes the token back to
// RemoveInterjector so it can only unregister its own registration.
func (sm *SessionManager) SetInterjector(sessionID string, fn func(content string) error) uint64 {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.interjectNextGen++
	sm.interjectMap[sessionID] = interjectorEntry{fn: fn, gen: sm.interjectNextGen}
	return sm.interjectNextGen
}

// RemoveInterjector clears a session's injection function (turn finished),
// but only when it still belongs to the given generation — a successor turn
// may already have registered its own injector. gen 0 (never registered)
// never matches.
func (sm *SessionManager) RemoveInterjector(sessionID string, gen uint64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if entry, ok := sm.interjectMap[sessionID]; ok && entry.gen == gen {
		delete(sm.interjectMap, sessionID)
	}
}

// Interject delivers content to a session's running turn. It returns
// (false, nil) when no live injector is registered (idle session, or a backend
// that doesn't support interjection) so the caller can fall back to queueing.
// A non-nil error means an injector existed but the write failed (e.g. the turn
// ended and the pipe closed) — the caller should likewise fall back.
func (sm *SessionManager) Interject(sessionID, content string) (bool, error) {
	sm.mu.RLock()
	entry, ok := sm.interjectMap[sessionID]
	sm.mu.RUnlock()
	if !ok {
		return false, nil
	}
	return true, entry.fn(content)
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

// TrySetActive atomically claims an idle session for a new turn. It returns
// false when another turn already owns the session. Callers that decide
// whether to start or queue work must use this instead of a separate
// IsActive/SetActive pair, which races under concurrent dispatches.
func (sm *SessionManager) TrySetActive(sessionID string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if info, ok := sm.sessions[sessionID]; ok {
		if info.IsActive {
			return false
		}
		info.IsActive = true
		return true
	}
	sm.sessions[sessionID] = &SessionInfo{
		SessionID: sessionID,
		IsActive:  true,
	}
	return true
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
