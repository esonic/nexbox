package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/store"
	"github.com/go-chi/chi/v5"
)

// Opt-in native-client checks use only loopback MCP/model endpoints. They do
// not invoke a real model, query production logs, or modify CLI configuration.
func nativeMCPFixture(t *testing.T, binary string) (string, string) {
	t.Helper()
	if os.Getenv("MCP_NATIVE_INTEGRATION") != "1" {
		t.Skip("set MCP_NATIVE_INTEGRATION=1 to verify installed native CLIs")
	}
	if _, err := exec.LookPath(binary); err != nil {
		t.Skip(binary + " is not installed")
	}
	s := newMCPRuntimeTestServer(t)
	var authorized atomic.Bool
	authorized.Store(true)
	upstream := httptest.NewServer(legacyRuntimeMCPHandler(&authorized, []string{"query.logs"}))
	t.Cleanup(upstream.Close)
	mcp := testRuntimeMCP(t, s, upstream.URL, "")
	session, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "native-mcp-check"})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Handle("/mcp-proxy/{name}", http.HandlerFunc(s.HandleMCPProxy))
	proxy := httptest.NewServer(router)
	t.Cleanup(proxy.Close)
	return installedMCPRuntimeKey(&mcp), proxy.URL + "/mcp-proxy/" + url.PathEscape(mcp.Name) + "?session=" + session.ID
}

func TestNativeCodexDiscoversNormalizedMCP(t *testing.T) {
	key, endpoint := nativeMCPFixture(t, "codex")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "app-server", "--listen", "stdio://", "-c", fmt.Sprintf("mcp_servers={%s={url=%q}}", key, endpoint))
	cmd.Dir = t.TempDir()
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	replies := make(chan map[string]json.RawMessage, 100)
	go func() {
		defer close(replies)
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		for scanner.Scan() {
			var record map[string]json.RawMessage
			if json.Unmarshal(scanner.Bytes(), &record) == nil {
				select {
				case replies <- record:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	nextID := 0
	call := func(method string, params any) json.RawMessage {
		t.Helper()
		nextID++
		encoded, _ := json.Marshal(map[string]any{"id": nextID, "method": method, "params": params})
		if _, err := fmt.Fprintln(in, string(encoded)); err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case reply, ok := <-replies:
				if !ok {
					t.Fatal("native app-server closed before " + method)
				}
				if string(reply["id"]) != fmt.Sprint(nextID) {
					continue
				}
				if len(reply["error"]) > 0 {
					t.Fatalf("native %s: %s", method, reply["error"])
				}
				return reply["result"]
			case <-ctx.Done():
				t.Fatal("native app-server timed out during " + method)
			}
		}
	}
	call("initialize", map[string]any{"clientInfo": map[string]string{"name": "nexbox-mcp-test", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}})
	_, _ = fmt.Fprintln(in, `{"method":"initialized"}`)
	thread := call("thread/start", map[string]any{"cwd": cmd.Dir, "ephemeral": true})
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(thread, &started); err != nil {
		t.Fatal(err)
	}
	status := call("mcpServerStatus/list", map[string]any{"threadId": started.Thread.ID, "serverName": key})
	var result struct {
		Data []struct {
			Name       string                     `json:"name"`
			Tools      map[string]json.RawMessage `json:"tools"`
			ToolsError *string                    `json:"toolsError"`
		} `json:"data"`
	}
	if err := json.Unmarshal(status, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 1 || result.Data[0].Name != key || len(result.Data[0].Tools) != 1 || result.Data[0].ToolsError != nil {
		t.Fatalf("native discovery did not expose the connector: %s", status)
	}
	for name := range result.Data[0].Tools {
		t.Logf("native Codex discovered %s", name)
	}
}

func TestNativePiSendsPortableMCPToolNames(t *testing.T) {
	key, endpoint := nativeMCPFixture(t, "pi")
	requests := make(chan []string, 1)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		var names []string
		for _, tool := range payload.Tools {
			names = append(names, tool.Name)
		}
		requests <- names
		w.Header().Set("Content-Type", "text/event-stream")
		item := map[string]any{"id": "msg_mock", "type": "message", "role": "assistant", "status": "completed", "content": []map[string]any{{"type": "output_text", "text": "OK", "annotations": []any{}}}}
		for _, event := range []map[string]any{
			{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "msg_mock", "type": "message", "role": "assistant", "content": []any{}}},
			{"type": "response.content_part.added", "output_index": 0, "content_index": 0, "item_id": "msg_mock", "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}},
			{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "item_id": "msg_mock", "delta": "OK"},
			{"type": "response.output_item.done", "output_index": 0, "item": item},
			{"type": "response.completed", "response": map[string]any{"id": "resp_mock", "status": "completed", "output": []any{item}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}}},
		} {
			encoded, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		}
	}))
	defer model.Close()
	dir := t.TempDir()
	config, _ := json.Marshal([]map[string]string{{"key": key, "url": endpoint}})
	configPath := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	t.Setenv("NEXBOX_PI_MCP_CONFIG", configPath)
	providers, _ := json.Marshal(map[string]any{"local": map[string]any{"baseUrl": model.URL + "/v1", "api": "openai-responses", "apiKey": "local-test-placeholder", "models": []map[string]any{{"id": "mock", "name": "Local model", "contextWindow": 200000, "maxTokens": 1000, "input": []string{"text"}, "reasoning": false, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}}}})
	if err := os.WriteFile(filepath.Join(dir, "models.json"), append([]byte(`{"providers":`), append(providers, '}')...), 0600); err != nil {
		t.Fatal(err)
	}
	extension, _ := filepath.Abs("../agent/pi_nexbox_extension.ts")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pi", "--print", "--no-session", "--no-approve", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-context-files", "--offline", "--extension", extension, "--model", "local/mock", "Reply OK; do not invoke tools.")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native Pi failed: %v: %s", err, output)
	}
	select {
	case names := <-requests:
		found := false
		for _, name := range names {
			if strings.HasPrefix(name, "mcp__"+key+"__") {
				found = true
			}
			if strings.ContainsAny(name, " .") || len(name) > 64 {
				t.Fatalf("nonportable tool in native request: %q", name)
			}
		}
		if !found {
			t.Fatalf("MCP tool missing from native Pi request: %v", names)
		}
		t.Logf("native Pi model request included normalized MCP tools")
	default:
		t.Fatal("Pi never reached the local model endpoint")
	}
}

func TestNativeClaudeDiscoversNormalizedMCP(t *testing.T) {
	key, endpoint := nativeMCPFixture(t, "claude")
	model := nativeAnthropicModel(nil)
	defer model.Close()
	dir := t.TempDir()
	config, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{key: map[string]string{"type": "http", "url": endpoint}}})
	configPath := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("ANTHROPIC_BASE_URL", model.URL)
	t.Setenv("ANTHROPIC_API_KEY", "local-test-placeholder")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "--print", "--no-session-persistence", "--strict-mcp-config", "--mcp-config", configPath, "--setting-sources", "", "--output-format", "stream-json", "--verbose", "--model", "claude-sonnet-4-6", "Reply OK; do not invoke tools.")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native Claude failed: %v: %s", err, output)
	}
	found := false
	for _, line := range strings.Split(string(output), "\n") {
		var event struct {
			Type    string   `json:"type"`
			Subtype string   `json:"subtype"`
			Tools   []string `json:"tools"`
			IsError bool     `json:"is_error"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if event.Type == "result" && event.IsError {
			t.Fatalf("native Claude returned an error: %s", line)
		}
		if event.Type == "system" && event.Subtype == "init" {
			for _, name := range event.Tools {
				if strings.HasPrefix(name, "mcp__"+key+"__") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("native Claude did not register the normalized MCP: %s", output)
	}
	t.Log("native Claude registered normalized MCP tools")
}

func nativeAnthropicModel(capture func([]string)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}
		if capture != nil {
			var payload struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			names := make([]string, 0, len(payload.Tools))
			for _, tool := range payload.Tools {
				names = append(names, tool.Name)
			}
			capture(names)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []map[string]any{
			{"type": "message_start", "message": map[string]any{"id": "msg_mock", "type": "message", "role": "assistant", "model": "claude-sonnet-4-6", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 1, "output_tokens": 0}}},
			{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}},
			{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "OK"}},
			{"type": "content_block_stop", "index": 0},
			{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 1}},
			{"type": "message_stop"},
		} {
			encoded, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], encoded)
		}
	}))
}

func TestNativeKimiDiscoversNormalizedMCP(t *testing.T) {
	name := "kimi"
	t.Run(name, func(t *testing.T) {
		key, endpoint := nativeMCPFixture(t, "kimi")
		requests := make(chan []string, 8)
		model := nativeAnthropicModel(func(names []string) { requests <- names })
		defer model.Close()
		home := t.TempDir()
		t.Setenv("KIMI_CODE_HOME", home)
		config := fmt.Sprintf(`default_model = "mock"
[providers.mock]
type = "anthropic"
base_url = %q
api_key = "local-test-placeholder"
[models.mock]
provider = "mock"
model = "claude-sonnet-4-6"
max_context_size = 200000
capabilities = ["tool_use"]
`, model.URL)
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		backend := agent.NewKimiAgent()
		backend.Verbose = false
		selectedModel := "mock"

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := backend.SendMessage(ctx, agent.Request{WorkDir: t.TempDir(), Model: selectedModel, Message: "Reply OK; do not invoke tools.", ExtraMCPServers: []agent.MCPServerEntry{{Key: key, Type: "http", URL: endpoint}}}, func(agent.StreamEvent) {})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case names := <-requests:
			found := false
			for _, tool := range names {
				if strings.Contains(tool, key) {
					found = true
					t.Logf("%s model request included %s", name, tool)
				}
			}
			if !found {
				t.Fatalf("normalized connector missing from %s model request: %v", name, names)
			}
		default:
			t.Fatal("Kimi never reached the local model endpoint")
		}
	})
}
