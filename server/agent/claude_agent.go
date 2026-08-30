package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ClaudeAgent implements the Agent interface using the Claude Code CLI.
// It spawns a claude subprocess with stream-json I/O for each message exchange.
type ClaudeAgent struct {
	CLIPath      string   // Path to claude CLI binary (default: "claude")
	MCPServerURL string   // Optional: URL of the NexBox MCP server
	MCPToolNames []string // MCP tool names (e.g. "mcp__nexbox__save_asset"); populated from mcp.Server.ToolNames()
	DataDir      string   // For isolated session storage
	Verbose      bool     // Enable verbose logging

	// Cached init_response data. Populated by the first ProbeInitialize() call
	// or opportunistically whenever a real SendMessage receives its init_response.
	modelsMu     sync.Mutex
	cachedModels []ModelInfo
	cachedProbed bool
}

// NewClaudeAgent creates a new Claude Code agent with default settings.
func NewClaudeAgent() *ClaudeAgent {
	return &ClaudeAgent{
		CLIPath: "claude",
		Verbose: true,
	}
}

// Ensure ClaudeAgent implements Agent at compile time.
var _ Agent = (*ClaudeAgent)(nil)

func (a *ClaudeAgent) Capabilities() Capabilities {
	return Capabilities{Fork: true, Interject: true, ChromeDevTools: true, ModelSwitchMode: ModelSwitchCurrentSession}
}

// HasMCP returns whether MCP is configured for this agent.
func (a *ClaudeAgent) HasMCP() bool {
	return a.MCPServerURL != ""
}

// stripWatermarkChars removes invisible combining marks that an upstream
// platform layer injects as a watermark into model text output (currently a
// repeated U+05B4 prefix). Filtering inside the agent keeps these characters
// out of every downstream consumer (WebSocket, DB, sub-agent callers) without
// touching tool inputs/outputs that may legitimately contain other Unicode.
func stripWatermarkChars(s string) string {
	if !strings.ContainsRune(s, '\u05B4') {
		return s
	}
	return strings.ReplaceAll(s, "\u05B4", "")
}

// terminalTaskStatuses mirrors TERMINAL_TASK_STATUSES in the Claude Agent SDK:
// a task with one of these statuses has finished. task_notification reports
// "stopped" (the CLI's mapped form of a killed task) while task_updated reports
// the raw "killed" — treat both the same. Non-terminal: pending/running/paused.
var terminalTaskStatuses = map[string]bool{
	"completed": true,
	"failed":    true,
	"stopped":   true,
	"killed":    true,
}

func isTerminalTaskStatus(status string) bool { return terminalTaskStatuses[status] }

// translateAssistantContent converts the content blocks of one complete
// `assistant` stream-json message into unified StreamEvents.
//
// parentToolUseID is the `parent_tool_use_id` of the message: empty for the
// main agent, set when the message was produced by a sub-agent (Task tool).
//
// Sub-agent text/thinking belongs to that sub-agent's context, NOT the main
// transcript. The CLI delivers a sub-agent's narration as ordinary `assistant`
// messages (carrying parent_tool_use_id) with NO partial streaming — verified
// against the real CLI in claude_subagent_stream_test.go. Forwarding it as a
// top-level text/thinking event leaked the sub-agent's words into the main
// reply (and merged them with the main agent's prose). So we suppress text and
// thinking from sub-agent messages here: their tool calls still stream (nested
// under the Agent card via ParentToolID), and their outcome flows back to the
// main agent through the Task tool_result plus the subtask summary.
//
// hasPartialText/hasPartialThinking indicate the main agent already streamed
// this block via stream_event deltas, so the consolidated copy is skipped to
// avoid duplication (only meaningful for the main agent — sub-agents never set
// these because they don't stream partials).
func translateAssistantContent(
	content []AssistantContent,
	parentToolUseID string,
	hasPartialText, hasPartialThinking bool,
	cb EventCallback,
) {
	isSubAgent := parentToolUseID != ""
	for _, c := range content {
		switch c.Type {
		case "text":
			if isSubAgent {
				continue // sub-agent narration: not part of the main transcript
			}
			if !hasPartialText {
				cb(StreamEvent{Type: EventTextDelta, Content: c.Text})
			}

		case "tool_use":
			inputJSON, _ := json.Marshal(c.Input)
			cb(StreamEvent{
				Type:         EventToolStart,
				ToolID:       c.ID,
				ToolName:     c.Name,
				Input:        string(inputJSON),
				ParentToolID: parentToolUseID,
			})

		case "thinking":
			if isSubAgent || c.Thinking == "" {
				continue // sub-agent reasoning is internal to the Agent card
			}
			if hasPartialThinking {
				continue // already streamed via stream_event deltas
			}
			var thinkingMeta map[string]interface{}
			if c.Signature != "" {
				thinkingMeta = map[string]interface{}{"signature": c.Signature}
			}
			cb(StreamEvent{
				Type:     EventThinking,
				ToolID:   c.ID,
				ToolName: "Thinking",
				Content:  c.Thinking,
				Metadata: thinkingMeta,
			})

		case "redacted_thinking":
			if isSubAgent {
				continue
			}
			log.Printf("[claude-agent] Redacted thinking block (content hidden)")
			cb(StreamEvent{
				Type:     EventThinking,
				ToolName: "Thinking",
				Content:  "[Thinking content redacted]",
			})

		default:
			log.Printf("[claude-agent] Unhandled content block type=%q id=%s", c.Type, c.ID)
		}
	}
}

// SendMessage implements Agent.SendMessage using the Claude Code CLI.
func (a *ClaudeAgent) SendMessage(
	ctx context.Context,
	req Request,
	cb EventCallback,
) (Result, error) {
	// Wrap cb so model-generated natural-language fields (text + thinking) are
	// scrubbed of upstream watermark chars before reaching the caller. Tool
	// inputs/outputs are passed through untouched.
	rawCB := cb
	cb = func(event StreamEvent) {
		if event.Type == EventTextDelta || event.Type == EventThinking {
			event.Content = stripWatermarkChars(event.Content)
			if event.Content == "" {
				return
			}
		}
		rawCB(event)
	}

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

	// Create Claude CLI client
	client := NewClaudeClient()
	client.Verbose = a.Verbose

	// Validate effort against the live Claude catalog. The catalog is the
	// CLI's own list — if the requested effort isn't advertised for the
	// selected model, drop it rather than forwarding an unsupported flag.
	effort := a.validateEffort(req.Model, req.Effort)

	// Build query options (no SystemPrompt - we use BuildPrompt for all context)
	opts := QueryOptions{
		WorkDir:                req.WorkDir,
		Model:                  req.Model,
		Effort:                 effort,
		IncludePartialMessages: true,
	}

	// Don't restrict tools — let Claude CLI discover all available tools from
	// MCP servers. bypassPermissions handles the security model.
	opts.AllowedTools = nil
	opts.PermissionMode = "bypassPermissions"

	// Wire up interactive tool support (AskUserQuestion)
	opts.UserInputHandler = req.UserInputHandler

	// Wire up live message injection (interjection) when the API layer requested it.
	opts.RegisterInterject = req.RegisterInterject

	// Claude has its own built-in AskUserQuestion tool, so block the MCP version
	// to avoid confusion. Codex doesn't have a built-in equivalent, so it uses
	// the MCP ask_question tool instead.
	opts.DisallowedTools = []string{"mcp__nexbox__ask_question"}

	// Generate MCP config if MCPServerURL is set
	var mcpConfigPath string
	if a.MCPServerURL != "" {
		// Append the nexbox conversation ID as ?session= so the MCP server can
		// identify which session is making tool calls without the AI passing it.
		nexboxURL := a.MCPServerURL
		if req.ConversationID != "" {
			nexboxURL += "?session=" + req.ConversationID
		}
		servers := map[string]interface{}{
			"nexbox": map[string]interface{}{
				"type": "http",
				"url":  nexboxURL,
			},
		}

		if hasFeature(req.Features, "chrome_devtools") && req.ChromeMCPURL != "" {
			servers["chrome"] = map[string]interface{}{
				"type": "http",
				"url":  req.ChromeMCPURL,
			}
			log.Printf("[claude-agent] Chrome DevTools MCP enabled: %s", req.ChromeMCPURL)
		}

		// Inject extra MCP servers from installed marketplace packages.
		for _, mcp := range req.ExtraMCPServers {
			entry := map[string]interface{}{}
			if mcp.Type == "http" {
				entry["type"] = "http"
				mcpURL := mcp.URL
				if mcp.AppendSessionURL && req.ConversationID != "" {
					mcpURL += "?session=" + req.ConversationID
				}
				entry["url"] = mcpURL
				if len(mcp.Headers) > 0 {
					entry["headers"] = mcp.Headers
				}
			} else {
				// stdio: Claude CLI infers type from "command" field; do NOT include "type"
				entry["command"] = mcp.Cmd
				if len(mcp.Args) > 0 {
					entry["args"] = mcp.Args
				}
				if len(mcp.Env) > 0 {
					entry["env"] = mcp.Env
				}
			}
			servers[mcp.Key] = entry
		}

		mcpConfig := map[string]interface{}{"mcpServers": servers}
		mcpConfigBytes, _ := json.Marshal(mcpConfig)

		tmpFile, err := os.CreateTemp(a.DataDir, "nexbox-mcp-*.json")
		if err != nil {
			return Result{}, fmt.Errorf("create MCP config temp file: %w", err)
		}
		mcpConfigPath = tmpFile.Name()
		defer os.Remove(mcpConfigPath)

		if _, err := tmpFile.Write(mcpConfigBytes); err != nil {
			tmpFile.Close()
			return Result{}, fmt.Errorf("write MCP config: %w", err)
		}
		tmpFile.Close()

		opts.MCPConfigPath = mcpConfigPath

		log.Printf("[claude-agent] MCP config: %s", mcpConfigPath)
		log.Printf("[claude-agent] MCP config content: %s", string(mcpConfigBytes))
	}

	// Plugin directories (installed from marketplace)
	if len(req.PluginDirs) > 0 {
		opts.PluginDirs = req.PluginDirs
		log.Printf("[claude-agent] Plugin dirs: %v", req.PluginDirs)
	}

	// Resume / fork: a fork is just a resume of the parent's CLI session id
	// with --fork-session (and optionally --resume-session-at <uuid>) layered
	// on. The new session id is reported back via the result message.
	if req.Fork != nil {
		if req.Fork.SessionID == "" || req.Fork.Checkpoint == "" {
			return Result{}, fmt.Errorf("claude fork requires session id and checkpoint")
		}
		opts.Resume = req.Fork.SessionID
		opts.ForkSession = true
		opts.ResumeAtMessageUUID = req.Fork.Checkpoint
	} else if req.SessionID != "" {
		opts.Resume = req.SessionID
	}

	// Ensure DataDir exists for any temp files
	if a.DataDir != "" {
		if err := os.MkdirAll(filepath.Join(a.DataDir, "tmp"), 0755); err != nil {
			log.Printf("[claude-agent] Failed to create tmp dir: %v", err)
		}
	}

	// Build the combined prompt. A fork inherits the parent's context, so
	// it's effectively a resume — the new session is "new" only on the
	// Nexbox side, not in terms of model context.
	isNewSession := req.SessionID == "" && req.Fork == nil
	combinedPrompt := BuildPrompt(req.StaticContext, req.DynamicContext, req.Message, isNewSession, req.Images)

	// Log request details
	log.Printf("[claude-agent] ========== Request ==========")
	log.Printf("[claude-agent] conversationID: %s", req.ConversationID)
	log.Printf("[claude-agent] sessionID: %s (new: %v)", req.SessionID, isNewSession)
	if req.Fork != nil {
		log.Printf("[claude-agent] fork: parent=%s", req.Fork.SessionID)
	}
	log.Printf("[claude-agent] workDir: %s", req.WorkDir)
	log.Printf("[claude-agent] message: %s", req.Message)
	log.Printf("[claude-agent] allowedTools: %v", opts.AllowedTools)
	if opts.MCPConfigPath != "" {
		log.Printf("[claude-agent] mcpConfigPath: %s", opts.MCPConfigPath)
	}
	log.Printf("[claude-agent] ===============================")

	// Execute query and translate Claude CLI events to unified StreamEvents
	var result Result
	// A fork is still "resuming" the parent CLI session under the hood, so a
	// missing parent session should surface as ErrSessionExpired.
	isResumeAttempt := req.SessionID != "" || req.Fork != nil
	resumeFailed := false
	var accInputTokens int
	var hasPartialText, hasPartialThinking bool
	// CLI-resolved main model slug (e.g. "claude-sonnet-4-6"). Captured at
	// init time so the later context_usage emit can pick the correct entry
	// out of `modelUsage` (which also contains subagent models like haiku).
	var mainModelSlug string
	// Last assistant-line UUID seen during the turn. Each assistant content
	// block (text / thinking / tool_use) is its own JSONL line with its own
	// uuid; we keep the latest because Claude's `--resume-session-at` cuts
	// inclusively, so forking at the end of a Nexbox turn requires the last
	// assistant uuid of that turn.
	var lastAssistantUUID string

	// Maps a sub-agent's task_id → the Agent tool_use_id that launched it. Built
	// from task_started (which carries both ids). A background sub-agent signals
	// completion with a `task_updated` message that carries ONLY task_id (no
	// tool_use_id), so we need this map to translate it back to the tool_use_id
	// the UI's Agent card is keyed on. Verified against the real CLI in
	// claude_subagent_stream_test.go: sync tools finish via task_notification
	// (with tool_use_id), but async sub-agents finish via task_updated. Accessed
	// only from the single stream-reading goroutine, so no lock needed.
	subAgentTaskTool := map[string]string{}

	resultSessionID, err := client.Query(ctx, combinedPrompt, opts, func(msg StreamMessage, parsed *AssistantMessage) {
		if msg.Type != "stream_event" {
			log.Printf("[claude-agent] Received: type=%s, subtype=%s, session=%s", msg.Type, msg.Subtype, msg.SessionID)
		}

		switch msg.Type {
		case "system":
			log.Printf("[claude-agent] System message: subtype=%s", msg.Subtype)

			switch msg.Subtype {
			case "init":
				// Init fields are at top level of the system.init message.
				log.Printf("[claude-agent] Session initialized: model=%s perm=%s version=%s tools=%d mcp=%d",
					msg.Model, msg.PermissionMode, msg.ClaudeCodeVersion, len(msg.Tools), len(msg.MCPServers))
				if msg.Model != "" {
					mainModelSlug = msg.Model
				}
				for _, srvMap := range msg.MCPServers {
					name, _ := srvMap["name"].(string)
					status, _ := srvMap["status"].(string)
					log.Printf("[claude-agent] MCP server: %s, status: %s", name, status)
				}
				sessionInfo := map[string]interface{}{}
				if msg.Model != "" {
					sessionInfo["model"] = msg.Model
				}
				if msg.PermissionMode != "" {
					sessionInfo["permissionMode"] = msg.PermissionMode
				}
				if msg.Cwd != "" {
					sessionInfo["cwd"] = msg.Cwd
				}
				if msg.ClaudeCodeVersion != "" {
					sessionInfo["version"] = msg.ClaudeCodeVersion
				}
				if msg.OutputStyle != "" {
					sessionInfo["outputStyle"] = msg.OutputStyle
				}
				if len(sessionInfo) > 0 {
					cb(StreamEvent{Type: EventSessionInfo, Metadata: sessionInfo})
				}
			case "compact_boundary":
				log.Printf("[claude-agent] Context compaction occurred")
				cb(StreamEvent{
					Type:    EventSystem,
					Content: "Context compacted to fit within limits",
				})
			case "init_response":
				log.Printf("[claude-agent] Initialize response: %d models", len(msg.InitModels))
				a.updateModelCacheFromInit(msg.InitModels)
			case "task_started":
				if msg.TaskID != "" && msg.ToolUseID != "" {
					subAgentTaskTool[msg.TaskID] = msg.ToolUseID
				}
				cb(StreamEvent{
					Type:    EventSubTaskStart,
					Content: msg.Description,
					Metadata: map[string]interface{}{
						"task_id":       msg.TaskID,
						"tool_use_id":   msg.ToolUseID,
						"task_type":     msg.TaskType,
						"workflow_name": msg.WorkflowName,
					},
				})
			case "task_progress":
				cb(StreamEvent{
					Type:    EventSubTaskProgress,
					Content: msg.Description,
					Metadata: map[string]interface{}{
						"task_id":           msg.TaskID,
						"last_tool_name":    msg.LastToolName,
						"tool_use_id":       msg.ToolUseID,
						"task_type":         msg.TaskType,
						"workflow_progress": msg.WorkflowProgress,
					},
				})
			case "task_notification":
				// Accumulate subagent token usage into input tokens.
				if msg.Usage != nil {
					if v, ok := msg.Usage["total_tokens"]; ok {
						if n, ok := toInt(v); ok && n > 0 {
							accInputTokens += n
							log.Printf("[claude-agent] Subagent tokens accumulated: %d (all→input)", n)
						}
					}
				}
				cb(StreamEvent{
					Type:    EventSubTaskDone,
					Content: msg.Summary,
					Metadata: map[string]interface{}{
						"task_id":           msg.TaskID,
						"status":            msg.Status,
						"tool_use_id":       msg.ToolUseID,
						"task_type":         msg.TaskType,
						"workflow_progress": msg.WorkflowProgress,
					},
				})
			case "task_updated":
				// A background sub-agent (Agent tool) can signal terminal completion
				// ONLY via task_updated — no task_notification (that's for sync
				// tools). Per the Claude Agent SDK (TaskUpdatedMessage): the status
				// lives in `patch`, not at top level, and only a TERMINAL status
				// means done — pending/running/paused (or a patch with no status)
				// must NOT complete the card. Translate task_id → the launching
				// Agent tool_use_id and emit once (drop from the map).
				patchStatus, _ := msg.Patch["status"].(string)
				if isTerminalTaskStatus(patchStatus) {
					if toolUseID := subAgentTaskTool[msg.TaskID]; toolUseID != "" {
						delete(subAgentTaskTool, msg.TaskID)
						cb(StreamEvent{
							Type:    EventSubTaskDone,
							Content: msg.Summary,
							Metadata: map[string]interface{}{
								"task_id":     msg.TaskID,
								"status":      patchStatus,
								"tool_use_id": toolUseID,
							},
						})
					}
				}
			}

		case "assistant":
			if msg.UUID != "" {
				lastAssistantUUID = msg.UUID
			}
			if parsed != nil {
				translateAssistantContent(parsed.Content, msg.ParentToolUseID, hasPartialText, hasPartialThinking, cb)
				// Reset partial flags after processing the complete assistant message.
				hasPartialText = false
				hasPartialThinking = false
			}

		case "user":
			// User messages contain tool results
			if len(msg.Message) > 0 {
				// Try to parse as a message with content array
				var userMsg struct {
					Role    string      `json:"role"`
					Content interface{} `json:"content"`
				}
				if err := json.Unmarshal(msg.Message, &userMsg); err == nil {
					// Content can be an array of blocks
					if contents, ok := userMsg.Content.([]interface{}); ok {
						for _, c := range contents {
							if cm, ok := c.(map[string]interface{}); ok {
								if cm["type"] == "tool_result" {
									toolUseID, _ := cm["tool_use_id"].(string)
									// Content can be string, array, or nil
									var output string
									var hasImageContent bool
									switch v := cm["content"].(type) {
									case string:
										output = v
									case []interface{}:
										// Array of content blocks - can contain text, image, etc.
										var textParts []string
										for _, block := range v {
											if bm, ok := block.(map[string]interface{}); ok {
												blockType, _ := bm["type"].(string)
												switch blockType {
												case "text":
													if text, ok := bm["text"].(string); ok {
														textParts = append(textParts, text)
													}
												case "image":
													// Image content - note it but don't include base64 data
													hasImageContent = true
													if source, ok := bm["source"].(map[string]interface{}); ok {
														mediaType, _ := source["media_type"].(string)
														textParts = append(textParts, fmt.Sprintf("[Image: %s]", mediaType))
													} else {
														textParts = append(textParts, "[Image]")
													}
												default:
													// Unknown block type - try to extract text
													if text, ok := bm["text"].(string); ok {
														textParts = append(textParts, text)
													}
												}
											}
										}
										output = ""
										for i, part := range textParts {
											if i > 0 {
												output += "\n"
											}
											output += part
										}
									case nil:
										// Nil content is valid for some tool results
										output = ""
									default:
										output = fmt.Sprintf("%v", cm["content"])
									}
									isError, _ := cm["is_error"].(bool)
									// Suppress known benign errors that Claude Code reports as errors
									// but don't indicate actual failures (e.g. sandbox restrictions,
									// parallel sibling call cascades).
									if isError && isBenignToolError(output) {
										log.Printf("[claude-agent] Tool result: id=%s, suppressing benign error: %q", toolUseID, output)
										isError = false
									}
									if hasImageContent {
										log.Printf("[claude-agent] Tool result: id=%s, isError=%v, output_len=%d (contains image)", toolUseID, isError, len(output))
									} else {
										log.Printf("[claude-agent] Tool result: id=%s, isError=%v, output_len=%d", toolUseID, isError, len(output))
									}
									cb(StreamEvent{
										Type:         EventToolResult,
										ToolID:       toolUseID,
										Output:       output,
										IsError:      isError,
										ParentToolID: msg.ParentToolUseID,
									})
								}
							}
						}
					}
				}
			}

		case "result":
			log.Printf("[claude-agent] Result: session=%s, isError=%v, subtype=%s, cost=%.6f, errors=%v",
				msg.SessionID, msg.IsError, msg.Subtype, msg.TotalCostUSD, msg.Errors)

			// Check for errors - both via isError flag AND error subtypes
			isErrorResult := msg.IsError || strings.HasPrefix(msg.Subtype, "error_")

			// Detect resume-related failures: suppress events and let the caller retry
			if isResumeAttempt && isErrorResult && hasSessionError(msg) {
				log.Printf("[claude-agent] Resume failed (errors=%v), marking for retry as new session", msg.Errors)
				resumeFailed = true
				return
			}

			if isErrorResult {
				if msg.Data != nil {
					log.Printf("[claude-agent] Error data: %+v", msg.Data)
				}

				errMsg := buildErrorMessage(msg)
				log.Printf("[claude-agent] Sending error event: %s", errMsg)
				cb(StreamEvent{
					Type:    EventError,
					Content: errMsg,
					IsError: true,
				})
			}

			// Build metadata
			metadata := make(map[string]interface{})
			if msg.Usage != nil {
				if v, ok := msg.Usage["input_tokens"].(float64); ok {
					metadata["inputTokens"] = int(v)
				}
				if v, ok := msg.Usage["output_tokens"].(float64); ok {
					metadata["outputTokens"] = int(v)
				}
				if v, ok := msg.Usage["cache_read_input_tokens"].(float64); ok {
					metadata["cacheReadInputTokens"] = int(v)
				}
				if v, ok := msg.Usage["cache_creation_input_tokens"].(float64); ok {
					metadata["cacheCreationInputTokens"] = int(v)
				}
			}
			if msg.TotalCostUSD > 0 {
				metadata["totalCostUsd"] = msg.TotalCostUSD
			}
			metadata["durationMs"] = msg.DurationMs
			if msg.DurationApiMs > 0 {
				metadata["durationApiMs"] = msg.DurationApiMs
			}
			if msg.StopReason != "" {
				metadata["stopReason"] = msg.StopReason
			}

			// Emit a context_usage event representing the main agent's
			// current prompt size (what Claude's `/context` shows).
			//
			// CAUTION: `result.modelUsage[model]` aggregates ALL API calls
			// made during this turn — including subagent calls (Task tool
			// launching Haiku/Opus subagents) AND each iteration of the
			// main agent's tool-use loop. Summing its cache_read/cache_create
			// inflates the value several-fold vs the actual context size.
			// For example, a 3-iteration turn with a 40k cached prefix
			// reports cache_read ≈ 120k while the true context is ~40k.
			//
			// The correct signal is `result.usage.iterations[last]`: each
			// iteration is one API call whose input+cache_read+cache_create
			// is the FULL prompt size of that call. The last iteration
			// therefore reflects the final state of the main agent's
			// context. Also excludes subagent API calls — they have their
			// own conversation.
			mainModel, mainUsage := pickMainModelUsage(msg.ModelUsage, mainModelSlug, opts.Model)
			used, haveIterUsage := lastIterationPromptSize(msg.Usage)
			if !haveIterUsage && mainUsage != nil {
				// Fallback: no iterations array (rare / unknown CLI shape).
				// Still better than summing cache_read across subagents;
				// at least this scopes to the main model.
				used = int(mainUsage.InputTokens + mainUsage.CacheReadInputTokens + mainUsage.CacheCreationInputTokens)
			}
			if used > 0 || mainUsage != nil {
				meta := map[string]interface{}{
					"model":      mainModel,
					"usedTokens": used,
				}
				if mainUsage != nil {
					meta["inputTokens"] = int(mainUsage.InputTokens)
					meta["outputTokens"] = int(mainUsage.OutputTokens)
					meta["cacheReadInputTokens"] = int(mainUsage.CacheReadInputTokens)
					meta["cacheCreationInputTokens"] = int(mainUsage.CacheCreationInputTokens)
					meta["contextWindow"] = int(mainUsage.ContextWindow)
					meta["maxOutputTokens"] = int(mainUsage.MaxOutputTokens)
				}
				cb(StreamEvent{Type: EventContextUsage, Metadata: meta})
			}

			result.SessionID = msg.SessionID
			result.Checkpoint = lastAssistantUUID
			result.Metadata = metadata

			cb(StreamEvent{
				Type:      EventDone,
				SessionID: msg.SessionID,
				Metadata:  metadata,
			})

		case "stream_event":
			// Partial message streaming: incremental text/thinking deltas.
			if len(msg.Event) > 0 {
				var event struct {
					Type  string `json:"type"`
					Delta struct {
						Type     string `json:"type"`
						Text     string `json:"text"`
						Thinking string `json:"thinking"`
					} `json:"delta"`
				}
				if json.Unmarshal(msg.Event, &event) == nil && event.Type == "content_block_delta" {
					switch event.Delta.Type {
					case "text_delta":
						if event.Delta.Text != "" {
							hasPartialText = true
							cb(StreamEvent{
								Type:    EventTextDelta,
								Content: event.Delta.Text,
							})
						}
					case "thinking_delta":
						if event.Delta.Thinking != "" {
							hasPartialThinking = true
							cb(StreamEvent{
								Type:     EventThinking,
								ToolName: "Thinking",
								Content:  event.Delta.Thinking,
							})
						}
					}
				}
			}

		default:
			log.Printf("[claude-agent] Unhandled message type=%q subtype=%q", msg.Type, msg.Subtype)
		}
	})

	if resumeFailed {
		return Result{}, ErrSessionExpired
	}
	if err != nil {
		// A stopped or errored turn never reaches the `case "result"` branch
		// that populates result.Metadata, so surface the last assistant uuid we
		// saw during streaming. Without this the partial turn is persisted with
		// no durable checkpoint unless we surface the last assistant identity.
		result.Checkpoint = lastAssistantUUID
		return result, fmt.Errorf("claude query: %w", err)
	}

	// Use session ID from query result if not set from result event
	if result.SessionID == "" {
		result.SessionID = resultSessionID
	}

	log.Printf("[claude-agent] Query completed, session ID: %s", result.SessionID)
	return result, nil
}

// ListSupportedModels returns the Claude CLI's live model catalog. The data
// originates from the `initialize` control_response: every field (display
// name, description, effort support) comes straight from the CLI. Once
// populated, the cache is refreshed opportunistically every time a real
// SendMessage completes an init handshake; callers get a fast response.
//
// The second return value is a source label ("dynamic" / "none") mirroring
// the Codex contract used by `ListAvailableModels`.
func (a *ClaudeAgent) ListSupportedModels(ctx context.Context) ([]ModelInfo, string) {
	a.modelsMu.Lock()
	if a.cachedProbed {
		out := append([]ModelInfo(nil), a.cachedModels...)
		a.modelsMu.Unlock()
		return out, "dynamic"
	}
	a.modelsMu.Unlock()

	client := NewClaudeClient()
	if a.CLIPath != "" {
		client.CLIPath = a.CLIPath
	}
	client.Verbose = false

	init, err := client.ProbeInitialize(ctx)
	if err != nil {
		log.Printf("[claude-agent] ProbeInitialize failed: %v — falling back to standard aliases", err)
		fallback := mergeStandardClaudeAliases(nil)
		a.modelsMu.Lock()
		a.cachedModels = fallback
		a.cachedProbed = true
		a.modelsMu.Unlock()
		return append([]ModelInfo(nil), fallback...), "fallback"
	}

	models := convertClaudeInitModels(init.Models)
	a.modelsMu.Lock()
	a.cachedModels = models
	a.cachedProbed = true
	a.modelsMu.Unlock()

	return append([]ModelInfo(nil), models...), "dynamic"
}

// WarmupSupportedModels is best-effort eager initialization, mirroring the
// Codex agent's hook so server startup can pre-warm the model cache.
func (a *ClaudeAgent) WarmupSupportedModels(ctx context.Context) {
	_, _ = a.ListSupportedModels(ctx)
}

// updateModelCacheFromInit refreshes the in-memory catalog with data
// delivered as part of a normal SendMessage's init handshake.
func (a *ClaudeAgent) updateModelCacheFromInit(models []ClaudeInitModel) {
	if len(models) == 0 {
		return
	}
	converted := convertClaudeInitModels(models)
	a.modelsMu.Lock()
	a.cachedModels = converted
	a.cachedProbed = true
	a.modelsMu.Unlock()
}

// convertClaudeInitModels projects the Claude-specific init payload onto the
// unified ModelInfo shape the API layer consumes. After conversion we make
// sure the three standard CLI aliases (opus / sonnet / haiku) are present:
// the CLI's catalog is sometimes incomplete (sonnet has been observed to
// disappear depending on subscription tier / region / CLI version) but the
// missing alias still works as a --model argument, so we synthesize an entry
// for any that the live response left out.
func convertClaudeInitModels(in []ClaudeInitModel) []ModelInfo {
	out := make([]ModelInfo, 0, len(in))
	for _, m := range in {
		info := ModelInfo{
			ID:          m.Value,
			Label:       m.DisplayName,
			Description: m.Description,
			IsDefault:   m.Value == "default",
		}
		if m.SupportsEffort {
			info.SupportedEfforts = append([]string(nil), m.SupportedEffortLevels...)
			// Claude doesn't return a per-model default; pick the highest
			// non-experimental level the model actually accepts.
			info.DefaultEffort = pickClaudeDefaultEffort(info.SupportedEfforts)
		}
		caps := map[string]bool{}
		if m.SupportsAdaptiveThinking {
			caps["adaptiveThinking"] = true
		}
		if m.SupportsFastMode {
			caps["fastMode"] = true
		}
		if m.SupportsAutoMode {
			caps["autoMode"] = true
		}
		if len(caps) > 0 {
			info.Capabilities = caps
		}
		out = append(out, info)
	}
	return mergeStandardClaudeAliases(out)
}

// standardClaudeAliases is the canonical short-name list the Claude CLI has
// historically accepted as --model arguments. Order here is the display
// order we use when synthesizing a missing entry.
var standardClaudeAliases = []struct {
	id, label string
}{
	{"opus", "Opus"},
	{"sonnet", "Sonnet"},
	{"haiku", "Haiku"},
}

// mergeStandardClaudeAliases ensures opus / sonnet / haiku each appear in the
// model list. Missing entries are appended with a minimal stub — effort
// levels are copied from any peer that exposes them so the UI's EFFORT picker
// has reasonable choices.
//
// "Already present" is matched case-insensitively against both the ID
// (claude-sonnet-4-6, sonnet) and the label (Sonnet) — the CLI typically
// returns the long ID with the short word as the label, so a literal
// `present[alias.id]` lookup would double-list every family.
func mergeStandardClaudeAliases(models []ModelInfo) []ModelInfo {
	hasAlias := func(alias string) bool {
		for _, m := range models {
			if strings.Contains(strings.ToLower(m.ID), alias) ||
				strings.EqualFold(m.Label, alias) {
				return true
			}
		}
		return false
	}

	var sampleEfforts []string
	var sampleDefault string
	for _, m := range models {
		if sampleEfforts == nil && len(m.SupportedEfforts) > 0 {
			sampleEfforts = append([]string(nil), m.SupportedEfforts...)
			sampleDefault = m.DefaultEffort
		}
	}
	for _, alias := range standardClaudeAliases {
		if hasAlias(alias.id) {
			continue
		}
		stub := ModelInfo{
			ID:    alias.id,
			Label: alias.label,
		}
		if len(sampleEfforts) > 0 {
			stub.SupportedEfforts = append([]string(nil), sampleEfforts...)
			stub.DefaultEffort = sampleDefault
		}
		models = append(models, stub)
	}
	return models
}

// validateEffort drops the requested effort level if it isn't advertised
// for the current model in the cached catalog. This is a defense-in-depth
// guard against stale session state (e.g. a session that kept an effort
// from a different agent or a since-deprecated model). If the cache is
// empty (CLI unreachable at startup), we forward the effort as-is.
func (a *ClaudeAgent) validateEffort(model, effort string) string {
	if effort == "" {
		return ""
	}
	a.modelsMu.Lock()
	info := a.cachedModels
	probed := a.cachedProbed
	a.modelsMu.Unlock()
	if !probed || len(info) == 0 {
		return effort
	}
	for _, m := range info {
		if m.ID != model {
			continue
		}
		for _, allowed := range m.SupportedEfforts {
			if allowed == effort {
				return effort
			}
		}
		log.Printf("[claude-agent] dropping unsupported effort %q for model %q (allowed: %v)",
			effort, model, m.SupportedEfforts)
		return ""
	}
	// Unknown model (e.g. user typed a custom slug) — pass through.
	return effort
}

// lastIterationPromptSize returns the size (input + cache_read +
// cache_creation) of the last API call made during this turn, as
// recorded in `result.usage.iterations[]`. That value matches what
// Claude's `/context` slash command reports. Returns (size, true) if
// iterations data is available, otherwise (0, false).
func lastIterationPromptSize(usage map[string]interface{}) (int, bool) {
	if usage == nil {
		return 0, false
	}
	itersRaw, ok := usage["iterations"].([]interface{})
	if !ok || len(itersRaw) == 0 {
		return 0, false
	}
	last, ok := itersRaw[len(itersRaw)-1].(map[string]interface{})
	if !ok {
		return 0, false
	}
	intOr := func(k string) int {
		if v, ok := last[k].(float64); ok {
			return int(v)
		}
		return 0
	}
	return intOr("input_tokens") + intOr("cache_read_input_tokens") + intOr("cache_creation_input_tokens"), true
}

// pickMainModelUsage selects the entry from `result.modelUsage` that
// belongs to the main agent (as opposed to subagent models like Haiku
// spawned by the Task tool). Strategy, in order:
//
//  1. The CLI-resolved slug captured from the `system.init` message
//     (exact match, e.g. "claude-sonnet-4-6").
//  2. Fuzzy match on the `--model` alias (e.g. opts.Model="sonnet"
//     matches key "claude-sonnet-4-6").
//  3. The entry with the largest input+cache_create — subagents are
//     typically called with tiny prompts compared to the main thread.
//
// Returns (slug, entry) or ("", nil) if the map is empty.
func pickMainModelUsage(usage map[string]ClaudeModelUsageEntry, resolvedSlug, alias string) (string, *ClaudeModelUsageEntry) {
	if len(usage) == 0 {
		return "", nil
	}
	if resolvedSlug != "" {
		if entry, ok := usage[resolvedSlug]; ok {
			entry := entry
			return resolvedSlug, &entry
		}
	}
	if alias != "" {
		needle := strings.ToLower(alias)
		for k, v := range usage {
			if strings.Contains(strings.ToLower(k), needle) {
				v := v
				return k, &v
			}
		}
	}
	var bestKey string
	var best ClaudeModelUsageEntry
	var bestScore int64 = -1
	for k, v := range usage {
		score := v.InputTokens + v.CacheCreationInputTokens + v.CacheReadInputTokens
		if score > bestScore {
			bestScore = score
			bestKey = k
			best = v
		}
	}
	return bestKey, &best
}

// pickClaudeDefaultEffort returns the pre-selected effort for a Claude
// model based on the CLI-reported `supportedEffortLevels`. Claude's
// initialize response does not include a per-model default, so we pick
// the safest common default — `medium` — falling back to `high` then
// `low` if `medium` isn't advertised. We intentionally avoid `xhigh` and
// `max` as auto-defaults because they're opt-in "burn" modes.
func pickClaudeDefaultEffort(efforts []string) string {
	has := map[string]bool{}
	for _, e := range efforts {
		has[e] = true
	}
	for _, candidate := range []string{"medium", "high", "low"} {
		if has[candidate] {
			return candidate
		}
	}
	if len(efforts) > 0 {
		return efforts[0]
	}
	return ""
}

// isSessionError returns true for error subtypes that indicate the resumed
// session is invalid and the request should be retried as a new session.
// hasSessionError returns true when the result message indicates the resumed
// session is invalid. It checks both the subtype and the errors array for
// session-not-found patterns.
func hasSessionError(msg StreamMessage) bool {
	switch msg.Subtype {
	case "session_not_found", "error_session":
		return true
	}
	for _, e := range msg.Errors {
		if strings.Contains(e, "No conversation found") {
			return true
		}
	}
	return false
}

func hasFeature(features []string, name string) bool {
	for _, f := range features {
		if f == name {
			return true
		}
	}
	return false
}

// isBenignToolError returns true for known tool errors that should not be
// surfaced as failures in the UI. These are environment-specific or
// Claude Code internal conditions that don't indicate actual task failures.
var benignToolErrorPatterns = []string{
	// Claude Code cascades this to sibling parallel tool calls when one fails;
	// the affected calls may still have succeeded or been retried.
	"Sibling tool call errored",
}

func isBenignToolError(output string) bool {
	for _, p := range benignToolErrorPatterns {
		if strings.Contains(output, p) {
			return true
		}
	}
	return false
}

// buildErrorMessage produces a user-friendly error string from a result message.
func buildErrorMessage(msg StreamMessage) string {
	// Prefer the errors array — it carries the most detail
	if len(msg.Errors) > 0 {
		return strings.Join(msg.Errors, "; ")
	}

	switch msg.Subtype {
	case "error_max_turns":
		return "Maximum conversation turns reached"
	case "error_during_execution":
		if msg.Result != "" {
			return msg.Result
		}
		if msg.Data != nil {
			if d, ok := msg.Data["error"].(string); ok && d != "" {
				return d
			}
			if d, ok := msg.Data["message"].(string); ok && d != "" {
				return d
			}
		}
		return "Error during execution (no details available)"
	case "error_max_budget_usd":
		return "Maximum budget exceeded"
	case "error_max_structured_output_retries":
		return "Failed to generate valid structured output"
	case "session_not_found", "error_session":
		return "Session expired or not found"
	default:
		if msg.Result != "" {
			return msg.Result
		}
		if msg.Subtype != "" {
			return "Error: " + msg.Subtype
		}
		return "Agent returned an error"
	}
}

// toInt converts a JSON-decoded numeric value (float64 or int) to int.
func toInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}
