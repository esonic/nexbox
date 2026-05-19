package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"

	"gitlab.okg.com/sre_ai/nexbox/gitlab"
)

// parseMRURL extracts the GitLab host, project path, and MR IID from a URL like:
// https://gitlab.example.com/group/project/-/merge_requests/123
func parseMRURL(mrURL string) (host, projectPath string, mrIID string, err error) {
	u, err := url.Parse(mrURL)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid URL: %w", err)
	}
	re := regexp.MustCompile(`^/(.+?)/-/merge_requests/(\d+)`)
	m := re.FindStringSubmatch(u.Path)
	if m == nil {
		return "", "", "", fmt.Errorf("URL does not match GitLab MR pattern: %s", mrURL)
	}
	return u.Scheme + "://" + u.Host, m[1], m[2], nil
}

type gitlabNote struct {
	ID        int    `json:"id"`
	Body      string `json:"body"`
	Author    struct {
		Name     string `json:"name"`
		Username string `json:"username"`
	} `json:"author"`
	CreatedAt string `json:"created_at"`
	System    bool   `json:"system"`
	Position  *struct {
		NewPath string `json:"new_path"`
		OldPath string `json:"old_path"`
		NewLine *int   `json:"new_line"`
		OldLine *int   `json:"old_line"`
	} `json:"position"`
}

var ignoredAuthors = map[string]bool{
	"cicd.dev@okcoin.com": true,
}

type mrComment struct {
	Author    string `json:"author"`
	Username  string `json:"username"`
	File      string `json:"file,omitempty"`
	Line      *int   `json:"line,omitempty"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

func fetchMRComments(gitlabHost, projectPath, mrIID, token string) (string, error) {
	encodedProject := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/notes?per_page=100&sort=asc",
		gitlabHost, encodedProject, mrIID)

	client := gitlab.NewHTTPClient()
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GitLab API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GitLab API returned %d: %s", resp.StatusCode, string(body))
	}

	var notes []gitlabNote
	if err := json.NewDecoder(resp.Body).Decode(&notes); err != nil {
		return "", fmt.Errorf("failed to decode GitLab response: %w", err)
	}

	var comments []mrComment
	for _, n := range notes {
		isBotAuthor := ignoredAuthors[n.Author.Username] || ignoredAuthors[n.Author.Name]
		hasPosition := n.Position != nil && (n.Position.NewPath != "" || n.Position.OldPath != "")
		if n.System || (isBotAuthor && !hasPosition) {
			continue
		}

		c := mrComment{
			Author:    n.Author.Name,
			Username:  n.Author.Username,
			Body:      n.Body,
			CreatedAt: n.CreatedAt,
		}
		if n.Position != nil {
			c.File = n.Position.NewPath
			if c.File == "" {
				c.File = n.Position.OldPath
			}
			if n.Position.NewLine != nil {
				c.Line = n.Position.NewLine
			} else {
				c.Line = n.Position.OldLine
			}
		}
		comments = append(comments, c)
	}

	if len(comments) == 0 {
		return `{"comments":[],"message":"No review comments found on this MR."}`, nil
	}

	result, err := json.Marshal(map[string]interface{}{
		"mr_iid":   mrIID,
		"total":    len(comments),
		"comments": comments,
	})
	if err != nil {
		return "", fmt.Errorf("failed to marshal result: %w", err)
	}

	log.Printf("[mcp] get_gitlab_mr_comments: found %d comments", len(comments))
	return string(result), nil
}
