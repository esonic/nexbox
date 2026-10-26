package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/mcpidentity"
	"github.com/esonic/nexbox/store"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

// Decode legacy string/wrapper formats once, identically for discovery,
// forwarding and runtime configuration. Display/OAuth identity lives outside
// this document and must never be changed while normalizing a runtime key.
func parseInstalledMCPManifest(raw string) (agent.MCPServerEntry, error) {
	var entry agent.MCPServerEntry
	var inner string
	if json.Unmarshal([]byte(raw), &inner) == nil {
		raw = inner
	}
	var wrapped struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapped); err != nil {
		return entry, fmt.Errorf("invalid MCP manifest")
	}
	key := ""
	if len(wrapped.Servers) > 0 {
		if len(wrapped.Servers) != 1 {
			return entry, fmt.Errorf("one MCP server is required per installation")
		}
		for name, server := range wrapped.Servers {
			key, raw = name, string(server)
		}
	}
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return entry, fmt.Errorf("invalid MCP server configuration")
	}
	if key != "" {
		entry.Key = key
	}
	if entry.Cmd == "" {
		var legacy struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal([]byte(raw), &legacy)
		entry.Cmd = legacy.Command
	}
	switch {
	case entry.URL != "":
		entry.Type = "http"
	case entry.Cmd != "":
		entry.Type = "stdio"
	}
	return entry, nil
}

// Availability is decided before selecting a CLI's MCP implementation. A CLI
// silently dropping a failed initialize must not turn an installed connector
// into "no such tool" in the model's view of the conversation.
type mcpRuntimeStatus struct {
	Name      string `json:"name"`
	Key       string `json:"key"`
	State     string `json:"state"`
	ToolCount int    `json:"tool_count,omitempty"`
	Message   string `json:"message,omitempty"`
}

type mcpToolCatalog struct {
	aliases map[string]string // model-facing MCP tool name -> upstream name
}

func mcpCatalogID(mcp *store.InstalledMCP) string {
	// Reinstallation with a different endpoint/schema must not reuse aliases.
	return fmt.Sprintf("%s\x00%s\x00%x", mcp.ProjectID, mcp.Name, sha256.Sum256([]byte(mcp.Manifest)))
}

func (s *Server) rememberMCPTools(mcp *store.InstalledMCP, key string, tools []gomcp.Tool) error {
	aliases := make(map[string]string, len(tools))
	for _, tool := range tools {
		if tool.Name == "" {
			return fmt.Errorf("MCP returned an empty tool name")
		}
		alias := mcpidentity.ToolKey(key, tool.Name)
		if _, exists := aliases[alias]; exists {
			return fmt.Errorf("MCP returned duplicate or conflicting tool names")
		}
		aliases[alias] = tool.Name
	}
	s.mcpRuntimeTools.Store(mcpCatalogID(mcp), &mcpToolCatalog{aliases: aliases})
	return nil
}

type mcpProbeTransport struct {
	unauthorized atomic.Bool
}

func (t *mcpProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(r)
	if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		t.unauthorized.Store(true)
	}
	if resp != nil && resp.StatusCode == http.StatusNoContent && r.Method == http.MethodPost && r.GetBody != nil {
		if body, bodyErr := r.GetBody(); bodyErr == nil {
			var request mcpProxyRequest
			if json.NewDecoder(io.LimitReader(body, 64<<10)).Decode(&request) == nil {
				normalizeMCPNotificationResponse(request, resp)
			}
			_ = body.Close()
		}
	}
	return resp, err
}

// probeInstalledMCP does real discovery, not just a connectivity/auth check.
// It uses the same NexBox-owned authorization as the runtime proxy and never
// hands OAuth credentials to an agent process.
func (s *Server) probeInstalledMCP(ctx context.Context, mcp *store.InstalledMCP, key string) mcpRuntimeStatus {
	status := mcpRuntimeStatus{Name: mcp.Name, Key: key, State: "unavailable"}
	target, err := mcpTargetURL(mcp.Manifest)
	if err != nil {
		status.Message = "Invalid MCP HTTP configuration."
		return status
	}
	headers, err := s.mcpRequestHeaders(mcp)
	if err != nil {
		status.Message = "Could not resolve MCP authorization."
		return status
	}
	probeTransport := &mcpProbeTransport{}
	client, err := mcpclient.NewStreamableHttpClient(target.String(),
		transport.WithHTTPHeaders(headers),
		transport.WithHTTPBasicClient(&http.Client{Transport: probeTransport, Timeout: 10 * time.Second}))
	if err != nil {
		status.Message = "Invalid MCP HTTP endpoint."
		return status
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err = client.Start(ctx)
	phase := "initialization"
	if err == nil {
		_, err = client.Initialize(ctx, gomcp.InitializeRequest{Params: gomcp.InitializeParams{
			ProtocolVersion: gomcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      gomcp.Implementation{Name: "nexbox", Version: "1"},
		}})
	}
	var tools []gomcp.Tool
	if err == nil {
		phase = "tool discovery"
		seen := map[string]bool{}
		request := gomcp.ListToolsRequest{}
		for {
			var result *gomcp.ListToolsResult
			result, err = client.ListTools(ctx, request)
			if err != nil {
				break
			}
			tools = append(tools, result.Tools...)
			if result.NextCursor == "" {
				break
			}
			cursor := string(result.NextCursor)
			if seen[cursor] || len(seen) >= 100 {
				err = fmt.Errorf("invalid MCP tool pagination")
				break
			}
			seen[cursor] = true
			request.Params.Cursor = result.NextCursor
		}
	}
	if err != nil {
		if probeTransport.unauthorized.Load() {
			oauth := mcp.AuthType == "oauth" || mcp.AuthType == "oauth_discovery"
			if mcp.AuthType == "none" {
				oauth = s.discoverAndMarkMCPOAuth(mcp, target.String())
			}
			if oauth {
				status.State = "needs_auth"
				status.Message = "Installed, but OAuth authorization is required. Click Authorize in NexBox, then send another message in this conversation."
				s.broadcastNeedsAuth(mcp.Name, mcp.ProjectID)
			} else {
				status.State = "auth_rejected"
				status.Message = "Installed, but the MCP server rejected its configured credentials."
			}
		} else {
			status.Message = fmt.Sprintf("Installed, but MCP %s failed. Check the connector endpoint and retry.", phase)
		}
		return status
	}
	if err := s.rememberMCPTools(mcp, key, tools); err != nil {
		status.Message = err.Error()
		return status
	}
	status.State, status.ToolCount = "ready", len(tools)
	return status
}

// prepareInstalledMCPs is shared by all agents, regardless of the selected backend.
// Failed HTTP connectors are omitted consistently, with authoritative status
// supplied to both the model and the UI. Authorization is checked every turn,
// so completing OAuth does not require deleting or recreating a conversation.
func (s *Server) prepareInstalledMCPs(ctx context.Context, entries []agent.MCPServerEntry) ([]agent.MCPServerEntry, []mcpRuntimeStatus) {
	statuses := make([]mcpRuntimeStatus, len(entries))
	counts := map[string]int{}
	for _, entry := range entries {
		counts[entry.Key]++
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i, entry := range entries {
		if counts[entry.Key] > 1 {
			statuses[i] = mcpRuntimeStatus{Name: entry.PackageName, Key: entry.Key, State: "unavailable", Message: "Multiple installed connectors use this MCP key; assign distinct technical identifiers."}
			continue
		}
		if entry.Type != "http" {
			statuses[i] = mcpRuntimeStatus{Name: entry.PackageName, Key: entry.Key, State: "runtime_managed", Message: "Local stdio connector; availability is determined by the agent runtime."}
			continue
		}
		wg.Add(1)
		go func(i int, entry agent.MCPServerEntry) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				statuses[i] = mcpRuntimeStatus{Name: entry.PackageName, Key: entry.Key, State: "unavailable", Message: "MCP discovery was cancelled."}
				return
			}
			mcp, err := s.Store.GetInstalledMCP(entry.PackageName, entry.ProjectID)
			if err != nil || mcp == nil {
				statuses[i] = mcpRuntimeStatus{Name: entry.PackageName, Key: entry.Key, State: "unavailable", Message: "The MCP installation no longer exists."}
				return
			}
			statuses[i] = s.probeInstalledMCP(ctx, mcp, entry.Key)
		}(i, entry)
	}
	wg.Wait()
	ready := make([]agent.MCPServerEntry, 0, len(entries))
	for i, status := range statuses {
		if status.State == "ready" || status.State == "runtime_managed" {
			ready = append(ready, entries[i])
		}
	}
	return ready, statuses
}

func mcpStatusContext(statuses []mcpRuntimeStatus) string {
	if len(statuses) == 0 {
		return ""
	}
	data, _ := json.Marshal(statuses)
	return "\n\n[NexBox MCP availability for this turn]\n" + string(data) +
		"\nThese connectors are installed. For HTTP connectors, only ready entries have usable discovered tools. Stdio entries marked runtime_managed have not been preflighted; use the actual runtime tool inventory to determine their availability. Explain needs_auth as an authorization requirement, not as a missing installation. Do not claim a connector is usable merely because its name appears in earlier conversation history."
}

func mcpStatusSummary(statuses []mcpRuntimeStatus) string {
	var lines []string
	for _, status := range statuses {
		if status.State != "ready" && status.State != "runtime_managed" {
			lines = append(lines, fmt.Sprintf("MCP %s: %s", status.Name, status.Message))
		}
	}
	return strings.Join(lines, "\n")
}
