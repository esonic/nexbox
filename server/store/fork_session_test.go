package store

import (
	"strings"
	"testing"
	"time"
)

// TestForkSession_HappyPath covers the common case: fork a session at one of
// its assistant messages, verify lineage fields, verify message-prefix copy,
// verify the parent is untouched.
func TestForkSession_HappyPath(t *testing.T) {
	s := newTestDB(t)

	parent, err := s.CreateSession(CreateSessionRequest{Name: "p", Agent: "claude", Model: "sonnet"})
	if err != nil {
		t.Fatalf("CreateSession parent: %v", err)
	}
	if err := s.UpdateSessionAgentID(parent.ID, "agent-sid-parent"); err != nil {
		t.Fatalf("UpdateSessionAgentID: %v", err)
	}
	// features must be inherited so things like chrome_devtools survive fork.
	if err := s.UpdateSessionFeatures(parent.ID, "chrome_devtools"); err != nil {
		t.Fatalf("UpdateSessionFeatures: %v", err)
	}

	// 4 messages: u1, a1, u2, a2 — fork at a1.
	mU1, err := s.CreateMessage(parent.ID, "user", []MessagePart{{Type: "text", Content: "hello"}}, MessageMetadata{})
	if err != nil {
		t.Fatalf("CreateMessage u1: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	mA1, err := s.CreateMessage(parent.ID, "assistant", []MessagePart{{Type: "text", Content: "hi"}}, MessageMetadata{SDKMessageUUID: "uuid-a1"})
	if err != nil {
		t.Fatalf("CreateMessage a1: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	mU2, err := s.CreateMessage(parent.ID, "user", []MessagePart{{Type: "text", Content: "more"}}, MessageMetadata{})
	if err != nil {
		t.Fatalf("CreateMessage u2: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	mA2, err := s.CreateMessage(parent.ID, "assistant", []MessagePart{{Type: "text", Content: "more reply"}}, MessageMetadata{SDKMessageUUID: "uuid-a2"})
	if err != nil {
		t.Fatalf("CreateMessage a2: %v", err)
	}

	// Fork at the FIRST assistant message — only u1 + a1 should be copied.
	forked, err := s.ForkSession(parent.ID, "", mA1.ID, "uuid-a1")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if forked.ID == parent.ID {
		t.Fatalf("forked session got parent's ID")
	}
	if forked.Agent != "claude" || forked.Model != "sonnet" {
		t.Errorf("agent/model not inherited: agent=%q model=%q", forked.Agent, forked.Model)
	}
	if forked.Features != "chrome_devtools" {
		t.Errorf("features not inherited: %q", forked.Features)
	}
	if forked.AgentSessionID != "" {
		t.Errorf("AgentSessionID should be empty on forked session, got %q", forked.AgentSessionID)
	}
	if forked.ForkedFromSessionID != parent.ID {
		t.Errorf("ForkedFromSessionID = %q, want %q", forked.ForkedFromSessionID, parent.ID)
	}
	if forked.ForkedFromMessageID != mA1.ID {
		t.Errorf("ForkedFromMessageID = %q, want %q", forked.ForkedFromMessageID, mA1.ID)
	}
	if forked.ForkedAtAgentMsgUUID != "uuid-a1" {
		t.Errorf("ForkedAtAgentMsgUUID = %q, want %q", forked.ForkedAtAgentMsgUUID, "uuid-a1")
	}
	if !strings.HasSuffix(forked.Name, "(fork)") {
		t.Errorf("auto-generated name = %q, want ...(fork)", forked.Name)
	}

	// Round-trip: Get should return everything we just wrote.
	got, err := s.GetSession(forked.ID)
	if err != nil {
		t.Fatalf("GetSession forked: %v", err)
	}
	if got == nil {
		t.Fatalf("forked session not found")
	}
	if got.ForkedFromSessionID != parent.ID || got.ForkedAtAgentMsgUUID != "uuid-a1" {
		t.Errorf("Get round-trip lost fork fields: %+v", got)
	}

	// Forked session messages: u1 + a1 only, distinct ids, same content.
	forkedMsgs, err := s.ListMessages(forked.ID)
	if err != nil {
		t.Fatalf("ListMessages forked: %v", err)
	}
	if len(forkedMsgs) != 2 {
		t.Fatalf("forked messages = %d, want 2; got %+v", len(forkedMsgs), forkedMsgs)
	}
	if forkedMsgs[0].Role != "user" || forkedMsgs[1].Role != "assistant" {
		t.Errorf("role order = [%s, %s], want [user, assistant]", forkedMsgs[0].Role, forkedMsgs[1].Role)
	}
	if forkedMsgs[1].Metadata.SDKMessageUUID != "uuid-a1" {
		t.Errorf("forked a1 metadata SDKMessageUUID lost: got %q", forkedMsgs[1].Metadata.SDKMessageUUID)
	}
	for _, m := range forkedMsgs {
		if m.SessionID != forked.ID {
			t.Errorf("forked message has session_id=%q, want %q", m.SessionID, forked.ID)
		}
		if m.ID == mU1.ID || m.ID == mA1.ID {
			t.Errorf("forked message reused parent ID %q", m.ID)
		}
	}

	// Parent untouched: still 4 messages, IDs unchanged.
	parentMsgs, err := s.ListMessages(parent.ID)
	if err != nil {
		t.Fatalf("ListMessages parent: %v", err)
	}
	if len(parentMsgs) != 4 {
		t.Errorf("parent messages = %d, want 4 (parent must not be modified)", len(parentMsgs))
	}
	wantIDs := []string{mU1.ID, mA1.ID, mU2.ID, mA2.ID}
	for i, m := range parentMsgs {
		if m.ID != wantIDs[i] {
			t.Errorf("parent message %d id = %q, want %q (parent must not be modified)", i, m.ID, wantIDs[i])
		}
	}
}

// TestForkSession_AtLastMessage covers the case where the cut point is the
// very last message in the session. The whole conversation should be copied.
func TestForkSession_AtLastMessage(t *testing.T) {
	s := newTestDB(t)
	parent, _ := s.CreateSession(CreateSessionRequest{Name: "p", Agent: "claude"})

	if _, err := s.CreateMessage(parent.ID, "user", []MessagePart{{Type: "text", Content: "1"}}, MessageMetadata{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	a, err := s.CreateMessage(parent.ID, "assistant", []MessagePart{{Type: "text", Content: "2"}}, MessageMetadata{SDKMessageUUID: "u"})
	if err != nil {
		t.Fatal(err)
	}

	forked, err := s.ForkSession(parent.ID, "named", a.ID, "u")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if forked.Name != "named" {
		t.Errorf("custom name not honored: %q", forked.Name)
	}
	msgs, _ := s.ListMessages(forked.ID)
	if len(msgs) != 2 {
		t.Errorf("expected entire conversation copied (2 messages), got %d", len(msgs))
	}
}

// TestGetMessage covers the single-message lookup helper used by the fork
// HTTP endpoint to validate the cut point. Without this, the endpoint would
// have to scan the entire message list.
func TestGetMessage(t *testing.T) {
	s := newTestDB(t)
	sess, _ := s.CreateSession(CreateSessionRequest{Name: "p", Agent: "claude"})
	created, err := s.CreateMessage(sess.ID, "assistant",
		[]MessagePart{{Type: "text", Content: "hi"}},
		MessageMetadata{SDKMessageUUID: "u"})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	got, err := s.GetMessage(created.ID)
	if err != nil {
		t.Fatalf("GetMessage existing: %v", err)
	}
	if got == nil {
		t.Fatalf("GetMessage returned nil for existing message")
	}
	if got.ID != created.ID || got.SessionID != sess.ID || got.Role != "assistant" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Metadata.SDKMessageUUID != "u" {
		t.Errorf("metadata round-trip lost SDKMessageUUID: %q", got.Metadata.SDKMessageUUID)
	}
	if len(got.Parts) != 1 || got.Parts[0].Content != "hi" {
		t.Errorf("parts round-trip mismatch: %+v", got.Parts)
	}

	missing, err := s.GetMessage("no-such-id")
	if err != nil {
		t.Fatalf("GetMessage missing: %v", err)
	}
	if missing != nil {
		t.Errorf("expected nil for missing message, got %+v", missing)
	}
}

// TestGetMessageBySDKUUID covers the lookup helper used by the fork
// HTTP endpoint when the caller only has a CLI-side message uuid (the
// frontend's `message.id` is a client-side UUID until the page reloads).
func TestGetMessageBySDKUUID(t *testing.T) {
	s := newTestDB(t)
	sess, _ := s.CreateSession(CreateSessionRequest{Name: "p", Agent: "claude"})
	other, _ := s.CreateSession(CreateSessionRequest{Name: "o", Agent: "claude"})
	created, err := s.CreateMessage(sess.ID, "assistant",
		[]MessagePart{{Type: "text", Content: "hi"}},
		MessageMetadata{SDKMessageUUID: "sdk-abc"})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	// A message with the same SDK uuid in a different session must not
	// shadow the parent-scoped lookup.
	_, _ = s.CreateMessage(other.ID, "assistant",
		[]MessagePart{{Type: "text", Content: "no"}},
		MessageMetadata{SDKMessageUUID: "sdk-abc"})

	got, err := s.GetMessageBySDKUUID(sess.ID, "sdk-abc")
	if err != nil {
		t.Fatalf("GetMessageBySDKUUID: %v", err)
	}
	if got == nil || got.ID != created.ID {
		t.Fatalf("expected to find message %s, got %+v", created.ID, got)
	}

	missing, err := s.GetMessageBySDKUUID(sess.ID, "nope")
	if err != nil {
		t.Fatalf("GetMessageBySDKUUID missing: %v", err)
	}
	if missing != nil {
		t.Errorf("expected nil for unknown sdk uuid, got %+v", missing)
	}

	if r, _ := s.GetMessageBySDKUUID("", "sdk-abc"); r != nil {
		t.Errorf("empty sessionID should return nil")
	}
	if r, _ := s.GetMessageBySDKUUID(sess.ID, ""); r != nil {
		t.Errorf("empty sdkUUID should return nil")
	}
}

// TestForkSession_RejectsBadInput covers validation: missing inputs, message
// from a different session, non-existent message.
func TestForkSession_RejectsBadInput(t *testing.T) {
	s := newTestDB(t)
	parent, _ := s.CreateSession(CreateSessionRequest{Name: "p", Agent: "claude"})
	other, _ := s.CreateSession(CreateSessionRequest{Name: "o", Agent: "claude"})
	otherMsg, _ := s.CreateMessage(other.ID, "assistant", []MessagePart{{Type: "text", Content: "x"}}, MessageMetadata{SDKMessageUUID: "u"})

	cases := []struct {
		name      string
		parent    string
		atMsg     string
		uuid      string
		wantErrIn string
	}{
		{"missing parent", "", "any", "u", "required"},
		{"missing message", parent.ID, "", "u", "required"},
		{"unknown parent", "no-such-session", "no-such-msg", "u", "not found"},
		{"message from other session", parent.ID, otherMsg.ID, "u", "does not belong"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ForkSession(tc.parent, "", tc.atMsg, tc.uuid)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErrIn)
			}
			if !strings.Contains(err.Error(), tc.wantErrIn) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErrIn)
			}
		})
	}
}
