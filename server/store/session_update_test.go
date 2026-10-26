package store

import (
	"fmt"
	"sync"
	"testing"
)

func TestSessionUpdateOnlyWritesRequestedFields(t *testing.T) {
	s := newTestDB(t)
	session, err := s.CreateSession(CreateSessionRequest{Name: "original", Model: "original-model"})
	if err != nil {
		t.Fatal(err)
	}
	// Audit writes to each independently editable field. A rename must never
	// write a snapshot of pin/model back, even when the snapshot had equal values.
	if _, err := s.db.Exec(`CREATE TABLE session_field_writes (field TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"name", "model", "is_pinned"} {
		if _, err := s.db.Exec(fmt.Sprintf(`CREATE TRIGGER audit_session_%s AFTER UPDATE OF %s ON sessions BEGIN INSERT INTO session_field_writes VALUES ('%s'); END`, field, field, field)); err != nil {
			t.Fatal(err)
		}
	}
	name := "renamed"
	if _, err := s.UpdateSession(session.ID, UpdateSessionRequest{Name: &name}); err != nil {
		t.Fatal(err)
	}
	var writes int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_field_writes WHERE field != 'name'`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatalf("rename wrote %d unrelated fields", writes)
	}
	if _, err := s.db.Exec(`DELETE FROM session_field_writes`); err != nil {
		t.Fatal(err)
	}
	pinned := true
	updated, err := s.UpdateSession(session.ID, UpdateSessionRequest{IsPinned: &pinned})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.IsPinned || updated.Name != name || updated.Model != "original-model" || !updated.LastActiveAt.Equal(session.LastActiveAt) {
		t.Fatalf("unexpected updated session: %+v", updated)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_field_writes WHERE field != 'is_pinned'`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatalf("pin wrote %d unrelated fields", writes)
	}
	// Explicit false and empty string must remain valid patches.
	pinned = false
	model := ""
	updated, err = s.UpdateSession(session.ID, UpdateSessionRequest{IsPinned: &pinned, Model: &model})
	if err != nil || updated.IsPinned || updated.Model != "" || updated.Name != name {
		t.Fatalf("clear fields: session=%+v err=%v", updated, err)
	}
	missing, err := s.UpdateSession("missing-session", UpdateSessionRequest{Name: &name})
	if err != nil || missing != nil {
		t.Fatalf("missing session=%+v err=%v", missing, err)
	}
}

func TestConcurrentSessionRenameAndPinPreserveBothChanges(t *testing.T) {
	s := newTestDB(t)
	for i := range 32 {
		session, err := s.CreateSession(CreateSessionRequest{Name: "original"})
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("renamed-%d", i)
		pinned := true
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, req := range []UpdateSessionRequest{{Name: &name}, {IsPinned: &pinned}} {
			wg.Add(1)
			go func(req UpdateSessionRequest) {
				defer wg.Done()
				<-start
				_, err := s.UpdateSession(session.ID, req)
				errs <- err
			}(req)
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		updated, err := s.GetSession(session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if updated.Name != name || !updated.IsPinned {
			t.Fatalf("concurrent edits lost: %+v", updated)
		}
		if !updated.LastActiveAt.Equal(session.LastActiveAt) {
			t.Fatal("metadata changes modified activity")
		}
	}
}
