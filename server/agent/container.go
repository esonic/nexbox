package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ContainerManager manages per-project Podman containers for isolated agent execution.
// Podman runs in rootless mode — no daemon, no root socket.
type ContainerManager struct {
	image         string
	network       string             // Podman network name (default: "boxnex-egress")
	proxy         *ContainerProxy    // Optional: egress proxy for API-key relay + whitelist
	mu            sync.Mutex
	lastActivity  map[string]time.Time   // projectID → last activity time
	sessionTokens map[string]string      // projectID → session token (fake API key)
	DiskQuotaBytes  int64                 // workspace quota; 0 = no monitoring
	WatchInterval   time.Duration         // workspace check interval; 0 = default (10s)
}

// NewContainerManager creates a ContainerManager using the given image and network.
// If image or network are empty they fall back to "boxnex-agent:latest" / "boxnex-egress".
// proxy may be nil; when set, a session token is generated per container and the
// proxy is configured to relay Anthropic API calls for that session.
func NewContainerManager(image, network string, proxy *ContainerProxy) *ContainerManager {
	if image == "" {
		image = "boxnex-agent:latest"
	}
	if network == "" {
		network = "boxnex-egress"
	}
	return &ContainerManager{
		image:         image,
		network:       network,
		proxy:         proxy,
		lastActivity:  make(map[string]time.Time),
		sessionTokens: make(map[string]string),
	}
}

// EnsureNetwork creates the Podman network if it does not already exist.
// Safe to call on every startup; it is a no-op when the network is present.
func (m *ContainerManager) EnsureNetwork() error {
	// Check existence first to avoid spamming logs.
	// Use substring filter to narrow results, then exact-match each line in case
	// the filter returns multiple entries (Podman filter is substring-based).
	out, err := exec.Command("podman", "network", "ls",
		"--filter", "name="+m.network,
		"--format", "{{.Name}}",
	).Output()
	if err != nil {
		return fmt.Errorf("podman network ls: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == m.network {
			return nil // already exists
		}
	}
	var stderr bytes.Buffer
	cmd := exec.Command("podman", "network", "create",
		"--opt", "isolate=true", // disable container-to-container traffic
		m.network,
	)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("podman network create %s: %w; stderr: %s", m.network, err, stderr.String())
	}
	log.Printf("[container] created network %s", m.network)
	return nil
}

// NetworkGatewayIP returns the gateway IP of the named Podman network.
// Used to bind the egress proxy to the network-internal interface rather than
// 0.0.0.0, so the proxy is not reachable from external network interfaces.
func NetworkGatewayIP(network string) (string, error) {
	out, err := exec.Command("podman", "network", "inspect", network).Output()
	if err != nil {
		return "", fmt.Errorf("podman network inspect %s: %w", network, err)
	}
	var nets []struct {
		Subnets []struct {
			Gateway string `json:"gateway"`
		} `json:"subnets"`
	}
	if err := json.Unmarshal(out, &nets); err != nil {
		return "", fmt.Errorf("parse network inspect output: %w", err)
	}
	if len(nets) == 0 || len(nets[0].Subnets) == 0 || nets[0].Subnets[0].Gateway == "" {
		return "", fmt.Errorf("no gateway found for network %s", network)
	}
	return nets[0].Subnets[0].Gateway, nil
}

// SetProxy wires a ContainerProxy into the manager after construction.
// Useful when the proxy listen address depends on the network gateway IP,
// which is only known after EnsureNetwork() has run.
func (m *ContainerManager) SetProxy(proxy *ContainerProxy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.proxy = proxy
}

// RecoverRunningContainers stops any boxnex-proj-* containers left over from a
// previous server run. Their session tokens are gone (in-memory state was lost),
// so letting them continue would cause every API call to return 401. Stopping
// them here means they will be restarted with fresh tokens on next use.
func (m *ContainerManager) RecoverRunningContainers() {
	out, err := exec.Command(
		"podman", "ps",
		"--filter", "name=boxnex-proj-",
		"--format", "{{.Names}}",
	).Output()
	if err != nil {
		log.Printf("[container] failed to list stale containers on recovery: %v", err)
		return
	}
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if err := exec.Command("podman", "stop", name).Run(); err != nil {
			log.Printf("[container] failed to stop stale container %s: %v", name, err)
		} else {
			log.Printf("[container] stopped stale container %s (session tokens lost on restart)", name)
		}
	}
}

// containerName returns a deterministic, Podman-safe container name for a project.
func containerName(projectID string) string {
	safe := true
	for _, c := range projectID {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			safe = false
			break
		}
	}
	id := projectID
	if !safe || len(id) > 48 {
		h := sha256.Sum256([]byte(projectID))
		id = fmt.Sprintf("%x", h[:8]) // 16 hex chars
	}
	return "boxnex-proj-" + id
}

// generateSessionToken produces a cryptographically random token injected into
// the container as CLAUDE_CODE_OAUTH_TOKEN. Claude CLI (claude-code agent) reads
// this env var and sends it as Authorization: Bearer — the proxy intercepts it,
// validates the session, and substitutes the real CLAUDE_CODE_OAUTH_TOKEN.
func generateSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "bxs-" + hex.EncodeToString(b), nil // "bxs-" prefix for easy identification
}

// EnsureContainer ensures a container for the given project is running.
// Returns the container name that can be passed to ExecInContainer.
func (m *ContainerManager) EnsureContainer(projectID, repoPath string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := containerName(projectID)

	// Check if a container with this exact name is already running.
	out, err := exec.Command(
		"podman", "ps",
		"--filter", "name=^"+name+"$",
		"--format", "{{.ID}}",
	).Output()
	if err != nil {
		return "", fmt.Errorf("podman ps: %w", err)
	}
	if id := strings.TrimSpace(string(out)); id != "" {
		log.Printf("[container] reusing existing container %s for project %s", name, projectID)
		m.lastActivity[projectID] = time.Now()
		return name, nil
	}

	// Generate session token for this container instance.
	sessionToken, err := generateSessionToken()
	if err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}

	// Ensure workspace is writable by the container user (uid 1000).
	// With --userns=keep-id the service-account uid maps to itself inside the
	// container, but --user 1000:1000 runs the process as uid 1000 regardless.
	// 0777 ensures Claude can always write to /workspace independent of host uid.
	if err := os.Chmod(repoPath, 0777); err != nil {
		log.Printf("[container] warning: could not chmod workspace %s: %v", repoPath, err)
	}

	// Determine proxy address for environment injection.
	proxyEnv := ""
	baseURL := ""
	if m.proxy != nil {
		proxyAddr := m.proxy.Addr()
		proxyEnv = "http://" + proxyAddr
		baseURL = "http://" + proxyAddr
		m.proxy.RegisterSession(sessionToken, projectID)
	}

	// Build the podman run arguments.
	// Security posture (all P0/P1 measures from container_security_tasks.md):
	//   --userns=keep-id        map host user → container user (rootless-friendly)
	//   --user 1000:1000        run as non-root agent user inside container
	//   --security-opt no-new-privileges  prevent setuid escalation
	//   --cap-drop ALL          drop all capabilities; add back only what is needed
	//   --cap-add CHOWN,DAC_OVERRIDE,FOWNER,SETUID,SETGID  minimum for git/npm/bash
	//   --tmpfs /tmp            writable temp space without exposing host /tmp
	//   --memory-swap           equal to --memory to disable swap
	//   --network               dedicated egress network (boxnex-egress)
	//   CLAUDE_CONFIG_DIR       session data stored under /workspace/.claude (no extra mount)
	startArgs := []string{
		"run", "-d",
		"--name", name,
		"--rm",
		"--memory", "4g",
		"--memory-swap", "4g",
		"--cpus", "1.0",
		"--pids-limit", "200",
		"--network", m.network,
		"--userns=keep-id",
		"--user", "1000:1000",
		"--security-opt", "no-new-privileges",
		"--cap-drop", "ALL",
		"--cap-add", "CHOWN",
		"--cap-add", "DAC_OVERRIDE",
		"--cap-add", "FOWNER",
		"--cap-add", "SETUID",
		"--cap-add", "SETGID",
		"--tmpfs", "/tmp:size=256m,noexec,nosuid",
		"-v", repoPath + ":/workspace",
		"-w", "/workspace",
		// Claude CLI environment tuning
		"-e", "CLAUDE_CONFIG_DIR=/workspace/.claude", // persist session under workspace (no extra mount needed)
		"-e", "USE_BUILTIN_RIPGREP=0",                // use system ripgrep
		"-e", "DISABLE_TELEMETRY=1",                  // no telemetry
		"-e", "DISABLE_AUTOUPDATER=1",                // no auto-update inside container
		// Proxy environment: containers use HTTP_PROXY for general egress
		// and ANTHROPIC_BASE_URL so Claude CLI sends API calls through our relay.
	}

	if proxyEnv != "" {
		startArgs = append(startArgs,
			"-e", "HTTP_PROXY="+proxyEnv,
			"-e", "HTTPS_PROXY="+proxyEnv,
			"-e", "NO_PROXY=localhost,127.0.0.1",
		)
	}
	if baseURL != "" {
		startArgs = append(startArgs, "-e", "ANTHROPIC_BASE_URL="+baseURL)
	}

	// Inject a one-time fake session token as CLAUDE_CODE_OAUTH_TOKEN — this is the
	// env var Claude CLI (claude-code agent) reads for authentication.
	// The proxy intercepts the Bearer header carrying this fake token, validates the
	// session, and substitutes the real CLAUDE_CODE_OAUTH_TOKEN before forwarding.
	startArgs = append(startArgs, "-e", "CLAUDE_CODE_OAUTH_TOKEN="+sessionToken)

	startArgs = append(startArgs, m.image, "tail", "-f", "/dev/null")

	var stderr bytes.Buffer
	startCmd := exec.Command("podman", startArgs...)
	startCmd.Stderr = &stderr
	startOut, err := startCmd.Output()
	if err != nil {
		// Revoke the session token if container start failed.
		if m.proxy != nil {
			m.proxy.RevokeSession(sessionToken)
		}
		return "", fmt.Errorf("podman run: %w; stderr: %s", err, stderr.String())
	}
	newID := strings.TrimSpace(string(startOut))
	log.Printf("[container] started container %s (id=%s) for project %s", name, newID, projectID)

	m.lastActivity[projectID] = time.Now()
	m.sessionTokens[projectID] = sessionToken
	return name, nil
}

// StopContainer stops the container for a project and revokes its session token.
// It is safe to call even if the container is not running.
func (m *ContainerManager) StopContainer(projectID string) error {
	name := containerName(projectID)
	out, err := exec.Command("podman", "stop", name).CombinedOutput()

	m.mu.Lock()
	token := m.sessionTokens[projectID]
	delete(m.lastActivity, projectID)
	delete(m.sessionTokens, projectID)
	m.mu.Unlock()

	if token != "" && m.proxy != nil {
		m.proxy.RevokeSession(token)
	}

	if err != nil {
		log.Printf("[container] stop container %s: %v (%s)", name, err, strings.TrimSpace(string(out)))
		return fmt.Errorf("podman stop %s: %w", name, err)
	}
	log.Printf("[container] stopped container %s for project %s", name, projectID)
	return nil
}

// TouchActivity records a user activity for the given project, resetting its idle timer.
func (m *ContainerManager) TouchActivity(projectID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastActivity[projectID] = time.Now()
}

// StopIdleContainers stops containers that have been idle for longer than maxIdle.
// Returns the number of containers stopped.
func (m *ContainerManager) StopIdleContainers(maxIdle time.Duration) int {
	m.mu.Lock()
	cutoff := time.Now().Add(-maxIdle)
	var idle []string
	for pid, t := range m.lastActivity {
		if t.Before(cutoff) {
			idle = append(idle, pid)
		}
	}
	m.mu.Unlock()

	stopped := 0
	for _, pid := range idle {
		name := pid
		if !strings.HasPrefix(pid, "boxnex-proj-") {
			name = containerName(pid)
		}
		out, err := exec.Command("podman", "stop", name).CombinedOutput()

		m.mu.Lock()
		token := m.sessionTokens[pid]
		delete(m.lastActivity, pid)
		delete(m.sessionTokens, pid)
		m.mu.Unlock()

		if token != "" && m.proxy != nil {
			m.proxy.RevokeSession(token)
		}

		if err != nil {
			log.Printf("[container] idle stop %s: %v (%s)", name, err, strings.TrimSpace(string(out)))
		} else {
			log.Printf("[container] stopped idle container %s", name)
			stopped++
		}
	}
	return stopped
}

// ExecInContainer returns an *exec.Cmd that runs cmd inside the named container.
// env entries must be in KEY=VALUE format.
// workDir is the working directory inside the container (empty = container default).
func (m *ContainerManager) ExecInContainer(containerID string, cmd []string, env []string, workDir string) *exec.Cmd {
	args := []string{"exec", "-i",
		"--user", "1000:1000", // exec processes must also run as non-root (U-04)
	}

	for _, e := range env {
		args = append(args, "-e", e)
	}
	if workDir != "" {
		args = append(args, "-w", workDir)
	}

	args = append(args, containerID)
	args = append(args, cmd...)

	return exec.Command("podman", args...)
}

// WatchWorkspace starts a background goroutine that periodically checks the size
// of workDir. When usage exceeds quotaBytes the provided cancel function is called,
// terminating the agent task. The goroutine exits when ctx is cancelled.
//
// Call this from the agent's SendMessage after setting up budgetCtx/budgetCancel.
func (m *ContainerManager) WatchWorkspace(ctx context.Context, workDir string, quotaBytes int64, cancel context.CancelFunc) {
	if quotaBytes <= 0 || workDir == "" {
		return
	}
	interval := m.WatchInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				used, err := workspaceSize(workDir)
				if err != nil {
					log.Printf("[container] workspace size check error (%s): %v", workDir, err)
					continue
				}
				if used >= quotaBytes {
					log.Printf("[container] workspace quota exceeded: used=%dMB limit=%dMB path=%s; cancelling agent",
						used>>20, quotaBytes>>20, workDir)
					cancel()
					return
				}
			}
		}
	}()
}

// workspaceSize returns the total byte size of a directory using the `du` command.
// This is faster than filepath.Walk for large directories as it leverages kernel VFS.
func workspaceSize(dir string) (int64, error) {
	out, err := exec.Command("du", "-sb", dir).Output()
	if err != nil {
		return 0, err
	}
	// du output: "<bytes>\t<path>"
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) == 0 {
		return 0, fmt.Errorf("unexpected du output: %q", string(out))
	}
	var size int64
	_, err = fmt.Sscanf(parts[0], "%d", &size)
	return size, err
}
