package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/store"
)

func newRelayServer(t *testing.T) *Server {
	t.Helper()
	db, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Server{
		Store:          store.NewSQLiteStore(db),
		Hub:            NewChatHub(),
		SessionManager: agent.NewSessionManager(),
		DataDir:        t.TempDir(),
	}
}

// ---------------------------------------------------------------------------
// Reply extraction
// ---------------------------------------------------------------------------

func TestFinalAnswerFromParts(t *testing.T) {
	tests := []struct {
		name  string
		parts []MessagePart
		want  string
	}{
		{
			name: "text after the last tool call is the answer",
			parts: []MessagePart{
				{Type: "text", Content: "Let me look."},
				{Type: "tool_use", ToolName: "Read"},
				{Type: "tool_result", Output: "..."},
				{Type: "text", Content: "The bug is in chat.go."},
			},
			want: "The bug is in chat.go.",
		},
		{
			name: "adjacent trailing text parts are joined",
			parts: []MessagePart{
				{Type: "tool_use", ToolName: "Bash"},
				{Type: "text", Content: "Done. "},
				{Type: "text", Content: "Error: agent crashed"},
			},
			want: "Done. Error: agent crashed",
		},
		{
			name: "thinking before the answer is dropped",
			parts: []MessagePart{
				{Type: "thinking", Content: "hmm"},
				{Type: "text", Content: "42"},
			},
			want: "42",
		},
		{
			name: "a turn ending on a tool call still yields its last text run",
			parts: []MessagePart{
				{Type: "text", Content: "checking"},
				{Type: "tool_use", ToolName: "Read"},
			},
			want: "checking",
		},
		{
			name:  "no text at all",
			parts: []MessagePart{{Type: "tool_use", ToolName: "Read"}},
			want:  "",
		},
		{
			name:  "empty turn",
			parts: nil,
			want:  "",
		},
		{
			name: "next-actions is stripped from the answer",
			parts: []MessagePart{
				{Type: "tool_use", ToolName: "Read", ToolCallID: "t1"},
				{Type: "text", Content: "Fixed it.\n\n<next-actions>\n- ship it\n</next-actions>"},
			},
			want: "Fixed it.",
		},
		{
			name: "sub-agent tool calls do not break the answer's text run",
			parts: []MessagePart{
				{Type: "tool_use", ToolName: "Task", ToolCallID: "agent1"},
				{Type: "text", Content: "The answer "},
				// The sub-agent's own calls are persisted flat, interleaved with
				// the main agent's text. The UI nests them out of the way.
				{Type: "tool_use", ToolName: "Grep", ToolCallID: "sub1", ParentToolID: "agent1"},
				{Type: "tool_result", ToolCallID: "sub1", Output: "hit"},
				{Type: "text", Content: "is 42."},
				{Type: "tool_result", ToolCallID: "agent1", Output: "done"},
			},
			want: "The answer is 42.",
		},
		{
			name: "a next-actions tag split across parts leaves no fragment",
			parts: []MessagePart{
				{Type: "tool_use", ToolName: "Task", ToolCallID: "agent1"},
				{Type: "text", Content: "All set.\n\n<next"},
				{Type: "tool_use", ToolName: "Grep", ToolCallID: "sub1", ParentToolID: "agent1"},
				{Type: "text", Content: "-actions>\n- ship it\n</next-actions>"},
			},
			want: "All set.",
		},
		{
			name: "a top-level tool call still ends the previous run",
			parts: []MessagePart{
				{Type: "text", Content: "first"},
				{Type: "tool_use", ToolName: "Bash", ToolCallID: "t1"},
				{Type: "text", Content: "second"},
			},
			want: "second",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := finalAnswerFromParts(tc.parts); got != tc.want {
				t.Fatalf("finalAnswerFromParts = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStripNextActionsBlocks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "trailing block is removed",
			in:   "All done.\n\n<next-actions>\n- run the tests\n</next-actions>",
			want: "All done.",
		},
		{
			name: "every block is removed",
			in:   "One.\n<next-actions>\n- a\n</next-actions>\nTwo.\n<next-actions>\n- b\n</next-actions>",
			want: "One.\nTwo.",
		},
		{
			name: "an unterminated block is removed too",
			in:   "Partial.\n\n<next-actions>\n- a",
			want: "Partial.",
		},
		{
			name: "a reply without a block is untouched",
			in:   "  spaces are preserved  ",
			want: "  spaces are preserved  ",
		},
		{
			// The bug this guards: a quoted opening tag paired with the real
			// block's closing tag, swallowing everything in between.
			name: "a tag quoted in an inline code span is not a block",
			in:   "Beware of `<next-actions>` tags.",
			want: "Beware of `<next-actions>` tags.",
		},
		{
			name: "a quoted tag is skipped but a later real block is not",
			in:   "See `<next-actions>` here.\n\n<next-actions>\n- go\n</next-actions>",
			want: "See `<next-actions>` here.",
		},
		{
			name: "a tag inside a fenced block is not a block",
			in:   "Example:\n\n```\n<next-actions>\n- not real\n</next-actions>\n```\n\n<next-actions>\n- real\n</next-actions>",
			want: "Example:\n\n```\n<next-actions>\n- not real\n</next-actions>\n```",
		},
		{
			name: "a stray backtick does not mask a later real block",
			in:   "it costs 5`C outside.\n\n<next-actions>\n- ok\n</next-actions>",
			want: "it costs 5`C outside.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripNextActionsBlocks(tc.in); got != tc.want {
				t.Fatalf("stripNextActionsBlocks = %q, want %q", got, tc.want)
			}
		})
	}
}

// A reply that quotes the tag in backticks and then ends with a real block —
// the exact shape that used to lose everything between the two — must keep its
// prose and drop only the trailing block.
func TestStripNextActionsBlocks_QuotedTagBeforeRealBlock(t *testing.T) {
	prose := "The relay strips `<next-actions>` from the answer.\n" +
		"Fenced example:\n\n```\n<next-actions>\n- sample\n</next-actions>\n```\n\nAll done."
	in := prose + "\n\n<next-actions>\n- restart the server\n- test A → B\n</next-actions>"

	got := stripNextActionsBlocks(in)
	if got != prose {
		t.Fatalf("stripNextActionsBlocks:\n got: %q\nwant: %q", got, prose)
	}
}

// The same guard on the part-aware path used to build a relayed reply.
func TestFinalAnswerFromParts_KeepsQuotedTag(t *testing.T) {
	parts := []MessagePart{
		{Type: "tool_use", ToolName: "Read", ToolCallID: "t1"},
		{Type: "text", Content: "Quoting `<next-actions>` is safe.\n\n<next-actions>\n- ship\n</next-actions>"},
	}
	want := "Quoting `<next-actions>` is safe."
	if got := finalAnswerFromParts(parts); got != want {
		t.Fatalf("finalAnswerFromParts = %q, want %q", got, want)
	}
}

func TestCodeRegions(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		inCode []string // substrings whose first byte must be inside a code region
		free   []string // substrings whose first byte must be outside
	}{
		{
			name:   "inline span",
			text:   "a `code` b",
			inCode: []string{"code"},
			free:   []string{"a ", " b"},
		},
		{
			name:   "fenced block",
			text:   "before\n```go\nfmt.Println()\n```\nafter",
			inCode: []string{"fmt.Println"},
			free:   []string{"before", "after"},
		},
		{
			name:   "backticks inside a fence do not open a span",
			text:   "```\n`x\n```\nplain `y` tail",
			inCode: []string{"`x", "y"},
			free:   []string{"plain", "tail"},
		},
		{
			name:   "an unterminated span is literal text",
			text:   "5`C then more text",
			inCode: nil,
			free:   []string{"5`C", "more"},
		},
		{
			name:   "a span may not cross a blank line",
			text:   "`open\n\nlater ` tail",
			inCode: nil,
			free:   []string{"`open", "later", "tail"},
		},
		{
			name:   "double-backtick span",
			text:   "a ``co`de`` b",
			inCode: []string{"co`de"},
			free:   []string{"b"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			regions := codeRegions(tc.text)
			for _, sub := range tc.inCode {
				at := strings.Index(tc.text, sub)
				if at < 0 {
					t.Fatalf("test bug: %q not in %q", sub, tc.text)
				}
				if !inRanges(regions, at) {
					t.Errorf("%q should be inside a code region (regions=%v)", sub, regions)
				}
			}
			for _, sub := range tc.free {
				at := strings.Index(tc.text, sub)
				if at < 0 {
					t.Fatalf("test bug: %q not in %q", sub, tc.text)
				}
				if inRanges(regions, at) {
					t.Errorf("%q should NOT be inside a code region (regions=%v)", sub, regions)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Turn correlation
// ---------------------------------------------------------------------------

// A relay must return the reply of ITS turn. A `done` carrying a different
// assistant message ID belongs to some other turn on the same session and must
// not end the wait.
func TestAwaitRelayTurn_IgnoresForeignDone(t *testing.T) {
	s := newRelayServer(t)
	session, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "target"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	ours, err := s.Store.CreateMessage(session.ID, "assistant",
		[]MessagePart{
			{Type: "tool_use", ToolName: "Read"},
			{Type: "text", Content: "our answer\n\n<next-actions>\n- keep going\n</next-actions>"},
		}, MessageMetadata{})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	attempt := newRelayAttempt()
	attempt.setTurn(ours.ID)
	unsub := s.Hub.SubscribeSession(session.ID, attempt.handle)
	defer unsub()

	// Keep the session marked active so the liveness net stays quiet.
	s.SessionManager.SetActive(session.ID, true)

	result := make(chan string, 1)
	go func() {
		answer, err := s.awaitRelayTurn(context.Background(), session.ID, attempt)
		if err != nil {
			t.Errorf("awaitRelayTurn: %v", err)
		}
		result <- answer
	}()

	s.Hub.Emit(session.ID, ChatEvent{Type: "done", MessageID: "some-other-turn"})
	select {
	case answer := <-result:
		t.Fatalf("returned on a foreign done: %q", answer)
	case <-time.After(200 * time.Millisecond):
	}

	s.Hub.Emit(session.ID, ChatEvent{Type: "done", MessageID: ours.ID})
	select {
	case answer := <-result:
		if answer != "our answer" {
			t.Fatalf("answer = %q, want %q", answer, "our answer")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("awaitRelayTurn did not return on our done")
	}
}

// An `error` mid-turn is usually followed by a normal `done` carrying the full
// reply, so it must not cut the wait short.
func TestAwaitRelayTurn_ErrorFollowedByDoneReturnsReply(t *testing.T) {
	s := newRelayServer(t)
	session, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "target"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	msg, err := s.Store.CreateMessage(session.ID, "assistant",
		[]MessagePart{{Type: "text", Content: "recovered"}}, MessageMetadata{})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	attempt := newRelayAttempt()
	attempt.setTurn(msg.ID)
	unsub := s.Hub.SubscribeSession(session.ID, attempt.handle)
	defer unsub()
	s.SessionManager.SetActive(session.ID, true)

	result := make(chan string, 1)
	go func() {
		answer, err := s.awaitRelayTurn(context.Background(), session.ID, attempt)
		if err != nil {
			t.Errorf("awaitRelayTurn: %v", err)
		}
		result <- answer
	}()

	s.Hub.Emit(session.ID, ChatEvent{Type: "error", Message: "transient agent error"})
	s.Hub.Emit(session.ID, ChatEvent{Type: "done", MessageID: msg.ID})

	select {
	case answer := <-result:
		if answer != "recovered" {
			t.Fatalf("answer = %q, want %q", answer, "recovered")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("awaitRelayTurn did not return")
	}
}

// A turn that dies without ever emitting `done` must surface as an error rather
// than hanging the caller forever.
func TestAwaitRelayTurn_DeadTurnFailsViaLiveness(t *testing.T) {
	s := newRelayServer(t)
	session, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "target"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	attempt := newRelayAttempt()
	attempt.setTurn("turn-that-never-finishes")
	unsub := s.Hub.SubscribeSession(session.ID, attempt.handle)
	defer unsub()

	// Session never goes active — the liveness net should trip.
	type outcome struct {
		answer string
		err    error
	}
	result := make(chan outcome, 1)
	go func() {
		answer, err := s.awaitRelayTurn(context.Background(), session.ID, attempt)
		result <- outcome{answer, err}
	}()

	s.Hub.Emit(session.ID, ChatEvent{Type: "error", Message: "failed to create message"})

	select {
	case got := <-result:
		if got.err == nil {
			t.Fatalf("expected an error, got answer %q", got.answer)
		}
		if !strings.Contains(got.err.Error(), "failed to create message") {
			t.Fatalf("error = %v, want it to carry the session's error text", got.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("awaitRelayTurn hung on a dead turn")
	}
}

// The live text accumulator is the fallback when the persisted message can't be
// read back. It must hold only the run of text after the last step, matching
// what survives the "Worked for …" collapse.
func TestRelayAttempt_LiveTextKeepsOnlyTrailingRun(t *testing.T) {
	a := newRelayAttempt()
	a.handle(ChatEvent{Type: "text_delta", Content: "let me check"})
	a.handle(ChatEvent{Type: "tool_start", Tool: "Read"})
	a.handle(ChatEvent{Type: "text_delta", Content: "the answer "})
	a.handle(ChatEvent{Type: "text_delta", Content: "is 42"})

	if live, _ := a.snapshot(); live != "the answer is 42" {
		t.Fatalf("live text = %q, want %q", live, "the answer is 42")
	}
}

// ---------------------------------------------------------------------------
// Cycle detection
// ---------------------------------------------------------------------------

func TestRegisterRelayWait_RejectsCycles(t *testing.T) {
	t.Cleanup(func() {
		relayMu.Lock()
		relayWaits = make(map[string][]string)
		relayMu.Unlock()
	})

	if err := registerRelayWait("a", "b"); err != nil {
		t.Fatalf("a→b should be allowed: %v", err)
	}
	if err := registerRelayWait("b", "a"); err == nil {
		t.Fatal("b→a closes a cycle and must be rejected")
	}
	if err := registerRelayWait("b", "c"); err != nil {
		t.Fatalf("b→c should be allowed: %v", err)
	}
	// a→b→c is live, so c→a would close a three-hop cycle.
	if err := registerRelayWait("c", "a"); err == nil {
		t.Fatal("c→a closes a transitive cycle and must be rejected")
	}

	unregisterRelayWait("a", "b")
	unregisterRelayWait("b", "c")
	if err := registerRelayWait("b", "a"); err != nil {
		t.Fatalf("b→a should be allowed once a is no longer waiting: %v", err)
	}
}

// One session can have several relays in flight at once (agents call tools in
// parallel); finishing one must not deregister the others.
func TestRegisterRelayWait_ParallelRelaysFromOneSession(t *testing.T) {
	t.Cleanup(func() {
		relayMu.Lock()
		relayWaits = make(map[string][]string)
		relayMu.Unlock()
	})

	if err := registerRelayWait("a", "b"); err != nil {
		t.Fatalf("a→b: %v", err)
	}
	if err := registerRelayWait("a", "c"); err != nil {
		t.Fatalf("a→c should be allowed alongside a→b: %v", err)
	}

	unregisterRelayWait("a", "b")

	if err := registerRelayWait("c", "a"); err == nil {
		t.Fatal("a is still waiting on c, so c→a must stay rejected")
	}
	if err := registerRelayWait("b", "a"); err != nil {
		t.Fatalf("a no longer waits on b, so b→a should be allowed: %v", err)
	}
}

func TestSendToSessionAndWait_RejectsSelfSend(t *testing.T) {
	s := newRelayServer(t)
	if _, err := s.SendToSessionAndWait(context.Background(), "same", "same", "hi"); err == nil {
		t.Fatal("a session sending to itself must be rejected")
	}
}

func createRelayOwnedSession(t *testing.T, s *Server, ownerID int64, projectName, sessionName string) *Session {
	t.Helper()
	project, err := s.Store.CreateProject(store.CreateProjectRequest{
		OwnerUserID: ownerID,
		Name:        projectName,
		GitRepoPath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	session, err := s.Store.CreateSession(store.CreateSessionRequest{
		Name:      sessionName,
		ProjectID: project.ID,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return session
}

func TestResolveSessionRefFrom_RestrictsTargetsToCallingOwner(t *testing.T) {
	s := newRelayServer(t)
	source := createRelayOwnedSession(t, s, 101, "source-project", "source")
	owned := createRelayOwnedSession(t, s, 101, "owned-project", "owned")
	foreign := createRelayOwnedSession(t, s, 202, "foreign-project", "foreign")

	id, name, err := s.ResolveSessionRefFrom(source.ID, strings.ToUpper(strings.ReplaceAll(owned.ID, "-", "")[:6]))
	if err != nil || id != owned.ID || name != owned.Name {
		t.Fatalf("same-owner resolve = (%q, %q, %v), want (%q, %q, nil)", id, name, err, owned.ID, owned.Name)
	}

	if _, _, err := s.ResolveSessionRefFrom(source.ID, foreign.ID); err == nil {
		t.Fatal("a target owned by another user must not resolve")
	} else if strings.Contains(err.Error(), foreign.Name) {
		t.Fatalf("cross-owner error leaks the target title: %v", err)
	}
}

func TestSendToSessionAndWait_RejectsCrossOwnerBeforeDispatch(t *testing.T) {
	s := newRelayServer(t)
	source := createRelayOwnedSession(t, s, 101, "source-project", "source")
	foreign := createRelayOwnedSession(t, s, 202, "foreign-project", "foreign")

	if _, err := s.SendToSessionAndWait(context.Background(), source.ID, foreign.ID, "hi"); err == nil {
		t.Fatal("a relay across project owners must be rejected")
	}
	messages, err := s.Store.ListMessages(foreign.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("cross-owner relay persisted %d message(s), want none", len(messages))
	}
}

// ---------------------------------------------------------------------------
// Session lookup
// ---------------------------------------------------------------------------

func TestResolveSessionRef(t *testing.T) {
	s := newRelayServer(t)
	source := createRelayOwnedSession(t, s, 101, "source-project", "source")
	target := createRelayOwnedSession(t, s, 101, "target-project", "target")

	t.Run("full id", func(t *testing.T) {
		id, name, err := s.ResolveSessionRefFrom(source.ID, target.ID)
		if err != nil || id != target.ID || name != "target" {
			t.Fatalf("got (%q, %q, %v), want (%q, target, nil)", id, name, err, target.ID)
		}
	})

	t.Run("badge prefix, uppercase and dash-free", func(t *testing.T) {
		badge := strings.ToUpper(strings.ReplaceAll(target.ID, "-", "")[:6])
		id, _, err := s.ResolveSessionRefFrom(source.ID, badge)
		if err != nil || id != target.ID {
			t.Fatalf("got (%q, %v), want (%q, nil)", id, err, target.ID)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		if _, _, err := s.ResolveSessionRefFrom(source.ID, "ffffffff"); err == nil {
			t.Fatal("an unknown prefix must not resolve")
		}
	})

	t.Run("too short", func(t *testing.T) {
		if _, _, err := s.ResolveSessionRefFrom(source.ID, "ab"); err == nil {
			t.Fatal("a 2-character prefix must be rejected")
		}
	})

}

// A prefix shared by two sessions must fail loudly rather than pick one.
func TestMatchSessionByPrefix_Ambiguous(t *testing.T) {
	sessions := []Session{
		{ID: "abc12345-0000-0000-0000-000000000001", Name: "first"},
		{ID: "abc12345-0000-0000-0000-000000000002", Name: "second"},
		{ID: "def67890-0000-0000-0000-000000000003", Name: "third"},
	}

	if _, _, err := matchSessionByPrefix(sessions, "abc1", "abc1"); err == nil {
		t.Fatal("an ambiguous prefix must be rejected")
	} else if !strings.Contains(err.Error(), "matches 2 sessions") {
		t.Fatalf("error = %v, want it to name both candidates", err)
	}

	id, name, err := matchSessionByPrefix(sessions, "def6", "def6")
	if err != nil || id != sessions[2].ID || name != "third" {
		t.Fatalf("got (%q, %q, %v), want the unique match", id, name, err)
	}
}
