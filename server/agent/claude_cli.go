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
	"time"
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
	PluginDirs     []string // Directories to pass via --plugin-dir

	// UserInputHandler is called when the CLI sends a can_use_tool control_request
	// for an interactive tool (e.g., AskUserQuestion). The handler shows the
	// question to the user and blocks until they respond. If nil, all tools are
	// auto-allowed (matching the old bypassPermissions behavior).
	UserInputHandler UserInputHandler
}

// ---------------------------------------------------------------------------
// Stream-JSON protocol types
// ---------------------------------------------------------------------------

// StreamMessage represents a message from Claude CLI stream-json output.
// Covers: assistant, user, system, result, control_request, control_response.
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
	Usage        map[string]interface{} `json:"usage,omitempty"`
	NumTurns     int                    `json:"num_turns,omitempty"`
	Errors       []string               `json:"errors,omitempty"`

	// For system messages
	Data map[string]interface{} `json:"data,omitempty"`

	// For control_request messages
	RequestID string          `json:"request_id,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`

	// For control_response messages
	Response json.RawMessage `json:"response,omitempty"`
}

// AssistantContent represents content from an assistant message
type AssistantContent struct {
	Type     string      `json:"type"`
	Text     string      `json:"text,omitempty"`
	ID       string      `json:"id,omitempty"`
	Name     string      `json:"name,omitempty"`
	Input    interface{} `json:"input,omitempty"`
	Thinking string      `json:"thinking,omitempty"`
}

// AssistantMessage represents a parsed assistant message
type AssistantMessage struct {
	Role    string                 `json:"role"`
	Content []AssistantContent     `json:"content"`
	ID      string                 `json:"id"`
	Usage   map[string]interface{} `json:"usage"`
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

// ControlRequest is the request payload inside a control_request message.
type ControlRequest struct {
	Subtype              string                 `json:"subtype"`
	ToolName             string                 `json:"tool_name,omitempty"`
	Input                map[string]interface{} `json:"input,omitempty"`
	PermissionSuggestions []interface{}          `json:"permission_suggestions,omitempty"`
	BlockedPath          string                 `json:"blocked_path,omitempty"`
	// Hook callback fields
	CallbackID string      `json:"callback_id,omitempty"`
	ToolUseID  string      `json:"tool_use_id,omitempty"`
}

// interactiveToolNames lists tool names that should be routed to the
// UserInputHandler instead of being auto-allowed.
var interactiveToolNames = map[string]bool{
	"AskUserQuestion": true,
}

// StreamCallback is called for each streaming event
type StreamCallback func(msg StreamMessage, parsed *AssistantMessage)

// Query sends a message to Claude and streams the response.
// Uses the Agent SDK control protocol (no -p flag) for full bidirectional
// communication, supporting interactive tools like AskUserQuestion.
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
		var envSlice []string
		for k, v := range c.Env {
			envSlice = append(envSlice, k+"="+v)
		}
		containerWorkDir := ""
		if opts.WorkDir != "" {
			containerWorkDir = "/workspace"
		}
		cliCmd := append([]string{c.CLIPath}, args...)
		cmd = c.ContainerMgr.ExecInContainer(c.ContainerID, cliCmd, envSlice, containerWorkDir)
	} else {
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
					// Send SIGTERM first to allow Claude CLI to finish the current
					// API call and emit the final result message (with token usage).
					log.Printf("[claude] Context cancelled, sending SIGTERM to process group %d", pgid)
					_ = syscall.Kill(-pgid, syscall.SIGTERM)
					// Give it up to 5 seconds to exit gracefully.
					select {
					case <-doneCh:
						log.Printf("[claude] Process group %d exited gracefully after SIGTERM", pgid)
					case <-time.After(5 * time.Second):
						log.Printf("[claude] Process group %d still running after 5s, sending SIGKILL", pgid)
						_ = syscall.Kill(-pgid, syscall.SIGKILL)
					}
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

	// Mutex for stdin writes — control_response writes and deferred stdin.Close()
	// can race with each other, so all stdin operations go through this lock.
	var stdinMu sync.Mutex
	writeStdin := func(data []byte) error {
		stdinMu.Lock()
		defer stdinMu.Unlock()
		_, wErr := stdin.Write(data)
		return wErr
	}
	var stdinOnce sync.Once
	closeStdin := func() {
		stdinOnce.Do(func() {
			stdinMu.Lock()
			defer stdinMu.Unlock()
			stdin.Close()
		})
	}

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

	if err := writeStdin(append(msgBytes, '\n')); err != nil {
		return "", fmt.Errorf("write to stdin: %w", err)
	}

	// Keep stdin open — we need it for control_response messages.
	// Close it when the result arrives or context is cancelled.
	defer closeStdin()

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

		// Handle control_request from CLI — this is the bidirectional protocol.
		if msg.Type == "control_request" {
			c.handleControlRequest(ctx, msg, opts, writeStdin)
			continue
		}

		// Ignore control_response (responses to our own requests, if any)
		if msg.Type == "control_response" || msg.Type == "control_cancel_request" {
			continue
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

	// Close stdin now that we've received the result. This signals the CLI
	// process to exit, which in turn closes stderr. Without this, the stderr
	// reader (io.ReadAll) would block forever waiting for EOF, deadlocking
	// against the deferred closeStdin.
	closeStdin()

	// Wait for stderr reader
	wg.Wait()

	// Signal the context monitor goroutine that the process is done
	close(doneCh)

	// Wait for process to exit
	if err := cmd.Wait(); err != nil {
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

// handleControlRequest processes a control_request from the CLI and writes
// back a control_response via stdin.
func (c *ClaudeClient) handleControlRequest(
	ctx context.Context,
	msg StreamMessage,
	opts QueryOptions,
	writeStdin func([]byte) error,
) {
	requestID := msg.RequestID
	if requestID == "" {
		log.Printf("[claude] control_request with empty request_id, skipping")
		return
	}

	// Parse the request payload
	var req ControlRequest
	if err := json.Unmarshal(msg.Request, &req); err != nil {
		log.Printf("[claude] Failed to parse control_request payload: %v", err)
		c.sendControlError(writeStdin, requestID, "failed to parse request")
		return
	}

	log.Printf("[claude] control_request: subtype=%s, tool=%s, id=%s", req.Subtype, req.ToolName, requestID)

	switch req.Subtype {
	case "can_use_tool":
		c.handleCanUseTool(ctx, writeStdin, requestID, req, opts)

	case "hook_callback":
		// Auto-continue for hooks — we don't register any hooks currently
		c.sendControlSuccess(writeStdin, requestID, map[string]interface{}{
			"continue": true,
		})

	default:
		// For unrecognized subtypes (initialize, mcp_message, etc.),
		// send a success response with empty payload to avoid blocking the CLI.
		log.Printf("[claude] Unhandled control_request subtype=%q, auto-acknowledging", req.Subtype)
		c.sendControlSuccess(writeStdin, requestID, map[string]interface{}{})
	}
}

// handleCanUseTool processes a can_use_tool control_request.
// Interactive tools (AskUserQuestion) are routed to the UserInputHandler;
// all other tools are auto-allowed.
func (c *ClaudeClient) handleCanUseTool(
	ctx context.Context,
	writeStdin func([]byte) error,
	requestID string,
	req ControlRequest,
	opts QueryOptions,
) {
	// For interactive tools, route to the user input handler
	if interactiveToolNames[req.ToolName] && opts.UserInputHandler != nil {
		inputJSON, _ := json.Marshal(req.Input)
		log.Printf("[claude] Interactive tool %s — requesting user input", req.ToolName)

		response, err := opts.UserInputHandler(requestID, req.ToolName, string(inputJSON))
		if err != nil {
			log.Printf("[claude] UserInputHandler error: %v — denying tool", err)
			c.sendControlSuccess(writeStdin, requestID, map[string]interface{}{
				"behavior": "deny",
				"message":  "User did not respond: " + err.Error(),
			})
			return
		}

		// Build updatedInput with original questions + structured answers map.
		// The Claude CLI AskUserQuestion tool expects:
		//   { "questions": [...original...], "answers": { "question text": "selected label" } }
		updatedInput := make(map[string]interface{})
		for k, v := range req.Input {
			updatedInput[k] = v
		}

		// Try to parse structured answers from frontend (JSON with "answers" map)
		var structured struct {
			Answers map[string]string `json:"answers"`
		}
		if err := json.Unmarshal([]byte(response), &structured); err == nil && structured.Answers != nil {
			updatedInput["answers"] = structured.Answers
		} else {
			// Fallback: use raw text response as answer for first question
			if questions, ok := req.Input["questions"].([]interface{}); ok && len(questions) > 0 {
				if q, ok := questions[0].(map[string]interface{}); ok {
					if qText, ok := q["question"].(string); ok {
						updatedInput["answers"] = map[string]string{qText: response}
					}
				}
			}
		}

		c.sendControlSuccess(writeStdin, requestID, map[string]interface{}{
			"behavior":     "allow",
			"updatedInput": updatedInput,
		})
		return
	}

	// Non-interactive tools: auto-allow (same as bypassPermissions)
	c.sendControlSuccess(writeStdin, requestID, map[string]interface{}{
		"behavior":     "allow",
		"updatedInput": req.Input,
	})
}

// sendControlSuccess writes a successful control_response to stdin.
func (c *ClaudeClient) sendControlSuccess(writeStdin func([]byte) error, requestID string, response map[string]interface{}) {
	msg := map[string]interface{}{
		"type": "control_response",
		"response": map[string]interface{}{
			"subtype":    "success",
			"request_id": requestID,
			"response":   response,
		},
	}
	data, _ := json.Marshal(msg)
	if err := writeStdin(append(data, '\n')); err != nil {
		log.Printf("[claude] Failed to write control_response: %v", err)
	} else if c.Verbose {
		log.Printf("[claude] Sent control_response: %s", string(data))
	}
}

// sendControlError writes an error control_response to stdin.
func (c *ClaudeClient) sendControlError(writeStdin func([]byte) error, requestID string, errMsg string) {
	msg := map[string]interface{}{
		"type": "control_response",
		"response": map[string]interface{}{
			"subtype":    "error",
			"request_id": requestID,
			"error":      errMsg,
		},
	}
	data, _ := json.Marshal(msg)
	if err := writeStdin(append(data, '\n')); err != nil {
		log.Printf("[claude] Failed to write error control_response: %v", err)
	}
}

// buildArgs constructs CLI arguments from options.
// Uses Agent SDK mode (no -p flag) with stream-json for full bidirectional
// control protocol support.
func (c *ClaudeClient) buildArgs(opts QueryOptions) []string {
	args := []string{
		// No -p flag: use Agent SDK mode for full control protocol support.
		// This enables bidirectional communication via control_request/control_response,
		// which is required for interactive tools like AskUserQuestion.
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
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

	args = append(args, "--tools", "default")

	if opts.PermissionMode != "" {
		args = append(args, "--permission-mode", opts.PermissionMode)
	}

	// When a UserInputHandler is set, route tool permission prompts through
	// the stdio control protocol so AskUserQuestion (and similar interactive
	// tools) send can_use_tool control_requests instead of trying to prompt
	// on the terminal. This mirrors the Python SDK's behavior of automatically
	// setting --permission-prompt-tool stdio when can_use_tool is provided.
	if opts.UserInputHandler != nil {
		args = append(args, "--permission-prompt-tool", "stdio")
	}

	// MCP configuration
	if opts.MCPConfigPath != "" {
		args = append(args, "--mcp-config", opts.MCPConfigPath)
	}

	// Plugin directories
	for _, dir := range opts.PluginDirs {
		args = append(args, "--plugin-dir", dir)
	}

	return args
}
