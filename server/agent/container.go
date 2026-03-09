package agent

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ContainerManager manages per-project Docker containers for isolated agent execution.
// It uses the docker CLI (not the Docker SDK) to avoid adding new dependencies.
type ContainerManager struct {
	image        string
	mu           sync.Mutex
	lastActivity map[string]time.Time // projectID → last activity time
}

// NewContainerManager creates a ContainerManager using the given image.
// If image is empty, it defaults to "boxnex-agent:latest".
func NewContainerManager(image string) *ContainerManager {
	if image == "" {
		image = "boxnex-agent:latest"
	}
	return &ContainerManager{image: image, lastActivity: make(map[string]time.Time)}
}

// RecoverRunningContainers scans for existing boxnex-proj-* containers and seeds
// the lastActivity map so the idle reaper can track them after a server restart.
func (m *ContainerManager) RecoverRunningContainers() {
	out, err := exec.Command(
		"docker", "ps",
		"--filter", "name=boxnex-proj-",
		"--format", "{{.Names}}",
	).Output()
	if err != nil {
		log.Printf("[container] failed to recover running containers: %v", err)
		return
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		// Store container name as key (not projectID, which we can't reverse
		// from the possibly-hashed name). StopIdleContainers handles both forms.
		m.lastActivity[name] = now
		log.Printf("[container] recovered running container: %s", name)
	}
}

// containerName returns a deterministic, Docker-safe container name for a project.
// projectID may be a UUID, a short token, or a sanitized path — we hash it to
// guarantee the result is always a valid Docker name (alphanumeric and hyphens only).
func containerName(projectID string) string {
	// If the ID is already short and safe (e.g. UUID without hyphens issues), use it directly.
	// Otherwise hash to get a fixed-length safe identifier.
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
		id = fmt.Sprintf("%x", h[:8]) // 16 hex chars, unique enough
	}
	return "boxnex-proj-" + id
}

// EnsureContainer ensures a container for the given project is running.
// It returns the container ID (or name) that can be passed to ExecInContainer.
func (m *ContainerManager) EnsureContainer(projectID, repoPath string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := containerName(projectID)

	// Check if a container with this name is already running.
	out, err := exec.Command(
		"docker", "ps",
		"--filter", "name=^/"+name+"$",
		"--format", "{{.ID}}",
	).Output()
	if err != nil {
		return "", fmt.Errorf("docker ps: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if id != "" {
		log.Printf("[container] reusing existing container %s (id=%s) for project %s", name, id, projectID)
		m.lastActivity[projectID] = time.Now()
		return name, nil
	}

	// Start a new detached container that stays alive indefinitely.
	startArgs := []string{
		"run", "-d",
		"--name", name,
		"--rm",
		"--memory", "4g",
		"--cpus", "1.0",
		"--pids-limit", "200",
		"--storage-opt", "size=1g",
		"-v", repoPath + ":/workspace",
		"-w", "/workspace",
		m.image,
		"tail", "-f", "/dev/null",
	}
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

// StopContainer stops (and removes, because of --rm) the container for a project.
// It is safe to call even if the container is not running.
func (m *ContainerManager) StopContainer(projectID string) error {
	name := containerName(projectID)
	out, err := exec.Command("docker", "stop", name).CombinedOutput()

	m.mu.Lock()
	delete(m.lastActivity, projectID)
	m.mu.Unlock()

	if err != nil {
		// docker stop exits non-zero when the container doesn't exist — treat as no-op.
		log.Printf("[container] stop container %s: %v (%s)", name, err, strings.TrimSpace(string(out)))
		return fmt.Errorf("docker stop %s: %w", name, err)
	}
	log.Printf("[container] stopped container %s for project %s", name, projectID)
	return nil
}

// TouchActivity records a user activity for the given project, resetting its
// idle timer. Call this from operations that indicate the user is still working
// with the project (e.g. file upload/download).
func (m *ContainerManager) TouchActivity(projectID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastActivity[projectID] = time.Now()
}

// StopIdleContainers stops containers that have been idle for longer than maxIdle.
// It returns the number of containers stopped.
func (m *ContainerManager) StopIdleContainers(maxIdle time.Duration) int {
	m.mu.Lock()
	// Collect idle project IDs under lock, then release before running docker commands.
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
		// pid may be a projectID (normal tracking) or a container name
		// (recovered after restart). Derive the docker name accordingly.
		name := pid
		if !strings.HasPrefix(pid, "boxnex-proj-") {
			name = containerName(pid)
		}
		out, err := exec.Command("docker", "stop", name).CombinedOutput()
		if err != nil {
			// Container may already be gone — just clean up the map entry.
			log.Printf("[container] idle stop %s: %v (%s)", name, err, strings.TrimSpace(string(out)))
		} else {
			log.Printf("[container] stopped idle container %s", name)
			stopped++
		}
		m.mu.Lock()
		delete(m.lastActivity, pid)
		m.mu.Unlock()
	}
	return stopped
}

// ExecInContainer returns an *exec.Cmd that runs cmd inside the named container.
// env entries must be in KEY=VALUE format; they are forwarded via -e flags.
// workDir is the working directory inside the container (empty means the container default).
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
