package store

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// UpdateSessionWorktree tests
// ---------------------------------------------------------------------------

func TestUpdateSessionWorktree(t *testing.T) {
	s := newTestDB(t)

	// Create a session first
	sess, err := s.CreateSession(CreateSessionRequest{Name: "wt-test"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Read back from DB — scanSession normalises empty string to "local"
	fetched, err := s.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("GetSession (initial): %v", err)
	}
	if fetched.WorktreeMode != "local" {
		t.Errorf("default WorktreeMode = %q, want %q", fetched.WorktreeMode, "local")
	}
	if fetched.WorktreePath != "" {
		t.Errorf("default WorktreePath = %q, want empty", fetched.WorktreePath)
	}

	// Set to worktree mode with a path
	if err := s.UpdateSessionWorktree(sess.ID, "worktree", "/tmp/worktrees/abc12345"); err != nil {
		t.Fatalf("UpdateSessionWorktree: %v", err)
	}

	got, err := s.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.WorktreeMode != "worktree" {
		t.Errorf("WorktreeMode = %q, want %q", got.WorktreeMode, "worktree")
	}
	if got.WorktreePath != "/tmp/worktrees/abc12345" {
		t.Errorf("WorktreePath = %q, want %q", got.WorktreePath, "/tmp/worktrees/abc12345")
	}

	// Reset back to local
	if err := s.UpdateSessionWorktree(sess.ID, "local", ""); err != nil {
		t.Fatalf("UpdateSessionWorktree reset: %v", err)
	}
	got, err = s.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("GetSession after reset: %v", err)
	}
	if got.WorktreeMode != "local" {
		t.Errorf("WorktreeMode after reset = %q, want %q", got.WorktreeMode, "local")
	}
	if got.WorktreePath != "" {
		t.Errorf("WorktreePath after reset = %q, want empty", got.WorktreePath)
	}
}

// ---------------------------------------------------------------------------
// GetExpiredSessionWorktrees tests
// ---------------------------------------------------------------------------

func TestGetExpiredSessionWorktrees(t *testing.T) {
	s := newTestDB(t)

	// Create three sessions, two with worktree paths
	sessA, _ := s.CreateSession(CreateSessionRequest{Name: "wt-a"})
	sessB, _ := s.CreateSession(CreateSessionRequest{Name: "wt-b"})
	sessC, _ := s.CreateSession(CreateSessionRequest{Name: "no-wt"})

	_ = s.UpdateSessionWorktree(sessA.ID, "worktree", "/tmp/worktrees/aaaaaaaa")
	_ = s.UpdateSessionWorktree(sessB.ID, "worktree", "/tmp/worktrees/bbbbbbbb")
	// sessC stays local, no worktree path

	// Soft-delete all three
	pastTime := time.Now().Add(-8 * 24 * time.Hour)
	for _, id := range []string{sessA.ID, sessB.ID, sessC.ID} {
		if _, err := s.db.Exec(`UPDATE sessions SET deleted_at = ? WHERE id = ?`, pastTime, id); err != nil {
			t.Fatalf("set deleted_at: %v", err)
		}
	}

	// Cutoff one week ago — all deleted sessions are older than this
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	paths, err := s.GetExpiredSessionWorktrees(cutoff)
	if err != nil {
		t.Fatalf("GetExpiredSessionWorktrees: %v", err)
	}

	if len(paths) != 2 {
		t.Fatalf("got %d paths, want 2: %v", len(paths), paths)
	}
	pathSet := map[string]bool{}
	for _, p := range paths {
		pathSet[p] = true
	}
	if !pathSet["/tmp/worktrees/aaaaaaaa"] {
		t.Errorf("path aaaaaaaa missing from results: %v", paths)
	}
	if !pathSet["/tmp/worktrees/bbbbbbbb"] {
		t.Errorf("path bbbbbbbb missing from results: %v", paths)
	}

	// Sessions deleted recently (within cutoff) should not appear
	sessD, _ := s.CreateSession(CreateSessionRequest{Name: "wt-d"})
	_ = s.UpdateSessionWorktree(sessD.ID, "worktree", "/tmp/worktrees/dddddddd")
	recentTime := time.Now().Add(-1 * time.Hour)
	if _, err := s.db.Exec(`UPDATE sessions SET deleted_at = ? WHERE id = ?`, recentTime, sessD.ID); err != nil {
		t.Fatalf("set recent deleted_at: %v", err)
	}

	paths, err = s.GetExpiredSessionWorktrees(cutoff)
	if err != nil {
		t.Fatalf("GetExpiredSessionWorktrees after adding recent: %v", err)
	}
	// Should still be 2 — sessD is too recent
	if len(paths) != 2 {
		t.Fatalf("got %d paths after adding recent, want 2: %v", len(paths), paths)
	}
}

func TestGetExpiredSessionWorktrees_EmptyWhenNone(t *testing.T) {
	s := newTestDB(t)
	paths, err := s.GetExpiredSessionWorktrees(time.Now())
	if err != nil {
		t.Fatalf("GetExpiredSessionWorktrees: %v", err)
	}
	if len(paths) != 0 {
		t.Errorf("expected empty, got %v", paths)
	}
}

// ---------------------------------------------------------------------------
// WorktreeMode persists across ListSessions / scanSession
// ---------------------------------------------------------------------------

func TestWorktreeModeInListSessions(t *testing.T) {
	s := newTestDB(t)

	sess, _ := s.CreateSession(CreateSessionRequest{Name: "list-test"})
	_ = s.UpdateSessionWorktree(sess.ID, "worktree", "/tmp/worktrees/listtest")

	sessions, err := s.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	var found *Session
	for i := range sessions {
		if sessions[i].ID == sess.ID {
			found = &sessions[i]
			break
		}
	}
	if found == nil {
		t.Fatal("session not found in ListSessions result")
	}
	if found.WorktreeMode != "worktree" {
		t.Errorf("WorktreeMode = %q, want %q", found.WorktreeMode, "worktree")
	}
	if found.WorktreePath != "/tmp/worktrees/listtest" {
		t.Errorf("WorktreePath = %q, want %q", found.WorktreePath, "/tmp/worktrees/listtest")
	}
}

func TestDefaultWorktreeMode(t *testing.T) {
	s := newTestDB(t)
	sess, _ := s.CreateSession(CreateSessionRequest{Name: "default-mode"})

	got, err := s.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	// scanSession normalises empty string to "local"
	if got.WorktreeMode != "local" {
		t.Errorf("WorktreeMode = %q, want %q", got.WorktreeMode, "local")
	}
}
