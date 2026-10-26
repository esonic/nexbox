package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// Codex 0.124.0 sends webSearch item/started with empty query/action and only
// fills in the real data at item/completed. These tests verify the canonical
// emit helpers produce the right ToolStart/ToolResult shapes for the action
// variants the wire format uses.

func TestEmitCodexItemStartedWebSearchSearchAction(t *testing.T) {
	item := codexThreadItem{
		ID:    "ws_1",
		Type:  "webSearch",
		Query: "OpenAI Codex CLI release notes 2025",
		Action: json.RawMessage(`{
			"queries": ["OpenAI Codex CLI release notes 2025"],
			"query": "OpenAI Codex CLI release notes 2025",
			"type": "search"
		}`),
	}
	var got []StreamEvent
	emitCodexItemStarted(item, func(e StreamEvent) { got = append(got, e) })
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	ev := got[0]
	if ev.Type != EventToolStart || ev.ToolName != "WebSearch" {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if !strings.Contains(ev.Input, `"query":"OpenAI Codex CLI release notes 2025"`) {
		t.Fatalf("expected query in input, got: %s", ev.Input)
	}
}

func TestEmitCodexItemStartedWebSearchOpenPageAction(t *testing.T) {
	item := codexThreadItem{
		ID:    "ws_2",
		Type:  "webSearch",
		Query: "https://example.com/docs",
		Action: json.RawMessage(`{
			"type": "openPage",
			"url": "https://example.com/docs"
		}`),
	}
	var got []StreamEvent
	emitCodexItemStarted(item, func(e StreamEvent) { got = append(got, e) })
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	ev := got[0]
	if ev.Type != EventToolStart {
		t.Fatalf("expected ToolStart, got %+v", ev)
	}
	if ev.ToolName != "WebFetch" {
		t.Fatalf("expected WebFetch for openPage, got %q", ev.ToolName)
	}
	if !strings.Contains(ev.Input, `"url":"https://example.com/docs"`) {
		t.Fatalf("expected url in input, got: %s", ev.Input)
	}
}

func TestEmitCodexItemStartedWebSearchFindInPageAction(t *testing.T) {
	item := codexThreadItem{
		ID:    "ws_3",
		Type:  "webSearch",
		Query: "'release' in https://example.com/changelog",
		Action: json.RawMessage(`{
			"type": "findInPage",
			"pattern": "release",
			"url": "https://example.com/changelog"
		}`),
	}
	var got []StreamEvent
	emitCodexItemStarted(item, func(e StreamEvent) { got = append(got, e) })
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	ev := got[0]
	if ev.Type != EventToolStart || ev.ToolName != "WebSearch" {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if !strings.Contains(ev.Input, `'release' in https://example.com/changelog`) {
		t.Fatalf("expected query containing pattern+url, got: %s", ev.Input)
	}
}

func TestEmitCodexItemCompletedWebSearchEmitsResultText(t *testing.T) {
	cases := []struct {
		name string
		item codexThreadItem
		want string
	}{
		{
			name: "search",
			item: codexThreadItem{
				ID:    "ws_a",
				Type:  "webSearch",
				Query: "rust async",
				Action: json.RawMessage(`{
					"type": "search",
					"queries": ["rust async runtime", "tokio vs async-std"]
				}`),
			},
			want: "Searched: - rust async runtime\n- tokio vs async-std",
		},
		{
			name: "openPage",
			item: codexThreadItem{
				ID:    "ws_b",
				Type:  "webSearch",
				Query: "https://example.com",
				Action: json.RawMessage(`{
					"type": "openPage",
					"url": "https://example.com"
				}`),
			},
			want: "Opened https://example.com",
		},
		{
			name: "findInPage",
			item: codexThreadItem{
				ID:    "ws_c",
				Type:  "webSearch",
				Query: "'foo' in https://example.com",
				Action: json.RawMessage(`{
					"type": "findInPage",
					"pattern": "foo",
					"url": "https://example.com"
				}`),
			},
			want: "Found 'foo' in https://example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []StreamEvent
			emitCodexItemCompleted(tc.item, false, false, func(e StreamEvent) { got = append(got, e) })
			if len(got) != 1 {
				t.Fatalf("expected 1 event, got %d", len(got))
			}
			ev := got[0]
			if ev.Type != EventToolResult {
				t.Fatalf("expected ToolResult, got %+v", ev)
			}
			if ev.Output != tc.want {
				t.Fatalf("output mismatch: want=%q got=%q", tc.want, ev.Output)
			}
		})
	}
}
