package api

import "sync"

// interjectQueue serializes work per session in strict arrival order.
//
// Interjections must run off the WS read loop (a Codex steer is a network
// round-trip that would stall every frame on the connection), but a plain
// per-session mutex acquired inside spawned goroutines loses arrival order —
// goroutine scheduling doesn't hand the lock out FIFO. Instead the read loop
// enqueues synchronously (so slice order IS arrival order) and a single
// drainer goroutine per session executes items sequentially. The drainer
// exits and the map entry is removed once the queue is empty, so idle
// sessions hold no state.
//
// The zero value is ready to use.
type interjectQueue struct {
	mu     sync.Mutex
	queues map[string][]func()
}

// enqueue appends fn for the session and starts a drainer if none is running.
// Callers that need cross-call ordering must call enqueue from one goroutine
// (the WS read loop).
func (q *interjectQueue) enqueue(sessionID string, fn func()) {
	q.mu.Lock()
	if q.queues == nil {
		q.queues = make(map[string][]func())
	}
	pending, running := q.queues[sessionID]
	q.queues[sessionID] = append(pending, fn)
	q.mu.Unlock()
	if running {
		// The key was present, so a drainer is still alive: it only deletes
		// the key (under q.mu) right before exiting.
		return
	}
	go func() {
		for {
			q.mu.Lock()
			items := q.queues[sessionID]
			if len(items) == 0 {
				delete(q.queues, sessionID)
				q.mu.Unlock()
				return
			}
			next := items[0]
			q.queues[sessionID] = items[1:]
			q.mu.Unlock()
			next()
		}
	}()
}
