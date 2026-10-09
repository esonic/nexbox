package api

import (
	"context"
	"testing"
	"time"

	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/store"
)

// failedAgentToolBackend mirrors the Codex top-level sub-agent failure path:
// the Agent tool starts normally and terminates through EventToolResult with
// IsError=true.
type failedAgentToolBackend struct{}

func (failedAgentToolBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }

func (failedAgentToolBackend) SendMessage(_ context.Context, req agent.Request, cb agent.EventCallback) (agent.Result, error) {
	cb(agent.StreamEvent{
		Type:     agent.EventToolStart,
		ToolID:   "agent-failure",
		ToolName: "Agent",
		Input:    `{"name":"reviewer"}`,
	})
	cb(agent.StreamEvent{
		Type:    agent.EventToolResult,
		ToolID:  "agent-failure",
		Output:  "child failed",
		IsError: true,
	})
	return agent.Result{SessionID: req.SessionID}, nil
}

func TestDispatchPersistsFailedAgentToolAsError(t *testing.T) {
	s := newRelayServer(t)
	s.Agent = failedAgentToolBackend{}
	s.AgentName = "codex"
	s.Agents = map[string]agent.Agent{"codex": s.Agent}

	project, err := s.Store.CreateProject(store.CreateProjectRequest{
		Name:        "tool-result-state",
		GitRepoPath: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.Store.CreateSession(store.CreateSessionRequest{
		Name:      "tool-result-state",
		ProjectID: project.ID,
		Agent:     "codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Avoid first-turn static configuration; this test exercises only event
	// persistence and uses an already-established backend session.
	if err := s.Store.UpdateSessionAgentID(session.ID, "codex-session"); err != nil {
		t.Fatal(err)
	}

	outcome, err := s.dispatchUserMessage(session.ID, "run child", nil, nil, nil, dispatchOpts{noQueue: true})
	if err != nil {
		t.Fatalf("dispatchUserMessage: %v", err)
	}
	if outcome != dispatchStarted {
		t.Fatalf("dispatch outcome = %v, want dispatchStarted", outcome)
	}

	deadline := time.Now().Add(3 * time.Second)
	for s.SessionManager.IsActive(session.ID) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.SessionManager.IsActive(session.ID) {
		t.Fatal("agent turn did not finish")
	}

	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	var toolUse *MessagePart
	var toolResult *MessagePart
	for i := range messages {
		if messages[i].Role != "assistant" {
			continue
		}
		for j := range messages[i].Parts {
			part := &messages[i].Parts[j]
			switch {
			case part.Type == "tool_use" && part.ToolCallID == "agent-failure":
				toolUse = part
			case part.Type == "tool_result" && part.ToolCallID == "agent-failure":
				toolResult = part
			}
		}
	}
	if toolUse == nil || toolUse.State != "error" {
		t.Fatalf("persisted Agent tool state = %+v, want error", toolUse)
	}
	if toolResult == nil || !toolResult.IsError || toolResult.Output != "child failed" {
		t.Fatalf("persisted Agent tool result = %+v", toolResult)
	}
}
