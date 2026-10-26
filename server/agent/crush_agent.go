package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// crushModel is a model reported by Crush's own native catalog. Provider and
// ID are split only at the adapter boundary before sending its HTTP request.
type crushModel struct {
	ID       string
	Provider string
}

// CrushAgent implements the native Crush server protocol: HTTP requests for
// workspace/session operations and one long-lived SSE stream per workspace.
// It intentionally does not parse terminal output.
type CrushAgent struct {
	CLIPath string
	DataDir string
	Verbose bool

	serverMu      sync.Mutex
	serverURL     string
	serverCmd     *exec.Cmd
	httpClient    *http.Client
	serverStarted bool

	lifecycleMu sync.Mutex
	activeTurns int

	workspacesMu sync.Mutex
	workspaces   map[string]*crushWorkspace
}

type crushWorkspace struct {
	id       string
	path     string
	clientID string

	ctx    context.Context
	cancel context.CancelFunc

	mu           sync.Mutex
	runs         map[string]*crushRun
	messageState map[string]crushMessageState
	eventsReady  chan error
	turnSlot     chan struct{}
}

type crushRun struct {
	sessionID string
	callback  EventCallback
	input     UserInputHandler
	done      chan crushRunComplete
}

type crushRunComplete struct {
	sessionID string
	runID     string
	text      string
	err       string
	cancelled bool
}

type crushMessageState struct {
	text      string
	thinking  string
	toolCalls map[string]bool
	toolDone  map[string]bool
}

func NewCrushAgent() *CrushAgent {
	return &CrushAgent{
		CLIPath:    defaultCrushCLIPath(),
		Verbose:    true,
		workspaces: make(map[string]*crushWorkspace),
	}
}

// defaultCrushCLIPath makes the official `go install` location work even
// when the invoking service did not add ~/go/bin to PATH. The explicit
// override remains useful for packaged or remotely managed installations.
func defaultCrushCLIPath() string {
	if path := strings.TrimSpace(os.Getenv("NEXBOX_CRUSH_CLI")); path != "" {
		return path
	}
	if _, err := exec.LookPath("crush"); err == nil {
		return "crush"
	}

	dirs := make([]string, 0, 6)
	addGoBinDirs := func(goBin, goPath string) {
		if goBin = strings.TrimSpace(goBin); goBin != "" {
			dirs = append(dirs, goBin)
		}
		for _, entry := range filepath.SplitList(goPath) {
			if entry != "" {
				dirs = append(dirs, filepath.Join(entry, "bin"))
			}
		}
	}
	addGoBinDirs(os.Getenv("GOBIN"), os.Getenv("GOPATH"))
	// GOPATH is commonly a Go default rather than an exported environment
	// variable. Ask the installed Go tool for it so `go install` works in
	// service environments with a different HOME/PATH.
	if output, err := exec.Command("go", "env", "GOBIN", "GOPATH").Output(); err == nil {
		values := strings.Split(strings.TrimRight(string(output), "\n"), "\n")
		if len(values) == 2 {
			addGoBinDirs(values[0], values[1])
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "go", "bin"))
	}
	for _, dir := range dirs {
		candidate := filepath.Join(dir, "crush")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return "crush"
}

var _ Agent = (*CrushAgent)(nil)
var _ ModelCatalog = (*CrushAgent)(nil)

func (a *CrushAgent) Capabilities() Capabilities {
	// Crush's native API can update the active model while preserving the
	// underlying session. Fork and active-turn interjection are not exposed as
	// Nexbox semantics by the current API.
	return Capabilities{ModelSwitchMode: ModelSwitchCurrentSession}
}

// ListSupportedModels reads the native server's workspace configuration. Unlike
// `crush models`, this intentionally excludes Catwalk's unconfigured catalog,
// so Nexbox only shows models the user explicitly configured in Crush.
func (a *CrushAgent) ListSupportedModels(ctx context.Context) ([]ModelInfo, string) {
	a.beginTurn()
	defer a.finishTurn()
	if err := a.ensureServer(ctx); err != nil {
		if a.Verbose {
			log.Printf("[crush-agent] start model catalog server: %v", err)
		}
		return nil, "none"
	}
	catalogDir, err := os.MkdirTemp("", "nexbox-crush-catalog-")
	if err != nil {
		return nil, "none"
	}
	defer os.RemoveAll(catalogDir)
	clientID, err := newCrushID()
	if err != nil {
		return nil, "none"
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := a.postJSON(ctx, "/v1/workspaces", nil, map[string]interface{}{
		"path": catalogDir, "data_dir": filepath.Join(catalogDir, "state"), "client_id": clientID,
	}, http.StatusOK, &created); err != nil {
		if a.Verbose {
			log.Printf("[crush-agent] create model catalog workspace: %v", err)
		}
		return nil, "none"
	}
	if strings.TrimSpace(created.ID) == "" {
		if a.Verbose {
			log.Printf("[crush-agent] model catalog workspace did not return an ID")
		}
		return nil, "none"
	}
	defer a.deleteWorkspace(created.ID)
	var config crushNativeConfig
	if err := a.getJSON(ctx, "/v1/workspaces/"+url.PathEscape(created.ID)+"/config", &config); err != nil {
		if a.Verbose {
			log.Printf("[crush-agent] get configured models: %v", err)
		}
		return nil, "none"
	}
	return configuredCrushModels(config.Providers), "dynamic"
}

func (a *CrushAgent) SendMessage(ctx context.Context, req Request, cb EventCallback) (Result, error) {
	if req.Fork != nil {
		return Result{}, fmt.Errorf("crush native API does not support fork")
	}
	if strings.TrimSpace(req.WorkDir) == "" {
		return Result{}, fmt.Errorf("crush requires a working directory")
	}
	// Start Crush only for active work. The final concurrent turn retires the
	// local server so idle Crush/LSP/MCP processes do not consume resources.
	a.beginTurn()
	defer a.finishTurn()
	if err := a.ensureServer(ctx); err != nil {
		return Result{}, err
	}

	workspace, err := a.ensureWorkspace(ctx, req.WorkDir)
	if err != nil {
		return Result{}, err
	}
	if err := workspace.acquireTurn(ctx); err != nil {
		return Result{}, err
	}
	defer workspace.releaseTurn()
	model, err := a.selectedModel(req.Model)
	if err != nil {
		return Result{}, err
	}
	if err := a.selectModel(ctx, workspace.id, model); err != nil {
		return Result{}, err
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID, err = a.createSession(ctx, workspace.id)
		if err != nil {
			return Result{}, err
		}
	}

	prompt := buildCrushPrompt(req, req.SessionID == "")
	runID, err := newCrushID()
	if err != nil {
		return Result{}, err
	}
	run := &crushRun{sessionID: sessionID, callback: cb, input: req.UserInputHandler, done: make(chan crushRunComplete, 1)}
	workspace.mu.Lock()
	workspace.runs[runID] = run
	workspace.mu.Unlock()
	defer func() {
		workspace.mu.Lock()
		delete(workspace.runs, runID)
		workspace.mu.Unlock()
	}()

	cb(StreamEvent{Type: EventSessionInfo, Metadata: map[string]interface{}{
		"model": model.ID, "cwd": req.WorkDir, "provider": model.Provider,
	}})
	if err := a.sendPrompt(ctx, workspace.id, sessionID, runID, prompt); err != nil {
		return Result{SessionID: sessionID}, err
	}

	select {
	case complete := <-run.done:
		// RunComplete is Crush's authoritative terminal event and carries the
		// full response text. SSE message updates normally stream the same
		// text first; append only the suffix that was not observed so a missed
		// or differently-shaped update never leaves Nexbox with an empty reply.
		workspace.mu.Lock()
		seen := workspace.messageState[sessionID].text
		workspace.mu.Unlock()
		if delta := strings.TrimPrefix(complete.text, seen); delta != "" {
			cb(StreamEvent{Type: EventTextDelta, Content: delta})
		}
		if complete.err != "" {
			return Result{SessionID: sessionID}, errors.New(complete.err)
		}
		if complete.cancelled {
			return Result{SessionID: sessionID}, context.Canceled
		}
		cb(StreamEvent{Type: EventDone, SessionID: sessionID, Metadata: map[string]interface{}{
			"model": model.ID, "provider": model.Provider,
		}})
		return Result{SessionID: sessionID}, nil
	case <-ctx.Done():
		cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.cancelSession(cancelCtx, workspace.id, sessionID); err != nil && a.Verbose {
			log.Printf("[crush-agent] cancel session %s: %v", sessionID, err)
		}
		return Result{SessionID: sessionID}, ctx.Err()
	}
}

func (a *CrushAgent) selectedModel(requested string) (crushModel, error) {
	model, err := parseCrushModel(requested)
	if err != nil {
		return crushModel{}, err
	}
	return model, nil
}

type crushConfiguredProvider struct {
	ID      string             `json:"id"`
	Disable bool               `json:"disable"`
	Models  []crushModelConfig `json:"models"`
}

// crushNativeConfig is intentionally limited to the model catalog fields.
// Crush's complete config also contains credentials, which Nexbox neither
// needs nor stores.
type crushNativeConfig struct {
	Providers map[string]crushConfiguredProvider `json:"providers"`
}

type crushModelConfig struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextWindow int64  `json:"context_window"`
}

func configuredCrushModels(providers map[string]crushConfiguredProvider) []ModelInfo {
	models := make([]ModelInfo, 0)
	seen := make(map[string]bool)
	providerKeys := make([]string, 0, len(providers))
	for key := range providers {
		providerKeys = append(providerKeys, key)
	}
	sort.Strings(providerKeys)
	for _, key := range providerKeys {
		provider := providers[key]
		if provider.Disable {
			continue
		}
		providerID := strings.TrimSpace(provider.ID)
		if providerID == "" {
			providerID = strings.TrimSpace(key)
		}
		for _, configured := range provider.Models {
			modelID := strings.TrimSpace(configured.ID)
			if providerID == "" || modelID == "" {
				continue
			}
			id := providerID + "/" + modelID
			if seen[id] {
				continue
			}
			seen[id] = true
			label := strings.TrimSpace(configured.Name)
			if label == "" {
				label = modelID
			}
			models = append(models, ModelInfo{ID: id, Label: label, ContextWindow: int(configured.ContextWindow)})
		}
	}
	return models
}

func parseCrushModel(identifier string) (crushModel, error) {
	identifier = strings.TrimSpace(identifier)
	provider, modelID, ok := strings.Cut(identifier, "/")
	if !ok || strings.TrimSpace(provider) == "" || strings.TrimSpace(modelID) == "" {
		if identifier == "" {
			return crushModel{}, fmt.Errorf("select a Crush model before sending a message")
		}
		return crushModel{}, fmt.Errorf("invalid Crush model %q", identifier)
	}
	return crushModel{Provider: provider, ID: modelID}, nil
}

func buildCrushPrompt(req Request, isNewSession bool) string {
	return BuildPrompt(req.StaticContext, req.DynamicContext, req.Message, isNewSession, req.Images)
}

func (a *CrushAgent) ensureServer(ctx context.Context) error {
	a.serverMu.Lock()
	defer a.serverMu.Unlock()
	if a.serverStarted {
		return nil
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("allocate crush server port: %w", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return fmt.Errorf("release crush server port: %w", err)
	}

	args := []string{"server", "--host", "tcp://" + address}
	if strings.TrimSpace(a.DataDir) != "" {
		args = append(args, "--data-dir", a.DataDir)
	}
	cmd := exec.Command(a.CLIPath, args...)
	cmd.Env = crushProcessEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start crush server: %w", err)
	}

	serverURL := "http://" + address
	httpClient := &http.Client{}
	a.serverURL = serverURL
	a.httpClient = httpClient
	a.serverCmd = cmd
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := crushHealth(probeCtx, serverURL, httpClient)
		cancel()
		if err == nil {
			a.serverStarted = true
			return nil
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return fmt.Errorf("start crush server: did not become healthy: %s", strings.TrimSpace(stderr.String()))
}

func crushProcessEnv() []string {
	return append([]string(nil), os.Environ()...)
}

func (a *CrushAgent) beginTurn() {
	a.lifecycleMu.Lock()
	a.activeTurns++
	a.lifecycleMu.Unlock()
}

func (a *CrushAgent) finishTurn() {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if a.activeTurns == 0 {
		return
	}
	a.activeTurns--
	if a.activeTurns == 0 {
		a.closeResources()
	}
}

// Close releases local Crush state.
func (a *CrushAgent) Close() {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	a.closeResources()
}

// closeResources must be called while lifecycleMu is held.
func (a *CrushAgent) closeResources() {
	a.workspacesMu.Lock()
	for _, workspace := range a.workspaces {
		workspace.cancel()
	}
	a.workspaces = make(map[string]*crushWorkspace)
	a.workspacesMu.Unlock()

	a.serverMu.Lock()
	defer a.serverMu.Unlock()
	cmd := a.serverCmd
	a.serverCmd = nil
	a.serverStarted = false
	a.serverURL = ""
	a.httpClient = nil
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

func crushHealth(ctx context.Context, serverURL string, httpClient *http.Client) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/v1/health", nil)
	if err != nil {
		return err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected health status %s", response.Status)
	}
	return nil
}

// serverConnection returns a stable connection for one request. Callers may
// outlive a turn (for example after an interactive question is cancelled), so
// they must not read serverURL or httpClient directly while Close can release
// them.
func (a *CrushAgent) serverConnection() (string, *http.Client, error) {
	a.serverMu.Lock()
	defer a.serverMu.Unlock()
	if a.serverURL == "" || a.httpClient == nil {
		return "", nil, errors.New("Crush server is not running")
	}
	return a.serverURL, a.httpClient, nil
}

func (a *CrushAgent) getJSON(ctx context.Context, path string, target interface{}) error {
	serverURL, httpClient, err := a.serverConnection()
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+path, nil)
	if err != nil {
		return err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: unexpected status %s", path, response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode GET %s: %w", path, err)
	}
	return nil
}

func (a *CrushAgent) deleteWorkspace(workspaceID string) {
	if strings.TrimSpace(workspaceID) == "" {
		return
	}
	serverURL, httpClient, err := a.serverConnection()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, serverURL+"/v1/workspaces/"+url.PathEscape(workspaceID), nil)
	if err != nil {
		return
	}
	response, err := httpClient.Do(request)
	if err == nil && response != nil {
		response.Body.Close()
	}
}

func (a *CrushAgent) ensureWorkspace(ctx context.Context, workDir string) (*crushWorkspace, error) {
	clean := filepath.Clean(workDir)
	a.workspacesMu.Lock()
	if existing := a.workspaces[clean]; existing != nil {
		a.workspacesMu.Unlock()
		return existing, nil
	}
	a.workspacesMu.Unlock()

	clientID, err := newCrushID()
	if err != nil {
		return nil, err
	}
	dataDir, err := a.workspaceDataDir(clean)
	if err != nil {
		return nil, err
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := a.postJSON(ctx, "/v1/workspaces", nil, map[string]interface{}{
		"path": clean, "data_dir": dataDir, "client_id": clientID, "yolo": true,
	}, http.StatusOK, &created); err != nil {
		return nil, fmt.Errorf("create crush workspace: %w", err)
	}
	if created.ID == "" {
		return nil, fmt.Errorf("create crush workspace returned an empty id")
	}

	workspaceCtx, cancel := context.WithCancel(context.Background())
	workspace := &crushWorkspace{
		id: created.ID, path: clean, clientID: clientID, ctx: workspaceCtx, cancel: cancel,
		runs: make(map[string]*crushRun), messageState: make(map[string]crushMessageState), eventsReady: make(chan error, 1), turnSlot: make(chan struct{}, 1),
	}
	workspace.turnSlot <- struct{}{}
	a.workspacesMu.Lock()
	if existing := a.workspaces[clean]; existing != nil {
		a.workspacesMu.Unlock()
		cancel()
		return existing, nil
	}
	a.workspaces[clean] = workspace
	a.workspacesMu.Unlock()
	go a.streamWorkspaceEvents(workspace)
	select {
	case err := <-workspace.eventsReady:
		if err == nil {
			return workspace, nil
		}
		a.workspacesMu.Lock()
		delete(a.workspaces, clean)
		a.workspacesMu.Unlock()
		cancel()
		return nil, fmt.Errorf("connect crush workspace events: %w", err)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// workspaceDataDir keeps Crush's mutable project state under Nexbox's own
// data directory. Passing it explicitly avoids Crush's default .crush folder
// in the user's repository while preserving state across server restarts.
func (a *CrushAgent) workspaceDataDir(workDir string) (string, error) {
	root := strings.TrimSpace(a.DataDir)
	if root == "" {
		return "", fmt.Errorf("crush data directory is not configured")
	}
	sum := sha256.Sum256([]byte(filepath.Clean(workDir)))
	return filepath.Join(root, "workspaces", hex.EncodeToString(sum[:])), nil
}

// Crush selects the model at workspace scope. Serializing a workspace's
// turns prevents another session in the same working directory from changing
// that selection between selectModel and sendPrompt.
func (w *crushWorkspace) acquireTurn(ctx context.Context) error {
	select {
	case <-w.turnSlot:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *crushWorkspace) releaseTurn() {
	w.turnSlot <- struct{}{}
}

func (a *CrushAgent) selectModel(ctx context.Context, workspaceID string, model crushModel) error {
	body := map[string]interface{}{
		"scope":      1, // Crush workspace scope; avoids mutating global user config.
		"model_type": "large",
		"model": map[string]string{
			"provider": model.Provider,
			"model":    model.ID,
		},
	}
	if err := a.postJSON(ctx, "/v1/workspaces/"+url.PathEscape(workspaceID)+"/config/model", nil, body, http.StatusOK, nil); err != nil {
		return fmt.Errorf("select crush model %s: %w", model.ID, err)
	}
	return nil
}

func (a *CrushAgent) createSession(ctx context.Context, workspaceID string) (string, error) {
	var created struct {
		ID string `json:"id"`
	}
	if err := a.postJSON(ctx, "/v1/workspaces/"+url.PathEscape(workspaceID)+"/sessions", nil, map[string]string{"title": ""}, http.StatusOK, &created); err != nil {
		return "", fmt.Errorf("create crush session: %w", err)
	}
	if created.ID == "" {
		return "", fmt.Errorf("create crush session returned an empty id")
	}
	return created.ID, nil
}

func (a *CrushAgent) sendPrompt(ctx context.Context, workspaceID, sessionID, runID, prompt string) error {
	body := map[string]string{"session_id": sessionID, "run_id": runID, "prompt": prompt}
	if err := a.postJSON(ctx, "/v1/workspaces/"+url.PathEscape(workspaceID)+"/agent", nil, body, http.StatusAccepted, nil); err != nil {
		return fmt.Errorf("send crush prompt: %w", err)
	}
	return nil
}

func (a *CrushAgent) cancelSession(ctx context.Context, workspaceID, sessionID string) error {
	return a.postJSON(ctx, "/v1/workspaces/"+url.PathEscape(workspaceID)+"/agent/sessions/"+url.PathEscape(sessionID)+"/cancel", nil, nil, http.StatusOK, nil)
}

func (a *CrushAgent) streamWorkspaceEvents(workspace *crushWorkspace) {
	first := true
	notifyReady := func(err error) {
		if !first {
			return
		}
		first = false
		select {
		case workspace.eventsReady <- err:
		default:
		}
	}
	for workspace.ctx.Err() == nil {
		err := a.consumeWorkspaceEvents(workspace, func() { notifyReady(nil) })
		notifyReady(err)
		if workspace.ctx.Err() != nil {
			return
		}
		if a.Verbose && err != nil {
			log.Printf("[crush-agent] workspace event stream: %v; reconnecting", err)
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-workspace.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (a *CrushAgent) consumeWorkspaceEvents(workspace *crushWorkspace, connected func()) error {
	query := url.Values{"client_id": []string{workspace.clientID}}
	serverURL, httpClient, err := a.serverConnection()
	if err != nil {
		return err
	}
	endpoint := serverURL + "/v1/workspaces/" + url.PathEscape(workspace.id) + "/events?" + query.Encode()
	request, err := http.NewRequestWithContext(workspace.ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", response.Status)
	}
	connected()

	scanner := bufio.NewScanner(response.Body)
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		a.handleWorkspaceEvent(workspace, []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))))
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}

type crushEventEnvelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type crushEvent[T any] struct {
	Type    string `json:"type"`
	Payload T      `json:"payload"`
}

type crushWireMessage struct {
	SessionID string            `json:"session_id"`
	Role      string            `json:"role"`
	Parts     []json.RawMessage `json:"parts"`
}

type crushWirePart struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type crushWirePartData struct {
	Text       string `json:"text"`
	Thinking   string `json:"thinking"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	Input      string `json:"input"`
	Finished   bool   `json:"finished"`
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error"`
}

func (a *CrushAgent) handleWorkspaceEvent(workspace *crushWorkspace, raw []byte) {
	var envelope crushEventEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return
	}
	switch envelope.Type {
	case "message":
		var event crushEvent[crushWireMessage]
		if json.Unmarshal(envelope.Payload, &event) == nil {
			a.handleCrushMessage(workspace, event.Payload)
		}
	case "run_complete":
		var event crushEvent[crushRunComplete]
		if json.Unmarshal(envelope.Payload, &event) == nil {
			workspace.mu.Lock()
			run := workspace.runs[event.Payload.runID]
			workspace.mu.Unlock()
			if run != nil {
				select {
				case run.done <- event.Payload:
				default:
				}
			}
		}
	case "permission_request":
		var event crushEvent[json.RawMessage]
		if json.Unmarshal(envelope.Payload, &event) == nil {
			go a.handleCrushPermission(workspace, event.Payload)
		}
	case "question_batch_request":
		var event crushEvent[json.RawMessage]
		if json.Unmarshal(envelope.Payload, &event) == nil {
			go a.handleCrushQuestion(workspace, event.Payload)
		}
	case "agent_event":
		var event crushEvent[crushAgentEvent]
		if json.Unmarshal(envelope.Payload, &event) == nil {
			a.handleCrushAgentEvent(workspace, event.Payload)
		}
	}
}

// Wire messages do not carry RunID; the terminal run_complete event does.
// Keeping it separate makes terminal correlation explicit.
func (c *crushRunComplete) UnmarshalJSON(data []byte) error {
	type wire struct {
		SessionID string `json:"session_id"`
		RunID     string `json:"run_id"`
		Text      string `json:"text"`
		Error     string `json:"error"`
		Cancelled bool   `json:"cancelled"`
	}
	var value wire
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	c.sessionID, c.runID, c.text, c.err, c.cancelled = value.SessionID, value.RunID, value.Text, value.Error, value.Cancelled
	return nil
}

func (a *CrushAgent) handleCrushMessage(workspace *crushWorkspace, message crushWireMessage) {
	if message.SessionID == "" || (message.Role != "assistant" && message.Role != "tool") {
		return
	}
	workspace.mu.Lock()
	state := workspace.messageState[message.SessionID]
	var callback EventCallback
	for _, run := range workspace.runs {
		if run.sessionID == message.SessionID {
			callback = run.callback
			break
		}
	}
	workspace.mu.Unlock()
	if callback == nil {
		return
	}
	if state.toolCalls == nil {
		state.toolCalls = make(map[string]bool)
	}
	if state.toolDone == nil {
		state.toolDone = make(map[string]bool)
	}
	var text, thinking string
	for _, rawPart := range message.Parts {
		var part crushWirePart
		if json.Unmarshal(rawPart, &part) != nil || len(part.Data) == 0 {
			continue
		}
		var data crushWirePartData
		if json.Unmarshal(part.Data, &data) != nil {
			continue
		}
		switch part.Type {
		case "text":
			if message.Role == "assistant" {
				text += data.Text
			}
		case "reasoning":
			if message.Role == "assistant" {
				thinking += data.Thinking
			}
		case "tool_call":
			if data.ID != "" && data.Finished && !state.toolCalls[data.ID] {
				state.toolCalls[data.ID] = true
				callback(StreamEvent{Type: EventToolStart, ToolID: data.ID, ToolName: data.Name, Input: data.Input})
			}
		case "tool_result":
			if data.ToolCallID != "" && !state.toolDone[data.ToolCallID] {
				state.toolDone[data.ToolCallID] = true
				callback(StreamEvent{Type: EventToolResult, ToolID: data.ToolCallID, ToolName: data.Name, Output: data.Content, IsError: data.IsError})
			}
		}
	}
	if message.Role == "assistant" {
		if delta := strings.TrimPrefix(text, state.text); delta != "" {
			callback(StreamEvent{Type: EventTextDelta, Content: delta})
		}
		if delta := strings.TrimPrefix(thinking, state.thinking); delta != "" {
			callback(StreamEvent{Type: EventThinking, Content: delta})
		}
		state.text, state.thinking = text, thinking
	}
	workspace.mu.Lock()
	workspace.messageState[message.SessionID] = state
	workspace.mu.Unlock()
}

func (a *CrushAgent) handleCrushPermission(workspace *crushWorkspace, raw json.RawMessage) {
	var request crushPermissionRequest
	if json.Unmarshal(raw, &request) != nil || request.ID == "" {
		return
	}
	run := workspace.runForSession(request.SessionID)
	action := "deny"
	if run != nil && run.input != nil {
		title, input := crushPermissionInput(request)
		if answer, err := run.input(request.ID, "CrushPermission", input); err == nil {
			action = crushPermissionAction(answer, title)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = a.postJSON(ctx, "/v1/workspaces/"+url.PathEscape(workspace.id)+"/permissions/grant", nil, map[string]interface{}{"permission": json.RawMessage(raw), "action": action}, http.StatusOK, nil)
}

type crushPermissionRequest struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Path        string `json:"path"`
}

// crushPermissionInput adapts Crush's three-valued permission protocol to the
// same structured choice card used by ZeroClaw. It must not expose the raw
// protocol envelope as an arbitrary user question.
func crushPermissionInput(request crushPermissionRequest) (string, string) {
	action := strings.TrimSpace(request.Action)
	if action == "" {
		action = "tool action"
	}
	title := "Allow " + action + "?"
	description := strings.TrimSpace(request.Description)
	if description == "" {
		description = "Crush requests permission to use " + firstNonEmpty(request.ToolName, "a tool") + "."
	}
	if path := strings.TrimSpace(request.Path); path != "" && !strings.Contains(description, path) {
		description += "\n" + path
	}
	input := map[string]interface{}{
		"question": title,
		"questions": []map[string]interface{}{
			{
				"question":    title,
				"header":      description,
				"multiSelect": false,
				"options": []map[string]string{
					{"label": "Allow once", "description": "Allow only this operation"},
					{"label": "Allow for this turn", "description": "Allow matching requests during this response"},
					{"label": "Deny", "description": "Do not perform this operation"},
				},
			},
		},
	}
	encoded, _ := json.Marshal(input)
	return title, string(encoded)
}

func crushPermissionAction(response, title string) string {
	answer := strings.TrimSpace(response)
	var structured struct {
		Answers map[string]string `json:"answers"`
	}
	if json.Unmarshal([]byte(response), &structured) == nil {
		answer = strings.TrimSpace(structured.Answers[title])
	}
	switch strings.ToLower(answer) {
	case "allow", "allow once", "approve", "yes":
		return "allow"
	case "always", "allow_session", "allow for this turn", "allow for this session":
		return "allow_session"
	default:
		return "deny"
	}
}

func (w *crushWorkspace) runForSession(sessionID string) *crushRun {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, run := range w.runs {
		if run.sessionID == sessionID {
			return run
		}
	}
	return nil
}

type crushQuestionRequest struct {
	ID                 string              `json:"id"`
	SessionID          string              `json:"session_id"`
	ToolCallID         string              `json:"tool_call_id"`
	Questions          []crushQuestionItem `json:"questions"`
	ConfirmTitle       string              `json:"confirm_title"`
	ConfirmDescription string              `json:"confirm_description"`
}

type crushQuestionItem struct {
	ID          string                `json:"id"`
	Type        string                `json:"type"`
	Label       string                `json:"label"`
	Question    string                `json:"question"`
	Description string                `json:"description"`
	Choices     []crushQuestionChoice `json:"choices"`
}

type crushQuestionChoice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type crushQuestionAnswer struct {
	QuestionID  string   `json:"request_id"`
	SelectedIDs []string `json:"selected_ids,omitempty"`
	FillInText  string   `json:"fill_in_text,omitempty"`
	Yes         *bool    `json:"yes,omitempty"`
}

func (a *CrushAgent) handleCrushQuestion(workspace *crushWorkspace, raw json.RawMessage) {
	var request crushQuestionRequest
	if json.Unmarshal(raw, &request) != nil || request.ID == "" || request.SessionID == "" || len(request.Questions) == 0 {
		return
	}
	run := workspace.runForSession(request.SessionID)
	if run == nil || run.input == nil {
		a.cancelCrushQuestion(workspace.id)
		return
	}
	_, input := crushQuestionInput(request)
	response, err := run.input(request.ID, "CrushQuestion", input)
	if err != nil {
		a.cancelCrushQuestion(workspace.id)
		return
	}
	answers := crushQuestionAnswers(response, request)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = a.postJSON(ctx, "/v1/workspaces/"+url.PathEscape(workspace.id)+"/questions/answer", nil, map[string]interface{}{
		"batch_request_id": request.ID,
		"responses":        answers,
	}, http.StatusOK, nil)
}

func (a *CrushAgent) cancelCrushQuestion(workspaceID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = a.postJSON(ctx, "/v1/workspaces/"+url.PathEscape(workspaceID)+"/questions/cancel", nil, nil, http.StatusOK, nil)
}

func crushQuestionInput(request crushQuestionRequest) (string, string) {
	title := firstNonEmpty(strings.TrimSpace(request.ConfirmTitle), "Crush needs your input")
	questions := make([]map[string]interface{}, 0, len(request.Questions))
	for _, question := range request.Questions {
		header := strings.TrimSpace(question.Question)
		if description := strings.TrimSpace(question.Description); description != "" {
			header += "\n" + description
		}
		options := make([]map[string]string, 0, len(question.Choices))
		for _, choice := range question.Choices {
			options = append(options, map[string]string{"label": choice.Label, "description": choice.Description})
		}
		if question.Type == "yes_no" {
			options = []map[string]string{{"label": "Yes"}, {"label": "No"}}
		}
		questions = append(questions, map[string]interface{}{
			"question":    question.ID,
			"header":      header,
			"multiSelect": question.Type == "multi_choice",
			"options":     options,
		})
	}
	input, _ := json.Marshal(map[string]interface{}{"question": title, "questions": questions})
	return title, string(input)
}

func crushQuestionAnswers(response string, request crushQuestionRequest) []crushQuestionAnswer {
	var structured struct {
		Answers map[string]string `json:"answers"`
	}
	_ = json.Unmarshal([]byte(response), &structured)
	if structured.Answers == nil && len(request.Questions) == 1 {
		structured.Answers = map[string]string{request.Questions[0].ID: strings.TrimSpace(response)}
	}
	answers := make([]crushQuestionAnswer, 0, len(request.Questions))
	for _, question := range request.Questions {
		answer := strings.TrimSpace(structured.Answers[question.ID])
		resolved := crushQuestionAnswer{QuestionID: question.ID}
		switch question.Type {
		case "yes_no":
			if strings.EqualFold(answer, "yes") {
				yes := true
				resolved.Yes = &yes
			} else if strings.EqualFold(answer, "no") {
				yes := false
				resolved.Yes = &yes
			} else {
				resolved.FillInText = answer
			}
		case "free_text":
			resolved.FillInText = answer
		default:
			unmatched := make([]string, 0)
			for _, selection := range strings.Split(answer, ", ") {
				selection = strings.TrimSpace(selection)
				if selection == "" {
					continue
				}
				matched := false
				for _, choice := range question.Choices {
					if selection == choice.Label {
						resolved.SelectedIDs = append(resolved.SelectedIDs, choice.ID)
						matched = true
						break
					}
				}
				if !matched {
					unmatched = append(unmatched, selection)
				}
			}
			resolved.FillInText = strings.Join(unmatched, ", ")
		}
		answers = append(answers, resolved)
	}
	return answers
}

type crushAgentEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	RunID     string `json:"run_id"`
	Error     string `json:"error"`
}

func (a *CrushAgent) handleCrushAgentEvent(workspace *crushWorkspace, event crushAgentEvent) {
	if event.Type != "error" || strings.TrimSpace(event.Error) == "" {
		return
	}
	var run *crushRun
	workspace.mu.Lock()
	if event.RunID != "" {
		run = workspace.runs[event.RunID]
	}
	if run == nil {
		for _, candidate := range workspace.runs {
			if candidate.sessionID == event.SessionID {
				run = candidate
				break
			}
		}
	}
	workspace.mu.Unlock()
	if run == nil {
		return
	}
	select {
	case run.done <- crushRunComplete{sessionID: event.SessionID, runID: event.RunID, err: event.Error}:
	default:
	}
}

func (a *CrushAgent) postJSON(ctx context.Context, path string, query url.Values, payload interface{}, expectedStatus int, destination interface{}) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	serverURL, httpClient, err := a.serverConnection()
	if err != nil {
		return err
	}
	endpoint := serverURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("unexpected status %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	if destination != nil {
		if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
			return err
		}
	}
	return nil
}

func newCrushID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(bytes)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}
