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
	"unicode/utf8"
)

// CodexAgent implements the Agent interface using `codex app-server`.
type CodexAgent struct {
	CLIPath      string
	MCPServerURL string
	Verbose      bool

	urlMCPChecked bool
	urlMCPSupport bool
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
		result            Result
		stateMu           sync.Mutex
		currentTurnID     string
		doneEmitted       bool
		planToolStarted   bool
		streamedAgentText = make(map[string]bool)
		streamedReasoning = make(map[string]bool)
		latestTokenUsage  *codexThreadTokenUsageUpdatedNotification
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
			// Client-side dynamic tools are not wired in NexBox today.
			return map[string]interface{}{
				"success": false,
				"contentItems": []map[string]interface{}{
					{
						"type": "inputText",
						"text": "Dynamic client tools are not supported in NexBox Codex bridge.",
					},
				},
			}, nil
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

		case "item/started":
			var n codexItemNotification
			if err := json.Unmarshal(params, &n); err == nil {
				emitCodexItemStarted(n.Item, cb)
			}

		case "item/completed":
			var n codexItemNotification
			if err := json.Unmarshal(params, &n); err == nil {
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
			if a.Verbose {
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
	var turnResp codexTurnStartResponse
	if err := client.Request(ctx, "turn/start", map[string]interface{}{
		"threadId": result.SessionID,
		"input": []map[string]interface{}{
			{
				"type": "text",
				"text": prompt,
			},
		},
	}, &turnResp); err != nil {
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

func emitCodexItemStarted(item codexThreadItem, cb EventCallback) {
	switch item.Type {
	case "commandExecution":
		cmdJSON, _ := json.Marshal(map[string]string{"command": item.Command})
		cb(StreamEvent{
			Type:     EventToolStart,
			ToolID:   item.ID,
			ToolName: "Bash",
			Input:    string(cmdJSON),
		})

	case "mcpToolCall":
		cb(StreamEvent{
			Type:     EventToolStart,
			ToolID:   item.ID,
			ToolName: codexToolDisplayName(item),
			Input:    string(item.Arguments),
		})

	case "dynamicToolCall":
		cb(StreamEvent{
			Type:     EventToolStart,
			ToolID:   item.ID,
			ToolName: item.Tool,
			Input:    string(item.Arguments),
		})

	case "collabAgentToolCall":
		inputJSON, _ := json.Marshal(map[string]interface{}{
			"tool":   item.Tool,
			"prompt": item.Prompt,
		})
		cb(StreamEvent{
			Type:     EventToolStart,
			ToolID:   item.ID,
			ToolName: "collab/" + item.Tool,
			Input:    string(inputJSON),
		})

	case "webSearch":
		qJSON, _ := json.Marshal(map[string]string{"query": item.Query})
		cb(StreamEvent{
			Type:     EventToolStart,
			ToolID:   item.ID,
			ToolName: "WebSearch",
			Input:    string(qJSON),
		})

	case "fileChange":
		changesJSON, _ := json.Marshal(map[string]interface{}{"changes": item.Changes})
		cb(StreamEvent{
			Type:     EventToolStart,
			ToolID:   item.ID,
			ToolName: "FileChange",
			Input:    string(changesJSON),
		})
	}
}

func emitCodexItemCompleted(item codexThreadItem, agentStreamed, reasoningStreamed bool, cb EventCallback) {
	switch item.Type {
	case "commandExecution":
		output := ""
		if item.AggregatedOutput != nil {
			output = *item.AggregatedOutput
		}
		isError := item.Status == "failed" || item.Status == "declined"
		if item.ExitCode != nil && *item.ExitCode != 0 {
			isError = true
		}
		cb(StreamEvent{
			Type:    EventToolResult,
			ToolID:  item.ID,
			Output:  output,
			IsError: isError,
		})

	case "mcpToolCall":
		isError := item.Status == "failed" || (len(item.Error) > 0 && string(item.Error) != "null")
		cb(StreamEvent{
			Type:    EventToolResult,
			ToolID:  item.ID,
			Output:  codexMCPResultText(item.Result),
			IsError: isError,
		})

	case "dynamicToolCall":
		isError := item.Status == "failed" || (item.Success != nil && !*item.Success)
		cb(StreamEvent{
			Type:    EventToolResult,
			ToolID:  item.ID,
			Output:  codexDynamicToolOutputText(item.ContentItems),
			IsError: isError,
		})

	case "collabAgentToolCall":
		isError := item.Status == "failed"
		output := string(item.AgentsStates)
		cb(StreamEvent{
			Type:    EventToolResult,
			ToolID:  item.ID,
			Output:  output,
			IsError: isError,
		})

	case "webSearch":
		cb(StreamEvent{
			Type:   EventToolResult,
			ToolID: item.ID,
		})

	case "fileChange":
		isError := item.Status == "failed"
		cb(StreamEvent{
			Type:    EventToolResult,
			ToolID:  item.ID,
			IsError: isError,
		})

	case "agentMessage":
		if !agentStreamed && item.Text != "" {
			cb(StreamEvent{Type: EventTextDelta, Content: item.Text})
		}

	case "reasoning":
		if reasoningStreamed {
			return
		}
		var parts []string
		if len(item.Content) > 0 {
			parts = append(parts, strings.Join(item.Content, "\n"))
		}
		if len(item.Summary) > 0 {
			parts = append(parts, strings.Join(item.Summary, "\n"))
		}
		if len(parts) == 0 && item.Text != "" {
			parts = append(parts, item.Text)
		}
		if len(parts) > 0 {
			cb(StreamEvent{
				Type:    EventThinking,
				ToolID:  item.ID,
				Content: strings.Join(parts, "\n"),
			})
		}

	case "plan":
		if item.Text != "" {
			cb(StreamEvent{
				Type:    EventSystem,
				Content: "Plan: " + item.Text,
			})
		}

	case "contextCompaction":
		cb(StreamEvent{
			Type:    EventSystem,
			Content: "Context compacted to fit within limits",
		})
	}
}

func codexToolDisplayName(item codexThreadItem) string {
	if item.Tool == "" {
		return "Tool"
	}
	if item.Server != "" {
		return item.Server + "/" + item.Tool
	}
	return item.Tool
}

// codexMCPResultText extracts text blocks from an MCP tool result.
func codexMCPResultText(resultRaw json.RawMessage) string {
	if len(resultRaw) == 0 || string(resultRaw) == "null" {
		return ""
	}
	var result struct {
		Content []map[string]interface{} `json:"content"`
	}
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		return string(resultRaw)
	}
	var parts []string
	for _, c := range result.Content {
		t, _ := c["type"].(string)
		if t != "text" {
			continue
		}
		txt, _ := c["text"].(string)
		if txt != "" {
			parts = append(parts, txt)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return string(resultRaw)
}

func codexDynamicToolOutputText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return string(raw)
	}
	var parts []string
	for _, it := range items {
		if strings.EqualFold(it.Type, "inputText") || strings.EqualFold(it.Type, "text") {
			if it.Text != "" {
				parts = append(parts, it.Text)
			}
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

func formatCodexPlanUpdate(n codexTurnPlanUpdatedNotification) string {
	var lines []string
	if n.Explanation != "" {
		lines = append(lines, n.Explanation)
	}
	for _, step := range n.Plan {
		lines = append(lines, fmt.Sprintf("- [%s] %s", step.Status, step.Step))
	}
	return strings.Join(lines, "\n")
}

func isAppToolApprovalRequest(questions []struct {
	ID       string `json:"id"`
	Header   string `json:"header"`
	Question string `json:"question"`
	Options  []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"options"`
}) bool {
	if len(questions) == 0 {
		return false
	}
	for _, q := range questions {
		header := strings.ToLower(strings.TrimSpace(q.Header))
		prompt := strings.ToLower(strings.TrimSpace(q.Question))
		if strings.Contains(header, "approve app tool call") ||
			(strings.Contains(prompt, "allow this action") && strings.Contains(prompt, "tool")) {
			return true
		}
	}
	return false
}

func pickApprovalAnswerLabel(options []struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}) string {
	if len(options) == 0 {
		return ""
	}
	preferred := []string{
		"approve once",
		"approve this session",
		"allow",
		"yes",
	}
	for _, p := range preferred {
		for _, opt := range options {
			label := strings.TrimSpace(opt.Label)
			if label == "" {
				continue
			}
			lower := strings.ToLower(label)
			if lower == p || strings.Contains(lower, p) {
				return label
			}
		}
	}
	// Fallback to first non-empty option.
	for _, opt := range options {
		if label := strings.TrimSpace(opt.Label); label != "" {
			return label
		}
	}
	return ""
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
