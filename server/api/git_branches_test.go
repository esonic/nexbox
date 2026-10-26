package api

import (
	"context"
	"path/filepath"
	"testing"
)

// cloneWithRemoteBranches builds an upstream repo with an extra branch and
// returns a clone of it, so refs/remotes/origin/HEAD exists like in a real
// checkout.
func cloneWithRemoteBranches(t *testing.T, extraBranches ...string) string {
	t.Helper()
	root := t.TempDir()
	upstream := filepath.Join(root, "upstream")
	initAPIRepo(t, upstream)
	commitAPIFile(t, upstream, "README.md", "hello")
	runAPIGit(t, upstream, "branch", "-M", "main")
	for _, branch := range extraBranches {
		runAPIGit(t, upstream, "checkout", "-q", "-b", branch)
		commitAPIFile(t, upstream, branch+".txt", branch)
	}
	runAPIGit(t, upstream, "checkout", "-q", "main")

	clone := filepath.Join(root, "clone")
	runAPIGit(t, root, "clone", "-q", upstream, clone)
	runAPIGit(t, clone, "config", "user.email", "test@example.com")
	runAPIGit(t, clone, "config", "user.name", "NexBox Test")
	return clone
}

func findBranch(branches []GitBranchInfo, name string) (GitBranchInfo, bool) {
	for _, branch := range branches {
		if branch.Name == name {
			return branch, true
		}
	}
	return GitBranchInfo{}, false
}

// refs/remotes/origin/HEAD shortens to "origin", which must not be reported as
// a branch at all: checking it out lands on a detached HEAD.
func TestParseGitBranchesSkipsRemoteHEAD(t *testing.T) {
	clone := cloneWithRemoteBranches(t, "feature")

	result, err := parseGitBranches(context.Background(), clone)
	if err != nil {
		t.Fatal(err)
	}

	if branch, found := findBranch(result.Branches, "origin"); found {
		t.Fatalf("remote HEAD symref reported as branch: %+v", branch)
	}
	if result.Current != "main" {
		t.Fatalf("current = %q, want main", result.Current)
	}
	feature, found := findBranch(result.Branches, "origin/feature")
	if !found {
		t.Fatalf("origin/feature missing from %+v", result.Branches)
	}
	if !feature.IsRemote {
		t.Error("origin/feature not marked remote")
	}
	if feature.LocalName != "feature" {
		t.Errorf("origin/feature local_name = %q, want feature", feature.LocalName)
	}
}

// Remote classification must come from the full refname, not an "origin/"
// prefix on the short name: branches of other remotes are remote too.
func TestParseGitBranchesClassifiesNonOriginRemotes(t *testing.T) {
	clone := cloneWithRemoteBranches(t, "feature")
	other := filepath.Join(t.TempDir(), "fork")
	initAPIRepo(t, other)
	commitAPIFile(t, other, "fork.txt", "fork")
	runAPIGit(t, other, "branch", "-M", "shared")
	runAPIGit(t, clone, "remote", "add", "upstream", other)
	runAPIGit(t, clone, "fetch", "-q", "upstream")

	result, err := parseGitBranches(context.Background(), clone)
	if err != nil {
		t.Fatal(err)
	}

	shared, found := findBranch(result.Branches, "upstream/shared")
	if !found {
		t.Fatalf("upstream/shared missing from %+v", result.Branches)
	}
	if !shared.IsRemote {
		t.Error("upstream/shared not marked remote")
	}
	if shared.LocalName != "shared" {
		t.Errorf("upstream/shared local_name = %q, want shared", shared.LocalName)
	}
	if main, found := findBranch(result.Branches, "main"); !found || main.IsRemote {
		t.Errorf("local main misclassified: %+v found=%v", main, found)
	}
}

func TestValidateRemoteTrackingBranch(t *testing.T) {
	clone := cloneWithRemoteBranches(t, "feature")
	ctx := context.Background()

	if err := validateRemoteTrackingBranch(ctx, clone, "origin/feature"); err != nil {
		t.Fatalf("origin/feature rejected: %v", err)
	}
	for _, name := range []string{"main", "origin/missing", "HEAD", "origin"} {
		if err := validateRemoteTrackingBranch(ctx, clone, name); err == nil {
			t.Errorf("%q accepted as remote-tracking branch", name)
		}
	}
}

func TestLocalBranchExists(t *testing.T) {
	clone := cloneWithRemoteBranches(t, "feature")
	ctx := context.Background()

	if !localBranchExists(ctx, clone, "main") {
		t.Error("main reported missing")
	}
	for _, name := range []string{"feature", "origin/feature", "origin"} {
		if localBranchExists(ctx, clone, name) {
			t.Errorf("%q reported as a local branch", name)
		}
	}
}

// Creating a local branch from a remote-tracking start point must set upstream,
// which is what the git panel does when a REMOTE entry has no local branch yet.
func TestCheckoutTrackingStartPoint(t *testing.T) {
	clone := cloneWithRemoteBranches(t, "feature")
	ctx := context.Background()

	if err := runGitCommand(ctx, clone, "checkout", "-b", "feature", "--track", "origin/feature"); err != nil {
		t.Fatal(err)
	}

	result, err := parseGitBranches(ctx, clone)
	if err != nil {
		t.Fatal(err)
	}
	if result.Current != "feature" {
		t.Fatalf("current = %q, want feature", result.Current)
	}
	feature, found := findBranch(result.Branches, "feature")
	if !found {
		t.Fatalf("local feature missing from %+v", result.Branches)
	}
	if feature.Upstream != "origin/feature" {
		t.Errorf("upstream = %q, want origin/feature", feature.Upstream)
	}
}
