package agent

import "testing"

func TestCodexWebSearchQueryFromPayload_QueryField(t *testing.T) {
	payload := map[string]interface{}{
		"query": "golang slices explained",
	}
	got := codexWebSearchQueryFromPayload(payload)
	if got != "golang slices explained" {
		t.Fatalf("unexpected query: %q", got)
	}
}

func TestCodexWebSearchQueryFromPayload_QueriesArray(t *testing.T) {
	payload := map[string]interface{}{
		"queries": []interface{}{
			map[string]interface{}{"query": "rust ownership"},
		},
	}
	got := codexWebSearchQueryFromPayload(payload)
	if got != "rust ownership" {
		t.Fatalf("unexpected query: %q", got)
	}
}

func TestCodexWebSearchQueryFromPayload_InputFallback(t *testing.T) {
	payload := map[string]interface{}{
		"input": "site:openai.com codex app-server",
	}
	got := codexWebSearchQueryFromPayload(payload)
	if got != "site:openai.com codex app-server" {
		t.Fatalf("unexpected query: %q", got)
	}
}

func TestCodexWebSearchQueryFromPayload_SkipsNonQueryStructuredInput(t *testing.T) {
	payload := map[string]interface{}{
		"input": map[string]interface{}{"foo": "bar"},
	}
	got := codexWebSearchQueryFromPayload(payload)
	if got != "" {
		t.Fatalf("expected empty query, got %q", got)
	}
}
