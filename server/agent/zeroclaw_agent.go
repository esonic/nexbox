package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const zeroClawACPProtocolVersion = 1

// ZeroClawAgent implements Agent using ZeroClaw's ACP v1 stdio server.
// Provider/model selection, tools, skills and sub-agent policy remain owned by
// ZeroClaw's configuration. Nexbox only owns the ACP client/session surface.
type ZeroClawAgent struct {
	CLIPath    string
	CLIArgs    []string
	AgentAlias string
	Verbose    bool
}

func NewZeroClawAgent() *ZeroClawAgent {
	cliPath := strings.TrimSpace(os.Getenv("NEXBOX_ZEROCLAW_CLI"))
	if cliPath == "" {
		cliPath = "zeroclaw"
	}
	return &ZeroClawAgent{
		CLIPath:    cliPath,
		CLIArgs:    []string{"acp"},
		AgentAlias: strings.TrimSpace(os.Getenv("NEXBOX_ZEROCLAW_AGENT")),
		Verbose:    true,
	}
}

var _ Agent = (*ZeroClawAgent)(nil)

// ZeroClaw ACP has no stable message-level fork or active-turn interjection.
// Sub-agents are intentionally represented as ordinary delegate/spawn tool
// calls, not as Nexbox structured sub-task events.
func (a *ZeroClawAgent) Capabilities() Capabilities {
	return Capabilities{}
}

type zeroClawInitializeResult struct {
	ProtocolVersion int `json:"protocolVersion"`
	AgentInfo       struct {
		Name    string `json:"name"`
		Title   string `json:"title"`
		Version string `json:"version"`
	} `json:"agentInfo"`
	Meta struct {
		ZeroClaw struct {
			DefaultModel string `json:"defaultModel"`
		} `json:"zeroclaw"`
	} `json:"_meta"`
}

type zeroClawSessionNewResult struct {
	SessionID    string `json:"sessionId"`
	WorkspaceDir string `json:"workspaceDir"`
}

type zeroClawPromptResult struct {
	SessionID  string `json:"sessionId"`
	StopReason string `json:"stopReason"`
	Content    string `json:"content"`
}

type zeroClawSessionUpdateParams struct {
	SessionID string `json:"sessionId"`
	Update    struct {
		SessionUpdate string          `json:"sessionUpdate"`
		ToolCallID    string          `json:"toolCallId,omitempty"`
		Title         string          `json:"title,omitempty"`
		Name          string          `json:"name,omitempty"`
		Status        string          `json:"status,omitempty"`
		RawInput      json.RawMessage `json:"rawInput,omitempty"`
		RawOutput     json.RawMessage `json:"rawOutput,omitempty"`
		Content       json.RawMessage `json:"content,omitempty"`
	} `json:"update"`
}

type zeroClawPermissionParams struct {
	SessionID string `json:"sessionId"`
	Options   []struct {
		OptionID string `json:"optionId"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
	} `json:"options"`
	ToolCall struct {
		ToolCallID string          `json:"toolCallId"`
		Title      string          `json:"title"`
		RawInput   json.RawMessage `json:"rawInput"`
	} `json:"toolCall"`
}

func (a *ZeroClawAgent) SendMessage(ctx context.Context, req Request, cb EventCallback) (Result, error) {
	if req.Fork != nil {
		return Result{}, fmt.Errorf("zeroclaw ACP does not support fork")
	}
	if err := validateZeroClawWorkDir(req.WorkDir); err != nil {
		return Result{}, err
	}

	client := newACPStdioClient(a.CLIPath, a.CLIArgs)
	client.verbose = a.Verbose
	client.setNotificationHandler(func(method string, params json.RawMessage) {
		if method == "session/update" {
			a.handleSessionUpdate(params, cb)
		}
	})
	client.setServerRequestHandler(func(method string, params json.RawMessage) (interface{}, *acpWireError) {
		if method != "session/request_permission" {
			return nil, &acpWireError{Code: -32601, Message: "method not supported by Nexbox ACP client"}
		}
		return a.handlePermissionRequest(ctx, req.UserInputHandler, params)
	})

	if err := client.start(req.WorkDir); err != nil {
		return Result{}, fmt.Errorf("start zeroclaw ACP: %w", err)
	}
	defer func() {
		if err := client.close(); err != nil && a.Verbose {
			log.Printf("[zeroclaw-agent] ACP process close: %v", err)
		}
	}()

	var initialize zeroClawInitializeResult
	if err := client.request(ctx, "initialize", nil, &initialize); err != nil {
		return Result{}, withZeroClawStderr(err, client.stderr())
	}
	if initialize.ProtocolVersion != zeroClawACPProtocolVersion {
		return Result{}, fmt.Errorf("unsupported zeroclaw ACP protocol version %d (want %d)", initialize.ProtocolVersion, zeroClawACPProtocolVersion)
	}

	cb(StreamEvent{
		Type: EventSessionInfo,
		Metadata: map[string]interface{}{
			"model":   initialize.Meta.ZeroClaw.DefaultModel,
			"cwd":     req.WorkDir,
			"version": initialize.AgentInfo.Version,
		},
	})

	sessionID := req.SessionID
	if sessionID == "" {
		params := make(map[string]interface{})
		if req.WorkDir != "" {
			params["cwd"] = req.WorkDir
		}
		if a.AgentAlias != "" {
			params["agentAlias"] = a.AgentAlias
		}
		var created zeroClawSessionNewResult
		if err := client.request(ctx, "session/new", params, &created); err != nil {
			return Result{}, withZeroClawStderr(err, client.stderr())
		}
		if strings.TrimSpace(created.SessionID) == "" {
			return Result{}, fmt.Errorf("zeroclaw ACP session/new returned an empty sessionId")
		}
		sessionID = created.SessionID
	} else {
		err := client.request(ctx, "session/resume", map[string]interface{}{"sessionId": sessionID}, nil)
		if err != nil {
			var rpcErr *acpRPCError
			if errors.As(err, &rpcErr) && rpcErr.Code == -32000 {
				return Result{}, fmt.Errorf("%w: %s", ErrSessionExpired, rpcErr.Message)
			}
			return Result{}, withZeroClawStderr(err, client.stderr())
		}
	}

	prompt, err := buildZeroClawPrompt(req, req.SessionID == "")
	if err != nil {
		return Result{SessionID: sessionID}, err
	}
	promptParams := map[string]interface{}{
		"sessionId": sessionID,
		"prompt":    prompt,
	}

	type promptOutcome struct {
		result zeroClawPromptResult
		err    error
	}
	promptDone := make(chan promptOutcome, 1)
	promptCtx, cancelPrompt := context.WithCancel(context.Background())
	defer cancelPrompt()
	go func() {
		var response zeroClawPromptResult
		err := client.request(promptCtx, "session/prompt", promptParams, &response)
		promptDone <- promptOutcome{result: response, err: err}
	}()

	var outcome promptOutcome
	select {
	case outcome = <-promptDone:
	case <-ctx.Done():
		_ = client.notify("session/cancel", map[string]interface{}{"sessionId": sessionID})
		select {
		case <-promptDone:
		case <-time.After(5 * time.Second):
			cancelPrompt()
		}
		return Result{SessionID: sessionID}, ctx.Err()
	}

	if outcome.err != nil {
		return Result{SessionID: sessionID}, withZeroClawStderr(outcome.err, client.stderr())
	}
	if outcome.result.SessionID != "" {
		sessionID = outcome.result.SessionID
	}
	metadata := map[string]interface{}{"stopReason": outcome.result.StopReason}
	cb(StreamEvent{Type: EventDone, SessionID: sessionID, Metadata: metadata})
	return Result{SessionID: sessionID, Metadata: metadata}, nil
}

func validateZeroClawWorkDir(workDir string) error {
	if workDir == "" {
		return nil
	}
	info, err := os.Stat(workDir)
	if err != nil {
		return fmt.Errorf("cannot access work directory %s: %w", workDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("work directory path is not a directory: %s", workDir)
	}
	return nil
}

func buildZeroClawPrompt(req Request, isNewSession bool) (interface{}, error) {
	text := BuildPrompt(req.StaticContext, req.DynamicContext, req.Message, isNewSession, nil)
	if len(req.Images) == 0 {
		return text, nil
	}

	parts := make([]interface{}, 0, len(req.Images)+1)
	parts = append(parts, map[string]interface{}{"text": text})
	for _, imagePath := range req.Images {
		data, err := os.ReadFile(imagePath)
		if err != nil {
			return nil, fmt.Errorf("read attached image %s: %w", imagePath, err)
		}
		if len(data) > 10*1024*1024 {
			return nil, fmt.Errorf("attached image exceeds ZeroClaw ACP 10 MB limit: %s", imagePath)
		}
		mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(imagePath)))
		if mimeType == "" {
			mimeType = http.DetectContentType(data)
		}
		absolutePath, err := filepath.Abs(imagePath)
		if err != nil {
			return nil, fmt.Errorf("resolve attached image path %s: %w", imagePath, err)
		}
		parts = append(parts, map[string]interface{}{
			"type": "resource",
			"resource": map[string]interface{}{
				"uri":      (&url.URL{Scheme: "file", Path: absolutePath}).String(),
				"mimeType": mimeType,
				"blob":     base64.StdEncoding.EncodeToString(data),
			},
		})
	}
	return parts, nil
}

func (a *ZeroClawAgent) handleSessionUpdate(params json.RawMessage, cb EventCallback) {
	var update zeroClawSessionUpdateParams
	if err := json.Unmarshal(params, &update); err != nil {
		if a.Verbose {
			log.Printf("[zeroclaw-agent] invalid session/update: %v", err)
		}
		return
	}

	switch update.Update.SessionUpdate {
	case "agent_message_chunk":
		if text := acpContentText(update.Update.Content); text != "" {
			cb(StreamEvent{Type: EventTextDelta, Content: text})
		}
	case "agent_thought_chunk":
		if text := acpContentText(update.Update.Content); text != "" {
			cb(StreamEvent{Type: EventThinking, Content: text, ToolName: "Thinking"})
		}
	case "tool_call":
		cb(StreamEvent{
			Type:     EventToolStart,
			ToolID:   update.Update.ToolCallID,
			ToolName: firstNonEmpty(update.Update.Title, update.Update.Name, "tool"),
			Input:    renderACPRawValue(update.Update.RawInput),
		})
	case "tool_call_update":
		status := strings.ToLower(update.Update.Status)
		cb(StreamEvent{
			Type:     EventToolResult,
			ToolID:   update.Update.ToolCallID,
			ToolName: firstNonEmpty(update.Update.Name, update.Update.Title),
			Output:   renderACPRawValue(update.Update.RawOutput),
			IsError:  status == "failed" || status == "error" || status == "rejected" || status == "cancelled",
		})
	}
}

func (a *ZeroClawAgent) handlePermissionRequest(
	ctx context.Context,
	handler UserInputHandler,
	params json.RawMessage,
) (interface{}, *acpWireError) {
	var permission zeroClawPermissionParams
	if err := json.Unmarshal(params, &permission); err != nil {
		return nil, &acpWireError{Code: -32602, Message: "invalid session/request_permission params"}
	}
	if handler == nil || len(permission.Options) == 0 {
		return cancelledZeroClawPermission(), nil
	}

	title := strings.TrimSpace(permission.ToolCall.Title)
	if title == "" {
		title = "ZeroClaw permission request"
	}
	options := make([]map[string]interface{}, 0, len(permission.Options))
	for _, option := range permission.Options {
		options = append(options, map[string]interface{}{
			"label":       firstNonEmpty(option.Name, option.OptionID),
			"description": option.Kind,
		})
	}
	input := map[string]interface{}{
		"question": title,
		"questions": []map[string]interface{}{
			{
				"question":    title,
				"header":      "Permission",
				"multiSelect": false,
				"options":     options,
			},
		},
		"toolInput": jsonRawValue(permission.ToolCall.RawInput),
	}
	inputJSON, _ := json.Marshal(input)

	response, err := handler(permission.ToolCall.ToolCallID, "ZeroClawPermission", string(inputJSON))
	if err != nil || ctx.Err() != nil {
		return cancelledZeroClawPermission(), nil
	}
	selected := zeroClawSelectedPermission(response, title, permission)
	if selected == "" {
		return cancelledZeroClawPermission(), nil
	}
	return map[string]interface{}{
		"outcome": map[string]interface{}{
			"outcome":  "selected",
			"optionId": selected,
		},
	}, nil
}

func zeroClawSelectedPermission(response, title string, permission zeroClawPermissionParams) string {
	answer := strings.TrimSpace(response)
	var structured struct {
		Answers map[string]string `json:"answers"`
	}
	if json.Unmarshal([]byte(response), &structured) == nil {
		if value := strings.TrimSpace(structured.Answers[title]); value != "" {
			answer = value
		} else {
			for _, value := range structured.Answers {
				if strings.TrimSpace(value) != "" {
					answer = strings.TrimSpace(value)
					break
				}
			}
		}
	}
	for _, option := range permission.Options {
		if strings.EqualFold(answer, option.OptionID) || strings.EqualFold(answer, option.Name) {
			return option.OptionID
		}
	}
	return ""
}

func cancelledZeroClawPermission() map[string]interface{} {
	return map[string]interface{}{
		"outcome": map[string]interface{}{"outcome": "cancelled"},
	}
}

func renderACPRawValue(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var compact interface{}
	if json.Unmarshal(raw, &compact) == nil {
		if data, err := json.Marshal(compact); err == nil {
			return string(data)
		}
	}
	return string(raw)
}

func acpContentText(raw json.RawMessage) string {
	var content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &content) != nil {
		return ""
	}
	return content.Text
}

func jsonRawValue(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func withZeroClawStderr(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return err
	}
	const maxStderr = 2000
	if len(stderr) > maxStderr {
		stderr = stderr[len(stderr)-maxStderr:]
	}
	return fmt.Errorf("%w: %s", err, stderr)
}
