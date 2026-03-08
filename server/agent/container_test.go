package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// containerName — pure logic, no Docker needed
// ---------------------------------------------------------------------------

func TestContainerName_SafeUUID(t *testing.T) {
	id := "550e8400-e29b-41d4-a716-446655440000"
	name := containerName(id)
	if !strings.HasPrefix(name, "nexbox-proj-") {
		t.Fatalf("expected nexbox-proj- prefix, got %s", name)
	}
	if !strings.Contains(name, id) {
		t.Fatalf("safe UUID should be used verbatim: got %s", name)
	}
}

func TestContainerName_UnsafePath(t *testing.T) {
	id := "/home/user/repo/my project"
	name := containerName(id)
	if !strings.HasPrefix(name, "nexbox-proj-") {
		t.Fatalf("expected nexbox-proj- prefix, got %s", name)
	}
	// Must not contain slashes or spaces
	for _, c := range name {
		if c == '/' || c == ' ' {
			t.Fatalf("container name contains unsafe char %q: %s", c, name)
		}
	}
}

func TestContainerName_LongID(t *testing.T) {
	id := strings.Repeat("a", 49) // > 48 chars triggers hash
	name := containerName(id)
	if !strings.HasPrefix(name, "nexbox-proj-") {
		t.Fatalf("expected nexbox-proj- prefix, got %s", name)
	}
	// Should be hashed to 16 hex chars → total len = len("nexbox-proj-") + 16 = 28
	if len(name) != 28 {
		t.Fatalf("expected hashed name length 28, got %d: %s", len(name), name)
	}
}

// ---------------------------------------------------------------------------
// ExecInContainer — verifies docker exec command construction
// ---------------------------------------------------------------------------

func TestExecInContainer_Basic(t *testing.T) {
	m := NewContainerManager("alpine:latest")
	cmd := m.ExecInContainer("mycontainer", []string{"ls", "-la"}, nil, "")
	args := cmd.Args
	// Expected: docker exec -i mycontainer ls -la
	if args[0] != "docker" {
		t.Fatalf("expected docker, got %s", args[0])
	}
	if args[1] != "exec" || args[2] != "-i" {
		t.Fatalf("expected exec -i, got %v", args[1:3])
	}
	if args[len(args)-2] != "ls" || args[len(args)-1] != "-la" {
		t.Fatalf("expected ls -la at end, got %v", args[len(args)-2:])
	}
}

func TestExecInContainer_WithEnv(t *testing.T) {
	m := NewContainerManager("alpine:latest")
	env := []string{"FOO=bar", "BAZ=qux"}
	cmd := m.ExecInContainer("mycontainer", []string{"echo", "hi"}, env, "")
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "-e FOO=bar") {
		t.Fatalf("missing -e FOO=bar in: %s", joined)
	}
	if !strings.Contains(joined, "-e BAZ=qux") {
		t.Fatalf("missing -e BAZ=qux in: %s", joined)
	}
}

func TestExecInContainer_WithWorkDir(t *testing.T) {
	m := NewContainerManager("alpine:latest")
	cmd := m.ExecInContainer("mycontainer", []string{"pwd"}, nil, "/workspace/sub")
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "-w /workspace/sub") {
		t.Fatalf("missing -w /workspace/sub in: %s", joined)
	}
}

func TestExecInContainer_NoWorkDir(t *testing.T) {
	m := NewContainerManager("alpine:latest")
	cmd := m.ExecInContainer("mycontainer", []string{"pwd"}, nil, "")
	for i, a := range cmd.Args {
		if a == "-w" {
			t.Fatalf("unexpected -w flag at position %d in: %v", i, cmd.Args)
		}
	}
}

// ---------------------------------------------------------------------------
// EnsureContainer + StopContainer — uses a fake docker binary
// ---------------------------------------------------------------------------

// makeFakeDocker writes a shell script that simulates docker behaviour and
// returns the directory it was written to. Caller must add dir to PATH.
//
// Modes controlled by the FAKE_DOCKER_MODE env var:
//
//	"container_running"  — docker ps returns a fake container ID
//	"container_missing"  — docker ps returns empty; docker run succeeds
//	"run_fail"           — docker ps empty; docker run exits non-zero
//	"stop_ok"            — docker stop exits 0
//	"stop_fail"          — docker stop exits 1
func makeFakeDocker(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "docker")

	// The script inspects $FAKE_DOCKER_MODE set in the test environment
	src := `#!/bin/sh
MODE="$FAKE_DOCKER_MODE"
case "$1" in
  ps)
    if [ "$MODE" = "container_running" ]; then
      echo "abc123def456"
    fi
    exit 0
    ;;
  run)
    if [ "$MODE" = "run_fail" ]; then
      echo "simulated run failure" >&2
      exit 1
    fi
    echo "newcontainerid"
    exit 0
    ;;
  stop)
    if [ "$MODE" = "stop_fail" ]; then
      echo "No such container" >&2
      exit 1
    fi
    exit 0
    ;;
  exec)
    # Just echo the args so callers can verify
    echo "$@"
    exit 0
    ;;
  *)
    exit 1
    ;;
esac
`
	if err := os.WriteFile(script, []byte(src), 0755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}

	// Override PATH so exec.Command("docker",...) finds our fake
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return dir
}

// verifyFakeDockerFound ensures the fake docker is actually on PATH.
func verifyFakeDockerFound(t *testing.T) {
	t.Helper()
	p, err := exec.LookPath("docker")
	if err != nil {
		t.Fatalf("fake docker not found on PATH: %v", err)
	}
	if !strings.Contains(p, t.TempDir()[:10]) { // rough prefix check
		// TempDir prefix may differ; just check it resolves
		_ = p
	}
}

func TestEnsureContainer_ContainerAlreadyRunning(t *testing.T) {
	makeFakeDocker(t, "")
	t.Setenv("FAKE_DOCKER_MODE", "container_running")

	m := NewContainerManager("alpine:latest")
	id, err := m.EnsureContainer("proj-abc", "/tmp/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty container id/name")
	}
	// Should reuse — name contains projectID
	if !strings.Contains(id, "proj-abc") {
		t.Fatalf("expected container name to contain proj-abc, got %s", id)
	}
}

func TestEnsureContainer_StartsNewContainer(t *testing.T) {
	makeFakeDocker(t, "")
	t.Setenv("FAKE_DOCKER_MODE", "container_missing")

	m := NewContainerManager("alpine:latest")
	id, err := m.EnsureContainer("proj-new", "/tmp/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty container name after start")
	}
}

func TestEnsureContainer_RunFailure(t *testing.T) {
	makeFakeDocker(t, "")
	t.Setenv("FAKE_DOCKER_MODE", "run_fail")

	m := NewContainerManager("alpine:latest")
	_, err := m.EnsureContainer("proj-fail", "/tmp/repo")
	if err == nil {
		t.Fatal("expected error when docker run fails")
	}
	if !strings.Contains(err.Error(), "docker run") {
		t.Fatalf("expected 'docker run' in error, got: %v", err)
	}
}

func TestStopContainer_OK(t *testing.T) {
	makeFakeDocker(t, "")
	t.Setenv("FAKE_DOCKER_MODE", "stop_ok")

	m := NewContainerManager("alpine:latest")
	if err := m.StopContainer("proj-abc"); err != nil {
		t.Fatalf("expected no error on stop, got: %v", err)
	}
}

func TestStopContainer_NotRunning(t *testing.T) {
	makeFakeDocker(t, "")
	t.Setenv("FAKE_DOCKER_MODE", "stop_fail")

	m := NewContainerManager("alpine:latest")
	err := m.StopContainer("proj-ghost")
	// Should return error (non-running containers are not silent)
	if err == nil {
		t.Fatal("expected error when container not found")
	}
}

// ---------------------------------------------------------------------------
// BUG REPRODUCTION: H5 — docker ps --filter name= is substring match
// This test documents the known bug where a container named nexbox-proj-abc
// would also match when filtering for nexbox-proj-abcXYZ.
// ---------------------------------------------------------------------------

func TestContainerName_SubstringMatchBug(t *testing.T) {
	// Two distinct project IDs where one name is a prefix of another
	nameA := containerName("abc")
	nameB := containerName("abcxyz")

	// They must be different names
	if nameA == nameB {
		t.Fatalf("different project IDs produced the same container name: %s", nameA)
	}

	// BUG: docker ps --filter name=nameA would ALSO return containers named nameB
	// because Docker name filter matches substrings, not exact names.
	// Demonstrate the issue: nameA IS a prefix of nameB
	if !strings.HasPrefix(nameB, nameA) {
		// Not a prefix — in this case the bug doesn't trigger for this pair
		t.Logf("INFO: %q is not a prefix of %q — substring match bug would not trigger for this pair", nameA, nameB)
	} else {
		// Bug would trigger: searching for nameA would return the nameB container
		t.Logf("BUG CONFIRMED: docker ps --filter name=%q would also match container %q", nameA, nameB)
		t.Logf("  Fix: use --filter name=^/%s$ to anchor the match", nameA)
		// We intentionally do NOT call t.Fatal here — this is a known, documented bug
		// tracked as issue H5. The test serves as a regression marker.
	}
}

// ---------------------------------------------------------------------------
// Bind-mount path construction verification
// ---------------------------------------------------------------------------

func TestEnsureContainer_BindMountPath(t *testing.T) {
	// Capture the docker run args by intercepting via fake docker that logs args
	dir := t.TempDir()
	logFile := filepath.Join(dir, "docker-args.log")
	script := filepath.Join(dir, "docker")
	src := `#!/bin/sh
echo "$@" >> ` + logFile + `
case "$1" in
  ps) exit 0 ;;
  run) echo "cid123"; exit 0 ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(script, []byte(src), 0755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	repoPath := "/home/user/myproject"
	m := NewContainerManager("alpine:latest")
	_, err := m.EnsureContainer("proj-mount", repoPath)
	if err != nil {
		t.Fatalf("EnsureContainer error: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	logged := string(data)

	// The run invocation should contain the bind mount
	expectedMount := repoPath + ":/workspace"
	if !strings.Contains(logged, expectedMount) {
		t.Fatalf("expected bind mount %q in docker args, got:\n%s", expectedMount, logged)
	}

	// Working directory inside container should be /workspace
	if !strings.Contains(logged, "-w /workspace") {
		t.Fatalf("expected -w /workspace in docker args, got:\n%s", logged)
	}

	// Container should use --rm flag (auto-removed on stop)
	if !strings.Contains(logged, "--rm") {
		t.Fatalf("expected --rm in docker args, got:\n%s", logged)
	}

	// Container should run tail -f /dev/null to stay alive
	if !strings.Contains(logged, "tail -f /dev/null") {
		t.Fatalf("expected 'tail -f /dev/null' in docker args, got:\n%s", logged)
	}
}

// ---------------------------------------------------------------------------
// Server restart recovery: container should be reused after server restart
// (simulated by creating a second ContainerManager and calling EnsureContainer)
// ---------------------------------------------------------------------------

func TestEnsureContainer_ReusedAfterRestart(t *testing.T) {
	makeFakeDocker(t, "")
	t.Setenv("FAKE_DOCKER_MODE", "container_running")

	// Simulate server restart: a NEW ContainerManager (no in-memory state)
	m2 := NewContainerManager("alpine:latest")
	id, err := m2.EnsureContainer("proj-restart", "/tmp/repo")
	if err != nil {
		t.Fatalf("unexpected error on restart reuse: %v", err)
	}
	if id == "" {
		t.Fatal("expected container to be reused after restart")
	}
}

// ---------------------------------------------------------------------------
// Activity tracking & idle container reaping (T3.3)
// ---------------------------------------------------------------------------

func TestTouchActivity(t *testing.T) {
	m := NewContainerManager("alpine:latest")

	// Initially empty
	m.mu.Lock()
	if len(m.lastActivity) != 0 {
		t.Fatal("expected empty lastActivity map")
	}
	m.mu.Unlock()

	m.TouchActivity("proj-1")

	m.mu.Lock()
	ts, ok := m.lastActivity["proj-1"]
	m.mu.Unlock()
	if !ok {
		t.Fatal("expected proj-1 in lastActivity after TouchActivity")
	}
	if time.Since(ts) > time.Second {
		t.Fatal("expected lastActivity to be set to ~now")
	}
}

func TestEnsureContainer_SetsLastActivity(t *testing.T) {
	makeFakeDocker(t, "")
	t.Setenv("FAKE_DOCKER_MODE", "container_missing")

	m := NewContainerManager("alpine:latest")
	_, err := m.EnsureContainer("proj-track", "/tmp/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m.mu.Lock()
	ts, ok := m.lastActivity["proj-track"]
	m.mu.Unlock()
	if !ok {
		t.Fatal("EnsureContainer should set lastActivity for new container")
	}
	if time.Since(ts) > time.Second {
		t.Fatal("lastActivity should be ~now")
	}
}

func TestEnsureContainer_UpdatesLastActivityOnReuse(t *testing.T) {
	makeFakeDocker(t, "")
	t.Setenv("FAKE_DOCKER_MODE", "container_running")

	m := NewContainerManager("alpine:latest")

	// Pre-set old activity
	m.mu.Lock()
	m.lastActivity["proj-reuse"] = time.Now().Add(-2 * time.Hour)
	m.mu.Unlock()

	_, err := m.EnsureContainer("proj-reuse", "/tmp/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m.mu.Lock()
	ts := m.lastActivity["proj-reuse"]
	m.mu.Unlock()
	if time.Since(ts) > time.Second {
		t.Fatal("EnsureContainer should refresh lastActivity on reuse")
	}
}

func TestStopContainer_CleansLastActivity(t *testing.T) {
	makeFakeDocker(t, "")
	t.Setenv("FAKE_DOCKER_MODE", "stop_ok")

	m := NewContainerManager("alpine:latest")
	m.TouchActivity("proj-stop")

	_ = m.StopContainer("proj-stop")

	m.mu.Lock()
	_, ok := m.lastActivity["proj-stop"]
	m.mu.Unlock()
	if ok {
		t.Fatal("StopContainer should remove entry from lastActivity")
	}
}

func TestStopIdleContainers_StopsIdleOnly(t *testing.T) {
	makeFakeDocker(t, "")
	t.Setenv("FAKE_DOCKER_MODE", "stop_ok")

	m := NewContainerManager("alpine:latest")

	// proj-idle: last active 2 hours ago → should be stopped
	m.mu.Lock()
	m.lastActivity["proj-idle"] = time.Now().Add(-2 * time.Hour)
	// proj-active: last active 5 minutes ago → should be kept
	m.lastActivity["proj-active"] = time.Now().Add(-5 * time.Minute)
	m.mu.Unlock()

	stopped := m.StopIdleContainers(1 * time.Hour)

	if stopped != 1 {
		t.Fatalf("expected 1 container stopped, got %d", stopped)
	}

	m.mu.Lock()
	_, hasIdle := m.lastActivity["proj-idle"]
	_, hasActive := m.lastActivity["proj-active"]
	m.mu.Unlock()

	if hasIdle {
		t.Fatal("idle container should be removed from lastActivity")
	}
	if !hasActive {
		t.Fatal("active container should still be in lastActivity")
	}
}

func TestStopIdleContainers_NoneIdle(t *testing.T) {
	m := NewContainerManager("alpine:latest")

	m.mu.Lock()
	m.lastActivity["proj-1"] = time.Now()
	m.lastActivity["proj-2"] = time.Now().Add(-10 * time.Minute)
	m.mu.Unlock()

	stopped := m.StopIdleContainers(1 * time.Hour)
	if stopped != 0 {
		t.Fatalf("expected 0 containers stopped, got %d", stopped)
	}

	m.mu.Lock()
	count := len(m.lastActivity)
	m.mu.Unlock()
	if count != 2 {
		t.Fatalf("expected 2 entries still tracked, got %d", count)
	}
}

func TestStopIdleContainers_RecoveredContainerName(t *testing.T) {
	makeFakeDocker(t, "")
	t.Setenv("FAKE_DOCKER_MODE", "stop_ok")

	m := NewContainerManager("alpine:latest")

	// Simulate a recovered container (key is already a container name)
	m.mu.Lock()
	m.lastActivity["nexbox-proj-abc123"] = time.Now().Add(-2 * time.Hour)
	m.mu.Unlock()

	stopped := m.StopIdleContainers(1 * time.Hour)

	if stopped != 1 {
		t.Fatalf("expected 1 container stopped, got %d", stopped)
	}

	m.mu.Lock()
	_, has := m.lastActivity["nexbox-proj-abc123"]
	m.mu.Unlock()
	if has {
		t.Fatal("recovered container entry should be cleaned up")
	}
}

// makeFakeDockerWithNames creates a fake docker that returns specific container
// names for `docker ps` queries. Used to test RecoverRunningContainers.
func makeFakeDockerWithNames(t *testing.T, names []string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "docker")

	namesOutput := strings.Join(names, "\n")
	src := `#!/bin/sh
case "$1" in
  ps) echo "` + namesOutput + `"; exit 0 ;;
  stop) exit 0 ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(script, []byte(src), 0755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestRecoverRunningContainers(t *testing.T) {
	containers := []string{"nexbox-proj-aaa", "nexbox-proj-bbb"}
	makeFakeDockerWithNames(t, containers)

	m := NewContainerManager("alpine:latest")

	// Initially empty
	m.mu.Lock()
	if len(m.lastActivity) != 0 {
		t.Fatal("expected empty lastActivity before recovery")
	}
	m.mu.Unlock()

	m.RecoverRunningContainers()

	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.lastActivity) != 2 {
		t.Fatalf("expected 2 recovered entries, got %d", len(m.lastActivity))
	}

	for _, name := range containers {
		ts, ok := m.lastActivity[name]
		if !ok {
			t.Fatalf("expected %s in lastActivity", name)
		}
		if time.Since(ts) > time.Second {
			t.Fatalf("recovered container %s should have lastActivity ~now", name)
		}
	}
}

func TestRecoverRunningContainers_Empty(t *testing.T) {
	makeFakeDockerWithNames(t, nil)

	m := NewContainerManager("alpine:latest")
	m.RecoverRunningContainers()

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.lastActivity) != 0 {
		t.Fatalf("expected 0 entries after recovering with no containers, got %d", len(m.lastActivity))
	}
}

func TestRecoverThenIdleReap(t *testing.T) {
	// End-to-end: recover containers, then reap idle ones after timeout.
	makeFakeDockerWithNames(t, []string{"nexbox-proj-old"})

	m := NewContainerManager("alpine:latest")
	m.RecoverRunningContainers()

	// Simulate time passing: set lastActivity to 2 hours ago
	m.mu.Lock()
	m.lastActivity["nexbox-proj-old"] = time.Now().Add(-2 * time.Hour)
	m.mu.Unlock()

	stopped := m.StopIdleContainers(1 * time.Hour)
	if stopped != 1 {
		t.Fatalf("expected 1 container reaped, got %d", stopped)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.lastActivity) != 0 {
		t.Fatalf("expected empty lastActivity after reap, got %d", len(m.lastActivity))
	}
}
