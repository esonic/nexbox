package api

import (
	"net/http"
	"testing"

	"github.com/esonic/nexbox/agent"
	chromemcp "github.com/esonic/nexbox/mcp/chrome"
	"github.com/esonic/nexbox/store"
)

func TestUpdateSessionChromeBrowserURL(t *testing.T) {
	s := newAgentGateServer(t)
	s.SessionManager = agent.NewSessionManager()
	s.ChromeManager = chromemcp.NewChromeManager(t.TempDir(), "127.0.0.1", nil, nil, 0)
	t.Cleanup(s.ChromeManager.ShutdownAll)
	session, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "chrome"})
	if err != nil {
		t.Fatal(err)
	}
	// An existing per-session MCP must be discarded when its mode changes.
	oldServer := s.ChromeManager.GetOrCreate(session.ID)
	endpoint := "http://127.0.0.1:9222"
	rr := doRequest(t, s, http.MethodPut, "/api/sessions/"+session.ID, map[string]string{"chrome_browser_url": " " + endpoint + " "})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rr.Code, rr.Body.String())
	}
	stored, err := s.Store.GetSession(session.ID)
	if err != nil || stored.ChromeBrowserURL != endpoint {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	if s.SessionManager.IsActive(session.ID) {
		t.Fatal("configuration update left the session active")
	}
	if next := s.ChromeManager.GetOrCreate(session.ID, endpoint); next == oldServer {
		t.Fatal("old MCP server was reused after the endpoint changed")
	}
	s.SessionManager.SetActive(session.ID, true)
	rr = doRequest(t, s, http.MethodPut, "/api/sessions/"+session.ID, map[string]string{"chrome_browser_url": "http://127.0.0.1:9223"})
	if rr.Code != http.StatusConflict || !s.SessionManager.IsActive(session.ID) {
		t.Fatalf("active session update status=%d body=%s", rr.Code, rr.Body.String())
	}
	s.SessionManager.SetActive(session.ID, false)
	for _, invalid := range []string{"file:///tmp/browser", "ws://localhost:9222/devtools/page/id"} {
		rr = doRequest(t, s, http.MethodPut, "/api/sessions/"+session.ID, map[string]string{"chrome_browser_url": invalid})
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("invalid address status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
	stored, err = s.Store.GetSession(session.ID)
	if err != nil || stored.ChromeBrowserURL != endpoint {
		t.Fatalf("rejected updates changed endpoint: stored=%+v err=%v", stored, err)
	}
}
