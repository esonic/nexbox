package api

import (
	"net/http"
	"testing"

	"github.com/esonic/nexbox/store"
)

func TestKillTerminalCreatesFreshSession(t *testing.T) {
	s := newOptionalAuthServer(t)
	s.TerminalMgr = NewTerminalManager()
	session, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "terminal"})
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := getOrCreateTermSession(session.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(old.shutdown)
	s.TerminalMgr.Register(session.ID, old.ptmx)
	response := optionalAuthRequest(t, s, http.MethodDelete, "/api/sessions/"+session.ID+"/terminal", nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("kill terminal: %d %s", response.Code, response.Body.String())
	}
	fresh, reattached, err := getOrCreateTermSession(session.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fresh.shutdown)
	if reattached || fresh == old {
		t.Fatal("reopened terminal reused the killed shell")
	}
	s.TerminalMgr.Register(session.ID, fresh.ptmx)
	s.TerminalMgr.Unregister(session.ID, old.ptmx)
	if err := s.TerminalMgr.WriteCommand(session.ID, ""); err != nil {
		t.Fatalf("old shell cleanup unregistered the new shell: %v", err)
	}
}
