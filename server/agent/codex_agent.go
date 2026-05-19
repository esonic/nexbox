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
	supportedModels   []ModelInfo
	supportedModelSrc string // dynamic | fallback | none
	modelsLoaded      bool
}

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
	Last               codexTokenUsageBreakdown `json:"last"`
	Total              codexTokenUsageBreakdown `json:"total"`
	ModelContextWindow *int64                   `json:"modelContextWindow,omitempty"`
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
	// Action is the webSearch action descriptor introduced in codex-cli 0.124.0.
	// Shape varies by `action.type`: "search" (queries[], query), "openPage" (url),
	// "findInPage" (pattern, url), "other".
	Action           json.RawMessage   `json:"action,omitempty"`
	Content          []string          `json:"content,omitempty"`
	Summary          []string          `json:"summary,omitempty"`
	ContentItems     json.RawMessage   `json:"contentItems,omitempty"` // dynamicToolCall result content
	Success          *bool             `json:"success,omitempty"`      // dynamicToolCall
	Prompt           string            `json:"prompt,omitempty"`       // collabAgentToolCall
	AgentsStates     json.RawMessage   `json:"agentsStates,omitempty"` // collabAgentToolCall
}

type codexFileChange struct {
	Path    string              `json:"path"`
	KindRaw json.RawMessage     `json:"kind"`
}

// Kind returns the normalized kind string ("add", "update", "delete").
// The wire format may be a plain string or an object {"type": "...", "move_path": ...}.
func (c codexFileChange) Kind() string {
	if len(c.KindRaw) == 0 {
		return ""
	}
	// Try plain string first: "update"
	var s string
	if err := json.Unmarshal(c.KindRaw, &s); err == nil {
		return strings.ToLower(strings.TrimSpace(s))
	}
	// Object form: {"type": "update", "move_path": null}
	var obj struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(c.KindRaw, &obj); err == nil {
		return strings.ToLower(strings.TrimSpace(obj.Type))
	}
	return ""
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
		compatWebTools     = make(map[string]string) // codex/event web search id -> emitted tool id
		compatWebInputs    = make(map[string]string) // codex/event web search id -> deferred tool input
		compatWebQueries   = make(map[string]string) // codex/event web search id -> query text
		compatWebSkip      = make(map[string]bool)   // codex/event web search id -> canonical item/* already emitted
		compatPatchTools   = make(map[string]string) // codex/event patch id -> emitted tool id
		compatPatchNames   = make(map[string]string) // codex/event patch id -> Edit/Write/Delete
		compatPatchPaths   = make(map[string]string) // codex/event patch id -> changed file path
		compatPatchInputs  = make(map[string]string) // codex/event patch id -> buffered input for delayed start
		compatPatchOutputs = make(map[string]string) // codex/event patch id -> buffered delta/message
		compatPatchStarted = make(map[string]bool)   // codex/event patch id -> whether tool_start emitted
		// webSearchDeferStart tracks webSearch items whose item/started arrived with an empty query
		// (codex-cli >= 0.124.0 only fills `query` and `action` at item/completed). For these, we
		// suppress tool_start at started and emit it on completion alongside tool_result.
		webSearchDeferStart = make(map[string]bool)
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
			// Publish cached tokens under BOTH names: `cachedInputTokens` is
			// the historical Codex key, `cacheReadInputTokens` is the unified
			// name read by chat.go's done-event handler (matches Claude's
			// usage naming). Keeping both keeps any downstream reader that
			// still looks up the old name working.
			cached := int(usage.TokenUsage.Last.CachedInputTokens)
			metadata["cachedInputTokens"] = cached
			metadata["cacheReadInputTokens"] = cached
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

		case "codex/event/terminal_interaction", "item/commandExecution/terminalInteraction":
			if payload, ok := parseRawJSONMap(params); ok {
				rawID := firstJSONMapStringDeep(payload, "call_id", "callId", "item_id", "itemId", "id", "tool_call_id", "toolCallId")
				delta := firstJSONMapStringDeep(payload, "delta", "output", "stdout", "stderr", "text", "content", "prompt")
				if strings.TrimSpace(delta) == "" {
					return
				}
				stateMu.Lock()
				if rawID == "" && len(compatExecOutputs) == 1 {
					for id := range compatExecOutputs {
						rawID = id
					}
				}
				if _, ok := compatExecOutputs[rawID]; ok {
					compatExecOutputs[rawID] += delta
				}
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

		case "codex/event/web_search_begin":
			if payload, ok := parseRawJSONMap(params); ok {
				rawID := firstJSONMapStringDeep(payload, "search_id", "searchId", "call_id", "callId", "id", "item_id", "itemId", "tool_call_id", "toolCallId")
				if rawID == "" {
					rawID = fmt.Sprintf("web-search:%d", time.Now().UnixNano())
				}
				toolID := "compat-web-search:" + rawID
				query := codexWebSearchQueryFromPayload(payload)
				input := "{}"
				if query != "" {
					qJSON, _ := json.Marshal(map[string]string{"query": query})
					input = string(qJSON)
				}

				stateMu.Lock()
				compatWebTools[rawID] = toolID
				compatWebInputs[rawID] = input
				compatWebQueries[rawID] = query
				compatWebSkip[rawID] = false
				stateMu.Unlock()
			}

		case "codex/event/web_search_end":
			if payload, ok := parseRawJSONMap(params); ok {
				rawID := firstJSONMapStringDeep(payload, "search_id", "searchId", "call_id", "callId", "id", "item_id", "itemId", "tool_call_id", "toolCallId")
				stateMu.Lock()
				if rawID == "" && len(compatWebTools) == 1 {
					for id := range compatWebTools {
						rawID = id
					}
				}
				toolID := compatWebTools[rawID]
				if toolID == "" {
					toolID = "compat-web-search:" + rawID
				}
				input := compatWebInputs[rawID]
				query := compatWebQueries[rawID]
				skip := compatWebSkip[rawID]
				delete(compatWebTools, rawID)
				delete(compatWebInputs, rawID)
				delete(compatWebQueries, rawID)
				delete(compatWebSkip, rawID)
				stateMu.Unlock()
				if skip {
					return
				}

				if strings.TrimSpace(query) == "" {
					query = codexWebSearchQueryFromPayload(payload)
				}
				if strings.TrimSpace(query) == "" {
					return
				}
				if strings.TrimSpace(input) == "" || input == "{}" {
					if query != "" {
						qJSON, _ := json.Marshal(map[string]string{"query": query})
						input = string(qJSON)
					} else {
						input = "{}"
					}
				}

				output := firstJSONMapStringDeep(payload, "output", "result", "message", "text", "content")
				if input == "{}" && strings.TrimSpace(output) == "" {
					return
				}
				success := firstJSONMapBoolDeep(payload, "success")
				isError := !success && hasJSONMapKeyDeep(payload, "success")
				cb(StreamEvent{
					Type:     EventToolStart,
					ToolID:   toolID,
					ToolName: "WebSearch",
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
				if n.Item.Type == "webSearch" {
					stateMu.Lock()
					matchID := ""
					if _, ok := compatWebSkip[n.Item.ID]; ok {
						matchID = n.Item.ID
					}
					if matchID == "" && strings.TrimSpace(n.Item.Query) != "" {
						for id, q := range compatWebQueries {
							if strings.EqualFold(strings.TrimSpace(q), strings.TrimSpace(n.Item.Query)) {
								matchID = id
								break
							}
						}
					}
					if matchID == "" && len(compatWebSkip) == 1 {
						for id := range compatWebSkip {
							matchID = id
						}
					}
					if matchID == "" {
						for id, skipped := range compatWebSkip {
							if !skipped {
								matchID = id
								break
							}
						}
					}
					if matchID != "" {
						compatWebSkip[matchID] = true
						if strings.TrimSpace(n.Item.Query) == "" {
							n.Item.Query = compatWebQueries[matchID]
						}
					}
					// codex-cli 0.124.0+ sends item/started with an empty query/action;
					// the real query and action arrive at item/completed. Defer
					// tool_start so we don't emit a blank "Searched web" placeholder
					// (and so completion emits both tool_start and tool_result).
					if strings.TrimSpace(codexWebSearchQuery(n.Item)) == "" && n.Item.ID != "" {
						webSearchDeferStart[n.Item.ID] = true
					}
					stateMu.Unlock()
				}
				stateMu.Lock()
				_, skip := requestToolEmitted[n.Item.ID]
				_, deferStart := webSearchDeferStart[n.Item.ID]
				stateMu.Unlock()
				if skip || deferStart {
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
				deferStart := webSearchDeferStart[n.Item.ID]
				if deferStart {
					delete(webSearchDeferStart, n.Item.ID)
				}
				stateMu.Unlock()
				if skip {
					return
				}
				if deferStart {
					emitCodexItemStarted(n.Item, cb)
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

				// Empirical verification (codex-cli 0.124.0):
				//   totalTokens ≈ inputTokens + outputTokens
				// i.e. `cachedInputTokens` is a subset of `inputTokens`
				// (same semantics as OpenAI's usage.prompt_tokens /
				// cached_tokens), and `reasoningOutputTokens` is a subset
				// of `outputTokens`. So "context window occupancy" == the
				// prompt size == `Last.InputTokens`; do NOT add cached.
				meta := map[string]interface{}{
					"model":                 req.Model,
					"inputTokens":           int(n.TokenUsage.Last.InputTokens),
					"cachedInputTokens":     int(n.TokenUsage.Last.CachedInputTokens),
					"outputTokens":          int(n.TokenUsage.Last.OutputTokens),
					"reasoningOutputTokens": int(n.TokenUsage.Last.ReasoningOutputTokens),
					"totalTokens":           int(n.TokenUsage.Last.TotalTokens),
					"totalInputTokens":      int(n.TokenUsage.Total.InputTokens),
					"totalOutputTokens":     int(n.TokenUsage.Total.OutputTokens),
					"usedTokens":            int(n.TokenUsage.Last.InputTokens),
				}
				if n.TokenUsage.ModelContextWindow != nil {
					meta["contextWindow"] = int(*n.TokenUsage.ModelContextWindow)
				}
				cb(StreamEvent{Type: EventContextUsage, Metadata: meta})
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

// ListSupportedModels returns the full codex model catalog discovered via
// `codex app-server`'s `model/list` RPC. Fields (displayName, description,
// supportedReasoningEfforts, defaultReasoningEffort) are passed through
// unchanged from the RPC response.
func (a *CodexAgent) ListSupportedModels(ctx context.Context) ([]ModelInfo, string) {
	a.modelMu.RLock()
	if a.modelsLoaded {
		models := append([]ModelInfo(nil), a.supportedModels...)
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
	if !a.modelsLoaded && source == "dynamic" {
		a.supportedModels = append([]ModelInfo(nil), models...)
		a.supportedModelSrc = source
		a.modelsLoaded = true
	}
	a.modelMu.Unlock()

	return models, source
}

func (a *CodexAgent) detectSupportedModels(ctx context.Context) ([]ModelInfo, string) {
	models, err := a.listModelsViaAppServer(ctx)
	if err == nil && len(models) > 0 {
		if a.Verbose {
			log.Printf("[codex-agent] Loaded %d models via app-server model/list", len(models))
		}
		return models, "dynamic"
	}
	if a.Verbose && err != nil {
		log.Printf("[codex-agent] model/list failed: %v", err)
	}
	return nil, "none"
}

// codexRPCModel mirrors the `Model` shape from the codex app-server protocol
// (see `codex app-server generate-ts` output, `v2/Model.ts`). We keep the JSON
// keys in sync with the wire format so the decoder is tolerant to unknown
// additions.
type codexRPCModel struct {
	ID                    string `json:"id"`
	Model                 string `json:"model"`
	DisplayName           string `json:"displayName"`
	Description           string `json:"description"`
	Hidden                bool   `json:"hidden"`
	IsDefault             bool   `json:"isDefault"`
	DefaultReasoningEffort string `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []struct {
		ReasoningEffort string `json:"reasoningEffort"`
		Description     string `json:"description,omitempty"`
	} `json:"supportedReasoningEfforts"`
	SupportsPersonality bool `json:"supportsPersonality"`
}

func (a *CodexAgent) listModelsViaAppServer(ctx context.Context) ([]ModelInfo, error) {
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
		Data []codexRPCModel `json:"data"`
	}
	if err := client.Request(ctx, "model/list", map[string]interface{}{
		"includeHidden": false,
	}, &resp); err != nil {
		return nil, fmt.Errorf("request model/list: %w", err)
	}

	seen := make(map[string]struct{})
	out := make([]ModelInfo, 0, len(resp.Data))
	for _, item := range resp.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = strings.TrimSpace(item.Model)
		}
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}

		efforts := make([]string, 0, len(item.SupportedReasoningEfforts))
		for _, e := range item.SupportedReasoningEfforts {
			if s := strings.TrimSpace(e.ReasoningEffort); s != "" {
				efforts = append(efforts, s)
			}
		}

		label := item.DisplayName
		if label == "" {
			label = id
		}

		out = append(out, ModelInfo{
			ID:               id,
			Label:            label,
			Description:      item.Description,
			SupportedEfforts: efforts,
			DefaultEffort:    item.DefaultReasoningEffort,
			IsDefault:        item.IsDefault,
			Hidden:           item.Hidden,
		})
	}
	if len(out) == 0 {
		return nil, errors.New("model/list returned no models")
	}
	return out, nil
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
		"sandbox":        "danger-full-access",
	}
	if req.Model != "" {
		params["model"] = req.Model
	}
	if req.WorkDir != "" {
		params["cwd"] = req.WorkDir
	}
	if cfg := a.buildCodexConfigOverrides(req); len(cfg) > 0 {
		params["config"] = cfg
	}
	return params
}

func (a *CodexAgent) buildThreadResumeParams(req Request) map[string]interface{} {
	params := map[string]interface{}{
		"threadId":       req.SessionID,
		"approvalPolicy": "never",
		"sandbox":        "danger-full-access",
	}
	if req.Model != "" {
		params["model"] = req.Model
	}
	if req.WorkDir != "" {
		params["cwd"] = req.WorkDir
	}
	if cfg := a.buildCodexConfigOverrides(req); len(cfg) > 0 {
		params["config"] = cfg
	}
	return params
}

// buildCodexConfigOverrides translates request-level preferences into the
// `config` map that `thread/start` / `thread/resume` accept. Codex's
// ReasoningEffort enum accepts: none, minimal, low, medium, high, xhigh —
// notably NOT "max" (which Claude supports). This method validates the
// requested effort against the live model catalog before forwarding it,
// dropping invalid values rather than letting the CLI reject the whole
// thread/start call.
func (a *CodexAgent) buildCodexConfigOverrides(req Request) map[string]interface{} {
	cfg := map[string]interface{}{}
	if eff := a.validateEffort(req.Model, req.Effort); eff != "" {
		cfg["model_reasoning_effort"] = eff
	}
	return cfg
}

// validateEffort drops the requested effort level if the live catalog says
// the chosen model doesn't accept it. When the cache is empty (no dynamic
// lookup has succeeded yet), we pass the effort through and let the CLI
// decide.
func (a *CodexAgent) validateEffort(model, effort string) string {
	if effort == "" {
		return ""
	}
	a.modelMu.RLock()
	models := a.supportedModels
	loaded := a.modelsLoaded
	a.modelMu.RUnlock()
	if !loaded || len(models) == 0 {
		return effort
	}
	for _, m := range models {
		if m.ID != model {
			continue
		}
		for _, allowed := range m.SupportedEfforts {
			if allowed == effort {
				return effort
			}
		}
		log.Printf("[codex-agent] dropping unsupported effort %q for model %q (allowed: %v)",
			effort, model, m.SupportedEfforts)
		return ""
	}
	return effort
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
	cfg = a.appendExtraMCPConfig(cfg, req.ExtraMCPServers, req.ConversationID)
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

// tomlKey quotes a TOML key if it contains characters that require quoting.
// TOML bare keys may contain ASCII letters, digits, dashes, and underscores
// ([A-Za-z0-9_-]). Keys with other characters must be quoted.
func tomlKey(k string) string {
	for _, c := range k {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return fmt.Sprintf("%q", k)
		}
	}
	return k
}

func (a *CodexAgent) appendExtraMCPConfig(cfg []string, extras []MCPServerEntry, conversationID string) []string {
	supportsURL := a.supportsURLMCP()
	for _, mcp := range extras {
		key := tomlKey(mcp.Key)
		switch {
		case mcp.URL != "":
			if !supportsURL {
				log.Printf("[codex-agent] skip HTTP MCP %s: codex version does not support URL MCPs", mcp.Key)
				continue
			}
			mcpURL := mcp.URL
			if mcp.AppendSessionURL && conversationID != "" {
				mcpURL += "?session=" + conversationID
			}
			cfg = append(cfg, fmt.Sprintf("mcp_servers.%s.url=%q", key, mcpURL))
			for hk, hv := range mcp.Headers {
				cfg = append(cfg, fmt.Sprintf("mcp_servers.%s.http_headers.%s=%q", key, hk, hv))
			}
		case mcp.Cmd != "":
			cfg = append(cfg, fmt.Sprintf("mcp_servers.%s.command=%q", key, mcp.Cmd))
			for i, arg := range mcp.Args {
				cfg = append(cfg, fmt.Sprintf("mcp_servers.%s.args.%d=%q", key, i, arg))
			}
			for ek, ev := range mcp.Env {
				cfg = append(cfg, fmt.Sprintf("mcp_servers.%s.env.%s=%q", key, ek, ev))
			}
		}
		log.Printf("[codex-agent] extra MCP %s configured (key=%s)", mcp.Key, key)
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
