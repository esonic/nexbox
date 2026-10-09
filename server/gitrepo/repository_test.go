package gitrepo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func initRepo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	runRepoGit(t, path, "config", "user.email", "beehub@example.com")
	runRepoGit(t, path, "config", "user.name", "BeeHub Test")
}

func runRepoGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = path
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func testResolver() *Resolver {
	opts := DefaultOptions()
	opts.TopologyTTL = time.Millisecond
	opts.MaxDepth = 8
	return NewResolver(opts)
}

func TestDiscoverWorkspaceRepository(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	result, err := testResolver().Discover(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 1 || result.Repositories[0].Relation != RelationWorkspace || result.Repositories[0].DisplayPath != "." {
		t.Fatalf("unexpected repositories: %#v", result.Repositories)
	}
	if result.DefaultRepositoryID != result.Repositories[0].ID {
		t.Fatalf("default repository mismatch")
	}
}

func TestDiscoverAncestorRepository(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	workspace := filepath.Join(root, "modules", "app")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := testResolver().Discover(context.Background(), workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 1 || result.Repositories[0].Relation != RelationAncestor {
		t.Fatalf("unexpected repositories: %#v", result.Repositories)
	}
	if result.Repositories[0].CanonicalRoot != root {
		t.Fatalf("root = %q, want %q", result.Repositories[0].CanonicalRoot, root)
	}
}

func TestDiscoverMultipleNestedRepositories(t *testing.T) {
	workspace := t.TempDir()
	initRepo(t, filepath.Join(workspace, "repo-b"))
	initRepo(t, filepath.Join(workspace, "repo-a"))
	result, err := testResolver().Discover(context.Background(), workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 2 {
		t.Fatalf("got %d repositories: %#v", len(result.Repositories), result.Repositories)
	}
	if result.Repositories[0].DisplayPath != "repo-a" || result.Repositories[1].DisplayPath != "repo-b" {
		t.Fatalf("repositories are not stably sorted: %#v", result.Repositories)
	}
	if result.DefaultRepositoryID != result.Repositories[0].ID {
		t.Fatalf("default repository must be the first stable repository")
	}
	if _, err := testResolver().Resolve(context.Background(), workspace, ""); ErrorCode(err) != CodeSelectionRequired {
		t.Fatalf("expected selection-required error, got %v", err)
	}
}

func TestDiscoverDirectSymlinkRepository(t *testing.T) {
	workspace := t.TempDir()
	targetParent := t.TempDir()
	target := filepath.Join(targetParent, "shared-sdk")
	initRepo(t, target)
	link := filepath.Join(workspace, "linked-sdk")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.TopologyTTL = time.Hour
	resolver := NewResolver(opts)
	result, err := resolver.Discover(context.Background(), workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 1 || result.Repositories[0].Relation != RelationSymlink || result.Repositories[0].DisplayPath != "linked-sdk" {
		t.Fatalf("unexpected repositories: %#v", result.Repositories)
	}
	if !result.Repositories[0].OutsideWorkspace {
		t.Fatalf("symlink target outside workspace must be marked external")
	}
	selected, err := resolver.Resolve(context.Background(), workspace, result.Repositories[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	other := filepath.Join(targetParent, "other")
	initRepo(t, other)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), workspace, result.Repositories[0].ID); ErrorCode(err) != CodeStale {
		t.Fatalf("expected stale error after symlink retarget, got %v", err)
	}
	if _, err := resolver.LockMutation(context.Background(), selected); ErrorCode(err) != CodeStale {
		t.Fatalf("expected mutation-time stale error after symlink retarget, got %v", err)
	}
}

func TestDiscoverSymlinkToRepositorySubdirectory(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(t.TempDir(), "shared-skills")
	initRepo(t, target)
	linkedSubdirectory := filepath.Join(target, "skills")
	if err := os.MkdirAll(linkedSubdirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "linked-skills")
	if err := os.Symlink(linkedSubdirectory, link); err != nil {
		t.Fatal(err)
	}

	opts := DefaultOptions()
	opts.TopologyTTL = time.Hour
	resolver := NewResolver(opts)
	result, err := resolver.Discover(context.Background(), workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 1 {
		t.Fatalf("unexpected repositories: %#v", result.Repositories)
	}
	repository := result.Repositories[0]
	if repository.Relation != RelationSymlink || repository.CanonicalRoot != target || repository.LinkedTarget != linkedSubdirectory {
		t.Fatalf("symlinked subdirectory was not resolved to its repository: %#v", repository)
	}
	if repository.AliasPrefixes["linked-skills"] != "skills" {
		t.Fatalf("alias prefix = %q, want skills", repository.AliasPrefixes["linked-skills"])
	}
	if path, ok := WorkspacePath(workspace, repository, "skills/tool/SKILL.md"); !ok || path != "linked-skills/tool/SKILL.md" {
		t.Fatalf("workspace path = %q, %v", path, ok)
	}
	if path, ok := WorkspacePath(workspace, repository, "README.md"); ok || path != "" {
		t.Fatalf("repository-root file unexpectedly mapped through subdirectory link: %q, %v", path, ok)
	}

	otherSubdirectory := filepath.Join(target, "other")
	if err := os.Mkdir(otherSubdirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(otherSubdirectory, link); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), workspace, repository.ID); ErrorCode(err) != CodeStale {
		t.Fatalf("retargeting within the same repository must be stale, got %v", err)
	}
}

func TestDiscoverDeduplicatesAliases(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(t.TempDir(), "repo")
	initRepo(t, target)
	if err := os.Symlink(target, filepath.Join(workspace, "one")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(workspace, "two")); err != nil {
		t.Fatal(err)
	}
	result, err := testResolver().Discover(context.Background(), workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 1 || len(result.Repositories[0].Aliases) != 2 {
		t.Fatalf("unexpected repositories: %#v", result.Repositories)
	}
}

func TestDiscoverSymlinkRepositoriesOnlyWithinConfiguredDepth(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(t.TempDir(), "external")
	initRepo(t, target)
	if err := os.MkdirAll(filepath.Join(workspace, "one"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(workspace, "one", "shallow")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "one", "two"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(workspace, "one", "two", "deep")); err != nil {
		t.Fatal(err)
	}

	result, err := NewResolver(DefaultOptions()).Discover(context.Background(), workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 1 || len(result.Repositories[0].Aliases) != 1 || result.Repositories[0].Aliases[0] != "one/shallow" {
		t.Fatalf("unexpected symlink-depth discovery: %#v", result.Repositories)
	}
}

func TestReadStatusHandlesSpecialNames(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	names := []string{
		"space -> unicode-文件.txt",
		"tab\tname.txt",
		"newline\nname.txt",
		"-leading-dash.txt",
		":pathspec-magic.txt",
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(root, name), []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	status, err := ReadStatus(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool)
	for _, file := range status.Untracked {
		got[file.Path] = true
	}
	for _, name := range names {
		if !got[name] {
			t.Errorf("special filename %q missing from status", name)
		}
	}
	if len(got) != len(names) {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestReadStatusPreservesRenameAndDeduplicatesSummary(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	oldName := "old -> name.txt"
	newName := "new\t名称.txt"
	if err := os.WriteFile(filepath.Join(root, oldName), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runRepoGit(t, root, "add", "--", oldName)
	runRepoGit(t, root, "commit", "-q", "-m", "initial")
	if err := os.Rename(filepath.Join(root, oldName), filepath.Join(root, newName)); err != nil {
		t.Fatal(err)
	}
	runRepoGit(t, root, "add", "-A")
	if err := os.WriteFile(filepath.Join(root, newName), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := ReadStatus(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Staged) != 1 || status.Staged[0].Status != "renamed" || status.Staged[0].Path != newName || status.Staged[0].OriginalPath != oldName {
		t.Fatalf("unexpected staged rename: %#v", status.Staged)
	}
	if len(status.Unstaged) != 1 || status.Unstaged[0].Path != newName {
		t.Fatalf("unexpected unstaged status: %#v", status.Unstaged)
	}
	if summary := status.Summary(); summary.Total != 1 || summary.Staged != 1 || summary.Unstaged != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
}

func TestDiscoverOuterAndNestedRepositories(t *testing.T) {
	workspace := t.TempDir()
	initRepo(t, workspace)
	inner := filepath.Join(workspace, "packages", "inner")
	initRepo(t, inner)
	if err := os.WriteFile(filepath.Join(inner, "inner-only.txt"), []byte("inner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := testResolver().Discover(context.Background(), workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 2 || result.Repositories[0].Relation != RelationWorkspace || result.Repositories[1].Relation != RelationNested {
		t.Fatalf("unexpected repositories: %#v", result.Repositories)
	}
	outerStatus, err := ReadStatus(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(outerStatus.Untracked) != 0 {
		t.Fatalf("outer repository exposed nested repository contents: %#v", outerStatus.Untracked)
	}
	innerStatus, err := ReadStatus(context.Background(), inner)
	if err != nil || len(innerStatus.Untracked) != 1 || innerStatus.Untracked[0].Path != "inner-only.txt" {
		t.Fatalf("inner repository status mismatch: %#v, %v", innerStatus, err)
	}
}

func TestDiscoverBrokenSymlinkWarnsAndBareRepositoryIsIgnored(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Symlink(filepath.Join(workspace, "missing"), filepath.Join(workspace, "broken")); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(workspace, "archive.git")
	if out, err := exec.Command("git", "init", "--bare", "-q", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	result, err := testResolver().Discover(context.Background(), workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 0 {
		t.Fatalf("bare repository must not be discovered: %#v", result.Repositories)
	}
	if len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], "broken") {
		t.Fatalf("expected broken symlink warning, got %#v", result.Warnings)
	}
}

func TestDiscoverSymlinkCycleReturnsWithoutRecursing(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Symlink("cycle-b", filepath.Join(workspace, "cycle-a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("cycle-a", filepath.Join(workspace, "cycle-b")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := testResolver().Discover(ctx, workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatalf("symlink cycle exhausted discovery timeout: %v", ctx.Err())
	}
	if len(result.Repositories) != 0 || len(result.Warnings) != 2 {
		t.Fatalf("unexpected cycle discovery result: %#v", result)
	}
}

func TestDiscoverGitFileWorktree(t *testing.T) {
	source := t.TempDir()
	initRepo(t, source)
	if err := os.WriteFile(filepath.Join(source, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runRepoGit(t, source, "add", "--", "seed.txt")
	runRepoGit(t, source, "commit", "-q", "-m", "seed")
	workspace := t.TempDir()
	worktree := filepath.Join(workspace, "linked-worktree")
	runRepoGit(t, source, "worktree", "add", "-q", "-b", "test-worktree", worktree)
	result, err := testResolver().Discover(context.Background(), workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 1 || result.Repositories[0].DisplayPath != "linked-worktree" {
		t.Fatalf("worktree with .git file was not discovered: %#v", result.Repositories)
	}
}

func TestDiscoveryLimitReturnsPartialResult(t *testing.T) {
	workspace := t.TempDir()
	initRepo(t, filepath.Join(workspace, "a"))
	initRepo(t, filepath.Join(workspace, "b"))
	opts := DefaultOptions()
	opts.MaxRepositories = 1
	opts.MaxDepth = 8
	result, err := NewResolver(opts).Discover(context.Background(), workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 1 || !result.Truncated || len(result.Warnings) == 0 {
		t.Fatalf("expected partial truncated result, got %#v", result)
	}
}

func TestDepthPolicySilentlyPrunesOnlyDeepBranch(t *testing.T) {
	workspace := t.TempDir()
	initRepo(t, filepath.Join(workspace, "a-deep", "one", "two", "three"))
	initRepo(t, filepath.Join(workspace, "z-repository"))
	opts := DefaultOptions()
	opts.MaxDepth = 2
	result, err := NewResolver(opts).Discover(context.Background(), workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 1 || result.Repositories[0].DisplayPath != "z-repository" {
		t.Fatalf("deep branch prevented later shallow discovery: %#v", result.Repositories)
	}
	if result.Truncated || len(result.Warnings) != 0 {
		t.Fatalf("depth policy should be a silent discovery boundary: %#v", result)
	}
}

func TestDefaultGeneratedDirectoryIgnoreAvoidsDepthWarning(t *testing.T) {
	workspace := t.TempDir()
	initRepo(t, workspace)
	deepTarget := filepath.Join(workspace, "target")
	for index := 0; index < 12; index++ {
		deepTarget = filepath.Join(deepTarget, "generated")
	}
	if err := os.MkdirAll(deepTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	// Tool directories remain discoverable (they may themselves be linked
	// repositories), while the wider default depth accommodates normal content.
	for _, toolDirectory := range []string{".claude", ".agents"} {
		if err := os.MkdirAll(filepath.Join(workspace, toolDirectory, "one", "two", "three", "four", "five"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	result, err := NewResolver(DefaultOptions()).Discover(context.Background(), workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Truncated || containsWarning(result.Warnings, "limit") {
		t.Fatalf("generated directories caused a scan limit warning: %#v", result)
	}
}

func containsWarning(warnings []string, substring string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substring) {
			return true
		}
	}
	return false
}

func TestResolveFilePathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	repo := Repository{CanonicalRoot: root}
	if _, err := ResolveFilePath(repo, "escape/secret.txt"); ErrorCode(err) != CodeInvalidPath {
		t.Fatalf("expected invalid path, got %v", err)
	}
}

func TestStatusCacheCanBeInvalidated(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	opts := DefaultOptions()
	opts.StatusTTL = time.Hour
	resolver := NewResolver(opts)
	discovery, err := resolver.Discover(context.Background(), root, false)
	if err != nil || len(discovery.Repositories) != 1 {
		t.Fatalf("discovery failed: %#v, %v", discovery, err)
	}
	repository := discovery.Repositories[0]
	if status, err := resolver.Status(context.Background(), repository, false); err != nil || status.Summary().Total != 0 {
		t.Fatalf("initial status: %#v, %v", status, err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, err := resolver.Status(context.Background(), repository, false); err != nil || status.Summary().Total != 0 {
		t.Fatalf("status cache was not used: %#v, %v", status, err)
	}
	resolver.InvalidateStatus(repository.CanonicalRoot)
	if status, err := resolver.Status(context.Background(), repository, false); err != nil || status.Summary().Total != 1 {
		t.Fatalf("status invalidation failed: %#v, %v", status, err)
	}
}

func TestResolverEvictsExpiredCachesWithRealRepositories(t *testing.T) {
	base := t.TempDir()
	firstRoot := filepath.Join(base, "first")
	secondRoot := filepath.Join(base, "second")
	initRepo(t, firstRoot)
	initRepo(t, secondRoot)

	clock := time.Unix(1_700_000_000, 0)
	opts := DefaultOptions()
	opts.TopologyTTL = time.Minute
	opts.StatusTTL = time.Minute
	resolver := NewResolver(opts)
	resolver.now = func() time.Time { return clock }

	first, err := resolver.Discover(context.Background(), firstRoot, false)
	if err != nil || len(first.Repositories) != 1 {
		t.Fatalf("first discovery: %#v, %v", first, err)
	}
	if _, err := resolver.Status(context.Background(), first.Repositories[0], false); err != nil {
		t.Fatal(err)
	}
	if len(resolver.cache) != 1 || len(resolver.statusCache) != 1 {
		t.Fatalf("initial cache sizes = topology:%d status:%d", len(resolver.cache), len(resolver.statusCache))
	}

	clock = clock.Add(2 * time.Minute)
	second, err := resolver.Discover(context.Background(), secondRoot, false)
	if err != nil || len(second.Repositories) != 1 {
		t.Fatalf("second discovery: %#v, %v", second, err)
	}
	if len(resolver.cache) != 1 {
		t.Fatalf("expired topology cache was retained: %d entries", len(resolver.cache))
	}
	if len(resolver.statusCache) != 0 {
		t.Fatalf("expired status cache was retained: %d entries", len(resolver.statusCache))
	}
	for key := range resolver.cache {
		if strings.Contains(key, firstRoot) {
			t.Fatalf("expired first repository cache key remains: %q", key)
		}
	}
	if _, err := resolver.Status(context.Background(), second.Repositories[0], false); err != nil {
		t.Fatal(err)
	}
	if len(resolver.statusCache) != 1 {
		t.Fatalf("second repository status was not cached: %d entries", len(resolver.statusCache))
	}
}

func TestResolverCachesHaveHardLimits(t *testing.T) {
	resolver := NewResolver(DefaultOptions())
	baseTime := time.Unix(1_700_000_000, 0)
	resolver.mu.Lock()
	for index := 0; index < maxTopologyCacheEntries+20; index++ {
		key := fmt.Sprintf("topology-%03d", index)
		resolver.storeTopologyCacheLocked(key, cacheEntry{expiresAt: baseTime.Add(time.Duration(index+1) * time.Second)})
	}
	for index := 0; index < maxStatusCacheEntries+20; index++ {
		key := fmt.Sprintf("status-%03d", index)
		resolver.storeStatusCacheLocked(key, statusCacheEntry{expiresAt: baseTime.Add(time.Duration(index+1) * time.Second)})
	}
	topologySize := len(resolver.cache)
	statusSize := len(resolver.statusCache)
	resolver.mu.Unlock()

	if topologySize != maxTopologyCacheEntries {
		t.Fatalf("topology cache size = %d, want %d", topologySize, maxTopologyCacheEntries)
	}
	if statusSize != maxStatusCacheEntries {
		t.Fatalf("status cache size = %d, want %d", statusSize, maxStatusCacheEntries)
	}
}

func TestMutationLockSerializesRepositoryWrites(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	resolver := testResolver()
	selected, err := resolver.Resolve(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := resolver.LockMutation(context.Background(), selected)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := resolver.LockMutation(ctx, selected); ErrorCode(err) != CodeBusy {
		unlock()
		t.Fatalf("contended mutation lock error = %v, want %s", err, CodeBusy)
	}
	unlock()

	secondUnlock, err := resolver.LockMutation(context.Background(), selected)
	if err != nil {
		t.Fatalf("mutation lock was not reusable after cancellation: %v", err)
	}
	secondUnlock()
}
