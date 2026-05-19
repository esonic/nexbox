package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// GitLab Issue types (REST API v4 response)
// ---------------------------------------------------------------------------

type Issue struct {
	ID          int        `json:"id"`
	IID         int        `json:"iid"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	State       string     `json:"state"` // "opened" or "closed"
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ClosedAt    *time.Time `json:"closed_at"`
	DueDate     string     `json:"due_date,omitempty"` // "YYYY-MM-DD"
	WebURL      string     `json:"web_url"`
	Author      struct {
		Username string `json:"username"`
	} `json:"author"`
	Assignee *struct {
		Username string `json:"username"`
	} `json:"assignee"`
	Labels []string `json:"labels"`
	Weight int      `json:"weight"`
}

type IssueComment struct {
	ID     int    `json:"id"`
	Body   string `json:"body"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
	System    bool      `json:"system"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// ID helpers — GitLab task IDs: "gl:<projectID>:<iid>"
// ---------------------------------------------------------------------------

func MakeTaskID(projectID string, iid int) string {
	return fmt.Sprintf("gl:%s:%d", projectID, iid)
}

func IsGitLabTaskID(id string) bool {
	return strings.HasPrefix(id, "gl:")
}

func ParseTaskID(id string) (projectID string, iid int, err error) {
	if !IsGitLabTaskID(id) {
		return "", 0, fmt.Errorf("not a gitlab task id: %s", id)
	}
	rest := id[3:] // strip "gl:"
	lastColon := strings.LastIndex(rest, ":")
	if lastColon < 0 {
		return "", 0, fmt.Errorf("invalid gitlab task id: %s", id)
	}
	projectID = rest[:lastColon]
	iid, err = strconv.Atoi(rest[lastColon+1:])
	if err != nil {
		return "", 0, fmt.Errorf("invalid iid in gitlab task id %s: %w", id, err)
	}
	return projectID, iid, nil
}

// ---------------------------------------------------------------------------
// Project path extraction
// ---------------------------------------------------------------------------

func ProjectPathFromURL(gitlabURL string) (string, error) {
	if gitlabURL == "" {
		return "", fmt.Errorf("empty gitlab url")
	}
	if strings.HasPrefix(gitlabURL, "git@") {
		parts := strings.SplitN(gitlabURL, ":", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid SSH URL: %s", gitlabURL)
		}
		return strings.TrimSuffix(parts[1], ".git"), nil
	}
	_, afterScheme, found := strings.Cut(gitlabURL, "://")
	if !found {
		return "", fmt.Errorf("invalid URL: %s", gitlabURL)
	}
	_, path, found := strings.Cut(afterScheme, "/")
	if !found {
		return "", fmt.Errorf("no path in URL: %s", gitlabURL)
	}
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimSuffix(path, "/")
	return path, nil
}

// ---------------------------------------------------------------------------
// Status & Priority mapping
// ---------------------------------------------------------------------------

func MapGitLabStatus(state string) string {
	switch state {
	case "opened":
		return "open"
	case "closed":
		return "closed"
	default:
		return "open"
	}
}

func MapStatusToGitLab(status string) string {
	switch status {
	case "closed":
		return "close"
	case "open":
		return "reopen"
	default:
		return ""
	}
}

// PriorityFromWeight maps a GitLab issue weight to a priority string.
// w<=10: low, 10<w<=20: medium, 20<w<30: high, 30<=w: critical.
// If weight is 0 (not set), returns "medium".
func PriorityFromWeight(weight int) string {
	if weight <= 0 {
		return "medium"
	}
	switch {
	case weight <= 10:
		return "low"
	case weight <= 20:
		return "medium"
	case weight < 30:
		return "high"
	default:
		return "critical"
	}
}

// WeightFromPriority maps a priority string to a GitLab weight value.
func WeightFromPriority(priority string) int {
	switch priority {
	case "low":
		return 5
	case "medium":
		return 15
	case "high":
		return 25
	case "critical":
		return 35
	default:
		return 15
	}
}


// ---------------------------------------------------------------------------
// Project labels
// ---------------------------------------------------------------------------

// GitLabLabel represents a label from the GitLab API.
type GitLabLabel struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// ListProjectLabels fetches all labels from a GitLab project.
func (c *Client) ListProjectLabels(ctx context.Context, projectPath string) ([]GitLabLabel, error) {
	encoded := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/labels?per_page=100", baseURL, encoded)
	body, err := c.apiRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	var labels []GitLabLabel
	if err := json.Unmarshal(body, &labels); err != nil {
		return nil, fmt.Errorf("decode labels: %w", err)
	}
	return labels, nil
}

// CreateProjectLabel creates a new label in a GitLab project.
func (c *Client) CreateProjectLabel(ctx context.Context, projectPath, name, color string) (*GitLabLabel, error) {
	encoded := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/labels", baseURL, encoded)
	payload := map[string]any{"name": name}
	if color != "" {
		payload["color"] = color
	} else {
		payload["color"] = "#428BCA" // default blue
	}
	body, err := c.apiRequest(ctx, "POST", apiURL, payload)
	if err != nil {
		return nil, fmt.Errorf("create label: %w", err)
	}
	var label GitLabLabel
	if err := json.Unmarshal(body, &label); err != nil {
		return nil, fmt.Errorf("decode label: %w", err)
	}
	return &label, nil
}

// DeleteProjectLabel deletes a label from a GitLab project by label ID.
func (c *Client) DeleteProjectLabel(ctx context.Context, projectPath string, labelID int) error {
	encoded := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/labels/%d", baseURL, encoded, labelID)
	_, err := c.apiRequest(ctx, "DELETE", apiURL, nil)
	if err != nil {
		return fmt.Errorf("delete label: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// User lookup
// ---------------------------------------------------------------------------

// GetUserIDByUsername returns the GitLab user ID for a given username.
// Returns 0 if the user is not found.
func (c *Client) GetUserIDByUsername(ctx context.Context, username string) (int, error) {
	apiURL := fmt.Sprintf("%s/api/v4/users?username=%s", baseURL, url.QueryEscape(username))
	body, err := c.apiRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return 0, fmt.Errorf("lookup user: %w", err)
	}
	var users []struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &users); err != nil {
		return 0, fmt.Errorf("decode users: %w", err)
	}
	if len(users) == 0 {
		return 0, nil
	}
	return users[0].ID, nil
}

// ---------------------------------------------------------------------------
// Client methods for Issues
// ---------------------------------------------------------------------------

func (c *Client) ListIssues(ctx context.Context, projectPath string, limit, offset int, search string) ([]Issue, error) {
	encoded := url.PathEscape(projectPath)
	page := (offset / max(limit, 1)) + 1
	if limit <= 0 {
		limit = 30
	}

	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/issues?per_page=%d&page=%d&order_by=updated_at&sort=desc&state=all",
		baseURL, encoded, limit, page)
	if search != "" {
		apiURL += "&search=" + url.QueryEscape(search)
	}

	body, err := c.apiRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}

	var issues []Issue
	if err := json.Unmarshal(body, &issues); err != nil {
		return nil, fmt.Errorf("decode issues: %w", err)
	}
	return issues, nil
}

func (c *Client) GetIssue(ctx context.Context, projectPath string, iid int) (*Issue, error) {
	encoded := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/issues/%d", baseURL, encoded, iid)

	body, err := c.apiRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("get issue: %w", err)
	}

	var issue Issue
	if err := json.Unmarshal(body, &issue); err != nil {
		return nil, fmt.Errorf("decode issue: %w", err)
	}
	return &issue, nil
}

func (c *Client) CreateIssue(ctx context.Context, projectPath string, title, description, assignee, priority string, labels []string, dueDate string) (*Issue, error) {
	encoded := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/issues", baseURL, encoded)

	payload := map[string]any{
		"title": title,
	}
	if description != "" {
		payload["description"] = description
	}
	if len(labels) > 0 {
		payload["labels"] = strings.Join(labels, ",")
	}
	if priority != "" {
		payload["weight"] = WeightFromPriority(priority)
	}
	if dueDate != "" {
		payload["due_date"] = dueDate
	}
	if assignee != "" {
		if uid, err := c.GetUserIDByUsername(ctx, assignee); err == nil && uid > 0 {
			payload["assignee_ids"] = []int{uid}
		}
	}

	body, err := c.apiRequest(ctx, "POST", apiURL, payload)
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}

	var issue Issue
	if err := json.Unmarshal(body, &issue); err != nil {
		return nil, fmt.Errorf("decode issue: %w", err)
	}
	return &issue, nil
}

func (c *Client) UpdateIssue(ctx context.Context, projectPath string, iid int, updates map[string]any) (*Issue, error) {
	encoded := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/issues/%d", baseURL, encoded, iid)

	body, err := c.apiRequest(ctx, "PUT", apiURL, updates)
	if err != nil {
		return nil, fmt.Errorf("update issue: %w", err)
	}

	var issue Issue
	if err := json.Unmarshal(body, &issue); err != nil {
		return nil, fmt.Errorf("decode issue: %w", err)
	}
	return &issue, nil
}

func (c *Client) DeleteIssue(ctx context.Context, projectPath string, iid int) error {
	encoded := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/issues/%d", baseURL, encoded, iid)

	_, err := c.apiRequest(ctx, "DELETE", apiURL, nil)
	if err != nil {
		return fmt.Errorf("delete issue: %w", err)
	}
	return nil
}

func (c *Client) ListIssueComments(ctx context.Context, projectPath string, iid int) ([]IssueComment, error) {
	encoded := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/issues/%d/notes?sort=asc&per_page=100", baseURL, encoded, iid)

	body, err := c.apiRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("list issue comments: %w", err)
	}

	var comments []IssueComment
	if err := json.Unmarshal(body, &comments); err != nil {
		return nil, fmt.Errorf("decode comments: %w", err)
	}

	filtered := make([]IssueComment, 0, len(comments))
	for _, c := range comments {
		if !c.System {
			filtered = append(filtered, c)
		}
	}
	return filtered, nil
}

func (c *Client) CreateIssueComment(ctx context.Context, projectPath string, iid int, body string) (*IssueComment, error) {
	encoded := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/issues/%d/notes", baseURL, encoded, iid)

	respBody, err := c.apiRequest(ctx, "POST", apiURL, map[string]any{
		"body": body,
	})
	if err != nil {
		return nil, fmt.Errorf("create issue comment: %w", err)
	}

	var comment IssueComment
	if err := json.Unmarshal(respBody, &comment); err != nil {
		return nil, fmt.Errorf("decode comment: %w", err)
	}
	return &comment, nil
}
