package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/esonic/nexbox/store"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

type browserControlStore struct {
	store.Store
	features string
}

func (s *browserControlStore) GetSession(id string) (*store.Session, error) {
	return &store.Session{ID: id, Features: s.features}, nil
}

type browserControlEnabler struct {
	calls int
	err   error
}

func (e *browserControlEnabler) EnableChrome(string) (string, []string, error) {
	e.calls++
	return "", nil, e.err
}

func TestBrowserControlEnableGuidance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		features string
		want     string
	}{
		{"first enable", "", "has been enabled"},
		{"already enabled", "chrome_devtools", "already enabled"},
		{"other features", "other, chrome_devtools ", "already enabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(&browserControlStore{features: tc.features}, "127.0.0.1:0")
			enabler := &browserControlEnabler{}
			srv.SetChromeEnabler(enabler)
			ctx := context.WithValue(context.Background(), nexboxSessionKey{}, "session")
			tool := srv.mcpServer.ListTools()["enable_browser_control"]
			result, err := tool.Handler(ctx, gomcp.CallToolRequest{})
			if err != nil || result.IsError {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			text := resultText(result)
			if !strings.Contains(text, tc.want) || !strings.Contains(text, "continue the user's task now") {
				t.Fatalf("unexpected guidance: %s", text)
			}
			if !strings.Contains(text, "next message") || strings.Contains(text, "ask them to send a new message") {
				t.Fatalf("must explain delayed tools without unconditionally stopping: %s", text)
			}
			if enabler.calls != 1 {
				t.Fatalf("enable calls=%d, want 1", enabler.calls)
			}
			if !strings.Contains(tool.Tool.Description, "only when") {
				t.Fatalf("description must avoid redundant enabling: %s", tool.Tool.Description)
			}
		})
	}
}

func TestBrowserControlEnableFailure(t *testing.T) {
	srv := NewServer(&browserControlStore{features: "chrome_devtools"}, "127.0.0.1:0")
	srv.SetChromeEnabler(&browserControlEnabler{err: errors.New("MCP unavailable")})
	ctx := context.WithValue(context.Background(), nexboxSessionKey{}, "session")
	result, err := srv.handleEnableBrowserControl(ctx, gomcp.CallToolRequest{})
	if err != nil || !result.IsError || !strings.Contains(resultText(result), "MCP unavailable") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
