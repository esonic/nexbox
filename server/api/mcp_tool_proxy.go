package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/esonic/nexbox/mcpidentity"
	"github.com/esonic/nexbox/store"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

type mcpProxyRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name string `json:"name"`
	} `json:"params"`
}

// Some MCP servers acknowledge notifications with 204 rather than 202.
// Both have an empty body, but strict MCP clients accept only 200/202 here.
// Never apply this to requests with an ID: they still need a real RPC result.
func normalizeMCPNotificationResponse(request mcpProxyRequest, response *http.Response) {
	if response.StatusCode == http.StatusNoContent && len(request.ID) == 0 && strings.HasPrefix(request.Method, "notifications/") {
		response.StatusCode = http.StatusAccepted
		response.Status = "202 Accepted"
	}
}

func installedMCPRuntimeKey(mcp *store.InstalledMCP) string {
	entry, _ := parseInstalledMCPManifest(mcp.Manifest)
	if entry.Key == "" {
		entry.Key = mcp.MCPKey
	}
	return mcpidentity.ServerKey(entry.Key)
}

// translateMCPToolCall changes only the name; arguments and other extensions
// remain opaque. OAuth/installation identity is never derived from this alias.
func (s *Server) translateMCPToolCall(mcp *store.InstalledMCP, body []byte, request mcpProxyRequest) ([]byte, error) {
	value, ok := s.mcpRuntimeTools.Load(mcpCatalogID(mcp))
	if !ok {
		return body, nil // legacy callers can still send an upstream name directly
	}
	original, ok := value.(*mcpToolCatalog).aliases[request.Params.Name]
	if !ok || original == request.Params.Name {
		return body, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(envelope["params"], &params); err != nil {
		return nil, err
	}
	params["name"], _ = json.Marshal(original)
	envelope["params"], _ = json.Marshal(params)
	return json.Marshal(envelope)
}

// readMCPReply accepts JSON and actual SSE, including upstreams that mislabel
// a JSON response as SSE. Waiting ends at this request's result, not at stream
// closure, since a Streamable HTTP connection can remain open indefinitely.
func readMCPReply(body io.Reader, requestID json.RawMessage) (map[string]json.RawMessage, error) {
	reader := bufio.NewReader(body)
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == ' ' || b == '\n' || b == '\r' || b == '\t' {
			continue
		}
		_ = reader.UnreadByte()
		if b == '{' {
			var reply map[string]json.RawMessage
			err = json.NewDecoder(io.LimitReader(reader, 8<<20)).Decode(&reply)
			if err == nil && !bytes.Equal(bytes.TrimSpace(reply["id"]), bytes.TrimSpace(requestID)) {
				err = fmt.Errorf("MCP reply did not match the request ID")
			}
			return reply, err
		}
		break
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if line == "" && len(data) > 0 {
			var reply map[string]json.RawMessage
			if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &reply); err != nil {
				return nil, err
			}
			if bytes.Equal(bytes.TrimSpace(reply["id"]), bytes.TrimSpace(requestID)) {
				return reply, nil
			}
			data = nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("MCP stream ended before the tools/list reply")
}

func (s *Server) rewriteMCPToolList(resp *http.Response, mcp *store.InstalledMCP, requestID json.RawMessage) error {
	defer resp.Body.Close()
	reply, err := readMCPReply(resp.Body, requestID)
	if err != nil {
		return err
	}
	if len(reply["result"]) > 0 {
		var result map[string]json.RawMessage
		if err := json.Unmarshal(reply["result"], &result); err != nil {
			return err
		}
		var tools []map[string]json.RawMessage
		if err := json.Unmarshal(result["tools"], &tools); err != nil {
			return err
		}
		key := installedMCPRuntimeKey(mcp)
		names := make([]gomcp.Tool, 0, len(tools))
		for _, tool := range tools {
			var name string
			if err := json.Unmarshal(tool["name"], &name); err != nil || name == "" {
				return fmt.Errorf("MCP returned an invalid tool name")
			}
			names = append(names, gomcp.Tool{Name: name})
			tool["name"], _ = json.Marshal(mcpidentity.ToolKey(key, name))
		}
		if err := s.mergeMCPTools(mcp, key, names); err != nil {
			return err
		}
		result["tools"], _ = json.Marshal(tools)
		reply["result"], _ = json.Marshal(result)
	}
	encoded, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	resp.Body = io.NopCloser(bytes.NewReader(encoded))
	resp.ContentLength = int64(len(encoded))
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Content-Length", fmt.Sprint(len(encoded)))
	resp.Header.Del("Content-Encoding")
	resp.TransferEncoding = nil
	return nil
}

func (s *Server) mergeMCPTools(mcp *store.InstalledMCP, key string, tools []gomcp.Tool) error {
	id := mcpCatalogID(mcp)
	for {
		previous, loaded := s.mcpRuntimeTools.Load(id)
		next := &mcpToolCatalog{aliases: make(map[string]string)}
		if loaded {
			for alias, original := range previous.(*mcpToolCatalog).aliases {
				next.aliases[alias] = original
			}
		}
		seen := make(map[string]bool)
		for _, tool := range tools {
			alias := mcpidentity.ToolKey(key, tool.Name)
			if seen[alias] || (next.aliases[alias] != "" && next.aliases[alias] != tool.Name) {
				return fmt.Errorf("MCP returned duplicate or conflicting tool names")
			}
			seen[alias] = true
			next.aliases[alias] = tool.Name
		}
		if loaded {
			if s.mcpRuntimeTools.CompareAndSwap(id, previous, next) {
				return nil
			}
		} else if _, raced := s.mcpRuntimeTools.LoadOrStore(id, next); !raced {
			return nil
		}
	}
}
