package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ClaudeInitResponse mirrors the payload of a successful `initialize`
// control_response from the Claude CLI. The shape is dictated by the CLI's
// `S1J` zod schema (see reverse-engineered binary strings): it is the response
// to a `control_request{subtype:"initialize"}` sent via stream-json stdin.
type ClaudeInitResponse struct {
	OutputStyle           string                  `json:"output_style"`
	AvailableOutputStyles []string                `json:"available_output_styles"`
	Commands              []map[string]any        `json:"commands"`
	Agents                []map[string]any        `json:"agents"`
	Models                []ClaudeInitModel       `json:"models"`
	Account               *ClaudeInitAccount      `json:"account,omitempty"`
	PID                   int                     `json:"pid,omitempty"`
	FastModeState         string                  `json:"fast_mode_state,omitempty"`
	Extra                 map[string]any          `json:"-"` // unknown keys, preserved for forward-compat
}

// ClaudeInitModel matches the `lg7` schema (AvailableModel). Effort support is
// carried by `SupportsEffort + SupportedEffortLevels`; when SupportsEffort is
// false or the fields are absent, the CLI does not accept `--effort` for this
// model.
type ClaudeInitModel struct {
	Value                   string   `json:"value"`
	DisplayName             string   `json:"displayName"`
	Description             string   `json:"description"`
	SupportsEffort          bool     `json:"supportsEffort,omitempty"`
	SupportedEffortLevels   []string `json:"supportedEffortLevels,omitempty"`
	SupportsAdaptiveThinking bool    `json:"supportsAdaptiveThinking,omitempty"`
	SupportsFastMode        bool     `json:"supportsFastMode,omitempty"`
	SupportsAutoMode        bool     `json:"supportsAutoMode,omitempty"`
}

type ClaudeInitAccount struct {
	Email            string `json:"email,omitempty"`
	Organization     string `json:"organization,omitempty"`
	SubscriptionType string `json:"subscriptionType,omitempty"`
	TokenSource      string `json:"tokenSource,omitempty"`
	APIKeySource     string `json:"apiKeySource,omitempty"`
	APIProvider      string `json:"apiProvider,omitempty"`
}

// ProbeInitialize spawns a short-lived `claude -p` subprocess, sends the
// `initialize` control_request handshake, captures the first matching
// control_response, and returns the parsed init payload. It then kills the
// subprocess so the probe is cheap.
//
// Measured cost: ~3.5s cold for the handshake with a trivial `--model`
// argument. We intentionally send a no-op user message so the CLI stops
// blocking on stdin immediately after emitting the init response.
func (c *ClaudeClient) ProbeInitialize(ctx context.Context) (*ClaudeInitResponse, error) {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--permission-mode", "bypassPermissions",
	}

	// Give the probe its own deadline so a stuck CLI can't hang server startup.
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, c.CLIPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	env := append([]string{}, defaultClaudeEnv(c.Env)...)
	cmd.Env = env

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start probe: %w", err)
	}
	defer func() {
		// Force-kill the whole process group so background workers don't linger.
		if cmd.Process != nil {
			if pgid, perr := syscall.Getpgid(cmd.Process.Pid); perr == nil {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			} else {
				_ = cmd.Process.Kill()
			}
		}
		_ = cmd.Wait()
	}()

	// Send initialize + a no-op user message so the CLI commits stdin quickly.
	const reqID = "nexbox-init-probe"
	initMsg := map[string]any{
		"type":       "control_request",
		"request_id": reqID,
		"request":    map[string]any{"subtype": "initialize"},
	}
	userMsg := map[string]any{
		"type":       "user",
		"message":    map[string]any{"role": "user", "content": "noop"},
		"session_id": "nexbox-probe",
	}
	for _, m := range []map[string]any{initMsg, userMsg} {
		data, _ := json.Marshal(m)
		if _, werr := stdinPipe.Write(append(data, '\n')); werr != nil {
			return nil, fmt.Errorf("write probe stdin: %w", werr)
		}
	}

	reader := bufio.NewReader(stdoutPipe)
	for {
		line, rerr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var msg struct {
				Type     string          `json:"type"`
				Response json.RawMessage `json:"response"`
			}
			if json.Unmarshal(line, &msg) == nil && msg.Type == "control_response" && len(msg.Response) > 0 {
				var inner struct {
					Subtype   string          `json:"subtype"`
					RequestID string          `json:"request_id"`
					Response  json.RawMessage `json:"response"`
					Error     string          `json:"error,omitempty"`
				}
				if json.Unmarshal(msg.Response, &inner) != nil {
					continue
				}
				if inner.RequestID != reqID {
					continue
				}
				if inner.Subtype != "success" {
					return nil, fmt.Errorf("init probe rejected: %s", inner.Error)
				}
				var out ClaudeInitResponse
				if err := json.Unmarshal(inner.Response, &out); err != nil {
					return nil, fmt.Errorf("parse init payload: %w", err)
				}
				return &out, nil
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil, fmt.Errorf("probe exited before init response (stderr: %s)", stderrBuf.String())
			}
			return nil, fmt.Errorf("read probe stdout: %w", rerr)
		}
	}
}

// defaultClaudeEnv returns the process env plus any extras the ClaudeClient
// was configured with. Mirrors the env-merging done in Query() so the probe
// uses the same TMPDIR/etc overrides.
func defaultClaudeEnv(extra map[string]string) []string {
	base := append([]string{}, os.Environ()...)
	for k, v := range extra {
		base = append(base, k+"="+v)
	}
	return base
}
