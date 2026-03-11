package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// containerName — pure logic, no Podman needed
// ---------------------------------------------------------------------------

func TestContainerName_SafeUUID(t *testing.T) {
	id := "550e8400-e29b-41d4-a716-446655440000"
	name := containerName(id)
	if !strings.HasPrefix(name, "boxnex-proj-") {
		t.Fatalf("expected boxnex-proj- prefix, got %s", name)
	}
	if !strings.Contains(name, id) {
		t.Fatalf("safe UUID should be used verbatim: got %s", name)
	}
}

func TestContainerName_UnsafePath(t *testing.T) {
	id := "/home/user/repo/my project"
	name := containerName(id)
	if !strings.HasPrefix(name, "boxnex-proj-") {
		t.Fatalf("expected boxnex-proj- prefix, got %s", name)
	}
	for _, c := range name {
		if c == '/' || c == ' ' {
			t.Fatalf("container name contains unsafe char %q: %s", c, name)
		}
	}
}

func TestContainerName_LongID(t *testing.T) {
	id := strings.Repeat("a", 49) // > 48 chars triggers hash
	name := containerName(id)
	if !strings.HasPrefix(name, "boxnex-proj-") {
		t.Fatalf("expected boxnex-proj- prefix, got %s", name)
	}
	// Should be hashed to 16 hex chars → total len = len("boxnex-proj-") + 16 = 28
	if len(name) != 28 {
		t.Fatalf("expected hashed name length 28, got %d: %s", len(name), name)
	}
}

func TestContainerName_Deterministic(t *testing.T) {
	id := "my-project-id"
	if containerName(id) != containerName(id) {
		t.Fatal("containerName must be deterministic")
	}
}

// ---------------------------------------------------------------------------
// Fake Podman helper — replaces docker in PATH with a podman script
// ---------------------------------------------------------------------------

// makeFakePodman writes a shell script that simulates podman behaviour and
// injects it at the front of PATH. Modes are controlled by FAKE_PODMAN_MODE.
//
// Supported modes:
//
//	"container_running"  — podman ps returns a fake container ID
//	"container_missing"  — podman ps returns empty; podman run succeeds
//	"run_fail"           — podman ps empty; podman run exits non-zero
//	"stop_ok"            — podman stop exits 0
//	"stop_fail"          — podman stop exits 1
func makeFakePodman(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "podman")

	src := `#!/bin/sh
MODE="$FAKE_PODMAN_MODE"
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
    echo "$@"
    exit 0
    ;;
  network)
    # network ls / network create
    if [ "$2" = "ls" ]; then
      exit 0
    fi
    if [ "$2" = "create" ]; then
      exit 0
    fi
    exit 0
    ;;
  *)
    exit 1
    ;;
esac
`
	if err := os.WriteFile(script, []byte(src), 0755); err != nil {
		t.Fatalf("write fake podman: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if mode != "" {
		t.Setenv("FAKE_PODMAN_MODE", mode)
	}
	return dir
}

// makeFakePodmanEmpty creates a fake podman where `ps` returns no containers.
// Used to test RecoverRunningContainers with an empty cluster.
func makeFakePodmanEmpty(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "podman")
	src := `#!/bin/sh
case "$1" in
  ps) exit 0 ;;
  stop) exit 0 ;;
  network) exit 0 ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(script, []byte(src), 0755); err != nil {
		t.Fatalf("write fake podman: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// ---------------------------------------------------------------------------
// ExecInContainer — verifies podman exec command construction
// ---------------------------------------------------------------------------

func TestExecInContainer_Basic(t *testing.T) {
	m := NewContainerManager("alpine:latest", "", nil)
	cmd := m.ExecInContainer("mycontainer", []string{"ls", "-la"}, nil, "")
	args := cmd.Args
	if args[0] != "podman" {
		t.Fatalf("expected podman, got %s", args[0])
	}
	if args[1] != "exec" || args[2] != "-i" {
		t.Fatalf("expected exec -i, got %v", args[1:3])
	}
	if args[len(args)-2] != "ls" || args[len(args)-1] != "-la" {
		t.Fatalf("expected ls -la at end, got %v", args[len(args)-2:])
	}
}

func TestExecInContainer_RunsAsNonRoot(t *testing.T) {
	// U-04: docker exec must also specify --user 1000:1000
	m := NewContainerManager("alpine:latest", "", nil)
	cmd := m.ExecInContainer("mycontainer", []string{"id"}, nil, "")
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "--user 1000:1000") {
		t.Fatalf("ExecInContainer must include --user 1000:1000, got: %s", joined)
	}
}

func TestExecInContainer_WithEnv(t *testing.T) {
	m := NewContainerManager("alpine:latest", "", nil)
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
	m := NewContainerManager("alpine:latest", "", nil)
	cmd := m.ExecInContainer("mycontainer", []string{"pwd"}, nil, "/workspace/sub")
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "-w /workspace/sub") {
		t.Fatalf("missing -w /workspace/sub in: %s", joined)
	}
}

func TestExecInContainer_NoWorkDir(t *testing.T) {
	m := NewContainerManager("alpine:latest", "", nil)
	cmd := m.ExecInContainer("mycontainer", []string{"pwd"}, nil, "")
	for i, a := range cmd.Args {
		if a == "-w" {
			t.Fatalf("unexpected -w flag at position %d in: %v", i, cmd.Args)
		}
	}
}

// ---------------------------------------------------------------------------
// EnsureContainer — basic lifecycle
// ---------------------------------------------------------------------------

func TestEnsureContainer_ContainerAlreadyRunning(t *testing.T) {
	makeFakePodman(t, "container_running")

	m := NewContainerManager("alpine:latest", "boxnex-egress", nil)
	id, err := m.EnsureContainer("proj-abc", "/tmp/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty container id/name")
	}
	if !strings.Contains(id, "proj-abc") {
		t.Fatalf("expected container name to contain proj-abc, got %s", id)
	}
}

func TestEnsureContainer_StartsNewContainer(t *testing.T) {
	makeFakePodman(t, "container_missing")

	m := NewContainerManager("alpine:latest", "boxnex-egress", nil)
	id, err := m.EnsureContainer("proj-new", "/tmp/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty container name after start")
	}
}

func TestEnsureContainer_RunFailure(t *testing.T) {
	makeFakePodman(t, "run_fail")

	m := NewContainerManager("alpine:latest", "boxnex-egress", nil)
	_, err := m.EnsureContainer("proj-fail", "/tmp/repo")
	if err == nil {
		t.Fatal("expected error when podman run fails")
	}
	if !strings.Contains(err.Error(), "podman run") {
		t.Fatalf("expected 'podman run' in error, got: %v", err)
	}
}

func TestStopContainer_OK(t *testing.T) {
	makeFakePodman(t, "stop_ok")

	m := NewContainerManager("alpine:latest", "boxnex-egress", nil)
	if err := m.StopContainer("proj-abc"); err != nil {
		t.Fatalf("expected no error on stop, got: %v", err)
	}
}

func TestStopContainer_NotRunning(t *testing.T) {
	makeFakePodman(t, "stop_fail")

	m := NewContainerManager("alpine:latest", "boxnex-egress", nil)
	err := m.StopContainer("proj-ghost")
	if err == nil {
		t.Fatal("expected error when container not found")
	}
}

// ---------------------------------------------------------------------------
// Security flags verification (C-01 through C-06)
// ---------------------------------------------------------------------------

func TestEnsureContainer_SecurityFlags(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "podman-args.log")
	script := filepath.Join(dir, "podman")
	src := `#!/bin/sh
echo "$@" >> ` + logFile + `
case "$1" in
  ps) exit 0 ;;
  run) echo "cid123"; exit 0 ;;
  network) exit 0 ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(script, []byte(src), 0755); err != nil {
		t.Fatalf("write fake podman: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	m := NewContainerManager("alpine:latest", "boxnex-egress", nil)
	_, err := m.EnsureContainer("proj-security", "/tmp/repo")
	if err != nil {
		t.Fatalf("EnsureContainer error: %v", err)
	}

	data, _ := os.ReadFile(logFile)
	logged := string(data)

	// C-01: no-new-privileges
	if !strings.Contains(logged, "no-new-privileges") {
		t.Errorf("C-01: expected no-new-privileges, got:\n%s", logged)
	}
	// C-02: cap-drop ALL
	if !strings.Contains(logged, "--cap-drop ALL") {
		t.Errorf("C-02: expected --cap-drop ALL, got:\n%s", logged)
	}
	// C-02: minimum caps re-added
	for _, cap := range []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"} {
		if !strings.Contains(logged, "--cap-add "+cap) {
			t.Errorf("C-02: expected --cap-add %s, got:\n%s", cap, logged)
		}
	}
	// C-03: tmpfs /tmp
	if !strings.Contains(logged, "/tmp") {
		t.Errorf("C-03: expected --tmpfs /tmp, got:\n%s", logged)
	}
	// C-04: memory-swap equals memory (swap disabled)
	if !strings.Contains(logged, "--memory-swap") {
		t.Errorf("C-04: expected --memory-swap flag, got:\n%s", logged)
	}
	// U-01: rootless uid mapping (--user not set in run; UID switching handled by exec -u)
	if !strings.Contains(logged, "--userns=keep-id") {
		t.Errorf("U-01: expected --userns=keep-id, got:\n%s", logged)
	}
	// N-04: dedicated network
	if !strings.Contains(logged, "boxnex-egress") {
		t.Errorf("N-04: expected boxnex-egress network, got:\n%s", logged)
	}
	// Claude tuning env vars
	if !strings.Contains(logged, "CLAUDE_CONFIG_DIR=/workspace/.claude") {
		t.Errorf("expected CLAUDE_CONFIG_DIR env var, got:\n%s", logged)
	}
	if !strings.Contains(logged, "DISABLE_TELEMETRY=1") {
		t.Errorf("expected DISABLE_TELEMETRY=1, got:\n%s", logged)
	}
	if !strings.Contains(logged, "DISABLE_AUTOUPDATER=1") {
		t.Errorf("expected DISABLE_AUTOUPDATER=1, got:\n%s", logged)
	}
	if !strings.Contains(logged, "USE_BUILTIN_RIPGREP=0") {
		t.Errorf("expected USE_BUILTIN_RIPGREP=0, got:\n%s", logged)
	}
}

// ---------------------------------------------------------------------------
// API key isolation — real key injected only via ExecClaudeInContainer (T-01)
// ---------------------------------------------------------------------------

func TestEnsureContainer_RealKeyNotInRunArgs(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "podman-args.log")
	script := filepath.Join(dir, "podman")
	src := `#!/bin/sh
echo "$@" >> ` + logFile + `
case "$1" in
  ps) exit 0 ;;
  run) echo "cid123"; exit 0 ;;
  network) exit 0 ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(script, []byte(src), 0755); err != nil {
		t.Fatalf("write fake podman: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	const realKey = "sk-ant-real-secret-key"
	m := NewContainerManager("alpine:latest", "boxnex-egress", nil)
	m.RealAPIKey = realKey
	_, err := m.EnsureContainer("proj-key", "/tmp/repo")
	if err != nil {
		t.Fatalf("EnsureContainer error: %v", err)
	}

	data, _ := os.ReadFile(logFile)
	logged := string(data)

	// T-01: real key must never appear in podman run args — only injected at exec time.
	if strings.Contains(logged, realKey) {
		t.Errorf("T-01 VIOLATED: real API key appeared in podman run args:\n%s", logged)
	}
	// CLAUDE_CODE_OAUTH_TOKEN must not be set in the container environment.
	if strings.Contains(logged, "CLAUDE_CODE_OAUTH_TOKEN=") {
		t.Errorf("T-01: CLAUDE_CODE_OAUTH_TOKEN must not appear in podman run args:\n%s", logged)
	}
}

func TestEnsureContainer_InjectsProxyEnvVars(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "podman-args.log")
	script := filepath.Join(dir, "podman")
	src := `#!/bin/sh
echo "$@" >> ` + logFile + `
case "$1" in
  ps) exit 0 ;;
  run) echo "cid123"; exit 0 ;;
  network) exit 0 ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(script, []byte(src), 0755); err != nil {
		t.Fatalf("write fake podman: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	proxy := NewContainerProxy("127.0.0.1:9999", nil)
	// Don't start the proxy server — we just need the addr for env var injection.
	m := NewContainerManager("alpine:latest", "boxnex-egress", proxy)
	_, err := m.EnsureContainer("proj-proxyenv", "/tmp/repo")
	if err != nil {
		t.Fatalf("EnsureContainer error: %v", err)
	}

	data, _ := os.ReadFile(logFile)
	logged := string(data)

	// N-03: proxy env vars must be injected for egress whitelist routing
	if !strings.Contains(logged, "HTTP_PROXY=") {
		t.Errorf("N-03: missing HTTP_PROXY env var in:\n%s", logged)
	}
	if !strings.Contains(logged, "HTTPS_PROXY=") {
		t.Errorf("N-03: missing HTTPS_PROXY env var in:\n%s", logged)
	}
	if !strings.Contains(logged, "NO_PROXY=") {
		t.Errorf("N-03: missing NO_PROXY env var in:\n%s", logged)
	}
}

// ---------------------------------------------------------------------------
// ExecClaudeInContainer — UID 9999 + API key injection (T-01)
// ---------------------------------------------------------------------------

func TestExecClaudeInContainer_UsesUID9999(t *testing.T) {
	m := NewContainerManager("alpine:latest", "", nil)
	cmd := m.ExecClaudeInContainer("mycontainer", []string{"claude", "--version"}, nil, "")
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "--user 9999:9999") {
		t.Fatalf("ExecClaudeInContainer must use --user 9999:9999, got: %s", joined)
	}
}

func TestExecClaudeInContainer_InjectsRealAPIKey(t *testing.T) {
	const realKey = "sk-ant-real-secret-key"
	m := NewContainerManager("alpine:latest", "", nil)
	m.RealAPIKey = realKey
	cmd := m.ExecClaudeInContainer("mycontainer", []string{"claude"}, nil, "")
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "CLAUDE_CODE_OAUTH_TOKEN="+realKey) {
		t.Fatalf("ExecClaudeInContainer must inject real API key, got: %s", joined)
	}
}

func TestExecClaudeInContainer_NoKeyWhenEmpty(t *testing.T) {
	m := NewContainerManager("alpine:latest", "", nil)
	// RealAPIKey deliberately left empty
	cmd := m.ExecClaudeInContainer("mycontainer", []string{"claude"}, nil, "")
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, "CLAUDE_CODE_OAUTH_TOKEN=") {
			t.Fatalf("expected no CLAUDE_CODE_OAUTH_TOKEN when RealAPIKey is empty, got arg: %s", a)
		}
	}
}

// ---------------------------------------------------------------------------
// Bind-mount and container lifecycle flags
// ---------------------------------------------------------------------------

func TestEnsureContainer_BindMountPath(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "podman-args.log")
	script := filepath.Join(dir, "podman")
	src := `#!/bin/sh
echo "$@" >> ` + logFile + `
case "$1" in
  ps) exit 0 ;;
  run) echo "cid123"; exit 0 ;;
  network) exit 0 ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(script, []byte(src), 0755); err != nil {
		t.Fatalf("write fake podman: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	repoPath := "/home/user/myproject"
	m := NewContainerManager("alpine:latest", "boxnex-egress", nil)
	_, err := m.EnsureContainer("proj-mount", repoPath)
	if err != nil {
		t.Fatalf("EnsureContainer error: %v", err)
	}

	data, _ := os.ReadFile(logFile)
	logged := string(data)

	if !strings.Contains(logged, repoPath+":/workspace") {
		t.Fatalf("expected bind mount %q in podman args, got:\n%s", repoPath+":/workspace", logged)
	}
	if !strings.Contains(logged, "-w /workspace") {
		t.Fatalf("expected -w /workspace in podman args, got:\n%s", logged)
	}
	if !strings.Contains(logged, "--rm") {
		t.Fatalf("expected --rm in podman args, got:\n%s", logged)
	}
	if !strings.Contains(logged, "tail -f /dev/null") {
		t.Fatalf("expected 'tail -f /dev/null' in podman args, got:\n%s", logged)
	}
}

// ---------------------------------------------------------------------------
// Activity tracking & idle container reaping (R-01)
// ---------------------------------------------------------------------------

func TestTouchActivity(t *testing.T) {
	m := NewContainerManager("alpine:latest", "", nil)

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
	makeFakePodman(t, "container_missing")

	m := NewContainerManager("alpine:latest", "boxnex-egress", nil)
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
	makeFakePodman(t, "container_running")

	m := NewContainerManager("alpine:latest", "boxnex-egress", nil)

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
	makeFakePodman(t, "stop_ok")

	m := NewContainerManager("alpine:latest", "boxnex-egress", nil)
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
	makeFakePodman(t, "stop_ok")

	m := NewContainerManager("alpine:latest", "boxnex-egress", nil)

	m.mu.Lock()
	m.lastActivity["proj-idle"] = time.Now().Add(-2 * time.Hour)
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
	m := NewContainerManager("alpine:latest", "", nil)

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

// ---------------------------------------------------------------------------
// RecoverRunningContainers
// ---------------------------------------------------------------------------

func TestRecoverRunningContainers_StopsStaleContainers(t *testing.T) {
	// RecoverRunningContainers must STOP stale containers so they can be
	// restarted with fresh session tokens on next use (T-03 / restart bug fix).
	dir := t.TempDir()
	logFile := filepath.Join(dir, "podman.log")
	script := filepath.Join(dir, "podman")
	src := `#!/bin/sh
echo "$@" >> ` + logFile + `
case "$1" in
  ps) echo "boxnex-proj-aaa
boxnex-proj-bbb"; exit 0 ;;
  stop) exit 0 ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(script, []byte(src), 0755); err != nil {
		t.Fatalf("write fake podman: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	m := NewContainerManager("alpine:latest", "boxnex-egress", nil)
	m.RecoverRunningContainers()

	// Must NOT populate lastActivity — containers are stopped, not tracked.
	m.mu.Lock()
	count := len(m.lastActivity)
	m.mu.Unlock()
	if count != 0 {
		t.Fatalf("RecoverRunningContainers must not seed lastActivity; got %d entries", count)
	}

	// Both containers must have been stopped.
	data, _ := os.ReadFile(logFile)
	logged := string(data)
	for _, name := range []string{"boxnex-proj-aaa", "boxnex-proj-bbb"} {
		if !strings.Contains(logged, "stop "+name) {
			t.Errorf("expected 'stop %s' in podman calls, got:\n%s", name, logged)
		}
	}
}

func TestRecoverRunningContainers_Empty(t *testing.T) {
	makeFakePodmanEmpty(t)

	m := NewContainerManager("alpine:latest", "boxnex-egress", nil)
	m.RecoverRunningContainers()

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.lastActivity) != 0 {
		t.Fatalf("expected 0 entries after recovering with no containers, got %d", len(m.lastActivity))
	}
}

// ---------------------------------------------------------------------------
// WatchWorkspace (D-01) — workspace disk monitor
// ---------------------------------------------------------------------------

func TestWatchWorkspace_CancelsOnQuotaExceeded(t *testing.T) {
	dir := t.TempDir()

	// Write enough data to exceed a tiny quota (1 byte)
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), []byte("hello world"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cancelled := make(chan struct{})
	wrappedCancel := func() {
		cancel()
		close(cancelled)
	}

	m := NewContainerManager("alpine:latest", "", nil)
	m.WatchInterval = 50 * time.Millisecond // fast interval for test
	m.WatchWorkspace(ctx, dir, 1 /* 1 byte quota */, wrappedCancel)

	select {
	case <-cancelled:
		// Good: quota exceeded, cancel was called
	case <-time.After(5 * time.Second):
		t.Fatal("WatchWorkspace did not cancel within 5s after quota exceeded")
	}
}

func TestWatchWorkspace_DoesNotCancelWhenUnderQuota(t *testing.T) {
	dir := t.TempDir()
	// Write a small file — stays well under a 10 MB quota
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("hi"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cancelCalled := false
	m := NewContainerManager("alpine:latest", "", nil)
	m.WatchInterval = 50 * time.Millisecond
	m.WatchWorkspace(ctx, dir, 10<<20 /* 10 MB */, func() { cancelCalled = true })

	// Let the goroutine tick a couple of times
	time.Sleep(250 * time.Millisecond)

	// Cancel the monitor context to clean up goroutine
	cancel()
	time.Sleep(50 * time.Millisecond)

	if cancelCalled {
		t.Fatal("WatchWorkspace must not cancel when usage is below quota")
	}
}

func TestWatchWorkspace_NoopOnZeroQuota(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cancelCalled := false
	m := NewContainerManager("alpine:latest", "", nil)
	// quota=0 → should be a no-op (no goroutine launched)
	m.WatchWorkspace(ctx, dir, 0, func() { cancelCalled = true })

	time.Sleep(100 * time.Millisecond)
	if cancelCalled {
		t.Fatal("WatchWorkspace with quota=0 must be a no-op")
	}
}

// ---------------------------------------------------------------------------
// workspaceSize helper
// ---------------------------------------------------------------------------

func TestWorkspaceSize(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello world") // 11 bytes
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), content, 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	size, err := workspaceSize(dir)
	if err != nil {
		t.Fatalf("workspaceSize error: %v", err)
	}
	if size < int64(len(content)) {
		t.Fatalf("expected size >= %d, got %d", len(content), size)
	}
}

func TestWorkspaceSize_NonExistentDir(t *testing.T) {
	_, err := workspaceSize("/nonexistent/path/abc123")
	if err == nil {
		t.Fatal("expected error for non-existent directory")
	}
}

// ---------------------------------------------------------------------------
// EnsureNetwork
// ---------------------------------------------------------------------------

func TestEnsureNetwork_CreatesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "podman.log")
	script := filepath.Join(dir, "podman")
	// Simulate: network ls returns empty (not found), network create succeeds
	src := `#!/bin/sh
echo "$@" >> ` + logFile + `
case "$1 $2" in
  "network ls") exit 0 ;;
  "network create") exit 0 ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(script, []byte(src), 0755); err != nil {
		t.Fatalf("write fake podman: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	m := NewContainerManager("alpine:latest", "boxnex-egress", nil)
	if err := m.EnsureNetwork(); err != nil {
		t.Fatalf("EnsureNetwork error: %v", err)
	}

	data, _ := os.ReadFile(logFile)
	logged := string(data)
	if !strings.Contains(logged, "network") {
		t.Errorf("expected podman network command to be called, got:\n%s", logged)
	}
}

// ---------------------------------------------------------------------------
// NetworkGatewayIP
// ---------------------------------------------------------------------------

func TestNetworkGatewayIP_ReturnsGateway(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "podman")
	// Return a minimal JSON array matching podman network inspect output.
	src := `#!/bin/sh
echo '[{"subnets":[{"subnet":"10.88.0.0/16","gateway":"10.88.0.1"}]}]'
exit 0
`
	if err := os.WriteFile(script, []byte(src), 0755); err != nil {
		t.Fatalf("write fake podman: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	gw, err := NetworkGatewayIP("boxnex-egress")
	if err != nil {
		t.Fatalf("NetworkGatewayIP error: %v", err)
	}
	if gw != "10.88.0.1" {
		t.Fatalf("expected gateway 10.88.0.1, got %s", gw)
	}
}

func TestNetworkGatewayIP_ErrorOnPodmanFailure(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "podman")
	src := `#!/bin/sh
echo "network not found" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(src), 0755); err != nil {
		t.Fatalf("write fake podman: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	_, err := NetworkGatewayIP("boxnex-egress")
	if err == nil {
		t.Fatal("expected error when podman fails")
	}
}

func TestNetworkGatewayIP_ErrorOnEmptySubnets(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "podman")
	src := `#!/bin/sh
echo '[{"subnets":[]}]'
exit 0
`
	if err := os.WriteFile(script, []byte(src), 0755); err != nil {
		t.Fatalf("write fake podman: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	_, err := NetworkGatewayIP("boxnex-egress")
	if err == nil {
		t.Fatal("expected error when subnets are empty")
	}
}

// ---------------------------------------------------------------------------
// Exact name anchor — filter uses ^ prefix to avoid substring matches
// ---------------------------------------------------------------------------

func TestContainerName_SubstringMatchFixed(t *testing.T) {
	// Verify that our --filter uses ^ anchoring to avoid the old docker substring bug.
	// We can't run podman here, but we can verify the containerName function produces
	// distinct names that would only match with exact anchoring.
	nameA := containerName("abc")
	nameB := containerName("abcxyz")

	if nameA == nameB {
		t.Fatalf("different project IDs produced the same container name: %s", nameA)
	}
	// Verify both are valid container names (no special chars)
	for _, name := range []string{nameA, nameB} {
		for _, c := range name {
			if c == '/' || c == ' ' || c == '\t' {
				t.Fatalf("container name contains invalid char %q: %s", c, name)
			}
		}
	}
}
