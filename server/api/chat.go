package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/store"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// HandleGlobalWS is the single global WebSocket endpoint.
// All sessions are multiplexed over this one connection via subscribe/unsubscribe.
func (s *Server) HandleGlobalWS(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.authorizeWS(w, r)
	if !ok {
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	s.Hub.AddClient(conn, uid)
	defer s.Hub.RemoveClient(conn)

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[ws] WebSocket read error: %v", err)
			}
			break
		}

		var chatReq ChatRequest
		if err := json.Unmarshal(msg, &chatReq); err != nil {
			s.Hub.SendDirect(conn, "", ChatEvent{Type: "error", Message: "invalid message format"})
			continue
		}

		switch chatReq.Type {
		case "subscribe":
			continue

		case "unsubscribe":
			continue

		case "user_message":
			if chatReq.SessionID == "" {
				s.Hub.SendDirect(conn, "", ChatEvent{Type: "error", Message: "missing session_id"})
				continue
			}
			if chatReq.Content == "" && len(chatReq.Images) == 0 && len(chatReq.Parts) == 0 {
				s.Hub.Emit(chatReq.SessionID, ChatEvent{Type: "error", Message: "empty message"})
				continue
			}
			s.handleUserMessage(chatReq.SessionID, chatReq.Content, chatReq.Parts, chatReq.Images, chatReq.Features)

		case "interject":
			if chatReq.SessionID == "" {
				s.Hub.SendDirect(conn, "", ChatEvent{Type: "error", Message: "interject: missing session_id"})
				continue
			}
			if chatReq.Content == "" && len(chatReq.Images) == 0 && len(chatReq.Parts) == 0 {
				s.Hub.Emit(chatReq.SessionID, ChatEvent{Type: "error", Message: "empty message"})
				continue
			}
			// Dispatch off the read loop: unlike Claude's near-instant stdin
			// write, an interject can be a network round-trip to the backend
			// (Codex turn/steer, up to 30s). Running it inline would stall
			// every frame on this connection — including "stop". Enqueueing
			// here, synchronously in the read loop, preserves arrival order;
			// the per-session drainer then executes strictly FIFO.
			req := chatReq
			s.interjectQ.enqueue(req.SessionID, func() {
				s.handleInterject(req.SessionID, req.Content, req.Parts, req.Images, req.Features)
			})

		case "tool_response":
			if chatReq.SessionID == "" || chatReq.ToolUseID == "" {
				s.Hub.SendDirect(conn, "", ChatEvent{Type: "error", Message: "tool_response: missing session_id or tool_use_id"})
				continue
			}
			ch := getToolResponseChan(chatReq.SessionID, chatReq.ToolUseID)
			if ch != nil {
				log.Printf("[ws] Routing tool_response for session %s, tool_use_id=%s (skip=%v)", chatReq.SessionID, chatReq.ToolUseID, chatReq.Skip)
				select {
				case ch <- toolResponse{Response: chatReq.ToolResponse, Skip: chatReq.Skip}:
				default:
					log.Printf("[ws] tool_response channel full or closed for session %s", chatReq.SessionID)
				}
			} else {
				log.Printf("[ws] No tool_response channel for session %s (tool_use_id=%s)", chatReq.SessionID, chatReq.ToolUseID)
			}

		case "stop":
			if chatReq.SessionID == "" {
				s.Hub.SendDirect(conn, "", ChatEvent{Type: "error", Message: "stop: missing session_id"})
				continue
			}
			s.SessionManager.Cancel(chatReq.SessionID)
			s.CancelWorkflowBySession(chatReq.SessionID)

		default:
			s.Hub.SendDirect(conn, "", ChatEvent{Type: "error", Message: "unknown message type: " + chatReq.Type})
		}
	}
}

// persistSessionAgent writes the effective agent onto the session row when it
// was never set, mutating session.Agent in place on success. Scratch sessions
// are created with agent='' and (unless the user manually switches the agent
// dropdown) promoted in place without ever writing the column — UnhideSession
// only flips is_hidden, and the turn still runs because ResolveAgent falls back
// to the server default. Persisting the resolved name lets clients associate
// the session with its advertised capabilities without a separate fallback.
// No-op once agent is set.
func (s *Server) persistSessionAgent(session *Session) {
	if session == nil || session.Agent != "" {
		return
	}
	_, agentName := s.ResolveAgent(session)
	if agentName == "" {
		return
	}
	if _, err := s.Store.UpdateSession(session.ID, UpdateSessionRequest{Agent: &agentName}); err != nil {
		log.Printf("[chat] Failed to persist resolved agent %q on session %s: %v", agentName, session.ID, err)
		return
	}
	session.Agent = agentName
}

// handleUserMessage resolves context and starts the agent goroutine for a session.
func (s *Server) handleUserMessage(sessionID, message string, parts []MessagePart, imageFilenames []string, features []string) {
	session, err := s.Store.GetSession(sessionID)
	if err != nil || session == nil {
		s.Hub.Emit(sessionID, ChatEvent{Type: "error", Message: "session not found"})
		return
	}

	// Resolve inline task/asset refs from this message's parts.
	var refTasks []agent.TaskRef
	var refAssets []agent.AssetRef
	for _, p := range parts {
		switch p.Type {
		case "task_ref":
			if p.TaskID != "" {
				if info, err := store.GetTaskInfo(s.Store, p.TaskID); err == nil && info != nil {
					refTasks = append(refTasks, agent.TaskRef{
						ID: info.ID, Title: info.Title, Description: info.Description, Status: info.Status, Priority: info.Priority,
					})
				}
			}
		case "asset_ref":
			if p.AssetID != "" {
				if info, err := store.GetAssetInfo(s.Store, p.AssetID); err == nil && info != nil {
					refAssets = append(refAssets, agent.AssetRef{
						ID: info.ID, Type: info.Type, Title: info.Title, Status: info.Status,
					})
				}
			}
		}
	}

	// If no project bound, bind to the system "Unassigned" project.
	if session.ProjectID == "" {
		unassignedProject, err := s.getOrCreateUnassignedProject()
		if err != nil {
			log.Printf("[chat] Failed to get/create unassigned project: %v", err)
			s.Hub.Emit(sessionID, ChatEvent{Type: "error", Message: "failed to initialize project"})
			return
		}
		pid := unassignedProject.ID
		if _, err := s.Store.UpdateSession(sessionID, UpdateSessionRequest{ProjectID: &pid}); err != nil {
			log.Printf("[chat] Failed to bind session to unassigned project: %v", err)
		}
		session.ProjectID = unassignedProject.ID
		log.Printf("[chat] Bound session %s to unassigned project %s", sessionID, unassignedProject.ID)
	}

	// Get project (guaranteed to exist at this point)
	project, err := s.getProjectWithLocalPath(session.ProjectID)
	if err != nil || project == nil {
		s.Hub.Emit(sessionID, ChatEvent{Type: "error", Message: "project not found"})
		return
	}

	projectWorkDir := project.GitRepoPath
	if projectWorkDir == "" {
		projectWorkDir = "."
	}

	// Create worktree on first message if requested (worktree_mode="worktree", path not yet set).
	workDir := s.ensureWorktree(sessionID, session, projectWorkDir)
	if workDir == "" {
		workDir = projectWorkDir
	}

	// Build contexts. A forked session on its very first turn has no
	// AgentSessionID yet but inherits its parent's history (and therefore
	// the static context that was injected on the parent's first turn) via
	// the CLI fork. So treat fork-first-turn as a continuation, not a new
	// session, and skip re-injecting the static context.
	var staticContext string
	isFirstForkTurn := session.AgentSessionID == "" && session.ForkedFromSessionID != ""
	isNewSession := session.AgentSessionID == "" && !isFirstForkTurn
	if isNewSession {
		staticContext = agent.BuildStaticContext(agent.ProjectInfo{
			ID:          project.ID,
			Name:        project.Name,
			Description: project.Description,
			GitRepoPath: workDir,
		}, sessionID)
		if s.nextActionsEnabled() {
			staticContext += "\n\n" + agent.NextActionsInstruction
		}
	}

	dynamicContext := agent.BuildDynamicContext(&agent.ProjectInfo{
		ID:          project.ID,
		Name:        project.Name,
		Description: project.Description,
		GitRepoPath: workDir,
	}, refTasks, refAssets)

	log.Printf("[chat] Agent request for session %s (project: %s, new: %v, refs: %d tasks, %d assets)",
		sessionID, project.Name, isNewSession, len(refTasks), len(refAssets))

	if s.SessionManager.IsActive(sessionID) {
		s.enqueueMessage(sessionID, message, imageFilenames)
		return
	}

	s.SessionManager.SetActive(sessionID, true)
	s.Hub.SetActive(sessionID)

	// Persist user message
	if len(parts) > 0 {
		if _, err := s.Store.CreateMessage(sessionID, "user", parts, MessageMetadata{}); err != nil {
			log.Printf("[chat] Failed to persist user message (with parts): %v", err)
		}
	} else {
		if _, err := s.Store.CreateUserMessage(sessionID, message); err != nil {
			log.Printf("[chat] Failed to persist user message: %v", err)
		}
	}
	// Stamp the effective agent onto the session row before promoting it, so
	// every session ends up with a concrete agent the first time it is used.
	s.persistSessionAgent(session)
	// Promote a scratch (hidden) session in place when the user sends their
	// first message. Broadcast a session change so every connected client
	// pulls the freshly-visible session into their sidebar list.
	if promoted, err := s.Store.UnhideSession(sessionID); err != nil {
		log.Printf("[chat] Failed to unhide session %s: %v", sessionID, err)
	} else if promoted {
		s.Hub.NotifyChange("session", sessionID, "unhidden")
	}
	if s.MarketplaceStats != nil {
		s.MarketplaceStats.RecordMessage()
	}

	// Notify browsers about new user message (needed for Lark-injected messages;
	// harmless for UI-sent messages since the frontend already has them locally)
	s.Hub.Emit(sessionID, ChatEvent{Type: "user_message", Content: message})

	imagePaths := s.resolveImagePaths(sessionID, imageFilenames)

	go func() {
		released := false
		// interjectGen records the generation of the injector THIS turn
		// registered (0 = never). This defer runs after processNextQueued has
		// potentially started the next turn, so removal must be scoped to our
		// own registration or it would strip the successor's injector.
		var interjectGen atomic.Uint64
		defer func() {
			if !released {
				s.SessionManager.SetActive(sessionID, false)
				s.Hub.ClearActive(sessionID)
			}
			s.SessionManager.RemoveInterjector(sessionID, interjectGen.Load())
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Hour)
		defer func() {
			if !released {
				cancel()
				s.SessionManager.RemoveCancel(sessionID)
			}
		}()
		s.SessionManager.SetCancel(sessionID, cancel)

		// Create an empty assistant message upfront so partial progress survives
		// a process restart.
		assistantMsg, err := s.Store.CreateMessage(sessionID, "assistant", []MessagePart{}, MessageMetadata{})
		if err != nil {
			log.Printf("[chat] Failed to create assistant message: %v", err)
			s.Hub.Emit(sessionID, ChatEvent{Type: "error", Message: "failed to create message"})
			return
		}
		assistantMsgID := assistantMsg.ID

		var mu sync.Mutex
		var msgParts []MessagePart
		var metadata MessageMetadata
		dirty := false

		// wfSnapshots accumulates dynamic-workflow progress deltas per Workflow
		// tool_use_id (merge logic in workflow_progress.go). Only touched from
		// the serial event handler below, so it needs no separate lock.
		wfSnapshots := map[string]*wfSnapshot{}

		startTime := time.Now()
		toolCallCount := 0

		appendText := func(text string) {
			mu.Lock()
			defer mu.Unlock()
			if len(msgParts) > 0 {
				last := &msgParts[len(msgParts)-1]
				if last.Type == "text" {
					last.Content += text
					dirty = true
					return
				}
			}
			msgParts = append(msgParts, MessagePart{
				Type:    "text",
				Content: text,
			})
			dirty = true
		}

		// Periodic flush: save accumulated parts to DB every 2 seconds (local mode only).
		flushTicker := time.NewTicker(2 * time.Second)
		flushStop := make(chan struct{})
		flush := func() {
			if assistantMsgID == "" {
				return
			}
			mu.Lock()
			if !dirty {
				mu.Unlock()
				return
			}
			partsCopy := make([]MessagePart, len(msgParts))
			copy(partsCopy, msgParts)
			metaCopy := metadata
			dirty = false
			mu.Unlock()

			if err := s.Store.UpdateMessageParts(assistantMsgID, partsCopy, metaCopy); err != nil {
				log.Printf("[chat] Flush failed: %v", err)
			}
		}
		go func() {
			for {
				select {
				case <-flushTicker.C:
					flush()
				case <-flushStop:
					return
				}
			}
		}()

		sessionAgent, agentNameUsed := s.ResolveAgent(session)
		log.Printf("[chat] Using agent: %s for session %s", agentNameUsed, sessionID)

		// Merge session-level features with message features.
		activeFeatures := s.mergeSessionFeatures(sessionID, features)

		// E2E testing: ensure per-session Chrome MCP is started and agent supports it.
		var chromeMCPURL string
		var chromeMCPToolNames []string
		if containsFeature(activeFeatures, "chrome_devtools") {
			if !sessionAgent.Capabilities().ChromeDevTools {
				s.Hub.Emit(sessionID, ChatEvent{
					Type:    "error",
					Message: "E2E browser testing is not supported by the current agent. Please switch in Settings.",
				})
				return
			}
			if s.ChromeManager != nil {
				chromeSrv := s.ChromeManager.GetOrCreate(sessionID)
				if err := chromeSrv.EnsureStarted(ctx); err != nil {
					log.Printf("[chat] Chrome MCP start failed for session %s: %v", sessionID, err)
					s.Hub.Emit(sessionID, ChatEvent{
						Type:    "error",
						Message: "Failed to start Chrome browser: " + err.Error(),
					})
					return
				}
				chromeMCPURL = chromeSrv.URL()
				chromeMCPToolNames = chromeSrv.ToolNames()
			}
		}

		// Collect installed MCP servers and key→package map in a single store query.
		// Pass session.ProjectID so project-specific MCPs take precedence over global ones.
		extraMCPs, mcpKeyToPkg := s.resolveInstalledMCPsAndMap(session.ProjectID)
		pluginDirs := s.resolveInstalledPluginDirs(session.ProjectID)
		// toolIDToName maps tool_id → tool_name for EventToolStart events so that
		// EventToolResult (which carries only tool_id) can look up the name.
		toolIDToName := make(map[string]string)

		eventHandler := func(event agent.StreamEvent) {
			switch event.Type {
			case agent.EventTextDelta:
				appendText(event.Content)
				s.Hub.Emit(sessionID, ChatEvent{
					Type:    "text_delta",
					Content: event.Content,
				})

			case agent.EventToolStart:
				mu.Lock()
				toolCallCount++
				msgParts = append(msgParts, MessagePart{
					Type:         "tool_use",
					ToolCallID:   event.ToolID,
					ToolName:     event.ToolName,
					Input:        event.Input,
					State:        "running",
					ParentToolID: event.ParentToolID,
				})
				// Record tool_id → tool_name so EventToolResult can attribute the call
				if event.ToolID != "" && event.ToolName != "" {
					toolIDToName[event.ToolID] = event.ToolName
				}
				dirty = true
				mu.Unlock()
				s.Hub.Emit(sessionID, ChatEvent{
					Type:         "tool_start",
					Tool:         event.ToolName,
					ToolID:       event.ToolID,
					Input:        event.Input,
					ParentToolID: event.ParentToolID,
				})

				// Intercept visualize_show_widget: parse input and emit a widget event + part.
				if strings.HasSuffix(event.ToolName, "visualize_show_widget") && event.Input != "" {
					var widgetInput struct {
						Title           string   `json:"title"`
						WidgetCode      string   `json:"widget_code"`
						LoadingMessages []string `json:"loading_messages"`
					}
					if err := json.Unmarshal([]byte(event.Input), &widgetInput); err == nil && widgetInput.WidgetCode != "" {
						mu.Lock()
						msgParts = append(msgParts, MessagePart{
							Type:            "widget",
							Title:           widgetInput.Title,
							WidgetCode:      widgetInput.WidgetCode,
							LoadingMessages: widgetInput.LoadingMessages,
						})
						dirty = true
						mu.Unlock()
						s.Hub.Emit(sessionID, ChatEvent{
							Type:            "widget",
							WidgetTitle:     widgetInput.Title,
							WidgetCode:      widgetInput.WidgetCode,
							LoadingMessages: widgetInput.LoadingMessages,
						})
					}
				}

			case agent.EventToolResult:
				mu.Lock()
				for i := range msgParts {
					if msgParts[i].Type == "tool_use" && msgParts[i].ToolCallID == event.ToolID {
						msgParts[i].State = "complete"
					}
				}
				msgParts = append(msgParts, MessagePart{
					Type:       "tool_result",
					ToolCallID: event.ToolID,
					Output:     event.Output,
					IsError:    event.IsError,
				})
				// Track MCP call stats: mcp__<key>__<tool_name> → package name
				if !event.IsError && s.MarketplaceStats != nil && len(mcpKeyToPkg) > 0 {
					if toolName, ok := toolIDToName[event.ToolID]; ok {
						if strings.HasPrefix(toolName, "mcp__") {
							parts := strings.SplitN(toolName, "__", 3)
							if len(parts) >= 2 {
								if pkgName, ok := mcpKeyToPkg[parts[1]]; ok {
									s.MarketplaceStats.RecordCall(pkgName, "mcp")
								}
							}
						}
					}
				}
				dirty = true
				mu.Unlock()
				s.Hub.Emit(sessionID, ChatEvent{
					Type:         "tool_result",
					ToolID:       event.ToolID,
					Output:       event.Output,
					IsError:      event.IsError,
					ParentToolID: event.ParentToolID,
				})

			case agent.EventThinking:
				mu.Lock()
				lastIdx := -1
				for i := len(msgParts) - 1; i >= 0; i-- {
					if msgParts[i].Type == "thinking" {
						lastIdx = i
						break
					}
					if msgParts[i].Type != "thinking" {
						break
					}
				}
				if lastIdx >= 0 {
					msgParts[lastIdx].Content += event.Content
				} else {
					msgParts = append(msgParts, MessagePart{
						Type:    "thinking",
						Content: event.Content,
					})
				}
				dirty = true
				mu.Unlock()
				s.Hub.Emit(sessionID, ChatEvent{
					Type:    "thinking_delta",
					Content: event.Content,
				})

			case agent.EventSubTaskStart:
				taskID, _ := event.Metadata["task_id"].(string)
				log.Printf("[chat] Sub-agent start: %s (task_id=%s)", event.Content, taskID)
				s.Hub.Emit(sessionID, ChatEvent{
					Type:    "subtask_start",
					Content: event.Content,
					TaskID:  taskID,
				})

			case agent.EventSubTaskProgress:
				taskID, _ := event.Metadata["task_id"].(string)
				toolUseID, _ := event.Metadata["tool_use_id"].(string)
				raw, _ := event.Metadata["workflow_progress"].(json.RawMessage)
				if toolUseID != "" && len(raw) > 0 {
					// Dynamic Workflow tool: merge the delta into the full
					// snapshot, persist it on the Workflow tool_use part (so it
					// survives refresh), and push the full tree to clients.
					snap := wfSnapshots[toolUseID]
					if snap == nil {
						snap = newWFSnapshot()
						wfSnapshots[toolUseID] = snap
					}
					snap.merge(raw)
					full := snap.snapshotJSON()
					attachWorkflowProgress(&mu, msgParts, toolUseID, full, &dirty)
					s.Hub.Emit(sessionID, ChatEvent{
						Type:             "subtask_progress",
						Content:          event.Content,
						TaskID:           taskID,
						ToolID:           toolUseID,
						WorkflowProgress: full,
					})
				} else {
					// Non-workflow background task (Task tool): unchanged — the
					// sub-agent's own tool calls stream as nested tool_start events.
					s.Hub.Emit(sessionID, ChatEvent{
						Type:    "subtask_progress",
						Content: event.Content,
						TaskID:  taskID,
					})
				}

			case agent.EventSubTaskDone:
				taskID, _ := event.Metadata["task_id"].(string)
				toolUseID, _ := event.Metadata["tool_use_id"].(string)
				status, _ := event.Metadata["status"].(string)
				raw, _ := event.Metadata["workflow_progress"].(json.RawMessage)
				log.Printf("[chat] Sub-agent done: %s (task_id=%s)", event.Content, taskID)
				snap := wfSnapshots[toolUseID]
				// Only treat this as a workflow when we actually saw workflow
				// progress: a snapshot exists from prior task_progress, or this
				// done event itself carries workflow_progress. Regular Task
				// sub-agents have a tool_use_id but no workflow data, so they
				// fall through to the unchanged legacy emit below.
				if toolUseID != "" && (snap != nil || len(raw) > 0) {
					if snap == nil {
						snap = newWFSnapshot()
						wfSnapshots[toolUseID] = snap
					}
					snap.merge(raw)
					if status != "" {
						snap.status = status
					}
					if event.Content != "" {
						snap.summary = event.Content
					}
					full := snap.snapshotJSON()
					attachWorkflowProgress(&mu, msgParts, toolUseID, full, &dirty)
					s.Hub.Emit(sessionID, ChatEvent{
						Type:             "subtask_done",
						Content:          event.Content,
						TaskID:           taskID,
						ToolID:           toolUseID,
						WorkflowProgress: full,
					})
				} else {
					// Regular background sub-agent (Agent tool) finished. Its own
					// tool_result returned immediately at launch and left the card
					// "running", so complete it now — both in the persisted parts
					// (so a mid-turn refresh stays consistent) and live to clients.
					if toolUseID != "" {
						mu.Lock()
						for i := range msgParts {
							if msgParts[i].Type == "tool_use" && msgParts[i].ToolCallID == toolUseID && msgParts[i].State == "running" {
								msgParts[i].State = "complete"
								dirty = true
							}
						}
						mu.Unlock()
					}
					s.Hub.Emit(sessionID, ChatEvent{
						Type:    "subtask_done",
						Content: event.Content,
						TaskID:  taskID,
						ToolID:  toolUseID,
					})
				}

			case agent.EventSystem:
				log.Printf("[chat] System event: %s", event.Content)
				s.Hub.Emit(sessionID, ChatEvent{
					Type:    "system",
					Content: event.Content,
				})

			case agent.EventContextUsage:
				usage := &ContextUsage{}
				m := event.Metadata
				if v, ok := m["model"].(string); ok {
					usage.Model = v
				}
				intField := func(k string) int {
					if v, ok := m[k].(int); ok {
						return v
					}
					return 0
				}
				usage.UsedTokens = intField("usedTokens")
				usage.InputTokens = intField("inputTokens")
				usage.OutputTokens = intField("outputTokens")
				if v := intField("cacheReadInputTokens"); v > 0 {
					usage.CachedInputTokens = v
				} else if v := intField("cachedInputTokens"); v > 0 {
					usage.CachedInputTokens = v
				}
				usage.ReasoningOutputTokens = intField("reasoningOutputTokens")
				usage.ContextWindow = intField("contextWindow")
				usage.MaxOutputTokens = intField("maxOutputTokens")
				s.Hub.Emit(sessionID, ChatEvent{
					Type:         "context_usage",
					ContextUsage: usage,
				})

			case agent.EventSessionInfo:
				info := &SessionInfo{}
				m := event.Metadata
				if v, ok := m["model"].(string); ok {
					info.Model = v
				}
				if v, ok := m["permissionMode"].(string); ok {
					info.PermissionMode = v
				}
				if v, ok := m["cwd"].(string); ok {
					info.Cwd = v
				}
				if v, ok := m["version"].(string); ok {
					info.Version = v
				}
				if v, ok := m["outputStyle"].(string); ok {
					info.OutputStyle = v
				}
				s.Hub.Emit(sessionID, ChatEvent{
					Type:        "session_info",
					SessionInfo: info,
				})

			case agent.EventError:
				log.Printf("[chat] Error event: %s", event.Content)
				s.Hub.Emit(sessionID, ChatEvent{
					Type:    "error",
					Message: event.Content,
				})

			case agent.EventDone:
				mu.Lock()
				metadata.SessionID = event.SessionID
				if event.Metadata != nil {
					if v, ok := event.Metadata["inputTokens"].(int); ok {
						metadata.InputTokens = v
					}
					if v, ok := event.Metadata["outputTokens"].(int); ok {
						metadata.OutputTokens = v
					}
					if v, ok := event.Metadata["totalCostUsd"].(float64); ok {
						metadata.TotalCostUSD = v
					}
					if v, ok := event.Metadata["cacheReadInputTokens"].(int); ok {
						metadata.CacheReadInputTokens = v
					}
					if v, ok := event.Metadata["cacheCreationInputTokens"].(int); ok {
						metadata.CacheCreationInputTokens = v
					}
				}
				dirty = true
				mu.Unlock()
			}
		}

		// Build the UserInputHandler for interactive tool support (AskUserQuestion).
		// When the CLI sends a can_use_tool control_request for an interactive tool:
		// 1. Emits a user_input_request WS event to the frontend
		// 2. Blocks on a channel until the user responds via tool_response WS message
		// 3. Returns the user's response so the agent can write control_response to stdin
		userInputHandler := func(requestID, toolName, input string) (string, error) {
			log.Printf("[chat] Interactive tool request: session=%s tool=%s id=%s", sessionID, toolName, requestID)

			s.Hub.Emit(sessionID, ChatEvent{
				Type:   "user_input_request",
				ToolID: requestID,
				Tool:   toolName,
				Input:  input,
			})

			// Mirror the request so a reloaded client can restore the dialog.
			setPendingUserInput(sessionID, PendingUserInput{
				ToolUseID: requestID,
				ToolName:  toolName,
				Input:     input,
			})
			defer clearPendingUserInput(sessionID, requestID)

			responseCh := make(chan toolResponse, 1)
			setToolResponseChan(sessionID, requestID, responseCh)
			defer removeToolResponseChan(sessionID, requestID)

			inputTimeout := time.NewTimer(1 * time.Hour)
			defer inputTimeout.Stop()

			select {
			case resp := <-responseCh:
				if resp.Skip {
					log.Printf("[chat] User skipped tool %s in session %s", requestID, sessionID)
					return "", fmt.Errorf("user skipped the question")
				}
				log.Printf("[chat] Got user response for tool %s: %d bytes", requestID, len(resp.Response))
				return resp.Response, nil
			case <-inputTimeout.C:
				log.Printf("[chat] User input timeout for tool %s in session %s", requestID, sessionID)
				return "", fmt.Errorf("user input timeout after 1 hour")
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		// On the very first turn of a forked session, build the agent-level
		// fork hints from the parent's persisted CLI session id and the cut
		// point recorded at fork-creation time. Once the CLI returns the new
		// session id, AgentSessionID gets populated and subsequent turns
		// take the normal resume path.
		//
		// If the parent's CLI session id is no longer available (parent
		// purged, or its agent_session_id got cleared by an expiry retry),
		// surface a synthetic error through the normal agentErr path —
		// which writes the error text to the assistant stub message and
		// emits an error event — instead of silently dropping the fork
		// (the static context built above is empty, so a "fresh new
		// session" fallback would produce a model with no project context).
		var forkContext *agent.ForkContext
		var forkPreflightErr error
		if isFirstForkTurn {
			parentSess, perr := s.Store.GetSession(session.ForkedFromSessionID)
			switch {
			case perr != nil:
				log.Printf("[chat] Fork %s: failed to load parent %s: %v", sessionID, session.ForkedFromSessionID, perr)
				forkPreflightErr = fmt.Errorf("fork failed: could not load parent session: %w", perr)
			case parentSess == nil:
				log.Printf("[chat] Fork %s: parent %s no longer exists", sessionID, session.ForkedFromSessionID)
				forkPreflightErr = fmt.Errorf("fork failed: parent session no longer exists")
			case parentSess.AgentSessionID == "":
				log.Printf("[chat] Fork %s: parent %s has no agent_session_id (likely cleared by expiry retry)",
					sessionID, session.ForkedFromSessionID)
				forkPreflightErr = fmt.Errorf("fork failed: parent session has no active CLI session to fork from")
			case session.ForkedAtAgentCheckpoint == "":
				forkPreflightErr = fmt.Errorf("fork failed: fork checkpoint is missing")
			default:
				forkContext = &agent.ForkContext{
					SessionID:  parentSess.AgentSessionID,
					Checkpoint: session.ForkedAtAgentCheckpoint,
				}
			}
		}

		// registerInterject exposes a backend-neutral active-turn control handle
		// for later queue-jumping messages. The generation token feeds the
		// deferred removal above so this turn can only unregister its own
		// injector, never a successor turn's.
		registerInterject := func(inject func(content string) error) {
			interjectGen.Store(s.SessionManager.SetInterjector(sessionID, inject))
		}

		var agentResult agent.Result
		var agentErr error
		if forkPreflightErr != nil {
			agentErr = forkPreflightErr
		} else {
			agentResult, agentErr = sessionAgent.SendMessage(
				ctx,
				agent.Request{
					ConversationID:     sessionID,
					SessionID:          session.AgentSessionID,
					WorkDir:            workDir,
					Model:              session.Model,
					Effort:             session.Effort,
					StaticContext:      staticContext,
					DynamicContext:     dynamicContext,
					Message:            message,
					Images:             imagePaths,
					Features:           activeFeatures,
					ChromeMCPURL:       chromeMCPURL,
					ChromeMCPToolNames: chromeMCPToolNames,
					ExtraMCPServers:    extraMCPs,
					PluginDirs:         pluginDirs,
					UserInputHandler:   agent.UserInputHandler(userInputHandler),
					Fork:               forkContext,
					RegisterInterject:  registerInterject,
				},
				eventHandler,
			)
		}

		// Auto-retry with a fresh session when the stored agent session has expired
		if errors.Is(agentErr, agent.ErrSessionExpired) && session.AgentSessionID != "" {
			log.Printf("[chat] Agent session expired for %s, clearing and retrying as new session", sessionID)

			if err := s.Store.UpdateSessionAgentID(sessionID, ""); err != nil {
				log.Printf("[chat] Failed to clear agent session ID: %v", err)
			}
			session.AgentSessionID = ""

			s.Hub.Emit(sessionID, ChatEvent{
				Type:    "system",
				Content: "Previous session expired. Retrying with a fresh one...",
			})

			newStaticContext := agent.BuildStaticContext(agent.ProjectInfo{
				ID:          project.ID,
				Name:        project.Name,
				Description: project.Description,
				GitRepoPath: workDir,
			}, sessionID)
			if s.nextActionsEnabled() {
				newStaticContext += "\n\n" + agent.NextActionsInstruction
			}

			agentResult, agentErr = sessionAgent.SendMessage(
				ctx,
				agent.Request{
					ConversationID:     sessionID,
					SessionID:          "",
					WorkDir:            workDir,
					Model:              session.Model,
					Effort:             session.Effort,
					StaticContext:      newStaticContext,
					DynamicContext:     dynamicContext,
					Message:            message,
					Images:             imagePaths,
					Features:           activeFeatures,
					ChromeMCPURL:       chromeMCPURL,
					ChromeMCPToolNames: chromeMCPToolNames,
					ExtraMCPServers:    extraMCPs,
					PluginDirs:         pluginDirs,
					UserInputHandler:   agent.UserInputHandler(userInputHandler),
					RegisterInterject:  registerInterject,
				},
				eventHandler,
			)
		}

		// Stop periodic flusher.
		flushTicker.Stop()
		close(flushStop)

		durationMs := time.Since(startTime).Milliseconds()

		if agentErr != nil {
			if ctx.Err() != nil {
				log.Printf("[chat] Agent stopped by user")
				appendText("\n\n*[Stopped]*")
			} else {
				log.Printf("[chat] Agent error: %v", agentErr)
				s.Hub.Emit(sessionID, ChatEvent{
					Type:    "error",
					Message: agentErr.Error(),
				})
				mu.Lock()
				msgParts = append(msgParts, MessagePart{
					Type:    "text",
					Content: "Error: " + agentErr.Error(),
				})
				dirty = true
				mu.Unlock()
			}
		}

		newAgentSessionID := agentResult.SessionID
		if newAgentSessionID != "" && newAgentSessionID != session.AgentSessionID {
			if err := s.Store.UpdateSessionAgentID(sessionID, newAgentSessionID); err != nil {
				log.Printf("[chat] Failed to update agent session ID: %v", err)
			}
			session.AgentSessionID = newAgentSessionID
			mu.Lock()
			metadata.SessionID = newAgentSessionID
			dirty = true
			mu.Unlock()
		}

		mu.Lock()
		metadata.DurationMs = durationMs
		metadata.ToolCalls = toolCallCount
		// The checkpoint travels only on the adapter's Result — set on every
		// path, including stopped/error turns that never emit EventDone.
		metadata.AgentCheckpoint = agentResult.Checkpoint
		finalParts := make([]MessagePart, len(msgParts))
		copy(finalParts, msgParts)
		finalMeta := metadata
		mu.Unlock()

		// Final persist: save full parts to the main message table. Always
		// update the pre-created message (even if empty) so it is never left
		// in an un-finalized state that shows as "Thinking" after reload.
		if err := s.Store.UpdateMessageParts(assistantMsgID, finalParts, finalMeta); err != nil {
			log.Printf("[chat] Failed to persist final assistant message: %v", err)
		}
		if s.MarketplaceStats != nil && len(finalParts) > 0 {
			s.MarketplaceStats.RecordMessage()
		}

		stats := &ExecutionStats{
			DurationMs:               durationMs,
			ToolCalls:                toolCallCount,
			InputTokens:              metadata.InputTokens,
			OutputTokens:             metadata.OutputTokens,
			CacheReadInputTokens:     metadata.CacheReadInputTokens,
			CacheCreationInputTokens: metadata.CacheCreationInputTokens,
			TotalCostUSD:             metadata.TotalCostUSD,
		}

		log.Printf("[chat] Completed: %.1fs, %d tool calls, %d in/%d out tokens, $%.4f",
			float64(durationMs)/1000.0, toolCallCount,
			metadata.InputTokens, metadata.OutputTokens, metadata.TotalCostUSD)

		// Clear the active flag *before* emitting `done`. The flag is what
		// GET /messages reports as `active`, and a client that reloads mid-turn
		// uses it to decide whether to keep the last assistant message in the
		// streaming state. Emitting `done` first opens a window where a client
		// can observe active=true while the `done` it needs has already been
		// broadcast (the hub never replays), leaving the message stuck
		// streaming. Ordering clear-then-emit guarantees: active=true ⇒ `done`
		// not yet sent, so any subscribed socket still receives it.
		s.SessionManager.SetActive(sessionID, false)
		s.Hub.ClearActive(sessionID)

		s.Hub.Emit(sessionID, ChatEvent{
			Type:            "done",
			SessionID:       session.AgentSessionID,
			MessageID:       assistantMsgID,
			AgentCheckpoint: finalMeta.AgentCheckpoint,
			Stats:           stats,
		})
		s.Hub.NotifyChange("session", sessionID, "done")
		s.SessionManager.RemoveCancel(sessionID)
		cancel()
		released = true

		s.processNextQueued(sessionID)
	}()
}

// resolveImagePaths converts image filenames to absolute paths, filtering out any that don't exist.
func (s *Server) resolveImagePaths(sessionID string, filenames []string) []string {
	var paths []string
	for _, fname := range filenames {
		p := filepath.Join(s.sessionImageDir(sessionID), fname)
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if _, err := os.Stat(abs); err == nil {
			paths = append(paths, abs)
		}
	}
	return paths
}

// ---------------------------------------------------------------------------
// Message Queue
// ---------------------------------------------------------------------------

// handleInterject delivers a queue-jumping ("interjection") message into a
// session's running agent turn through the backend's registered control
// handle. When no live turn accepts it, the request falls back to the ordinary
// send/queue path.
func (s *Server) handleInterject(sessionID, message string, parts []MessagePart, imageFilenames []string, features []string) {
	imagePaths := s.resolveImagePaths(sessionID, imageFilenames)
	// Wrap with the interjection framing so the model knows this message arrived
	// mid-turn (finish current task, then address it). No per-session static/
	// dynamic context — that already lives in the running turn.
	content := agent.BuildInterjectPrompt(message, imagePaths)

	ok, err := s.SessionManager.Interject(sessionID, content)
	if err != nil && errors.Is(err, agent.ErrInterjectOutcomeUnknown) {
		// The message may already be folded into the live turn; re-sending it
		// through the queue path would risk delivering it twice. Persist it
		// (so the transcript has no gap if it did land) and surface the
		// uncertainty, letting the user decide whether to resend.
		log.Printf("[interject] delivery outcome unknown for %s, not re-sending: %v", sessionID, err)
		persistParts := parts
		if len(persistParts) == 0 {
			persistParts = []MessagePart{{Type: "text", Content: message}}
		}
		if _, perr := s.Store.CreateMessage(sessionID, "user", persistParts, MessageMetadata{Interjected: true}); perr != nil {
			log.Printf("[interject] Failed to persist unknown-outcome message: %v", perr)
		}
		s.Hub.Emit(sessionID, ChatEvent{Type: "user_message", Content: message})
		s.Hub.Emit(sessionID, ChatEvent{
			Type:    "error",
			Message: "Interjection may not have reached the running turn. If the agent doesn't pick it up, send it again.",
		})
		return
	}
	if !ok || err != nil {
		if err != nil {
			log.Printf("[interject] live injection failed for %s, falling back to queue/send: %v", sessionID, err)
		}
		s.handleUserMessage(sessionID, message, parts, imageFilenames, features)
		return
	}

	// Delivered into the live turn. Persist + echo the user message so it shows
	// in the transcript and survives a reload. The frontend already added it
	// optimistically and ignores this echo while streaming.
	persistParts := parts
	if len(persistParts) == 0 {
		persistParts = []MessagePart{{Type: "text", Content: message}}
	}
	if _, err := s.Store.CreateMessage(sessionID, "user", persistParts, MessageMetadata{Interjected: true}); err != nil {
		log.Printf("[interject] Failed to persist user message: %v", err)
	}
	s.Hub.Emit(sessionID, ChatEvent{Type: "user_message", Content: message})
	log.Printf("[interject] Delivered queue-jump message into live turn: session=%s", sessionID)
}

// enqueueMessage auto-queues a message when the session is busy.
func (s *Server) enqueueMessage(sessionID, message string, imageFilenames []string) {
	req := store.CreateQueuedMessageRequest{Text: message}
	for _, img := range imageFilenames {
		req.Images = append(req.Images, store.QueuedMessageImage{Filename: img})
	}
	if _, err := s.Store.CreateQueuedMessage(sessionID, req); err != nil {
		log.Printf("[queue] Failed to auto-queue message: %v", err)
		s.Hub.Emit(sessionID, ChatEvent{Type: "error", Message: "failed to queue message"})
		return
	}
	log.Printf("[queue] Auto-queued message (session busy): %s", sessionID)
	s.Hub.Emit(sessionID, ChatEvent{Type: "queue_updated"})
}

// processNextQueued pops and executes the next queued message for a session.
func (s *Server) processNextQueued(sessionID string) {
	if s.SessionManager.IsActive(sessionID) {
		return
	}
	if item := s.popPriorityItem(sessionID); item != nil {
		log.Printf("[queue] Priority-dequeue: session=%s", sessionID)
		s.executeQueuedItem(sessionID, item)
		return
	}
	queued, err := s.Store.PopQueuedMessage(sessionID)
	if err != nil {
		log.Printf("[queue] Failed to pop queued message: session=%s err=%v", sessionID, err)
		return
	}
	if queued == nil {
		return
	}
	log.Printf("[queue] Auto-dequeue: session=%s text_len=%d", sessionID, len(queued.Text))
	s.executeQueuedItem(sessionID, queued)
}

func (s *Server) executeQueuedItem(sessionID string, item *store.QueuedMessage) {
	s.Hub.Emit(sessionID, ChatEvent{Type: "queue_updated"})

	var imageFilenames []string
	for _, img := range item.Images {
		if img.Filename != "" {
			imageFilenames = append(imageFilenames, img.Filename)
		}
	}
	s.handleUserMessage(sessionID, item.Text, nil, imageFilenames, nil)
}

// RunQueuedNow pops a specific queue item and triggers immediate execution.
// If the session is busy, the item is stored as a priority item and the
// current agent is stopped so processNextQueued picks it up.
func (s *Server) RunQueuedNow(sessionID, queuedID string) error {
	item, err := s.Store.PopQueuedMessageByID(sessionID, queuedID)
	if err != nil {
		return fmt.Errorf("pop queued message: %w", err)
	}
	if item == nil {
		return fmt.Errorf("queued message %s not found", queuedID)
	}
	s.Hub.Emit(sessionID, ChatEvent{Type: "queue_updated"})

	if s.SessionManager.IsActive(sessionID) {
		s.setPriorityItem(sessionID, item)
		s.SessionManager.Cancel(sessionID)
	} else {
		s.executeQueuedItem(sessionID, item)
	}
	return nil
}

// StartQueuePoller runs a background loop that processes pending queue items
// for idle sessions.
func (s *Server) StartQueuePoller(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollQueues()
		}
	}
}

func (s *Server) pollQueues() {
	sessionIDs, err := s.Store.SessionsWithQueuedMessages()
	if err != nil {
		log.Printf("[queue] Poll error: %v", err)
		return
	}
	for _, sid := range sessionIDs {
		if !s.SessionManager.IsActive(sid) {
			s.processNextQueued(sid)
		}
	}
}

// priority items — per-session in-memory override for RunQueuedNow
var (
	priorityMu    sync.Mutex
	priorityItems = make(map[string]*store.QueuedMessage)
)

func (s *Server) setPriorityItem(sessionID string, item *store.QueuedMessage) {
	priorityMu.Lock()
	defer priorityMu.Unlock()
	priorityItems[sessionID] = item
}

func (s *Server) popPriorityItem(sessionID string) *store.QueuedMessage {
	priorityMu.Lock()
	defer priorityMu.Unlock()
	item := priorityItems[sessionID]
	delete(priorityItems, sessionID)
	return item
}

// getOrCreateUnassignedProject returns the system "Unassigned" project, creating it if needed.
func (s *Server) getOrCreateUnassignedProject() (*Project, error) {
	const unassignedProjectName = "(Unassigned)"

	projects, err := s.Store.ListProjects()
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		if p.Name == unassignedProjectName {
			return &p, nil
		}
	}

	tmpDir := filepath.Join(s.DataDir, "nexbox-unassigned")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return nil, err
	}

	return s.Store.CreateProject(CreateProjectRequest{
		Name:           unassignedProjectName,
		Description:    "System project for sessions without a specific project",
		GitRepoPath:    tmpDir,
		AutoManagedDir: true,
	})
}

// mergeSessionFeatures merges new features from the current message with
// features already persisted on the session row. The merged set is written
// back to the DB so it survives server restarts.
func (s *Server) mergeSessionFeatures(sessionID string, msgFeatures []string) []string {
	sess, err := s.Store.GetSession(sessionID)
	if err != nil || sess == nil {
		return msgFeatures
	}

	merged := make(map[string]struct{})
	if sess.Features != "" {
		for _, f := range strings.Split(sess.Features, ",") {
			if f = strings.TrimSpace(f); f != "" {
				merged[f] = struct{}{}
			}
		}
	}
	for _, f := range msgFeatures {
		if f != "" {
			merged[f] = struct{}{}
		}
	}

	result := make([]string, 0, len(merged))
	for f := range merged {
		result = append(result, f)
	}

	newVal := strings.Join(result, ",")
	if newVal != sess.Features {
		_ = s.Store.UpdateSessionFeatures(sessionID, newVal)
	}
	return result
}

func containsFeature(features []string, name string) bool {
	for _, f := range features {
		if f == name {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Interactive tool response channels
// ---------------------------------------------------------------------------
// When the agent calls an interactive tool (e.g., AskUserQuestion), the
// UserInputHandler blocks on a channel waiting for the user's response.
// The WS handler writes the response to the channel when it receives a
// tool_response message.

// toolResponse carries a user's reply to an interactive tool call. Skip marks
// a deliberate skip, which the UserInputHandler turns into an error (tool
// denied) instead of an answer.
type toolResponse struct {
	Response string
	Skip     bool
}

var (
	toolResponseMu sync.Mutex
	// Keyed by "sessionID:toolUseID" so concurrent/stale requests can't
	// misroute a reply to the wrong pending tool call.
	toolResponseChans = make(map[string]chan toolResponse)
)

func toolResponseKey(sessionID, toolUseID string) string {
	return sessionID + ":" + toolUseID
}

func setToolResponseChan(sessionID, toolUseID string, ch chan toolResponse) {
	toolResponseMu.Lock()
	defer toolResponseMu.Unlock()
	toolResponseChans[toolResponseKey(sessionID, toolUseID)] = ch
}

func removeToolResponseChan(sessionID, toolUseID string) {
	toolResponseMu.Lock()
	defer toolResponseMu.Unlock()
	delete(toolResponseChans, toolResponseKey(sessionID, toolUseID))
}

func getToolResponseChan(sessionID, toolUseID string) chan toolResponse {
	toolResponseMu.Lock()
	defer toolResponseMu.Unlock()
	return toolResponseChans[toolResponseKey(sessionID, toolUseID)]
}

// ---------------------------------------------------------------------------
// Pending interactive request registry
// ---------------------------------------------------------------------------
// The user_input_request WS event is broadcast live and never replayed, so a
// client that reloads the page mid-question loses the dialog. We mirror each
// in-flight interactive request here while its UserInputHandler goroutine is
// blocked (up to the 1-hour timeout, with its response channel still
// registered). A reloaded client fetches this via ListSessionMessages and
// re-renders the dialog with the original tool_use_id; the existing
// tool_response routing then delivers the answer to the still-waiting handler.

// PendingUserInput is the snapshot of an interactive tool request needed to
// reconstruct the question dialog after a page reload.
type PendingUserInput struct {
	ToolUseID string `json:"toolUseId"`
	ToolName  string `json:"toolName"`
	Input     string `json:"input"`
}

var (
	pendingInputMu    sync.Mutex
	pendingUserInputs = make(map[string]PendingUserInput) // sessionID -> pending request
)

func setPendingUserInput(sessionID string, p PendingUserInput) {
	pendingInputMu.Lock()
	defer pendingInputMu.Unlock()
	pendingUserInputs[sessionID] = p
}

// clearPendingUserInput removes the pending request for a session, but only if
// it still matches toolUseID — so a newer request can't be wiped by an older
// handler's deferred cleanup.
func clearPendingUserInput(sessionID, toolUseID string) {
	pendingInputMu.Lock()
	defer pendingInputMu.Unlock()
	if p, ok := pendingUserInputs[sessionID]; ok && p.ToolUseID == toolUseID {
		delete(pendingUserInputs, sessionID)
	}
}

func getPendingUserInput(sessionID string) (PendingUserInput, bool) {
	pendingInputMu.Lock()
	defer pendingInputMu.Unlock()
	p, ok := pendingUserInputs[sessionID]
	return p, ok
}
