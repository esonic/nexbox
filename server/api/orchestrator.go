package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gitlab.okg.com/sre_ai/nexbox/agent"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

// ---------------------------------------------------------------------------
// Execute request / run tracking
// ---------------------------------------------------------------------------

type ExecuteWorkflowRequest struct {
	WorkflowID  string
	ProjectID   string
	UserContext string
	Tasks       []store.Task
	TriggeredBy string // "ui" | "mcp" | "api"
}

type workflowRun struct {
	executionID string
	workflowID  string
	projectID   string
	cancel      context.CancelFunc
	mu          sync.Mutex
	sessionIDs  []string
}

// hookConfig holds the parsed hook settings from a node's config.
type hookConfig struct {
	Trigger string // currently only "on_reply_done"
	Prompt  string
	Reset   bool
}

// hookResult is the JSON response expected from the hook agent.
type hookResult struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// hookOKRe matches `"ok": true` or `"ok": false` in potentially malformed JSON.
var hookOKRe = regexp.MustCompile(`"ok"\s*:\s*(true|false)`)

// hookReasonStartRe finds the start of the reason value (the opening quote).
var hookReasonStartRe = regexp.MustCompile(`"reason"\s*:\s*"`)

// parseHookResult parses the hook agent's JSON response. It first tries strict
// json.Unmarshal; on failure it falls back to regex-extracting both fields so
// that malformed reason strings (e.g. unescaped quotes inside Chinese text) don't
// cause the entire hook evaluation to fail.
func parseHookResult(raw string) (hookResult, error) {
	var hr hookResult
	if err := json.Unmarshal([]byte(raw), &hr); err == nil {
		return hr, nil
	}
	// Fallback: extract the boolean "ok" field.
	m := hookOKRe.FindStringSubmatch(raw)
	if m == nil {
		return hr, fmt.Errorf("no \"ok\" field found in hook response (raw: %q)", raw)
	}
	hr.OK = m[1] == "true"
	// Extract the reason text. The value starts after `"reason": "` and ends
	// just before the last `"` in the string (which closes the JSON object).
	// This correctly handles unescaped inner quotes like: "reason": "以"你好"开头"}
	if loc := hookReasonStartRe.FindStringIndex(raw); loc != nil {
		rest := raw[loc[1]:] // everything after the opening quote
		if end := strings.LastIndex(rest, "\""); end > 0 {
			hr.Reason = rest[:end]
		}
	}
	log.Printf("[workflow] Hook JSON was malformed but ok=%v reason=%q recovered via regex", hr.OK, hr.Reason)
	return hr, nil
}

var (
	activeRunsMu sync.RWMutex
	activeRuns   = make(map[string]*workflowRun) // executionID -> run
)

// isWorkflowRunningByID is a package-level helper used by both the Server methods
// and the pre-check in Execute. Caller must not hold activeRunsMu.
func isWorkflowRunningByID(workflowID string) bool {
	activeRunsMu.RLock()
	defer activeRunsMu.RUnlock()
	for _, r := range activeRuns {
		if r.workflowID == workflowID {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Execute — unified entry point
// ---------------------------------------------------------------------------

func (s *Server) Execute(req ExecuteWorkflowRequest) (*store.WorkflowExecution, error) {
	// Fast pre-check before hitting the DB (the definitive check is under the write lock below).
	if isWorkflowRunningByID(req.WorkflowID) {
		return nil, fmt.Errorf("workflow is already running")
	}

	wf, err := s.Store.GetWorkflow(req.WorkflowID)
	if err != nil {
		return nil, fmt.Errorf("load workflow: %w", err)
	}
	if wf == nil {
		return nil, fmt.Errorf("workflow %s not found", req.WorkflowID)
	}

	var def store.WorkflowDefinition
	if err := json.Unmarshal(wf.Definition, &def); err != nil {
		return nil, fmt.Errorf("parse definition: %w", err)
	}
	if len(def.Nodes) == 0 {
		return nil, fmt.Errorf("workflow has no nodes")
	}

	if req.ProjectID == "" {
		req.ProjectID = wf.ProjectID
	}

	// Atomically check for a concurrent run. CreateExecution is deferred until
	// after the check so we never produce stale "failed" execution records when
	// two callers race through the fast pre-check above.
	activeRunsMu.Lock()
	for _, r := range activeRuns {
		if r.workflowID == wf.ID {
			activeRunsMu.Unlock()
			return nil, fmt.Errorf("workflow is already running")
		}
	}

	defSnapshot := string(wf.Definition)
	exec, err := s.Store.CreateExecution(store.CreateExecutionRequest{
		WorkflowID:         wf.ID,
		WorkflowVersion:    wf.Version,
		DefinitionSnapshot: defSnapshot,
		TriggeredBy:        req.TriggeredBy,
		UserContext:        req.UserContext,
	})
	if err != nil {
		activeRunsMu.Unlock()
		return nil, fmt.Errorf("create execution: %w", err)
	}

	// No global timeout: human nodes must wait indefinitely for approval.
	// Per-AI-operation timeouts are applied inside waitSessionDone.
	ctx, cancel := context.WithCancel(context.Background())
	run := &workflowRun{
		executionID: exec.ID,
		workflowID:  wf.ID,
		projectID:   req.ProjectID,
		cancel:      cancel,
	}
	activeRuns[exec.ID] = run
	activeRunsMu.Unlock()

	log.Printf("[workflow] Execute %q (exec=%s, nodes=%d, triggered_by=%s)",
		wf.Name, exec.ID, len(def.Nodes), req.TriggeredBy)

	go s.runWorkflow(ctx, run, wf.Name, &def, req)

	// Notify frontend: this workflow is now running
	s.Hub.NotifyChange("workflow", wf.ID, "updated")

	return exec, nil
}

// CancelExecution cancels a running workflow execution and all its sessions.
func (s *Server) CancelExecution(executionID string) error {
	activeRunsMu.RLock()
	run, ok := activeRuns[executionID]
	activeRunsMu.RUnlock()
	if !ok {
		return fmt.Errorf("execution %s not found or already finished", executionID)
	}
	run.cancel()
	return nil
}

// CancelWorkflowBySession checks if the given session belongs to any running
// workflow execution. If so, it cancels the entire workflow (not just the session).
// Called from the stop handler in chat.go.
func (s *Server) CancelWorkflowBySession(sessionID string) {
	activeRunsMu.RLock()
	defer activeRunsMu.RUnlock()
	for _, run := range activeRuns {
		run.mu.Lock()
		found := false
		for _, sid := range run.sessionIDs {
			if sid == sessionID {
				found = true
				break
			}
		}
		run.mu.Unlock()
		if found {
			log.Printf("[workflow] Session %s stop → cancelling workflow execution %s", sessionID, run.executionID)
			run.cancel()
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Background workflow runner
// ---------------------------------------------------------------------------

func (s *Server) runWorkflow(ctx context.Context, run *workflowRun, wfName string, def *store.WorkflowDefinition, req ExecuteWorkflowRequest) {
	defer func() {
		run.cancel()
		activeRunsMu.Lock()
		delete(activeRuns, run.executionID)
		activeRunsMu.Unlock()
	}()

	startTime := time.Now()
	_ = s.Store.UpdateExecutionStatus(run.executionID, "running", "", nil)
	s.emitWorkflowProgress(run.executionID, "", "running")

	levels := buildLevels(def)

	nodeLabels := make(map[string]string)
	for _, n := range def.Nodes {
		label, _ := n.Data["label"].(string)
		if label == "" {
			label = n.Type
		}
		nodeLabels[n.ID] = label
	}

	parentMap := make(map[string][]string)
	for _, n := range def.Nodes {
		parentMap[n.ID] = nil
	}
	for _, e := range def.Edges {
		parentMap[e.Target] = append(parentMap[e.Target], e.Source)
	}

	nodeOutputs := make(map[string]string)
	humanGateOutputs := make(map[string]string) // continued inputs from human nodes; always injected into downstream prompts
	var nodeOutputsMu sync.Mutex
	firstAI := true
	var primarySID string // reused session for serial AI chain

	for _, n := range def.Nodes {
		s.emitWorkflowProgress(run.executionID, n.ID, "pending")
	}

	for li, level := range levels {
		if ctx.Err() != nil {
			s.markRemainingFailed(run.executionID, levels, li, nodeLabels)
			break
		}

		aiNodes := filterAINodes(level)
		humanNodes := filterHumanNodes(level)
		passNodes := filterPassNodes(level)

		// Human nodes: pause and wait for explicit approval
		for _, n := range humanNodes {
			humanLog := &store.WorkflowNodeLog{
				ID:          uuid.New().String(),
				ExecutionID: run.executionID,
				NodeID:      n.ID,
				NodeType:    n.Type,
				NodeLabel:   nodeLabels[n.ID],
				Level:       li,
				Status:      "running",
				CreatedAt:   time.Now(),
			}
			humanNow := time.Now()
			humanLog.StartedAt = &humanNow
			_ = s.Store.CreateNodeLog(humanLog)
			_ = s.Store.UpdateNodeLog(humanLog.ID, "waiting", "", "", "", 0)

			s.emitWorkflowProgress(run.executionID, n.ID, "running")
			continuedInput, err := s.waitHumanApproval(ctx, run.executionID, n.ID, nodeLabels[n.ID])
			humanDur := time.Since(humanNow).Milliseconds()
			if err != nil {
				_ = s.Store.UpdateNodeLog(humanLog.ID, "failed", "", "", err.Error(), humanDur)
				log.Printf("[workflow] Human node %s rejected/cancelled: %v", nodeLabels[n.ID], err)
				s.markRemainingFailed(run.executionID, levels, li+1, nodeLabels)
				s.finishExecution(run, startTime, "failed", err.Error())
				return
			}
			_ = s.Store.UpdateNodeLog(humanLog.ID, "completed", continuedInput, "", "", humanDur)
			s.emitWorkflowProgress(run.executionID, n.ID, "completed")

			// Record the continued input so downstream AI nodes can inject it into
			// their prompts regardless of whether they reuse the serial session.
			if continuedInput != "" {
				humanGateOutputs[n.ID] = continuedInput
			}
		}

		for _, n := range passNodes {
			passLog := &store.WorkflowNodeLog{
				ID:          uuid.New().String(),
				ExecutionID: run.executionID,
				NodeID:      n.ID,
				NodeType:    n.Type,
				NodeLabel:   nodeLabels[n.ID],
				Level:       li,
				Status:      "completed",
				CreatedAt:   time.Now(),
			}
			passNow := time.Now()
			passLog.StartedAt = &passNow
			_ = s.Store.CreateNodeLog(passLog)
			s.emitWorkflowProgress(run.executionID, n.ID, "running")
			_ = s.Store.UpdateNodeLog(passLog.ID, "completed", "", "", "", 0)
			s.emitWorkflowProgress(run.executionID, n.ID, "completed")
		}

		if len(aiNodes) == 0 {
			continue
		}

		if len(aiNodes) == 1 {
			node := aiNodes[0]
			sid, output, err := s.executeAINodeOnSession(ctx, run, wfName, node, li, req, nodeLabels, parentMap, nodeOutputs, humanGateOutputs, firstAI, primarySID)
			firstAI = false
			if err != nil {
				log.Printf("[workflow] Node %s failed: %v", nodeLabels[node.ID], err)
				s.markRemainingFailed(run.executionID, levels, li+1, nodeLabels)
				s.finishExecution(run, startTime, "failed", err.Error())
				return
			}
			if primarySID == "" {
				primarySID = sid
			}
			// Serial nodes share session; only capture output when new session was created
			if output != "" {
				nodeOutputsMu.Lock()
				nodeOutputs[node.ID] = output
				nodeOutputsMu.Unlock()
			}
		} else {
			// Parallel: each node gets its own session; primary session is invalidated
			primarySID = ""
			type result struct {
				nodeID string
				output string
				err    error
			}
			ch := make(chan result, len(aiNodes))
			isFirst := firstAI
			firstAI = false

			for i, node := range aiNodes {
				go func(n store.WorkflowNode, injectContext bool) {
					_, output, err := s.executeAINodeOnSession(ctx, run, wfName, n, li, req, nodeLabels, parentMap, nodeOutputs, humanGateOutputs, injectContext, "")
					ch <- result{nodeID: n.ID, output: output, err: err}
				}(node, isFirst && i == 0)
			}

			var firstErr error
			for range aiNodes {
				r := <-ch
				if r.err != nil && firstErr == nil {
					firstErr = fmt.Errorf("node %s: %w", nodeLabels[r.nodeID], r.err)
				} else if r.output != "" {
					nodeOutputsMu.Lock()
					nodeOutputs[r.nodeID] = r.output
					nodeOutputsMu.Unlock()
				}
			}
			if firstErr != nil {
				log.Printf("[workflow] Parallel level %d failed: %v", li, firstErr)
				s.markRemainingFailed(run.executionID, levels, li+1, nodeLabels)
				s.finishExecution(run, startTime, "failed", firstErr.Error())
				return
			}
		}

		log.Printf("[workflow] Level %d/%d done (exec=%s)", li+1, len(levels), run.executionID)
	}

	if ctx.Err() != nil {
		s.finishExecution(run, startTime, "cancelled", "execution cancelled")
	} else {
		s.finishExecution(run, startTime, "completed", "")
	}
}

// ---------------------------------------------------------------------------
// Single AI node execution
// ---------------------------------------------------------------------------

// executeAINodeOnSession runs an AI node. If reuseSID is non-empty, the node's
// prompt is queued onto that existing session (serial chain reuse). Otherwise a
// new session is created. Returns the session ID used and the node's text output
// (non-empty only for new sessions; serial-chain nodes share context so output is "").
func (s *Server) executeAINodeOnSession(
	ctx context.Context,
	run *workflowRun,
	wfName string,
	node store.WorkflowNode,
	level int,
	req ExecuteWorkflowRequest,
	nodeLabels map[string]string,
	parentMap map[string][]string,
	nodeOutputs map[string]string,
	humanGateOutputs map[string]string,
	injectContext bool,
	reuseSID string,
) (string, string, error) {
	config, _ := node.Data["config"].(map[string]interface{})
	prompt, _ := config["prompt"].(string)
	agentName, _ := config["agent"].(string)

	nodeLog := &store.WorkflowNodeLog{
		ID:          uuid.New().String(),
		ExecutionID: run.executionID,
		NodeID:      node.ID,
		NodeType:    node.Type,
		NodeLabel:   nodeLabels[node.ID],
		Level:       level,
		Status:      "running",
		CreatedAt:   time.Now(),
	}
	now := time.Now()
	nodeLog.StartedAt = &now
	_ = s.Store.CreateNodeLog(nodeLog)

	s.emitWorkflowProgress(run.executionID, node.ID, "running")

	// When reusing the same session (serial chain), skip upstream AI output injection
	// — the session already has full conversation history.
	// Only inject upstream AI context for new sessions (parallel/map/reduce).
	var upstreamOutputs map[string]string
	if reuseSID == "" {
		upstreamOutputs = nodeOutputs
	}

	fullPrompt := buildNodePrompt(node, prompt, req.UserContext, req.Tasks, upstreamOutputs, parentMap, nodeLabels, injectContext, config)

	// Always prepend continued input from human gate parent nodes, even when reusing
	// the serial session (the human's input was never sent to that session).
	for _, pid := range parentMap[node.ID] {
		if input, ok := humanGateOutputs[pid]; ok && input != "" {
			fullPrompt = fmt.Sprintf("[%s]\n%s\n\n---\n\n%s", nodeLabels[pid], input, fullPrompt)
		}
	}

	parts := []MessagePart{{Type: "text", Content: fullPrompt}}
	if injectContext {
		parts = append(parts, buildRefParts(req.Tasks, config)...)
	}

	features := extractFeatures(config)

	sid := reuseSID
	if sid == "" {
		sessionName := fmt.Sprintf("WF: %s — %s", wfName, nodeLabels[node.ID])
		var err error
		sid, err = s.createWorkflowSession(run, sessionName, agentName)
		if err != nil {
			dur := time.Since(now).Milliseconds()
			_ = s.Store.UpdateNodeLog(nodeLog.ID, "failed", "", "", err.Error(), dur)
			s.emitWorkflowProgress(run.executionID, node.ID, "failed")
			return "", "", fmt.Errorf("create session: %w", err)
		}
	}

	log.Printf("[workflow] Node %q → session %s (reuse=%v, features=%v)", nodeLabels[node.ID], sid, reuseSID != "", features)

	if len(features) > 0 {
		s.mergeSessionFeatures(sid, features)
	}

	s.handleUserMessage(sid, fullPrompt, parts, nil, features)

	hook := parseHookConfig(config)
	workDir := s.getWorkDirForProject(run.projectID)

	var output string
	var finalDur int64
	hookRetries := 0
	hookOutcome := ""

	for {
		var iterErr error
		output, iterErr = s.waitSessionDone(ctx, sid)
		finalDur = time.Since(now).Milliseconds()

		if iterErr != nil {
			_ = s.Store.UpdateNodeLog(nodeLog.ID, "failed", "", "", iterErr.Error(), finalDur)
			s.emitWorkflowProgress(run.executionID, node.ID, "failed")
			return sid, "", iterErr
		}

		if hook == nil || hook.Trigger != "on_reply_done" {
			break
		}

		hookPrompt := buildHookPrompt(hook.Prompt, output)
		hookOutput, hookErr := s.runEphemeralAgent(ctx, agentName, workDir, hookPrompt)
		if hookErr != nil {
			log.Printf("[workflow] Hook agent error: %v", hookErr)
			hookOutcome = "agent_error"
			_ = s.Store.SetNodeHookInfo(nodeLog.ID, hookRetries, hookOutcome)
			errMsg := fmt.Sprintf("hook agent error: %v", hookErr)
			_ = s.Store.UpdateNodeLog(nodeLog.ID, "failed", "", truncateStr(output, 10000), errMsg, finalDur)
			s.emitWorkflowProgress(run.executionID, node.ID, "failed")
			return sid, "", fmt.Errorf("%s", errMsg)
		}

		trimmed := strings.TrimSpace(hookOutput)
		hr, parseErr := parseHookResult(trimmed)
		if parseErr != nil {
			log.Printf("[workflow] Hook response parse error: %v (raw: %q)", parseErr, trimmed)
			hookOutcome = "parse_error"
			_ = s.Store.SetNodeHookInfo(nodeLog.ID, hookRetries, hookOutcome)
			errMsg := fmt.Sprintf("hook parse error: %v (raw: %q)", parseErr, trimmed)
			_ = s.Store.UpdateNodeLog(nodeLog.ID, "failed", "", truncateStr(output, 10000), errMsg, finalDur)
			s.emitWorkflowProgress(run.executionID, node.ID, "failed")
			return sid, "", fmt.Errorf("%s", errMsg)
		}

		log.Printf("[workflow] Hook for node %q: ok=%v reason=%q retries=%d", nodeLabels[node.ID], hr.OK, hr.Reason, hookRetries)

		if hr.OK {
			if hookRetries > 0 {
				hookOutcome = "ok_after_retries"
			} else {
				hookOutcome = "ok"
			}
			break
		}

		// Hook says not OK — send reason back to session and loop.
		hookRetries++
		reason := hr.Reason
		if reason == "" {
			reason = "The response was incomplete or incorrect. Please try again."
		}

		if hook.Reset {
			if err := s.Store.UpdateSessionAgentID(sid, ""); err != nil {
				log.Printf("[workflow] Hook reset: failed to clear agent session ID: %v", err)
			}
		}

		s.handleUserMessage(sid, reason, nil, nil, nil)
	}

	_ = s.Store.UpdateNodeLog(nodeLog.ID, "completed", "", truncateStr(output, 10000), "", finalDur)
	if hookOutcome != "" {
		_ = s.Store.SetNodeHookInfo(nodeLog.ID, hookRetries, hookOutcome)
	}
	s.emitWorkflowProgress(run.executionID, node.ID, "completed")

	// Return output only for new sessions; serial-chain reuses share conversation history.
	if reuseSID != "" {
		return sid, "", nil
	}
	return sid, output, nil
}

// parseHookConfig extracts hook settings from a node's config map.
func parseHookConfig(config map[string]interface{}) *hookConfig {
	hookRaw, ok := config["hook"].(map[string]interface{})
	if !ok || hookRaw == nil {
		return nil
	}
	trigger, _ := hookRaw["trigger"].(string)
	if trigger == "" {
		return nil
	}
	prompt, _ := hookRaw["prompt"].(string)
	if prompt == "" {
		return nil
	}
	reset, _ := hookRaw["reset"].(bool)
	return &hookConfig{Trigger: trigger, Prompt: prompt, Reset: reset}
}

// buildHookPrompt builds the prompt for the ephemeral hook agent call.
// It prepends the node's output and appends the JSON format requirement.
func buildHookPrompt(userPrompt, nodeOutput string) string {
	var sb strings.Builder
	if nodeOutput != "" {
		sb.WriteString("[Previous Step Output]\n")
		sb.WriteString(nodeOutput)
		sb.WriteString("\n\n---\n\n")
	}
	sb.WriteString("[User Instruction]\n")
	sb.WriteString(userPrompt)
	sb.WriteString("\n\n---\n\nDoes the Previous Step Output satisfy the User Instruction above? ")
	sb.WriteString("Respond with ONLY a single-line valid JSON object: ")
	sb.WriteString(`{"ok": true} if it satisfies the instruction, or {"ok": false, "reason": "..."} explaining what is missing or wrong if not. `)
	sb.WriteString("The JSON must be strictly valid: any quotes or special characters inside string values must be escaped with a backslash (e.g. \\\"text\\\"). Do not include any text outside the JSON object.")
	return sb.String()
}

const hookAgentTimeout = 60 * time.Second

// runEphemeralAgent performs a one-shot agent call without creating a Nexbox session.
// The agent is resolved by name (falling back to the server default). The call
// always starts a fresh underlying conversation (SessionID = "").
func (s *Server) runEphemeralAgent(ctx context.Context, agentName, workDir, prompt string) (string, error) {
	var a agent.Agent
	if agentName != "" {
		if ag, ok := s.Agents[agentName]; ok {
			a = ag
		}
	}
	if a == nil {
		a = s.Agent
	}
	if a == nil {
		return "", fmt.Errorf("no agent available for ephemeral hook call")
	}

	hookCtx, cancel := context.WithTimeout(ctx, hookAgentTimeout)
	defer cancel()

	var output string
	var mu sync.Mutex

	_, err := a.SendMessage(hookCtx, agent.Request{
		WorkDir: workDir,
		Message: prompt,
	}, func(event agent.StreamEvent) {
		if event.Type == agent.EventTextDelta {
			mu.Lock()
			output += event.Content
			mu.Unlock()
		}
	})
	if err != nil {
		return "", fmt.Errorf("hook agent: %w", err)
	}
	return output, nil
}

// getWorkDirForProject returns the local working directory for a project.
func (s *Server) getWorkDirForProject(projectID string) string {
	project, err := s.Store.GetProject(projectID)
	if err != nil || project == nil || project.GitRepoPath == "" {
		return "."
	}
	return project.GitRepoPath
}

// ---------------------------------------------------------------------------
// Session management
// ---------------------------------------------------------------------------

func (s *Server) createWorkflowSession(run *workflowRun, name, agent string) (string, error) {
	if agent == "" {
		agent = s.getDefaultAgent()
	}
	session, err := s.Store.CreateSession(store.CreateSessionRequest{
		Name:      name,
		ProjectID: run.projectID,
		Agent:     agent,
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}

	run.mu.Lock()
	run.sessionIDs = append(run.sessionIDs, session.ID)
	run.mu.Unlock()

	s.Hub.NotifyChange("session", session.ID, "created")
	log.Printf("[workflow] Created session %s (%s, agent=%s)", session.ID, name, agent)
	return session.ID, nil
}

const aiOperationTimeout = 2 * time.Hour

func (s *Server) waitSessionDone(ctx context.Context, sessionID string) (string, error) {
	type doneResult struct {
		output string
		err    error
	}
	doneCh := make(chan doneResult, 1)
	var mu sync.Mutex
	var output string

	unsub := s.Hub.SubscribeSession(sessionID, func(event ChatEvent) {
		switch event.Type {
		case "text_delta":
			mu.Lock()
			output += event.Content
			mu.Unlock()
		case "done":
			mu.Lock()
			o := output
			mu.Unlock()
			select {
			case doneCh <- doneResult{output: o}:
			default:
			}
		case "error":
			mu.Lock()
			o := output
			mu.Unlock()
			select {
			case doneCh <- doneResult{output: o, err: fmt.Errorf("session error: %s", event.Message)}:
			default:
			}
		}
	})
	defer unsub()

	// AI operations have a per-operation timeout so a stuck AI session doesn't block
	// the workflow forever. Human nodes use waitHumanApproval (not this function) and
	// wait indefinitely, cancelled only by manual CancelExecution.
	opCtx, opCancel := context.WithTimeout(ctx, aiOperationTimeout)
	defer opCancel()

	select {
	case result := <-doneCh:
		log.Printf("[workflow] Session %s done (output_len=%d)", sessionID, len(result.output))
		return result.output, result.err
	case <-opCtx.Done():
		s.SessionManager.Cancel(sessionID)
		if ctx.Err() != nil {
			// Parent cancelled by CancelExecution / session stop
			mu.Lock()
			o := output
			mu.Unlock()
			return o, ctx.Err()
		}
		mu.Lock()
		o := output
		mu.Unlock()
		return o, fmt.Errorf("AI operation timed out after %s", aiOperationTimeout)
	}
}

func (s *Server) cancelAllSessions(run *workflowRun) {
	run.mu.Lock()
	ids := make([]string, len(run.sessionIDs))
	copy(ids, run.sessionIDs)
	run.mu.Unlock()

	for _, sid := range ids {
		s.SessionManager.Cancel(sid)
	}
	if len(ids) > 0 {
		log.Printf("[workflow] Cancelled %d sessions for execution %s", len(ids), run.executionID)
	}
}

// ---------------------------------------------------------------------------
// Prompt building
// ---------------------------------------------------------------------------

func buildNodePrompt(
	node store.WorkflowNode,
	prompt, userContext string,
	tasks []store.Task,
	nodeOutputs map[string]string,
	parentMap map[string][]string,
	nodeLabels map[string]string,
	injectContext bool,
	config map[string]interface{},
) string {
	var sb strings.Builder

	if injectContext {
		if userContext != "" {
			sb.WriteString("[User Context]\n")
			sb.WriteString(userContext)
			sb.WriteString("\n\n---\n\n")
		}
		if len(tasks) > 0 {
			sb.WriteString("[Task Context]\n")
			for _, t := range tasks {
				sb.WriteString(fmt.Sprintf("- %s (status: %s, priority: %s)\n", t.Title, t.Status, t.Priority))
				if t.Description != "" {
					sb.WriteString(fmt.Sprintf("  %s\n", t.Description))
				}
			}
			sb.WriteString("\n---\n\n")
		}
	}

	parents := parentMap[node.ID]
	var upstreams []string
	for _, pid := range parents {
		if out, ok := nodeOutputs[pid]; ok && out != "" {
			upstreams = append(upstreams, fmt.Sprintf("## %s\n%s", nodeLabels[pid], out))
		}
	}
	if len(upstreams) > 0 {
		sb.WriteString("[Context from previous steps]\n\n")
		sb.WriteString(strings.Join(upstreams, "\n\n"))
		sb.WriteString("\n\n---\n\n")
	}

	sb.WriteString(prompt)

	if enableE2E, _ := config["enableE2E"].(bool); enableE2E {
		sb.WriteString("\n\n---\n\n[E2E Browser Testing]\n")
		sb.WriteString("You now have Chrome DevTools MCP tools available for end-to-end browser testing. ")
		sb.WriteString("Use take_snapshot to see the page structure (accessibility tree with UIDs), then interact with elements using their UIDs. ")
		sb.WriteString("Typical workflow: navigate_page -> take_snapshot -> click/fill/type_text -> take_screenshot to verify. ")
		sb.WriteString("Always call take_snapshot before interacting to get fresh UIDs. ")
		sb.WriteString("After completing the above task, use these tools to verify the changes work correctly in the browser.")
	}

	return sb.String()
}

func buildRefParts(tasks []store.Task, config map[string]interface{}) []MessagePart {
	var parts []MessagePart
	for _, t := range tasks {
		parts = append(parts, MessagePart{Type: "task_ref", TaskID: t.ID, TaskData: t})
	}
	if assetRefs, ok := config["assetRef"].([]interface{}); ok {
		for _, a := range assetRefs {
			if aid, ok := a.(string); ok && aid != "" {
				parts = append(parts, MessagePart{Type: "asset_ref", AssetID: aid})
			}
		}
	}
	return parts
}

func extractFeatures(config map[string]interface{}) []string {
	if enableE2E, _ := config["enableE2E"].(bool); enableE2E {
		return []string{"chrome_devtools"}
	}
	return nil
}

// ---------------------------------------------------------------------------
// DAG helpers
// ---------------------------------------------------------------------------

func buildLevels(def *store.WorkflowDefinition) [][]store.WorkflowNode {
	inDegree := make(map[string]int)
	adj := make(map[string][]string)
	nodeMap := make(map[string]store.WorkflowNode)

	for _, n := range def.Nodes {
		nodeMap[n.ID] = n
		inDegree[n.ID] = 0
	}
	for _, e := range def.Edges {
		adj[e.Source] = append(adj[e.Source], e.Target)
		inDegree[e.Target]++
	}

	var levels [][]store.WorkflowNode
	var queue []string
	for id, d := range inDegree {
		if d == 0 {
			queue = append(queue, id)
		}
	}

	for len(queue) > 0 {
		level := make([]store.WorkflowNode, 0, len(queue))
		for _, id := range queue {
			level = append(level, nodeMap[id])
		}
		levels = append(levels, level)

		var next []string
		for _, id := range queue {
			for _, neighbor := range adj[id] {
				inDegree[neighbor]--
				if inDegree[neighbor] == 0 {
					next = append(next, neighbor)
				}
			}
		}
		queue = next
	}

	return levels
}

func filterAINodes(level []store.WorkflowNode) []store.WorkflowNode {
	var out []store.WorkflowNode
	for _, n := range level {
		if n.Type != "ai" {
			continue
		}
		config, _ := n.Data["config"].(map[string]interface{})
		if prompt, _ := config["prompt"].(string); prompt != "" {
			out = append(out, n)
		}
	}
	return out
}

func filterHumanNodes(level []store.WorkflowNode) []store.WorkflowNode {
	var out []store.WorkflowNode
	for _, n := range level {
		if n.Type == "human" {
			out = append(out, n)
		}
	}
	return out
}

func filterPassNodes(level []store.WorkflowNode) []store.WorkflowNode {
	var out []store.WorkflowNode
	for _, n := range level {
		if n.Type == "human" {
			continue // handled separately
		}
		if n.Type == "ai" {
			config, _ := n.Data["config"].(map[string]interface{})
			if prompt, _ := config["prompt"].(string); prompt != "" {
				continue
			}
		}
		out = append(out, n)
	}
	return out
}

// ---------------------------------------------------------------------------
// Human node gate — pause workflow until explicit approval via API
// ---------------------------------------------------------------------------

func humanGateKey(executionID, nodeID string) string {
	return executionID + ":" + nodeID
}

// waitHumanApproval pauses workflow execution until the human node receives a Continue signal.
// Returns the input string provided by the user (may be empty), or an error if cancelled.
func (s *Server) waitHumanApproval(ctx context.Context, executionID, nodeID, nodeLabel string) (string, error) {
	s.humanGatesMu.Lock()
	if s.humanGates == nil {
		s.humanGates = make(map[string]chan string)
	}
	key := humanGateKey(executionID, nodeID)
	ch := make(chan string, 1)
	s.humanGates[key] = ch
	s.humanGatesMu.Unlock()

	defer func() {
		s.humanGatesMu.Lock()
		delete(s.humanGates, key)
		s.humanGatesMu.Unlock()
	}()

	log.Printf("[workflow] Human node %q waiting for continue (exec=%s, node=%s)", nodeLabel, executionID, nodeID)
	s.emitWorkflowProgress(executionID, nodeID, "waiting_approval")

	select {
	case input := <-ch:
		log.Printf("[workflow] Human node %q continued (exec=%s)", nodeLabel, executionID)
		return input, nil
	case <-ctx.Done():
		return "", fmt.Errorf("human node %q cancelled: %w", nodeLabel, ctx.Err())
	}
}

// ContinueHumanNode is called by the HTTP handler to unblock a waiting human node.
// input is the optional message provided by the user when continuing.
func (s *Server) ContinueHumanNode(executionID, nodeID, input string) error {
	s.humanGatesMu.Lock()
	ch, ok := s.humanGates[humanGateKey(executionID, nodeID)]
	s.humanGatesMu.Unlock()
	if !ok {
		return fmt.Errorf("node %s is not currently waiting", nodeID)
	}
	select {
	case ch <- input:
		return nil
	default:
		return fmt.Errorf("continue already submitted for node %s", nodeID)
	}
}

// ---------------------------------------------------------------------------
// Progress / finish helpers
// ---------------------------------------------------------------------------

func (s *Server) emitWorkflowProgress(executionID, nodeID, status string) {
	data, _ := json.Marshal(map[string]interface{}{
		"type":         "workflow_progress",
		"execution_id": executionID,
		"node_id":      nodeID,
		"status":       status,
	})
	s.Hub.BroadcastRaw(data)
}

func (s *Server) finishExecution(run *workflowRun, startTime time.Time, status, errMsg string) {
	dur := time.Since(startTime).Milliseconds()
	_ = s.Store.UpdateExecutionStatus(run.executionID, status, errMsg, &dur)

	// Safety net: any node log still showing "running" means the node was
	// interrupted (context cancel, DB write error, crash). Stamp them with the
	// execution's terminal status so the UI never gets stuck spinning.
	if status == "failed" || status == "cancelled" {
		_ = s.Store.FailStaleNodeLogs(run.executionID, status)
	}

	if status == "cancelled" || status == "failed" {
		s.cancelAllSessions(run)
	}

	s.emitWorkflowProgress(run.executionID, "", status)
	// Notify frontend: is_running is now false for this workflow
	s.Hub.NotifyChange("workflow", run.workflowID, "updated")

	log.Printf("[workflow] Execution %s %s (%.1fs)", run.executionID, status, float64(dur)/1000)
}

func (s *Server) markRemainingFailed(executionID string, levels [][]store.WorkflowNode, fromLevel int, nodeLabels map[string]string) {
	for i := fromLevel; i < len(levels); i++ {
		for _, n := range levels[i] {
			s.emitWorkflowProgress(executionID, n.ID, "failed")
		}
	}
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
}

// ---------------------------------------------------------------------------
// WorkflowExecutor interface for MCP
// ---------------------------------------------------------------------------

func (s *Server) ExecuteWorkflow(workflowID, projectID, userContext, triggeredBy string, taskIDs []string) (string, error) {
	var tasks []store.Task
	for _, tid := range taskIDs {
		if t, err := s.Store.GetTask(tid); err == nil && t != nil {
			tasks = append(tasks, *t)
		}
	}

	exec, err := s.Execute(ExecuteWorkflowRequest{
		WorkflowID:  workflowID,
		ProjectID:   projectID,
		UserContext: userContext,
		Tasks:       tasks,
		TriggeredBy: triggeredBy,
	})
	if err != nil {
		return "", err
	}
	return exec.ID, nil
}

// ---------------------------------------------------------------------------
// Legacy SessionOrchestrator interface (still used by MCP for queue)
// ---------------------------------------------------------------------------

func (s *Server) OrchestrateCreateSession(name, projectID, agent string) (string, error) {
	session, err := s.Store.CreateSession(store.CreateSessionRequest{
		Name:      name,
		ProjectID: projectID,
		Agent:     agent,
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	s.Hub.NotifyChange("session", session.ID, "created")
	return session.ID, nil
}

func (s *Server) OrchestrateSendMessage(sessionID, message string) error {
	s.handleUserMessage(sessionID, message, nil, nil, nil)
	return nil
}

func (s *Server) OrchestrateQueueMessage(sessionID, message string) error {
	_, err := s.Store.CreateQueuedMessage(sessionID, store.CreateQueuedMessageRequest{
		Text: message,
	})
	if err != nil {
		return fmt.Errorf("queue message: %w", err)
	}
	s.Hub.Emit(sessionID, ChatEvent{Type: "queue_updated"})
	return nil
}

func (s *Server) OrchestrateWaitDone(ctx context.Context, sessionID string) (string, error) {
	return s.waitSessionDone(ctx, sessionID)
}
