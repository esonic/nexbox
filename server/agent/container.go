package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ContainerManager manages per-project Podman containers for isolated agent execution.
// Podman runs in rootless mode — no daemon, no root socket.
type ContainerManager struct {
	image          string
	network        string          // Podman network name (default: "boxnex-egress")
	proxy          *ContainerProxy // Optional: egress whitelist proxy
	RealAPIKey     string          // CLAUDE_CODE_OAUTH_TOKEN injected only into ExecClaudeInContainer (UID 9999)
	mu             sync.Mutex
	lastActivity   map[string]time.Time // projectID → last activity time
	DiskQuotaBytes int64                // workspace quota; 0 = no monitoring
	WatchInterval  time.Duration        // workspace check interval; 0 = default (10s)
}

// NewContainerManager creates a ContainerManager using the given image and network.
// If image or network are empty they fall back to "boxnex-agent:latest" / "boxnex-egress".
// proxy may be nil; when set, containers are configured to route egress through it.
func NewContainerManager(image, network string, proxy *ContainerProxy) *ContainerManager {
	if image == "" {
		image = "boxnex-agent:latest"
	}
	if network == "" {
		network = "boxnex-egress"
	}
	return &ContainerManager{
		image:        image,
		network:      network,
		proxy:        proxy,
		lastActivity: make(map[string]time.Time),
	}
}

// EnsureNetwork creates the Podman network if it does not already exist.
// Safe to call on every startup; it is a no-op when the network is present.
func (m *ContainerManager) EnsureNetwork() error {
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

// HostOutboundIP returns the host's primary outbound IP address by connecting a
// UDP socket to a public IP (no packet is actually sent). This is the IP that
// containers on Podman rootless + pasta networks can use to reach the host when
// the bridge gateway is virtual and not bound to a real host interface.
func HostOutboundIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
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
// previous server run. Their in-memory state is lost on restart, so we stop them
// here — they will be recreated with fresh state on next use.
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
			log.Printf("[container] stopped stale container %s on recovery", name)
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

// EnsureContainer ensures a container for the given project is running.
// Returns the container name that can be passed to ExecInContainer / ExecClaudeInContainer.
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

	// 0777 ensures both the agent user (UID 1000) and the claude-runner user
	// (UID 9999) can write to /workspace. With --userns=keep-id the host UID
	// maps directly inside the container, but UID 9999 maps through the subuid
	// range — making the directory world-writable is the simplest way to give
	// both users write access without requiring host-side chown (which needs root).
	if err := os.Chmod(repoPath, 0777); err != nil {
		log.Printf("[container] warning: could not chmod workspace %s: %v", repoPath, err)
	}

	// Determine proxy address for egress routing.
	proxyEnv := ""
	if m.proxy != nil {
		proxyEnv = "http://" + m.proxy.ContainerAddr()
	}

	// Build the podman run arguments.
	//
	// Security posture:
	//   --userns=keep-id        map host user → same UID inside container
	//   --security-opt no-new-privileges  prevent setuid escalation
	//   --cap-drop ALL          drop all capabilities
	//   --cap-add CHOWN,DAC_OVERRIDE,FOWNER  minimum for git/file ops
	//   --tmpfs /tmp            writable temp space, noexec+nosuid
	//   --memory-swap = --memory  disable swap
	//   --network               dedicated egress network (boxnex-egress)
	//   CLAUDE_CONFIG_DIR       session data under /workspace/.claude (no extra mount)
	//
	// Note: SETUID/SETGID are intentionally NOT added. Switching to the
	// claude-runner user (UID 9999) is handled externally via `podman exec -u`
	// in ExecClaudeInContainer — no in-container privilege escalation needed.
	startArgs := []string{
		"run", "-d",
		"--name", name,
		"--rm",
		"--memory", "2g",
		"--memory-swap", "2g",
		"--cpus", "1.0",
		"--pids-limit", "200",
		"--network", m.network,
		"--userns=keep-id",
		"--security-opt", "no-new-privileges",
		"--cap-drop", "ALL",
		"--cap-add", "CHOWN",
		"--cap-add", "DAC_OVERRIDE",
		"--cap-add", "FOWNER",
		"--tmpfs", "/tmp:size=256m,noexec,nosuid",
		"-v", repoPath + ":/workspace",
		"-w", "/workspace",
		"-e", "CLAUDE_CONFIG_DIR=/workspace/.claude",
		"-e", "USE_BUILTIN_RIPGREP=0",
		"-e", "DISABLE_TELEMETRY=1",
		"-e", "DISABLE_AUTOUPDATER=1",
	}

	if proxyEnv != "" {
		startArgs = append(startArgs,
			"-e", "HTTP_PROXY="+proxyEnv,
			"-e", "HTTPS_PROXY="+proxyEnv,
			"-e", "NO_PROXY=localhost,127.0.0.1",
		)
	}

	startArgs = append(startArgs, m.image, "tail", "-f", "/dev/null")

	var stderr bytes.Buffer
	startCmd := exec.Command("podman", startArgs...)
	startCmd.Stderr = &stderr
	startOut, err := startCmd.Output()
	if err != nil {
		return "", fmt.Errorf("podman run: %w; stderr: %s", err, stderr.String())
	}
	newID := strings.TrimSpace(string(startOut))
	log.Printf("[container] started container %s (id=%s) for project %s", name, newID, projectID)

	m.lastActivity[projectID] = time.Now()
	return name, nil
}

// StopContainer stops the container for a project.
// It is safe to call even if the container is not running.
func (m *ContainerManager) StopContainer(projectID string) error {
	name := containerName(projectID)
	out, err := exec.Command("podman", "stop", name).CombinedOutput()

	m.mu.Lock()
	delete(m.lastActivity, projectID)
	m.mu.Unlock()

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
		delete(m.lastActivity, pid)
		m.mu.Unlock()

		if err != nil {
			log.Printf("[container] idle stop %s: %v (%s)", name, err, strings.TrimSpace(string(out)))
		} else {
			log.Printf("[container] stopped idle container %s", name)
			stopped++
		}
	}
	return stopped
}

// ExecInContainer returns an *exec.Cmd that runs cmd inside the named container
// as the agent user (UID 1000:1000). Use for general file/git operations.
// env entries must be in KEY=VALUE format.
func (m *ContainerManager) ExecInContainer(containerID string, cmd []string, env []string, workDir string) *exec.Cmd {
	args := []string{"exec", "-i",
		"--user", "1000:1000",
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

// ExecClaudeInContainer returns an *exec.Cmd that runs cmd inside the named
// container as the claude-runner user (UID 9999:9999) with the real API key
// injected via -e. Running as a separate UID means the agent user (1000) cannot
// read the API key from /proc/<pid>/environ (no ptrace capability in container).
// env entries must be in KEY=VALUE format.
func (m *ContainerManager) ExecClaudeInContainer(containerID string, cmd []string, env []string, workDir string) *exec.Cmd {
	args := []string{"exec", "-i",
		"--user", "9999:9999",
	}
	// Inject real API key only at exec time — never stored in the container's
	// persistent environment (not in `podman run` args).
	if m.RealAPIKey != "" {
		args = append(args, "-e", "CLAUDE_CODE_OAUTH_TOKEN="+m.RealAPIKey)
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

// workspaceSize returns the total byte size of a directory using `du -sb`.
// Consistent with the dirSize implementation in api/files.go.
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
