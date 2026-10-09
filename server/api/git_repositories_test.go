package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/esonic/nexbox/config"
	"github.com/esonic/nexbox/gitrepo"
	"github.com/esonic/nexbox/store"
)

func initAPIRepo(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	runAPIGit(t, root, "init", "-q")
	runAPIGit(t, root, "config", "user.email", "test@example.com")
	runAPIGit(t, root, "config", "user.name", "BeeHub Test")
}

func runAPIGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func commitAPIFile(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runAPIGit(t, root, "add", "--", path)
	runAPIGit(t, root, "commit", "-q", "-m", "initial")
}

func createAPIProject(t *testing.T, server *Server, root string) *store.Project {
	t.Helper()
	project, err := server.Store.CreateProject(store.CreateProjectRequest{Name: "multi-repo", GitRepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	server.GitRepositories = gitrepo.NewResolver(gitrepo.DefaultOptions())
	return project
}

func discoverOnlyAPIRepository(t *testing.T, server *Server, projectID string) string {
	t.Helper()
	response := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-repositories", projectID), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("repository discovery = %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Repositories []struct {
			ID string `json:"id"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Repositories) != 1 {
		t.Fatalf("repository count = %d, want 1: %s", len(body.Repositories), response.Body.String())
	}
	return body.Repositories[0].ID
}

func TestGitRepositoriesAndScopedStatus(t *testing.T) {
	server := newWorktreeServer(t)
	workspace := t.TempDir()
	repoA := filepath.Join(workspace, "repo-a")
	repoB := filepath.Join(workspace, "repo-b")
	initAPIRepo(t, repoA)
	initAPIRepo(t, repoB)
	if err := os.WriteFile(filepath.Join(repoA, "only-a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := createAPIProject(t, server, workspace)

	response := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-repositories", project.ID), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("repositories status = %d: %s", response.Code, response.Body.String())
	}
	var discovery struct {
		DefaultRepositoryID string `json:"default_repository_id"`
		Repositories        []struct {
			ID          string `json:"id"`
			DisplayPath string `json:"display_path"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &discovery); err != nil {
		t.Fatal(err)
	}
	if len(discovery.Repositories) != 2 || discovery.Repositories[0].DisplayPath != "repo-a" || discovery.Repositories[1].DisplayPath != "repo-b" {
		t.Fatalf("unexpected discovery: %+v", discovery)
	}
	if discovery.DefaultRepositoryID != discovery.Repositories[0].ID {
		t.Fatalf("unexpected default repository")
	}

	missingScope := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-status", project.ID), nil)
	if missingScope.Code != http.StatusConflict {
		t.Fatalf("missing repository scope status = %d, want 409: %s", missingScope.Code, missingScope.Body.String())
	}
	unknownDiff := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/diff-stats?repository_id=repo_unknown", project.ID), nil)
	if unknownDiff.Code != http.StatusNotFound || !strings.Contains(unknownDiff.Body.String(), gitrepo.CodeNotFound) {
		t.Fatalf("unknown repository diff = %d: %s", unknownDiff.Code, unknownDiff.Body.String())
	}

	scoped := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-status?repository_id=%s", project.ID, discovery.Repositories[0].ID), nil)
	if scoped.Code != http.StatusOK {
		t.Fatalf("scoped status = %d: %s", scoped.Code, scoped.Body.String())
	}
	var status GitStatusResponse
	if err := json.Unmarshal(scoped.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Untracked) != 1 || status.Untracked[0].Path != "only-a.txt" {
		t.Fatalf("unexpected scoped status: %+v", status)
	}
}

func TestSingleRepositoryHistoryKeepsLegacyUnscopedShape(t *testing.T) {
	server := newWorktreeServer(t)
	repository := t.TempDir()
	initAPIRepo(t, repository)
	commitAPIFile(t, repository, "tracked.txt", "tracked\n")
	project := createAPIProject(t, server, repository)

	logResponse := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-log", project.ID), nil)
	if logResponse.Code != http.StatusOK {
		t.Fatalf("legacy log = %d: %s", logResponse.Code, logResponse.Body.String())
	}
	var commits []GitCommitInfo
	if err := json.Unmarshal(logResponse.Body.Bytes(), &commits); err != nil || len(commits) != 1 {
		t.Fatalf("legacy log shape: commits=%+v error=%v body=%s", commits, err, logResponse.Body.String())
	}

	filesResponse := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-commit-files?hash=%s", project.ID, commits[0].Hash), nil)
	if filesResponse.Code != http.StatusOK {
		t.Fatalf("legacy commit files = %d: %s", filesResponse.Code, filesResponse.Body.String())
	}
	var files []GitCommitFileEntry
	if err := json.Unmarshal(filesResponse.Body.Bytes(), &files); err != nil || len(files) != 1 || files[0].Path != "tracked.txt" {
		t.Fatalf("legacy commit-files shape: files=%+v error=%v body=%s", files, err, filesResponse.Body.String())
	}

	repositoryID := discoverOnlyAPIRepository(t, server, project.ID)
	scopedLog := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-log?repository_id=%s", project.ID, repositoryID), nil)
	var scoped struct {
		RepositoryID string          `json:"repository_id"`
		Commits      []GitCommitInfo `json:"commits"`
	}
	if scopedLog.Code != http.StatusOK || json.Unmarshal(scopedLog.Body.Bytes(), &scoped) != nil || scoped.RepositoryID != repositoryID || len(scoped.Commits) != 1 {
		t.Fatalf("scoped log shape = %d: %+v: %s", scopedLog.Code, scoped, scopedLog.Body.String())
	}
}

func TestGitRepositoryRefreshInvalidatesTopologyCache(t *testing.T) {
	server := newWorktreeServer(t)
	workspace := t.TempDir()
	initAPIRepo(t, filepath.Join(workspace, "first"))
	project := createAPIProject(t, server, workspace)
	listPath := fmt.Sprintf("/api/projects/%s/git-repositories", project.ID)

	readCount := func(path string) int {
		response := doRequest(t, server, http.MethodGet, path, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("repository list = %d: %s", response.Code, response.Body.String())
		}
		var body struct {
			Repositories []json.RawMessage `json:"repositories"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return len(body.Repositories)
	}
	if count := readCount(listPath); count != 1 {
		t.Fatalf("initial repository count = %d", count)
	}
	initAPIRepo(t, filepath.Join(workspace, "second"))
	if count := readCount(listPath); count != 1 {
		t.Fatalf("topology cache unexpectedly rescanned: %d", count)
	}
	if count := readCount(listPath + "?refresh=true"); count != 2 {
		t.Fatalf("refresh repository count = %d, want 2", count)
	}
}

func TestAncestorRepositoryDiffUsesRepositoryRelativePaths(t *testing.T) {
	server := newWorktreeServer(t)
	repositoryRoot := t.TempDir()
	initAPIRepo(t, repositoryRoot)
	commitAPIFile(t, repositoryRoot, "module-a/inside.txt", "inside\n")
	commitAPIFile(t, repositoryRoot, "module-b/outside.txt", "before\n")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "module-b", "outside.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(repositoryRoot, "module-a")
	project := createAPIProject(t, server, workspace)

	list := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-repositories", project.ID), nil)
	var discovery struct {
		Repositories []struct {
			ID       string `json:"id"`
			Relation string `json:"relation"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &discovery); err != nil || len(discovery.Repositories) != 1 {
		t.Fatalf("discovery failed: %v: %s", err, list.Body.String())
	}
	if discovery.Repositories[0].Relation != "ancestor" {
		t.Fatalf("relation = %q, want ancestor", discovery.Repositories[0].Relation)
	}

	diff := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/diff-stats?repository_id=%s", project.ID, discovery.Repositories[0].ID), nil)
	if diff.Code != http.StatusOK {
		t.Fatalf("diff status = %d: %s", diff.Code, diff.Body.String())
	}
	var stats GitDiffResponse
	if err := json.Unmarshal(diff.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if len(stats.Files) != 1 || stats.Files[0].Path != "module-b/outside.txt" {
		t.Fatalf("unexpected ancestor diff: %+v", stats.Files)
	}
}

func TestAncestorRepositoryCanReadAndSaveOutsideProjectDirectory(t *testing.T) {
	server := newWorktreeServer(t)
	manager, err := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(func(cfg *config.Config) { cfg.EnableFileEdit = true }); err != nil {
		t.Fatal(err)
	}
	server.Config = manager
	repositoryRoot := t.TempDir()
	initAPIRepo(t, repositoryRoot)
	commitAPIFile(t, repositoryRoot, "module-b/outside.txt", "before\n")
	workspace := filepath.Join(repositoryRoot, "module-a")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	project := createAPIProject(t, server, workspace)
	repositoryID := discoverOnlyAPIRepository(t, server, project.ID)

	read := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/file-content?repository_id=%s&path=module-b%%2Foutside.txt", project.ID, repositoryID), nil)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), "before\\n") {
		t.Fatalf("ancestor file read = %d: %s", read.Code, read.Body.String())
	}
	save := doRequest(t, server, http.MethodPut, fmt.Sprintf("/api/projects/%s/file-content?repository_id=%s&path=module-b%%2Foutside.txt", project.ID, repositoryID), map[string]string{"content": "after\n"})
	if save.Code != http.StatusOK {
		t.Fatalf("ancestor file save = %d: %s", save.Code, save.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(repositoryRoot, "module-b", "outside.txt"))
	if err != nil || string(content) != "after\n" {
		t.Fatalf("ancestor file content = %q, %v", content, err)
	}
}

func TestSymlinkRepositoryScopedFileRead(t *testing.T) {
	server := newWorktreeServer(t)
	manager, err := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(func(cfg *config.Config) { cfg.EnableFileEdit = true }); err != nil {
		t.Fatal(err)
	}
	server.Config = manager
	workspace := t.TempDir()
	target := filepath.Join(t.TempDir(), "shared")
	initAPIRepo(t, target)
	commitAPIFile(t, target, "src/shared.txt", "shared content\n")
	if err := os.Symlink(target, filepath.Join(workspace, "linked")); err != nil {
		t.Fatal(err)
	}
	project := createAPIProject(t, server, workspace)

	list := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-repositories", project.ID), nil)
	var discovery struct {
		Repositories []struct {
			ID       string `json:"id"`
			Relation string `json:"relation"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &discovery); err != nil || len(discovery.Repositories) != 1 {
		t.Fatalf("discovery failed: %v: %s", err, list.Body.String())
	}
	if discovery.Repositories[0].Relation != "symlink" {
		t.Fatalf("relation = %q, want symlink", discovery.Repositories[0].Relation)
	}

	file := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/file-content?repository_id=%s&path=src%%2Fshared.txt", project.ID, discovery.Repositories[0].ID), nil)
	if file.Code != http.StatusOK || !strings.Contains(file.Body.String(), "shared content") {
		t.Fatalf("file read = %d: %s", file.Code, file.Body.String())
	}
	save := doRequest(t, server, http.MethodPut, fmt.Sprintf("/api/projects/%s/file-content?repository_id=%s&path=src%%2Fshared.txt", project.ID, discovery.Repositories[0].ID), map[string]string{"content": "updated through repository scope\n"})
	if save.Code != http.StatusOK {
		t.Fatalf("file save = %d: %s", save.Code, save.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(target, "src", "shared.txt"))
	if err != nil || string(content) != "updated through repository scope\n" {
		t.Fatalf("symlink target content = %q, %v", content, err)
	}
}

func TestOuterAndSymlinkRepositoriesKeepChangesIsolated(t *testing.T) {
	server := newWorktreeServer(t)
	workspace := t.TempDir()
	shared := filepath.Join(t.TempDir(), "shared")
	initAPIRepo(t, workspace)
	initAPIRepo(t, shared)
	commitAPIFile(t, shared, "src/shared.txt", "before\n")
	link := filepath.Join(workspace, "linked-sdk")
	if err := os.Symlink(shared, link); err != nil {
		t.Fatal(err)
	}
	runAPIGit(t, workspace, "add", "--", "linked-sdk")
	runAPIGit(t, workspace, "commit", "-q", "-m", "track repository link")
	if err := os.WriteFile(filepath.Join(shared, "src", "shared.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := createAPIProject(t, server, workspace)

	list := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-repositories", project.ID), nil)
	if list.Code != http.StatusOK {
		t.Fatalf("repository list = %d: %s", list.Code, list.Body.String())
	}
	var discovery struct {
		Repositories []struct {
			ID          string `json:"id"`
			DisplayPath string `json:"display_path"`
			Relation    string `json:"relation"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &discovery); err != nil {
		t.Fatal(err)
	}
	if len(discovery.Repositories) != 2 {
		t.Fatalf("repositories = %+v", discovery.Repositories)
	}
	var outerID, sharedID string
	for _, repository := range discovery.Repositories {
		switch repository.Relation {
		case "workspace":
			outerID = repository.ID
		case "symlink":
			sharedID = repository.ID
			if repository.DisplayPath != "linked-sdk" {
				t.Fatalf("symlink display path = %q", repository.DisplayPath)
			}
		}
	}
	if outerID == "" || sharedID == "" {
		t.Fatalf("missing outer or symlink repository: %+v", discovery.Repositories)
	}

	readStats := func(repositoryID string) GitDiffResponse {
		t.Helper()
		response := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/diff-stats?repository_id=%s", project.ID, repositoryID), nil)
		if response.Code != http.StatusOK {
			t.Fatalf("diff stats = %d: %s", response.Code, response.Body.String())
		}
		var body GitDiffResponse
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	if outer := readStats(outerID); len(outer.Files) != 0 {
		t.Fatalf("outer repository leaked linked target changes: %+v", outer.Files)
	}
	linked := readStats(sharedID)
	if len(linked.Files) != 1 || linked.Files[0].Path != "src/shared.txt" || linked.Files[0].WorkspacePath != "linked-sdk/src/shared.txt" {
		t.Fatalf("symlink repository diff mismatch: %+v", linked.Files)
	}

	aggregate := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-workspace-status?include_files=true", project.ID), nil)
	if aggregate.Code != http.StatusOK || !strings.Contains(aggregate.Body.String(), `"workspace_path":"linked-sdk/src/shared.txt"`) {
		t.Fatalf("aggregate workspace mapping = %d: %s", aggregate.Code, aggregate.Body.String())
	}
}

func TestOuterRepositoryAndSymlinkedRepositorySubdirectoryAreBothDiscovered(t *testing.T) {
	server := newWorktreeServer(t)
	workspace := t.TempDir()
	initAPIRepo(t, workspace)
	commitAPIFile(t, workspace, "outer.txt", "outer repository\n")

	external := filepath.Join(t.TempDir(), "odin-skills")
	initAPIRepo(t, external)
	commitAPIFile(t, external, "skills/tool/SKILL.md", "linked repository content\n")
	if err := os.Symlink(filepath.Join(external, "skills"), filepath.Join(workspace, "skills")); err != nil {
		t.Fatal(err)
	}
	project := createAPIProject(t, server, workspace)

	response := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-repositories", project.ID), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("repository discovery = %d: %s", response.Code, response.Body.String())
	}
	var discovery struct {
		Repositories []struct {
			Name        string           `json:"name"`
			DisplayPath string           `json:"display_path"`
			Relation    gitrepo.Relation `json:"relation"`
		} `json:"repositories"`
		Truncated bool     `json:"truncated"`
		Warnings  []string `json:"warnings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &discovery); err != nil {
		t.Fatal(err)
	}
	if len(discovery.Repositories) != 2 {
		t.Fatalf("expected outer and linked repositories: %+v", discovery.Repositories)
	}
	foundLinked := false
	for _, repository := range discovery.Repositories {
		if repository.Name == "odin-skills" && repository.DisplayPath == "skills" && repository.Relation == gitrepo.RelationSymlink {
			foundLinked = true
		}
	}
	if !foundLinked || discovery.Truncated || len(discovery.Warnings) != 0 {
		t.Fatalf("unexpected linked repository discovery: %+v", discovery)
	}

	head := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-file-head?path=skills%%2Ftool%%2FSKILL.md", project.ID), nil)
	if head.Code != http.StatusOK || !strings.Contains(head.Body.String(), "linked repository content") {
		t.Fatalf("linked subdirectory file owner = %d: %s", head.Code, head.Body.String())
	}
}

func TestSelectedRepositoryMutationIsIsolatedAndLiteral(t *testing.T) {
	server := newWorktreeServer(t)
	workspace := t.TempDir()
	repoA := filepath.Join(workspace, "repo-a")
	repoB := filepath.Join(workspace, "repo-b")
	initAPIRepo(t, repoA)
	initAPIRepo(t, repoB)
	if err := os.WriteFile(filepath.Join(repoA, "only-a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	specialNames := []string{"-leading.txt", ":pathspec.txt", "tab\t名称.txt"}
	for _, name := range specialNames {
		if err := os.WriteFile(filepath.Join(repoB, name), []byte("b\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	project := createAPIProject(t, server, workspace)

	list := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-repositories", project.ID), nil)
	var discovery struct {
		Repositories []struct {
			ID          string `json:"id"`
			DisplayPath string `json:"display_path"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &discovery); err != nil {
		t.Fatal(err)
	}
	var repoBID string
	for _, repository := range discovery.Repositories {
		if repository.DisplayPath == "repo-b" {
			repoBID = repository.ID
		}
	}
	if repoBID == "" {
		t.Fatalf("repo-b missing from discovery: %s", list.Body.String())
	}

	stage := doRequest(t, server, http.MethodPost, fmt.Sprintf("/api/projects/%s/git-stage?repository_id=%s", project.ID, repoBID), map[string]interface{}{"file_paths": specialNames})
	if stage.Code != http.StatusNoContent {
		t.Fatalf("stage status = %d: %s", stage.Code, stage.Body.String())
	}
	if output := runAPIGit(t, repoA, "diff", "--cached", "--name-only"); output != "" {
		t.Fatalf("repo-a index was modified: %q", output)
	}
	staged := runAPIGit(t, repoB, "diff", "--cached", "--name-only", "-z")
	for _, name := range specialNames {
		if !strings.Contains(staged, name+"\x00") {
			t.Errorf("literal path %q was not staged; output %q", name, staged)
		}
	}

	statusResponse := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-status?repository_id=%s", project.ID, repoBID), nil)
	var status GitStatusResponse
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.RepositoryID != repoBID {
		t.Fatalf("response repository_id = %q, want %q", status.RepositoryID, repoBID)
	}
}

func TestUntrackedSymlinkDiscardRemovesOnlyLink(t *testing.T) {
	server := newWorktreeServer(t)
	repository := t.TempDir()
	initAPIRepo(t, repository)
	target := filepath.Join(t.TempDir(), "target.txt")
	if err := os.WriteFile(target, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkName := "external-link"
	if err := os.Symlink(target, filepath.Join(repository, linkName)); err != nil {
		t.Fatal(err)
	}
	project := createAPIProject(t, server, repository)
	list := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-repositories", project.ID), nil)
	var discovery struct {
		Repositories []struct {
			ID string `json:"id"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &discovery); err != nil || len(discovery.Repositories) != 1 {
		t.Fatalf("discovery failed: %v: %s", err, list.Body.String())
	}
	discard := doRequest(t, server, http.MethodPost, fmt.Sprintf("/api/projects/%s/git-discard?repository_id=%s", project.ID, discovery.Repositories[0].ID), map[string]string{"file_path": linkName})
	if discard.Code != http.StatusNoContent {
		t.Fatalf("discard status = %d: %s", discard.Code, discard.Body.String())
	}
	if _, err := os.Lstat(filepath.Join(repository, linkName)); !os.IsNotExist(err) {
		t.Fatalf("symlink still exists or unexpected error: %v", err)
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "keep\n" {
		t.Fatalf("symlink target changed: %q, %v", content, err)
	}
}

func TestGitDiscardRestoresIndexAndWorktree(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, repository string)
		path    string
		verify  func(t *testing.T, repository string)
	}{
		{
			name: "staged and unstaged modification",
			prepare: func(t *testing.T, repository string) {
				commitAPIFile(t, repository, "tracked.txt", "before\n")
				if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("staged\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				runAPIGit(t, repository, "add", "--", "tracked.txt")
				if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("worktree\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			path: "tracked.txt",
			verify: func(t *testing.T, repository string) {
				content, err := os.ReadFile(filepath.Join(repository, "tracked.txt"))
				if err != nil || string(content) != "before\n" {
					t.Fatalf("restored content = %q, %v", content, err)
				}
			},
		},
		{
			name: "staged literal-pathspec addition",
			prepare: func(t *testing.T, repository string) {
				commitAPIFile(t, repository, "base.txt", "base\n")
				if err := os.WriteFile(filepath.Join(repository, ":added.txt"), []byte("added\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				runAPIGit(t, repository, "--literal-pathspecs", "add", "--", ":added.txt")
			},
			path: ":added.txt",
			verify: func(t *testing.T, repository string) {
				if _, err := os.Lstat(filepath.Join(repository, ":added.txt")); !os.IsNotExist(err) {
					t.Fatalf("staged addition still exists: %v", err)
				}
			},
		},
		{
			name: "staged rename",
			prepare: func(t *testing.T, repository string) {
				commitAPIFile(t, repository, "old name.txt", "before rename\n")
				if err := os.Rename(filepath.Join(repository, "old name.txt"), filepath.Join(repository, "-new name.txt")); err != nil {
					t.Fatal(err)
				}
				runAPIGit(t, repository, "add", "-A")
			},
			path: "-new name.txt",
			verify: func(t *testing.T, repository string) {
				content, err := os.ReadFile(filepath.Join(repository, "old name.txt"))
				if err != nil || string(content) != "before rename\n" {
					t.Fatalf("original rename path was not restored: %q, %v", content, err)
				}
				if _, err := os.Lstat(filepath.Join(repository, "-new name.txt")); !os.IsNotExist(err) {
					t.Fatalf("renamed destination still exists: %v", err)
				}
			},
		},
		{
			name: "unborn staged addition",
			prepare: func(t *testing.T, repository string) {
				if err := os.WriteFile(filepath.Join(repository, "unborn.txt"), []byte("new\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				runAPIGit(t, repository, "add", "--", "unborn.txt")
			},
			path: "unborn.txt",
			verify: func(t *testing.T, repository string) {
				if _, err := os.Lstat(filepath.Join(repository, "unborn.txt")); !os.IsNotExist(err) {
					t.Fatalf("unborn staged file still exists: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newWorktreeServer(t)
			repository := t.TempDir()
			initAPIRepo(t, repository)
			test.prepare(t, repository)
			project := createAPIProject(t, server, repository)
			repositoryID := discoverOnlyAPIRepository(t, server, project.ID)
			response := doRequest(t, server, http.MethodPost, fmt.Sprintf("/api/projects/%s/git-discard?repository_id=%s", project.ID, repositoryID), map[string]string{"file_path": test.path})
			if response.Code != http.StatusNoContent {
				t.Fatalf("discard = %d: %s", response.Code, response.Body.String())
			}
			test.verify(t, repository)
			if status := runAPIGit(t, repository, "status", "--porcelain=v2", "--untracked-files=all"); status != "" {
				t.Fatalf("repository is not clean after discard: %q", status)
			}
		})
	}
}

func TestGitCommitAndUnstageKeepRenamePathsTogether(t *testing.T) {
	prepare := func(t *testing.T) (*Server, string, *store.Project, string) {
		t.Helper()
		server := newWorktreeServer(t)
		repository := t.TempDir()
		initAPIRepo(t, repository)
		commitAPIFile(t, repository, "old name.txt", "rename me\n")
		if err := os.Rename(filepath.Join(repository, "old name.txt"), filepath.Join(repository, "new name.txt")); err != nil {
			t.Fatal(err)
		}
		runAPIGit(t, repository, "add", "-A")
		project := createAPIProject(t, server, repository)
		return server, repository, project, discoverOnlyAPIRepository(t, server, project.ID)
	}

	t.Run("commit", func(t *testing.T) {
		server, repository, project, repositoryID := prepare(t)
		response := doRequest(t, server, http.MethodPost, fmt.Sprintf("/api/projects/%s/git-commit?repository_id=%s", project.ID, repositoryID), GitCommitRequest{
			FilePaths: []string{"new name.txt"},
			Message:   "rename file",
		})
		if response.Code != http.StatusOK {
			t.Fatalf("commit rename = %d: %s", response.Code, response.Body.String())
		}
		if status := runAPIGit(t, repository, "status", "--porcelain=v2", "--untracked-files=all"); status != "" {
			t.Fatalf("rename commit left changes behind: %q", status)
		}
		tree := runAPIGit(t, repository, "ls-tree", "-r", "--name-only", "HEAD")
		if tree != "new name.txt\n" {
			t.Fatalf("rename commit tree = %q", tree)
		}
	})

	t.Run("unstage", func(t *testing.T) {
		server, repository, project, repositoryID := prepare(t)
		response := doRequest(t, server, http.MethodPost, fmt.Sprintf("/api/projects/%s/git-unstage?repository_id=%s", project.ID, repositoryID), GitStageRequest{
			FilePaths: []string{"new name.txt"},
		})
		if response.Code != http.StatusNoContent {
			t.Fatalf("unstage rename = %d: %s", response.Code, response.Body.String())
		}
		if cached := runAPIGit(t, repository, "diff", "--cached", "--name-only"); cached != "" {
			t.Fatalf("rename was only partially unstaged: %q", cached)
		}
		if _, err := os.Stat(filepath.Join(repository, "new name.txt")); err != nil {
			t.Fatalf("renamed worktree file disappeared: %v", err)
		}
	})
}

func TestRepositoryScopedFileRejectsSymlinkEscape(t *testing.T) {
	server := newWorktreeServer(t)
	repository := t.TempDir()
	initAPIRepo(t, repository)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repository, "escape")); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.Symlink(outsideFile, filepath.Join(repository, "secret-link")); err != nil {
		t.Fatal(err)
	}
	project := createAPIProject(t, server, repository)
	list := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-repositories", project.ID), nil)
	var discovery struct {
		Repositories []struct {
			ID string `json:"id"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &discovery); err != nil || len(discovery.Repositories) != 1 {
		t.Fatalf("discovery failed: %v", err)
	}
	read := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/file-content?repository_id=%s&path=escape%%2Fsecret.txt", project.ID, discovery.Repositories[0].ID), nil)
	if read.Code != http.StatusBadRequest || !strings.Contains(read.Body.String(), gitrepo.CodeInvalidPath) {
		t.Fatalf("escape read = %d: %s", read.Code, read.Body.String())
	}
	readLink := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/file-content?repository_id=%s&path=secret-link", project.ID, discovery.Repositories[0].ID), nil)
	if readLink.Code != http.StatusOK || !strings.Contains(readLink.Body.String(), outsideFile) || strings.Contains(readLink.Body.String(), "secret\\n") {
		t.Fatalf("final symlink read followed its target: %d: %s", readLink.Code, readLink.Body.String())
	}
	manager, err := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(func(cfg *config.Config) { cfg.EnableFileEdit = true }); err != nil {
		t.Fatal(err)
	}
	server.Config = manager
	saveEscape := doRequest(t, server, http.MethodPut, fmt.Sprintf("/api/projects/%s/file-content?repository_id=%s&path=escape%%2Fwritten.txt", project.ID, discovery.Repositories[0].ID), map[string]string{"content": "must not escape"})
	if saveEscape.Code != http.StatusBadRequest || !strings.Contains(saveEscape.Body.String(), gitrepo.CodeInvalidPath) {
		t.Fatalf("escape save = %d: %s", saveEscape.Code, saveEscape.Body.String())
	}
	if _, err := os.Stat(filepath.Join(outside, "written.txt")); !os.IsNotExist(err) {
		t.Fatalf("escape write reached outside directory: %v", err)
	}
	saveNew := doRequest(t, server, http.MethodPut, fmt.Sprintf("/api/projects/%s/file-content?repository_id=%s&path=new%%2Fnested%%2Ffile.txt", project.ID, discovery.Repositories[0].ID), map[string]string{"content": "created safely\n"})
	if saveNew.Code != http.StatusOK {
		t.Fatalf("safe repository save = %d: %s", saveNew.Code, saveNew.Body.String())
	}
	if content, err := os.ReadFile(filepath.Join(repository, "new", "nested", "file.txt")); err != nil || string(content) != "created safely\n" {
		t.Fatalf("safe repository save mismatch: %q, %v", content, err)
	}
}

func TestStaleSymlinkRepositoryMutationReturnsCode(t *testing.T) {
	server := newWorktreeServer(t)
	workspace := t.TempDir()
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	initAPIRepo(t, first)
	initAPIRepo(t, second)
	link := filepath.Join(workspace, "linked")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	project := createAPIProject(t, server, workspace)
	list := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-repositories", project.ID), nil)
	var discovery struct {
		Repositories []struct {
			ID string `json:"id"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &discovery); err != nil || len(discovery.Repositories) != 1 {
		t.Fatalf("discovery failed: %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	stage := doRequest(t, server, http.MethodPost, fmt.Sprintf("/api/projects/%s/git-stage?repository_id=%s", project.ID, discovery.Repositories[0].ID), map[string]interface{}{"file_paths": []string{"new.txt"}})
	if stage.Code != http.StatusConflict || !strings.Contains(stage.Body.String(), gitrepo.CodeStale) {
		t.Fatalf("stale mutation = %d: %s", stage.Code, stage.Body.String())
	}
}

func TestGitRepositoryRejectsSessionFromAnotherProject(t *testing.T) {
	server := newWorktreeServer(t)
	firstRoot := t.TempDir()
	secondRoot := t.TempDir()
	initAPIRepo(t, firstRoot)
	initAPIRepo(t, secondRoot)
	first := createAPIProject(t, server, firstRoot)
	second := createAPIProject(t, server, secondRoot)
	session, err := server.Store.CreateSession(store.CreateSessionRequest{Name: "other", ProjectID: second.ID})
	if err != nil {
		t.Fatal(err)
	}
	response := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-repositories?session_id=%s", first.ID, session.ID), nil)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "does not belong") {
		t.Fatalf("cross-project session = %d: %s", response.Code, response.Body.String())
	}
}

func TestGitRepositoryUsesSessionWorktreeAsWorkspace(t *testing.T) {
	server := newWorktreeServer(t)
	repository := t.TempDir()
	initAPIRepo(t, repository)
	commitAPIFile(t, repository, "tracked.txt", "local\n")
	project := createAPIProject(t, server, repository)
	session, err := server.Store.CreateSession(store.CreateSessionRequest{Name: "worktree", ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "session-worktree")
	runAPIGit(t, repository, "worktree", "add", "-q", "-b", "session-test", worktree)
	if err := server.Store.UpdateSessionWorktree(session.ID, "worktree", worktree); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	localRepositoryID := discoverOnlyAPIRepository(t, server, project.ID)
	worktreeList := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-repositories?session_id=%s", project.ID, session.ID), nil)
	if worktreeList.Code != http.StatusOK {
		t.Fatalf("worktree repositories = %d: %s", worktreeList.Code, worktreeList.Body.String())
	}
	var body struct {
		WorkspaceMode string `json:"workspace_mode"`
		Repositories  []struct {
			ID       string `json:"id"`
			Relation string `json:"relation"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(worktreeList.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.WorkspaceMode != "worktree" || len(body.Repositories) != 1 || body.Repositories[0].Relation != "workspace" {
		t.Fatalf("unexpected worktree discovery: %+v", body)
	}
	worktreeRepositoryID := body.Repositories[0].ID
	if worktreeRepositoryID == localRepositoryID {
		t.Fatal("local checkout and linked worktree must have distinct repository IDs")
	}

	diff := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/diff-stats?session_id=%s&repository_id=%s", project.ID, session.ID, worktreeRepositoryID), nil)
	if diff.Code != http.StatusOK {
		t.Fatalf("worktree diff = %d: %s", diff.Code, diff.Body.String())
	}
	var stats GitDiffResponse
	if err := json.Unmarshal(diff.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if len(stats.Files) != 1 || stats.Files[0].Path != "tracked.txt" {
		t.Fatalf("worktree diff mismatch: %+v", stats.Files)
	}

	staleScope := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-status?session_id=%s&repository_id=%s", project.ID, session.ID, localRepositoryID), nil)
	if staleScope.Code != http.StatusNotFound || !strings.Contains(staleScope.Body.String(), gitrepo.CodeNotFound) {
		t.Fatalf("local repository ID was accepted in worktree scope: %d: %s", staleScope.Code, staleScope.Body.String())
	}
}

func TestLegacyAncestorSessionWorktreeRootIsRemapped(t *testing.T) {
	server := newWorktreeServer(t)
	repository := t.TempDir()
	initAPIRepo(t, repository)
	commitAPIFile(t, repository, "module/tracked.txt", "tracked\n")
	projectDirectory := filepath.Join(repository, "module")
	project := createAPIProject(t, server, projectDirectory)
	session, err := server.Store.CreateSession(store.CreateSessionRequest{Name: "legacy-root", ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(t.TempDir(), "legacy-worktree")
	runAPIGit(t, repository, "worktree", "add", "-q", "-b", "legacy-root", legacyRoot)
	if err := server.Store.UpdateSessionWorktree(session.ID, "worktree", legacyRoot); err != nil {
		t.Fatal(err)
	}

	list := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-repositories?session_id=%s", project.ID, session.ID), nil)
	var discovery struct {
		Repositories []struct {
			Relation string `json:"relation"`
		} `json:"repositories"`
	}
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &discovery) != nil || len(discovery.Repositories) != 1 {
		t.Fatalf("legacy worktree discovery = %d: %+v: %s", list.Code, discovery, list.Body.String())
	}
	if discovery.Repositories[0].Relation != "ancestor" {
		t.Fatalf("legacy worktree relation = %q, want ancestor", discovery.Repositories[0].Relation)
	}
	wantDirectory := filepath.Join(legacyRoot, "module")
	persisted, err := server.Store.GetSession(session.ID)
	if err != nil || persisted == nil {
		t.Fatalf("load legacy session: %v", err)
	}
	if got := server.sessionWorkDir(persisted); got != wantDirectory {
		t.Fatalf("legacy terminal directory = %q, want %q", got, wantDirectory)
	}
	if got := server.ensureWorktree(session.ID, persisted, projectDirectory); got != wantDirectory {
		t.Fatalf("normalized existing worktree directory = %q, want %q", got, wantDirectory)
	}
	persisted, err = server.Store.GetSession(session.ID)
	if err != nil || persisted == nil || persisted.WorktreePath != wantDirectory {
		t.Fatalf("normalized persisted worktree = %#v, %v", persisted, err)
	}
	if err := RemoveWorktree(persisted.WorktreePath); err != nil {
		t.Fatalf("remove normalized legacy worktree: %v", err)
	}
}

func TestAncestorProjectWorktreePreservesProjectDirectoryAndRepositoryScope(t *testing.T) {
	server := newWorktreeServer(t)
	repository := t.TempDir()
	initAPIRepo(t, repository)
	commitAPIFile(t, repository, "module-a/tracked.txt", "module\n")
	commitAPIFile(t, repository, "outside.txt", "before\n")
	projectDirectory := filepath.Join(repository, "module-a")
	project := createAPIProject(t, server, projectDirectory)
	session, err := server.Store.CreateSession(store.CreateSessionRequest{Name: "ancestor-worktree", ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Store.UpdateSessionWorktree(session.ID, "worktree", ""); err != nil {
		t.Fatal(err)
	}
	session, err = server.Store.GetSession(session.ID)
	if err != nil || session == nil {
		t.Fatalf("load session: %v", err)
	}

	effectiveDirectory := server.ensureWorktree(session.ID, session, projectDirectory)
	worktreeRoot, err := gitWorktreeRoot(effectiveDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(worktreeRoot, "module-a"); effectiveDirectory != want {
		t.Fatalf("effective worktree directory = %q, want %q", effectiveDirectory, want)
	}
	if prefix := runAPIGit(t, effectiveDirectory, "rev-parse", "--show-prefix"); prefix != "module-a/\n" {
		t.Fatalf("worktree Git prefix = %q, want module-a/", prefix)
	}
	persisted, err := server.Store.GetSession(session.ID)
	if err != nil || persisted == nil || persisted.WorktreePath != effectiveDirectory {
		t.Fatalf("persisted worktree path = %#v, %v", persisted, err)
	}
	if workDir := server.sessionWorkDir(persisted); workDir != effectiveDirectory {
		t.Fatalf("terminal work directory = %q, want %q", workDir, effectiveDirectory)
	}

	if err := os.WriteFile(filepath.Join(worktreeRoot, "outside.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	list := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-repositories?session_id=%s", project.ID, session.ID), nil)
	if list.Code != http.StatusOK {
		t.Fatalf("worktree repository discovery = %d: %s", list.Code, list.Body.String())
	}
	var discovery struct {
		WorkspaceMode string `json:"workspace_mode"`
		Repositories  []struct {
			ID       string `json:"id"`
			Relation string `json:"relation"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &discovery); err != nil {
		t.Fatal(err)
	}
	if discovery.WorkspaceMode != "worktree" || len(discovery.Repositories) != 1 || discovery.Repositories[0].Relation != "ancestor" {
		t.Fatalf("unexpected ancestor worktree discovery: %+v", discovery)
	}
	repositoryID := discovery.Repositories[0].ID
	statusResponse := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-status?session_id=%s&repository_id=%s", project.ID, session.ID, repositoryID), nil)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("ancestor worktree status = %d: %s", statusResponse.Code, statusResponse.Body.String())
	}
	var status GitStatusResponse
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Unstaged) != 1 || status.Unstaged[0].Path != "outside.txt" {
		t.Fatalf("repository-root-relative worktree status = %+v", status)
	}
	read := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/file-content?session_id=%s&repository_id=%s&path=outside.txt", project.ID, session.ID, repositoryID), nil)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), "after\\n") {
		t.Fatalf("ancestor worktree file read = %d: %s", read.Code, read.Body.String())
	}

	if err := RemoveWorktree(effectiveDirectory); err != nil {
		t.Fatalf("remove worktree through effective subdirectory: %v", err)
	}
	if _, err := os.Stat(worktreeRoot); !os.IsNotExist(err) {
		t.Fatalf("worktree root still exists after removal: %v", err)
	}
}

func TestDiffStatsAndCommitFilesPreserveRenameAndBinaryMetadata(t *testing.T) {
	repository := t.TempDir()
	initAPIRepo(t, repository)
	oldName := "old -> file.txt"
	newName := "new\t文件.txt"
	commitAPIFile(t, repository, oldName, "before\n")
	if err := os.WriteFile(filepath.Join(repository, "binary.dat"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	runAPIGit(t, repository, "add", "--", "binary.dat")
	runAPIGit(t, repository, "commit", "-q", "-m", "binary")
	if err := os.Rename(filepath.Join(repository, oldName), filepath.Join(repository, newName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "binary.dat"), []byte{'x', 0, 'y'}, 0o644); err != nil {
		t.Fatal(err)
	}
	runAPIGit(t, repository, "add", "-A")
	renameDiff, err := getFileDiff(context.Background(), repository, newName, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(renameDiff, "rename from ") || !strings.Contains(renameDiff, "rename to ") {
		t.Fatalf("file diff lost rename pair: %q", renameDiff)
	}

	stats, err := runGitDiffStats(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string]GitDiffFile)
	for _, file := range stats {
		byPath[file.Path] = file
	}
	if rename := byPath[newName]; rename.Status != "renamed" || rename.OriginalPath != oldName {
		t.Fatalf("rename metadata lost: %#v", rename)
	}
	if binary := byPath["binary.dat"]; !binary.Binary || binary.Insertions != 0 || binary.Deletions != 0 {
		t.Fatalf("binary metadata lost: %#v", binary)
	}

	runAPIGit(t, repository, "commit", "-q", "-m", "rename and binary")
	hash := strings.TrimSpace(runAPIGit(t, repository, "rev-parse", "HEAD"))
	files, err := getCommitFiles(context.Background(), repository, hash)
	if err != nil {
		t.Fatal(err)
	}
	foundRename := false
	for _, file := range files {
		if file.Path == newName && file.OriginalPath == oldName && file.Status == "renamed" {
			foundRename = true
		}
	}
	if !foundRename {
		t.Fatalf("commit files lost rename metadata: %#v", files)
	}
}

func TestUnbornRepositoryUsesDynamicEmptyTreeBaseline(t *testing.T) {
	repository := t.TempDir()
	initAPIRepo(t, repository)
	if err := os.WriteFile(filepath.Join(repository, "staged.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runAPIGit(t, repository, "add", "--", "staged.txt")
	if err := os.WriteFile(filepath.Join(repository, "staged.txt"), []byte("staged\nand modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := runGitDiffStats(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]bool)
	for _, file := range files {
		paths[file.Path] = true
	}
	if !paths["staged.txt"] || !paths["untracked.txt"] {
		t.Fatalf("unborn diff omitted files: %#v", files)
	}
}

func TestRedactRemoteURLStripsCredentials(t *testing.T) {
	remote := "https://user:secret@git.example.com/group/project.git"
	if redacted := redactRemoteURL(remote); strings.Contains(redacted, "secret") || strings.Contains(redacted, "user") {
		t.Fatalf("remote credentials were not redacted: %s", redacted)
	}
}

func TestUntrackedDiffHandlesSpecialFilenameAndDoesNotFollowSymlink(t *testing.T) {
	repository := t.TempDir()
	initAPIRepo(t, repository)
	specialName := "tab\tand\nunicode-文件.txt"
	if err := os.WriteFile(filepath.Join(repository, specialName), []byte("special content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff, err := getFileDiff(context.Background(), repository, specialName, false, false)
	if err != nil || diff == "" || !strings.Contains(diff, "special content") {
		t.Fatalf("special filename diff failed: %v: %q", err, diff)
	}

	target := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(target, []byte("TOP SECRET CONTENT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkName := "linked-file"
	if err := os.Symlink(target, filepath.Join(repository, linkName)); err != nil {
		t.Fatal(err)
	}
	linkDiff, err := getFileDiff(context.Background(), repository, linkName, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(linkDiff, "TOP SECRET CONTENT") || !strings.Contains(linkDiff, target) {
		t.Fatalf("symlink diff followed target instead of showing link text: %q", linkDiff)
	}
	stats, err := runGitDiffStats(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	foundLink := false
	for _, file := range stats {
		if file.Path == linkName && (file.Binary || file.Insertions != 1) {
			t.Fatalf("symlink stats must count link text without following target: %#v", file)
		}
		if file.Path == linkName {
			foundLink = true
		}
	}
	if !foundLink {
		t.Fatalf("symlink missing from diff stats: %#v", stats)
	}
}

func TestFileDiffOnlySynthesizesUntrackedFiles(t *testing.T) {
	repository := t.TempDir()
	initAPIRepo(t, repository)
	commitAPIFile(t, repository, "tracked.txt", "tracked content\n")

	cleanDiff, err := getFileDiff(context.Background(), repository, "tracked.txt", false, false)
	if err != nil {
		t.Fatalf("clean tracked diff: %v", err)
	}
	if cleanDiff != "" {
		t.Fatalf("clean tracked file was represented as changed: %q", cleanDiff)
	}

	if err := os.WriteFile(filepath.Join(repository, "untracked.txt"), []byte("new content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stagedDiff, err := getFileDiff(context.Background(), repository, "untracked.txt", false, true)
	if err != nil {
		t.Fatalf("staged untracked diff: %v", err)
	}
	if stagedDiff != "" {
		t.Fatalf("untracked file appeared in a staged-only diff: %q", stagedDiff)
	}

	untrackedDiff, err := getFileDiff(context.Background(), repository, "untracked.txt", false, false)
	if err != nil {
		t.Fatalf("untracked worktree diff: %v", err)
	}
	if !strings.Contains(untrackedDiff, "new content") || !strings.Contains(untrackedDiff, "new file mode") {
		t.Fatalf("untracked file diff missing content: %q", untrackedDiff)
	}
}

func TestPushWithoutRemoteAndDetachedHeadAreUnsupported(t *testing.T) {
	server := newWorktreeServer(t)
	repository := t.TempDir()
	initAPIRepo(t, repository)
	commitAPIFile(t, repository, "tracked.txt", "tracked\n")
	project := createAPIProject(t, server, repository)

	withoutRemote := doRequest(t, server, http.MethodPost, fmt.Sprintf("/api/projects/%s/git-push", project.ID), nil)
	if withoutRemote.Code != http.StatusUnprocessableEntity || !strings.Contains(withoutRemote.Body.String(), gitrepo.CodeUnsupported) {
		t.Fatalf("push without remote = %d: %s", withoutRemote.Code, withoutRemote.Body.String())
	}
	runAPIGit(t, repository, "remote", "add", "origin", "git@git.example.com:group/project.git")
	runAPIGit(t, repository, "checkout", "-q", "--detach")
	detached := doRequest(t, server, http.MethodPost, fmt.Sprintf("/api/projects/%s/git-push", project.ID), nil)
	if detached.Code != http.StatusUnprocessableEntity || !strings.Contains(detached.Body.String(), "detached HEAD") {
		t.Fatalf("push from detached HEAD = %d: %s", detached.Code, detached.Body.String())
	}
}

func TestWorkspaceFileOwnerResolvesSecondaryRepositoryAlias(t *testing.T) {
	server := newWorktreeServer(t)
	workspace := t.TempDir()
	repository := filepath.Join(t.TempDir(), "shared")
	initAPIRepo(t, repository)
	commitAPIFile(t, repository, "tracked.txt", "from shared repository\n")
	if err := os.Symlink(repository, filepath.Join(workspace, "alias-a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repository, filepath.Join(workspace, "alias-b")); err != nil {
		t.Fatal(err)
	}
	project := createAPIProject(t, server, workspace)

	response := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-file-head?path=alias-b/tracked.txt", project.ID), nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "from shared repository") {
		t.Fatalf("secondary alias owner response = %d: %s", response.Code, response.Body.String())
	}
}

func TestWorkspaceFileOwnerPrefersNestedRepositoryUnderLongAncestorPath(t *testing.T) {
	server := newWorktreeServer(t)
	ancestor := t.TempDir()
	// Keep every component below NAME_MAX while making the ancestor's absolute
	// path longer than the old fixed-priority offset used by file-owner lookup.
	for index := 0; index < 6; index++ {
		ancestor = filepath.Join(ancestor, fmt.Sprintf("%d-%s", index, strings.Repeat("x", 180)))
		if err := os.Mkdir(ancestor, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if len(ancestor) <= 1000 {
		t.Fatalf("test ancestor path is too short: %d", len(ancestor))
	}
	initAPIRepo(t, ancestor)
	commitAPIFile(t, ancestor, "outer.txt", "outer repository\n")

	workspace := filepath.Join(ancestor, "workspace")
	nested := filepath.Join(workspace, "nested")
	initAPIRepo(t, nested)
	commitAPIFile(t, nested, "tracked.txt", "nested repository\n")
	project := createAPIProject(t, server, workspace)

	response := doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/projects/%s/git-file-head?path=nested%%2Ftracked.txt", project.ID), nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "nested repository") {
		t.Fatalf("nested repository did not own workspace path: %d: %s", response.Code, response.Body.String())
	}
}
