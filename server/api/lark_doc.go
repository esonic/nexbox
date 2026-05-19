package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

const defaultMCPName = "OKEngine LARK MCP"

var larkHTTPClient = &http.Client{Timeout: 30 * time.Second}

// POST /api/assets/lark-fetch
// Body: { "url": "https://..." }
// Calls the lark_docs_get_content tool on the Lark MCP and returns { "title": "...", "content": "..." }.
func (s *Server) FetchLarkDoc(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}

	mcp, err := s.findLarkMCP()
	if err != nil {
		writeError(w, http.StatusBadRequest, "Lark MCP not found: "+err.Error())
		return
	}

	targetURL, err := mcpTargetURL(mcp.Manifest)
	if err != nil {
		writeError(w, http.StatusBadGateway, "invalid Lark MCP manifest: "+err.Error())
		return
	}
	authHeader, err := s.mcpAuthHeader(mcp)
	if err != nil {
		writeError(w, http.StatusBadGateway, "auth error: "+err.Error())
		return
	}
	mcpBase := targetURL.String()

	docID, err := larkDocIDFromURL(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "cannot extract document ID from URL: "+err.Error())
		return
	}

	raw, err := s.larkCallMCPTool(mcp, mcpBase, authHeader, "lark_docs_get_content", map[string]any{"doc_token": docID})
	if err != nil {
		writeError(w, http.StatusBadGateway, "tools/call failed: "+err.Error())
		return
	}

	title, content := extractLarkDoc(raw)
	writeJSON(w, http.StatusOK, map[string]string{"title": title, "content": content})
}

// POST /api/assets/:id/publish-to-lark
// Body: { "title": "optional override" }
// Publishes the asset content to Lark via lark_drive_import_document and returns { "url": "..." }.
func (s *Server) PublishLarkDoc(w http.ResponseWriter, r *http.Request) {
	assetID := chi.URLParam(r, "assetId")

	var req struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	asset, err := s.Store.GetAsset(assetID)
	if err != nil || asset == nil {
		writeError(w, http.StatusNotFound, "asset not found")
		return
	}

	// Bug fix: Asset has no Content field; fetch from latest version.
	content, err := s.Store.GetLatestAssetContent(assetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get asset content: "+err.Error())
		return
	}

	title := asset.Title
	if req.Title != "" {
		title = req.Title
	}

	mcp, err := s.findLarkMCP()
	if err != nil {
		writeError(w, http.StatusBadRequest, "Lark MCP not found: "+err.Error())
		return
	}

	targetURL, err := mcpTargetURL(mcp.Manifest)
	if err != nil {
		writeError(w, http.StatusBadGateway, "invalid Lark MCP manifest: "+err.Error())
		return
	}

	authHeader, err := s.mcpAuthHeader(mcp)
	if err != nil {
		writeError(w, http.StatusBadGateway, "auth error: "+err.Error())
		return
	}
	mcpBase := targetURL.String()

	raw, err := s.larkCallMCPTool(mcp, mcpBase, authHeader, "lark_drive_import_document",
		map[string]any{"file_name": title + ".md", "content": content})
	if err != nil {
		writeError(w, http.StatusBadGateway, "tools/call failed: "+err.Error())
		return
	}

	docURL := extractLarkDocURLFromMcpResponse(raw)
	_ = s.Store.SetAssetLarkDoc(assetID, docURL)

	writeJSON(w, http.StatusOK, map[string]string{"url": docURL})
}

// extractLarkDoc extracts the title and body from the plain markdown returned by
// lark_docs_get_content. Scans for the first "# Heading" line (skipping any
// preamble such as "文档内容如下") and uses it as the title.
var markdownEscapeRe = regexp.MustCompile(`\\(.)`)

func extractLarkDoc(raw string) (title, content string) {
	lines := strings.Split(strings.TrimLeft(raw, "\n\r "), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			title = markdownEscapeRe.ReplaceAllString(title, "$1")
			title = html.UnescapeString(title)
			content = strings.TrimLeft(strings.Join(lines[i+1:], "\n"), "\n\r")
			content = markdownEscapeRe.ReplaceAllString(content, "$1")
			content = html.UnescapeString(content)
			return title, content
		}
	}
	return "", raw
}

// extractLarkDocURL pulls the published document URL from the importDocument response.
// Tries JSON field names first, then scans for an https:// URL in plain text, falls back to raw string.
func extractLarkDocURLFromMcpResponse(raw string) string {
	// Try JSON envelope first.
	if idx := strings.Index(raw, "{"); idx >= 0 {
		var envelope struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw[idx:]), &envelope); err == nil {
			for _, key := range []string{"url", "documentUrl", "document_url", "webUrl", "web_url", "objUrl", "obj_url"} {
				if v, ok := envelope.Data[key]; ok {
					var s string
					if json.Unmarshal(v, &s) == nil && s != "" {
						return s
					}
				}
			}
		}
	}
	// Fallback: extract the first https:// URL from plain text.
	if i := strings.Index(raw, "https://"); i >= 0 {
		u := raw[i:]
		if end := strings.IndexAny(u, " \t\n\r"); end >= 0 {
			u = u[:end]
		}
		return u
	}
	return raw
}

// larkDocIDFromURL extracts the document ID from a Lark URL.
// e.g. https://okg-block.sg.larksuite.com/wiki/MfSmwamC7izP7kkXKjilneAfgUg -> MfSmwamC7izP7kkXKjilneAfgUg
func larkDocIDFromURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := len(segments) - 1; i >= 0; i-- {
		if segments[i] != "" {
			return segments[i], nil
		}
	}
	return "", fmt.Errorf("Error larkDocIDFromURL")
}

func (s *Server) findLarkMCP() (*store.InstalledMCP, error) {
	if s.LocalStore == nil {
		return nil, fmt.Errorf("local store not available")
	}
	mcp, err := s.resolveMCPForProxy(defaultMCPName, "")
	if err != nil {
		return nil, fmt.Errorf("Lark MCP %q not found: %w", defaultMCPName, err)
	}
	return mcp, nil
}

// larkCallMCPTool calls tools/call on the Lark MCP and returns the concatenated text content.
// On HTTP 401 it broadcasts a needs_auth event so the frontend can initiate OAuth.
func (s *Server) larkCallMCPTool(mcp *store.InstalledMCP, baseURL, authHeader, toolName string, args map[string]any) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": toolName, "arguments": args},
	})
	resp, err := larkMCPPost(baseURL, authHeader, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		s.broadcastNeedsAuth(mcp.Name, mcp.ProjectID)
		return "", fmt.Errorf("Lark MCP needs authorization — please click Authorize in the notification")
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("MCP returned HTTP %d", resp.StatusCode)
	}

	var out struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Error) > 0 && string(out.Error) != "null" {
		// error may be a string or an object {message: "..."}
		var errStr string
		var errObj struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(out.Error, &errStr) == nil {
			return "", fmt.Errorf("MCP: %s", errStr)
		} else if json.Unmarshal(out.Error, &errObj) == nil {
			return "", fmt.Errorf("MCP: %s", errObj.Message)
		}
		return "", fmt.Errorf("MCP error: %s", out.Error)
	}
	if out.Result.IsError {
		for _, c := range out.Result.Content {
			if c.Type == "text" {
				return "", fmt.Errorf("tool error: %s", c.Text)
			}
		}
		return "", fmt.Errorf("tool returned error")
	}

	var sb strings.Builder
	for _, c := range out.Result.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String(), nil
}

// larkMCPPost sends a JSON-RPC POST to an MCP endpoint.
func larkMCPPost(baseURL, authHeader string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	return larkHTTPClient.Do(req)
}


// PUT /api/assets/:id/lark-doc
// Body: { "url": "..." }
// Persists the Lark document URL on the asset (used by RemoteStore proxy).
func (s *Server) SetLarkDoc(w http.ResponseWriter, r *http.Request) {
	assetID := chi.URLParam(r, "assetId")

	var req struct {
		URL string `json:"lark_doc_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.Store.SetAssetLarkDoc(assetID, req.URL); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
