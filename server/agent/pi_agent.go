package agent

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

//go:embed pi_nexbox_extension.ts
var piNexboxExtension []byte

// PiAgent adapts the Pi coding-agent CLI's RPC mode. A fresh pi process is
// started for each Nexbox turn; Pi owns its session files and resumes them by
// the opaque session ID returned from get_state.
type PiAgent struct {
	CLIPath      string
	AgentDir     string // Empty means Pi's normal user-level agent directory.
	MCPServerURL string
	Verbose      bool
}

type piMCPServerSpec struct {
	Key     string            `json:"key"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

func NewPiAgent() *PiAgent {
	path := strings.TrimSpace(os.Getenv("NEXBOX_PI_CLI"))
	if path == "" {
		path = "pi"
	}
	return &PiAgent{CLIPath: path, AgentDir: strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")), Verbose: true}
}

var _ Agent = (*PiAgent)(nil)

func (a *PiAgent) Capabilities() Capabilities {
	return Capabilities{Fork: true, Interject: true, ModelSwitchMode: ModelSwitchCurrentSession}
}

var _ ModelCatalog = (*PiAgent)(nil)

// piSettings contains only the model-selection settings that Pi itself owns.
// enabledModels is deliberately opt-in: without it Nexbox does not surface
// Pi's entire provider catalog as a model picker.
type piSettings struct {
	DefaultProvider string   `json:"defaultProvider"`
	DefaultModel    string   `json:"defaultModel"`
	EnabledModels   []string `json:"enabledModels"`
}

// ListSupportedModels exposes only models explicitly scoped by the user's Pi
// settings.json. Each pattern is resolved by Pi's own --list-models command,
// so Nexbox does not need provider-specific catalogs or pattern semantics.
func (a *PiAgent) ListSupportedModels(ctx context.Context) ([]ModelInfo, string) {
	settings, err := a.readSettings()
	if err != nil || len(settings.EnabledModels) == 0 {
		return nil, "none"
	}

	models := make(map[string]ModelInfo)
	for _, pattern := range settings.EnabledModels {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		modelPattern, thinking := piSplitThinkingSuffix(pattern)
		search := modelPattern
		if strings.ContainsAny(modelPattern, "*?") {
			// --list-models only understands fuzzy search. A glob deliberately
			// requests a broad scope, so fetch the catalog then resolve it here.
			search = ""
		}
		cmd := exec.CommandContext(ctx, a.CLIPath, "--list-models", search)
		output, err := cmd.Output()
		if err != nil {
			if a.Verbose {
				fmt.Fprintf(os.Stderr, "[pi-agent] list models for %q: %v\n", pattern, err)
			}
			continue
		}
		resolved := parsePiModelList(output)
		if !strings.Contains(modelPattern, "/") && !strings.ContainsAny(modelPattern, "*?") {
			if best, ok := piBestFuzzyModel(modelPattern, resolved); ok {
				resolved = []ModelInfo{best}
			}
		}
		for _, model := range resolved {
			// Pi's list command accepts fuzzy patterns and can return related
			// models for a literal query. Filter only a literal provider/model;
			// glob and fuzzy patterns must retain Pi's own matching semantics.
			if piModelMatchesPattern(model.ID, modelPattern) {
				if thinking != "" {
					model.ID += ":" + thinking
					model.Label += " (" + thinking + ")"
				}
				models[model.ID] = model
			}
		}
	}

	result := make([]ModelInfo, 0, len(models))
	for _, model := range models {
		result = append(result, model)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return piIsDefaultModel(result[i].ID, settings) && !piIsDefaultModel(result[j].ID, settings)
	})
	return result, "configured"
}

func piModelMatchesPattern(id, pattern string) bool {
	if strings.ContainsAny(pattern, "*?") {
		pattern = strings.ToLower(pattern)
		id = strings.ToLower(id)
		return piGlobMatch(pattern, id) || piGlobMatch(pattern, strings.TrimPrefix(id, strings.SplitN(id, "/", 2)[0]+"/"))
	}
	if !strings.Contains(pattern, "/") || strings.Contains(pattern, "~") {
		return true
	}
	return strings.EqualFold(id, pattern)
}

func piGlobMatch(pattern, value string) bool {
	expr := regexp.QuoteMeta(pattern)
	expr = strings.ReplaceAll(expr, `\*`, `.*`)
	expr = strings.ReplaceAll(expr, `\?`, `.`)
	matched, err := regexp.MatchString("^"+expr+"$", value)
	return err == nil && matched
}

func piSplitThinkingSuffix(pattern string) (string, string) {
	last := strings.LastIndex(pattern, ":")
	if last < 0 {
		return pattern, ""
	}
	suffix := pattern[last+1:]
	for _, level := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		if suffix == level {
			return pattern[:last], suffix
		}
	}
	return pattern, ""
}

func (a *PiAgent) readSettings() (piSettings, error) {
	dir := a.AgentDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return piSettings{}, err
		}
		dir = filepath.Join(home, ".pi", "agent")
	}
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		return piSettings{}, err
	}
	var settings piSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return piSettings{}, err
	}
	return settings, nil
}

func parsePiModelList(output []byte) []ModelInfo {
	lines := strings.Split(string(output), "\n")
	models := make([]ModelInfo, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "provider" {
			continue
		}
		provider, modelID := fields[0], fields[1]
		models = append(models, ModelInfo{ID: provider + "/" + modelID, Label: modelID})
	}
	return models
}

func piIsDefaultModel(id string, settings piSettings) bool {
	if settings.DefaultModel == "" {
		return false
	}
	want := settings.DefaultModel
	if settings.DefaultProvider != "" && !strings.HasPrefix(want, settings.DefaultProvider+"/") {
		want = settings.DefaultProvider + "/" + want
	}
	id, _ = piSplitThinkingSuffix(id)
	return strings.EqualFold(id, want)
}

func piBestFuzzyModel(pattern string, models []ModelInfo) (ModelInfo, bool) {
	if len(models) == 0 {
		return ModelInfo{}, false
	}
	for _, model := range models {
		if strings.EqualFold(piBareModelID(model.ID), pattern) {
			return model, true
		}
	}
	sort.SliceStable(models, func(i, j int) bool {
		alias := func(id string) bool { return !regexp.MustCompile(`-\d{8}$`).MatchString(piBareModelID(id)) }
		if alias(models[i].ID) != alias(models[j].ID) {
			return alias(models[i].ID)
		}
		return piBareModelID(models[i].ID) > piBareModelID(models[j].ID)
	})
	return models[0], true
}

func piBareModelID(id string) string {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return id
}

func (a *PiAgent) SendMessage(ctx context.Context, req Request, cb EventCallback) (Result, error) {
	// Explicitly load only Nexbox-managed project skills. --no-approve keeps
	// Pi from trusting arbitrary project .pi resources or extensions.
	args := []string{"--mode", "rpc", "--no-approve"}
	if skillDir := filepath.Join(req.WorkDir, ".agents", "skills"); isDir(skillDir) {
		args = append(args, "--skill", skillDir)
	}
	servers := a.mcpServers(req)
	var extensionPath string
	if len(servers) > 0 {
		extensionDir := filepath.Join(req.WorkDir, ".nexbox", "pi-extensions")
		if err := os.MkdirAll(extensionDir, 0700); err != nil {
			return Result{}, fmt.Errorf("create Pi extension directory: %w", err)
		}
		file, err := os.CreateTemp(extensionDir, "nexbox-mcp-*.ts")
		if err != nil {
			return Result{}, fmt.Errorf("create Pi Nexbox extension: %w", err)
		}
		extensionPath = file.Name()
		if _, err := file.Write(piNexboxExtension); err != nil {
			file.Close()
			os.Remove(extensionPath)
			return Result{}, fmt.Errorf("write Pi Nexbox extension: %w", err)
		}
		file.Close()
		defer os.Remove(extensionPath)
		args = append(args, "--extension", extensionPath)
	}
	// Fork starts by opening the parent session. The RPC fork command then
	// switches Pi to the child session before we send the first child prompt.
	sessionID := req.SessionID
	if req.Fork != nil {
		if req.Fork.SessionID == "" || req.Fork.Checkpoint == "" {
			return Result{}, fmt.Errorf("pi fork requires session id and checkpoint")
		}
		sessionID = req.Fork.SessionID
	}
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}
	model := strings.TrimSpace(req.Model)
	if model != "" {
		args = append(args, "--model", model)
	}
	if req.Effort != "" {
		args = append(args, "--thinking", req.Effort)
	}

	client := newPiRPCClient(a.CLIPath, args, a.Verbose)
	if extensionPath != "" {
		encoded, err := json.Marshal(servers)
		if err != nil {
			return Result{}, fmt.Errorf("encode Pi MCP configuration: %w", err)
		}
		configFile, err := os.CreateTemp(filepath.Dir(extensionPath), "nexbox-mcp-config-*.json")
		if err != nil {
			return Result{}, fmt.Errorf("create Pi MCP configuration: %w", err)
		}
		configPath := configFile.Name()
		if err := configFile.Chmod(0600); err != nil {
			configFile.Close()
			os.Remove(configPath)
			return Result{}, fmt.Errorf("protect Pi MCP configuration: %w", err)
		}
		if _, err := configFile.Write(encoded); err != nil {
			configFile.Close()
			os.Remove(configPath)
			return Result{}, fmt.Errorf("write Pi MCP configuration: %w", err)
		}
		if err := configFile.Close(); err != nil {
			os.Remove(configPath)
			return Result{}, fmt.Errorf("close Pi MCP configuration: %w", err)
		}
		defer os.Remove(configPath)
		client.env = append(os.Environ(), "NEXBOX_PI_MCP_CONFIG="+configPath)
	}
	if err := client.start(req.WorkDir); err != nil {
		return Result{}, err
	}
	defer client.close()

	var result Result
	var resultMu sync.Mutex
	settled := make(chan struct{})
	var settledOnce sync.Once
	var terminalErr error
	client.onEvent = func(event piRPCRecord) {
		a.handleEvent(event, cb, &result, &resultMu, &terminalErr, func() { settledOnce.Do(func() { close(settled) }) })
	}

	if req.Fork != nil {
		var forkResult struct {
			Cancelled bool `json:"cancelled"`
		}
		if err := client.command(ctx, "fork", map[string]interface{}{"entryId": req.Fork.Checkpoint}, &forkResult); err != nil {
			return Result{}, fmt.Errorf("fork pi session: %w", err)
		}
		if forkResult.Cancelled {
			return Result{}, fmt.Errorf("fork pi session: cancelled by a Pi extension")
		}
	}
	var state piState
	if err := client.command(ctx, "get_state", nil, &state); err != nil {
		return Result{}, fmt.Errorf("get pi session state: %w", err)
	}
	resultMu.Lock()
	result.SessionID = state.SessionID
	resultMu.Unlock()

	prompt := BuildPrompt(req.StaticContext, req.DynamicContext, req.Message, req.SessionID == "" && req.Fork == nil, req.Images)
	if err := client.command(ctx, "prompt", map[string]interface{}{"message": prompt}, nil); err != nil {
		return result, fmt.Errorf("send pi prompt: %w", err)
	}
	if req.RegisterInterject != nil {
		req.RegisterInterject(func(content string) error {
			if strings.TrimSpace(content) == "" {
				return fmt.Errorf("pi steer input must not be empty")
			}
			steerCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			return client.command(steerCtx, "steer", map[string]interface{}{"message": BuildInterjectPrompt(content, nil)}, nil)
		})
	}

	select {
	case <-settled:
	case <-ctx.Done():
		abortCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = client.command(abortCtx, "abort", nil, nil)
		cancel()
		return result, ctx.Err()
	case <-client.readDone:
		return result, client.connectionError()
	}
	resultMu.Lock()
	err := terminalErr
	resultMu.Unlock()
	if err != nil {
		return result, err
	}

	// Pi's fork cursor is a user entry. Query it after the turn settles and
	// retain the latest one on the assistant message as Nexbox's checkpoint.
	var forkMessages struct {
		Messages []struct {
			EntryID string `json:"entryId"`
		} `json:"messages"`
	}
	if err := client.command(ctx, "get_fork_messages", nil, &forkMessages); err == nil && len(forkMessages.Messages) > 0 {
		resultMu.Lock()
		result.Checkpoint = forkMessages.Messages[len(forkMessages.Messages)-1].EntryID
		resultMu.Unlock()
	}
	resultMu.Lock()
	final := result
	resultMu.Unlock()
	cb(StreamEvent{Type: EventDone, SessionID: final.SessionID, Metadata: final.Metadata})
	return final, nil
}

func (a *PiAgent) mcpServers(req Request) []piMCPServerSpec {
	servers := make([]piMCPServerSpec, 0, 1+len(req.ExtraMCPServers))
	if a.MCPServerURL != "" {
		servers = append(servers, piMCPServerSpec{Key: "nexbox", URL: withPiMCPSession(a.MCPServerURL, req.ConversationID)})
	}
	for _, mcp := range req.ExtraMCPServers {
		if mcp.Type != "http" || mcp.URL == "" {
			continue
		}
		serverURL := mcp.URL
		if mcp.AppendSessionURL {
			serverURL = withPiMCPSession(serverURL, req.ConversationID)
		}
		servers = append(servers, piMCPServerSpec{Key: mcp.Key, URL: serverURL, Headers: mcp.Headers})
	}
	return servers
}

func isDir(path string) bool { info, err := os.Stat(path); return err == nil && info.IsDir() }

func withPiMCPSession(raw, session string) string {
	if session == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := parsed.Query()
	query.Set("session", session)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

type piState struct {
	SessionID string `json:"sessionId"`
}

type piRPCRecord struct {
	ID                    string          `json:"id,omitempty"`
	Type                  string          `json:"type"`
	Command               string          `json:"command,omitempty"`
	Success               *bool           `json:"success,omitempty"`
	Data                  json.RawMessage `json:"data,omitempty"`
	Message               json.RawMessage `json:"message,omitempty"`
	AssistantMessageEvent json.RawMessage `json:"assistantMessageEvent,omitempty"`
	ToolCallID            string          `json:"toolCallId,omitempty"`
	ToolName              string          `json:"toolName,omitempty"`
	Args                  json.RawMessage `json:"args,omitempty"`
	Result                json.RawMessage `json:"result,omitempty"`
	IsError               bool            `json:"isError,omitempty"`
	Usage                 *piUsage        `json:"usage,omitempty"`
	Error                 string          `json:"error,omitempty"`
}

type piUsage struct {
	Input       int `json:"input"`
	Output      int `json:"output"`
	CacheRead   int `json:"cacheRead"`
	CacheWrite  int `json:"cacheWrite"`
	Reasoning   int `json:"reasoning"`
	TotalTokens int `json:"totalTokens"`
	Cost        struct {
		Total float64 `json:"total"`
	} `json:"cost"`
}

func (a *PiAgent) handleEvent(event piRPCRecord, cb EventCallback, result *Result, resultMu *sync.Mutex, terminalErr *error, settled func()) {
	switch event.Type {
	case "message_update":
		var update struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if json.Unmarshal(event.AssistantMessageEvent, &update) != nil {
			return
		}
		switch update.Type {
		case "text_delta":
			cb(StreamEvent{Type: EventTextDelta, Content: update.Delta})
		case "thinking_delta":
			cb(StreamEvent{Type: EventThinking, ToolName: "Thinking", Content: update.Delta})
		}
	case "tool_execution_start":
		cb(StreamEvent{Type: EventToolStart, ToolID: event.ToolCallID, ToolName: event.ToolName, Input: string(event.Args)})
	case "tool_execution_end":
		cb(StreamEvent{Type: EventToolResult, ToolID: event.ToolCallID, ToolName: event.ToolName, Output: piToolText(event.Result), IsError: event.IsError})
	case "compaction_start":
		cb(StreamEvent{Type: EventSystem, Content: "Pi is compacting conversation context"})
	case "compaction_end":
		cb(StreamEvent{Type: EventSystem, Content: "Pi finished compacting conversation context"})
	case "message_end":
		var message struct {
			Role         string  `json:"role"`
			StopReason   string  `json:"stopReason"`
			ErrorMessage string  `json:"errorMessage"`
			Usage        piUsage `json:"usage"`
		}
		if json.Unmarshal(event.Message, &message) != nil || message.Role != "assistant" {
			return
		}
		a.publishUsage(message.Usage, cb, result, resultMu)
		resultMu.Lock()
		*terminalErr = nil
		if message.StopReason == "error" || message.StopReason == "aborted" {
			messageText := strings.TrimSpace(message.ErrorMessage)
			if messageText == "" {
				messageText = "Pi assistant response " + message.StopReason
			}
			*terminalErr = fmt.Errorf("%s", messageText)
		}
		resultMu.Unlock()
	case "agent_settled":
		settled()
	case "extension_error":
		cb(StreamEvent{Type: EventError, Content: event.Error, IsError: true})
	}
}

func (a *PiAgent) publishUsage(usage piUsage, cb EventCallback, result *Result, resultMu *sync.Mutex) {
	resultMu.Lock()
	result.Metadata = piAccumulateUsage(result.Metadata, usage)
	metadata := result.Metadata
	resultMu.Unlock()
	cb(StreamEvent{Type: EventContextUsage, Metadata: metadata})
}

func piToolText(raw json.RawMessage) string {
	var value struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &value) == nil && len(value.Content) > 0 {
		parts := make([]string, 0, len(value.Content))
		for _, part := range value.Content {
			parts = append(parts, part.Text)
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

func piUsageMetadata(usage piUsage) map[string]interface{} {
	return map[string]interface{}{
		"inputTokens":              usage.Input,
		"outputTokens":             usage.Output,
		"cacheReadInputTokens":     usage.CacheRead,
		"cacheCreationInputTokens": usage.CacheWrite,
		"reasoningOutputTokens":    usage.Reasoning,
		"totalTokens":              usage.TotalTokens,
		"totalCostUsd":             usage.Cost.Total,
	}
}

func piAccumulateUsage(previous map[string]interface{}, usage piUsage) map[string]interface{} {
	metadata := piUsageMetadata(usage)
	if previous == nil {
		return metadata
	}
	for _, key := range []string{"inputTokens", "outputTokens", "cacheReadInputTokens", "cacheCreationInputTokens", "reasoningOutputTokens", "totalTokens"} {
		metadata[key] = metadata[key].(int) + previous[key].(int)
	}
	metadata["totalCostUsd"] = metadata["totalCostUsd"].(float64) + previous["totalCostUsd"].(float64)
	return metadata
}

type piRPCResponse struct {
	data json.RawMessage
	err  error
}

type piRPCClient struct {
	cmdPath   string
	args      []string
	verbose   bool
	cmd       *exec.Cmd
	env       []string
	stdin     io.WriteCloser
	writeMu   sync.Mutex
	nextID    int64
	pendingMu sync.Mutex
	pending   map[string]chan piRPCResponse
	onEvent   func(piRPCRecord)
	readDone  chan struct{}
	readErr   error
	stderrMu  sync.Mutex
	stderr    []byte
	wg        sync.WaitGroup
}

func newPiRPCClient(command string, args []string, verbose bool) *piRPCClient {
	return &piRPCClient{cmdPath: command, args: args, verbose: verbose, pending: map[string]chan piRPCResponse{}, readDone: make(chan struct{})}
}

func (c *piRPCClient) start(workDir string) error {
	cmd := exec.Command(c.cmdPath, c.args...)
	cmd.Dir = workDir
	cmd.Env = c.env
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start pi RPC: %w", err)
	}
	c.cmd, c.stdin = cmd, stdin
	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		data, _ := io.ReadAll(stderr)
		c.stderrMu.Lock()
		c.stderr = data
		c.stderrMu.Unlock()
	}()
	go func() { defer c.wg.Done(); c.readLoop(stdout) }()
	return nil
}

func (c *piRPCClient) command(ctx context.Context, kind string, fields map[string]interface{}, out interface{}) error {
	id := fmt.Sprintf("nexbox-%d", atomic.AddInt64(&c.nextID, 1))
	line := map[string]interface{}{"id": id, "type": kind}
	for key, value := range fields {
		line[key] = value
	}
	ch := make(chan piRPCResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	data, err := json.Marshal(line)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	_, err = c.stdin.Write(append(data, '\n'))
	c.writeMu.Unlock()
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.readDone:
		return c.connectionError()
	case response := <-ch:
		if response.err != nil {
			return response.err
		}
		if out != nil && len(response.data) > 0 {
			if err := json.Unmarshal(response.data, out); err != nil {
				return err
			}
		}
		return nil
	}
}

func (c *piRPCClient) readLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024*1024), 20*1024*1024)
	for scanner.Scan() {
		var record piRPCRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		if record.Type == "response" && record.ID != "" {
			response := piRPCResponse{data: record.Data}
			if record.Success != nil && !*record.Success {
				response.err = fmt.Errorf("pi RPC %s failed", record.Command)
			}
			c.pendingMu.Lock()
			ch := c.pending[record.ID]
			delete(c.pending, record.ID)
			c.pendingMu.Unlock()
			if ch != nil {
				ch <- response
			}
			continue
		}
		if c.onEvent != nil {
			c.onEvent(record)
		}
	}
	if err := scanner.Err(); err != nil {
		c.readErr = err
	} else {
		c.readErr = io.EOF
	}
	close(c.readDone)
}

func (c *piRPCClient) connectionError() error {
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()
	return fmt.Errorf("pi RPC connection closed: %w; stderr: %s", c.readErr, string(c.stderr))
}
func (c *piRPCClient) close() {
	c.writeMu.Lock()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	c.writeMu.Unlock()
	if c.cmd != nil {
		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}
	c.wg.Wait()
}
