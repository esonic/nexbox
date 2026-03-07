package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const baseURL = "https://gitlab.okg.com"

// Client handles GitLab API interactions
type Client struct {
	accessToken string
	httpClient  *http.Client
}

// MRRequest contains parameters for creating a merge request
type MRRequest struct {
	SourceBranch string `json:"source_branch"`
	Title        string `json:"title"`
	Description  string `json:"description,omitempty"`
}

// MRResponse represents the GitLab MR API response (subset of fields)
type MRResponse struct {
	IID     int    `json:"iid"`
	WebURL  string `json:"web_url"`
	Title   string `json:"title"`
	State   string `json:"state"`
	MergeID int    `json:"id"`
}

// ProjectInfo represents basic project info from GitLab API
type ProjectInfo struct {
	DefaultBranch string `json:"default_branch"`
}

// NewHTTPClient returns an *http.Client that respects NEXBOX_GITLAB_PROXY
// (format: host:port@user:pass). Shared by both the GitLab Client and MCP tools.
func NewHTTPClient() *http.Client {
	transport := &http.Transport{}

	if proxyEnv := os.Getenv("NEX_GITLAB_PROXY"); proxyEnv != "" {
		var proxyAddr, proxyUser, proxyPass string

		if idx := strings.Index(proxyEnv, "@"); idx >= 0 {
			proxyAddr = proxyEnv[:idx]
			creds := proxyEnv[idx+1:]
			if parts := strings.SplitN(creds, ":", 2); len(parts) == 2 {
				proxyUser = parts[0]
				proxyPass = parts[1]
			}
		} else {
			proxyAddr = proxyEnv
		}

		proxyURL := &url.URL{
			Scheme: "http",
			Host:   proxyAddr,
		}
		if proxyUser != "" {
			proxyURL.User = url.UserPassword(proxyUser, proxyPass)
		}

		transport.Proxy = http.ProxyURL(proxyURL)
		log.Printf("[gitlab] Using proxy: %s (auth: %v)", proxyAddr, proxyUser != "")
	}

	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}
}

// NewClient creates a new GitLab client.
func NewClient(accessToken string) *Client {
	return &Client{
		accessToken: accessToken,
		httpClient:  NewHTTPClient(),
	}
}

// CreateMR creates a merge request on GitLab
func (c *Client) CreateMR(ctx context.Context, projectPath string, req MRRequest) (*MRResponse, error) {
	encodedProject := url.PathEscape(projectPath)

	// Get project default branch for target
	defaultBranch, err := c.getDefaultBranch(ctx, encodedProject)
	if err != nil {
		log.Printf("[gitlab] Failed to get default branch, falling back to main: %v", err)
		defaultBranch = "main"
	}

	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests", baseURL, encodedProject)

	payload := map[string]interface{}{
		"source_branch": req.SourceBranch,
		"target_branch": defaultBranch,
		"title":         req.Title,
	}
	if req.Description != "" {
		payload["description"] = req.Description
	}

	respBody, err := c.apiRequest(ctx, "POST", apiURL, payload)
	if err != nil {
		return nil, fmt.Errorf("create MR: %w", err)
	}

	var mr MRResponse
	if err := json.Unmarshal(respBody, &mr); err != nil {
		return nil, fmt.Errorf("decode MR response: %w", err)
	}

	log.Printf("[gitlab] Created MR !%d: %s", mr.IID, mr.WebURL)
	return &mr, nil
}

// getDefaultBranch fetches the project's default branch from GitLab API
func (c *Client) getDefaultBranch(ctx context.Context, encodedProject string) (string, error) {
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s", baseURL, encodedProject)

	respBody, err := c.apiRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return "", err
	}

	var info ProjectInfo
	if err := json.Unmarshal(respBody, &info); err != nil {
		return "", fmt.Errorf("decode project info: %w", err)
	}

	if info.DefaultBranch == "" {
		return "main", nil
	}
	return info.DefaultBranch, nil
}

// apiRequest makes an HTTP request to GitLab API
func (c *Client) apiRequest(ctx context.Context, method, apiURL string, body interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, apiURL, reqBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("PRIVATE-TOKEN", c.accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	defer resp.Body.Close()

	rspBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitLab API %s %s failed with status %d: %s",
			method, apiURL, resp.StatusCode, string(rspBody))
	}

	return rspBody, nil
}
