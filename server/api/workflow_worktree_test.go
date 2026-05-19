package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/esonic/nexbox/store"
)

// ---------------------------------------------------------------------------
// Helpers for orchestrator + worktree tests
// ---------------------------------------------------------------------------

// resetActiveRuns saves and clears the package-level activeRuns map for the
// duration of a test, restoring it on cleanup. Tests that mutate activeRuns
// must call this so they don't leak state across runs.
func resetActiveRuns(t *testing.T) {
	t.Helper()
	activeRunsMu.Lock()
	saved := activeRuns
	activeRuns = make(map[string]*workflowRun)
	activeRunsMu.Unlock()
	t.Cleanup(func() {
		activeRunsMu.Lock()
		activeRuns = saved
		activeRunsMu.Unlock()
	})
}

// initGitRepo creates a minimal git repo at dir and returns its path. The repo
// has one commit so HEAD is valid (required for `git worktree add`).
func initGitRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	mustGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mustGit("init")
	mustGit("config", "user.email", "test@test.com")
	mustGit("config", "user.name", "Test")
	mustGit("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustGit("add", ".")
	mustGit("commit", "-m", "init")
	return dir
}

// newOrchestratorTestServer builds a Server with everything orchestration code
// needs to run a pass-through workflow (no Agent / SessionManager required as
// long as the workflow has no AI nodes).
func newOrchestratorTestServer(t *testing.T, dataDir string) *Server {
	t.Helper()
	dbDir := filepath.Join(dataDir, "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatalf("mkdir dbdir: %v", err)
	}
	db, err := store.New(dbDir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Server{
		Store:   store.NewSQLiteStore(db),
		Hub:     NewChatHub(),
		DataDir: dataDir,
	}
}

// startNodeWorkflow creates a workflow whose only node is a single "start" node.
// runWorkflow processes "start" as a pass-through node so the run completes
// without needing any agent.
func startNodeWorkflow(t *testing.T, s *Server, projectID, name string) *store.Workflow {
	t.Helper()
	def := store.WorkflowDefinition{
		Nodes: []store.WorkflowNode{makeNode("start1", "start")},
	}
	defBytes, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal def: %v", err)
	}
	wf, err := s.Store.CreateWorkflow(projectID, name, "", "test-user", defBytes)
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	return wf
}

// waitForExecutionStatus polls the execution record until it reaches a terminal
// state or the deadline is exceeded.
func waitForExecutionStatus(t *testing.T, s *Server, execID string, want string) *store.WorkflowExecution {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e, err := s.Store.GetExecution(execID)
		if err == nil && e != nil && e.Status == want {
			return e
		}
		time.Sleep(20 * time.Millisecond)
	}
	e, _ := s.Store.GetExecution(execID)
	if e == nil {
		t.Fatalf("execution %s never appeared", execID)
	}
	t.Fatalf("execution %s status = %q, want %q (last: %+v)", execID, e.Status, want, e)
	return nil
}

// ---------------------------------------------------------------------------
// Schema migration: new columns are present
// ---------------------------------------------------------------------------

func TestSchemaMigration_WorktreeColumns(t *testing.T) {
	base := testTempDir(t)
	s := newOrchestratorTestServer(t, base)

	wf, err := s.Store.CreateWorkflow("p1", "test", "desc", "user", []byte(`{"nodes":[],"edges":[]}`))
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if wf.WorktreeMode != "local" {
		t.Errorf("default WorktreeMode = %q, want %q", wf.WorktreeMode, "local")
	}

	exec, err := s.Store.CreateExecution(store.CreateExecutionRequest{
		WorkflowID: wf.ID, WorkflowVersion: 1, TriggeredBy: "test",
	})
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	if exec.WorktreePath != "" {
		t.Errorf("default WorktreePath = %q, want empty", exec.WorktreePath)
	}
}

// ---------------------------------------------------------------------------
// UpdateWorkflow: worktree_mode update + validation
// ---------------------------------------------------------------------------

func TestUpdateWorkflow_WorktreeModeRoundtrip(t *testing.T) {
	base := testTempDir(t)
	s := newOrchestratorTestServer(t, base)

	wf, _ := s.Store.CreateWorkflow("p1", "wf", "", "u", []byte(`{"nodes":[],"edges":[]}`))

	// Flip to worktree mode
	mode := "worktree"
	updated, err := s.Store.UpdateWorkflow(wf.ID, store.UpdateWorkflowRequest{WorktreeMode: &mode})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.WorktreeMode != "worktree" {
		t.Errorf("WorktreeMode = %q, want %q", updated.WorktreeMode, "worktree")
	}

	// And back to local
	mode = "local"
	updated, err = s.Store.UpdateWorkflow(wf.ID, store.UpdateWorkflowRequest{WorktreeMode: &mode})
	if err != nil {
		t.Fatalf("update back: %v", err)
	}
	if updated.WorktreeMode != "local" {
		t.Errorf("WorktreeMode = %q, want %q", updated.WorktreeMode, "local")
	}
}

func TestUpdateWorkflow_InvalidWorktreeModeRejected(t *testing.T) {
	base := testTempDir(t)
	s := newOrchestratorTestServer(t, base)

	wf, _ := s.Store.CreateWorkflow("p1", "wf", "", "u", []byte(`{"nodes":[],"edges":[]}`))

	bad := "shared"
	_, err := s.Store.UpdateWorkflow(wf.ID, store.UpdateWorkflowRequest{WorktreeMode: &bad})
	if err == nil {
		t.Fatal("expected validation error for invalid worktree_mode")
	}
	if !store.IsValidationError(err) {
		t.Errorf("expected ValidationError, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// createWorkflowRunWorktree: actually shells out to git
// ---------------------------------------------------------------------------

func TestCreateWorkflowRunWorktree_Success(t *testing.T) {
	base := testTempDir(t)
	repoDir := initGitRepo(t, filepath.Join(base, "repo"))
	s := newOrchestratorTestServer(t, base)

	proj, err := s.Store.CreateProject(store.CreateProjectRequest{Name: "p", GitRepoPath: repoDir})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	const execID = "abcd1234deadbeef"
	path, err := s.createWorkflowRunWorktree(execID, proj.ID)
	if err != nil {
		t.Fatalf("createWorkflowRunWorktree: %v", err)
	}

	wantSuffix := filepath.Join("worktrees", "wfrun-abcd1234")
	if !strings.HasSuffix(path, wantSuffix) {
		t.Errorf("worktree path = %q, want suffix %q", path, wantSuffix)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("worktree dir not created: %v", err)
	}

	// Verify the branch was created.
	cmd := exec.Command("git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "wfrun-abcd1234" {
		t.Errorf("branch = %q, want %q", got, "wfrun-abcd1234")
	}
}

func TestCreateWorkflowRunWorktree_NoGitRepo(t *testing.T) {
	base := testTempDir(t)
	s := newOrchestratorTestServer(t, base)

	// Project with empty GitRepoPath.
	proj, err := s.Store.CreateProject(store.CreateProjectRequest{Name: "p", GitRepoPath: ""})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	_, err = s.createWorkflowRunWorktree("abcd1234", proj.ID)
	if err == nil {
		t.Fatal("expected error for project without git repo")
	}
}

func TestCreateWorkflowRunWorktree_NotAGitRepo(t *testing.T) {
	base := testTempDir(t)
	notRepo := filepath.Join(base, "plain")
	if err := os.MkdirAll(notRepo, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s := newOrchestratorTestServer(t, base)
	proj, _ := s.Store.CreateProject(store.CreateProjectRequest{Name: "p", GitRepoPath: notRepo})

	_, err := s.createWorkflowRunWorktree("abcd1234", proj.ID)
	if err == nil {
		t.Fatal("expected error when project path is not a git repo")
	}
}

// ---------------------------------------------------------------------------
// Execute concurrency: lock skipped only in worktree mode
// ---------------------------------------------------------------------------

// TestExecute_LocalModeRejectsConcurrent injects a fake active run for the
// workflow and verifies the second Execute call is rejected.
func TestExecute_LocalModeRejectsConcurrent(t *testing.T) {
	resetActiveRuns(t)

	base := testTempDir(t)
	s := newOrchestratorTestServer(t, base)

	proj, _ := s.Store.CreateProject(store.CreateProjectRequest{Name: "p", GitRepoPath: ""})
	wf := startNodeWorkflow(t, s, proj.ID, "wf-local")

	// Pre-register a fake run for this workflow so the concurrency check trips.
	activeRunsMu.Lock()
	activeRuns["fake-exec"] = &workflowRun{executionID: "fake-exec", workflowID: wf.ID}
	activeRunsMu.Unlock()

	_, err := s.Execute(ExecuteWorkflowRequest{WorkflowID: wf.ID, ProjectID: proj.ID})
	if err == nil {
		t.Fatal("expected concurrency error, got nil")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("wrong error: %v", err)
	}
}

// TestExecute_WorktreeModeSkipsConcurrencyCheck verifies that when the
// workflow has worktree_mode=worktree, Execute does NOT reject a new run
// even if another run is already active for the same workflow.
func TestExecute_WorktreeModeSkipsConcurrencyCheck(t *testing.T) {
	resetActiveRuns(t)

	base := testTempDir(t)
	repoDir := initGitRepo(t, filepath.Join(base, "repo"))
	s := newOrchestratorTestServer(t, base)

	proj, _ := s.Store.CreateProject(store.CreateProjectRequest{Name: "p", GitRepoPath: repoDir})
	wf := startNodeWorkflow(t, s, proj.ID, "wf-worktree")

	mode := "worktree"
	if _, err := s.Store.UpdateWorkflow(wf.ID, store.UpdateWorkflowRequest{WorktreeMode: &mode}); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Pre-register a fake run for the same workflow.
	activeRunsMu.Lock()
	activeRuns["fake-exec"] = &workflowRun{executionID: "fake-exec", workflowID: wf.ID}
	activeRunsMu.Unlock()

	exec, err := s.Execute(ExecuteWorkflowRequest{WorkflowID: wf.ID, ProjectID: proj.ID})
	if err != nil {
		t.Fatalf("Execute should have skipped concurrency check, got: %v", err)
	}
	if exec == nil {
		t.Fatal("expected execution record")
	}

	// Wait for the actual run to complete so it doesn't leak goroutines.
	final := waitForExecutionStatus(t, s, exec.ID, "completed")
	if final.WorktreePath == "" {
		t.Error("worktree_path was not recorded on the execution")
	}
}

// ---------------------------------------------------------------------------
// End-to-end: two parallel runs each get their own worktree
// ---------------------------------------------------------------------------

func TestExecute_WorktreeModeAllowsTwoConcurrentRuns(t *testing.T) {
	resetActiveRuns(t)

	base := testTempDir(t)
	repoDir := initGitRepo(t, filepath.Join(base, "repo"))
	s := newOrchestratorTestServer(t, base)

	proj, _ := s.Store.CreateProject(store.CreateProjectRequest{Name: "p", GitRepoPath: repoDir})
	wf := startNodeWorkflow(t, s, proj.ID, "wf-parallel")

	mode := "worktree"
	if _, err := s.Store.UpdateWorkflow(wf.ID, store.UpdateWorkflowRequest{WorktreeMode: &mode}); err != nil {
		t.Fatalf("update: %v", err)
	}

	exec1, err := s.Execute(ExecuteWorkflowRequest{WorkflowID: wf.ID, ProjectID: proj.ID})
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	exec2, err := s.Execute(ExecuteWorkflowRequest{WorkflowID: wf.ID, ProjectID: proj.ID})
	if err != nil {
		t.Fatalf("second Execute (should be allowed in worktree mode): %v", err)
	}
	if exec1.ID == exec2.ID {
		t.Fatal("expected two distinct executions")
	}

	e1 := waitForExecutionStatus(t, s, exec1.ID, "completed")
	e2 := waitForExecutionStatus(t, s, exec2.ID, "completed")

	if e1.WorktreePath == "" || e2.WorktreePath == "" {
		t.Fatalf("worktree paths missing: e1=%q e2=%q", e1.WorktreePath, e2.WorktreePath)
	}
	if e1.WorktreePath == e2.WorktreePath {
		t.Errorf("expected distinct worktree paths, both = %q", e1.WorktreePath)
	}
	for _, p := range []string{e1.WorktreePath, e2.WorktreePath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("worktree dir %q missing: %v", p, err)
		}
	}

	// Each run produced a distinct branch named after its execution short ID.
	for _, e := range []*store.WorkflowExecution{e1, e2} {
		cmd := exec.Command("git", "-C", e.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD")
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("git rev-parse for %s: %v", e.ID, err)
			continue
		}
		branch := strings.TrimSpace(string(out))
		wantPrefix := "wfrun-"
		if !strings.HasPrefix(branch, wantPrefix) {
			t.Errorf("branch %q missing prefix %q", branch, wantPrefix)
		}
	}
}

// TestExecute_WorktreeFailureMarksRunFailed verifies that when the workflow's
// worktree_mode is "worktree" but the underlying repo is not a git repo,
// runWorkflow marks the execution as failed instead of silently proceeding.
func TestExecute_WorktreeFailureMarksRunFailed(t *testing.T) {
	resetActiveRuns(t)

	base := testTempDir(t)
	notRepo := filepath.Join(base, "plain")
	if err := os.MkdirAll(notRepo, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s := newOrchestratorTestServer(t, base)

	proj, _ := s.Store.CreateProject(store.CreateProjectRequest{Name: "p", GitRepoPath: notRepo})
	wf := startNodeWorkflow(t, s, proj.ID, "wf-bad")

	mode := "worktree"
	if _, err := s.Store.UpdateWorkflow(wf.ID, store.UpdateWorkflowRequest{WorktreeMode: &mode}); err != nil {
		t.Fatalf("update: %v", err)
	}

	exec, err := s.Execute(ExecuteWorkflowRequest{WorkflowID: wf.ID, ProjectID: proj.ID})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	final := waitForExecutionStatus(t, s, exec.ID, "failed")
	if !strings.Contains(final.Error, "worktree") {
		t.Errorf("expected worktree-related error message, got: %q", final.Error)
	}
}

// ---------------------------------------------------------------------------
// createWorkflowSession: pins the new session to the run's worktree path
// ---------------------------------------------------------------------------

func TestCreateWorkflowSession_PinsToRunWorktree(t *testing.T) {
	resetActiveRuns(t)

	base := testTempDir(t)
	s := newOrchestratorTestServer(t, base)

	proj, _ := s.Store.CreateProject(store.CreateProjectRequest{Name: "p"})

	// Simulate a workflow run that already has its worktree populated.
	run := &workflowRun{
		executionID:  "exec-test",
		workflowID:   "wf-test",
		projectID:    proj.ID,
		worktreePath: "/tmp/fake/wfrun-shared",
	}

	sid, err := s.createWorkflowSession(run, "WF[abc] test — start", "test-agent")
	if err != nil {
		t.Fatalf("createWorkflowSession: %v", err)
	}

	sess, err := s.Store.GetSession(sid)
	if err != nil || sess == nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.WorktreeMode != "worktree" {
		t.Errorf("WorktreeMode = %q, want %q", sess.WorktreeMode, "worktree")
	}
	if sess.WorktreePath != "/tmp/fake/wfrun-shared" {
		t.Errorf("WorktreePath = %q, want /tmp/fake/wfrun-shared", sess.WorktreePath)
	}
}

// TestCompositeStore_WorktreeModePersistsViaLocalTable verifies that when the
// store is a CompositeStore (the real production wrapping), an UpdateWorkflow
// with worktree_mode is persisted in the local override table rather than
// forwarded to the primary store. This protects against the cloud-mode case
// where the primary store is a remote server that may not know the field.
func TestCompositeStore_WorktreeModePersistsViaLocalTable(t *testing.T) {
	base := testTempDir(t)
	dbDir := filepath.Join(base, "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	db, err := store.New(dbDir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	local := store.NewSQLiteStore(db)
	composite := store.NewCompositeStore(local, local) // local-mode wiring (primary == local)

	wf, err := composite.CreateWorkflow("p1", "wf", "", "u", []byte(`{"nodes":[],"edges":[]}`))
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}

	mode := "worktree"
	updated, err := composite.UpdateWorkflow(wf.ID, store.UpdateWorkflowRequest{WorktreeMode: &mode})
	if err != nil {
		t.Fatalf("UpdateWorkflow: %v", err)
	}
	if updated.WorktreeMode != "worktree" {
		t.Errorf("after update: WorktreeMode = %q, want %q", updated.WorktreeMode, "worktree")
	}

	// Local override table should be the source of truth — read it directly.
	if got := local.GetLocalWorktreeMode(wf.ID); got != "worktree" {
		t.Errorf("local override = %q, want %q", got, "worktree")
	}

	// Reload via GetWorkflow — simulates what happens after a page refresh.
	reloaded, err := composite.GetWorkflow(wf.ID)
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if reloaded.WorktreeMode != "worktree" {
		t.Errorf("after reload: WorktreeMode = %q, want %q", reloaded.WorktreeMode, "worktree")
	}

	// And via ListWorkflows.
	listed, err := composite.ListWorkflows("p1")
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	if len(listed) == 0 {
		t.Fatal("expected workflow in list")
	}
	if listed[0].WorktreeMode != "worktree" {
		t.Errorf("list: WorktreeMode = %q, want %q", listed[0].WorktreeMode, "worktree")
	}

	// Toggling back to local persists too.
	mode = "local"
	if _, err := composite.UpdateWorkflow(wf.ID, store.UpdateWorkflowRequest{WorktreeMode: &mode}); err != nil {
		t.Fatalf("toggle off: %v", err)
	}
	reloaded, _ = composite.GetWorkflow(wf.ID)
	if reloaded.WorktreeMode != "local" {
		t.Errorf("after toggle off: WorktreeMode = %q, want %q", reloaded.WorktreeMode, "local")
	}
}

func TestCreateWorkflowSession_LeavesLocalModeAlone(t *testing.T) {
	resetActiveRuns(t)

	base := testTempDir(t)
	s := newOrchestratorTestServer(t, base)

	proj, _ := s.Store.CreateProject(store.CreateProjectRequest{Name: "p"})

	// Run with no worktree path → local mode workflow.
	run := &workflowRun{
		executionID: "exec-test",
		workflowID:  "wf-test",
		projectID:   proj.ID,
	}

	sid, err := s.createWorkflowSession(run, "local-only", "test-agent")
	if err != nil {
		t.Fatalf("createWorkflowSession: %v", err)
	}

	sess, _ := s.Store.GetSession(sid)
	if sess.WorktreeMode != "local" {
		t.Errorf("WorktreeMode = %q, want %q (default)", sess.WorktreeMode, "local")
	}
	if sess.WorktreePath != "" {
		t.Errorf("WorktreePath = %q, want empty", sess.WorktreePath)
	}
}
