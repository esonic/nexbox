package ws

import (
	"log"
	"sync"

	"github.com/gorilla/websocket"
)

// clientConn wraps a WebSocket connection with its own write mutex and the
// authenticated user that owns this connection (0 for legacy/unauthenticated).
type clientConn struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	userID  int64
}

// Hub is the base WebSocket hub that manages client connections and
// provides serialized broadcasting.
type Hub struct {
	Label   string // log prefix, e.g. "hub" or "connect-hub"
	mu      sync.RWMutex
	clients map[*websocket.Conn]*clientConn
}

// NewHub creates a new base hub with the given log label.
func NewHub(label string) Hub {
	return Hub{
		Label:   label,
		clients: make(map[*websocket.Conn]*clientConn),
	}
}

// AddClient registers a new WebSocket connection. userID identifies the
// authenticated user; pass 0 only for endpoints that intentionally serve
// unauthenticated traffic.
func (h *Hub) AddClient(conn *websocket.Conn, userID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[conn] = &clientConn{conn: conn, userID: userID}
	log.Printf("[%s] Client added (%d total)", h.Label, len(h.clients))
}

// RemoveClient unregisters a WebSocket connection.
func (h *Hub) RemoveClient(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, conn)
	log.Printf("[%s] Client removed (%d remaining)", h.Label, len(h.clients))
}

// ClientCount returns the number of connected clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// BroadcastRaw sends pre-serialized data to all connected clients concurrently.
func (h *Hub) BroadcastRaw(data []byte) {
	h.broadcastFiltered(data, 0)
}

// BroadcastRawToUser sends pre-serialized data only to connections owned by
// the given user. userID must be > 0; pass 0 to broadcast to everyone via
// BroadcastRaw instead.
func (h *Hub) BroadcastRawToUser(data []byte, userID int64) {
	if userID <= 0 {
		return
	}
	h.broadcastFiltered(data, userID)
}

// broadcastFiltered fans data out to all connections (when userID == 0) or
// only to connections whose userID matches.
func (h *Hub) broadcastFiltered(data []byte, userID int64) {
	h.mu.RLock()
	clients := make([]*clientConn, 0, len(h.clients))
	for _, client := range h.clients {
		if userID != 0 && client.userID != userID {
			continue
		}
		clients = append(clients, client)
	}
	h.mu.RUnlock()

	if len(clients) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, client := range clients {
		wg.Add(1)
		go func(c *clientConn) {
			defer wg.Done()
			c.writeMu.Lock()
			defer c.writeMu.Unlock()
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				log.Printf("[%s] Write error: %v", h.Label, err)
			}
		}(client)
	}
	wg.Wait()
}

// SendRaw sends pre-serialized data to a single client.
func (h *Hub) SendRaw(conn *websocket.Conn, data []byte) {
	h.mu.RLock()
	client, ok := h.clients[conn]
	h.mu.RUnlock()

	if !ok {
		log.Printf("[%s] SendRaw: connection not found", h.Label)
		return
	}

	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	if err := client.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("[%s] Direct write error: %v", h.Label, err)
	}
}
