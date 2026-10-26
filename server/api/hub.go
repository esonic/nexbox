package api

import (
	"encoding/json"
	"log"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/esonic/nexbox/ws"
)

// TaggedEvent is a ChatEvent annotated with session_id for WS routing.
type TaggedEvent struct {
	ChatEvent
	SessionID string `json:"session_id"`
}

// SessionEventHandler is a callback for server-side session event subscriptions.
type SessionEventHandler func(event ChatEvent)

// ChatHub manages WebSocket clients and broadcasts session events to all connected clients.
// Embeds ws.Hub for shared connection management.
type ChatHub struct {
	ws.Hub
	activeMu sync.RWMutex
	active   map[string]bool // sessionID -> agent is running

	subMu       sync.RWMutex
	subscribers map[string]map[uint64]SessionEventHandler
	subCounter  uint64

	// SessionOwnerLookup resolves a session id to the user id that owns the
	// session's project. Set from main.go after the store is wired up. When
	// it returns 0 (unknown session) the hub falls back to broadcasting —
	// safer than silently swallowing system events for sessions whose owner
	// can't be resolved (e.g. very early startup, transient DB issues).
	SessionOwnerLookup func(sessionID string) int64

	// ownerCache memoizes sessionID → ownerUserID so the hot Emit path
	// avoids a DB round-trip per text-delta event. Sessions can be moved
	// between projects, so callers that change ownership should invoke
	// InvalidateSessionOwner.
	ownerCache sync.Map // sessionID(string) -> int64
}

func NewChatHub() *ChatHub {
	return &ChatHub{
		Hub:         ws.NewHub("hub"),
		active:      make(map[string]bool),
		subscribers: make(map[string]map[uint64]SessionEventHandler),
	}
}

// sessionOwnerID returns the owner user id for a session, consulting the
// cache first and falling back to SessionOwnerLookup. Returns 0 when the
// owner can't be determined.
func (h *ChatHub) sessionOwnerID(sessionID string) int64 {
	if sessionID == "" || h.SessionOwnerLookup == nil {
		return 0
	}
	if v, ok := h.ownerCache.Load(sessionID); ok {
		return v.(int64)
	}
	uid := h.SessionOwnerLookup(sessionID)
	if uid > 0 {
		h.ownerCache.Store(sessionID, uid)
	}
	return uid
}

// InvalidateSessionOwner drops a cached sessionID → ownerUserID mapping. Call
// after moving a session to a different project, or on delete.
func (h *ChatHub) InvalidateSessionOwner(sessionID string) {
	h.ownerCache.Delete(sessionID)
}

// --- Active state tracking ---

func (h *ChatHub) SetActive(sessionID string) {
	h.activeMu.Lock()
	defer h.activeMu.Unlock()
	h.active[sessionID] = true
}

func (h *ChatHub) ClearActive(sessionID string) {
	h.activeMu.Lock()
	defer h.activeMu.Unlock()
	delete(h.active, sessionID)
}

func (h *ChatHub) IsActive(sessionID string) bool {
	h.activeMu.RLock()
	defer h.activeMu.RUnlock()
	return h.active[sessionID]
}

func (h *ChatHub) ActiveSessionIDs() []string {
	h.activeMu.RLock()
	defer h.activeMu.RUnlock()
	ids := make([]string, 0, len(h.active))
	for id := range h.active {
		ids = append(ids, id)
	}
	return ids
}

// --- Server-side session subscriptions ---

// SubscribeSession registers a server-side handler for a session's events.
// Returns an unsubscribe function.
func (h *ChatHub) SubscribeSession(sessionID string, handler SessionEventHandler) func() {
	h.subMu.Lock()
	h.subCounter++
	id := h.subCounter
	if h.subscribers[sessionID] == nil {
		h.subscribers[sessionID] = make(map[uint64]SessionEventHandler)
	}
	h.subscribers[sessionID][id] = handler
	h.subMu.Unlock()

	return func() {
		h.subMu.Lock()
		defer h.subMu.Unlock()
		delete(h.subscribers[sessionID], id)
		if len(h.subscribers[sessionID]) == 0 {
			delete(h.subscribers, sessionID)
		}
	}
}

// --- Event emission ---

// Emit broadcasts a session event only to WebSocket clients whose
// authenticated user owns the session's project. This is what stops one
// user's streaming text/tool/done events (and the resulting "Session
// complete" browser notification) from leaking into other users' clients.
// Server-side subscribers are notified regardless, since they are internal
// (workflow orchestrator etc.).
func (h *ChatHub) Emit(sessionID string, event ChatEvent) {
	tagged := TaggedEvent{
		ChatEvent: event,
		SessionID: sessionID,
	}

	data, err := json.Marshal(tagged)
	if err != nil {
		log.Printf("[hub] Marshal error: %v", err)
		return
	}

	if uid := h.sessionOwnerID(sessionID); uid > 0 {
		h.BroadcastRawToUser(data, uid)
	} else {
		// Unknown owner — typically only happens for sessions that don't
		// exist yet or system events. Falling back to broadcast preserves
		// existing behavior for those edge cases instead of dropping them.
		h.BroadcastRaw(data)
	}

	// Notify server-side subscribers
	h.subMu.RLock()
	handlers := make([]SessionEventHandler, 0, len(h.subscribers[sessionID]))
	for _, handler := range h.subscribers[sessionID] {
		handlers = append(handlers, handler)
	}
	h.subMu.RUnlock()

	for _, handler := range handlers {
		handler(event)
	}
}

// NotifyChange broadcasts a data-change event to connected clients.
// For "session" entities we filter to the session's owner so other users
// don't see sidebar refresh triggers for sessions they can't access.
// For task/asset/workflow entities we still broadcast to all — the API
// handlers already gate read access by user, and the change ping just
// causes a refetch on the client which is a no-op for non-owners. Refining
// these further is worthwhile follow-up work.
func (h *ChatHub) NotifyChange(entityType, entityID, action string) {
	data, _ := json.Marshal(map[string]interface{}{
		"type":        "change",
		"entity_type": entityType,
		"entity_id":   entityID,
		"action":      action,
	})
	if entityType == "session" {
		if uid := h.sessionOwnerID(entityID); uid > 0 {
			h.BroadcastRawToUser(data, uid)
			if action == "deleted" {
				h.InvalidateSessionOwner(entityID)
			}
			return
		}
	}
	h.BroadcastRaw(data)
}

func (h *ChatHub) SendDirect(conn *websocket.Conn, sessionID string, event ChatEvent) {
	tagged := TaggedEvent{
		ChatEvent: event,
		SessionID: sessionID,
	}
	data, err := json.Marshal(tagged)
	if err != nil {
		return
	}
	h.SendRaw(conn, data)
}
