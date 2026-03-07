package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

const prismAdminDomain = "https://sre-admin.okg.com"

type reviewRuleGroup struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type reviewRule struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
	Language    string `json:"language"`
	Method      string `json:"method"`
	PositiveEg  string `json:"positiveEg"`
	NegativeEg  string `json:"negativeEg"`
}

func (s *Server) handleGetCodeReviewRuleGroups(ctx context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", prismAdminDomain+"/rca/api/v1/review/rule-groups", nil)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to create request: %v", err)), nil
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return errorResult(fmt.Sprintf("request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return errorResult(fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body))), nil
	}

	var result struct {
		Data []reviewRuleGroup `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return errorResult(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	out, _ := json.Marshal(result.Data)
	log.Printf("[mcp] get_code_review_rule_groups: returned %d groups", len(result.Data))
	return textResult(string(out)), nil
}

func (s *Server) handleGetCodeReviewRules(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()
	groupIDsStr, _ := args["group_ids"].(string)
	if groupIDsStr == "" {
		return errorResult("group_ids is required"), nil
	}

	params := url.Values{}
	for _, p := range strings.Split(groupIDsStr, ",") {
		p = strings.TrimSpace(p)
		if _, err := strconv.Atoi(p); err != nil {
			return errorResult(fmt.Sprintf("invalid group_id %q: must be a number", p)), nil
		}
		params.Add("group_id", p)
	}

	apiURL := prismAdminDomain + "/rca/api/v1/review/rules?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to create request: %v", err)), nil
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return errorResult(fmt.Sprintf("request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return errorResult(fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body))), nil
	}

	var result struct {
		Data []reviewRule `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return errorResult(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	out, _ := json.Marshal(result.Data)
	log.Printf("[mcp] get_code_review_rules: groups=%s returned %d rules", groupIDsStr, len(result.Data))
	return textResult(string(out)), nil
}
