package mcp

import (
	"context"
	"strings"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func listedTools(t *testing.T, srv *Server) map[string]bool {
	t.Helper()
	response := srv.mcpServer.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	rpc, ok := response.(gomcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("response type = %T", response)
	}
	result, ok := rpc.Result.(gomcp.ListToolsResult)
	if !ok {
		t.Fatalf("result type = %T", rpc.Result)
	}
	names := map[string]bool{}
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}
	return names
}

func TestTaskToolsAreOptIn(t *testing.T) {
	taskTools := []string{"create_task", "get_task", "list_tasks", "update_task"}
	srv := NewServer(nil, "127.0.0.1:0")

	names := listedTools(t, srv)
	for _, n := range taskTools {
		if names[n] {
			t.Errorf("%s advertised without opt-in", n)
		}
	}
	if !names["ask_question"] {
		t.Error("uncontrolled tool ask_question should stay advertised")
	}

	enabled := true
	srv.SetCategoryChecker(func(c string) bool { return enabled && c == MCPToolCategoryTaskManagement })
	names = listedTools(t, srv)
	for _, n := range taskTools {
		if !names[n] {
			t.Errorf("%s missing after enabling category", n)
		}
	}
}

func TestDisabledToolRejectsCachedInvocation(t *testing.T) {
	srv := NewServer(nil, "127.0.0.1:0")
	tool := srv.mcpServer.ListTools()["list_tasks"]
	if tool == nil {
		t.Fatal("list_tasks should stay registered")
	}
	req := gomcp.CallToolRequest{}
	req.Params.Name = "list_tasks"
	res, err := tool.Handler(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(resultText(res), "disabled in Settings") {
		t.Fatalf("unexpected result %#v", res)
	}
}
