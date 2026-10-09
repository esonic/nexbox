package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// taskNotificationIDRe extracts <task-id>…</task-id> from a re-invocation
// `<task-notification>` that the CLI injects as a user message when a background
// task (Workflow) completes. Used to clear the task from the running set even
// when the completion is delivered only via this user-message path.
var taskNotificationIDRe = regexp.MustCompile(`<task-id>([^<]+)</task-id>`)

// taskNotificationIDs returns the task ids referenced by a `<task-notification>`
// in a raw user-message payload, or nil if the payload carries none.
func taskNotificationIDs(raw []byte) []string {
	if !bytes.Contains(raw, []byte("task-notification")) {
		return nil
	}
	matches := taskNotificationIDRe.FindAllSubmatch(raw, -1)
	if len(matches) == 0 {
		return nil
	}
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, string(m[1]))
	}
	return ids
}

// postTurnExitGrace bounds how long we wait for the CLI process to exit AFTER
// the turn's terminal result was received and the read loop broke. Normally the
// process exits in well under a second; if a leftover background task (e.g. an
// orphaned Workflow from earlier in the session) keeps it alive, it can linger
// up to the CLI's own 600s background-wait ceiling, blocking wg.Wait()/cmd.Wait()
// and stranding the turn as "processing". The turn is already decided complete,
// so after this grace we force-kill the process group and return cleanly.
const postTurnExitGrace = 30 * time.Second

// outboundCtrlSeq is a monotonically increasing counter for generating unique
// request IDs in outbound control_request messages sent to the CLI.
var outboundCtrlSeq int64

// ClaudeClient wraps the Claude CLI for programmatic use
type ClaudeClient struct {
	CLIPath string            // Path to claude CLI (default: "claude")
	Env     map[string]string // Additional environment variables
	Verbose bool              // Enable verbose logging
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
	// send_to_session can block for the duration of another agent turn. Match
	// NexBox's per-turn ceiling unless the operator set an explicit timeout.
	if os.Getenv("MCP_TOOL_TIMEOUT") == "" {
		c.Env["MCP_TOOL_TIMEOUT"] = strconv.Itoa(int(mcpToolTimeout / time.Millisecond))
	}
	return c
}

// mcpToolTimeout matches the per-turn ceiling in api.dispatchUserMessage.
const mcpToolTimeout = 5 * time.Hour

// QueryOptions contains options for a Claude query
type QueryOptions struct {
	WorkDir        string   // Working directory
	Resume         string   // Session ID to resume
	SessionID      string   // Specific session ID to use
	Model          string   // Model to use
	Effort         string   // Reasoning effort level: low|medium|high|xhigh|max
	AllowedTools   []string // List of allowed tools
	PermissionMode string   // Permission mode: default, acceptEdits, plan, etc.
	MCPConfigPath  string   // Path to MCP config file (enables --mcp-config and --strict-mcp-config)
	PluginDirs     []string // Directories to pass via --plugin-dir

	// ForkSession, when set with Resume, instructs the CLI to materialize a
	// new session (new session id + new JSONL file) instead of appending to
	// the resumed one. Maps to the `--fork-session` CLI flag.
	ForkSession bool
	// ResumeAtMessageUUID, when set with Resume + ForkSession, asks the CLI
	// to truncate the parent's history at (and including) this assistant
	// message UUID before continuing the new session. Maps to the hidden
	// `--resume-session-at <uuid>` CLI flag (confirmed working on
	// claude-code 2.1.119; not listed in --help but accepted by the parser).
	ResumeAtMessageUUID string

	// IncludePartialMessages enables --include-partial-messages, causing the CLI
	// to emit stream_event messages with incremental text deltas.
	// When true, CLAUDE_CODE_ENABLE_FINE_GRAINED_TOOL_STREAMING=1 is also set.
	IncludePartialMessages bool

	// RegisterInterject, when set, is called once the CLI process is live and the
	// initial user message has been sent. It receives a function that writes an
	// additional user message to the still-open stdin — the CLI folds it into the
	// running turn (the model reads it at the next turn boundary). See Request.
	RegisterInterject func(inject func(content string) error)

	// UserInputHandler, when set, handles interactive tool calls (e.g. AskUserQuestion).
	// The handler is called with (requestID, toolName, inputJSON) and should block
	// until the user responds.
	UserInputHandler UserInputHandler

	// DisallowedTools lists tools that should be explicitly blocked.
	DisallowedTools []string
}

// StreamMessage represents a message from Claude CLI stream-json output.
// Covers: assistant, user, system, result, stream_event, control_request,
// control_response, rate_limit_event.
type StreamMessage struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	UUID      string          `json:"uuid,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`

	// ParentToolUseID is set on assistant/user JSONL lines emitted by a
	// sub-agent. It points at the tool_use_id of the Task tool that
	// launched this sub-agent. Empty/absent on main-agent messages and
	// (per Claude SDK convention) on system/result messages.
	ParentToolUseID string `json:"parent_tool_use_id,omitempty"`

	// ToolUseResult carries Claude Code's structured per-tool-call result data
	// on "user" (tool_result) JSONL lines, sitting alongside `message`. For the
	// Edit/Write/MultiEdit family it includes filePath/oldString/newString and
	// (for Edit) a `structuredPatch` array of ready-made diff hunks.
	ToolUseResult json.RawMessage `json:"toolUseResult,omitempty"`

	// For result messages
	Result        string                           `json:"result,omitempty"`
	IsError       bool                             `json:"is_error,omitempty"`
	DurationMs    int64                            `json:"duration_ms,omitempty"`
	DurationApiMs int64                            `json:"duration_api_ms,omitempty"`
	TotalCostUSD  float64                          `json:"total_cost_usd,omitempty"`
	Usage         map[string]interface{}           `json:"usage,omitempty"`
	ModelUsage    map[string]ClaudeModelUsageEntry `json:"modelUsage,omitempty"`
	NumTurns      int                              `json:"num_turns,omitempty"`
	StopReason    string                           `json:"stop_reason,omitempty"`
	Errors        []string                         `json:"errors,omitempty"`

	// For system messages (general data payload; legacy wrapper, rarely present)
	Data map[string]interface{} `json:"data,omitempty"`

	// InitModels carries the parsed `models[]` from the initialize
	// control_response. Populated by the CLI wrapper before the synthetic
	// `system.init_response` message is emitted so ClaudeAgent can refresh
	// its cached model catalog opportunistically.
	InitModels []ClaudeInitModel `json:"-"`

	// For system.init messages (all fields are at top level)
	Model             string                   `json:"model,omitempty"`
	Cwd               string                   `json:"cwd,omitempty"`
	PermissionMode    string                   `json:"permissionMode,omitempty"`
	APIKeySource      string                   `json:"apiKeySource,omitempty"`
	ClaudeCodeVersion string                   `json:"claude_code_version,omitempty"`
	OutputStyle       string                   `json:"output_style,omitempty"`
	FastModeState     string                   `json:"fast_mode_state,omitempty"`
	Tools             []string                 `json:"tools,omitempty"`
	MCPServers        []map[string]interface{} `json:"mcp_servers,omitempty"`
	SlashCommands     []string                 `json:"slash_commands,omitempty"`
	Agents            []string                 `json:"agents,omitempty"`
	Skills            []string                 `json:"skills,omitempty"`
	Plugins           []interface{}            `json:"plugins,omitempty"`

	// For task_* system subtypes (fields are at top level, not inside data)
	TaskID       string `json:"task_id,omitempty"`
	Description  string `json:"description,omitempty"`
	Status       string `json:"status,omitempty"`
	Summary      string `json:"summary,omitempty"`
	LastToolName string `json:"last_tool_name,omitempty"`

	// For the dynamic Workflow tool (task_type == "local_workflow"). ToolUseID
	// is the Workflow tool_use_id that launched the background run, so progress
	// can be attached to the right tool card. WorkflowProgress is the structured
	// phase/agent snapshot — incremental: the first task_progress is a full
	// snapshot, later ones carry only the changed phase/agent entries.
	ToolUseID        string          `json:"tool_use_id,omitempty"`
	TaskType         string          `json:"task_type,omitempty"`
	WorkflowName     string          `json:"workflow_name,omitempty"`
	WorkflowProgress json.RawMessage `json:"workflow_progress,omitempty"`

	// For system.task_updated: the changed fields (status, end_time, result…).
	// A background sub-agent's terminal completion can arrive ONLY here — as a
	// task_updated whose patch.status is terminal, with no task_notification.
	// Mirrors TaskUpdatedMessage in the Claude Agent SDK; status lives in the
	// patch, NOT at the top level.
	Patch map[string]interface{} `json:"patch,omitempty"`

	// For system.background_tasks_changed (CC 2.1.206+): the authoritative
	// snapshot of currently-running detached background tasks (Workflows / Agent
	// subagents). Emitted on every change; an empty slice means none are running.
	// More reliable than incrementally tracking task_started/task_notification,
	// which can desync on churning task sets.
	BackgroundTasks []ClaudeBackgroundTask `json:"tasks,omitempty"`

	// For stream_event messages (partial message streaming)
	Event json.RawMessage `json:"event,omitempty"`

	// For rate_limit_event messages.
	RateLimitInfo *ClaudeRateLimitInfo `json:"rate_limit_info,omitempty"`

	// For control_request messages
	RequestID string          `json:"request_id,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`

	// For control_response messages
	Response json.RawMessage `json:"response,omitempty"`
}

// ClaudeModelUsageEntry is the per-model usage block from result messages.
// The `contextWindow` and `maxOutputTokens` fields are the authoritative
// capacity values for the resolved model slug.
type ClaudeModelUsageEntry struct {
	InputTokens              int64   `json:"inputTokens"`
	OutputTokens             int64   `json:"outputTokens"`
	CacheReadInputTokens     int64   `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64   `json:"cacheCreationInputTokens"`
	WebSearchRequests        int64   `json:"webSearchRequests,omitempty"`
	CostUSD                  float64 `json:"costUSD,omitempty"`
	ContextWindow            int64   `json:"contextWindow,omitempty"`
	MaxOutputTokens          int64   `json:"maxOutputTokens,omitempty"`
}

// ClaudeRateLimitInfo mirrors the payload of the `rate_limit_event` message.
type ClaudeRateLimitInfo struct {
	Status          string `json:"status,omitempty"`
	ResetsAt        int64  `json:"resetsAt,omitempty"`
	RateLimitType   string `json:"rateLimitType,omitempty"`
	OverageStatus   string `json:"overageStatus,omitempty"`
	OverageResetsAt int64  `json:"overageResetsAt,omitempty"`
	IsUsingOverage  bool   `json:"isUsingOverage,omitempty"`
}

// ClaudeBackgroundTask is one entry in a `background_tasks_changed` snapshot
// (CC 2.1.206+): a detached background task (Workflow / Agent subagent) that is
// currently still running.
type ClaudeBackgroundTask struct {
	TaskID      string `json:"task_id"`
	TaskType    string `json:"task_type"`
	Description string `json:"description,omitempty"`
}

// AssistantContent represents content from an assistant message
type AssistantContent struct {
	Type      string      `json:"type"`
	Text      string      `json:"text,omitempty"`
	ID        string      `json:"id,omitempty"`
	Name      string      `json:"name,omitempty"`
	Input     interface{} `json:"input,omitempty"`
	Thinking  string      `json:"thinking,omitempty"`
	Signature string      `json:"signature,omitempty"` // Extended thinking verification signature
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

// interactiveToolNames lists tool names that should be routed to the
// UserInputHandler instead of being auto-allowed.
var interactiveToolNames = map[string]bool{
	"AskUserQuestion": true,
}

// ControlRequest is the request payload inside a control_request message.
type ControlRequest struct {
	Subtype               string                 `json:"subtype"`
	ToolName              string                 `json:"tool_name,omitempty"`
	Input                 map[string]interface{} `json:"input,omitempty"`
	PermissionSuggestions []interface{}          `json:"permission_suggestions,omitempty"`
	BlockedPath           string                 `json:"blocked_path,omitempty"`
	CallbackID            string                 `json:"callback_id,omitempty"`
	ToolUseID             string                 `json:"tool_use_id,omitempty"`
}

// StreamCallback is called for each streaming event
type StreamCallback func(msg StreamMessage, parsed *AssistantMessage)

// Query sends a message to Claude and streams the response.
// Uses the Agent SDK control protocol (no -p flag) for full bidirectional
// communication.
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

	// Use exec.Command (not CommandContext) so we can control process group killing ourselves.
	cmd := exec.Command(c.CLIPath, args...)
	cmd.Dir = opts.WorkDir

	// Create a new process group so we can kill all child processes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Set environment.
	cmd.Env = os.Environ()
	for k, v := range c.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if opts.IncludePartialMessages {
		cmd.Env = append(cmd.Env, "CLAUDE_CODE_ENABLE_FINE_GRAINED_TOOL_STREAMING=1")
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

	// Mutex for stdin writes — control_response writes, outbound control_requests,
	// and deferred stdin.Close() can race; all stdin operations go through this lock.
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

	// doneCh signals the context monitor that the main loop is done.
	doneCh := make(chan struct{})

	// Monitor context cancellation and kill the entire process group.
	// We intentionally use force-kill behavior here to stop immediately.
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				pgid, perr := syscall.Getpgid(cmd.Process.Pid)
				if perr == nil {
					log.Printf("[claude] Context cancelled, killing process group %d", pgid)
					_ = syscall.Kill(-pgid, syscall.SIGKILL)
				} else {
					log.Printf("[claude] Context cancelled, killing process %d", cmd.Process.Pid)
					_ = cmd.Process.Kill()
				}
			}
		case <-doneCh:
			// Process finished normally — nothing to do.
		}
	}()

	// Read stderr in background.
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

	// Send initialize control_request before the user message. The CLI processes
	// stdin in order, so the initialize response arrives before the assistant turn.
	// We track initReqID to route its control_response to the callback.
	var initReqID string
	{
		seq := atomic.AddInt64(&outboundCtrlSeq, 1)
		initReqID = fmt.Sprintf("sdk-initialize-%d", seq)
		initMsg := map[string]interface{}{
			"type":       "control_request",
			"request_id": initReqID,
			"request":    map[string]interface{}{"subtype": "initialize"},
		}
		if data, merr := json.Marshal(initMsg); merr == nil {
			if werr := writeStdin(append(data, '\n')); werr != nil {
				log.Printf("[claude] Failed to send initialize control_request: %v", werr)
				initReqID = ""
			}
		}
	}

	// Send user message.
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

	// Keep stdin open — needed for control_response messages.
	defer closeStdin()

	// Expose live message injection ("interjection"): writing another user
	// message to the still-open stdin makes the CLI fold it into the running
	// turn (the model reads it at the next turn boundary, without aborting).
	// writeStdin is mutex-guarded, so this is safe to call from other goroutines
	// (the API layer) concurrently with control_response writes. A write error
	// (e.g. the turn already ended and the pipe closed) propagates to the caller,
	// which falls back to ordinary queueing.
	if opts.RegisterInterject != nil {
		injectSessionID := userMsg.SessionID
		opts.RegisterInterject(func(content string) error {
			im := UserInput{
				Type:      "user",
				Message:   UserInputMessage{Role: "user", Content: content},
				SessionID: injectSessionID,
			}
			data, mErr := json.Marshal(im)
			if mErr != nil {
				return mErr
			}
			return writeStdin(append(data, '\n'))
		})
	}

	// Read and process output.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10 MB max line size

	// sawContent flips true once the turn produces real assistant output. It
	// gates the premature-result skip below: a no-op `result` is only treated
	// as a catch-up artifact when it arrives *before* any turn content.
	var sawContent bool
	// prematureResultSkips bounds how many no-op catch-up results we skip before
	// giving up and breaking — a safety valve so a pathological stream can never
	// wedge the read loop waiting for a real result that never comes.
	prematureResultSkips := 0

	// runningBgTasks tracks detached background tasks that outlive the foreground
	// turn: the dynamic Workflow tool (task_type "local_workflow") AND background
	// Agent subagents (task_type "local_agent"). Both return from their tool call
	// immediately and keep running in the background — the CLI can emit the
	// turn's `result` while they're still running (verified: local_agent detaches
	// at ~7s while the subagent runs ~30s). task_started adds; a
	// task_notification/task_updated (system) OR a <task-notification>
	// re-invocation (user message) removes. While the set is non-empty, a
	// `result` only means the foreground turn went idle — the turn is NOT over —
	// so we keep the stream open (see the result handling below).
	runningBgTasks := map[string]bool{}
	// bgResultSwallows caps how many results we swallow while waiting on a
	// background task, so a task that never signals completion (bug / unusual
	// delivery) can't wedge the read loop forever — after the cap we break and
	// the post-turn grace-kill reaps the process.
	bgResultSwallows := 0

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

		if c.Verbose && msg.Type != "stream_event" {
			log.Printf("[claude] Received: type=%s, subtype=%s", msg.Type, msg.Subtype)
		}

		// Handle control_request from CLI — bidirectional protocol.
		// Interactive tools (AskUserQuestion) block waiting for user input,
		// so handle them in a goroutine to avoid blocking the scanner loop.
		if msg.Type == "control_request" {
			go c.handleControlRequest(ctx, msg, writeStdin, opts)
			continue
		}

		// Handle control_response — route responses to our outbound requests.
		if msg.Type == "control_response" {
			if initReqID != "" && len(msg.Response) > 0 {
				var resp struct {
					RequestID string                 `json:"request_id"`
					Response  map[string]interface{} `json:"response"`
				}
				if json.Unmarshal(msg.Response, &resp) == nil && resp.RequestID == initReqID {
					log.Printf("[claude] Initialize handshake complete (keys: %v)", mapKeys(resp.Response))
					// Re-parse the payload with a typed struct to extract the
					// strongly-typed `models[]` slice without relying on the
					// generic map[string]interface{} walk.
					var typed struct {
						Response struct {
							Models []ClaudeInitModel `json:"models"`
						} `json:"response"`
					}
					_ = json.Unmarshal(msg.Response, &typed)
					callback(StreamMessage{
						Type:       "system",
						Subtype:    "init_response",
						Data:       resp.Response,
						InitModels: typed.Response.Models,
					}, nil)
					initReqID = ""
				}
			}
			continue
		}

		if msg.Type == "control_cancel_request" {
			continue
		}

		// Log the full raw JSON for error results to aid debugging.
		if msg.Type == "result" && (msg.IsError || msg.Subtype != "success") {
			log.Printf("[claude] Error result (raw): %s", string(line))
		}

		// Track session ID.
		if msg.SessionID != "" {
			sessionID = msg.SessionID
		}

		// Track detached background tasks so the result handling below knows
		// whether the exchange is really over. A Workflow / background Agent
		// subagent returns from its tool call immediately and keeps running in
		// the background: its task_started lands before the foreground turn's
		// result, and its terminal notification only later, when it completes.
		if msg.Type == "system" {
			switch msg.Subtype {
			case "background_tasks_changed":
				// CC 2.1.206+ authoritative snapshot: replace the tracked set with
				// exactly the tasks the CLI reports as still running. This is the
				// source of truth (fires on every change, incl. completion → empty
				// slice) and corrects any drift in the incremental tracking below,
				// which desyncs on churning task sets (e.g. a review's finder→
				// verifier pipeline) — the bug this handles. No task_type filter:
				// the snapshot lists only background tasks, so every entry counts
				// (also future-proof against new detached task_types).
				next := make(map[string]bool, len(msg.BackgroundTasks))
				for _, tk := range msg.BackgroundTasks {
					if tk.TaskID != "" {
						next[tk.TaskID] = true
					}
				}
				runningBgTasks = next
			case "task_started":
				// "local_workflow" = dynamic Workflow tool; "local_agent" =
				// background Agent subagent. Both outlive the foreground turn.
				// (Incremental fallback for CLIs without background_tasks_changed,
				// e.g. 2.1.199; when present, the snapshot above is authoritative.)
				if msg.TaskID != "" && (msg.TaskType == "local_workflow" || msg.TaskType == "local_agent") {
					runningBgTasks[msg.TaskID] = true
				}
			case "task_notification":
				// task_notification IS the terminal "done" signal for a task
				// (the SDK/agent layer treats it as Sub-agent done regardless of
				// the status vocabulary), so clear unconditionally by task_id.
				if msg.TaskID != "" {
					delete(runningBgTasks, msg.TaskID)
				}
			case "task_updated":
				if msg.TaskID != "" {
					if st, _ := msg.Patch["status"].(string); isTerminalTaskStatus(st) {
						delete(runningBgTasks, msg.TaskID)
					}
				}
			}
		}
		// A Workflow's completion is also delivered as a `<task-notification>`
		// re-invocation (a user message) — the path that outlives the foreground
		// turn. Clear the running set on it too, so the round can end even when
		// no system task_notification/task_updated is seen for that workflow.
		if msg.Type == "user" && len(runningBgTasks) > 0 && len(msg.Message) > 0 {
			for _, id := range taskNotificationIDs(msg.Message) {
				delete(runningBgTasks, id)
			}
		}

		// A `result` while a detached background task (Workflow or Agent subagent)
		// is still running does NOT end the exchange: the foreground turn merely
		// went idle. The CLI keeps the session open, and when the task completes
		// it injects a task-notification on this same stream that re-invokes the
		// agent (auto-continuation). Breaking here would tear the process down —
		// killing the still-running task and losing the follow-up (the "没执行完
		// 就结束了" symptom, for both Workflows and Agent subagents). So swallow
		// the interim result and keep reading; the turn ends only when a result
		// coincides with no outstanding background task. A wedged task is bounded
		// by the user's Stop button (ctx cancel → process-group kill) —
		// intentionally no internal timeout.
		if msg.Type == "result" && !msg.IsError && len(runningBgTasks) > 0 {
			bgResultSwallows++
			if bgResultSwallows > 64 {
				log.Printf("[claude] Swallowed %d results with %d background task(s) still marked running; giving up waiting and ending turn (session=%s)",
					bgResultSwallows, len(runningBgTasks), sessionID)
				// Fall through to break — the post-turn grace-kill reaps the CLI.
			} else {
				log.Printf("[claude] Foreground result received but %d background task(s) still running; keeping stream open for auto-continuation (session=%s)",
					len(runningBgTasks), sessionID)
				continue
			}
		}

		// Skip premature "catch-up" results. On --resume, when a background task
		// (e.g. a Workflow interrupted by a previous CLI process exiting) has a
		// pending notification, the CLI flushes it and emits a no-op `result`
		// (num_turns==0, cost==0) *before* the real turn runs. Breaking on it
		// ends the read loop before the actual assistant turn streams, so the
		// message is persisted empty even though the CLI goes on to answer — the
		// reply lands in the CLI transcript but never reaches BeeHub. The Agent
		// SDK keeps a single stdout open across turns and never stops reading on
		// a result (see claude-agent-sdk _read_messages), so the real turn
		// arrives on this same stream: skip the no-op result and keep reading.
		// Gated on !sawContent (only pre-turn) and a skip cap so the loop can
		// never wedge waiting for a real result.
		if msg.Type == "result" && !msg.IsError && msg.NumTurns == 0 && msg.TotalCostUSD == 0 &&
			!sawContent && prematureResultSkips < 8 {
			prematureResultSkips++
			log.Printf("[claude] Skipping premature catch-up result #%d (num_turns=0, cost=0, pre-turn); session=%s",
				prematureResultSkips, sessionID)
			continue
		}

		// Parse assistant message content if present.
		var assistantMsg *AssistantMessage
		if msg.Type == "assistant" && len(msg.Message) > 0 {
			sawContent = true
			var am AssistantMessage
			if err := json.Unmarshal(msg.Message, &am); err == nil {
				assistantMsg = &am
			}
		}

		callback(msg, assistantMsg)

		// Exit loop once the result message is received.
		if msg.Type == "result" {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return sessionID, fmt.Errorf("read stdout: %w", err)
	}

	// Close stdin to signal the CLI process to exit (flushing stderr).
	closeStdin()

	// The turn is complete (we broke on its terminal result). Give the CLI a
	// short grace to exit on its own; if a lingering background task keeps it
	// alive past the grace, force-kill the process group so wg.Wait()/cmd.Wait()
	// below don't block up to the CLI's 600s ceiling and strand the turn as
	// "processing". A force-kill here is a clean completion, not an error — the
	// result was already delivered to the callback.
	var graceKilled atomic.Bool
	procExited := make(chan struct{})
	go func() {
		select {
		case <-procExited:
		case <-time.After(postTurnExitGrace):
			if cmd.Process != nil {
				if pgid, perr := syscall.Getpgid(cmd.Process.Pid); perr == nil {
					log.Printf("[claude] CLI still alive %s after turn end (lingering background task); force-killing process group %d", postTurnExitGrace, pgid)
					graceKilled.Store(true)
					_ = syscall.Kill(-pgid, syscall.SIGKILL)
				} else {
					graceKilled.Store(true)
					_ = cmd.Process.Kill()
				}
			}
		}
	}()

	// Wait for stderr reader.
	wg.Wait()

	// Signal the context monitor goroutine.
	close(doneCh)

	// Wait for process to exit.
	werr := cmd.Wait()
	close(procExited)
	if werr != nil {
		// A force-kill we issued after the turn ended is a clean completion.
		if graceKilled.Load() {
			log.Printf("[claude] Turn complete; force-killed lingering CLI after %s", postTurnExitGrace)
			return sessionID, nil
		}
		if ctx.Err() != nil {
			return sessionID, context.Canceled
		}
		if exitErr, ok := werr.(*exec.ExitError); ok {
			return sessionID, fmt.Errorf("claude CLI exited with code %d: %s", exitErr.ExitCode(), string(stderrBuf))
		}
		return sessionID, fmt.Errorf("wait for claude CLI: %w", werr)
	}

	return sessionID, nil
}

// mapKeys returns the keys of a map[string]interface{} for logging.
func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// handleControlRequest processes a control_request from the CLI and writes
// back a control_response via stdin.
func (c *ClaudeClient) handleControlRequest(
	ctx context.Context,
	msg StreamMessage,
	writeStdin func([]byte) error,
	opts QueryOptions,
) {
	requestID := msg.RequestID
	if requestID == "" {
		log.Printf("[claude] control_request with empty request_id, skipping")
		return
	}

	// Parse the request payload.
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
		// Auto-continue for hooks — we don't register any hooks currently.
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

	// Non-interactive tools: auto-allow
	c.sendControlSuccess(writeStdin, requestID, map[string]interface{}{
		"behavior":     "allow",
		"updatedInput": req.Input,
	})
}

// buildArgs constructs CLI arguments from options.
// Uses Agent SDK mode (no -p flag) with stream-json for full bidirectional
// control protocol support.
func (c *ClaudeClient) buildArgs(opts QueryOptions) []string {
	args := []string{
		// No -p flag: use Agent SDK mode for full control protocol support.
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
	}

	if opts.Resume != "" {
		args = append(args, "--resume", opts.Resume)
		if opts.ForkSession {
			// `--fork-session` only makes sense in combination with --resume
			// (or --continue); the CLI rejects it otherwise.
			args = append(args, "--fork-session")
			if opts.ResumeAtMessageUUID != "" {
				args = append(args, "--resume-session-at", opts.ResumeAtMessageUUID)
			}
		}
	} else if opts.SessionID != "" {
		args = append(args, "--session-id", opts.SessionID)
	}

	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
	}

	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowed-tools")
		args = append(args, opts.AllowedTools...)
	}

	args = append(args, "--tools", "default")

	if len(opts.DisallowedTools) > 0 {
		args = append(args, "--disallowed-tools")
		args = append(args, opts.DisallowedTools...)
	}

	if opts.PermissionMode != "" {
		args = append(args, "--permission-mode", opts.PermissionMode)
	}

	// When a UserInputHandler is set, route tool permission prompts through
	// the stdio control protocol so AskUserQuestion (and similar interactive
	// tools) send can_use_tool control_requests instead of trying to prompt
	// on the terminal.
	if opts.UserInputHandler != nil {
		args = append(args, "--permission-prompt-tool", "stdio")
	}

	// MCP configuration
	if opts.MCPConfigPath != "" {
		args = append(args, "--mcp-config", opts.MCPConfigPath)
	}

	// Plugin directories.
	for _, dir := range opts.PluginDirs {
		args = append(args, "--plugin-dir", dir)
	}

	if opts.IncludePartialMessages {
		args = append(args, "--include-partial-messages")
	}

	return args
}
