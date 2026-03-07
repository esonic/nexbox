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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
)

// CodexAgent implements the Agent interface using the OpenAI Codex CLI.
// It spawns `codex exec --json` as a subprocess and maps its JSONL events
// to the unified StreamEvent protocol.
type CodexAgent struct {
	CLIPath      string // Path to codex CLI binary (default: "codex")
	MCPServerURL string // Optional: URL of the NexBox MCP server
	Verbose      bool   // Enable verbose logging

	urlMCPChecked bool // whether we've probed URL-based MCP support
	urlMCPSupport bool // true if codex version supports mcp_servers.*.url
}

// NewCodexAgent creates a new Codex agent with default settings.
func NewCodexAgent() *CodexAgent {
	return &CodexAgent{
		CLIPath: "codex",
		Verbose: true,
	}
}

// Ensure CodexAgent implements Agent at compile time.
var _ Agent = (*CodexAgent)(nil)

// ---------------------------------------------------------------------------
// Codex JSONL event types (from `codex exec --json`)
//
// {"type":"thread.started","thread_id":"..."}
// {"type":"turn.started"}
// {"type":"item.started","item":{"id":"...","type":"command_execution","command":"...","status":"in_progress"}}
// {"type":"item.completed","item":{"id":"...","type":"command_execution","command":"...","aggregated_output":"...","exit_code":0,"status":"completed"}}
// {"type":"item.completed","item":{"id":"...","type":"agent_message","text":"..."}}
// {"type":"turn.completed","usage":{"input_tokens":...,"cached_input_tokens":...,"output_tokens":...}}
// ---------------------------------------------------------------------------

// codexEvent is the top-level JSONL envelope from codex exec --json.
type codexEvent struct {
	Type     string                 `json:"type"`
	ThreadID string                 `json:"thread_id,omitempty"`
	Item     json.RawMessage        `json:"item,omitempty"`
	Usage    *codexUsage            `json:"usage,omitempty"`
	Message  string                 `json:"message,omitempty"` // for "error" events
	Error    map[string]interface{} `json:"error,omitempty"`   // for "turn.failed" events
}

// codexItem is a parsed item payload (used in item.started / item.completed).
type codexItem struct {
	ID               string `json:"id"`
	Type             string `json:"type"` // "command_execution" | "agent_message" | "reasoning" | "function_call" | "error" | ...
	Command          string `json:"command,omitempty"`
	AggregatedOutput string `json:"aggregated_output,omitempty"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	Status           string `json:"status,omitempty"` // "in_progress" | "completed"
	Text             string `json:"text,omitempty"`
	Message          string `json:"message,omitempty"` // for error items
	// Function call fields
	Name      string          `json:"name,omitempty"`      // function name
	Arguments json.RawMessage `json:"arguments,omitempty"` // arguments (can be object or string)
	Output    json.RawMessage `json:"output,omitempty"`    // function output (can be object or string)
	// MCP tool call fields
	Server string          `json:"server,omitempty"` // MCP server label (e.g. "nexbox")
	Tool   string          `json:"tool,omitempty"`   // MCP tool name (e.g. "ask_question")
	Result json.RawMessage `json:"result,omitempty"` // MCP tool result
	Error  json.RawMessage `json:"error,omitempty"`  // MCP tool error
	// File change fields
	Changes []codexFileChange `json:"changes,omitempty"`
}

// codexFileChange represents a single file change in a file_change item.
type codexFileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // "create" | "update" | "delete"
}

// codexUsage contains token usage from turn.completed events.
type codexUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
}

// toolDisplayName returns a human-readable name for a tool call item.
func (item *codexItem) toolDisplayName() string {
	// MCP tool call: server/tool format
	if item.Tool != "" {
		if item.Server != "" {
			return item.Server + "/" + item.Tool
		}
		return item.Tool
	}
	// Function call: use name
	if item.Name != "" {
		return item.Name
	}
	return "Tool"
}

// resultText extracts text from an MCP tool result JSON.
// Format: {"content":[{"type":"text","text":"..."}],...}
func (item *codexItem) resultText() string {
	if len(item.Result) == 0 || string(item.Result) == "null" {
		return ""
	}
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(item.Result, &r); err != nil {
		return string(item.Result)
	}
	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return string(item.Result)
}

// SendMessage implements Agent.SendMessage using the Codex CLI.
func (a *CodexAgent) SendMessage(
	ctx context.Context,
	req Request,
	cb EventCallback,
) (Result, error) {
	// Pre-check work directory
	if req.WorkDir != "" {
		info, err := os.Stat(req.WorkDir)
		if err != nil {
			if os.IsNotExist(err) {
				return Result{}, fmt.Errorf("work directory does not exist: %s", req.WorkDir)
			}
			return Result{}, fmt.Errorf("cannot access work directory: %s (%w)", req.WorkDir, err)
		}
		if !info.IsDir() {
			return Result{}, fmt.Errorf("work directory path is not a directory: %s", req.WorkDir)
		}
	}

	// Build the combined prompt (system prompt + user message)
	prompt := a.buildPrompt(req)

	// Build command arguments (prompt is passed via stdin, not as a CLI arg)
	args := a.buildArgs(req)

	if a.Verbose {
		log.Printf("[codex-agent] CLI command: %s %v", a.CLIPath, args)
	}

	cmd := exec.Command(a.CLIPath, args...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Sanitize prompt to valid UTF-8 (database content may contain non-UTF-8 bytes)
	if !utf8.ValidString(prompt) {
		log.Printf("[codex-agent] Warning: prompt contains invalid UTF-8 at offset ~%d, sanitizing",
			findInvalidUTF8Offset(prompt))
		prompt = strings.ToValidUTF8(prompt, "\uFFFD")
	}
	// Pass prompt via stdin to avoid encoding issues with CLI arguments
	cmd.Stdin = strings.NewReader(prompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, fmt.Errorf("create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start codex CLI: %w", err)
	}

	// Monitor context cancellation and kill the entire process group
	doneCh := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				pgid, err := syscall.Getpgid(cmd.Process.Pid)
				if err == nil {
					log.Printf("[codex-agent] Context cancelled, killing process group %d", pgid)
					_ = syscall.Kill(-pgid, syscall.SIGKILL)
				} else {
					_ = cmd.Process.Kill()
				}
			}
		case <-doneCh:
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
			log.Printf("[codex-agent] stderr: %s", string(stderrBuf))
		}
	}()

	// Log request details
	log.Printf("[codex-agent] ========== Request ==========")
	log.Printf("[codex-agent] conversationID: %s", req.ConversationID)
	log.Printf("[codex-agent] sessionID: %s (new: %v)", req.SessionID, req.SessionID == "")
	log.Printf("[codex-agent] workDir: %s", req.WorkDir)
	log.Printf("[codex-agent] message: %s", req.Message)
	if a.MCPServerURL != "" {
		log.Printf("[codex-agent] MCP: %s", a.MCPServerURL)
	}
	log.Printf("[codex-agent] ===============================")

	// Parse JSONL output and map to unified events
	var result Result
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10MB max line

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var evt codexEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			if a.Verbose {
				log.Printf("[codex-agent] Failed to parse: %s, error: %v", string(line), err)
			}
			continue
		}

		if a.Verbose {
			log.Printf("[codex-agent] Received: type=%s", evt.Type)
		}

		switch evt.Type {
		case "thread.started":
			// Extract thread ID as session ID
			result.SessionID = evt.ThreadID
			log.Printf("[codex-agent] Thread started: %s", evt.ThreadID)

		case "turn.started":
			// Informational, no action needed

		case "item.started":
			var item codexItem
			if err := json.Unmarshal(evt.Item, &item); err != nil {
				log.Printf("[codex-agent] Failed to parse item.started: %v", err)
				continue
			}
			switch item.Type {
			case "command_execution":
				cmdJSON, _ := json.Marshal(map[string]string{"command": item.Command})
				cb(StreamEvent{
					Type:     EventToolStart,
					ToolID:   item.ID,
					ToolName: "Bash",
					Input:    string(cmdJSON),
				})
			case "function_call", "mcp_tool_call":
				// Function or MCP tool call
				cb(StreamEvent{
					Type:     EventToolStart,
					ToolID:   item.ID,
					ToolName: item.toolDisplayName(),
					Input:    string(item.Arguments),
				})
			default:
				// Log unknown item types for debugging
				if a.Verbose {
					log.Printf("[codex-agent] Unhandled item.started type=%q id=%s raw=%s", item.Type, item.ID, string(evt.Item))
				}
			}

		case "item.completed":
			var item codexItem
			if err := json.Unmarshal(evt.Item, &item); err != nil {
				log.Printf("[codex-agent] Failed to parse item.completed: %v", err)
				continue
			}

			switch item.Type {
			case "command_execution":
				// Shell command result
				isError := item.ExitCode != nil && *item.ExitCode != 0
				cb(StreamEvent{
					Type:    EventToolResult,
					ToolID:  item.ID,
					Output:  item.AggregatedOutput,
					IsError: isError,
				})

			case "agent_message":
				// Text response from the agent
				cb(StreamEvent{
					Type:    EventTextDelta,
					Content: item.Text,
				})

			case "reasoning":
				// Thinking/reasoning output
				if item.Text != "" {
					cb(StreamEvent{
						Type:    EventThinking,
						Content: item.Text,
					})
				}

			case "function_call":
				// Function call result
				cb(StreamEvent{
					Type:   EventToolResult,
					ToolID: item.ID,
					Output: string(item.Output),
				})

			case "mcp_tool_call":
				// MCP tool call result - extract text from result content
				output := item.resultText()
				isError := len(item.Error) > 0 && string(item.Error) != "null"
				cb(StreamEvent{
					Type:    EventToolResult,
					ToolID:  item.ID,
					Output:  output,
					IsError: isError,
				})

			case "file_change":
				// File modifications made by Codex — emit as a tool call pair
				changesJSON, _ := json.Marshal(map[string]interface{}{"changes": item.Changes})
				cb(StreamEvent{
					Type:     EventToolStart,
					ToolID:   item.ID,
					ToolName: "FileChange",
					Input:    string(changesJSON),
				})
				cb(StreamEvent{
					Type:    EventToolResult,
					ToolID:  item.ID,
					Output:  "",
					IsError: false,
				})

			case "error":
				// Error item (e.g., model mismatch warning) - show to user
				log.Printf("[codex-agent] Item error: %s", item.Message)
				cb(StreamEvent{
					Type:    EventSystem,
					Content: item.Message,
				})

			default:
				if a.Verbose {
					log.Printf("[codex-agent] Unhandled item.completed type=%q id=%s raw=%s", item.Type, item.ID, string(evt.Item))
				}
			}

		case "error":
			// Error event (e.g., reconnection attempts, network errors) - show to user
			log.Printf("[codex-agent] Error: %s", evt.Message)
			cb(StreamEvent{
				Type:    EventSystem,
				Content: evt.Message,
			})

		case "turn.failed":
			// Turn failed - send error to client
			errMsg := "Agent turn failed"
			if evt.Error != nil {
				if msg, ok := evt.Error["message"].(string); ok {
					errMsg = msg
				}
			}
			log.Printf("[codex-agent] Turn failed: %s", errMsg)
			cb(StreamEvent{
				Type:    EventError,
				Content: errMsg,
			})

		case "turn.completed":
			// Build metadata from usage
			metadata := make(map[string]interface{})
			if evt.Usage != nil {
				metadata["inputTokens"] = evt.Usage.InputTokens
				metadata["outputTokens"] = evt.Usage.OutputTokens
				metadata["cachedInputTokens"] = evt.Usage.CachedInputTokens
			}
			result.Metadata = metadata

			cb(StreamEvent{
				Type:      EventDone,
				SessionID: result.SessionID,
				Metadata:  metadata,
			})

		case "item.updated":
			// Codex updates items in-place (e.g., todo_list progress).
			// We don't surface these to the UI; the final state arrives in item.completed.
			if a.Verbose {
				log.Printf("[codex-agent] item.updated: %s", string(line))
			}

		default:
			if a.Verbose {
				log.Printf("[codex-agent] Unhandled event type=%q raw=%s", evt.Type, string(line))
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("read codex stdout: %w", err)
	}

	// Wait for stderr reader
	wg.Wait()

	// Signal the context monitor goroutine
	close(doneCh)

	// Wait for process to exit
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return result, context.Canceled
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return result, fmt.Errorf("codex CLI exited with code %d: %s", exitErr.ExitCode(), string(stderrBuf))
		}
		return result, fmt.Errorf("wait for codex CLI: %w", err)
	}

	log.Printf("[codex-agent] Query completed, session ID: %s", result.SessionID)
	return result, nil
}

// buildPrompt constructs the combined prompt.
// For new sessions: staticContext + dynamicContext + message
// For resumed sessions: dynamicContext + message
func (a *CodexAgent) buildPrompt(req Request) string {
	isNewSession := req.SessionID == ""
	return BuildPrompt(req.StaticContext, req.DynamicContext, req.Message, isNewSession, req.Images)
}

// supportsURLMCP checks (once) whether the installed codex CLI supports
// url-based MCP servers (available since codex-cli 0.99.0).
// Older versions only support stdio transport and require a `command` field.
func (a *CodexAgent) supportsURLMCP() bool {
	if a.urlMCPChecked {
		return a.urlMCPSupport
	}
	a.urlMCPChecked = true

	out, err := exec.Command(a.CLIPath, "--version").Output()
	if err != nil {
		log.Printf("[codex-agent] Failed to detect version, assuming no URL MCP support: %v", err)
		return false
	}
	versionStr := strings.TrimSpace(string(out))
	// Expected format: "codex-cli X.Y.Z"
	parts := strings.Fields(versionStr)
	if len(parts) < 2 {
		log.Printf("[codex-agent] Unexpected version output: %q", versionStr)
		return false
	}
	version := parts[len(parts)-1]
	a.urlMCPSupport = compareVersionGE(version, "0.99.0")
	if a.urlMCPSupport {
		log.Printf("[codex-agent] Codex %s supports URL-based MCP", version)
	} else {
		log.Printf("[codex-agent] Codex %s does not support URL-based MCP, will use stdio bridge", version)
	}
	return a.urlMCPSupport
}

// compareVersionGE returns true if version a >= version b (semver-style).
func compareVersionGE(a, b string) bool {
	parse := func(v string) [3]int {
		var parts [3]int
		segs := strings.SplitN(v, ".", 3)
		for i, s := range segs {
			if i >= 3 {
				break
			}
			n, _ := strconv.Atoi(s)
			parts[i] = n
		}
		return parts
	}
	av, bv := parse(a), parse(b)
	for i := 0; i < 3; i++ {
		if av[i] > bv[i] {
			return true
		}
		if av[i] < bv[i] {
			return false
		}
	}
	return true
}

// appendMCPArgs appends the MCP server configuration flags to args.
// Requires codex >= 0.99.0 (URL-based MCP support); older versions are skipped.
func (a *CodexAgent) appendMCPArgs(args []string, conversationID string) []string {
	if a.MCPServerURL == "" || !a.supportsURLMCP() {
		return args
	}
	url := a.MCPServerURL
	if conversationID != "" {
		url += "?session=" + conversationID
	}
	return append(args, "-c", fmt.Sprintf("mcp_servers.nexbox.url=%q", url))
}

// appendChromeMCPArgs appends Chrome DevTools MCP server configuration flags to args.
// Requires codex >= 0.99.0 (URL-based MCP support); older versions are skipped.
func (a *CodexAgent) appendChromeMCPArgs(args []string, req Request) []string {
	if !hasFeature(req.Features, "chrome_devtools") || req.ChromeMCPURL == "" {
		return args
	}
	if !a.supportsURLMCP() {
		log.Printf("[codex-agent] Chrome DevTools MCP requires codex >= 0.99.0, skipping")
		return args
	}
	args = append(args, "-c", fmt.Sprintf("mcp_servers.chrome.url=%q", req.ChromeMCPURL))
	log.Printf("[codex-agent] Chrome DevTools MCP enabled: %s", req.ChromeMCPURL)
	return args
}

// appendExtraMCPArgs appends extra MCP server flags from installed marketplace packages.
// Codex config keys reference: https://openai.github.io/codex/configuration/
//   stdio: mcp_servers.<id>.command, .args.N, .env.KEY
//   http:  mcp_servers.<id>.url, .http_headers.KEY  (requires codex >= 0.99.0)
func (a *CodexAgent) appendExtraMCPArgs(args []string, extras []MCPServerEntry) []string {
	supportsURL := a.supportsURLMCP()
	for _, mcp := range extras {
		switch {
		case mcp.URL != "": // HTTP / SSE transport
			if !supportsURL {
				log.Printf("[codex-agent] skip HTTP MCP %s: codex version does not support URL MCPs", mcp.Key)
				continue
			}
			args = append(args, "-c", fmt.Sprintf("mcp_servers.%s.url=%q", mcp.Key, mcp.URL))
			for hk, hv := range mcp.Headers {
				// Static HTTP headers: mcp_servers.<id>.http_headers.<name>
				args = append(args, "-c", fmt.Sprintf("mcp_servers.%s.http_headers.%s=%q", mcp.Key, hk, hv))
			}
		case mcp.Cmd != "": // stdio transport
			args = append(args, "-c", fmt.Sprintf("mcp_servers.%s.command=%q", mcp.Key, mcp.Cmd))
			for i, arg := range mcp.Args {
				args = append(args, "-c", fmt.Sprintf("mcp_servers.%s.args.%d=%q", mcp.Key, i, arg))
			}
			for ek, ev := range mcp.Env {
				args = append(args, "-c", fmt.Sprintf("mcp_servers.%s.env.%s=%q", mcp.Key, ek, ev))
			}
		}
	}
	return args
}

// buildArgs constructs CLI arguments for codex exec.
// The prompt is NOT included here — it is passed via stdin to avoid
// encoding issues with non-ASCII characters in CLI arguments.
func (a *CodexAgent) buildArgs(req Request) []string {
	// Check if this is a session resume
	// Note: `exec resume` does NOT support -C; working directory is set via cmd.Dir
	if req.SessionID != "" {
		args := []string{
			"exec", "resume",
			req.SessionID,
			"-", // read prompt from stdin
			"--json",
			"--full-auto",
			"--skip-git-repo-check",
		}
		args = a.appendMCPArgs(args, req.ConversationID)
		args = a.appendChromeMCPArgs(args, req)
		return a.appendExtraMCPArgs(args, req.ExtraMCPServers)
	}

	// New session — omit PROMPT so codex reads from stdin
	args := []string{
		"exec",
		"--json",
		"--full-auto",
		"--skip-git-repo-check",
	}

	if req.WorkDir != "" {
		args = append(args, "-C", req.WorkDir)
	}

	args = a.appendMCPArgs(args, req.ConversationID)
	args = a.appendChromeMCPArgs(args, req)
	return a.appendExtraMCPArgs(args, req.ExtraMCPServers)
}

// findInvalidUTF8Offset returns the byte offset of the first invalid UTF-8 byte in s.
func findInvalidUTF8Offset(s string) int {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			return i
		}
		i += size
	}
	return -1
}
