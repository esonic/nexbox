package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// ClaudeAgent implements the Agent interface using the Claude Code CLI.
// It spawns a claude subprocess with stream-json I/O for each message exchange.
type ClaudeAgent struct {
	CLIPath      string            // Path to claude CLI binary (default: "claude")
	MCPServerURL string            // Optional: URL of the NexBox MCP server
	MCPToolNames []string          // MCP tool names (e.g. "mcp__nexbox__save_asset"); populated from mcp.Server.ToolNames()
	DataDir      string            // For isolated session storage
	Verbose      bool              // Enable verbose logging
	ContainerMgr *ContainerManager // Optional: run inside Docker container per project
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

// HasMCP returns whether MCP is configured for this agent.
func (a *ClaudeAgent) HasMCP() bool {
	return a.MCPServerURL != ""
}

// SendMessage implements Agent.SendMessage using the Claude Code CLI.
func (a *ClaudeAgent) SendMessage(
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

	// Create Claude CLI client
	client := NewClaudeClient()
	client.Verbose = a.Verbose

	// If Docker isolation is enabled, ensure a container is running for this project
	// and wire it into the client so commands execute inside the container.
	if a.ContainerMgr != nil && req.WorkDir != "" {
		// Derive a project key from the work directory to identify the container.
		// req.WorkDir is typically the project's git repo path.
		containerID, err := a.ContainerMgr.EnsureContainer(req.WorkDir, req.WorkDir)
		if err != nil {
			log.Printf("[claude-agent] failed to ensure container for %s: %v; falling back to host execution", req.WorkDir, err)
		} else {
			client.ContainerMgr = a.ContainerMgr
			client.ContainerID = containerID
		}
	}

	// Build query options (no SystemPrompt - we use BuildPrompt for all context)
	opts := QueryOptions{
		WorkDir: req.WorkDir,
		Model:   req.Model,
	}

	// Full capabilities - all tools available
	// opts.AllowedTools = []string{
	// 	// Core development tools
	// 	"Bash", "Read", "Write", "Edit", "Glob", "Grep",
	// 	// Planning & organization
	// 	"EnterPlanMode", "ExitPlanMode",
	// 	// Web resources
	// 	"WebSearch", "WebFetch",
	// 	// task
	// 	"AskUserQuestion", "Agent", "Skill", "TaskCreate", "TaskGet", "TaskList", "TaskUpdate", "TaskStop", "TaskOutput",
	// }
	// Use bypassPermissions as the base mode — the control protocol's
	// can_use_tool handler in claude_cli.go will auto-allow non-interactive
	// tools, so this is effectively the same behavior. Interactive tools
	// (AskUserQuestion) are routed to the UserInputHandler.
	opts.PermissionMode = "bypassPermissions"

	// Wire interactive handler for AskUserQuestion support
	if req.UserInputHandler != nil {
		opts.UserInputHandler = req.UserInputHandler
	}

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

		opts.AllowedTools = append(opts.AllowedTools, a.MCPToolNames...)

		if hasFeature(req.Features, "chrome_devtools") && req.ChromeMCPURL != "" {
			servers["chrome"] = map[string]interface{}{
				"type": "http",
				"url":  req.ChromeMCPURL,
			}
			opts.AllowedTools = append(opts.AllowedTools, req.ChromeMCPToolNames...)
			log.Printf("[claude-agent] Chrome DevTools MCP enabled: %s", req.ChromeMCPURL)
		}

		// Inject extra MCP servers from installed marketplace packages
		for _, mcp := range req.ExtraMCPServers {
			entry := map[string]interface{}{"type": mcp.Type}
			if mcp.Type == "http" {
				entry["url"] = mcp.URL
				if len(mcp.Headers) > 0 {
					entry["headers"] = mcp.Headers
				}
			} else if mcp.Type == "stdio" {
				entry["command"] = mcp.Cmd
				if len(mcp.Args) > 0 {
					entry["args"] = mcp.Args
				}
				if len(mcp.Env) > 0 {
					entry["env"] = mcp.Env
				}
			}
			servers[mcp.Key] = entry
			for _, toolName := range mcp.ToolNames {
				opts.AllowedTools = append(opts.AllowedTools, "mcp__"+mcp.Key+"__"+toolName)
			}
		}

		mcpConfig := map[string]interface{}{"mcpServers": servers}
		mcpConfigBytes, _ := json.Marshal(mcpConfig)

		tmpFile, err := os.CreateTemp(a.DataDir, "beehub-mcp-*.json")
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

	// Resume session if exists
	if req.SessionID != "" {
		opts.Resume = req.SessionID
	}

	// Ensure DataDir exists for any temp files
	if a.DataDir != "" {
		if err := os.MkdirAll(filepath.Join(a.DataDir, "tmp"), 0755); err != nil {
			log.Printf("[claude-agent] Failed to create tmp dir: %v", err)
		}
	}

	// Build the combined prompt
	isNewSession := req.SessionID == ""
	combinedPrompt := BuildPrompt(req.StaticContext, req.DynamicContext, req.Message, isNewSession, req.Images)

	// Log request details
	log.Printf("[claude-agent] ========== Request ==========")
	log.Printf("[claude-agent] conversationID: %s", req.ConversationID)
	log.Printf("[claude-agent] sessionID: %s (new: %v)", req.SessionID, isNewSession)
	log.Printf("[claude-agent] workDir: %s", req.WorkDir)
	log.Printf("[claude-agent] message: %s", req.Message)
	log.Printf("[claude-agent] allowedTools: %v", opts.AllowedTools)
	if opts.MCPConfigPath != "" {
		log.Printf("[claude-agent] mcpConfigPath: %s", opts.MCPConfigPath)
	}
	log.Printf("[claude-agent] ===============================")

	// Execute query and translate Claude CLI events to unified StreamEvents
	var result Result
	isResumeAttempt := req.SessionID != ""
	resumeFailed := false

	resultSessionID, err := client.Query(ctx, combinedPrompt, opts, func(msg StreamMessage, parsed *AssistantMessage) {
		log.Printf("[claude-agent] Received: type=%s, subtype=%s, session=%s", msg.Type, msg.Subtype, msg.SessionID)

		switch msg.Type {
		case "system":
			log.Printf("[claude-agent] System message: subtype=%s", msg.Subtype)

			// Handle system init message - extract tools, MCP servers info
			if msg.Subtype == "init" {
				// Parse init data for useful information
				if msg.Data != nil {
					var tools []string
					if toolsRaw, ok := msg.Data["tools"].([]interface{}); ok {
						for _, t := range toolsRaw {
							if toolName, ok := t.(string); ok {
								tools = append(tools, toolName)
							}
						}
					}
					log.Printf("[claude-agent] Session initialized with tools: %v", tools)

					// Extract MCP server status
					if mcpServersRaw, ok := msg.Data["mcp_servers"].([]interface{}); ok {
						for _, srv := range mcpServersRaw {
							if srvMap, ok := srv.(map[string]interface{}); ok {
								name, _ := srvMap["name"].(string)
								status, _ := srvMap["status"].(string)
								log.Printf("[claude-agent] MCP server: %s, status: %s", name, status)
							}
						}
					}
				}
			}

			// Handle compact boundary - context was compacted
			if msg.Subtype == "compact_boundary" {
				log.Printf("[claude-agent] Context compaction occurred")
				cb(StreamEvent{
					Type:    EventSystem,
					Content: "Context compacted to fit within limits",
				})
			}

		case "assistant":
			if parsed != nil {
				for _, content := range parsed.Content {
					switch content.Type {
					case "text":
						cb(StreamEvent{
							Type:    EventTextDelta,
							Content: content.Text,
						})

					case "tool_use":
						inputJSON, _ := json.Marshal(content.Input)
						cb(StreamEvent{
							Type:     EventToolStart,
							ToolID:   content.ID,
							ToolName: content.Name,
							Input:    string(inputJSON),
						})

					case "thinking":
						if content.Thinking != "" {
							cb(StreamEvent{
								Type:     EventThinking,
								ToolID:   content.ID,
								ToolName: "Thinking",
								Content:  content.Thinking,
							})
						}

					case "redacted_thinking":
						log.Printf("[claude-agent] Redacted thinking block (content hidden)")
						cb(StreamEvent{
							Type:     EventThinking,
							ToolName: "Thinking",
							Content:  "[Thinking content redacted]",
						})

					default:
						log.Printf("[claude-agent] Unhandled content block type=%q id=%s", content.Type, content.ID)
					}
				}
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
										Type:    EventToolResult,
										ToolID:  toolUseID,
										Output:  output,
										IsError: isError,
									})
								}
							}
						}
					}
				}
			}

		case "result":
			log.Printf("[claude-agent] Result: session=%s, isError=%v, subtype=%s, result=%s, errors=%v",
				msg.SessionID, msg.IsError, msg.Subtype, msg.Result, msg.Errors)

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
			}
			if msg.TotalCostUSD > 0 {
				metadata["totalCostUsd"] = msg.TotalCostUSD
			}
			metadata["durationMs"] = msg.DurationMs

			result.SessionID = msg.SessionID
			result.Metadata = metadata

			cb(StreamEvent{
				Type:      EventDone,
				SessionID: msg.SessionID,
				Metadata:  metadata,
			})

		default:
			log.Printf("[claude-agent] Unhandled message type=%q subtype=%q", msg.Type, msg.Subtype)
		}
	})

	if resumeFailed {
		return Result{}, ErrSessionExpired
	}
	if err != nil {
		return Result{}, fmt.Errorf("claude query: %w", err)
	}

	// Use session ID from query result if not set from result event
	if result.SessionID == "" {
		result.SessionID = resultSessionID
	}

	log.Printf("[claude-agent] Query completed, session ID: %s", result.SessionID)
	return result, nil
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
