package store

import (
	"testing"
	"time"
)

func TestSessionActivityBoundaries(t *testing.T) {
	s := newTestDB(t)
	session, err := s.CreateSession(CreateSessionRequest{Name: "activity"})
	if err != nil {
		t.Fatal(err)
	}
	// Use an old timestamp so advancing activity is deterministic without sleeps.
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := s.db.Exec(`UPDATE sessions SET last_active_at = ? WHERE id = ?`, old, session.ID); err != nil {
		t.Fatal(err)
	}
	assertActivity := func(want time.Time) {
		t.Helper()
		got, err := s.GetSession(session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.LastActiveAt.Equal(want) {
			t.Fatalf("activity = %v, want %v", got.LastActiveAt, want)
		}
	}
	name, model, effort, browser := "renamed", "another-model", "high", "http://127.0.0.1:9222"
	pinned := true
	if _, err := s.UpdateSession(session.ID, UpdateSessionRequest{Name: &name, Model: &model, Effort: &effort, ChromeBrowserURL: &browser, IsPinned: &pinned}); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		s.UpdateSessionAgentID(session.ID, "agent-id"),
		s.UpdateSessionStreamID(session.ID, "stream-id"),
		s.UpdateSessionFeatures(session.ID, "features"),
		s.UpdateSessionWorktree(session.ID, "local", ""),
		s.ReorderSessions([]string{session.ID}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertActivity(old)
	placeholder, err := s.CreateMessage(session.ID, "assistant", []MessagePart{}, MessageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := s.UpdateMessageParts(placeholder.ID, []MessagePart{{Type: "text", Content: "partial"}}, MessageMetadata{}); err != nil {
			t.Fatal(err)
		}
	}
	assertActivity(old)
	if _, err := s.CreateUserMessage(session.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	sent, err := s.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sent.LastActiveAt.After(old) {
		t.Fatal("sending did not advance activity")
	}
	if err := s.TouchSessionActivity(session.ID); err != nil {
		t.Fatal(err)
	}
	finished, err := s.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.LastActiveAt.Before(sent.LastActiveAt) {
		t.Fatal("completion moved activity backwards")
	}
	if !finished.IsPinned {
		t.Fatal("unrelated changes lost pin")
	}
	if _, err := s.UpdateSession(session.ID, UpdateSessionRequest{Name: &name}); err != nil {
		t.Fatal(err)
	}
	assertActivity(finished.LastActiveAt)
	listed, err := s.ListSessions()
	if err != nil || len(listed) != 1 || !listed[0].IsPinned || !listed[0].LastActiveAt.Equal(finished.LastActiveAt) {
		t.Fatalf("list = %+v, err=%v", listed, err)
	}
	if err := s.DeleteSession(session.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreSession(session.ID); err != nil {
		t.Fatal(err)
	}
	assertActivity(finished.LastActiveAt)
	pinned = false
	unpinned, err := s.UpdateSession(session.ID, UpdateSessionRequest{IsPinned: &pinned})
	if err != nil || unpinned.IsPinned {
		t.Fatalf("unpin = %+v, err=%v", unpinned, err)
	}
}

func TestSessionActivityBackfill(t *testing.T) {
	s := newTestDB(t)
	session, err := s.CreateSession(CreateSessionRequest{Name: "old session"})
	if err != nil {
		t.Fatal(err)
	}
	message, err := s.CreateUserMessage(session.ID, "old conversation")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-migration row whose metadata was edited after its messages.
	if _, err := s.db.Exec(`UPDATE sessions SET last_active_at = NULL, updated_at = ? WHERE id = ?`, time.Now().Add(time.Hour), session.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.migrate(); err != nil {
		t.Fatal(err)
	}
	restored, err := s.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.LastActiveAt.Equal(message.CreatedAt) {
		t.Fatalf("backfill = %v, want message time %v", restored.LastActiveAt, message.CreatedAt)
	}
	// Re-running the migration must preserve a real completion timestamp.
	if err := s.TouchSessionActivity(session.ID); err != nil {
		t.Fatal(err)
	}
	completed, _ := s.GetSession(session.ID)
	if err := s.db.migrate(); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetSession(session.ID)
	if !again.LastActiveAt.Equal(completed.LastActiveAt) {
		t.Fatal("migration reset completion activity")
	}
}
