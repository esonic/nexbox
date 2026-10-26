package api

import (
	"sync"
	"testing"
	"time"
)

// TestInterjectQueue_FIFOPerSession: items enqueued from one goroutine must
// execute in exactly that order within a session, even when handlers are slow
// enough that naive goroutine-per-item dispatch would interleave.
func TestInterjectQueue_FIFOPerSession(t *testing.T) {
	var q interjectQueue
	const n = 200

	var mu sync.Mutex
	got := make(map[string][]int)
	var wg sync.WaitGroup
	wg.Add(2 * n)

	for i := 0; i < n; i++ {
		for _, sid := range []string{"a", "b"} {
			i, sid := i, sid
			q.enqueue(sid, func() {
				mu.Lock()
				got[sid] = append(got[sid], i)
				mu.Unlock()
				wg.Done()
			})
		}
	}
	waitDone := make(chan struct{})
	go func() { wg.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(10 * time.Second):
		t.Fatal("queue did not drain")
	}

	for _, sid := range []string{"a", "b"} {
		if len(got[sid]) != n {
			t.Fatalf("session %s executed %d items, want %d", sid, len(got[sid]), n)
		}
		for i, v := range got[sid] {
			if v != i {
				t.Fatalf("session %s executed out of order at %d: got %d", sid, i, v)
			}
		}
	}
}

// TestInterjectQueue_PanicDoesNotWedgeSession: a panicking handler must not
// kill the drainer permanently — the queue must recover and keep processing
// later items for that session (and not crash the process).
func TestInterjectQueue_PanicDoesNotWedgeSession(t *testing.T) {
	var q interjectQueue
	q.enqueue("s", func() { panic("boom") })

	ran := make(chan struct{}, 1)
	q.enqueue("s", func() { ran <- struct{}{} })

	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("drainer wedged after a handler panic: later item never ran")
	}
}

// TestInterjectQueue_CleansUpWhenDrained: an idle session must not keep an
// entry in the queue map (the old per-session mutex map leaked one mutex per
// session forever).
func TestInterjectQueue_CleansUpWhenDrained(t *testing.T) {
	var q interjectQueue
	done := make(chan struct{})
	q.enqueue("s", func() { close(done) })
	<-done

	deadline := time.Now().Add(5 * time.Second)
	for {
		q.mu.Lock()
		_, exists := q.queues["s"]
		q.mu.Unlock()
		if !exists {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("drained session still holds a queue entry")
		}
		time.Sleep(time.Millisecond)
	}
}
