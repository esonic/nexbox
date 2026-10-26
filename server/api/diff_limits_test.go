package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileDiffBufferLimit(t *testing.T) {
	var buffer fileDiffBuffer
	if _, err := buffer.Write([]byte(strings.Repeat("a", maxFileDiffBytes))); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte("b")); !errors.Is(err, errFileDiffTooLarge) {
		t.Fatalf("overflow error = %v", err)
	}
	if !buffer.exceeded || buffer.Len() != maxFileDiffBytes {
		t.Fatalf("overflow retained data: exceeded=%v size=%d", buffer.exceeded, buffer.Len())
	}
}

func TestFileDiffPreviewLimits(t *testing.T) {
	for _, mode := range []string{"untracked", "tracked", "staged", "deleted", "full"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			initAPIRepo(t, root)
			large := strings.Repeat("large preview content\n", maxFileDiffBytes/20)
			if mode == "deleted" || mode == "full" {
				commitAPIFile(t, root, "large.txt", large)
			} else if mode != "untracked" {
				commitAPIFile(t, root, "large.txt", "initial\n")
			}
			path := filepath.Join(root, "large.txt")
			if mode == "deleted" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				content := large
				if mode == "full" {
					content += "extra\n"
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "staged" {
				runAPIGit(t, root, "add", "--", "large.txt")
			}
			diff, err := getFileDiff(context.Background(), root, "large.txt", mode == "full", mode == "staged")
			if !errors.Is(err, errFileDiffTooLarge) || diff != "" {
				t.Fatalf("oversized diff: size=%d err=%v", len(diff), err)
			}
		})
	}
}

func TestFileDiffTooLargeResponse(t *testing.T) {
	server := newWorktreeServer(t)
	root := t.TempDir()
	initAPIRepo(t, root)
	project := createAPIProject(t, server, root)
	repositoryID := discoverOnlyAPIRepository(t, server, project.ID)
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(strings.Repeat("a", maxFileDiffBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	response := doRequest(t, server, http.MethodGet, "/api/projects/"+project.ID+"/file-diff?repository_id="+url.QueryEscape(repositoryID)+"&path=large.txt", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		RepositoryID string `json:"repository_id"`
		Diff         string `json:"diff"`
		TooLarge     bool   `json:"too_large"`
		MaxBytes     int    `json:"max_bytes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.RepositoryID != repositoryID || result.Diff != "" || !result.TooLarge || result.MaxBytes != maxFileDiffBytes {
		t.Fatalf("unexpected response: %+v", result)
	}
}
