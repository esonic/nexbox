package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/store"
)

type activityBoundaryAgent struct {
	started chan struct{}
	release chan struct{}
}

func (*activityBoundaryAgent) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (a *activityBoundaryAgent) SendMessage(ctx context.Context, req agent.Request, cb agent.EventCallback) (agent.Result, error) {
	cb(agent.StreamEvent{Type: agent.EventTextDelta, Content: "reply"})
	close(a.started)
	select {
	case <-a.release:
	case <-ctx.Done():
		return agent.Result{}, ctx.Err()
	}
	return agent.Result{SessionID: req.SessionID}, nil
}

func TestDispatchUpdatesActivityAtCompletion(t *testing.T) {
	s := newRelayServer(t)
	backend := &activityBoundaryAgent{started: make(chan struct{}), release: make(chan struct{})}
	s.Agent, s.AgentName = backend, "activity-test"
	s.Agents = map[string]agent.Agent{"activity-test": backend}
	project, err := s.Store.CreateProject(store.CreateProjectRequest{Name: "activity", GitRepoPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.Store.CreateSession(store.CreateSessionRequest{ProjectID: project.ID, Agent: "activity-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Store.UpdateSessionAgentID(session.ID, "established-session"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(backend.release) }) }
	outcome, err := s.dispatchUserMessage(session.ID, "hello", nil, nil, nil, dispatchOpts{
		noQueue: true,
	})
	go func() {
		defer close(done)
		for s.SessionManager.IsActive(session.ID) {
			time.Sleep(time.Millisecond)
		}
	}()
	if err != nil || outcome != dispatchStarted {
		t.Fatalf("dispatch = %v, err=%v", outcome, err)
	}
	t.Cleanup(func() {
		release()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})
	select {
	case <-backend.started:
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not start")
	}
	sent, err := s.Store.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sent.LastActiveAt.After(session.LastActiveAt) {
		t.Fatal("user message did not update activity")
	}
	// Saving a partially streamed reply and editing metadata while it runs must
	// leave the displayed activity and therefore automatic sort order unchanged.
	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.Role == "assistant" {
			if err := s.Store.UpdateMessageParts(message.ID, []MessagePart{{Type: "text", Content: "partial reply"}}, MessageMetadata{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	name := "renamed while streaming"
	if _, err := s.Store.UpdateSession(session.ID, UpdateSessionRequest{Name: &name}); err != nil {
		t.Fatal(err)
	}
	streaming, err := s.Store.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !streaming.LastActiveAt.Equal(sent.LastActiveAt) {
		t.Fatal("activity changed during streaming")
	}
	release()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not complete")
	}
	finished, err := s.Store.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !finished.LastActiveAt.After(sent.LastActiveAt) {
		t.Fatal("completion did not update activity")
	}
}
