package api

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/esonic/nexbox/gitlab"
)

// CreateMR creates a GitLab merge request for a project
func (s *Server) CreateMR(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")

	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	gitlabToken := s.Config.Get().GitLabToken
	if gitlabToken == "" {
		writeError(w, http.StatusBadRequest, "GitLab personal access token not configured. Set it in Settings.")
		return
	}

	if project.GitRepoPath == "" {
		writeError(w, http.StatusBadRequest, "project has no git repo path configured")
		return
	}

	title, err := gitlab.GetLastCommitMessage(project.GitRepoPath)
	if err != nil || title == "" {
		title = project.Name + " update"
	}

	mr, err := gitlab.CreateMRForProject(r.Context(), project.GitRepoPath, title, "", gitlabToken)
	if err != nil {
		log.Printf("[gitlab] CreateMR failed: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"iid":     mr.IID,
		"web_url": mr.WebURL,
		"title":   mr.Title,
		"state":   mr.State,
	})
}

// extractGitLabURL extracts the origin remote URL from a local git repo
// and normalizes it to a canonical HTTPS form for deduplication:
//   git@gitlab.example.com:group/project.git → https://gitlab.example.com/group/project
//   https://gitlab.example.com/GROUP/Project.git → https://gitlab.example.com/group/project
func extractGitLabURL(dir string) (string, error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("no git remote 'origin' found: %s", strings.TrimSpace(stderr.String()))
	}

	raw := strings.TrimSpace(stdout.String())
	if raw == "" {
		return "", fmt.Errorf("empty git remote URL")
	}

	return normalizeGitLabURL(raw)
}

// gitlabNamespaceFromURL returns the first path segment of a normalized GitLab URL.
// e.g. "https://gitlab.okg.com/sre/sre-ai/foo" → "sre"
// Returns "" if the URL has no path segments.
func gitlabNamespaceFromURL(gitlabURL string) string {
	// URL is already normalized: https://host/seg1/seg2/...
	idx := strings.Index(gitlabURL, "://")
	if idx < 0 {
		return ""
	}
	afterScheme := gitlabURL[idx+3:]
	slashIdx := strings.Index(afterScheme, "/")
	if slashIdx < 0 {
		return ""
	}
	path := afterScheme[slashIdx+1:]
	parts := strings.SplitN(path, "/", 2)
	return parts[0]
}

// normalizeGitLabURL converts any git remote URL to a canonical HTTPS form.
func normalizeGitLabURL(raw string) (string, error) {
	var host, path string

	switch {
	case strings.HasPrefix(raw, "git@"):
		// git@gitlab.example.com:group/project.git
		colonIdx := strings.Index(raw, ":")
		if colonIdx < 0 {
			return "", fmt.Errorf("invalid SSH remote URL: %s", raw)
		}
		host = raw[4:colonIdx] // between "git@" and ":"
		path = raw[colonIdx+1:]

	case strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://"):
		schemeEnd := strings.Index(raw, "://")
		afterScheme := raw[schemeEnd+3:]
		slashIdx := strings.Index(afterScheme, "/")
		if slashIdx < 0 {
			return "", fmt.Errorf("invalid HTTPS remote URL: %s", raw)
		}
		host = afterScheme[:slashIdx]
		path = afterScheme[slashIdx+1:]

	default:
		return "", fmt.Errorf("unrecognized remote URL format: %s", raw)
	}

	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimSuffix(path, "/")

	return "https://" + strings.ToLower(host) + "/" + strings.ToLower(path), nil
}
