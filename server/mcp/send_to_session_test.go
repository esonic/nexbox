package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

// fakeOrchestrator records what the send_to_session handler asked for and hands
// back a canned reply.
type fakeOrchestrator struct {
	resolveErr error
	createErr  error

	createCalls     int
	gotCreateFrom   string
	gotCreateName   string
	gotCreateFirstM string
	gotCreateAgent  string
	gotCreateModel  string

	gotFrom        string
	gotResolveFrom string
	gotTarget      string
	gotMessage     string

	reply    string
	replyErr error
	// block, when non-nil, holds the call until closed — standing in for the
	// target session taking its time to answer.
	block chan struct{}
}

func (f *fakeOrchestrator) OrchestrateCreateSession(name, projectID, agent string) (string, error) {
	return "", errors.New("not used")
}
func (f *fakeOrchestrator) OrchestrateSendMessage(sessionID, message string) error { return nil }
func (f *fakeOrchestrator) OrchestrateQueueMessage(sessionID, message string) error {
	return nil
}
func (f *fakeOrchestrator) OrchestrateWaitDone(ctx context.Context, sessionID string) (string, error) {
	return "", nil
}

func (f *fakeOrchestrator) OrchestrateResolveSession(fromSessionID, ref string) (string, string, error) {
	f.gotResolveFrom = fromSessionID
	if f.resolveErr != nil {
		return "", "", f.resolveErr
	}
	return "target-session-id", "Target Session", nil
}

func (f *fakeOrchestrator) OrchestrateCreateSiblingSession(fromSessionID, name, firstMessage, agentName, model string) (string, string, error) {
	f.createCalls++
	f.gotCreateFrom, f.gotCreateName, f.gotCreateFirstM = fromSessionID, name, firstMessage
	f.gotCreateAgent, f.gotCreateModel = agentName, model
	if f.createErr != nil {
		return "", "", f.createErr
	}
	return "new-session-id", "New Session", nil
}

func (f *fakeOrchestrator) OrchestrateSendAndWait(ctx context.Context, fromSessionID, targetSessionID, message string) (string, error) {
	f.gotFrom, f.gotTarget, f.gotMessage = fromSessionID, targetSessionID, message
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return f.reply, f.replyErr
}

// newTestMCPClient boots the MCP server on a random port and returns a client
// connected to it as session `callerSessionID`.
func newTestMCPClient(t *testing.T, orch SessionOrchestrator, callerSessionID string) *mcpclient.Client {
	t.Helper()

	srv := NewServer(nil, "127.0.0.1:0")
	srv.SetSessionOrchestrator(orch)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	url := srv.URL()
	if callerSessionID != "" {
		url += "?session=" + callerSessionID
	}
	c, err := mcpclient.NewStreamableHttpClient(url)
	if err != nil {
		t.Fatalf("NewStreamableHttpClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("client Start: %v", err)
	}
	if _, err := c.Initialize(ctx, gomcp.InitializeRequest{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return c
}

func callSendToSession(t *testing.T, c *mcpclient.Client, ctx context.Context, args map[string]any) *gomcp.CallToolResult {
	t.Helper()
	req := gomcp.CallToolRequest{}
	req.Params.Name = "send_to_session"
	req.Params.Arguments = args
	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	return res
}

func resultText(res *gomcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(gomcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// The tool must be advertised with only message required.
func TestSendToSession_IsAdvertised(t *testing.T) {
	c := newTestMCPClient(t, &fakeOrchestrator{}, "caller-session")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	list, err := c.ListTools(ctx, gomcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	var found *gomcp.Tool
	for i := range list.Tools {
		if list.Tools[i].Name == "send_to_session" {
			found = &list.Tools[i]
			break
		}
	}
	if found == nil {
		t.Fatal("send_to_session is not advertised")
	}
	required := map[string]bool{}
	for _, r := range found.InputSchema.Required {
		required[r] = true
	}
	if !required["message"] {
		t.Errorf("message should be required, required=%v", found.InputSchema.Required)
	}
	if required["session_id"] {
		t.Errorf("session_id must be optional so a new session can be created, required=%v", found.InputSchema.Required)
	}
	for _, want := range []string{"session_id", "new_session_title", "agent", "model"} {
		if _, ok := found.InputSchema.Properties[want]; !ok {
			t.Errorf("parameter %q is not advertised", want)
		}
	}

	srv := NewServer(nil, "127.0.0.1:0")
	var advertised bool
	for _, n := range srv.ToolNames() {
		if n == "mcp__nexbox__send_to_session" {
			advertised = true
		}
	}
	if !advertised {
		t.Error("send_to_session missing from ToolNames(), so agents would not be allowed to call it")
	}
}

// A round trip: the caller's session ID is taken from ?session=, the reply comes
// back as the tool result.
func TestSendToSession_ReturnsTargetReply(t *testing.T) {
	orch := &fakeOrchestrator{reply: "the answer is 42"}
	c := newTestMCPClient(t, orch, "caller-session")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res := callSendToSession(t, c, ctx, map[string]any{
		"session_id": "ABC123",
		"message":    "what is the answer?",
	})

	if res.IsError {
		t.Fatalf("unexpected error result: %s", resultText(res))
	}
	if orch.gotFrom != "caller-session" {
		t.Errorf("caller session = %q, want %q — ?session= was not threaded through", orch.gotFrom, "caller-session")
	}
	if orch.gotResolveFrom != "caller-session" {
		t.Errorf("resolve caller session = %q, want %q", orch.gotResolveFrom, "caller-session")
	}
	if orch.gotTarget != "target-session-id" {
		t.Errorf("target = %q, want the resolved ID", orch.gotTarget)
	}
	if orch.gotMessage != "what is the answer?" {
		t.Errorf("message = %q, want it delivered verbatim", orch.gotMessage)
	}
	if text := resultText(res); !strings.Contains(text, "the answer is 42") {
		t.Errorf("result = %q, want it to carry the reply", text)
	}
}

// A tool call that blocks for longer than a quick request must still complete
// over the streamable-HTTP transport rather than being cut off.
func TestSendToSession_SurvivesASlowReply(t *testing.T) {
	orch := &fakeOrchestrator{reply: "took a while", block: make(chan struct{})}
	c := newTestMCPClient(t, orch, "caller-session")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan *gomcp.CallToolResult, 1)
	go func() {
		done <- callSendToSession(t, c, ctx, map[string]any{"session_id": "ABC123", "message": "hi"})
	}()

	select {
	case res := <-done:
		t.Fatalf("tool returned before the target replied: %s", resultText(res))
	case <-time.After(3 * time.Second):
	}

	close(orch.block)
	select {
	case res := <-done:
		if res.IsError {
			t.Fatalf("unexpected error result: %s", resultText(res))
		}
		if text := resultText(res); !strings.Contains(text, "took a while") {
			t.Errorf("result = %q, want the delayed reply", text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tool never returned after the target replied")
	}
}

// The regression that produced "transport dropped mid-call; response for tool
// send_to_session was lost": while the tool blocks, the transport must not sit
// silent. The client has to see the response start — and keep seeing traffic —
// long before any HTTP client would give up waiting for headers.
func TestSendToSession_KeepsTransportAliveWhileBlocked(t *testing.T) {
	orch := &fakeOrchestrator{reply: "finally", block: make(chan struct{})}
	c := newTestMCPClient(t, orch, "caller-session")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	got := make(chan gomcp.JSONRPCNotification, 16)
	c.OnNotification(func(n gomcp.JSONRPCNotification) {
		select {
		case got <- n:
		default:
		}
	})

	done := make(chan *gomcp.CallToolResult, 1)
	go func() {
		done <- callSendToSession(t, c, ctx, map[string]any{"session_id": "ABC123", "message": "hi"})
	}()

	// The first one goes out immediately, which is what flushes the response
	// headers and stops a header timeout from ever firing.
	select {
	case n := <-got:
		if n.Method != "notifications/progress" {
			t.Fatalf("first notification method = %q, want notifications/progress", n.Method)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no traffic on the transport while the tool was blocked — the response would be dropped")
	}

	// And traffic keeps coming for as long as the wait lasts.
	select {
	case <-got:
	case <-time.After(mcpKeepAliveInterval + 10*time.Second):
		t.Fatalf("keep-alive stopped after the first notification; expected one every %s", mcpKeepAliveInterval)
	}

	close(orch.block)
	select {
	case res := <-done:
		if res.IsError {
			t.Fatalf("unexpected error result: %s", resultText(res))
		}
		if text := resultText(res); !strings.Contains(text, "finally") {
			t.Errorf("result = %q, want the reply to survive the SSE upgrade", text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tool never returned")
	}
}

// A failure on the target side is reported back to the caller, not swallowed.
func TestSendToSession_ReportsFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Run("target failed", func(t *testing.T) {
		orch := &fakeOrchestrator{replyErr: errors.New("session stopped without answering")}
		c := newTestMCPClient(t, orch, "caller-session")
		res := callSendToSession(t, c, ctx, map[string]any{"session_id": "ABC123", "message": "hi"})
		if !res.IsError {
			t.Fatal("a failed relay must produce an error result")
		}
		if text := resultText(res); !strings.Contains(text, "session stopped without answering") {
			t.Errorf("result = %q, want the underlying reason", text)
		}
	})

	t.Run("unknown session", func(t *testing.T) {
		orch := &fakeOrchestrator{resolveErr: errors.New(`no session matches "ZZZZZZ"`)}
		c := newTestMCPClient(t, orch, "caller-session")
		res := callSendToSession(t, c, ctx, map[string]any{"session_id": "ZZZZZZ", "message": "hi"})
		if !res.IsError {
			t.Fatal("an unresolvable session must produce an error result")
		}
		if text := resultText(res); !strings.Contains(text, "no session matches") {
			t.Errorf("result = %q, want the resolve error", text)
		}
	})

	t.Run("empty message", func(t *testing.T) {
		orch := &fakeOrchestrator{}
		c := newTestMCPClient(t, orch, "caller-session")
		res := callSendToSession(t, c, ctx, map[string]any{"session_id": "ABC123", "message": "   "})
		if !res.IsError {
			t.Fatal("an empty message must be rejected")
		}
		if orch.gotTarget != "" {
			t.Error("an empty message must not reach the target session")
		}
	})

	t.Run("missing calling session", func(t *testing.T) {
		orch := &fakeOrchestrator{}
		c := newTestMCPClient(t, orch, "")
		res := callSendToSession(t, c, ctx, map[string]any{"session_id": "ABC123", "message": "hi"})
		if !res.IsError {
			t.Fatal("a relay without the calling session context must be rejected")
		}
		if orch.gotResolveFrom != "" || orch.gotTarget != "" {
			t.Fatal("a missing calling session must be rejected before target resolution")
		}
	})
}

// Omitting session_id creates a sibling session and relays the message there.
func TestSendToSession_CreatesSessionWhenIDOmitted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Run("creates and relays", func(t *testing.T) {
		orch := &fakeOrchestrator{reply: "done"}
		c := newTestMCPClient(t, orch, "caller-session")
		res := callSendToSession(t, c, ctx, map[string]any{"message": "do the thing", "new_session_title": "Helper"})
		if res.IsError {
			t.Fatalf("unexpected error result: %s", resultText(res))
		}
		if orch.createCalls != 1 || orch.gotCreateFrom != "caller-session" || orch.gotCreateName != "Helper" || orch.gotCreateFirstM != "do the thing" {
			t.Errorf("create calls=%d from=%q name=%q msg=%q", orch.createCalls, orch.gotCreateFrom, orch.gotCreateName, orch.gotCreateFirstM)
		}
		if orch.gotResolveFrom != "" {
			t.Error("resolve must not be called when creating a session")
		}
		if orch.gotTarget != "new-session-id" || orch.gotMessage != "do the thing" {
			t.Errorf("relayed to %q with %q", orch.gotTarget, orch.gotMessage)
		}
		text := resultText(res)
		if !strings.Contains(text, "Created new session") || !strings.Contains(text, "new-session-id") || !strings.Contains(text, "done") {
			t.Errorf("result = %q, want creation note, ID and reply", text)
		}
	})

	t.Run("agent and model are forwarded", func(t *testing.T) {
		orch := &fakeOrchestrator{reply: "ok"}
		c := newTestMCPClient(t, orch, "caller-session")
		res := callSendToSession(t, c, ctx, map[string]any{"message": "hi", "agent": "codex", "model": "gpt-5"})
		if res.IsError {
			t.Fatalf("unexpected error result: %s", resultText(res))
		}
		if orch.gotCreateAgent != "codex" || orch.gotCreateModel != "gpt-5" {
			t.Errorf("create agent=%q model=%q", orch.gotCreateAgent, orch.gotCreateModel)
		}
	})

	t.Run("explicit id does not create", func(t *testing.T) {
		orch := &fakeOrchestrator{reply: "ok"}
		c := newTestMCPClient(t, orch, "caller-session")
		res := callSendToSession(t, c, ctx, map[string]any{"session_id": "ABC123", "message": "hi", "new_session_title": "x", "agent": "codex", "model": "m"})
		if res.IsError {
			t.Fatalf("unexpected error result: %s", resultText(res))
		}
		if orch.createCalls != 0 {
			t.Error("an explicit session_id must not create a session")
		}
		if strings.Contains(resultText(res), "Created new session") {
			t.Error("result must not claim a session was created")
		}
	})

	t.Run("create failure", func(t *testing.T) {
		orch := &fakeOrchestrator{createErr: errors.New("calling session has no project")}
		c := newTestMCPClient(t, orch, "caller-session")
		res := callSendToSession(t, c, ctx, map[string]any{"message": "hi"})
		if !res.IsError || !strings.Contains(resultText(res), "no project") {
			t.Fatalf("want error carrying the reason, got %q", resultText(res))
		}
		if orch.gotTarget != "" {
			t.Error("nothing may be relayed when creation fails")
		}
	})

	t.Run("unknown caller", func(t *testing.T) {
		orch := &fakeOrchestrator{}
		c := newTestMCPClient(t, orch, "")
		res := callSendToSession(t, c, ctx, map[string]any{"message": "hi"})
		if !res.IsError || orch.createCalls != 0 {
			t.Fatal("creation without a calling session must be rejected")
		}
	})
}
