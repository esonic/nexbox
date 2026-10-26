package store

import "testing"

func TestSessionChromeBrowserURLPersistsAndForks(t *testing.T) {
	s := newTestDB(t)
	session, err := s.CreateSession(CreateSessionRequest{Name: "external chrome"})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://127.0.0.1:9222"
	if _, err := s.UpdateSession(session.ID, UpdateSessionRequest{ChromeBrowserURL: &endpoint}); err != nil {
		t.Fatal(err)
	}
	// An unrelated update must preserve the browser connection configuration.
	name := "renamed"
	if _, err := s.UpdateSession(session.ID, UpdateSessionRequest{Name: &name}); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetSession(session.ID)
	if err != nil || stored.ChromeBrowserURL != endpoint {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	msg, err := s.CreateMessage(session.ID, "user", []MessagePart{{Type: "text", Content: "hello"}}, MessageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	fork, err := s.ForkSession(session.ID, "fork", msg.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetSession(fork.ID)
	if err != nil || stored.ChromeBrowserURL != endpoint {
		t.Fatalf("fork=%+v err=%v", stored, err)
	}
	managed := ""
	if _, err := s.UpdateSession(session.ID, UpdateSessionRequest{ChromeBrowserURL: &managed}); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetSession(session.ID)
	if err != nil || stored.ChromeBrowserURL != "" {
		t.Fatalf("managed=%+v err=%v", stored, err)
	}
}
