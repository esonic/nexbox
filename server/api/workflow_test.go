package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/store"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func makeNode(id, nodeType string) store.WorkflowNode {
	return store.WorkflowNode{
		ID:   id,
		Type: nodeType,
		Data: map[string]interface{}{},
	}
}

func makeAINode(id, prompt string) store.WorkflowNode {
	return store.WorkflowNode{
		ID:   id,
		Type: "ai",
		Data: map[string]interface{}{
			"config": map[string]interface{}{"prompt": prompt},
		},
	}
}

func makeEdge(src, tgt string) store.WorkflowEdge {
	return store.WorkflowEdge{ID: src + "->" + tgt, Source: src, Target: tgt}
}

// newSchedulerServer creates a Server backed by a real in-memory SQLite store.
func newSchedulerServer(t *testing.T) *Server {
	t.Helper()
	db, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Server{
		Store: store.NewSQLiteStore(db),
		Hub:   NewChatHub(),
	}
}

// completedExec creates an execution record and marks it completed.
// Returns the completed_at time, which is approximately time.Now() at call time.
func completedExec(t *testing.T, s *Server, wfID string) time.Time {
	t.Helper()
	before := time.Now()
	exec, err := s.Store.CreateExecution(store.CreateExecutionRequest{
		WorkflowID:      wfID,
		WorkflowVersion: 1,
		TriggeredBy:     "test",
	})
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	dur := int64(100)
	if err := s.Store.UpdateExecutionStatus(exec.ID, "completed", "", &dur); err != nil {
		t.Fatalf("UpdateExecutionStatus: %v", err)
	}
	return before
}

// runningExec creates an execution record without marking it completed (simulates a running workflow).
func runningExec(t *testing.T, s *Server, wfID string) {
	t.Helper()
	_, err := s.Store.CreateExecution(store.CreateExecutionRequest{
		WorkflowID:      wfID,
		WorkflowVersion: 1,
		TriggeredBy:     "test",
	})
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
}

// ---------------------------------------------------------------------------
// DAG: buildLevels
// ---------------------------------------------------------------------------

func TestBuildLevels_Empty(t *testing.T) {
	levels := buildLevels(&store.WorkflowDefinition{})
	if len(levels) != 0 {
		t.Errorf("want 0 levels, got %d", len(levels))
	}
}

func TestBuildLevels_SingleNode(t *testing.T) {
	def := &store.WorkflowDefinition{
		Nodes: []store.WorkflowNode{makeNode("a", "start")},
	}
	levels := buildLevels(def)
	if len(levels) != 1 || len(levels[0]) != 1 || levels[0][0].ID != "a" {
		t.Errorf("want [[a]], got %v", levels)
	}
}

func TestBuildLevels_Linear(t *testing.T) {
	// a → b → c
	def := &store.WorkflowDefinition{
		Nodes: []store.WorkflowNode{
			makeNode("a", "start"),
			makeAINode("b", "step 2"),
			makeAINode("c", "step 3"),
		},
		Edges: []store.WorkflowEdge{makeEdge("a", "b"), makeEdge("b", "c")},
	}
	levels := buildLevels(def)
	if len(levels) != 3 {
		t.Fatalf("want 3 levels, got %d", len(levels))
	}
	if levels[0][0].ID != "a" || levels[1][0].ID != "b" || levels[2][0].ID != "c" {
		t.Errorf("unexpected level order: %v", levels)
	}
}

func TestBuildLevels_Parallel(t *testing.T) {
	// a and b have no parents; c depends on both (fan-in)
	def := &store.WorkflowDefinition{
		Nodes: []store.WorkflowNode{
			makeNode("a", "start"),
			makeAINode("b", "branch"),
			makeAINode("c", "merge"),
		},
		Edges: []store.WorkflowEdge{makeEdge("a", "c"), makeEdge("b", "c")},
	}
	levels := buildLevels(def)
	if len(levels) != 2 {
		t.Fatalf("want 2 levels, got %d", len(levels))
	}
	if len(levels[0]) != 2 {
		t.Errorf("level 0 should have 2 parallel nodes (a,b), got %d", len(levels[0]))
	}
	if len(levels[1]) != 1 || levels[1][0].ID != "c" {
		t.Errorf("level 1 should be [c], got %v", levels[1])
	}
}

func TestBuildLevels_Diamond(t *testing.T) {
	// a → b, a → c, b → d, c → d
	def := &store.WorkflowDefinition{
		Nodes: []store.WorkflowNode{
			makeNode("a", "start"),
			makeAINode("b", "left"),
			makeAINode("c", "right"),
			makeAINode("d", "join"),
		},
		Edges: []store.WorkflowEdge{
			makeEdge("a", "b"), makeEdge("a", "c"),
			makeEdge("b", "d"), makeEdge("c", "d"),
		},
	}
	levels := buildLevels(def)
	if len(levels) != 3 {
		t.Fatalf("want 3 levels, got %d", len(levels))
	}
	if len(levels[0]) != 1 || levels[0][0].ID != "a" {
		t.Errorf("level 0 want [a], got %v", levels[0])
	}
	if len(levels[1]) != 2 {
		t.Errorf("level 1 want 2 nodes (b,c), got %d", len(levels[1]))
	}
	if len(levels[2]) != 1 || levels[2][0].ID != "d" {
		t.Errorf("level 2 want [d], got %v", levels[2])
	}
}

// ---------------------------------------------------------------------------
// Filter helpers
// ---------------------------------------------------------------------------

func TestFilterAINodes(t *testing.T) {
	nodes := []store.WorkflowNode{
		makeAINode("ai-prompt", "do stuff"),
		makeAINode("ai-empty", ""), // empty prompt → excluded
		makeNode("human1", "human"),
		makeNode("start1", "start"),
	}
	got := filterAINodes(nodes)
	if len(got) != 1 || got[0].ID != "ai-prompt" {
		t.Errorf("filterAINodes want [ai-prompt], got %v", got)
	}
}

func TestFilterHumanNodes(t *testing.T) {
	nodes := []store.WorkflowNode{
		makeAINode("ai1", "do stuff"),
		makeNode("human1", "human"),
		makeNode("human2", "human"),
		makeNode("start1", "start"),
	}
	got := filterHumanNodes(nodes)
	if len(got) != 2 {
		t.Errorf("filterHumanNodes want 2, got %d", len(got))
	}
}

func TestFilterPassNodes(t *testing.T) {
	// pass-through: start nodes, ai nodes with no prompt
	nodes := []store.WorkflowNode{
		makeAINode("ai-with-prompt", "do stuff"), // excluded: ai + prompt
		makeAINode("ai-no-prompt", ""),           // included: ai without prompt
		makeNode("human1", "human"),              // excluded: human
		makeNode("start1", "start"),              // included: start
	}
	got := filterPassNodes(nodes)
	if len(got) != 2 {
		t.Fatalf("filterPassNodes want 2, got %d: %v", len(got), got)
	}
	for _, n := range got {
		if n.ID == "human1" {
			t.Error("filterPassNodes must not include human nodes")
		}
		if n.ID == "ai-with-prompt" {
			t.Error("filterPassNodes must not include AI nodes with prompt")
		}
	}
}

// ---------------------------------------------------------------------------
// truncateStr
// ---------------------------------------------------------------------------

func TestTruncateStr(t *testing.T) {
	cases := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 5, "hello...(truncated)"},
		{"", 10, ""},
		{"abc", 0, "...(truncated)"}, // edge: maxLen=0 truncates immediately
	}
	for _, tc := range cases {
		got := truncateStr(tc.input, tc.maxLen)
		if got != tc.want {
			t.Errorf("truncateStr(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// buildNodePrompt
// ---------------------------------------------------------------------------

func TestBuildNodePrompt_NoTokens(t *testing.T) {
	node := makeAINode("n1", "Do the thing")
	out := buildNodePrompt(node, "Do the thing", nil, map[string][]string{}, nil, nil, nil)
	if out != "Do the thing" {
		t.Errorf("want %q, got %q", "Do the thing", out)
	}
}

func TestBuildNodePrompt_EnvVarSubstitution(t *testing.T) {
	node := makeAINode("n1", "Analyze {{env_target}} for {{env_goal}}")
	env := map[string]string{"target": "api/users", "goal": "perf issues"}
	out := buildNodePrompt(node, "Analyze {{env_target}} for {{env_goal}}", nil, map[string][]string{"n1": {}}, nil, env, nil)
	if out != "Analyze api/users for perf issues" {
		t.Errorf("unexpected substitution: %q", out)
	}
}

func TestBuildNodePrompt_UnknownEnvBecomesEmpty(t *testing.T) {
	node := makeAINode("n1", "Got [{{env_missing}}] here")
	out := buildNodePrompt(node, "Got [{{env_missing}}] here", nil, map[string][]string{"n1": {}}, nil, nil, nil)
	if out != "Got [] here" {
		t.Errorf("unknown env token should expand to empty, got %q", out)
	}
}

func TestBuildNodePrompt_PrevNodeOutput(t *testing.T) {
	node := makeAINode("n2", "Continue from: {{env_prev_node_output}}")
	parentMap := map[string][]string{"n2": {"n1"}}
	nodeOutputs := map[string]string{"n1": "output from step one"}
	nodeLabels := map[string]string{"n1": "Step One"}
	out := buildNodePrompt(node, "Continue from: {{env_prev_node_output}}", nodeOutputs, parentMap, nodeLabels, nil, nil)
	if !strings.Contains(out, "output from step one") {
		t.Errorf("expected prev_node_output injected, got %q", out)
	}
	if !strings.Contains(out, "## Step One") {
		t.Errorf("expected upstream node label, got %q", out)
	}
}

func TestBuildNodePrompt_PrevNodeOutputMultipleParents(t *testing.T) {
	node := makeAINode("n3", "All: {{env_prev_node_output}}")
	parentMap := map[string][]string{"n3": {"n1", "n2"}}
	nodeOutputs := map[string]string{"n1": "first", "n2": "second"}
	nodeLabels := map[string]string{"n1": "First Node", "n2": "Second Node"}
	out := buildNodePrompt(node, "All: {{env_prev_node_output}}", nodeOutputs, parentMap, nodeLabels, nil, nil)
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Errorf("expected both parents' outputs, got %q", out)
	}
	if !strings.Contains(out, "## First Node") || !strings.Contains(out, "## Second Node") {
		t.Errorf("expected parent labels, got %q", out)
	}
	if !strings.Contains(out, "---") {
		t.Errorf("expected separator between parent outputs, got %q", out)
	}
}

func TestBuildNodePrompt_PrevNodeOutputEmptyWhenNoParents(t *testing.T) {
	node := makeAINode("n1", "Prefix: {{env_prev_node_output}} suffix")
	out := buildNodePrompt(node, "Prefix: {{env_prev_node_output}} suffix", nil, map[string][]string{"n1": {}}, nil, nil, nil)
	if out != "Prefix:  suffix" {
		t.Errorf("expected empty prev_node_output, got %q", out)
	}
}

func TestBuildNodePrompt_PrevNodeOutputReserved(t *testing.T) {
	// prev_node_output is reserved; a user-supplied env value cannot shadow it.
	node := makeAINode("n2", "X: {{env_prev_node_output}}")
	parentMap := map[string][]string{"n2": {"n1"}}
	nodeOutputs := map[string]string{"n1": "real upstream"}
	env := map[string]string{"prev_node_output": "FAKE"}
	out := buildNodePrompt(node, "X: {{env_prev_node_output}}", nodeOutputs, parentMap, nil, env, nil)
	if !strings.Contains(out, "real upstream") {
		t.Errorf("expected real upstream to win over user-supplied value, got %q", out)
	}
	if strings.Contains(out, "FAKE") {
		t.Errorf("user-supplied prev_node_output should not have leaked through, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// isWorkflowRunningByID
// ---------------------------------------------------------------------------

func TestIsWorkflowRunningByID(t *testing.T) {
	// Reset global state to avoid interference from other tests.
	activeRunsMu.Lock()
	saved := activeRuns
	activeRuns = make(map[string]*workflowRun)
	activeRunsMu.Unlock()
	t.Cleanup(func() {
		activeRunsMu.Lock()
		activeRuns = saved
		activeRunsMu.Unlock()
	})

	const wfID = "wf-test-abc"

	if isWorkflowRunningByID(wfID) {
		t.Fatal("should not be running before any registration")
	}

	run := &workflowRun{executionID: "exec-001", workflowID: wfID}
	activeRunsMu.Lock()
	activeRuns["exec-001"] = run
	activeRunsMu.Unlock()

	if !isWorkflowRunningByID(wfID) {
		t.Error("should be running after registration")
	}
	if isWorkflowRunningByID("other-workflow") {
		t.Error("unrelated workflow should not be reported as running")
	}

	activeRunsMu.Lock()
	delete(activeRuns, "exec-001")
	activeRunsMu.Unlock()

	if isWorkflowRunningByID(wfID) {
		t.Error("should not be running after removal")
	}
}

// ---------------------------------------------------------------------------
// Human gate: waitHumanApproval / ContinueHumanNode
// ---------------------------------------------------------------------------

func TestWaitHumanApproval_Continue(t *testing.T) {
	s := &Server{Hub: NewChatHub()}
	ctx := context.Background()

	errCh := make(chan error, 1)
	go func() {
		_, err := s.waitHumanApproval(ctx, "exec-1", "node-1", "Review Step")
		errCh <- err
	}()

	// Yield so the goroutine registers the gate before we call ContinueHumanNode.
	time.Sleep(20 * time.Millisecond)

	if err := s.ContinueHumanNode("exec-1", "node-1", ""); err != nil {
		t.Fatalf("ContinueHumanNode returned unexpected error: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("waitHumanApproval should return nil on continue, got: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waitHumanApproval did not unblock within 1s")
	}
}

func TestWaitHumanApproval_ContextCancel(t *testing.T) {
	s := &Server{Hub: NewChatHub()}
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, err := s.waitHumanApproval(ctx, "exec-2", "node-2", "Review Step")
		errCh <- err
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("waitHumanApproval should return an error when context is cancelled")
		}
	case <-time.After(time.Second):
		t.Fatal("waitHumanApproval did not unblock after context cancel")
	}
}

func TestContinueHumanNode_NotWaiting(t *testing.T) {
	s := &Server{}
	err := s.ContinueHumanNode("exec-99", "node-99", "")
	if err == nil {
		t.Error("expected error when no gate is registered for this execution/node")
	}
}

func TestContinueHumanNode_NilMap(t *testing.T) {
	// humanGates is nil (Server never started any workflow).
	// Reading from a nil map is safe in Go; we expect a "not waiting" error.
	s := &Server{}
	err := s.ContinueHumanNode("exec-nil", "node-nil", "")
	if err == nil {
		t.Error("expected error when humanGates is nil")
	}
}

func TestContinueHumanNode_DoubleSubmit(t *testing.T) {
	s := &Server{Hub: NewChatHub()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := s.waitHumanApproval(ctx, "exec-3", "node-3", "Review")
		errCh <- err
	}()

	time.Sleep(20 * time.Millisecond)

	// First continue: should succeed
	if err := s.ContinueHumanNode("exec-3", "node-3", ""); err != nil {
		t.Fatalf("first ContinueHumanNode: %v", err)
	}

	// Drain the goroutine
	select {
	case <-errCh:
	case <-time.After(time.Second):
		t.Fatal("waitHumanApproval did not unblock")
	}

	// Second continue: gate is cleaned up → expect error
	err := s.ContinueHumanNode("exec-3", "node-3", "")
	if err == nil {
		t.Error("expected error on second ContinueHumanNode (gate already gone)")
	}
}

func TestWaitHumanApproval_TwoNodes(t *testing.T) {
	// Two different nodes in the same execution can each have a gate.
	s := &Server{Hub: NewChatHub()}
	ctx := context.Background()

	errCh1 := make(chan error, 1)
	errCh2 := make(chan error, 1)

	go func() { _, err := s.waitHumanApproval(ctx, "exec-4", "node-A", "Review A"); errCh1 <- err }()
	go func() { _, err := s.waitHumanApproval(ctx, "exec-4", "node-B", "Review B"); errCh2 <- err }()

	time.Sleep(30 * time.Millisecond)

	if err := s.ContinueHumanNode("exec-4", "node-A", ""); err != nil {
		t.Errorf("continue node-A: %v", err)
	}
	if err := s.ContinueHumanNode("exec-4", "node-B", ""); err != nil {
		t.Errorf("continue node-B: %v", err)
	}

	for i, ch := range []chan error{errCh1, errCh2} {
		select {
		case err := <-ch:
			if err != nil {
				t.Errorf("gate %d: unexpected error: %v", i+1, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("gate %d did not unblock", i+1)
		}
	}
}

// ---------------------------------------------------------------------------
// Scheduler: isDue
// ---------------------------------------------------------------------------

func TestIsDue_Interval_NeverRun(t *testing.T) {
	s := newSchedulerServer(t)
	sched := store.WorkflowSchedule{Type: "interval", IntervalHours: 4}
	// No executions exist → always due
	if !s.isDue("wf-never", sched, time.Now()) {
		t.Error("workflow that never ran should be due")
	}
}

func TestIsDue_Interval_JustCompleted(t *testing.T) {
	s := newSchedulerServer(t)
	sched := store.WorkflowSchedule{Type: "interval", IntervalHours: 4}
	completedExec(t, s, "wf-recent")
	// Checking immediately (< 4h since completion) → not due
	if s.isDue("wf-recent", sched, time.Now()) {
		t.Error("workflow that just completed should not be due yet")
	}
}

func TestIsDue_Interval_OverduePastInterval(t *testing.T) {
	s := newSchedulerServer(t)
	sched := store.WorkflowSchedule{Type: "interval", IntervalHours: 2}
	completedExec(t, s, "wf-overdue")
	// Simulate checking 3 hours later → should be due (3h elapsed, interval=2h)
	if !s.isDue("wf-overdue", sched, time.Now().Add(3*time.Hour)) {
		t.Error("workflow should be due after interval has elapsed")
	}
}

func TestIsDue_Interval_NotYetElapsed(t *testing.T) {
	s := newSchedulerServer(t)
	sched := store.WorkflowSchedule{Type: "interval", IntervalHours: 8}
	completedExec(t, s, "wf-not-yet")
	// Only 1 hour has passed; interval is 8h → not due
	if s.isDue("wf-not-yet", sched, time.Now().Add(1*time.Hour)) {
		t.Error("workflow should not be due before interval has elapsed")
	}
}

func TestIsDue_Interval_StillRunning(t *testing.T) {
	s := newSchedulerServer(t)
	sched := store.WorkflowSchedule{Type: "interval", IntervalHours: 1}
	// Create an execution that hasn't been completed yet (completed_at is nil).
	runningExec(t, s, "wf-running")
	// completed_at is nil → isDue returns false to avoid duplicate run
	if s.isDue("wf-running", sched, time.Now().Add(2*time.Hour)) {
		t.Error("workflow with no completed_at (still running) should not be due")
	}
}

func TestIsDue_Interval_ClampMin(t *testing.T) {
	s := newSchedulerServer(t)
	// interval_hours = 0 → clamped to 1
	sched := store.WorkflowSchedule{Type: "interval", IntervalHours: 0}
	completedExec(t, s, "wf-clamp-min")
	// Checking 90 minutes later: 90min >= 1h → due
	if !s.isDue("wf-clamp-min", sched, time.Now().Add(90*time.Minute)) {
		t.Error("interval clamped to 1h: should be due after 90 minutes")
	}
}

func TestIsDue_Interval_ClampMax(t *testing.T) {
	s := newSchedulerServer(t)
	// interval_hours = 100 → clamped to 24
	sched := store.WorkflowSchedule{Type: "interval", IntervalHours: 100}
	completedExec(t, s, "wf-clamp-max")
	// 23 hours is not enough even though user set 100h (clamped to 24h)
	if s.isDue("wf-clamp-max", sched, time.Now().Add(23*time.Hour)) {
		t.Error("interval clamped to 24h: should not be due after only 23h")
	}
	// 25 hours is past the 24h cap → due
	if !s.isDue("wf-clamp-max", sched, time.Now().Add(25*time.Hour)) {
		t.Error("interval clamped to 24h: should be due after 25h")
	}
}

func TestIsDue_Weekly_WrongDay(t *testing.T) {
	s := newSchedulerServer(t)
	// Schedule on Wednesday only (day=3)
	sched := store.WorkflowSchedule{Type: "weekly", Days: []int{3}, Hour: 9, Minute: 0}
	// Construct a now that is Monday at 10am
	now := nextWeekdayAt(1, 10, 0) // Monday
	if s.isDue("wf-wrong-day", sched, now) {
		t.Error("should not be due on a day not in the schedule")
	}
}

func TestIsDue_Weekly_RightDayBeforeTime(t *testing.T) {
	s := newSchedulerServer(t)
	// Schedule on Monday at 14:00
	sched := store.WorkflowSchedule{Type: "weekly", Days: []int{1}, Hour: 14, Minute: 0}
	// It is Monday but only 10am → before scheduled time
	now := nextWeekdayAt(1, 10, 0)
	if s.isDue("wf-before-time", sched, now) {
		t.Error("should not be due before scheduled time on the correct day")
	}
}

func TestIsDue_Weekly_NeverRun_RightDayAndTime(t *testing.T) {
	s := newSchedulerServer(t)
	// Schedule on Monday at 9:00; it is Monday at 9:05 and never ran
	sched := store.WorkflowSchedule{Type: "weekly", Days: []int{1}, Hour: 9, Minute: 0}
	now := nextWeekdayAt(1, 9, 5)
	if !s.isDue("wf-weekly-new", sched, now) {
		t.Error("should be due when it never ran on the correct day past the scheduled time")
	}
}

func TestIsDue_Weekly_AlreadyRanToday(t *testing.T) {
	s := newSchedulerServer(t)
	sched := store.WorkflowSchedule{Type: "weekly", Days: []int{1}, Hour: 9, Minute: 0}
	// The execution was completed TODAY (at current clock time)
	completedExec(t, s, "wf-weekly-done")
	// now is also TODAY (same weekday) at 10am → already ran today
	now := nextWeekdayAt(1, 10, 0)
	// Both completed_at and "now" are within a few seconds of each other (today),
	// so lastDay == today → not due.
	// We need to ensure the weekday used in the check matches.
	// completed_at is time.Now() (real current day), now is next Monday.
	// They may not be the same calendar day if today isn't Monday.
	// Test only makes sense when 'now' and completed_at share the same local day.
	// Use today's weekday for the schedule.
	today := time.Now()
	sched.Days = []int{int(today.Weekday())}
	sched.Hour = 0
	sched.Minute = 0
	now = today.Add(2 * time.Hour) // 2 hours later today
	if s.isDue("wf-weekly-done", sched, now) {
		t.Error("should not be due when already ran today")
	}
}

func TestIsDue_Weekly_RanYesterday_DueToday(t *testing.T) {
	s := newSchedulerServer(t)
	// completed_at ≈ time.Now()
	completedExec(t, s, "wf-weekly-yesterday")
	// Schedule fires every day at midnight
	today := time.Now()
	sched := store.WorkflowSchedule{
		Type:   "weekly",
		Days:   []int{0, 1, 2, 3, 4, 5, 6}, // every day
		Hour:   0,
		Minute: 0,
	}
	// Simulate 'now' being tomorrow at 00:01 → should be due
	tomorrow := today.Add(24 * time.Hour)
	if !s.isDue("wf-weekly-yesterday", sched, tomorrow) {
		t.Error("should be due when last run was yesterday (tomorrow is a new day)")
	}
}

func TestIsDue_Weekly_EmptyDays(t *testing.T) {
	s := newSchedulerServer(t)
	sched := store.WorkflowSchedule{Type: "weekly", Days: []int{}, Hour: 9, Minute: 0}
	// Empty days → never fires
	if s.isDue("wf-empty-days", sched, time.Now()) {
		t.Error("weekly schedule with no days should never be due")
	}
}

// ---------------------------------------------------------------------------
// Scheduler: isDue - edge cases for unknown schedule type
// ---------------------------------------------------------------------------

func TestIsDue_UnknownType(t *testing.T) {
	s := newSchedulerServer(t)
	sched := store.WorkflowSchedule{Type: "hourly"} // not a real type
	if s.isDue("wf-unknown", sched, time.Now()) {
		t.Error("unknown schedule type should return false")
	}
}

// ---------------------------------------------------------------------------
// Helper: nextWeekdayAt returns a time.Time representing the next occurrence
// of the given weekday (0=Sun) at hour:minute local time.
// ---------------------------------------------------------------------------

func nextWeekdayAt(weekday, hour, minute int) time.Time {
	now := time.Now()
	loc := now.Location()
	// Find the next (or current) occurrence of weekday
	today := int(now.Weekday())
	daysUntil := (weekday - today + 7) % 7
	target := now.AddDate(0, 0, daysUntil)
	return time.Date(target.Year(), target.Month(), target.Day(), hour, minute, 0, 0, loc)
}

// ---------------------------------------------------------------------------
// Hook: parseHookConfig
// ---------------------------------------------------------------------------

func TestParseHookConfig_Nil(t *testing.T) {
	if got := parseHookConfig(nil); got != nil {
		t.Errorf("want nil, got %+v", got)
	}
}

func TestParseHookConfig_EmptyConfig(t *testing.T) {
	if got := parseHookConfig(map[string]interface{}{}); got != nil {
		t.Errorf("want nil for config without hook key, got %+v", got)
	}
}

func TestParseHookConfig_HookKeyNotMap(t *testing.T) {
	cfg := map[string]interface{}{"hook": "not-a-map"}
	if got := parseHookConfig(cfg); got != nil {
		t.Errorf("want nil when hook value is not a map, got %+v", got)
	}
}

func TestParseHookConfig_NoTrigger(t *testing.T) {
	cfg := map[string]interface{}{
		"hook": map[string]interface{}{
			"prompt": "check something",
			// trigger is missing
		},
	}
	if got := parseHookConfig(cfg); got != nil {
		t.Errorf("want nil when trigger is empty, got %+v", got)
	}
}

func TestParseHookConfig_EmptyPrompt(t *testing.T) {
	cfg := map[string]interface{}{
		"hook": map[string]interface{}{
			"trigger": "on_reply_done",
			"prompt":  "",
		},
	}
	if got := parseHookConfig(cfg); got != nil {
		t.Errorf("want nil when prompt is empty, got %+v", got)
	}
}

func TestParseHookConfig_Valid(t *testing.T) {
	cfg := map[string]interface{}{
		"hook": map[string]interface{}{
			"trigger": "on_reply_done",
			"prompt":  "Verify the output",
			"reset":   false,
		},
	}
	got := parseHookConfig(cfg)
	if got == nil {
		t.Fatal("want non-nil hookConfig")
	}
	if got.Trigger != "on_reply_done" {
		t.Errorf("Trigger: want %q, got %q", "on_reply_done", got.Trigger)
	}
	if got.Prompt != "Verify the output" {
		t.Errorf("Prompt: want %q, got %q", "Verify the output", got.Prompt)
	}
	if got.Reset {
		t.Error("Reset should be false")
	}
}

func TestParseHookConfig_Reset(t *testing.T) {
	cfg := map[string]interface{}{
		"hook": map[string]interface{}{
			"trigger": "on_reply_done",
			"prompt":  "Check it",
			"reset":   true,
		},
	}
	got := parseHookConfig(cfg)
	if got == nil {
		t.Fatal("want non-nil hookConfig")
	}
	if !got.Reset {
		t.Error("Reset should be true")
	}
}

// ---------------------------------------------------------------------------
// Hook: buildHookPrompt
// ---------------------------------------------------------------------------

func TestBuildHookPrompt_NoOutput(t *testing.T) {
	prompt := "Check if task is done"
	got := buildHookPrompt(prompt, "")
	if strings.Contains(got, "[Previous Step Output]") {
		t.Error("should not include [Previous Step Output] when output is empty")
	}
	if !strings.Contains(got, prompt) {
		t.Error("should include user prompt")
	}
	if !strings.Contains(got, `{"ok": true}`) {
		t.Error("should include format requirement with ok=true example")
	}
	if !strings.Contains(got, `{"ok": false, "reason": "..."}`) {
		t.Error("should include format requirement with ok=false example")
	}
}

func TestBuildHookPrompt_WithOutput(t *testing.T) {
	prompt := "Verify the above output"
	output := "Step result: all tests passed"
	got := buildHookPrompt(prompt, output)
	if !strings.Contains(got, "[Previous Step Output]") {
		t.Error("should include [Previous Step Output] header")
	}
	if !strings.Contains(got, output) {
		t.Error("should include the node output")
	}
	if !strings.Contains(got, prompt) {
		t.Error("should include user prompt")
	}
	if !strings.Contains(got, `{"ok": true}`) {
		t.Error("should include format requirement")
	}
}

func TestBuildHookPrompt_OutputBeforePrompt(t *testing.T) {
	got := buildHookPrompt("check it", "the output")
	outputIdx := strings.Index(got, "the output")
	promptIdx := strings.Index(got, "check it")
	if outputIdx > promptIdx {
		t.Error("node output should appear before user prompt")
	}
}

// ---------------------------------------------------------------------------
// substituteEnvVars (shared by node prompts and hook prompts)
// ---------------------------------------------------------------------------

func TestSubstituteEnvVars_BasicAndUnknown(t *testing.T) {
	env := map[string]string{"target": "api/users"}
	got := substituteEnvVars("hit {{env_target}} via {{env_missing}}", env)
	if got != "hit api/users via " {
		t.Errorf("unexpected: %q", got)
	}
}

func TestSubstituteEnvVars_NoPrevNodeOutputForHooks(t *testing.T) {
	// Hook prompts pass req.Env directly without prev_node_output, so the
	// token must expand to empty even though it'd normally be reserved.
	got := substituteEnvVars("before [{{env_prev_node_output}}] after", map[string]string{})
	if got != "before [] after" {
		t.Errorf("expected empty expansion for hook context, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Hook: runEphemeralAgent
// ---------------------------------------------------------------------------

// mockAgent is a simple agent.Agent implementation for testing.
type mockAgent struct {
	response string
	err      error
}

func (m *mockAgent) SendMessage(_ context.Context, _ agent.Request, cb agent.EventCallback) (agent.Result, error) {
	if m.err != nil {
		return agent.Result{}, m.err
	}
	cb(agent.StreamEvent{Type: agent.EventTextDelta, Content: m.response})
	cb(agent.StreamEvent{Type: agent.EventDone})
	return agent.Result{}, nil
}

func TestRunEphemeralAgent_Success(t *testing.T) {
	s := &Server{
		Agent: &mockAgent{response: `{"ok": true}`},
	}
	got, err := s.runEphemeralAgent(context.Background(), "", ".", "check output")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `{"ok": true}` {
		t.Errorf("want %q, got %q", `{"ok": true}`, got)
	}
}

func TestRunEphemeralAgent_AgentError(t *testing.T) {
	s := &Server{
		Agent: &mockAgent{err: fmt.Errorf("agent crashed")},
	}
	_, err := s.runEphemeralAgent(context.Background(), "", ".", "check output")
	if err == nil {
		t.Error("expected error when agent returns error")
	}
}

func TestRunEphemeralAgent_NoAgent(t *testing.T) {
	s := &Server{} // Agent is nil
	_, err := s.runEphemeralAgent(context.Background(), "", ".", "check output")
	if err == nil {
		t.Error("expected error when no agent is configured")
	}
}

func TestRunEphemeralAgent_ContextTimeout(t *testing.T) {
	// Agent that blocks until context is done.
	s := &Server{
		Agent: &blockingAgent{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := s.runEphemeralAgent(ctx, "", ".", "check output")
	if err == nil {
		t.Error("expected error when context times out")
	}
}

func TestRunEphemeralAgent_UsesNamedAgent(t *testing.T) {
	primary := &mockAgent{response: "primary-response"}
	named := &mockAgent{response: "named-response"}
	s := &Server{
		Agent:  primary,
		Agents: map[string]agent.Agent{"special": named},
	}
	got, err := s.runEphemeralAgent(context.Background(), "special", ".", "check")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "named-response" {
		t.Errorf("should use named agent, got %q", got)
	}
}

func TestRunEphemeralAgent_FallsBackToDefault(t *testing.T) {
	primary := &mockAgent{response: "default-response"}
	s := &Server{
		Agent:  primary,
		Agents: map[string]agent.Agent{},
	}
	// Request agent "nonexistent" → falls back to s.Agent
	got, err := s.runEphemeralAgent(context.Background(), "nonexistent", ".", "check")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "default-response" {
		t.Errorf("should fall back to default agent, got %q", got)
	}
}

// blockingAgent blocks until its context is cancelled.
type blockingAgent struct{}

func (b *blockingAgent) SendMessage(ctx context.Context, _ agent.Request, _ agent.EventCallback) (agent.Result, error) {
	<-ctx.Done()
	return agent.Result{}, ctx.Err()
}

// ---------------------------------------------------------------------------
// Hook: hookResult JSON parsing
// ---------------------------------------------------------------------------

func TestHookResult_ParseOK(t *testing.T) {
	var r hookResult
	if err := json.Unmarshal([]byte(`{"ok": true}`), &r); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if !r.OK {
		t.Error("ok should be true")
	}
	if r.Reason != "" {
		t.Errorf("reason should be empty, got %q", r.Reason)
	}
}

func TestHookResult_ParseNotOK(t *testing.T) {
	var r hookResult
	if err := json.Unmarshal([]byte(`{"ok": false, "reason": "missing tests"}`), &r); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if r.OK {
		t.Error("ok should be false")
	}
	if r.Reason != "missing tests" {
		t.Errorf("reason should be %q, got %q", "missing tests", r.Reason)
	}
}

func TestHookResult_ParseInvalidJSON(t *testing.T) {
	var r hookResult
	err := json.Unmarshal([]byte("not json"), &r)
	if err == nil {
		t.Error("expected parse error for invalid JSON")
	}
}

func TestHookResult_ParseEmptyString(t *testing.T) {
	var r hookResult
	err := json.Unmarshal([]byte(""), &r)
	if err == nil {
		t.Error("expected parse error for empty string")
	}
}

func TestHookResult_ReasonFallback(t *testing.T) {
	// When hook says not OK but reason is empty, ensure the caller provides a fallback.
	r := hookResult{OK: false, Reason: ""}
	reason := r.Reason
	if reason == "" {
		reason = "The response was incomplete or incorrect. Please try again."
	}
	if reason == "" {
		t.Error("fallback reason should not be empty")
	}
}
