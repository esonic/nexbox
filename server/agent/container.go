package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// injectSOHostPath is the path on the host where the LD_PRELOAD inject.so lives.
// ContainerManager bind-mounts this file into each container at /usr/lib/inject.so
// when it is present, enabling token injection without embedding the token in the image.
const injectSOHostPath = "/opt/boxnex/inject.so"

// ContainerQuota holds per-user resource limits for Docker containers.
type ContainerQuota struct {
	CPU    string `json:"cpu"`    // e.g. "1.0"; empty = default (1.0)
	Memory string `json:"memory"` // e.g. "2g"; empty = default (2g)
	Pids   int    `json:"pids"`   // 0 = default (200)
}

// ContainerManager manages per-project Docker containers for isolated agent execution.
// Uses Docker rootless + Sysbox for full DinD capability.
type ContainerManager struct {
	image        string
	network      string
	mu           sync.Mutex
	lastActivity map[string]time.Time
}

// NewContainerManager creates a ContainerManager using the given image and network.
// Defaults: "boxnex-agent:latest" / "boxnex-net".
func NewContainerManager(image, network string) *ContainerManager {
	if image == "" {
		image = "boxnex-agent:latest"
	}
	if network == "" {
		network = "boxnex-net"
	}
	return &ContainerManager{
		image:        image,
		network:      network,
		lastActivity: make(map[string]time.Time),
	}
}

// EnsureNetwork creates the Docker network if it does not already exist.
func (m *ContainerManager) EnsureNetwork() error {
	out, err := exec.Command("docker", "network", "ls",
		"--filter", "name=^"+m.network+"$",
		"--format", "{{.Name}}",
	).Output()
	if err != nil {
		return fmt.Errorf("docker network ls: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == m.network {
			return nil // already exists
		}
	}
	var stderr bytes.Buffer
	cmd := exec.Command("docker", "network", "create", m.network)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker network create %s: %w; stderr: %s", m.network, err, stderr.String())
	}
	log.Printf("[container] created network %s", m.network)
	return nil
}

// NetworkGatewayIP returns the gateway IP of the named Docker network.
// Used for tc qdisc per-container bandwidth limiting.
func NetworkGatewayIP(network string) (string, error) {
	out, err := exec.Command("docker", "network", "inspect", network).Output()
	if err != nil {
		return "", fmt.Errorf("docker network inspect %s: %w", network, err)
	}
	var nets []struct {
		IPAM struct {
			Config []struct {
				Gateway string `json:"Gateway"`
			} `json:"Config"`
		} `json:"IPAM"`
	}
	if err := json.Unmarshal(out, &nets); err != nil {
		return "", fmt.Errorf("parse network inspect output: %w", err)
	}
	if len(nets) == 0 || len(nets[0].IPAM.Config) == 0 || nets[0].IPAM.Config[0].Gateway == "" {
		return "", fmt.Errorf("no gateway found for network %s", network)
	}
	return nets[0].IPAM.Config[0].Gateway, nil
}

// RecoverRunningContainers scans for containers with the boxnex.project label
// and re-registers their project IDs in lastActivity so they are not stopped
// immediately after restart (T10: container state recovery).
func (m *ContainerManager) RecoverRunningContainers() {
	out, err := exec.Command("docker", "ps",
		"--filter", "label=boxnex.project",
		"--format", `{{.Label "boxnex.project"}}`,
	).Output()
	if err != nil {
		log.Printf("[container] failed to list running containers on recovery: %v", err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, projectID := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		projectID = strings.TrimSpace(projectID)
		if projectID == "" {
			continue
		}
		m.lastActivity[projectID] = time.Now()
		log.Printf("[container] re-registered running container for project %s", projectID)
	}
}

// containerName returns a deterministic, Docker-safe container name for a project.
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
// userID is used to create/mount a persistent home volume (boxnex-home-<userID>).
// quota overrides default resource limits; nil uses defaults.
// Returns the container name that can be passed to ExecInContainer / ExecClaudeInContainer.
func (m *ContainerManager) EnsureContainer(projectID, repoPath, userID string, quota *ContainerQuota) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := containerName(projectID)

	out, err := exec.Command(
		"docker", "ps",
		"--filter", "name=^"+name+"$",
		"--format", "{{.ID}}",
	).Output()
	if err != nil {
		return "", fmt.Errorf("docker ps: %w", err)
	}
	if id := strings.TrimSpace(string(out)); id != "" {
		log.Printf("[container] reusing existing container %s for project %s", name, projectID)
		m.lastActivity[projectID] = time.Now()
		return name, nil
	}

	// Resolve resource limits from quota (fall back to defaults).
	memory := "2g"
	cpus := "1.0"
	pids := "200"
	if quota != nil {
		if quota.Memory != "" {
			memory = quota.Memory
		}
		if quota.CPU != "" {
			cpus = quota.CPU
		}
		if quota.Pids > 0 {
			pids = fmt.Sprintf("%d", quota.Pids)
		}
	}

	startArgs := []string{
		"run", "-d",
		"--name", name,
		"--rm",
		"--runtime", "sysbox-runc",
		"--memory", memory,
		"--memory-swap", memory,
		"--cpus", cpus,
		"--pids-limit", pids,
		"--network", m.network,
		"--label", "boxnex.project=" + projectID,
		"-v", repoPath + ":/workspace",
		"-w", "/workspace",
		"-e", "CLAUDE_CONFIG_DIR=/workspace/.claude",
		"-e", "USE_BUILTIN_RIPGREP=0",
		"-e", "DISABLE_TELEMETRY=1",
		"-e", "DISABLE_AUTOUPDATER=1",
	}

	// T6: Per-user persistent home volume.
	if userID != "" {
		volumeName := "boxnex-home-" + userID
		startArgs = append(startArgs, "-v", volumeName+":/root")
	}

	// Mount inject.so for LD_PRELOAD token injection if available on the host.
	if _, err := os.Stat(injectSOHostPath); err == nil {
		startArgs = append(startArgs, "-v", injectSOHostPath+":/usr/lib/inject.so:ro")
	} else {
		log.Printf("[container] warning: %s not found; CLAUDE_CODE_OAUTH_TOKEN will not be injected", injectSOHostPath)
	}

	startArgs = append(startArgs, m.image, "tail", "-f", "/dev/null")

	var stderr bytes.Buffer
	startCmd := exec.Command("docker", startArgs...)
	startCmd.Stderr = &stderr
	startOut, err := startCmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker run: %w; stderr: %s", err, stderr.String())
	}
	newID := strings.TrimSpace(string(startOut))
	log.Printf("[container] started container %s (id=%s) for project %s", name, newID, projectID)

	m.lastActivity[projectID] = time.Now()
	return name, nil
}

// StopContainer stops the container for a project.
func (m *ContainerManager) StopContainer(projectID string) error {
	name := containerName(projectID)
	out, err := exec.Command("docker", "stop", name).CombinedOutput()

	m.mu.Lock()
	delete(m.lastActivity, projectID)
	m.mu.Unlock()

	if err != nil {
		log.Printf("[container] stop container %s: %v (%s)", name, err, strings.TrimSpace(string(out)))
		return fmt.Errorf("docker stop %s: %w", name, err)
	}
	log.Printf("[container] stopped container %s for project %s", name, projectID)
	return nil
}

// TouchActivity records user activity for the given project, resetting its idle timer.
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
		name := containerName(pid)
		out, err := exec.Command("docker", "stop", name).CombinedOutput()

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

// ExecInContainer returns an *exec.Cmd that runs cmd inside the named container.
// env entries must be in KEY=VALUE format.
func (m *ContainerManager) ExecInContainer(containerID string, cmd []string, env []string, workDir string) *exec.Cmd {
	args := []string{"exec", "-i"}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	if workDir != "" {
		args = append(args, "-w", workDir)
	}
	args = append(args, containerID)
	args = append(args, cmd...)
	return exec.Command("docker", args...)
}

// ExecClaudeInContainer returns an *exec.Cmd that runs cmd inside the named container.
// If inject.so is present on the host at /opt/boxnex/inject.so (meaning it was
// bind-mounted into the container by EnsureContainer), LD_PRELOAD is set so the
// constructor injects CLAUDE_CODE_OAUTH_TOKEN at process startup.
// env entries must be in KEY=VALUE format.
func (m *ContainerManager) ExecClaudeInContainer(containerID string, cmd []string, env []string, workDir string) *exec.Cmd {
	args := []string{"exec", "-i"}
	if _, err := os.Stat(injectSOHostPath); err == nil {
		args = append(args, "-e", "LD_PRELOAD=/usr/lib/inject.so")
	}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	if workDir != "" {
		args = append(args, "-w", workDir)
	}
	args = append(args, containerID)
	args = append(args, cmd...)
	return exec.Command("docker", args...)
}

// WatchWorkspace starts a background goroutine that periodically checks the size
// of workDir. When usage exceeds quotaBytes the provided cancel function is called.
// The goroutine exits when ctx is cancelled.
func (m *ContainerManager) WatchWorkspace(ctx context.Context, workDir string, quotaBytes int64, cancel context.CancelFunc) {
	if quotaBytes <= 0 || workDir == "" {
		return
	}
	interval := 10 * time.Second
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
func workspaceSize(dir string) (int64, error) {
	out, err := exec.Command("du", "-sb", dir).Output()
	if err != nil {
		return 0, err
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) == 0 {
		return 0, fmt.Errorf("unexpected du output: %q", string(out))
	}
	var size int64
	_, err = fmt.Sscanf(parts[0], "%d", &size)
	return size, err
}
