package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/esonic/nexbox/config"
)

func TestPromptTemplates_RoundTrip(t *testing.T) {
	s := newAgentGateServer(t)

	rr := doRequest(t, s, http.MethodGet, "/api/prompt-templates", nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "[]\n" {
		t.Fatalf("empty list: status=%d body=%q", rr.Code, rr.Body.String())
	}

	rr = doRequest(t, s, http.MethodPut, "/api/prompt-templates", []map[string]string{
		{"name": "  Review  ", "content": "Please review this diff."},
		{"name": "Explain", "content": "Explain this code."},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("put: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var saved []config.PromptTemplate
	if err := json.NewDecoder(rr.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 || saved[0].Name != "Review" || saved[1].Name != "Explain" {
		t.Fatalf("saved = %+v", saved)
	}
	if got := s.Config.Get().PromptTemplates; len(got) != 2 {
		t.Fatalf("persisted = %+v", got)
	}
}

func TestPromptTemplates_RejectsInvalid(t *testing.T) {
	s := newAgentGateServer(t)
	for _, body := range [][]map[string]string{
		{{"name": "", "content": "x"}},
		{{"name": "x", "content": "   "}},
		{{"name": "Review", "content": "a"}, {"name": " review ", "content": "b"}},
	} {
		if rr := doRequest(t, s, http.MethodPut, "/api/prompt-templates", body); rr.Code != http.StatusBadRequest {
			t.Errorf("body %v: status=%d, want 400", body, rr.Code)
		}
	}
}
