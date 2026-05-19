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
}

func NewChatHub() *ChatHub {
	return &ChatHub{
		Hub:         ws.NewHub("hub"),
		active:      make(map[string]bool),
		subscribers: make(map[string]map[uint64]SessionEventHandler),
	}
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

// Emit broadcasts an event to ALL connected clients (tagged with sessionID),
// and also notifies server-side subscribers for this session.
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

	h.BroadcastRaw(data)

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

// NotifyChange broadcasts a data-change event to all connected clients.
// This is used by API handlers and MCP tools to notify the frontend
// when assets, tasks, or other entities are created/updated/deleted.
func (h *ChatHub) NotifyChange(entityType, entityID, action string) {
	data, _ := json.Marshal(map[string]interface{}{
		"type":        "change",
		"entity_type": entityType,
		"entity_id":   entityID,
		"action":      action,
	})
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
