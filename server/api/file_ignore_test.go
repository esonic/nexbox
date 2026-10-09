package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// mavenLikeRepo mirrors the shape that exposed the bug: a Maven project whose
// build output under target/ is gitignored but was still being walked, so
// compiled artefacts crowded the search results ahead of the source file.
func mavenLikeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	ignoreTestInitRepo(t, root)
	ignoreTestCommitFile(t, root, ".gitignore", "target/\n*.log\n")
	ignoreTestCommitFile(t, root, "src/main/java/SignalAggregationService.java", "class SignalAggregationService {}\n")

	ignoreTestWriteUntracked(t, root, "target/classes/SignalAggregationService.class", "SignalAggregationService bytecode\n")
	ignoreTestWriteUntracked(t, root, "target/surefire-reports/SignalAggregationService.txt", "SignalAggregationService report\n")
	ignoreTestWriteUntracked(t, root, "debug.log", "SignalAggregationService noise\n")
	return root
}

func ignoreTestWriteUntracked(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIgnoreFilterExcludesGitignoredPaths(t *testing.T) {
	root := mavenLikeRepo(t)

	filter := ignoreFilterForRoot(root)
	if filter == nil {
		t.Fatal("no ignore filter for a git repository")
	}

	// target/ collapses to one directory entry, so the walk never descends.
	if !excludedFromSearch(filter, root, filepath.Join(root, "target"), true) {
		t.Error("gitignored target/ not excluded")
	}
	if !excludedFromSearch(filter, root, filepath.Join(root, "debug.log"), false) {
		t.Error("gitignored debug.log not excluded")
	}
	if excludedFromSearch(filter, root, filepath.Join(root, "src/main/java/SignalAggregationService.java"), false) {
		t.Error("tracked source file excluded from search")
	}
	if excludedFromSearch(filter, root, filepath.Join(root, "src"), true) {
		t.Error("src/ excluded from search")
	}
}

// A file that is checked in stays searchable even when a later ignore rule would
// match it — same rule git status follows, and what VSCode shows.
func TestIgnoreFilterKeepsTrackedFileMatchingIgnoreRule(t *testing.T) {
	root := t.TempDir()
	ignoreTestInitRepo(t, root)
	ignoreTestCommitFile(t, root, "keep.log", "tracked despite the rule\n")
	ignoreTestCommitFile(t, root, ".gitignore", "*.log\n")

	filter := ignoreFilterForRoot(root)
	if filter == nil {
		t.Fatal("no ignore filter for a git repository")
	}
	if excludedFromSearch(filter, root, filepath.Join(root, "keep.log"), false) {
		t.Error("tracked keep.log excluded by an ignore rule")
	}
}

// Outside a repository there are no ignore rules, but the hard-coded deny-list
// must still apply.
func TestExcludedFromSearchWithoutRepository(t *testing.T) {
	root := t.TempDir()
	ignoreTestWriteUntracked(t, root, "notes.txt", "hello\n")
	ignoreTestWriteUntracked(t, root, "node_modules/left-pad/index.js", "module.exports = 1\n")

	if filter := ignoreFilterForRoot(root); filter != nil {
		t.Fatalf("ignore filter built for a non-repository: %+v", filter)
	}
	if !excludedFromSearch(nil, root, filepath.Join(root, "node_modules"), true) {
		t.Error("node_modules not excluded by the deny-list")
	}
	if excludedFromSearch(nil, root, filepath.Join(root, "notes.txt"), false) {
		t.Error("plain file excluded outside a repository")
	}
}

func TestSearchFileContentsSkipsGitignored(t *testing.T) {
	root := mavenLikeRepo(t)

	results := searchFileContents(root, ignoreFilterForRoot(root), "signalaggregationservice", false, 50)
	if len(results) == 0 {
		t.Fatal("no content matches")
	}
	for _, match := range results {
		if match.Path != filepath.FromSlash("src/main/java/SignalAggregationService.java") {
			t.Errorf("match in gitignored file: %s:%d", match.Path, match.Line)
		}
	}
}

func ignoreTestInitRepo(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	ignoreTestGit(t, root, "init", "-q")
	ignoreTestGit(t, root, "config", "user.email", "test@example.com")
	ignoreTestGit(t, root, "config", "user.name", "Test")
}

func ignoreTestGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func ignoreTestCommitFile(t *testing.T, root, path, content string) {
	t.Helper()
	ignoreTestWriteUntracked(t, root, path, content)
	ignoreTestGit(t, root, "add", "--", path)
	ignoreTestGit(t, root, "commit", "-q", "-m", "initial")
}
