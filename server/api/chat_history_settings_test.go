package api

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/esonic/nexbox/config"
	"github.com/esonic/nexbox/store"
)

func TestChatHistoryPageSize(t *testing.T) {
	s := newAgentGateServer(t)
	session, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "history"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.Store.CreateMessage(session.ID, "user", []store.MessagePart{{Type: "text", Content: "hello"}}, store.MessageMetadata{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Config.Get().HistoryPageSize(); got != 20 {
		t.Fatalf("default size: %d", got)
	}
	pathToConfig := s.Config.Path()
	if err := os.WriteFile(pathToConfig, []byte(`{"chat_history_page_size":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.Config, err = config.NewManager(pathToConfig)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Config.Get().HistoryPageSize(); got != 2 {
		t.Fatalf("saved size: %d", got)
	}
	path := "/api/sessions/" + session.ID + "/messages?limit=0"
	for page := 0; page < 2; page++ {
		rr := doRequest(t, s, http.MethodGet, path, nil)
		var result struct {
			Messages []store.ChatMessage `json:"messages"`
			HasMore  bool                `json:"has_more"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if rr.Code != http.StatusOK || len(result.Messages) != 2 || !result.HasMore {
			t.Fatalf("page %d: %d %s", page, rr.Code, rr.Body.String())
		}
		path = "/api/sessions/" + session.ID + "/messages?before=" + result.Messages[0].ID
	}
}
