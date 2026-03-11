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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// codexRPCError is a JSON-RPC method failure from codex app-server.
type codexRPCError struct {
	Method  string
	Code    int
	Message string
	Data    json.RawMessage
}

func (e *codexRPCError) Error() string {
	if e == nil {
		return "codex rpc error"
	}
	if e.Method != "" {
		return fmt.Sprintf("%s failed (%d): %s", e.Method, e.Code, e.Message)
	}
	return fmt.Sprintf("codex rpc failed (%d): %s", e.Code, e.Message)
}

// codexWireError is the JSON-RPC error payload on the wire.
type codexWireError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// codexWireMessage is a lenient JSON-RPC envelope for parsing inbound lines.
type codexWireMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *codexWireError `json:"error,omitempty"`
}

type codexResponse struct {
	IDKey  string
	Result json.RawMessage
	Error  *codexWireError
}

// CodexServerRequestHandler handles server-initiated requests that require
// a response from the client.
type CodexServerRequestHandler func(method string, params json.RawMessage) (result interface{}, rpcErr *codexWireError)

// CodexNotificationHandler handles server notifications.
type CodexNotificationHandler func(method string, params json.RawMessage)

// CodexAppServerClient wraps a codex app-server subprocess over stdio JSON-RPC.
type CodexAppServerClient struct {
	CLIPath string
	Args    []string
	Verbose bool

	cmd   *exec.Cmd
	stdin io.WriteCloser

	nextRequestID int64

	pendingMu sync.Mutex
	pending   map[string]chan codexResponse

	writeMu sync.Mutex

	readDone chan struct{}
	readErr  error

	clientDone chan struct{}

	stderrMu  sync.Mutex
	stderrBuf []byte

	wg sync.WaitGroup

	notificationHandler CodexNotificationHandler
	requestHandler      CodexServerRequestHandler

	closeOnce sync.Once
}

// NewCodexAppServerClient creates a client for `codex app-server`.
func NewCodexAppServerClient(cliPath string, args []string) *CodexAppServerClient {
	if cliPath == "" {
		cliPath = "codex"
	}
	return &CodexAppServerClient{
		CLIPath:       cliPath,
		Args:          args,
		pending:       make(map[string]chan codexResponse),
		readDone:      make(chan struct{}),
		clientDone:    make(chan struct{}),
		nextRequestID: 0,
	}
}

func (c *CodexAppServerClient) SetNotificationHandler(h CodexNotificationHandler) {
	c.notificationHandler = h
}

func (c *CodexAppServerClient) SetServerRequestHandler(h CodexServerRequestHandler) {
	c.requestHandler = h
}

// Start launches the codex app-server subprocess and begins streaming reads.
func (c *CodexAppServerClient) Start(ctx context.Context, workDir string) error {
	cmd := exec.Command(c.CLIPath, c.Args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create codex stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create codex stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("create codex stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start codex app-server: %w", err)
	}

	c.cmd = cmd
	c.stdin = stdin

	// Context cancellation force-kills the full process group.
	go func() {
		select {
		case <-ctx.Done():
			c.killProcessGroup()
		case <-c.clientDone:
		}
	}()

	// Drain stderr in background for diagnostics.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		buf, _ := io.ReadAll(stderr)
		if len(buf) > 0 {
			c.stderrMu.Lock()
			c.stderrBuf = append(c.stderrBuf, buf...)
			c.stderrMu.Unlock()
			if c.Verbose {
				log.Printf("[codex-cli] stderr: %s", string(buf))
			}
		}
	}()

	// Start stdout read loop.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.readLoop(stdout)
	}()

	return nil
}

// Initialize performs initialize request + initialized notification handshake.
func (c *CodexAppServerClient) Initialize(ctx context.Context, clientName, clientVersion string) error {
	if clientName == "" {
		clientName = "nexbox"
	}
	if clientVersion == "" {
		clientVersion = "unknown"
	}

	params := map[string]interface{}{
		"clientInfo": map[string]interface{}{
			"name":    clientName,
			"title":   "NexBox",
			"version": clientVersion,
		},
		"capabilities": map[string]interface{}{
			"experimentalApi": true,
		},
	}

	var initResp map[string]interface{}
	if err := c.Request(ctx, "initialize", params, &initResp); err != nil {
		return err
	}
	if c.Verbose {
		log.Printf("[codex-cli] initialize ok, keys=%v", mapKeys(initResp))
	}
	if err := c.Notify("initialized", nil); err != nil {
		return fmt.Errorf("send initialized notification: %w", err)
	}
	return nil
}

// Request sends a JSON-RPC request and waits for the matching response.
func (c *CodexAppServerClient) Request(ctx context.Context, method string, params interface{}, out interface{}) error {
	id := atomic.AddInt64(&c.nextRequestID, 1)
	idKey := strconv.FormatInt(id, 10)

	respCh := make(chan codexResponse, 1)
	c.pendingMu.Lock()
	c.pending[idKey] = respCh
	c.pendingMu.Unlock()

	msg := map[string]interface{}{
		"id":     id,
		"method": method,
		"params": params,
	}
	if err := c.writeJSONLine(msg); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, idKey)
		c.pendingMu.Unlock()
		return fmt.Errorf("write request %s: %w", method, err)
	}

	if c.Verbose {
		log.Printf("[codex-cli] -> request method=%s id=%s", method, idKey)
	}

	select {
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, idKey)
		c.pendingMu.Unlock()
		return ctx.Err()
	case <-c.readDone:
		return fmt.Errorf("codex connection closed while waiting for %s: %w", method, c.readErr)
	case resp, ok := <-respCh:
		if !ok {
			return fmt.Errorf("codex connection closed while waiting for %s: %w", method, c.readErr)
		}
		if resp.Error != nil {
			return &codexRPCError{
				Method:  method,
				Code:    resp.Error.Code,
				Message: resp.Error.Message,
				Data:    resp.Error.Data,
			}
		}
		if out != nil {
			if len(resp.Result) == 0 {
				resp.Result = json.RawMessage("null")
			}
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("decode %s response: %w", method, err)
			}
		}
		return nil
	}
}

// Notify sends a JSON-RPC notification.
func (c *CodexAppServerClient) Notify(method string, params interface{}) error {
	msg := map[string]interface{}{"method": method}
	if params != nil {
		msg["params"] = params
	}
	if c.Verbose && codexShouldLogNotifyMethods() {
		log.Printf("[codex-cli] -> notify method=%s", method)
	}
	return c.writeJSONLine(msg)
}

// Close terminates the client process and waits for background readers to stop.
func (c *CodexAppServerClient) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		close(c.clientDone)

		// Close stdin first to request graceful shutdown.
		c.writeMu.Lock()
		if c.stdin != nil {
			_ = c.stdin.Close()
			c.stdin = nil
		}
		c.writeMu.Unlock()

		if c.cmd != nil {
			waitCh := make(chan error, 1)
			go func() {
				waitCh <- c.cmd.Wait()
			}()

			select {
			case err := <-waitCh:
				closeErr = err
			case <-time.After(2 * time.Second):
				c.killProcessGroup()
				closeErr = <-waitCh
			}
		}

		c.wg.Wait()
	})
	return closeErr
}

// Stderr returns buffered stderr output from codex app-server.
func (c *CodexAppServerClient) Stderr() string {
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()
	return string(c.stderrBuf)
}

func (c *CodexAppServerClient) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 20*1024*1024) // 20MB per line

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg codexWireMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			if c.Verbose {
				log.Printf("[codex-cli] skip invalid json line: %v, line=%s", err, string(line))
			}
			continue
		}

		switch {
		case msg.Method != "" && len(msg.ID) > 0:
			c.handleServerRequest(msg.ID, msg.Method, msg.Params)
		case msg.Method != "":
			if c.Verbose && codexShouldLogNotifyMethods() && !codexIsNoisyNotifyMethod(msg.Method) {
				log.Printf("[codex-cli] <- notify method=%s", msg.Method)
			}
			if c.notificationHandler != nil {
				c.notificationHandler(msg.Method, msg.Params)
			}
		case len(msg.ID) > 0 && (len(msg.Result) > 0 || msg.Error != nil):
			idKey := normalizeRequestID(msg.ID)
			c.pendingMu.Lock()
			ch := c.pending[idKey]
			delete(c.pending, idKey)
			c.pendingMu.Unlock()
			if ch != nil {
				ch <- codexResponse{IDKey: idKey, Result: msg.Result, Error: msg.Error}
			}
		default:
			if c.Verbose {
				log.Printf("[codex-cli] <- unknown envelope: %s", string(line))
			}
		}
	}

	if err := scanner.Err(); err != nil {
		c.readErr = fmt.Errorf("read codex stdout: %w", err)
	} else {
		c.readErr = io.EOF
	}

	c.pendingMu.Lock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		close(ch)
	}
	c.pendingMu.Unlock()

	close(c.readDone)
}

func (c *CodexAppServerClient) handleServerRequest(idRaw json.RawMessage, method string, params json.RawMessage) {
	if c.Verbose {
		log.Printf("[codex-cli] <- server request method=%s", method)
	}

	var result interface{} = map[string]interface{}{}
	var rpcErr *codexWireError
	if c.requestHandler != nil {
		result, rpcErr = c.requestHandler(method, params)
		if result == nil && rpcErr == nil {
			result = map[string]interface{}{}
		}
	}

	var idVal interface{}
	if err := json.Unmarshal(idRaw, &idVal); err != nil {
		if c.Verbose {
			log.Printf("[codex-cli] invalid request id: %v", err)
		}
		return
	}

	if rpcErr != nil {
		_ = c.writeJSONLine(map[string]interface{}{
			"id": idVal,
			"error": map[string]interface{}{
				"code":    rpcErr.Code,
				"message": rpcErr.Message,
				"data":    jsonRawToInterface(rpcErr.Data),
			},
		})
		return
	}

	_ = c.writeJSONLine(map[string]interface{}{
		"id":     idVal,
		"result": result,
	})
}

func (c *CodexAppServerClient) writeJSONLine(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if c.Verbose {
		log.Printf("[codex-cli] -> %s", string(data))
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.stdin == nil {
		return fmt.Errorf("stdin is closed")
	}
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func (c *CodexAppServerClient) killProcessGroup() {
	if c.cmd == nil || c.cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(c.cmd.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = c.cmd.Process.Kill()
}

func normalizeRequestID(raw json.RawMessage) string {
	return strings.TrimSpace(string(raw))
}

func jsonRawToInterface(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

func codexIsNoisyNotifyMethod(method string) bool {
	switch method {
	case "item/agentMessage/delta",
		"codex/event/agent_message_delta",
		"codex/event/agent_message_content_delta":
		return true
	default:
		return false
	}
}

func codexShouldLogNotifyMethods() bool {
	return strings.TrimSpace(os.Getenv("NEXBOX_CODEX_LOG_NOTIFY")) == "1"
}
