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
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// CursorAgent implements the Agent interface using the Cursor CLI (agent command).
// It spawns `agent --print --output-format stream-json` as a subprocess and maps
// its JSONL events to the unified StreamEvent protocol.
type CursorAgent struct {
	CLIPath      string // Path to cursor agent CLI binary (default: "agent")
	MCPServerURL string // Optional: URL of the NexBox MCP server
	Model        string // Optional model override (e.g., "sonnet-4", "gpt-5")
	Verbose      bool   // Enable verbose logging
}

// NewCursorAgent creates a new Cursor agent with default settings.
func NewCursorAgent() *CursorAgent {
	return &CursorAgent{
		CLIPath: "agent",
		Verbose: true,
	}
}

// Ensure CursorAgent implements Agent at compile time.
var _ Agent = (*CursorAgent)(nil)

// ---------------------------------------------------------------------------
// Cursor CLI JSONL event types (from `agent --print --output-format stream-json --stream-partial-output`)
//
// {"type":"system","subtype":"init","session_id":"...","model":"...","permissionMode":"..."}
// {"type":"user","message":{"role":"user","content":[...]},"session_id":"..."}
// {"type":"thinking","subtype":"delta","text":"...","session_id":"...","timestamp_ms":...}
// {"type":"thinking","subtype":"completed","session_id":"...","timestamp_ms":...}
// {"type":"tool_call","subtype":"started","call_id":"...","tool_call":{...},"session_id":"..."}
// {"type":"tool_call","subtype":"completed","call_id":"...","tool_call":{...},"session_id":"..."}
// {"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"..."}]},"session_id":"...","timestamp_ms":...}
// {"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"..."}]},"session_id":"..."}   (final, no timestamp_ms)
// {"type":"result","subtype":"success|error","duration_ms":...,"is_error":false,"result":"...","session_id":"..."}
// ---------------------------------------------------------------------------

// cursorEvent is the top-level JSONL envelope from cursor agent stream-json.
type cursorEvent struct {
	Type        string          `json:"type"`
	Subtype     string          `json:"subtype,omitempty"`
	SessionID   string          `json:"session_id,omitempty"`
	Text        string          `json:"text,omitempty"` // for thinking deltas
	Message     json.RawMessage `json:"message,omitempty"`
	CallID      string          `json:"call_id,omitempty"`
	ToolCall    json.RawMessage `json:"tool_call,omitempty"`
	TimestampMs *int64          `json:"timestamp_ms,omitempty"` // present on streaming deltas

	// Result fields
	DurationMs    int64  `json:"duration_ms,omitempty"`
	DurationAPIMs int64  `json:"duration_api_ms,omitempty"`
	IsError       bool   `json:"is_error,omitempty"`
	ResultText    string `json:"result,omitempty"`
	RequestID     string `json:"request_id,omitempty"`

	// System init fields
	Model          string `json:"model,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
}

// cursorMessage is the message envelope inside assistant/user events.
type cursorMessage struct {
	Role    string               `json:"role"`
	Content []cursorContentBlock `json:"content"`
}

// cursorContentBlock is a content block inside a message.
type cursorContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// SendMessage implements Agent.SendMessage using the Cursor CLI.
func (a *CursorAgent) SendMessage(
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

	// Write .cursor/mcp.json into the workspace directory.
	// Cursor only reads MCP config from <workspace>/.cursor/ or ~/.cursor/,
	// so we must write to the workspace despite it not being ideal.
	if a.MCPServerURL != "" && req.WorkDir != "" {
		if err := a.ensureMCPConfig(req.WorkDir); err != nil {
			log.Printf("[cursor-agent] Warning: failed to write MCP config: %v", err)
		}
	}

	// Build the combined prompt (system prompt + user message)
	prompt := a.buildPrompt(req)

	// Build command arguments
	args := a.buildArgs(req, prompt)

	if a.Verbose {
		log.Printf("[cursor-agent] CLI command: %s %v", a.CLIPath, args)
	}

	cmd := exec.Command(a.CLIPath, args...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, fmt.Errorf("create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start cursor agent CLI: %w", err)
	}

	// Monitor context cancellation and kill the entire process group
	doneCh := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				pgid, err := syscall.Getpgid(cmd.Process.Pid)
				if err == nil {
					log.Printf("[cursor-agent] Context cancelled, killing process group %d", pgid)
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
			log.Printf("[cursor-agent] stderr: %s", string(stderrBuf))
		}
	}()

	// Log request details
	log.Printf("[cursor-agent] ========== Request ==========")
	log.Printf("[cursor-agent] conversationID: %s", req.ConversationID)
	log.Printf("[cursor-agent] sessionID: %s (new: %v)", req.SessionID, req.SessionID == "")
	log.Printf("[cursor-agent] workDir: %s", req.WorkDir)
	if a.MCPServerURL != "" {
		log.Printf("[cursor-agent] MCP: %s", a.MCPServerURL)
	}
	log.Printf("[cursor-agent] ===============================")

	// Parse JSONL output and map to unified events
	var result Result
	var thinkingBuf string      // accumulate thinking deltas
	var assistantTextBuf string // track per-snapshot cumulative text to compute deltas
	var totalEmittedText string // track ALL text emitted across the entire response
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10MB max line

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var evt cursorEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			if a.Verbose {
				log.Printf("[cursor-agent] Failed to parse: %s, error: %v", string(line), err)
			}
			continue
		}

		// Log non-streaming events (skip high-frequency deltas to avoid log spam)
		if a.Verbose && !(evt.Type == "thinking" && evt.Subtype == "delta") && !(evt.Type == "assistant" && evt.TimestampMs != nil) {
			log.Printf("[cursor-agent] Received: type=%s, subtype=%s", evt.Type, evt.Subtype)
		}

		// Track session ID
		if evt.SessionID != "" {
			result.SessionID = evt.SessionID
		}

		switch evt.Type {
		case "system":
			if evt.Subtype == "init" {
				log.Printf("[cursor-agent] Session init: session=%s, model=%s, mode=%s",
					evt.SessionID, evt.Model, evt.PermissionMode)
			}

		case "user":
			// Echo of the user message, skip

		case "thinking":
			switch evt.Subtype {
			case "delta":
				thinkingBuf += evt.Text
				cb(StreamEvent{
					Type:    EventThinking,
					Content: evt.Text,
				})
			case "completed":
				if thinkingBuf != "" {
					log.Printf("[cursor-agent] Thinking completed (%d chars)", len(thinkingBuf))
					thinkingBuf = ""
				}
			default:
				if a.Verbose {
					log.Printf("[cursor-agent] Unhandled thinking subtype=%q", evt.Subtype)
				}
			}

		case "tool_call":
			a.handleToolCall(evt, cb)

		case "assistant":
			a.handleAssistant(evt, cb, &assistantTextBuf, &totalEmittedText)

		case "result":
			a.handleResult(evt, cb, &result)

		default:
			if a.Verbose {
				log.Printf("[cursor-agent] Unhandled event type=%q subtype=%q raw=%s", evt.Type, evt.Subtype, string(line))
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("read cursor stdout: %w", err)
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
			return result, fmt.Errorf("cursor agent CLI exited with code %d: %s", exitErr.ExitCode(), string(stderrBuf))
		}
		return result, fmt.Errorf("wait for cursor agent CLI: %w", err)
	}

	log.Printf("[cursor-agent] Query completed, session ID: %s", result.SessionID)
	return result, nil
}

// ensureMCPConfig merges the nexbox MCP server into the workspace's .cursor/mcp.json.
// If the file already exists, only the "nexbox" entry is added/updated; other servers are preserved.
func (a *CursorAgent) ensureMCPConfig(workDir string) error {
	cursorDir := filepath.Join(workDir, ".cursor")
	mcpPath := filepath.Join(cursorDir, "mcp.json")

	if err := os.MkdirAll(cursorDir, 0755); err != nil {
		return fmt.Errorf("create .cursor dir: %w", err)
	}

	// Read existing config if present
	var config map[string]interface{}
	if existing, err := os.ReadFile(mcpPath); err == nil {
		if err := json.Unmarshal(existing, &config); err != nil {
			log.Printf("[cursor-agent] Warning: existing mcp.json is invalid JSON, will overwrite: %v", err)
			config = nil
		}
	}
	if config == nil {
		config = make(map[string]interface{})
	}

	// Get or create the mcpServers map
	servers, ok := config["mcpServers"].(map[string]interface{})
	if !ok {
		servers = make(map[string]interface{})
	}

	// Add/update only the nexbox entry
	servers["nexbox"] = map[string]interface{}{
		"url": a.MCPServerURL,
	}
	config["mcpServers"] = servers

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal MCP config: %w", err)
	}
	if err := os.WriteFile(mcpPath, data, 0644); err != nil {
		return fmt.Errorf("write MCP config: %w", err)
	}

	log.Printf("[cursor-agent] MCP config written to %s (nexbox entry merged)", mcpPath)
	return nil
}

// handleToolCall processes tool_call events (started/completed).
func (a *CursorAgent) handleToolCall(evt cursorEvent, cb EventCallback) {
	switch evt.Subtype {
	case "started":
		// Extract tool name from the tool_call JSON
		toolName, input := a.parseToolCall(evt.ToolCall)
		cb(StreamEvent{
			Type:     EventToolStart,
			ToolID:   evt.CallID,
			ToolName: toolName,
			Input:    input,
		})

	case "completed":
		// Extract result from tool_call JSON
		_, output := a.parseToolCallResult(evt.ToolCall)
		cb(StreamEvent{
			Type:   EventToolResult,
			ToolID: evt.CallID,
			Output: output,
		})

	default:
		if a.Verbose {
			log.Printf("[cursor-agent] Unhandled tool_call subtype=%q call_id=%s", evt.Subtype, evt.CallID)
		}
	}
}

// handleAssistant processes assistant message events.
// Events with timestamp_ms are streaming deltas; those without are final messages.
//
// Cursor interleaves two kinds of cumulative snapshots:
//   - Global cumulative: totalText covers the entire response from the beginning
//   - Per-turn cumulative: totalText covers only the current segment (after the
//     last tool call)
//
// The interleaving can cause duplication if a per-turn event is naively emitted
// when its content was already emitted via a global cumulative event. We use
// suffix-prefix overlap detection in the fallback path to prevent this.
func (a *CursorAgent) handleAssistant(evt cursorEvent, cb EventCallback, textBuf *string, totalEmitted *string) {
	if len(evt.Message) == 0 {
		return
	}

	var msg cursorMessage
	if err := json.Unmarshal(evt.Message, &msg); err != nil {
		log.Printf("[cursor-agent] Failed to parse assistant message: %v", err)
		return
	}

	if evt.TimestampMs == nil {
		return
	}

	// Extract text from only the last contiguous group of text blocks
	// (after the final non-text block in the content array).
	lastNonTextIdx := -1
	for i, block := range msg.Content {
		if block.Type != "text" {
			lastNonTextIdx = i
		}
	}
	var totalText string
	for i, block := range msg.Content {
		if block.Type == "text" && i > lastNonTextIdx {
			totalText += block.Text
		}
	}
	if totalText == "" {
		return
	}

	// Case 1: totalText extends totalEmitted (global cumulative).
	if strings.HasPrefix(totalText, *totalEmitted) {
		delta := totalText[len(*totalEmitted):]
		if delta != "" {
			cb(StreamEvent{Type: EventTextDelta, Content: delta})
			*totalEmitted = totalText
		}
		*textBuf = totalText
		return
	}

	// Case 2: totalText extends textBuf (per-turn cumulative).
	if strings.HasPrefix(totalText, *textBuf) {
		delta := totalText[len(*textBuf):]
		if delta != "" {
			cb(StreamEvent{Type: EventTextDelta, Content: delta})
			*totalEmitted += delta
		}
		*textBuf = totalText
		return
	}

	// Case 3: re-stream — totalText is a prefix of what we already have.
	if strings.HasPrefix(*textBuf, totalText) || strings.HasPrefix(*totalEmitted, totalText) {
		return
	}

	// Case 3b: totalText is fully contained as a tail of totalEmitted — already
	// emitted as part of a global cumulative event, skip.
	if strings.HasSuffix(*totalEmitted, totalText) {
		*textBuf = totalText
		return
	}

	// Case 3c: totalText partially overlaps with the tail of totalEmitted.
	// Find the longest suffix of totalEmitted that equals a prefix of totalText
	// and emit only the non-overlapping remainder.
	if overlap := suffixPrefixOverlap(*totalEmitted, totalText); overlap > 0 {
		delta := totalText[overlap:]
		if delta != "" {
			cb(StreamEvent{Type: EventTextDelta, Content: delta})
			*totalEmitted += delta
		}
		*textBuf = totalText
		return
	}

	// Case 4: check if this is a cumulative snapshot that mostly matches
	// totalEmitted but diverged mid-stream (e.g. token boundary differences).
	cpLen := 0
	cpLimit := len(totalText)
	if len(*totalEmitted) < cpLimit {
		cpLimit = len(*totalEmitted)
	}
	for cpLen < cpLimit && totalText[cpLen] == (*totalEmitted)[cpLen] {
		cpLen++
	}
	shorter := len(totalText)
	if len(*totalEmitted) < shorter {
		shorter = len(*totalEmitted)
	}
	if cpLen > 0 && shorter > 0 && cpLen > shorter/2 {
		*totalEmitted = totalText
		*textBuf = totalText
		return
	}

	// Case 5: genuinely new text (first text of a new assistant turn).
	cb(StreamEvent{Type: EventTextDelta, Content: totalText})
	*totalEmitted += totalText
	*textBuf = totalText
}

// suffixPrefixOverlap returns the length of the longest suffix of a that
// equals a prefix of b. Returns 0 if there is no overlap.
func suffixPrefixOverlap(a, b string) int {
	maxCheck := len(a)
	if len(b) < maxCheck {
		maxCheck = len(b)
	}
	for i := maxCheck; i > 0; i-- {
		if strings.HasSuffix(a, b[:i]) {
			return i
		}
	}
	return 0
}

// handleResult processes the final result event.
func (a *CursorAgent) handleResult(evt cursorEvent, cb EventCallback, result *Result) {
	log.Printf("[cursor-agent] Result: subtype=%s, is_error=%v, duration=%dms, session=%s",
		evt.Subtype, evt.IsError, evt.DurationMs, evt.SessionID)

	if evt.IsError || evt.Subtype == "error" {
		errMsg := "Agent returned an error"
		if evt.ResultText != "" {
			errMsg = evt.ResultText
		}
		cb(StreamEvent{
			Type:    EventError,
			Content: errMsg,
			IsError: true,
		})
	}

	metadata := make(map[string]interface{})
	metadata["durationMs"] = evt.DurationMs
	metadata["durationApiMs"] = evt.DurationAPIMs
	if evt.RequestID != "" {
		metadata["requestId"] = evt.RequestID
	}

	result.SessionID = evt.SessionID
	result.Metadata = metadata

	cb(StreamEvent{
		Type:      EventDone,
		SessionID: evt.SessionID,
		Metadata:  metadata,
	})
}

// parseToolCall extracts tool name and input from a tool_call JSON.
// Formats:
//   - Built-in: {"bashToolCall":{"args":{...}}}
//   - MCP:      {"mcpToolCall":{"args":{"name":"nexbox-ask_question","args":{actual args},"providerIdentifier":"nexbox","toolName":"ask_question",...}}}
func (a *CursorAgent) parseToolCall(raw json.RawMessage) (toolName string, input string) {
	if len(raw) == 0 {
		return "unknown", ""
	}

	// Parse outer envelope: {"someToolCall": inner}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "unknown", string(raw)
	}

	for key, val := range obj {
		// MCP tool call: {"mcpToolCall": {"args": {"name":"...", "args":{...}, "providerIdentifier":"...", "toolName":"..."}}}
		if key == "mcpToolCall" {
			return a.parseMCPToolCallStarted(val)
		}

		// Built-in tool: {"bashToolCall": {"args": {...}}}
		toolName = cursorToolKeyToName(key)
		var inner struct {
			Args json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal(val, &inner); err == nil && len(inner.Args) > 0 {
			input = string(inner.Args)
		} else {
			input = string(val)
		}
		return toolName, input
	}

	return "unknown", ""
}

// parseMCPToolCallStarted extracts tool name and input from mcpToolCall inner JSON.
// Input: {"args":{"name":"nexbox-ask_question","args":{actual},"providerIdentifier":"nexbox","toolName":"ask_question",...}}
func (a *CursorAgent) parseMCPToolCallStarted(inner json.RawMessage) (toolName string, input string) {
	var wrapper struct {
		Args struct {
			Name               string          `json:"name"`
			Args               json.RawMessage `json:"args"`
			ProviderIdentifier string          `json:"providerIdentifier"`
			ToolName           string          `json:"toolName"`
		} `json:"args"`
	}
	if err := json.Unmarshal(inner, &wrapper); err != nil {
		return "unknown", string(inner)
	}

	name := wrapper.Args.ToolName
	if name == "" {
		name = wrapper.Args.Name
	}
	if wrapper.Args.ProviderIdentifier != "" {
		name = wrapper.Args.ProviderIdentifier + "/" + name
	}
	return name, string(wrapper.Args.Args)
}

// parseToolCallResult extracts tool result from a completed tool_call JSON.
// Formats:
//   - Built-in: {"bashToolCall":{"args":{...},"result":{...}}}
//   - MCP:      {"mcpToolCall":{"args":{...},"result":{"success":{"content":[{"text":{"text":"..."}}],"isError":false}}}}
func (a *CursorAgent) parseToolCallResult(raw json.RawMessage) (toolName string, output string) {
	if len(raw) == 0 {
		return "unknown", ""
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "unknown", string(raw)
	}

	for key, val := range obj {
		// MCP tool call result
		if key == "mcpToolCall" {
			return a.parseMCPToolCallResult(val)
		}

		// Built-in tool result
		toolName = cursorToolKeyToName(key)
		var inner struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(val, &inner); err == nil && len(inner.Result) > 0 {
			output = string(inner.Result)
		} else {
			output = string(val)
		}
		return toolName, output
	}

	return "unknown", ""
}

// parseMCPToolCallResult extracts tool name and result from mcpToolCall completed JSON.
// Input: {"args":{"providerIdentifier":"nexbox","toolName":"ask_question",...},"result":{"success":{"content":[{"text":{"text":"..."}}],"isError":false}}}
func (a *CursorAgent) parseMCPToolCallResult(inner json.RawMessage) (toolName string, output string) {
	var wrapper struct {
		Args struct {
			ProviderIdentifier string `json:"providerIdentifier"`
			ToolName           string `json:"toolName"`
			Name               string `json:"name"`
		} `json:"args"`
		Result struct {
			Success struct {
				Content []struct {
					Text struct {
						Text string `json:"text"`
					} `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"success"`
		} `json:"result"`
	}
	if err := json.Unmarshal(inner, &wrapper); err != nil {
		return "unknown", string(inner)
	}

	name := wrapper.Args.ToolName
	if name == "" {
		name = wrapper.Args.Name
	}
	if wrapper.Args.ProviderIdentifier != "" {
		name = wrapper.Args.ProviderIdentifier + "/" + name
	}

	// Extract text from result.success.content[].text.text
	var parts []string
	for _, c := range wrapper.Result.Success.Content {
		if c.Text.Text != "" {
			parts = append(parts, c.Text.Text)
		}
	}
	if len(parts) > 0 {
		output = strings.Join(parts, "\n")
	} else {
		output = string(inner)
	}

	return name, output
}

// cursorToolKeyToName maps cursor's tool_call key names to friendly tool names.
func cursorToolKeyToName(key string) string {
	// Map known cursor tool call keys to canonical names
	nameMap := map[string]string{
		"lsToolCall":              "LS",
		"bashToolCall":            "Bash",
		"shellToolCall":           "Shell",
		"readToolCall":            "Read",
		"writeToolCall":           "Write",
		"editToolCall":            "Edit",
		"multiEditToolCall":       "MultiEdit",
		"grepToolCall":            "Grep",
		"globToolCall":            "Glob",
		"semSearchToolCall":       "SemanticSearch",
		"semanticSearchToolCall":  "SemanticSearch",
		"webSearchToolCall":       "WebSearch",
		"webFetchToolCall":        "WebFetch",
		"taskToolCall":            "Task",
		"deleteToolCall":          "Delete",
		"strReplaceToolCall":      "StrReplace",
		"updateTodosToolCall":     "UpdateTodos",
		"readTodosToolCall":       "ReadTodos",
		"readLintsToolCall":       "ReadLints",
		"switchModeToolCall":      "SwitchMode",
		"createPlanToolCall":      "CreatePlan",
		"writeShellStdinToolCall": "WriteShellStdin",
		"listMcpResourcesToolCall": "ListMcpResources",
		"readMcpResourceToolCall":  "ReadMcpResource",
		"askQuestionToolCall":      "AskQuestion",
	}
	if name, ok := nameMap[key]; ok {
		return name
	}
	return key
}

// buildPrompt constructs the combined prompt.
// For new sessions: staticContext + dynamicContext + message
// For resumed sessions: dynamicContext + message
func (a *CursorAgent) buildPrompt(req Request) string {
	isNewSession := req.SessionID == ""
	return BuildPrompt(req.StaticContext, req.DynamicContext, req.Message, isNewSession, req.Images)
}

// buildArgs constructs CLI arguments for cursor agent.
func (a *CursorAgent) buildArgs(req Request, prompt string) []string {
	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--stream-partial-output",
		"-f", // force allow commands
	}

	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}

	if req.WorkDir != "" {
		args = append(args, "--workspace", req.WorkDir)
	}

	// Session resume
	if req.SessionID != "" {
		args = append(args, "--resume", req.SessionID)
	}

	// Auto-approve MCP servers (needed for workspace .cursor/mcp.json)
	args = append(args, "--approve-mcps")

	// Append the prompt
	args = append(args, prompt)

	return args
}
