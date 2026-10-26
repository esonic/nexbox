package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadStatusCountsUnpublishedCommitsWithoutUpstream(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	commit := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		runRepoGit(t, repo, "add", "tracked.txt")
		runRepoGit(t, repo, "commit", "-q", "-m", "change")
	}
	assertAhead := func(want int) {
		t.Helper()
		status, err := ReadStatus(context.Background(), repo)
		if err != nil || status == nil {
			t.Fatalf("read status: %v", err)
		}
		if status.Ahead != want || status.Behind != 0 || status.Summary().Total != 0 {
			t.Fatalf("status = %+v, want ahead=%d and clean files", status, want)
		}
	}
	assertAhead(0) // An unborn branch has no commits to publish.
	commit("initial\n")
	assertAhead(1) // With no remote, the local commit is unpublished.
	remote := t.TempDir()
	runRepoGit(t, remote, "init", "--bare", "-q")
	runRepoGit(t, repo, "remote", "add", "origin", remote)
	runRepoGit(t, repo, "push", "origin", "HEAD:refs/heads/main")
	runRepoGit(t, repo, "checkout", "-q", "-b", "session-branch")
	assertAhead(0) // A new branch sharing a published base is clean.
	commit("unpublished\n")
	assertAhead(1)
	if got := runRepoGit(t, repo, "for-each-ref", "--format=%(upstream)", "refs/heads/session-branch"); got != "\n" {
		t.Fatalf("status check configured an upstream: %q", got)
	}
	runRepoGit(t, repo, "push", "origin", "HEAD:refs/heads/session-branch")
	assertAhead(0) // Publishing without -u is sufficient to protect the commit.
	runRepoGit(t, repo, "branch", "--set-upstream-to=origin/session-branch")
	commit("ahead of upstream\n")
	// Once upstream exists, preserve Git's normal ahead/behind comparison,
	// even when that commit is already reachable from another remote branch.
	runRepoGit(t, repo, "push", "origin", "HEAD:refs/heads/other-branch")
	assertAhead(1)
}
