package gitlab

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// ParseProjectPath extracts the GitLab namespace/project path from the git remote URL.
// Supports SSH (git@host:group/project.git) and HTTPS formats.
func ParseProjectPath(dir string) (string, error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git remote get-url origin: %s", stderr.String())
	}
	remoteURL := strings.TrimSpace(stdout.String())
	if remoteURL == "" {
		return "", fmt.Errorf("empty git remote URL")
	}
	if strings.HasPrefix(remoteURL, "git@") {
		parts := strings.SplitN(remoteURL, ":", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid SSH remote URL: %s", remoteURL)
		}
		return strings.TrimSuffix(parts[1], ".git"), nil
	}
	if strings.HasPrefix(remoteURL, "http://") || strings.HasPrefix(remoteURL, "https://") {
		idx := strings.Index(remoteURL, "://")
		afterScheme := remoteURL[idx+3:]
		slashIdx := strings.Index(afterScheme, "/")
		if slashIdx < 0 {
			return "", fmt.Errorf("invalid HTTPS remote URL: %s", remoteURL)
		}
		return strings.TrimSuffix(afterScheme[slashIdx+1:], ".git"), nil
	}
	return "", fmt.Errorf("unrecognized remote URL format: %s", remoteURL)
}

// GetCurrentBranch returns the name of the currently checked-out branch.
func GetCurrentBranch(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s", stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Push runs `git push -u origin <branch>` in the given directory.
func Push(dir, branch string) error {
	cmd := exec.Command("git", "push", "-u", "origin", branch)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	log.Printf("[gitlab] Pushing branch %s to origin...", branch)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr.String()))
	}
	log.Printf("[gitlab] Push completed")
	return nil
}

// GetLastCommitMessage returns the subject line of the most recent commit.
func GetLastCommitMessage(dir string) (string, error) {
	cmd := exec.Command("git", "log", "-1", "--pretty=%s")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s", stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// CreateMRForProject pushes the current branch and opens a GitLab MR against the default branch.
func CreateMRForProject(ctx context.Context, workDir, title, desc, gitlabToken string) (*MRResponse, error) {
	projectPath, err := ParseProjectPath(workDir)
	if err != nil {
		return nil, fmt.Errorf("failed to parse GitLab project from git remote: %w", err)
	}
	branch, err := GetCurrentBranch(workDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get current branch: %w", err)
	}
	if err := Push(workDir, branch); err != nil {
		return nil, fmt.Errorf("git push failed: %w", err)
	}
	client := NewClient(gitlabToken)
	return client.CreateMR(ctx, projectPath, MRRequest{
		SourceBranch: branch,
		Title:        title,
		Description:  desc,
	})
}
