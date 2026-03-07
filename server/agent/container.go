package agent

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
)

// ContainerManager manages per-project Docker containers for isolated agent execution.
// It uses the docker CLI (not the Docker SDK) to avoid adding new dependencies.
type ContainerManager struct {
	image string
	mu    sync.Mutex
}

// NewContainerManager creates a ContainerManager using the given image.
// If image is empty, it defaults to "nexbox-agent:latest".
func NewContainerManager(image string) *ContainerManager {
	if image == "" {
		image = "nexbox-agent:latest"
	}
	return &ContainerManager{image: image}
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
	return "nexbox-proj-" + id
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
		"--filter", "name="+name,
		"--format", "{{.ID}}",
	).Output()
	if err != nil {
		return "", fmt.Errorf("docker ps: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if id != "" {
		log.Printf("[container] reusing existing container %s (id=%s) for project %s", name, id, projectID)
		return name, nil
	}

	// Start a new detached container that stays alive indefinitely.
	startArgs := []string{
		"run", "-d",
		"--name", name,
		"--rm",
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
	return name, nil
}

// StopContainer stops (and removes, because of --rm) the container for a project.
// It is safe to call even if the container is not running.
func (m *ContainerManager) StopContainer(projectID string) error {
	name := containerName(projectID)
	out, err := exec.Command("docker", "stop", name).CombinedOutput()
	if err != nil {
		// docker stop exits non-zero when the container doesn't exist — treat as no-op.
		log.Printf("[container] stop container %s: %v (%s)", name, err, strings.TrimSpace(string(out)))
		return fmt.Errorf("docker stop %s: %w", name, err)
	}
	log.Printf("[container] stopped container %s for project %s", name, projectID)
	return nil
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
