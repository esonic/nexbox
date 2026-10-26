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

// acpWireError is the JSON-RPC error object used by ACP.
type acpWireError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// acpRPCError reports a failed client-to-agent request.
type acpRPCError struct {
	Method  string
	Code    int
	Message string
	Data    json.RawMessage
}

func (e *acpRPCError) Error() string {
	if e == nil {
		return "ACP request failed"
	}
	return fmt.Sprintf("%s failed (%d): %s", e.Method, e.Code, e.Message)
}

type acpWireMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpWireError   `json:"error,omitempty"`
}

type acpResponse struct {
	Result json.RawMessage
	Error  *acpWireError
}

type acpServerRequestHandler func(method string, params json.RawMessage) (interface{}, *acpWireError)
type acpNotificationHandler func(method string, params json.RawMessage)

// acpStdioClient owns one newline-delimited JSON-RPC subprocess. It is kept
// transport-only so future ACP adapters can reuse it without inheriting any
// ZeroClaw session semantics.
type acpStdioClient struct {
	command string
	args    []string
	verbose bool
	// env is the subprocess environment; nil inherits BeeHub's own.
	env []string

	cmd   *exec.Cmd
	stdin io.WriteCloser

	nextRequestID int64
	pendingMu     sync.Mutex
	pending       map[string]chan acpResponse
	writeMu       sync.Mutex

	readDone chan struct{}
	readErr  error

	stderrMu  sync.Mutex
	stderrBuf []byte
	wg        sync.WaitGroup
	closeOnce sync.Once

	notificationHandler acpNotificationHandler
	requestHandler      acpServerRequestHandler
}

func newACPStdioClient(command string, args []string) *acpStdioClient {
	return &acpStdioClient{
		command:  command,
		args:     append([]string(nil), args...),
		pending:  make(map[string]chan acpResponse),
		readDone: make(chan struct{}),
	}
}

func (c *acpStdioClient) setNotificationHandler(handler acpNotificationHandler) {
	c.notificationHandler = handler
}

func (c *acpStdioClient) setServerRequestHandler(handler acpServerRequestHandler) {
	c.requestHandler = handler
}

func (c *acpStdioClient) start(workDir string) error {
	if strings.TrimSpace(c.command) == "" {
		return fmt.Errorf("ACP command is empty")
	}

	cmd := exec.Command(c.command, c.args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Env = c.env
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create ACP stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create ACP stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("create ACP stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ACP subprocess: %w", err)
	}

	c.cmd = cmd
	c.stdin = stdin

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		buf, _ := io.ReadAll(stderr)
		if len(buf) == 0 {
			return
		}
		c.stderrMu.Lock()
		c.stderrBuf = append(c.stderrBuf, buf...)
		c.stderrMu.Unlock()
		if c.verbose {
			log.Printf("[acp] stderr: %s", string(buf))
		}
	}()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.readLoop(stdout)
	}()

	return nil
}

func (c *acpStdioClient) request(ctx context.Context, method string, params, out interface{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	id := atomic.AddInt64(&c.nextRequestID, 1)
	idKey := strconv.FormatInt(id, 10)
	responseCh := make(chan acpResponse, 1)

	c.pendingMu.Lock()
	c.pending[idKey] = responseCh
	c.pendingMu.Unlock()

	message := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		message["params"] = params
	}
	if err := c.writeJSONLine(message); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, idKey)
		c.pendingMu.Unlock()
		return fmt.Errorf("write ACP request %s: %w", method, err)
	}
	if c.verbose {
		log.Printf("[acp] -> request method=%s id=%s", method, idKey)
	}

	select {
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, idKey)
		c.pendingMu.Unlock()
		return ctx.Err()
	case <-c.readDone:
		return fmt.Errorf("ACP connection closed while waiting for %s: %w", method, c.readErr)
	case response, ok := <-responseCh:
		if !ok {
			return fmt.Errorf("ACP connection closed while waiting for %s: %w", method, c.readErr)
		}
		if response.Error != nil {
			return &acpRPCError{
				Method:  method,
				Code:    response.Error.Code,
				Message: response.Error.Message,
				Data:    response.Error.Data,
			}
		}
		if out == nil {
			return nil
		}
		result := response.Result
		if len(result) == 0 {
			result = json.RawMessage("null")
		}
		if err := json.Unmarshal(result, out); err != nil {
			return fmt.Errorf("decode ACP %s response: %w", method, err)
		}
		return nil
	}
}

func (c *acpStdioClient) notify(method string, params interface{}) error {
	message := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		message["params"] = params
	}
	if c.verbose {
		log.Printf("[acp] -> notify method=%s", method)
	}
	return c.writeJSONLine(message)
}

func (c *acpStdioClient) close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		c.writeMu.Lock()
		if c.stdin != nil {
			_ = c.stdin.Close()
			c.stdin = nil
		}
		c.writeMu.Unlock()

		if c.cmd != nil {
			waitCh := make(chan error, 1)
			go func() { waitCh <- c.cmd.Wait() }()
			select {
			case closeErr = <-waitCh:
			case <-time.After(2 * time.Second):
				c.killProcessGroup()
				closeErr = <-waitCh
			}
		}
		c.wg.Wait()
	})
	return closeErr
}

func (c *acpStdioClient) stderr() string {
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()
	return string(c.stderrBuf)
}

func (c *acpStdioClient) readLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024*1024), 20*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var message acpWireMessage
		if err := json.Unmarshal(line, &message); err != nil {
			if c.verbose {
				log.Printf("[acp] skipping invalid stdout line: %v", err)
			}
			continue
		}

		switch {
		case message.Method != "" && len(message.ID) > 0:
			c.handleServerRequest(message.ID, message.Method, message.Params)
		case message.Method != "":
			if c.notificationHandler != nil {
				c.notificationHandler(message.Method, message.Params)
			}
		case len(message.ID) > 0 && (len(message.Result) > 0 || message.Error != nil):
			key := strings.TrimSpace(string(message.ID))
			c.pendingMu.Lock()
			responseCh := c.pending[key]
			delete(c.pending, key)
			c.pendingMu.Unlock()
			if responseCh != nil {
				responseCh <- acpResponse{Result: message.Result, Error: message.Error}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		c.readErr = fmt.Errorf("read ACP stdout: %w", err)
	} else {
		c.readErr = io.EOF
	}

	c.pendingMu.Lock()
	for key, responseCh := range c.pending {
		delete(c.pending, key)
		close(responseCh)
	}
	c.pendingMu.Unlock()
	close(c.readDone)
}

func (c *acpStdioClient) handleServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	if c.verbose {
		log.Printf("[acp] <- server request method=%s", method)
	}

	var result interface{}
	var rpcErr *acpWireError
	if c.requestHandler == nil {
		rpcErr = &acpWireError{Code: -32601, Message: "method not supported by client"}
	} else {
		result, rpcErr = c.requestHandler(method, params)
		if result == nil && rpcErr == nil {
			result = map[string]interface{}{}
		}
	}

	message := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
	}
	if rpcErr != nil {
		message["error"] = rpcErr
	} else {
		message["result"] = result
	}
	if err := c.writeJSONLine(message); err != nil && c.verbose {
		log.Printf("[acp] failed to answer server request %s: %v", method, err)
	}
}

func (c *acpStdioClient) writeJSONLine(value interface{}) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.stdin == nil {
		return fmt.Errorf("ACP stdin is closed")
	}
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

func (c *acpStdioClient) killProcessGroup() {
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
