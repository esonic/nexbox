package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// ClaudeClient wraps the Claude CLI for programmatic use
type ClaudeClient struct {
	CLIPath      string            // Path to claude CLI (default: "claude")
	Env          map[string]string // Additional environment variables
	Verbose      bool              // Enable verbose logging
	ContainerMgr *ContainerManager // Optional: run inside Docker container
	ContainerID  string            // Container name/ID (set when ContainerMgr is active)
}

// NewClaudeClient creates a new Claude CLI client
func NewClaudeClient() *ClaudeClient {
	c := &ClaudeClient{
		CLIPath: "claude",
		Env:     make(map[string]string),
	}
	if dacs := os.Getenv("DACS"); dacs != "" {
		c.Env["CLAUDE_CODE_TMPDIR"] = dacs + "/.claude-tmp"
	}
	return c
}

// QueryOptions contains options for a Claude query
type QueryOptions struct {
	WorkDir        string   // Working directory
	Resume         string   // Session ID to resume
	SessionID      string   // Specific session ID to use
	Model          string   // Model to use
	AllowedTools   []string // List of allowed tools
	PermissionMode string   // Permission mode: default, acceptEdits, plan, etc.
	MCPConfigPath  string   // Path to MCP config file (enables --mcp-config and --strict-mcp-config)
}

// StreamMessage represents a message from Claude CLI stream-json output
type StreamMessage struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	UUID      string          `json:"uuid,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`

	// For result messages
	Result       string                 `json:"result,omitempty"`
	IsError      bool                   `json:"is_error,omitempty"`
	DurationMs   int64                  `json:"duration_ms,omitempty"`
	TotalCostUSD float64                `json:"total_cost_usd,omitempty"`
	Usage        map[string]interface{} `json:"usage,omitempty"` // Can contain nested objects
	NumTurns     int                    `json:"num_turns,omitempty"`
	Errors       []string               `json:"errors,omitempty"`

	// For system messages
	Data map[string]interface{} `json:"data,omitempty"`
}

// AssistantContent represents content from an assistant message
type AssistantContent struct {
	Type     string      `json:"type"`
	Text     string      `json:"text,omitempty"`
	ID       string      `json:"id,omitempty"`
	Name     string      `json:"name,omitempty"`
	Input    interface{} `json:"input,omitempty"`
	Thinking string      `json:"thinking,omitempty"` // For Extended Thinking feature
	// Note: "redacted_thinking" type also exists but contains no accessible content
}

// AssistantMessage represents a parsed assistant message
type AssistantMessage struct {
	Role    string             `json:"role"`
	Content []AssistantContent `json:"content"`
}

// UserInput represents input sent to Claude CLI
type UserInput struct {
	Type      string           `json:"type"`
	Message   UserInputMessage `json:"message"`
	SessionID string           `json:"session_id"`
}

// UserInputMessage represents the message part of user input
type UserInputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// StreamCallback is called for each streaming event
type StreamCallback func(msg StreamMessage, parsed *AssistantMessage)

// Query sends a message to Claude and streams the response
func (c *ClaudeClient) Query(
	ctx context.Context,
	prompt string,
	opts QueryOptions,
	callback StreamCallback,
) (sessionID string, err error) {
	args := c.buildArgs(opts)

	if c.Verbose {
		log.Printf("[claude] CLI command: %s %v", c.CLIPath, args)
		log.Printf("[claude] Prompt: %s", prompt)
	}

	// Build the command — either directly on the host or inside a Docker container.
	var cmd *exec.Cmd
	if c.ContainerMgr != nil && c.ContainerID != "" {
		// Collect env overrides as KEY=VALUE strings for docker exec -e flags.
		var envSlice []string
		for k, v := range c.Env {
			envSlice = append(envSlice, k+"="+v)
		}
		// Pass the work directory as the in-container working dir.
		// When using a container the host WorkDir is mounted as /workspace;
		// we use the container's default /workspace (set at run time).
		containerWorkDir := ""
		if opts.WorkDir != "" {
			containerWorkDir = "/workspace"
		}
		cliCmd := append([]string{c.CLIPath}, args...)
		cmd = c.ContainerMgr.ExecInContainer(c.ContainerID, cliCmd, envSlice, containerWorkDir)
		// Use exec.Command (not CommandContext) so we can control process group killing ourselves
	} else {
		// Use exec.Command (not CommandContext) so we can control process group killing ourselves
		cmd = exec.Command(c.CLIPath, args...)
		cmd.Dir = opts.WorkDir
	}

	// Create a new process group so we can kill all child processes
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Set environment (host-only path; container env is passed via -e flags above)
	if c.ContainerMgr == nil || c.ContainerID == "" {
		cmd.Env = os.Environ()
		for k, v := range c.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude CLI: %w", err)
	}

	// Monitor context cancellation and kill the entire process group
	doneCh := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				pgid, err := syscall.Getpgid(cmd.Process.Pid)
				if err == nil {
					log.Printf("[claude] Context cancelled, killing process group %d", pgid)
					_ = syscall.Kill(-pgid, syscall.SIGKILL)
				} else {
					log.Printf("[claude] Context cancelled, killing process %d", cmd.Process.Pid)
					_ = cmd.Process.Kill()
				}
			}
		case <-doneCh:
			// Process finished normally
		}
	}()

	// Read stderr in background
	var stderrBuf []byte
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		stderrBuf, _ = io.ReadAll(stderr)
		if len(stderrBuf) > 0 {
			log.Printf("[claude] stderr: %s", string(stderrBuf))
		}
	}()

	// Send user message
	userMsg := UserInput{
		Type: "user",
		Message: UserInputMessage{
			Role:    "user",
			Content: prompt,
		},
		SessionID: opts.Resume,
	}
	if userMsg.SessionID == "" {
		userMsg.SessionID = opts.SessionID
	}
	if userMsg.SessionID == "" {
		userMsg.SessionID = "new-session"
	}

	msgBytes, err := json.Marshal(userMsg)
	if err != nil {
		return "", fmt.Errorf("marshal user message: %w", err)
	}

	if c.Verbose {
		log.Printf("[claude] Sending: %s", string(msgBytes))
	}

	if _, err := stdin.Write(append(msgBytes, '\n')); err != nil {
		return "", fmt.Errorf("write to stdin: %w", err)
	}

	// Close stdin to signal end of input
	stdin.Close()

	// Read and process output
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10MB max line size

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg StreamMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			if c.Verbose {
				log.Printf("[claude] Failed to parse line: %s, error: %v", string(line), err)
			}
			continue
		}

		if c.Verbose {
			log.Printf("[claude] Received: type=%s, subtype=%s", msg.Type, msg.Subtype)
		}

		// Log the full raw JSON for error results to aid debugging
		if msg.Type == "result" && (msg.IsError || msg.Subtype != "success") {
			log.Printf("[claude] Error result (raw): %s", string(line))
		}

		// Track session ID
		if msg.SessionID != "" {
			sessionID = msg.SessionID
		}

		// Parse assistant message content if present
		var assistantMsg *AssistantMessage
		if msg.Type == "assistant" && len(msg.Message) > 0 {
			var am AssistantMessage
			if err := json.Unmarshal(msg.Message, &am); err == nil {
				assistantMsg = &am
			}
		}

		callback(msg, assistantMsg)

		// Check for result/error
		if msg.Type == "result" {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return sessionID, fmt.Errorf("read stdout: %w", err)
	}

	// Wait for stderr reader
	wg.Wait()

	// Signal the context monitor goroutine that the process is done
	close(doneCh)

	// Wait for process to exit
	if err := cmd.Wait(); err != nil {
		// If the context was cancelled, return a clean cancellation error
		if ctx.Err() != nil {
			return sessionID, context.Canceled
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return sessionID, fmt.Errorf("claude CLI exited with code %d: %s", exitErr.ExitCode(), string(stderrBuf))
		}
		return sessionID, fmt.Errorf("wait for claude CLI: %w", err)
	}

	return sessionID, nil
}

// buildArgs constructs CLI arguments from options
func (c *ClaudeClient) buildArgs(opts QueryOptions) []string {
	args := []string{
		"-p", // CRITICAL: Print mode for non-interactive use
		"--input-format", "stream-json",
		"--output-format", "stream-json",
	}

	if c.Verbose {
		args = append(args, "--verbose")
	}

	if opts.Resume != "" {
		args = append(args, "--resume", opts.Resume)
	} else if opts.SessionID != "" {
		args = append(args, "--session-id", opts.SessionID)
	}

	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowed-tools")
		args = append(args, opts.AllowedTools...)
	}

	if opts.PermissionMode != "" {
		args = append(args, "--permission-mode", opts.PermissionMode)
	}

	// MCP configuration
	if opts.MCPConfigPath != "" {
		args = append(args, "--mcp-config", opts.MCPConfigPath)
		// args = append(args, "--strict-mcp-config") // Only use our MCP, ignore user's local config
	}

	return args
}
