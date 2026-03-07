package ws

import (
	"log"
	"sync"

	"github.com/gorilla/websocket"
)

// clientConn wraps a WebSocket connection with its own write mutex
type clientConn struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
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

// AddClient registers a new WebSocket connection.
func (h *Hub) AddClient(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[conn] = &clientConn{conn: conn}
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
	h.mu.RLock()
	clients := make([]*clientConn, 0, len(h.clients))
	for _, client := range h.clients {
		clients = append(clients, client)
	}
	h.mu.RUnlock()

	if len(clients) == 0 {
		return
	}

	// Send to all clients concurrently
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
