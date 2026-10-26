package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/mcpidentity"
	"github.com/esonic/nexbox/store"
	"github.com/go-chi/chi/v5"
	mcpclient "github.com/mark3labs/mcp-go/client"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func newMCPRuntimeTestServer(t *testing.T) *Server {
	t.Helper()
	s := newAgentGateServer(t)
	return s
}

func testRuntimeMCP(t *testing.T, s *Server, endpoint, projectID string) store.InstalledMCP {
	t.Helper()
	mcp := store.InstalledMCP{Name: "OKLog User", MCPKey: "OKLog User", ProjectID: projectID,
		Manifest: fmt.Sprintf(`{"key":"OKLog User","type":"http","url":%q}`, endpoint), AuthType: "oauth"}
	if err := s.Store.UpsertInstalledMCP(mcp); err != nil {
		t.Fatal(err)
	}
	return mcp
}

func runtimeMCPHandler(authorized *atomic.Bool, tools []string, calls chan string, sse bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !authorized.Load() {
			w.Header().Set("WWW-Authenticate", `Bearer realm="mcp"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"authorization_required"}`))
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name            string `json:"name"`
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": request.Params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "mock-log", "version": "1"}}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			listed := make([]map[string]any, 0, len(tools))
			for _, name := range tools {
				listed = append(listed, map[string]any{"name": name, "description": "Read mock logs", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}, "_meta": map[string]string{"source": "retained"}})
			}
			result = map[string]any{"tools": listed}
		case "tools/call":
			if calls != nil {
				calls <- request.Params.Name
			}
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": "mock logs"}}}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			data, _ := json.Marshal(reply)
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		} else {
			writeJSON(w, http.StatusOK, reply)
		}
	}
}

// Match the installed Lark server: older protocol, data-only SSE events and
// 204 acknowledgements for notifications/initialized.
func legacyRuntimeMCPHandler(authorized *atomic.Bool, tools []string) http.HandlerFunc {
	regular := runtimeMCPHandler(authorized, tools, nil, true)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			regular(w, r)
			return
		}
		var request map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var method string
		_ = json.Unmarshal(request["method"], &method)
		if method == "notifications/initialized" && authorized.Load() {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if method == "initialize" {
			var params map[string]json.RawMessage
			_ = json.Unmarshal(request["params"], &params)
			params["protocolVersion"] = json.RawMessage(`"2024-11-05"`)
			request["params"], _ = json.Marshal(params)
		}
		body, _ := json.Marshal(request)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		recorder := httptest.NewRecorder()
		regular(recorder, r)
		for key, values := range recorder.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorder.Code)
		_, _ = io.WriteString(w, strings.ReplaceAll(recorder.Body.String(), "event: message\n", ""))
	}
}

func TestMCPLegacyServer204AcknowledgementIsReady(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	var authorized atomic.Bool
	authorized.Store(true)
	upstream := httptest.NewServer(legacyRuntimeMCPHandler(&authorized, []string{"lark_docs_get_content"}))
	defer upstream.Close()
	mcp := testRuntimeMCP(t, s, upstream.URL, "")
	status := s.probeInstalledMCP(context.Background(), &mcp, installedMCPRuntimeKey(&mcp))
	if status.State != "ready" || status.ToolCount != 1 {
		t.Fatalf("legacy MCP incorrectly marked unavailable: %+v", status)
	}
	for _, request := range []mcpProxyRequest{{Method: "tools/list", ID: json.RawMessage(`1`)}, {Method: "tools/list"}, {Method: "notifications/initialized", ID: json.RawMessage(`1`)}} {
		response := &http.Response{StatusCode: http.StatusNoContent}
		normalizeMCPNotificationResponse(request, response)
		if response.StatusCode != http.StatusNoContent {
			t.Fatal("a request that needs a result was incorrectly acknowledged")
		}
	}
}

func TestMCPReadOnlyEndpointIntegration(t *testing.T) {
	endpoint := os.Getenv("MCP_READONLY_ENDPOINT")
	if endpoint == "" {
		t.Skip("set MCP_READONLY_ENDPOINT to inspect initialization and tool discovery only")
	}
	s := newMCPRuntimeTestServer(t)
	mcp := testRuntimeMCP(t, s, endpoint, "")
	status := s.probeInstalledMCP(context.Background(), &mcp, installedMCPRuntimeKey(&mcp))
	if status.State != "ready" {
		t.Fatalf("read-only discovery: %+v", status)
	}
	t.Logf("read-only discovery: state=%s tools=%d", status.State, status.ToolCount)
}

func TestMCPInstallationSeparatesDisplayNameAndManifestKey(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	for _, key := range []string{"oklog_user", "OKLog User"} {
		body := map[string]any{"name": "OKLog User", "mcp_key": "display name sent by old client", "manifest": map[string]any{"mcpServers": map[string]any{key: map[string]string{"url": "https://example.com/mcp"}}}}
		encoded, _ := json.Marshal(body)
		rr := httptest.NewRecorder()
		s.InstallMCP(rr, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded))))
		if rr.Code != http.StatusOK {
			t.Fatalf("install: %s", rr.Body.String())
		}
		installed, err := s.Store.GetInstalledMCP("OKLog User", "")
		if err != nil {
			t.Fatal(err)
		}
		var manifest agent.MCPServerEntry
		if err := json.Unmarshal([]byte(installed.Manifest), &manifest); err != nil {
			t.Fatal(err)
		}
		if installed.Name != "OKLog User" || installed.MCPKey != mcpidentity.ServerKey(key) || manifest.Key != installed.MCPKey {
			t.Fatalf("name/key mismatch: name=%s key=%s manifest=%s", installed.Name, installed.MCPKey, manifest.Key)
		}
	}
}

func TestMCPProxyNormalizesToolsForEveryClient(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("SSE=%v", sse), func(t *testing.T) {
			s := newMCPRuntimeTestServer(t)
			var authorized atomic.Bool
			authorized.Store(true)
			names := []string{"query.logs", "query_logs", strings.Repeat("x", 100)}
			calls := make(chan string, len(names))
			upstream := httptest.NewServer(runtimeMCPHandler(&authorized, names, calls, sse))
			defer upstream.Close()
			mcp := testRuntimeMCP(t, s, upstream.URL, "")
			session, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "mcp"})
			if err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			router.Handle("/mcp-proxy/{name}", http.HandlerFunc(s.HandleMCPProxy))
			proxy := httptest.NewServer(router)
			defer proxy.Close()
			client, err := mcpclient.NewStreamableHttpClient(proxy.URL + "/mcp-proxy/" + url.PathEscape(mcp.Name) + "?session=" + session.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := client.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := client.Initialize(ctx, gomcp.InitializeRequest{Params: gomcp.InitializeParams{ProtocolVersion: gomcp.LATEST_PROTOCOL_VERSION}}); err != nil {
				t.Fatal(err)
			}
			listed, err := client.ListTools(ctx, gomcp.ListToolsRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if len(listed.Tools) != len(names) {
				t.Fatalf("tools=%d", len(listed.Tools))
			}
			key := installedMCPRuntimeKey(&mcp)
			for i, name := range names {
				if listed.Tools[i].Name != mcpidentity.ToolKey(key, name) {
					t.Fatalf("incorrect tool alias: %q", listed.Tools[i].Name)
				}
				request := gomcp.CallToolRequest{}
				request.Params.Name = listed.Tools[i].Name
				if _, err := client.CallTool(ctx, request); err != nil {
					t.Fatal(err)
				}
				if original := <-calls; original != name {
					t.Fatalf("upstream received %q, want %q", original, name)
				}
			}
		})
	}
}

func TestMCPProxyPreservesUnauthorizedResponse(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	var authorized atomic.Bool
	upstream := httptest.NewServer(runtimeMCPHandler(&authorized, nil, nil, false))
	defer upstream.Close()
	mcp := testRuntimeMCP(t, s, upstream.URL, "")
	session, _ := s.Store.CreateSession(store.CreateSessionRequest{Name: "mcp"})
	router := chi.NewRouter()
	router.Post("/mcp-proxy/{name}", s.HandleMCPProxy)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/mcp-proxy/"+url.PathEscape(mcp.Name)+"?session="+session.ID, strings.NewReader(`{"jsonrpc":"2.0","id":"init-42","method":"initialize"}`)))
	if rr.Code != http.StatusUnauthorized || rr.Header().Get("WWW-Authenticate") != `Bearer realm="mcp"` || rr.Body.String() != `{"error":"authorization_required"}` {
		t.Fatalf("auth response changed: status=%d headers=%v body=%s", rr.Code, rr.Header(), rr.Body.String())
	}
}

func TestMCPAvailabilitySharedByAllAgentsAndRecoversInSameSession(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	s.SessionManager = agent.NewSessionManager()
	requests := make(chan agent.Request, 1)
	backend := mcpRequestCaptureAgent{requests: requests}
	s.Agent, s.AgentName = backend, "codex"
	agentNames := []string{"claude", "codex", "pi", "kimi", "crush", "zeroclaw"}
	s.Agents = make(map[string]agent.Agent, len(agentNames))
	for _, name := range agentNames {
		s.Agents[name] = backend
	}
	project, err := s.Store.CreateProject(store.CreateProjectRequest{Name: "logs", GitRepoPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var authorized atomic.Bool
	upstream := httptest.NewServer(runtimeMCPHandler(&authorized, []string{"query_logs"}, nil, false))
	defer upstream.Close()
	testRuntimeMCP(t, s, upstream.URL, project.ID)
	for _, agentName := range agentNames {
		t.Run(agentName, func(t *testing.T) {
			session, err := s.Store.CreateSession(store.CreateSessionRequest{Name: "logs", ProjectID: project.ID, Agent: agentName})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Store.UpdateSessionAgentID(session.ID, "existing-native-session"); err != nil {
				t.Fatal(err)
			}
			for _, ready := range []bool{false, true} {
				authorized.Store(ready)
				if _, err := s.dispatchUserMessage(session.ID, "query logs", nil, nil, nil, dispatchOpts{noQueue: true}); err != nil {
					t.Fatal(err)
				}
				var request agent.Request
				select {
				case request = <-requests:
				case <-time.After(5 * time.Second):
					t.Fatal("agent request not dispatched")
				}
				deadline := time.Now().Add(3 * time.Second)
				for s.SessionManager.IsActive(session.ID) && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if s.SessionManager.IsActive(session.ID) {
					t.Fatal("turn did not finish")
				}
				if request.SessionID != "existing-native-session" {
					t.Fatal("native session was reset")
				}
				if ready {
					if len(request.ExtraMCPServers) != 1 || request.ExtraMCPServers[0].Key != mcpidentity.ServerKey("OKLog User") || !strings.Contains(request.DynamicContext, `"state":"ready"`) {
						t.Fatalf("ready request: %+v", request)
					}
				} else if len(request.ExtraMCPServers) != 0 || !strings.Contains(request.DynamicContext, `"state":"needs_auth"`) || !strings.Contains(request.DynamicContext, "OKLog User") {
					t.Fatalf("missing common auth status: %+v", request)
				}
			}
		})
	}
}

func TestMCPForwardURLPreservesEndpointQuery(t *testing.T) {
	base, _ := url.Parse("https://example.com/mcp?region=hk&route=default")
	forward, err := buildForwardURL(base, "", "route=logs")
	if err != nil || forward.Query().Get("region") != "hk" || forward.Query().Get("route") != "logs" {
		t.Fatalf("forward=%v err=%v", forward, err)
	}
}

func TestMCPLegacyManifestUsesSameScopeAndKeyEverywhere(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	project, err := s.Store.CreateProject(store.CreateProjectRequest{Name: "scope"})
	if err != nil {
		t.Fatal(err)
	}
	var authorized atomic.Bool
	authorized.Store(true)
	upstream := httptest.NewServer(runtimeMCPHandler(&authorized, []string{"query_logs"}, nil, false))
	defer upstream.Close()
	testRuntimeMCP(t, s, "http://127.0.0.1:1", "")
	mcp := testRuntimeMCP(t, s, upstream.URL, project.ID)
	wrapped, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"OKLog User": map[string]any{"url": upstream.URL, "headers": map[string]string{"X-Org": "scope"}}}})
	doubleEncoded, _ := json.Marshal(string(wrapped))
	mcp.Manifest = string(doubleEncoded)
	if err := s.Store.UpsertInstalledMCP(mcp); err != nil {
		t.Fatal(err)
	}
	s.ListenPort = 9042
	entries, packages := s.resolveInstalledMCPsAndMap(project.ID)
	if len(entries) != 1 || entries[0].ProjectID != project.ID || entries[0].Key != installedMCPRuntimeKey(&mcp) || packages[entries[0].Key] != mcp.Name || !strings.Contains(entries[0].URL, "OKLog%20User") {
		t.Fatalf("inconsistent legacy resolution: %+v", entries)
	}
	ready, statuses := s.prepareInstalledMCPs(context.Background(), entries)
	if len(ready) != 1 || statuses[0].State != "ready" {
		t.Fatalf("legacy discovery failed: %+v", statuses)
	}
	headers, err := s.mcpRequestHeaders(&mcp)
	if err != nil || headers["X-Org"] != "scope" {
		t.Fatalf("headers=%v err=%v", headers, err)
	}
	stored, _ := s.Store.GetInstalledMCP(mcp.Name, project.ID)
	if stored.MCPKey != "OKLog User" || stored.ProjectID != project.ID || stored.AuthType != "oauth" {
		t.Fatal("runtime normalization changed installation/OAuth identity")
	}
}

func TestMCPDiscoveryRejectsBadCatalogsAndDistinguishesAPIKeyAuth(t *testing.T) {
	for _, tc := range []struct {
		name            string
		tools           []string
		authorized      bool
		authType, state string
	}{
		{"duplicate", []string{"query", "query"}, true, "none", "unavailable"},
		{"empty-name", []string{""}, true, "none", "unavailable"},
		{"api-key-rejected", nil, false, "apikey", "auth_rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newMCPRuntimeTestServer(t)
			var authorized atomic.Bool
			authorized.Store(tc.authorized)
			upstream := httptest.NewServer(runtimeMCPHandler(&authorized, tc.tools, nil, false))
			defer upstream.Close()
			mcp := testRuntimeMCP(t, s, upstream.URL, "")
			mcp.AuthType = tc.authType
			if err := s.Store.UpsertInstalledMCP(mcp); err != nil {
				t.Fatal(err)
			}
			entries, _ := s.resolveInstalledMCPsAndMap("")
			ready, statuses := s.prepareInstalledMCPs(context.Background(), entries)
			if len(ready) != 0 || len(statuses) != 1 || statuses[0].State != tc.state || statuses[0].Name != mcp.Name {
				t.Fatalf("bad catalog leaked or lost installation state: %+v", statuses)
			}
		})
	}
}

func TestMCPToolListSSEStopsAtMatchingResultAndPreservesMetadata(t *testing.T) {
	s := newMCPRuntimeTestServer(t)
	mcp := testRuntimeMCP(t, s, "https://example.com/mcp", "")
	reader, writer := io.Pipe()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		defer writer.Close()
		_, _ = fmt.Fprint(writer, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\"}\n\n")
		_, _ = fmt.Fprint(writer, "data: {\"jsonrpc\":\"2.0\",\"id\":\"list-7\",\"result\":{\"nextCursor\":\"next-page\",\"tools\":[{\"name\":\"query.logs\",\"inputSchema\":{\"type\":\"object\"},\"_meta\":{\"preserved\":true}}]}}\n\n")
		<-stop // keep the SSE connection open after delivering the reply
	}()
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}
	done := make(chan error, 1)
	go func() { done <- s.rewriteMCPToolList(response, &mcp, json.RawMessage(`"list-7"`)) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("tools/list waited for SSE connection closure")
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(data), `"nextCursor":"next-page"`) || !strings.Contains(string(data), `"preserved":true`) || !strings.Contains(string(data), `"id":"list-7"`) || strings.Contains(string(data), `"name":"query.logs"`) {
		t.Fatalf("reply fields were lost or tool name not normalized: %s", data)
	}
}

type mcpRequestCaptureAgent struct{ requests chan agent.Request }

func (mcpRequestCaptureAgent) Capabilities() agent.Capabilities {
	return agent.Capabilities{ChromeDevTools: true}
}
func (a mcpRequestCaptureAgent) SendMessage(_ context.Context, req agent.Request, _ agent.EventCallback) (agent.Result, error) {
	a.requests <- req
	return agent.Result{}, nil
}
