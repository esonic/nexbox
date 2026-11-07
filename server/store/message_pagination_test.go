package store

import (
	"fmt"
	"testing"
	"time"
)

func TestMessagePaginationWithEqualTimestamps(t *testing.T) {
	s := newTestDB(t)
	session, err := s.CreateSession(CreateSessionRequest{Name: "history"})
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for i := 0; i < 45; i++ {
		msg, err := s.CreateUserMessage(session.ID, fmt.Sprintf("message %d", i))
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, msg.ID)
		// Multiple pages share timestamps; the final group is a newer timestamp.
		stamp := time.Date(2026, 1, 1, 0, 0, i/30, 0, time.UTC)
		if _, err := s.db.Exec(`UPDATE chat_messages SET created_at = ? WHERE id = ?`, stamp, msg.ID); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	before := ""
	for page := 0; page < 3; page++ {
		messages, more, err := s.ListMessagesPaginated(session.ID, 20, before)
		if err != nil {
			t.Fatal(err)
		}
		count := 20
		if page == 2 {
			count = 5
		}
		if len(messages) != count || more != (page < 2) {
			t.Fatalf("page %d: got %d messages, more=%v", page, len(messages), more)
		}
		var ids []string
		for _, m := range messages {
			ids = append(ids, m.ID)
		}
		got = append(ids, got...)
		before = messages[0].ID
	}
	for i, id := range got {
		if id != want[i] {
			t.Fatalf("message %d: got %s, want %s", i, id, want[i])
		}
	}
	messages, more, err := s.ListMessagesPaginated(session.ID, 20, before)
	if err != nil || len(messages) != 0 || more {
		t.Fatalf("end: messages=%v more=%v err=%v", messages, more, err)
	}
	for _, cursor := range []string{"missing", want[44]} {
		messages, more, err := s.ListMessagesPaginated("another-session", 20, cursor)
		if err != nil || len(messages) != 0 || more {
			t.Fatalf("foreign cursor: messages=%v more=%v err=%v", messages, more, err)
		}
	}
}
