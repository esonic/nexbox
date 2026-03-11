package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// CodexAgent implements the Agent interface using `codex app-server`.
type CodexAgent struct {
	CLIPath      string
	MCPServerURL string
	Verbose      bool

	urlMCPChecked bool
	urlMCPSupport bool

	modelMu           sync.RWMutex
	supportedModels   []string
	supportedModelSrc string // dynamic | fallback
	modelsLoaded      bool
}

var codexFallbackModels = []string{"ops", "sonnet", "haiku"}

// NewCodexAgent creates a Codex agent with default settings.
func NewCodexAgent() *CodexAgent {
	return &CodexAgent{
		CLIPath: "codex",
		Verbose: true,
	}
}

// Ensure CodexAgent implements Agent.
var _ Agent = (*CodexAgent)(nil)

// --- app-server payloads (subset) -------------------------------------------------------------

type codexThreadResponse struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
}

type codexTurnStartResponse struct {
	Turn struct {
		ID string `json:"id"`
	} `json:"turn"`
}

type codexThreadStartedNotification struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
}

type codexTurnStartedNotification struct {
	ThreadID string `json:"threadId"`
	Turn     struct {
		ID string `json:"id"`
	} `json:"turn"`
}

type codexErrorNotification struct {
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
	WillRetry bool   `json:"willRetry"`
	Error     struct {
		Message string `json:"message"`
	} `json:"error"`
}

type codexItemNotification struct {
	ThreadID string          `json:"threadId"`
	TurnID   string          `json:"turnId"`
	Item     codexThreadItem `json:"item"`
}

type codexTurnCompletedNotification struct {
	ThreadID string    `json:"threadId"`
	Turn     codexTurn `json:"turn"`
}

type codexTurn struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Error  *codexTurnError `json:"error,omitempty"`
}

type codexTurnError struct {
	Message           string `json:"message"`
	AdditionalDetails string `json:"additionalDetails,omitempty"`
}

type codexTurnPlanUpdatedNotification struct {
	ThreadID    string `json:"threadId"`
	TurnID      string `json:"turnId"`
	Explanation string `json:"explanation,omitempty"`
	Plan        []struct {
		Step   string `json:"step"`
		Status string `json:"status"`
	} `json:"plan"`
}

type codexAgentMessageDeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type codexReasoningTextDeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type codexReasoningSummaryTextDeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type codexMcpToolCallProgressNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Message  string `json:"message"`
}

type codexThreadTokenUsageUpdatedNotification struct {
	ThreadID   string                `json:"threadId"`
	TurnID     string                `json:"turnId"`
	TokenUsage codexThreadTokenUsage `json:"tokenUsage"`
}

type codexThreadTokenUsage struct {
	Last  codexTokenUsageBreakdown `json:"last"`
	Total codexTokenUsageBreakdown `json:"total"`
}

type codexTokenUsageBreakdown struct {
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	InputTokens           int64 `json:"inputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
	TotalTokens           int64 `json:"totalTokens"`
}

type codexThreadItem struct {
	ID               string            `json:"id"`
	Type             string            `json:"type"`
	Text             string            `json:"text,omitempty"`
	Phase            string            `json:"phase,omitempty"`
	Command          string            `json:"command,omitempty"`
	AggregatedOutput *string           `json:"aggregatedOutput,omitempty"`
	ExitCode         *int              `json:"exitCode,omitempty"`
	Status           string            `json:"status,omitempty"`
	Changes          []codexFileChange `json:"changes,omitempty"`
	Server           string            `json:"server,omitempty"`
	Tool             string            `json:"tool,omitempty"`
	Arguments        json.RawMessage   `json:"arguments,omitempty"`
	Result           json.RawMessage   `json:"result,omitempty"`
	Error            json.RawMessage   `json:"error,omitempty"`
	Query            string            `json:"query,omitempty"`
	Content          []string          `json:"content,omitempty"`
	Summary          []string          `json:"summary,omitempty"`
	ContentItems     json.RawMessage   `json:"contentItems,omitempty"` // dynamicToolCall result content
	Success          *bool             `json:"success,omitempty"`      // dynamicToolCall
	Prompt           string            `json:"prompt,omitempty"`       // collabAgentToolCall
	AgentsStates     json.RawMessage   `json:"agentsStates,omitempty"` // collabAgentToolCall
}

type codexFileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // add | update | delete
}

// -----------------------------------------------------------------------------------------------

// SendMessage executes one user turn using codex app-server and streams mapped events.
func (a *CodexAgent) SendMessage(
	ctx context.Context,
	req Request,
	cb EventCallback,
) (Result, error) {
	// Pre-check work directory.
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

	prompt := a.buildPrompt(req)
	if !utf8.ValidString(prompt) {
		log.Printf("[codex-agent] warning: prompt contains invalid UTF-8, sanitizing")
		prompt = strings.ToValidUTF8(prompt, "\uFFFD")
	}

	args := a.buildAppServerArgs(req)
	if a.Verbose {
		log.Printf("[codex-agent] CLI command: %s %v", a.CLIPath, args)
		log.Printf("[codex-agent] ========== Request ==========")
		log.Printf("[codex-agent] conversationID: %s", req.ConversationID)
		log.Printf("[codex-agent] sessionID: %s (new: %v)", req.SessionID, req.SessionID == "")
		log.Printf("[codex-agent] workDir: %s", req.WorkDir)
		log.Printf("[codex-agent] message: %s", req.Message)
		log.Printf("[codex-agent] ===============================")
	}

	client := NewCodexAppServerClient(a.CLIPath, args)
	client.Verbose = a.Verbose
	if err := client.Start(ctx, req.WorkDir); err != nil {
		return Result{}, err
	}
	defer func() {
		if err := client.Close(); err != nil && a.Verbose {
			log.Printf("[codex-agent] codex app-server close warning: %v", err)
		}
	}()

	var (
		result             Result
		stateMu            sync.Mutex
		currentTurnID      string
		doneEmitted        bool
		planToolStarted    bool
		requestToolEmitted = make(map[string]struct{})
		compatExecTools    = make(map[string]string) // codex/event exec id -> emitted tool id
		compatExecInputs   = make(map[string]string) // codex/event exec id -> deferred tool input
		compatExecOutputs  = make(map[string]string) // codex/event exec id -> buffered output delta
		compatExecSkip     = make(map[string]bool)   // codex/event exec id -> canonical item/* already emitted
		compatPatchTools   = make(map[string]string) // codex/event patch id -> emitted tool id
		compatPatchNames   = make(map[string]string) // codex/event patch id -> Edit/Write/Delete
		compatPatchPaths   = make(map[string]string) // codex/event patch id -> changed file path
		compatPatchInputs  = make(map[string]string) // codex/event patch id -> buffered input for delayed start
		compatPatchOutputs = make(map[string]string) // codex/event patch id -> buffered delta/message
		compatPatchStarted = make(map[string]bool)   // codex/event patch id -> whether tool_start emitted
		streamedAgentText  = make(map[string]bool)
		streamedReasoning  = make(map[string]bool)
		latestTokenUsage   *codexThreadTokenUsageUpdatedNotification
	)
	turnDone := make(chan struct{}, 1)
	var turnDoneOnce sync.Once

	emitDone := func(turnID string, turn *codexTurn) {
		stateMu.Lock()
		if doneEmitted {
			stateMu.Unlock()
			return
		}
		doneEmitted = true
		usage := latestTokenUsage
		sessionID := result.SessionID
		stateMu.Unlock()

		metadata := map[string]interface{}{}
		if usage != nil {
			metadata["inputTokens"] = int(usage.TokenUsage.Last.InputTokens)
			metadata["outputTokens"] = int(usage.TokenUsage.Last.OutputTokens)
			metadata["cachedInputTokens"] = int(usage.TokenUsage.Last.CachedInputTokens)
			metadata["reasoningOutputTokens"] = int(usage.TokenUsage.Last.ReasoningOutputTokens)
			metadata["totalTokens"] = int(usage.TokenUsage.Last.TotalTokens)
		}
		result.Metadata = metadata

		if turn != nil && turn.Status == "failed" && turn.Error != nil && turn.Error.Message != "" {
			errMsg := turn.Error.Message
			if turn.Error.AdditionalDetails != "" {
				errMsg += ": " + turn.Error.AdditionalDetails
			}
			cb(StreamEvent{Type: EventError, Content: errMsg})
		}

		cb(StreamEvent{
			Type:      EventDone,
			SessionID: sessionID,
			Metadata:  metadata,
		})

		turnDoneOnce.Do(func() { turnDone <- struct{}{} })
	}

	client.SetServerRequestHandler(func(method string, params json.RawMessage) (interface{}, *codexWireError) {
		switch method {
		case "item/commandExecution/requestApproval":
			return map[string]interface{}{"decision": "accept"}, nil
		case "item/fileChange/requestApproval":
			return map[string]interface{}{"decision": "accept"}, nil
		case "item/permissions/requestApproval":
			var p struct {
				Permissions interface{} `json:"permissions"`
			}
			_ = json.Unmarshal(params, &p)
			if p.Permissions == nil {
				p.Permissions = map[string]interface{}{}
			}
			return map[string]interface{}{
				"permissions": p.Permissions,
				"scope":       "turn",
			}, nil
		case "item/tool/requestUserInput":
			var p struct {
				ItemID    string `json:"itemId"`
				Questions []struct {
					ID       string `json:"id"`
					Header   string `json:"header"`
					Question string `json:"question"`
					Options  []struct {
						ID    string `json:"id"`
						Label string `json:"label"`
					} `json:"options"`
				} `json:"questions"`
			}
			_ = json.Unmarshal(params, &p)

			// Codex app-server still sends requestUserInput for app tool approvals
			// even when thread approvalPolicy is "never". Returning empty answers here
			// causes the tool call to be cancelled ("user cancelled MCP tool call").
			// For explicit app-tool approval prompts, auto-select an approval option.
			if isAppToolApprovalRequest(p.Questions) {
				answers := map[string]interface{}{}
				for qi, q := range p.Questions {
					qid := strings.TrimSpace(q.ID)
					if qid == "" {
						qid = fmt.Sprintf("q%d", qi+1)
					}
					selected := pickApprovalAnswerLabel(q.Options)
					if selected == "" {
						continue
					}
					answers[qid] = map[string]interface{}{
						"answers": []string{selected},
					}
				}
				cb(StreamEvent{
					Type:    EventSystem,
					Content: "Auto-approved app tool call request_user_input prompt to keep MCP tool flow unblocked.",
				})
				return map[string]interface{}{"answers": answers}, nil
			}

			// Reuse existing frontend question panel by emitting an ask_question tool call shape.
			type uiQuestionOption struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			}
			type uiQuestion struct {
				ID            string             `json:"id"`
				Prompt        string             `json:"prompt"`
				Options       []uiQuestionOption `json:"options"`
				AllowMultiple bool               `json:"allow_multiple"`
			}
			var uiQuestions []uiQuestion
			for qi, q := range p.Questions {
				uq := uiQuestion{
					ID:            q.ID,
					Prompt:        q.Question,
					AllowMultiple: false,
				}
				for oi, opt := range q.Options {
					label := strings.TrimSpace(opt.Label)
					if label == "" {
						continue
					}
					uq.Options = append(uq.Options, uiQuestionOption{
						ID:    fmt.Sprintf("q%d_opt%d", qi+1, oi+1),
						Label: label,
					})
				}
				// QuestionCard requires at least 2 options.
				if len(uq.Options) >= 2 {
					uiQuestions = append(uiQuestions, uq)
				}
			}
			if len(uiQuestions) > 0 {
				title := "Please confirm"
				if p.Questions[0].Header != "" {
					title = p.Questions[0].Header
				}
				askInputJSON, _ := json.Marshal(map[string]interface{}{
					"title":     title,
					"questions": uiQuestions,
				})
				cb(StreamEvent{
					Type:     EventToolStart,
					ToolID:   p.ItemID,
					ToolName: "nexbox/ask_question",
					Input:    string(askInputJSON),
				})
				cb(StreamEvent{
					Type:   EventToolResult,
					ToolID: p.ItemID,
					Output: "Questions shown. Wait for user's next message before finalizing assumptions.",
				})
			}

			// Do not auto-pick defaults here. Keep answers empty so we don't
			// pretend the user has made a selection.
			answers := map[string]interface{}{}
			cb(StreamEvent{
				Type:    EventSystem,
				Content: "Codex requested user input; emitted ask_question UI and returned empty answers (no auto-selection).",
			})
			return map[string]interface{}{"answers": answers}, nil
		case "mcpServer/elicitation/request":
			cb(StreamEvent{
				Type:    EventSystem,
				Content: "MCP elicitation request received and auto-declined in this integration.",
			})
			return map[string]interface{}{"action": "decline"}, nil
		case "item/tool/call":
			call, err := parseCodexDynamicToolCall(params)
			if err != nil {
				if a.Verbose {
					log.Printf("[codex-agent] failed to parse item/tool/call: %v, params=%s", err, string(params))
				}
				return buildCodexDynamicToolResponse(false, "Failed to parse tool call: "+err.Error()), nil
			}

			toolID := call.ItemID
			if strings.TrimSpace(toolID) == "" {
				toolID = fmt.Sprintf("toolreq:%d", time.Now().UnixNano())
			}
			toolInput := string(call.Arguments)
			if strings.TrimSpace(toolInput) == "" || toolInput == "null" {
				toolInput = "{}"
			}
			cb(StreamEvent{
				Type:     EventToolStart,
				ToolID:   toolID,
				ToolName: call.ToolName,
				Input:    toolInput,
			})

			toolCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()

			output, execErr := runCodexDynamicToolCall(toolCtx, req.WorkDir, call.ToolName, call.Arguments)
			if execErr != nil {
				msg := execErr.Error()
				if strings.TrimSpace(output) != "" {
					msg += "\n" + output
				}
				cb(StreamEvent{
					Type:    EventToolResult,
					ToolID:  toolID,
					Output:  msg,
					IsError: true,
				})
				if call.ItemID != "" {
					stateMu.Lock()
					requestToolEmitted[call.ItemID] = struct{}{}
					stateMu.Unlock()
				}
				if a.Verbose {
					log.Printf("[codex-agent] dynamic tool failed: tool=%s item=%s err=%v", call.ToolName, call.ItemID, execErr)
				}
				return buildCodexDynamicToolResponse(false, msg), nil
			}
			cb(StreamEvent{
				Type:   EventToolResult,
				ToolID: toolID,
				Output: output,
			})
			if call.ItemID != "" {
				stateMu.Lock()
				requestToolEmitted[call.ItemID] = struct{}{}
				stateMu.Unlock()
			}
			return buildCodexDynamicToolResponse(true, output), nil
		case "applyPatchApproval":
			return map[string]interface{}{"decision": "approved"}, nil
		case "execCommandApproval":
			return map[string]interface{}{"decision": "approved"}, nil
		case "account/chatgptAuthTokens/refresh":
			return nil, &codexWireError{
				Code:    -32601,
				Message: "chatgpt auth token refresh is not supported by NexBox Codex bridge",
			}
		default:
			if a.Verbose {
				log.Printf("[codex-agent] Unhandled server request method=%s", method)
			}
			return map[string]interface{}{}, nil
		}
	})

	client.SetNotificationHandler(func(method string, params json.RawMessage) {
		switch method {
		case "thread/started":
			var n codexThreadStartedNotification
			if json.Unmarshal(params, &n) == nil && n.Thread.ID != "" {
				stateMu.Lock()
				if result.SessionID == "" {
					result.SessionID = n.Thread.ID
				}
				stateMu.Unlock()
			}

		case "turn/started":
			var n codexTurnStartedNotification
			if json.Unmarshal(params, &n) == nil && n.Turn.ID != "" {
				stateMu.Lock()
				currentTurnID = n.Turn.ID
				stateMu.Unlock()
			}

		case "item/agentMessage/delta":
			var n codexAgentMessageDeltaNotification
			if err := json.Unmarshal(params, &n); err == nil && n.Delta != "" {
				stateMu.Lock()
				streamedAgentText[n.ItemID] = true
				stateMu.Unlock()
				cb(StreamEvent{Type: EventTextDelta, Content: n.Delta})
			}

		case "item/reasoning/textDelta":
			var n codexReasoningTextDeltaNotification
			if err := json.Unmarshal(params, &n); err == nil && n.Delta != "" {
				stateMu.Lock()
				streamedReasoning[n.ItemID] = true
				stateMu.Unlock()
				cb(StreamEvent{Type: EventThinking, Content: n.Delta})
			}

		case "item/reasoning/summaryTextDelta":
			var n codexReasoningSummaryTextDeltaNotification
			if err := json.Unmarshal(params, &n); err == nil && n.Delta != "" {
				stateMu.Lock()
				streamedReasoning[n.ItemID] = true
				stateMu.Unlock()
				cb(StreamEvent{Type: EventThinking, Content: n.Delta})
			}

		case "codex/event/exec_command_begin":
			if payload, ok := parseRawJSONMap(params); ok {
				rawID := firstJSONMapStringDeep(payload, "call_id", "callId", "id", "item_id", "itemId", "tool_call_id", "toolCallId")
				if rawID == "" {
					rawID = fmt.Sprintf("exec:%d", time.Now().UnixNano())
				}
				toolID := "compat-exec:" + rawID
				cmd := firstJSONMapStringDeep(payload, "command", "cmd")
				input := "{}"
				if cmd != "" {
					cmdJSON, _ := json.Marshal(map[string]string{"command": normalizeExecCommandText(cmd)})
					input = string(cmdJSON)
				}
				stateMu.Lock()
				compatExecTools[rawID] = toolID
				compatExecInputs[rawID] = input
				compatExecOutputs[rawID] = ""
				compatExecSkip[rawID] = false
				stateMu.Unlock()
			}

		case "codex/event/exec_command_output_delta":
			if payload, ok := parseRawJSONMap(params); ok {
				rawID := firstJSONMapStringDeep(payload, "call_id", "callId", "id", "item_id", "itemId", "tool_call_id", "toolCallId")
				if rawID == "" {
					return
				}
				delta := firstJSONMapStringDeep(payload, "delta", "output", "stdout", "stderr", "text", "content")
				if delta == "" {
					return
				}
				stateMu.Lock()
				compatExecOutputs[rawID] += delta
				stateMu.Unlock()
			}

		case "codex/event/exec_command_end":
			if payload, ok := parseRawJSONMap(params); ok {
				rawID := firstJSONMapStringDeep(payload, "call_id", "callId", "id", "item_id", "itemId", "tool_call_id", "toolCallId")
				stateMu.Lock()
				toolID := compatExecTools[rawID]
				if toolID == "" {
					toolID = "compat-exec:" + rawID
				}
				input := compatExecInputs[rawID]
				bufferedOutput := compatExecOutputs[rawID]
				skip := compatExecSkip[rawID]
				delete(compatExecTools, rawID)
				delete(compatExecInputs, rawID)
				delete(compatExecOutputs, rawID)
				delete(compatExecSkip, rawID)
				stateMu.Unlock()
				if skip {
					return
				}

				output := firstJSONMapStringDeep(payload, "output", "stdout", "stderr", "result", "message")
				if strings.TrimSpace(output) == "" {
					output = bufferedOutput
				}
				exitCode := firstJSONMapIntDeep(payload, "exit_code", "exitCode")
				success := firstJSONMapBoolDeep(payload, "success")
				isError := (exitCode != 0) || (!success && hasJSONMapKeyDeep(payload, "success"))
				cb(StreamEvent{
					Type:     EventToolStart,
					ToolID:   toolID,
					ToolName: "Bash",
					Input:    input,
				})
				cb(StreamEvent{
					Type:    EventToolResult,
					ToolID:  toolID,
					Output:  output,
					IsError: isError,
				})
			}

		case "codex/event/patch_apply_begin":
			if payload, ok := parseRawJSONMap(params); ok {
				rawID := firstJSONMapStringDeep(payload, "call_id", "callId", "id", "item_id", "itemId", "tool_call_id", "toolCallId")
				if rawID == "" {
					rawID = fmt.Sprintf("patch:%d", time.Now().UnixNano())
				}
				toolID := "compat-patch:" + rawID

				toolName, input, path := codexPatchToolStart(payload, params)
				if input == "" {
					input = "{}"
				}

				stateMu.Lock()
				compatPatchTools[rawID] = toolID
				compatPatchNames[rawID] = toolName
				compatPatchPaths[rawID] = path
				compatPatchInputs[rawID] = input
				compatPatchOutputs[rawID] = ""
				compatPatchStarted[rawID] = false
				stateMu.Unlock()
				// If path is unknown at begin, defer tool_start until we can infer file name
				// from outputDelta/end message so UI can show "Edited <file>".
				if path != "" {
					stateMu.Lock()
					compatPatchStarted[rawID] = true
					stateMu.Unlock()
					cb(StreamEvent{
						Type:     EventToolStart,
						ToolID:   toolID,
						ToolName: toolName,
						Input:    input,
					})
				}
			}

		case "item/fileChange/outputDelta":
			if payload, ok := parseRawJSONMap(params); ok {
				rawID := firstJSONMapStringDeep(payload, "item_id", "itemId", "id", "call_id", "callId")
				delta := firstJSONMapStringDeep(payload, "delta", "output", "text", "content")
				if delta == "" {
					return
				}
				var emitStart *StreamEvent
				stateMu.Lock()
				// Some payloads don't carry the same id as patch_apply_*.
				// If exactly one patch call is in-flight, attribute delta to it.
				if rawID == "" {
					if len(compatPatchTools) == 1 {
						for id := range compatPatchTools {
							rawID = id
						}
					}
				}
				if _, ok := compatPatchOutputs[rawID]; ok {
					compatPatchOutputs[rawID] += delta
					if compatPatchPaths[rawID] == "" {
						compatPatchPaths[rawID] = extractPatchPathFromResultText(delta)
					}
					if !compatPatchStarted[rawID] && compatPatchPaths[rawID] != "" {
						compatPatchStarted[rawID] = true
						inputJSON, _ := json.Marshal(map[string]string{"path": compatPatchPaths[rawID]})
						emitStart = &StreamEvent{
							Type:     EventToolStart,
							ToolID:   compatPatchTools[rawID],
							ToolName: compatPatchNames[rawID],
							Input:    string(inputJSON),
						}
					}
				}
				stateMu.Unlock()
				if emitStart != nil {
					cb(*emitStart)
				}
			}

		case "codex/event/patch_apply_end":
			if payload, ok := parseRawJSONMap(params); ok {
				rawID := firstJSONMapStringDeep(payload, "call_id", "callId", "id", "item_id", "itemId", "tool_call_id", "toolCallId")
				stateMu.Lock()
				toolID := compatPatchTools[rawID]
				if toolID == "" {
					toolID = "compat-patch:" + rawID
				}
				toolName := compatPatchNames[rawID]
				path := compatPatchPaths[rawID]
				toolInput := compatPatchInputs[rawID]
				bufferedOutput := compatPatchOutputs[rawID]
				started := compatPatchStarted[rawID]
				delete(compatPatchTools, rawID)
				delete(compatPatchNames, rawID)
				delete(compatPatchPaths, rawID)
				delete(compatPatchInputs, rawID)
				delete(compatPatchOutputs, rawID)
				delete(compatPatchStarted, rawID)
				stateMu.Unlock()

				msg := firstJSONMapStringDeep(payload, "message", "result", "output")
				if strings.TrimSpace(msg) == "" {
					msg = bufferedOutput
				}
				if path == "" {
					path = extractPatchPathFromResultText(msg)
				}
				if !started {
					if path != "" {
						inputJSON, _ := json.Marshal(map[string]string{"path": path})
						toolInput = string(inputJSON)
					}
					if strings.TrimSpace(toolInput) == "" {
						toolInput = "{}"
					}
					cb(StreamEvent{
						Type:     EventToolStart,
						ToolID:   toolID,
						ToolName: toolName,
						Input:    toolInput,
					})
				}
				if strings.TrimSpace(msg) == "" && path != "" {
					msg = codexPatchResultMessage(toolName, path)
				}
				success := firstJSONMapBoolDeep(payload, "success")
				isError := !success && hasJSONMapKeyDeep(payload, "success")
				cb(StreamEvent{
					Type:    EventToolResult,
					ToolID:  toolID,
					Output:  msg,
					IsError: isError,
				})
			}

		case "item/started":
			var n codexItemNotification
			if err := json.Unmarshal(params, &n); err == nil {
				if n.Item.Type == "commandExecution" && n.Item.ID != "" {
					stateMu.Lock()
					if _, ok := compatExecSkip[n.Item.ID]; ok {
						compatExecSkip[n.Item.ID] = true
					}
					stateMu.Unlock()
				}
				stateMu.Lock()
				_, skip := requestToolEmitted[n.Item.ID]
				stateMu.Unlock()
				if skip {
					return
				}
				emitCodexItemStarted(n.Item, cb)
			}

		case "item/completed":
			var n codexItemNotification
			if err := json.Unmarshal(params, &n); err == nil {
				stateMu.Lock()
				_, skip := requestToolEmitted[n.Item.ID]
				if skip {
					delete(requestToolEmitted, n.Item.ID)
				}
				stateMu.Unlock()
				if skip {
					return
				}
				stateMu.Lock()
				agentStreamed := streamedAgentText[n.Item.ID]
				reasoningStreamed := streamedReasoning[n.Item.ID]
				stateMu.Unlock()
				emitCodexItemCompleted(n.Item, agentStreamed, reasoningStreamed, cb)
			}

		case "item/mcpToolCall/progress":
			var n codexMcpToolCallProgressNotification
			if err := json.Unmarshal(params, &n); err == nil && n.Message != "" {
				cb(StreamEvent{Type: EventSystem, Content: n.Message})
			}

		case "turn/plan/updated":
			var n codexTurnPlanUpdatedNotification
			if err := json.Unmarshal(params, &n); err == nil {
				planJSON, _ := json.Marshal(n)
				if !planToolStarted {
					planToolStarted = true
					cb(StreamEvent{
						Type:     EventToolStart,
						ToolID:   "plan:" + n.TurnID,
						ToolName: "todo_list",
						Input:    string(planJSON),
					})
				}
				cb(StreamEvent{
					Type:   EventToolResult,
					ToolID: "plan:" + n.TurnID,
					Output: formatCodexPlanUpdate(n),
				})
			}

		case "thread/tokenUsage/updated":
			var n codexThreadTokenUsageUpdatedNotification
			if err := json.Unmarshal(params, &n); err == nil {
				stateMu.Lock()
				latestTokenUsage = &n
				stateMu.Unlock()
			}

		case "error":
			var n codexErrorNotification
			if err := json.Unmarshal(params, &n); err == nil {
				if n.WillRetry {
					cb(StreamEvent{Type: EventSystem, Content: n.Error.Message})
				} else {
					cb(StreamEvent{Type: EventError, Content: n.Error.Message})
				}
			}

		case "turn/completed":
			var n codexTurnCompletedNotification
			if err := json.Unmarshal(params, &n); err == nil {
				stateMu.Lock()
				matchTurn := currentTurnID == "" || n.Turn.ID == "" || n.Turn.ID == currentTurnID
				stateMu.Unlock()
				if matchTurn {
					emitDone(n.Turn.ID, &n.Turn)
				}
			}

		default:
			if a.Verbose && !codexIsIgnorableNotification(method) {
				log.Printf("[codex-agent] Unhandled notification method=%s", method)
			}
		}
	})

	if err := client.Initialize(ctx, "nexbox", "1.0.0"); err != nil {
		return Result{}, fmt.Errorf("initialize app-server: %w", err)
	}

	// Start or resume thread.
	if req.SessionID != "" {
		var resp codexThreadResponse
		err := client.Request(ctx, "thread/resume", a.buildThreadResumeParams(req), &resp)
		if err != nil {
			if isCodexSessionExpiredError(err) {
				return Result{}, ErrSessionExpired
			}
			return Result{}, err
		}
		result.SessionID = resp.Thread.ID
	} else {
		var resp codexThreadResponse
		if err := client.Request(ctx, "thread/start", a.buildThreadStartParams(req), &resp); err != nil {
			return Result{}, err
		}
		result.SessionID = resp.Thread.ID
	}
	if result.SessionID == "" {
		return Result{}, errors.New("codex app-server did not return thread id")
	}

	// Start the turn.
	turnStartParams := map[string]interface{}{
		"threadId": result.SessionID,
		"input": []map[string]interface{}{
			{
				"type": "text",
				"text": prompt,
			},
		},
	}
	if req.Model != "" {
		turnStartParams["model"] = req.Model
	}

	var turnResp codexTurnStartResponse
	if err := client.Request(ctx, "turn/start", turnStartParams, &turnResp); err != nil {
		return Result{}, err
	}
	if turnResp.Turn.ID != "" {
		stateMu.Lock()
		currentTurnID = turnResp.Turn.ID
		stateMu.Unlock()
	}

	// Wait for completion or termination.
	select {
	case <-turnDone:
	case <-ctx.Done():
		return result, context.Canceled
	case <-client.readDone:
		return result, fmt.Errorf("codex app-server terminated before turn completion: %w; stderr: %s", client.readErr, client.Stderr())
	}

	// Defensive done emission when the server exits right after turn completion.
	stateMu.Lock()
	missingDone := !doneEmitted
	stateMu.Unlock()
	if missingDone {
		emitDone("", nil)
	}

	return result, nil
}

// WarmupSupportedModels best-effort initializes the model cache.
func (a *CodexAgent) WarmupSupportedModels(ctx context.Context) {
	_, _ = a.ListSupportedModels(ctx)
}

// ListSupportedModels returns codex model IDs. It prefers dynamic discovery via
// app-server model/list and falls back to a fixed tier list.
func (a *CodexAgent) ListSupportedModels(ctx context.Context) ([]string, string) {
	a.modelMu.RLock()
	if a.modelsLoaded {
		models := append([]string(nil), a.supportedModels...)
		source := a.supportedModelSrc
		a.modelMu.RUnlock()
		return models, source
	}
	a.modelMu.RUnlock()

	if ctx == nil {
		ctx = context.Background()
	}

	models, source := a.detectSupportedModels(ctx)

	a.modelMu.Lock()
	if !a.modelsLoaded {
		a.supportedModels = append([]string(nil), models...)
		a.supportedModelSrc = source
		a.modelsLoaded = true
	}
	models = append([]string(nil), a.supportedModels...)
	source = a.supportedModelSrc
	a.modelMu.Unlock()

	return models, source
}

func (a *CodexAgent) detectSupportedModels(ctx context.Context) ([]string, string) {
	models, err := a.listModelsViaAppServer(ctx)
	if err == nil && len(models) > 0 {
		if a.Verbose {
			log.Printf("[codex-agent] Loaded %d models via app-server model/list", len(models))
		}
		return models, "dynamic"
	}
	if a.Verbose && err != nil {
		log.Printf("[codex-agent] model/list failed, fallback to static models: %v", err)
	}
	return append([]string(nil), codexFallbackModels...), "fallback"
}

func (a *CodexAgent) listModelsViaAppServer(ctx context.Context) ([]string, error) {
	client := NewCodexAppServerClient(a.CLIPath, a.buildAppServerArgs(Request{}))
	client.Verbose = false

	if err := client.Start(ctx, ""); err != nil {
		return nil, fmt.Errorf("start app-server for model/list: %w", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.Initialize(ctx, "nexbox", "1.0.0"); err != nil {
		return nil, fmt.Errorf("initialize app-server for model/list: %w", err)
	}

	var resp struct {
		Data []struct {
			Model string `json:"model"`
		} `json:"data"`
	}
	if err := client.Request(ctx, "model/list", map[string]interface{}{
		"includeHidden": false,
	}, &resp); err != nil {
		return nil, fmt.Errorf("request model/list: %w", err)
	}

	seen := make(map[string]struct{})
	models := make([]string, 0, len(resp.Data))
	for _, item := range resp.Data {
		model := strings.TrimSpace(item.Model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	if len(models) == 0 {
		return nil, errors.New("model/list returned no models")
	}
	return models, nil
}

// buildPrompt constructs the combined prompt.
func (a *CodexAgent) buildPrompt(req Request) string {
	isNewSession := req.SessionID == ""
	return BuildPrompt(req.StaticContext, req.DynamicContext, req.Message, isNewSession, req.Images)
}

func (a *CodexAgent) buildAppServerArgs(req Request) []string {
	args := []string{"app-server", "--listen", "stdio://"}
	for _, ov := range a.buildConfigOverrides(req) {
		args = append(args, "-c", ov)
	}
	return args
}

func (a *CodexAgent) buildThreadStartParams(req Request) map[string]interface{} {
	params := map[string]interface{}{
		"approvalPolicy": "never",
		"sandbox":        "workspace-write",
	}
	if req.Model != "" {
		params["model"] = req.Model
	}
	if req.WorkDir != "" {
		params["cwd"] = req.WorkDir
	}
	return params
}

func (a *CodexAgent) buildThreadResumeParams(req Request) map[string]interface{} {
	params := map[string]interface{}{
		"threadId":       req.SessionID,
		"approvalPolicy": "never",
		"sandbox":        "workspace-write",
	}
	if req.Model != "" {
		params["model"] = req.Model
	}
	if req.WorkDir != "" {
		params["cwd"] = req.WorkDir
	}
	return params
}

func isCodexSessionExpiredError(err error) bool {
	var rpcErr *codexRPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	msg := strings.ToLower(rpcErr.Message)
	if strings.Contains(msg, "thread") && strings.Contains(msg, "not found") {
		return true
	}
	if strings.Contains(msg, "thread") && strings.Contains(msg, "does not exist") {
		return true
	}
	if strings.Contains(msg, "thread") && strings.Contains(msg, "missing") {
		return true
	}
	return false
}

// supportsURLMCP checks whether codex CLI supports URL MCP servers.
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
	fields := strings.Fields(versionStr)
	if len(fields) == 0 {
		return false
	}
	version := fields[len(fields)-1]
	version = strings.TrimPrefix(version, "v")
	if i := strings.Index(version, "-"); i >= 0 {
		version = version[:i]
	}

	a.urlMCPSupport = compareVersionGE(version, "0.99.0")
	if a.urlMCPSupport {
		log.Printf("[codex-agent] Codex %s supports URL-based MCP", version)
	} else {
		log.Printf("[codex-agent] Codex %s does not support URL-based MCP", version)
	}
	return a.urlMCPSupport
}

func (a *CodexAgent) buildConfigOverrides(req Request) []string {
	var cfg []string
	cfg = a.appendNexboxMCPConfig(cfg, req.ConversationID)
	cfg = a.appendChromeMCPConfig(cfg, req)
	cfg = a.appendExtraMCPConfig(cfg, req.ExtraMCPServers)
	// Prefer ask_question MCP flow over app-server request_user_input so we can
	// reuse the existing frontend Q&A interaction loop.
	cfg = append(cfg, "features.default_mode_request_user_input=false")

	if hasFeature(req.Features, "web_search") {
		cfg = append(cfg, `web_search_mode="live"`)
	}

	return cfg
}

func (a *CodexAgent) appendNexboxMCPConfig(cfg []string, conversationID string) []string {
	if a.MCPServerURL == "" || !a.supportsURLMCP() {
		return cfg
	}
	url := a.MCPServerURL
	if conversationID != "" {
		url += "?session=" + conversationID
	}
	return append(cfg, fmt.Sprintf("mcp_servers.nexbox.url=%q", url))
}

func (a *CodexAgent) appendChromeMCPConfig(cfg []string, req Request) []string {
	if !hasFeature(req.Features, "chrome_devtools") || req.ChromeMCPURL == "" {
		return cfg
	}
	if !a.supportsURLMCP() {
		log.Printf("[codex-agent] Chrome DevTools MCP requires codex >= 0.99.0, skipping")
		return cfg
	}
	cfg = append(cfg, fmt.Sprintf("mcp_servers.chrome.url=%q", req.ChromeMCPURL))
	log.Printf("[codex-agent] Chrome DevTools MCP enabled: %s", req.ChromeMCPURL)
	return cfg
}

func (a *CodexAgent) appendExtraMCPConfig(cfg []string, extras []MCPServerEntry) []string {
	supportsURL := a.supportsURLMCP()
	for _, mcp := range extras {
		switch {
		case mcp.URL != "":
			if !supportsURL {
				log.Printf("[codex-agent] skip HTTP MCP %s: codex version does not support URL MCPs", mcp.Key)
				continue
			}
			cfg = append(cfg, fmt.Sprintf("mcp_servers.%s.url=%q", mcp.Key, mcp.URL))
			for hk, hv := range mcp.Headers {
				cfg = append(cfg, fmt.Sprintf("mcp_servers.%s.http_headers.%s=%q", mcp.Key, hk, hv))
			}
		case mcp.Cmd != "":
			cfg = append(cfg, fmt.Sprintf("mcp_servers.%s.command=%q", mcp.Key, mcp.Cmd))
			for i, arg := range mcp.Args {
				cfg = append(cfg, fmt.Sprintf("mcp_servers.%s.args.%d=%q", mcp.Key, i, arg))
			}
			for ek, ev := range mcp.Env {
				cfg = append(cfg, fmt.Sprintf("mcp_servers.%s.env.%s=%q", mcp.Key, ek, ev))
			}
		}
	}
	return cfg
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
