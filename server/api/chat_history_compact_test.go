package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/esonic/nexbox/store"
)

func TestCompactChatHistoryAndDetails(t *testing.T) {
	s := newAgentGateServer(t)
	session, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "history"})
	if err != nil {
		t.Fatal(err)
	}
	parts := []store.MessagePart{
		{Type: "thinking", Content: "private thought"},
		{Type: "tool_use", ToolName: "Edit", ToolCallID: "edit", Input: `{"file_path":"/repo/a"}`},
		{Type: "tool_result", ToolCallID: "edit", Output: "tool output", Diff: "large diff"},
		{Type: "text", Content: "Finished.\n<next-actions>\n- Continue\n</next-actions>"},
	}
	msg, err := s.Store.CreateMessage(session.ID, "assistant", parts, store.MessageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	rr := doRequest(t, s, http.MethodGet, "/api/sessions/"+session.ID+"/messages?limit=0&compact=1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, secret := range []string{"private thought", "tool output", "large diff", "/repo/a"} {
		if strings.Contains(body, secret) {
			t.Fatalf("compact response contains %q: %s", secret, body)
		}
	}
	var response struct {
		Messages []store.ChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Messages) != 1 || !response.Messages[0].LazyWork || response.Messages[0].FileChangeCount != 1 || !strings.Contains(body, "Finished.") || !strings.Contains(body, "Continue") {
		t.Fatalf("summary: %s", body)
	}
	rr = doRequest(t, s, http.MethodGet, "/api/messages/"+msg.ID+"/details?section=files", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "large diff") || strings.Contains(rr.Body.String(), "private thought") || strings.Contains(rr.Body.String(), "tool output") {
		t.Fatalf("files: %d %s", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, s, http.MethodGet, "/api/messages/"+msg.ID+"/details", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "private thought") {
		t.Fatalf("work: %d %s", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, s, http.MethodGet, "/api/sessions/"+session.ID+"/messages?limit=0", nil)
	if !strings.Contains(rr.Body.String(), "private thought") {
		t.Fatalf("legacy response changed: %s", rr.Body.String())
	}
}

func TestFileChangePartsCountsUniqueSuccessfulPaths(t *testing.T) {
	parts := []MessagePart{
		{Type: "tool_use", ToolCallID: "1", ToolName: "Write", Input: `{"path":"a","content":"large file contents"}`},
		{Type: "tool_result", ToolCallID: "1", Diff: "diff1", Output: "large output"},
		{Type: "tool_use", ToolCallID: "2", ToolName: "FileChange", Input: `{"changes":[{"path":"a","kind":"update","diff":"diff2"},{"path":"b","kind":"create"}]}`},
		{Type: "tool_use", ToolCallID: "3", ToolName: "Edit", Input: `{"path":"c"}`},
		{Type: "tool_result", ToolCallID: "3", IsError: true},
	}
	filtered, count := fileChangeParts(parts)
	data, _ := json.Marshal(filtered)
	if count != 2 || strings.Contains(string(data), "large file contents") || strings.Contains(string(data), "large output") || !strings.Contains(string(data), "diff2") {
		t.Fatalf("count %d, parts %s", count, data)
	}
}

func TestCompactChatMessageKeepsVisibleParts(t *testing.T) {
	tests := []struct {
		name  string
		parts []MessagePart
		lazy  bool
		want  string
	}{
		{"plain", []MessagePart{{Type: "text", Content: "hello"}}, false, "hello"},
		{"no answer", []MessagePart{{Type: "thinking", Content: "hidden"}}, true, "tool_group"},
		{"subagent and split answer", []MessagePart{{Type: "tool_use", ToolCallID: "parent", ToolName: "Agent"}, {Type: "text", Content: "one"}, {Type: "tool_use", ToolCallID: "child", ParentToolID: "parent", ToolName: "Read"}, {Type: "text", Content: " two"}, {Type: "image", URL: "image"}}, true, " two"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := compactChatMessage(ChatMessage{Role: "assistant", Parts: tc.parts})
			data, _ := json.Marshal(m)
			if m.LazyWork != tc.lazy || !strings.Contains(string(data), tc.want) {
				t.Fatalf("result: %s", data)
			}
		})
	}
}

func TestCompactPreservesAdjacentTextAndVisibleSubagents(t *testing.T) {
	m := compactChatMessage(ChatMessage{Role: "assistant", Parts: []MessagePart{
		{Type: "thinking", Content: "hidden"}, {Type: "text", Content: "Hello "}, {Type: "text", Content: "world\n<next-actions>\n- Go\n</next-actions>"},
		{Type: "tool_use", ToolCallID: "parent", ToolName: "Agent"},
		{Type: "tool_use", ToolCallID: "child", ParentToolID: "parent", ToolName: "Read", Input: "child details"},
		{Type: "tool_result", ToolCallID: "child", Output: "child result"},
	}})
	data, _ := json.Marshal(m)
	for _, value := range []string{"Hello world", "child details", "child result"} {
		if !strings.Contains(string(data), value) {
			t.Fatalf("missing %q: %s", value, data)
		}
	}
	if !m.LazyWork || strings.Contains(string(data), "hidden") {
		t.Fatalf("unexpected result: %s", data)
	}
}

func TestCompactDefersVisibleFileDetails(t *testing.T) {
	for _, prefix := range [][]MessagePart{nil, {{Type: "thinking", Content: "hidden"}}} {
		parts := append(prefix, MessagePart{Type: "text", Content: "answer"}, MessagePart{Type: "tool_use", ToolCallID: "1", ToolName: "Edit", Input: `{"path":"a","new_string":"private body"}`}, MessagePart{Type: "tool_result", ToolCallID: "1", Output: "tool output", Diff: "large diff"})
		m := compactChatMessage(ChatMessage{Role: "assistant", Parts: parts})
		data, _ := json.Marshal(m)
		for _, secret := range []string{"private body", "tool output", "large diff"} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("unexpected %q in %s", secret, data)
			}
		}
		if !m.LazyFiles || m.FileChangeCount != 1 || !strings.Contains(string(data), `"lazyDetails":true`) || !strings.Contains(string(data), `\"path\":\"a\"`) {
			t.Fatalf("missing summary: %s", data)
		}
	}
}

func TestCompactDefersFileDetailsInsideVisibleSubagent(t *testing.T) {
	m := compactChatMessage(ChatMessage{Role: "assistant", Parts: []MessagePart{
		{Type: "thinking", Content: "hidden"}, {Type: "text", Content: "answer"},
		{Type: "tool_use", ToolCallID: "parent", ToolName: "Agent"},
		{Type: "tool_use", ToolCallID: "child", ParentToolID: "parent", ToolName: "Write", Input: `{"path":"nested","content":"secret"}`},
		{Type: "tool_result", ToolCallID: "child", Diff: "large diff"},
	}})
	data, _ := json.Marshal(m)
	if !m.LazyFiles || m.FileChangeCount != 1 || !strings.Contains(string(data), `"parentToolCallId":"parent"`) || !strings.Contains(string(data), `"lazyDetails":true`) || strings.Contains(string(data), "large diff") || strings.Contains(string(data), "secret") {
		t.Fatalf("unexpected summary: %s", data)
	}
}
