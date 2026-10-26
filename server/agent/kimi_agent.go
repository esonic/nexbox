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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const kimiACPProtocolVersion = 1

// kimiCheckpointPrefix marks a completed-turn checkpoint: the number of
// turns recorded in the session's main wire log once that turn ended.
const kimiCheckpointPrefix = "kimi-turns-v1:"

// kimiAskUserTool is the tool name NexBox's question card understands. Kimi's
// own AskUserQuestion tool uses the same name and input schema.
const kimiAskUserTool = "AskUserQuestion"

// KimiAgent adapts the Kimi Code CLI over its ACP stdio server (`kimi acp`).
// A fresh process is started for each NexBox turn; Kimi owns login, provider
// configuration and session persistence (under KIMI_CODE_HOME). NexBox only
// injects per-session MCP servers and maps the ACP stream onto its events.
type KimiAgent struct {
	CLIPath      string
	MCPServerURL string
	Verbose      bool
}

func NewKimiAgent() *KimiAgent {
	path := strings.TrimSpace(os.Getenv("NEXBOX_KIMI_CLI"))
	if path == "" {
		path = "kimi"
	}
	return &KimiAgent{CLIPath: path, Verbose: true}
}

var _ Agent = (*KimiAgent)(nil)

// Kimi's ACP server rejects a second session/prompt while a turn runs and
// exposes no steer method, so mid-turn messages fall back to NexBox's queue.
// Its session/fork has no turn selector; Fork is still advertised because
// forking the latest completed turn is exact (see kimiForkPreflight).
func (a *KimiAgent) Capabilities() Capabilities {
	return Capabilities{Fork: true, ChromeDevTools: true, ModelSwitchMode: ModelSwitchCurrentSession}
}

var _ ModelCatalog = (*KimiAgent)(nil)

// kimiModelAlias mirrors one `[models.<alias>]` entry as printed by
// `kimi provider list --json`. Overrides take precedence over the top-level
// fields, matching how Kimi itself resolves the effective value.
type kimiModelAlias struct {
	kimiModelFields
	Overrides kimiModelFields `json:"overrides"`
}

type kimiModelFields struct {
	MaxContextSize int      `json:"maxContextSize"`
	Capabilities   []string `json:"capabilities"`
	DisplayName    string   `json:"displayName"`
	SupportEfforts []string `json:"supportEfforts"`
	DefaultEffort  string   `json:"defaultEffort"`
}

// ListSupportedModels exposes the model aliases from Kimi's own config.
// Thinking levels follow the ACP server's picker rules (kimi-code
// packages/acp-server/src/model-catalog.ts) so every offered effort is one
// session/set_config_option accepts.
func (a *KimiAgent) ListSupportedModels(ctx context.Context) ([]ModelInfo, string) {

	output, err := exec.CommandContext(ctx, a.CLIPath, "provider", "list", "--json").Output()
	if err != nil {
		if a.Verbose {
			log.Printf("[kimi-agent] list models: %v", err)
		}
		return nil, "none"
	}
	var config struct {
		Models map[string]kimiModelAlias `json:"models"`
	}
	if err := json.Unmarshal(output, &config); err != nil {
		if a.Verbose {
			log.Printf("[kimi-agent] parse model list: %v", err)
		}
		return nil, "none"
	}
	defaultModel := kimiDefaultModel()
	models := make([]ModelInfo, 0, len(config.Models))
	for alias, entry := range config.Models {
		models = append(models, kimiModelInfo(alias, entry, alias == defaultModel))
	}
	if len(models) == 0 {
		return nil, "none"
	}
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].IsDefault != models[j].IsDefault {
			return models[i].IsDefault
		}
		return models[i].ID < models[j].ID
	})
	return models, "configured"
}

func kimiModelInfo(alias string, entry kimiModelAlias, isDefault bool) ModelInfo {
	fields, overrides := entry.kimiModelFields, entry.Overrides
	fields.MaxContextSize = cmpOr(overrides.MaxContextSize, fields.MaxContextSize)
	fields.DisplayName = cmpOr(overrides.DisplayName, fields.DisplayName)
	fields.DefaultEffort = cmpOr(overrides.DefaultEffort, fields.DefaultEffort)
	if overrides.Capabilities != nil {
		fields.Capabilities = overrides.Capabilities
	}
	if overrides.SupportEfforts != nil {
		fields.SupportEfforts = overrides.SupportEfforts
	}
	info := ModelInfo{ID: alias, Label: cmpOr(fields.DisplayName, alias), ContextWindow: fields.MaxContextSize, IsDefault: isDefault}

	if !kimiThinkingSupported(alias, fields.Capabilities) {
		return info
	}
	always := slices.Contains(fields.Capabilities, "always_thinking")
	switch {
	case len(fields.SupportEfforts) > 0:
		if !always {
			info.SupportedEfforts = append(info.SupportedEfforts, "off")
		}
		info.SupportedEfforts = append(info.SupportedEfforts, fields.SupportEfforts...)
		info.DefaultEffort = cmpOr(fields.DefaultEffort, fields.SupportEfforts[len(fields.SupportEfforts)/2])
	case !always:
		// A boolean thinking toggle. An always-thinking boolean model has a
		// single fixed level, so no picker is offered for it.
		info.SupportedEfforts = []string{"off", "on"}
		info.DefaultEffort = "on"
	}
	return info
}

// kimiThinkingSupported mirrors ACP's deriveThinkingSupported, whose name
// heuristic matches the catalog id — the alias, not the upstream model name.
func kimiThinkingSupported(alias string, capabilities []string) bool {
	if slices.Contains(capabilities, "thinking") || slices.Contains(capabilities, "always_thinking") {
		return true
	}
	id := strings.ToLower(alias)
	return strings.Contains(id, "thinking") || strings.Contains(id, "reason") || alias == "kimi-for-coding" || alias == "kimi-code"
}

func cmpOr[T comparable](values ...T) T {
	var zero T
	for _, value := range values {
		if value != zero {
			return value
		}
	}
	return zero
}

// kimiHome is Kimi's data root, which the spawned CLI inherits from NexBox's
// own environment.
func kimiHome() string {
	if home := strings.TrimSpace(os.Getenv("KIMI_CODE_HOME")); home != "" {
		return home
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(userHome, ".kimi-code")
}

var kimiDefaultModelRe = regexp.MustCompile(`^\s*default_model\s*=\s*"([^"]*)"`)

// kimiDefaultModel reads the top-level default_model scalar from Kimi's
// config.toml (it is not part of `kimi provider list --json`).
func kimiDefaultModel() string {
	data, err := os.ReadFile(filepath.Join(kimiHome(), "config.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			break
		}
		if match := kimiDefaultModelRe.FindStringSubmatch(line); match != nil {
			return match[1]
		}
	}
	return ""
}

type kimiConfigOption struct {
	ID           string `json:"id"`
	CurrentValue string `json:"currentValue"`
	Options      []struct {
		Value string `json:"value"`
	} `json:"options"`
}

func (o *kimiConfigOption) offers(value string) bool {
	for _, option := range o.Options {
		if option.Value == value {
			return true
		}
	}
	return false
}

type kimiSessionResult struct {
	SessionID     string             `json:"sessionId"`
	ConfigOptions []kimiConfigOption `json:"configOptions"`
}

func kimiOption(options []kimiConfigOption, id string) *kimiConfigOption {
	for i := range options {
		if options[i].ID == id {
			return &options[i]
		}
	}
	return nil
}

func (a *KimiAgent) SendMessage(ctx context.Context, req Request, cb EventCallback) (Result, error) {
	if err := validateACPWorkDir(req.WorkDir); err != nil {
		return Result{}, err
	}
	if req.Fork != nil {
		if err := kimiForkPreflight(req.Fork, req.WorkDir); err != nil {
			return Result{}, err
		}
	}

	turn := newKimiTurn(cb, req.WorkDir)
	client := newACPStdioClient(a.CLIPath, []string{"acp"})
	client.verbose = a.Verbose
	acpModel, displayModel := strings.TrimSpace(req.Model), ""
	client.setNotificationHandler(func(method string, params json.RawMessage) {
		if method == "session/update" {
			turn.handleUpdate(params)
		}
	})
	client.setServerRequestHandler(func(method string, params json.RawMessage) (interface{}, *acpWireError) {
		switch method {
		case "elicitation/create":
			return turn.handleElicitation(ctx, req.UserInputHandler, params), nil
		case "session/request_permission":
			return turn.handlePermission(ctx, req.UserInputHandler, params), nil
		}
		return nil, &acpWireError{Code: -32601, Message: "method not supported by NexBox ACP client"}
	})
	if err := client.start(req.WorkDir); err != nil {
		return Result{}, fmt.Errorf("start kimi ACP: %w", err)
	}
	defer func() {
		if err := client.close(); err != nil && a.Verbose {
			log.Printf("[kimi-agent] ACP process close: %v", err)
		}
	}()
	rpcErr := func(err error) error { return kimiRPCError(err, client.stderr()) }

	var initialize struct {
		ProtocolVersion int `json:"protocolVersion"`
		AgentInfo       struct {
			Version string `json:"version"`
		} `json:"agentInfo"`
	}
	// Advertising the form elicitation capability routes AskUserQuestion
	// through elicitation/create, which keeps every question and multi-select;
	// without it Kimi degrades to a single-question permission prompt.
	initParams := map[string]interface{}{
		"protocolVersion":    kimiACPProtocolVersion,
		"clientCapabilities": map[string]interface{}{"elicitation": map[string]interface{}{"form": map[string]interface{}{}}},
	}
	if err := client.request(ctx, "initialize", initParams, &initialize); err != nil {
		return Result{}, rpcErr(err)
	}
	if initialize.ProtocolVersion != kimiACPProtocolVersion {
		return Result{}, fmt.Errorf("unsupported kimi ACP protocol version %d (want %d)", initialize.ProtocolVersion, kimiACPProtocolVersion)
	}

	// MCP servers are ephemeral per-session overlays in Kimi: they must be
	// supplied every time a process materializes the session.
	mcpServers := a.mcpServers(req)
	var session kimiSessionResult
	switch {
	case req.Fork != nil:
		// session/fork ignores mcpServers and keeps the fork live, so close it
		// and resume it with this turn's servers attached.
		var forked kimiSessionResult
		if err := client.request(ctx, "session/fork", map[string]interface{}{"sessionId": req.Fork.SessionID, "cwd": req.WorkDir, "mcpServers": []interface{}{}}, &forked); err != nil {
			return Result{}, fmt.Errorf("fork kimi session: %w", rpcErr(err))
		}
		if forked.SessionID == "" {
			return Result{}, fmt.Errorf("kimi session/fork returned an empty sessionId")
		}
		// The parent may have started another turn after the preflight (it
		// runs in another process, so Kimi's own busy check cannot see it).
		// Validate the copy itself and discard it if it moved past the
		// checkpoint.
		if err := kimiForkCopyMatches(req.Fork.Checkpoint, kimiWirePath(forked.SessionID)); err != nil {
			_ = client.request(ctx, "session/delete", map[string]interface{}{"sessionId": forked.SessionID}, nil)
			return Result{}, err
		}
		if err := client.request(ctx, "session/close", map[string]interface{}{"sessionId": forked.SessionID}, nil); err != nil {
			return Result{}, fmt.Errorf("close forked kimi session: %w", rpcErr(err))
		}
		if err := client.request(ctx, "session/resume", map[string]interface{}{"sessionId": forked.SessionID, "cwd": req.WorkDir, "mcpServers": mcpServers}, &session); err != nil {
			return Result{}, fmt.Errorf("resume forked kimi session: %w", rpcErr(err))
		}
		session.SessionID = forked.SessionID
	case req.SessionID != "":
		err := client.request(ctx, "session/resume", map[string]interface{}{"sessionId": req.SessionID, "cwd": req.WorkDir, "mcpServers": mcpServers}, &session)
		var acpErr *acpRPCError
		if errors.As(err, &acpErr) && acpErr.Code == -32602 {
			return Result{}, fmt.Errorf("%w: %s", ErrSessionExpired, acpErr.Message)
		}
		if err != nil {
			return Result{}, rpcErr(err)
		}
		session.SessionID = req.SessionID
	default:
		if err := client.request(ctx, "session/new", map[string]interface{}{"cwd": req.WorkDir, "mcpServers": mcpServers}, &session); err != nil {
			return Result{}, rpcErr(err)
		}
		if session.SessionID == "" {
			return Result{}, fmt.Errorf("kimi session/new returned an empty sessionId")
		}
	}
	sessionID := session.SessionID
	result := Result{SessionID: sessionID}

	// NexBox runs agents unattended (bypassPermissions for Claude, approval
	// "never" for Codex). YOLO auto-approves routine tools; the few approvals
	// it still raises are granted in handlePermission, while AskUserQuestion
	// keeps reaching the user.
	if err := client.request(ctx, "session/set_mode", map[string]interface{}{"sessionId": sessionID, "modeId": "yolo"}, nil); err != nil {
		return result, fmt.Errorf("set kimi permission mode: %w", rpcErr(err))
	}
	options := session.ConfigOptions
	if model := acpModel; model != "" {
		if current := kimiOption(options, "model"); current == nil || current.CurrentValue != model {
			var updated struct {
				ConfigOptions []kimiConfigOption `json:"configOptions"`
			}
			if err := client.request(ctx, "session/set_config_option", map[string]interface{}{"sessionId": sessionID, "configId": "model", "value": model}, &updated); err != nil {
				return result, fmt.Errorf("select kimi model %q: %w", model, rpcErr(err))
			}
			options = updated.ConfigOptions
		}
	}
	if effort := strings.TrimSpace(req.Effort); effort != "" {
		thinking := kimiOption(options, "thinking")
		switch {
		case thinking == nil || !thinking.offers(effort):
			if a.Verbose {
				log.Printf("[kimi-agent] dropping effort %q: not offered by the current model", effort)
			}
		case thinking.CurrentValue != effort:
			if err := client.request(ctx, "session/set_config_option", map[string]interface{}{"sessionId": sessionID, "configId": "thinking", "value": effort}, nil); err != nil {
				return result, fmt.Errorf("set kimi thinking %q: %w", effort, rpcErr(err))
			}
		}
	}
	model := displayModel
	if current := kimiOption(options, "model"); current != nil && model == "" {
		model = current.CurrentValue
	}
	turn.setModel(model)
	cb(StreamEvent{Type: EventSessionInfo, Metadata: map[string]interface{}{"model": model, "cwd": req.WorkDir, "version": initialize.AgentInfo.Version}})

	prompt, err := kimiPromptBlocks(req, req.SessionID == "" && req.Fork == nil)
	if err != nil {
		return result, err
	}
	wire := kimiWirePath(sessionID)
	before := scanKimiWire(wire, 0)

	type promptOutcome struct {
		stopReason string
		err        error
	}
	promptDone := make(chan promptOutcome, 1)
	promptCtx, cancelPrompt := context.WithCancel(context.Background())
	defer cancelPrompt()
	go func() {
		var response struct {
			StopReason string `json:"stopReason"`
		}
		err := client.request(promptCtx, "session/prompt", map[string]interface{}{"sessionId": sessionID, "prompt": prompt}, &response)
		promptDone <- promptOutcome{stopReason: response.StopReason, err: err}
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
		return result, ctx.Err()
	}
	if outcome.err != nil {
		return result, rpcErr(outcome.err)
	}

	// Kimi pushes the context-usage update just after settling the prompt.
	select {
	case <-turn.usage:
	case <-time.After(time.Second):
	}

	// A compaction started by this turn (auto after its last step, or a
	// manual /compact) would die with the process.
	for deadline := time.After(15 * time.Minute); turn.isCompacting(); {
		select {
		case <-turn.compacted:
		case <-ctx.Done():
			return result, ctx.Err()
		case <-deadline:
			return result, fmt.Errorf("kimi context compaction did not finish within 15 minutes")
		}
	}

	// The ACP stream reports a failed turn as a plain end_turn. The session's
	// wire log is the only place the failure (and per-turn token usage) is
	// recorded, so read what this turn appended. Built-in slash commands
	// (/usage, /compact, ...) run without a turn, so there is nothing to wait
	// for.
	after := scanKimiWire(wire, before.size)
	for deadline := time.Now().Add(2 * time.Second); after.turnsEnded == 0 && before.ok && !isSlashCommand(req.Message) && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		after = scanKimiWire(wire, before.size)
	}
	metadata := map[string]interface{}{"stopReason": outcome.stopReason}
	if model != "" {
		metadata["model"] = model
	}
	if after.ok {
		metadata["inputTokens"] = after.usage.InputOther
		metadata["outputTokens"] = after.usage.Output
		metadata["cacheReadInputTokens"] = after.usage.InputCacheRead
		metadata["cacheCreationInputTokens"] = after.usage.InputCacheCreation
	}
	result.Metadata = metadata
	if after.failure != "" {
		return result, fmt.Errorf("kimi turn failed: %s", after.failure)
	}
	if outcome.stopReason == "refusal" {
		return result, fmt.Errorf("kimi turn was refused (provider content filter or a prompt hook blocked it)")
	}
	if !after.ok && !turn.producedOutput() {
		return result, fmt.Errorf("kimi ended the turn without output; see %s for details", filepath.Join(kimiHome(), "logs", "kimi-code.log"))
	}
	if after.turnsEnded > 0 {
		result.Checkpoint = kimiCheckpointPrefix + strconv.Itoa(before.turnsEnded+after.turnsEnded)
	}
	cb(StreamEvent{Type: EventDone, SessionID: sessionID, Metadata: metadata})
	return result, nil
}

// kimiForkPreflight validates a fork against Kimi's head-only session/fork.
// The checkpoint is exact only while no turn has ended since it was taken.
// The fork also keeps the source's workspace, so it must run in the same
// directory or its tools would edit the parent's tree.
func kimiForkPreflight(fork *ForkContext, workDir string) error {
	turns, ok := strings.CutPrefix(fork.Checkpoint, kimiCheckpointPrefix)
	if _, err := strconv.Atoi(turns); fork.SessionID == "" || !ok || err != nil {
		return fmt.Errorf("kimi fork has an invalid checkpoint %q", fork.Checkpoint)
	}
	dir := kimiSessionDir(fork.SessionID)
	if dir == "" {
		return fmt.Errorf("kimi fork: parent session %s not found under %s", fork.SessionID, kimiHome())
	}
	if err := kimiForkCopyMatches(fork.Checkpoint, filepath.Join(dir, "agents", "main", "wire.jsonl")); err != nil {
		return err
	}
	var state struct {
		Cwd string `json:"cwd"`
	}
	if data, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil && json.Unmarshal(data, &state) == nil && state.Cwd != "" && workDir != "" {
		if !samePath(state.Cwd, workDir) {
			return fmt.Errorf("kimi fork must run in the parent session's directory %s (got %s): Kimi forks keep the parent's workspace", state.Cwd, workDir)
		}
	}
	return nil
}

// kimiForkCopyMatches reports whether a session's history ends exactly at
// the checkpoint: the same number of finished turns and no turn in flight.
func kimiForkCopyMatches(checkpoint, wire string) error {
	want, _ := strconv.Atoi(strings.TrimPrefix(checkpoint, kimiCheckpointPrefix))
	head := scanKimiWire(wire, 0)
	switch {
	case !head.ok:
		return fmt.Errorf("kimi fork: cannot read the parent session's history")
	case head.openTurn:
		return fmt.Errorf("kimi fork: the parent session is still running a turn; retry when it finishes")
	case head.turnsEnded != want:
		return fmt.Errorf("kimi can only fork from the latest turn of a session (Kimi's session/fork has no turn selector); fork from the last reply instead")
	}
	return nil
}

func samePath(a, b string) bool {
	resolve := func(p string) string {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return resolved
		}
		return filepath.Clean(p)
	}
	return resolve(a) == resolve(b)
}

func kimiRPCError(err error, stderr string) error {
	var acpErr *acpRPCError
	if errors.As(err, &acpErr) && acpErr.Code == -32000 {
		return fmt.Errorf("kimi is not authenticated; run `kimi login` or configure a provider: %w", err)
	}
	return withACPStderr(err, stderr)
}

// kimiPromptBlocks builds the ACP prompt. Image paths stay in the text (the
// model can re-read them with its file tools) and are also attached as image
// blocks for direct vision input.
func kimiPromptBlocks(req Request, isNewSession bool) ([]interface{}, error) {
	text := BuildPrompt(req.StaticContext, req.DynamicContext, req.Message, isNewSession, req.Images)
	blocks := []interface{}{map[string]interface{}{"type": "text", "text": text}}
	for _, imagePath := range req.Images {
		data, err := os.ReadFile(imagePath)
		if err != nil {
			return nil, fmt.Errorf("read attached image %s: %w", imagePath, err)
		}
		mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(imagePath)))
		if mimeType == "" {
			mimeType = http.DetectContentType(data)
		}
		blocks = append(blocks, map[string]interface{}{"type": "image", "mimeType": mimeType, "data": base64.StdEncoding.EncodeToString(data)})
	}
	return blocks, nil
}

func (a *KimiAgent) mcpServers(req Request) []interface{} {
	servers := []interface{}{}
	http := func(name, url string, headers map[string]string) {
		servers = append(servers, map[string]interface{}{"type": "http", "name": name, "url": url, "headers": kimiNamedPairs(headers)})
	}
	if a.MCPServerURL != "" {
		http("nexbox", withPiMCPSession(a.MCPServerURL, req.ConversationID), nil)
	}
	if hasFeature(req.Features, "chrome_devtools") && req.ChromeMCPURL != "" {
		http("chrome", req.ChromeMCPURL, nil)
	}
	for _, mcp := range req.ExtraMCPServers {
		switch {
		case mcp.Type == "http" && mcp.URL != "":
			serverURL := mcp.URL
			if mcp.AppendSessionURL {
				serverURL = withPiMCPSession(serverURL, req.ConversationID)
			}
			http(mcp.Key, serverURL, mcp.Headers)
		case mcp.Type == "stdio" && mcp.Cmd != "":
			servers = append(servers, map[string]interface{}{"name": mcp.Key, "command": mcp.Cmd, "args": append([]string{}, mcp.Args...), "env": kimiNamedPairs(mcp.Env)})
		}
	}
	return servers
}

// kimiNamedPairs converts a map to ACP's ordered {name, value} list.
func kimiNamedPairs(values map[string]string) []map[string]string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, map[string]string{"name": key, "value": values[key]})
	}
	return pairs
}

// kimiTurn holds the per-turn mapping state. ACP notifications and server
// requests are delivered from the client's single read loop, so tools needs
// no lock; the fields under mu are also read by SendMessage.
type kimiTurn struct {
	cb        EventCallback
	workDir   string
	tools     map[string]*kimiTool
	usage     chan struct{}
	compacted chan struct{}

	mu         sync.Mutex
	model      string
	output     bool
	compacting bool
}

func newKimiTurn(cb EventCallback, workDir string) *kimiTurn {
	return &kimiTurn{cb: cb, workDir: workDir, tools: map[string]*kimiTool{}, usage: make(chan struct{}, 1), compacted: make(chan struct{}, 1)}
}

type kimiTool struct {
	name     string
	input    json.RawMessage
	diff     string
	finished bool
}

func (t *kimiTurn) setModel(model string) {
	t.mu.Lock()
	t.model = model
	t.mu.Unlock()
}

func (t *kimiTurn) markOutput() {
	t.mu.Lock()
	t.output = true
	t.mu.Unlock()
}

func (t *kimiTurn) producedOutput() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.output
}

// setCompacting tracks the running compaction. A start drops any completion
// signal left by an earlier compaction, so waiters only wake for this one.
func (t *kimiTurn) setCompacting(compacting bool) {
	t.mu.Lock()
	t.compacting = compacting
	t.mu.Unlock()
	if compacting {
		select {
		case <-t.compacted:
		default:
		}
		return
	}
	select {
	case t.compacted <- struct{}{}:
	default:
	}
}

func (t *kimiTurn) isCompacting() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.compacting
}

type kimiToolCallContent struct {
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
	Path    string          `json:"path"`
	OldText *string         `json:"oldText"`
	NewText string          `json:"newText"`
}

type kimiSessionUpdate struct {
	SessionUpdate string                `json:"sessionUpdate"`
	Content       json.RawMessage       `json:"content"`
	ToolCallID    string                `json:"toolCallId"`
	Title         string                `json:"title"`
	Kind          string                `json:"kind"`
	Status        string                `json:"status"`
	RawInput      json.RawMessage       `json:"rawInput"`
	RawOutput     json.RawMessage       `json:"rawOutput"`
	Used          int                   `json:"used"`
	Size          int                   `json:"size"`
	toolContent   []kimiToolCallContent `json:"-"`
}

func (t *kimiTurn) handleUpdate(params json.RawMessage) {
	var notification struct {
		Update kimiSessionUpdate `json:"update"`
	}
	if json.Unmarshal(params, &notification) != nil {
		return
	}
	update := notification.Update
	switch update.SessionUpdate {
	case "agent_message_chunk":
		text := acpContentText(update.Content)
		if text == "" {
			return
		}
		// Kimi reports compaction as ordinary agent text (acp-server
		// session.ts). Compaction runs in the background, so track it to keep
		// the process alive until it finishes.
		switch {
		case strings.HasPrefix(text, "Compacting conversation context"):
			t.setCompacting(true)
			t.cb(StreamEvent{Type: EventSystem, Content: text})
			return
		case strings.HasPrefix(text, "Compaction completed.\n") || text == "Compaction cancelled.":
			t.setCompacting(false)
			t.cb(StreamEvent{Type: EventSystem, Content: text})
			return
		case strings.HasPrefix(text, "Compaction is blocked by the current turn"):
			t.cb(StreamEvent{Type: EventSystem, Content: text})
			return
		case strings.HasPrefix(text, "Context compaction started") || text == "A context compaction is already running.":
			// The reply to a manual /compact, shown as the turn's answer.
			t.setCompacting(true)
		}
		t.markOutput()
		t.cb(StreamEvent{Type: EventTextDelta, Content: text})
	case "agent_thought_chunk":
		if text := acpContentText(update.Content); text != "" {
			t.cb(StreamEvent{Type: EventThinking, ToolName: "Thinking", Content: text})
		}
	case "tool_call", "tool_call_update":
		if json.Unmarshal(update.Content, &update.toolContent) != nil {
			update.toolContent = nil
		}
		t.handleToolUpdate(update)
	case "usage_update":
		t.mu.Lock()
		model := t.model
		t.mu.Unlock()
		metadata := map[string]interface{}{"usedTokens": update.Used, "contextWindow": update.Size}
		if model != "" {
			metadata["model"] = model
		}
		t.cb(StreamEvent{Type: EventContextUsage, Metadata: metadata})
		select {
		case t.usage <- struct{}{}:
		default:
		}
	}
}

// handleToolUpdate maps Kimi's tool lifecycle. Kimi creates the card from
// the first streamed argument delta (status "pending", title = tool name),
// streams partial argument text, then sends the parsed rawInput once the call
// is dispatched and the output on completion.
func (t *kimiTurn) handleToolUpdate(update kimiSessionUpdate) {
	id := update.ToolCallID
	if id == "" {
		return
	}
	tool := t.tools[id]
	if tool == nil {
		if update.SessionUpdate != "tool_call" && len(update.RawInput) == 0 {
			return
		}
		name := update.Title
		if update.Status != "pending" {
			// Created at dispatch without streamed arguments: the title is the
			// human-readable description, so recover the tool name.
			name = kimiToolName(update.Kind, update.RawInput, update.Title)
		}
		tool = &kimiTool{name: name, input: update.RawInput}
		t.tools[id] = tool
		t.markOutput()
		t.cb(StreamEvent{Type: EventToolStart, ToolID: id, ToolName: tool.name, Input: renderACPRawValue(update.RawInput)})
	} else if len(update.RawInput) > 0 && string(update.RawInput) != string(tool.input) {
		tool.input = update.RawInput
		t.cb(StreamEvent{Type: EventToolInputReplace, ToolID: id, Input: renderACPRawValue(update.RawInput)})
	}

	var texts []string
	for _, content := range update.toolContent {
		switch content.Type {
		case "diff":
			oldText := ""
			if content.OldText != nil {
				oldText = *content.OldText
			}
			if diff := unifiedDiffFromTexts(content.Path, oldText, content.NewText); diff != "" {
				tool.diff = diff
			}
		case "content":
			if text := acpContentText(content.Content); text != "" {
				texts = append(texts, text)
			}
		}
	}

	if tool.finished || (update.Status != "completed" && update.Status != "failed") {
		return
	}
	tool.finished = true
	output := strings.Join(texts, "\n")
	if output == "" {
		output = renderACPRawValue(update.RawOutput)
	}
	diff := tool.diff
	if diff == "" && update.Status == "completed" && tool.name == "Write" {
		diff = t.writeDiff(tool.input)
	}
	t.cb(StreamEvent{Type: EventToolResult, ToolID: id, ToolName: tool.name, Output: output, Diff: diff, IsError: update.Status == "failed"})
}

// writeDiff renders a Write that Kimi sent no before/after for (a newly
// created file) as an all-added diff.
func (t *kimiTurn) writeDiff(input json.RawMessage) string {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if json.Unmarshal(input, &args) != nil || args.Path == "" {
		return ""
	}
	path := args.Path
	if !filepath.IsAbs(path) && t.workDir != "" {
		path = filepath.Join(t.workDir, path)
	}
	return unifiedDiffForFullContent(path, args.Content, false)
}

// kimiToolName recovers a tool name from the ACP kind and argument shape for
// calls created without streamed arguments, matching the names Kimi itself
// uses (most of which NexBox's tool cards already render).
func kimiToolName(kind string, rawInput json.RawMessage, title string) string {
	var args map[string]json.RawMessage
	_ = json.Unmarshal(rawInput, &args)
	has := func(key string) bool { _, ok := args[key]; return ok }
	switch {
	case kind == "execute" && has("command"):
		return "Bash"
	case kind == "edit" && has("old_string"):
		return "Edit"
	case kind == "edit" && has("content"):
		return "Write"
	case kind == "read" && has("pattern") && has("path") && !has("glob"):
		return "Grep"
	case kind == "read" && has("pattern"):
		return "Glob"
	case kind == "read" && has("path"):
		return "Read"
	case kind == "fetch" && has("url"):
		return "FetchURL"
	case kind == "fetch" && has("query"):
		return "WebSearch"
	case has("questions"):
		return kimiAskUserTool
	case has("subagent_type") || (has("prompt") && has("description")):
		return "Agent"
	}
	return firstNonEmpty(title, "tool")
}

// kimiElicitationSchema is the form schema Kimi builds for AskUserQuestion
// (acp-server question.ts): one property q<i> per question.
type kimiElicitationSchema struct {
	ToolCallID      string `json:"toolCallId"`
	RequestedSchema struct {
		Properties map[string]struct {
			Type        string          `json:"type"`
			Title       string          `json:"title"`
			Description string          `json:"description"`
			OneOf       []kimiEnumEntry `json:"oneOf"`
			Items       struct {
				AnyOf []kimiEnumEntry `json:"anyOf"`
			} `json:"items"`
		} `json:"properties"`
	} `json:"requestedSchema"`
}

type kimiEnumEntry struct {
	Const       string `json:"const"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type kimiQuestion struct {
	Question    string               `json:"question"`
	Header      string               `json:"header,omitempty"`
	MultiSelect bool                 `json:"multiSelect"`
	Options     []kimiQuestionOption `json:"options"`
}

type kimiQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

var kimiCancelledElicitation = map[string]interface{}{"action": "cancel"}

// handleElicitation shows Kimi's AskUserQuestion form as NexBox's question
// card and maps the answers back onto the form properties. The tool call's
// rawInput carries the full question texts and headers, which the form
// schema compresses into titles.
func (t *kimiTurn) handleElicitation(ctx context.Context, handler UserInputHandler, params json.RawMessage) interface{} {
	var form kimiElicitationSchema
	if handler == nil || json.Unmarshal(params, &form) != nil {
		return kimiCancelledElicitation
	}
	count := len(form.RequestedSchema.Properties)
	var asked struct {
		Questions []kimiQuestion `json:"questions"`
	}
	if tool := t.tools[form.ToolCallID]; tool != nil {
		_ = json.Unmarshal(tool.input, &asked)
	}
	questions := make([]kimiQuestion, count)
	for i := range questions {
		property, ok := form.RequestedSchema.Properties["q"+strconv.Itoa(i)]
		if !ok {
			return kimiCancelledElicitation
		}
		entries := property.OneOf
		question := kimiQuestion{Question: property.Title, Header: property.Title, MultiSelect: property.Type == "array"}
		if question.MultiSelect {
			entries = property.Items.AnyOf
		}
		if len(asked.Questions) == count {
			question.Question = cmpOr(asked.Questions[i].Question, question.Question)
			question.Header = asked.Questions[i].Header
		}
		for _, entry := range entries {
			question.Options = append(question.Options, kimiQuestionOption{Label: entry.Const, Description: entry.Description})
		}
		questions[i] = question
	}
	input, _ := json.Marshal(map[string]interface{}{"question": questions[0].Question, "questions": questions})

	response, err := handler(firstNonEmpty(form.ToolCallID, "kimi-question"), kimiAskUserTool, string(input))
	if err != nil || ctx.Err() != nil {
		return kimiCancelledElicitation
	}
	var answered struct {
		Answers map[string]string `json:"answers"`
	}
	if json.Unmarshal([]byte(response), &answered) != nil {
		answered.Answers = map[string]string{questions[0].Question: response}
	}
	content := map[string]interface{}{}
	for i, question := range questions {
		answer := strings.TrimSpace(answered.Answers[question.Question])
		if answer == "" {
			continue
		}
		key := "q" + strconv.Itoa(i)
		if !question.MultiSelect {
			content[key] = answer
			continue
		}
		// NexBox joins multi-select labels with ", ", and labels may contain
		// that separator themselves, so match whole delimited segments.
		var labels []string
		for _, option := range question.Options {
			if strings.Contains(", "+answer+", ", ", "+option.Label+", ") {
				labels = append(labels, option.Label)
			}
		}
		if len(labels) > 0 {
			content[key] = labels
		}
	}
	if len(content) == 0 {
		return kimiCancelledElicitation
	}
	return map[string]interface{}{"action": "accept", "content": content}
}

type kimiPermissionRequest struct {
	Options []struct {
		OptionID string `json:"optionId"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
	} `json:"options"`
	ToolCall struct {
		ToolCallID string                `json:"toolCallId"`
		Title      string                `json:"title"`
		Content    []kimiToolCallContent `json:"content"`
	} `json:"toolCall"`
}

func kimiSelectedPermission(optionID string) map[string]interface{} {
	return map[string]interface{}{"outcome": map[string]interface{}{"outcome": "selected", "optionId": optionID}}
}

var kimiCancelledPermission = map[string]interface{}{"outcome": map[string]interface{}{"outcome": "cancelled"}}

// handlePermission grants the approvals YOLO mode still raises (dangerous
// shell commands, plan exit). Kimi also routes AskUserQuestion here when an
// elicitation fails; that single-question form goes to the user.
func (t *kimiTurn) handlePermission(ctx context.Context, handler UserInputHandler, params json.RawMessage) interface{} {
	var permission kimiPermissionRequest
	if json.Unmarshal(params, &permission) != nil {
		return kimiCancelledPermission
	}
	if permission.ToolCall.Title == kimiAskUserTool {
		return t.askPermissionQuestion(ctx, handler, permission)
	}
	for _, kind := range []string{"allow_once", "allow_always"} {
		for _, option := range permission.Options {
			if option.Kind == kind {
				return kimiSelectedPermission(option.OptionID)
			}
		}
	}
	return kimiCancelledPermission
}

func (t *kimiTurn) askPermissionQuestion(ctx context.Context, handler UserInputHandler, permission kimiPermissionRequest) interface{} {
	if handler == nil {
		return kimiCancelledPermission
	}
	var text []string
	for _, content := range permission.ToolCall.Content {
		if value := acpContentText(content.Content); value != "" {
			text = append(text, value)
		}
	}
	question := kimiQuestion{Question: firstNonEmpty(strings.Join(text, "\n"), kimiAskUserTool)}
	for _, option := range permission.Options {
		if option.Kind == "allow_once" {
			question.Options = append(question.Options, kimiQuestionOption{Label: option.Name})
		}
	}
	input, _ := json.Marshal(map[string]interface{}{"question": question.Question, "questions": []kimiQuestion{question}})
	response, err := handler(firstNonEmpty(permission.ToolCall.ToolCallID, "kimi-question"), kimiAskUserTool, string(input))
	if err != nil || ctx.Err() != nil {
		return kimiCancelledPermission
	}
	answer := strings.TrimSpace(response)
	var answered struct {
		Answers map[string]string `json:"answers"`
	}
	if json.Unmarshal([]byte(response), &answered) == nil {
		answer = strings.TrimSpace(answered.Answers[question.Question])
	}
	for _, option := range permission.Options {
		if option.Kind == "allow_once" && option.Name == answer {
			return kimiSelectedPermission(option.OptionID)
		}
	}
	return kimiCancelledPermission
}
