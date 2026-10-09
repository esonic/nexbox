package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/esonic/nexbox/agent"
	"github.com/go-chi/chi/v5"
)

// wfPrefixRegex matches the WF[xxxxx] prefix that orchestrator.go writes onto
// workflow-created session names (e.g. "WF[b54a0] my-workflow — node-label").
// The trailing whitespace is captured so the prefix can be re-applied verbatim
// in front of an auto-generated title.
var wfPrefixRegex = regexp.MustCompile(`^WF\[[A-Fa-f0-9]+\]\s*`)

// sessionNNNRegex matches the "Session 12" placeholder name assigned at
// creation time when the caller doesn't supply one.
var sessionNNNRegex = regexp.MustCompile(`^Session\s+\d+$`)

// ListSessions returns all sessions.
func (s *Server) ListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.Store.ListSessions()
	if writeStoreError(w, err, "ListSessions", "failed to list sessions") {
		return
	}
	if sessions == nil {
		sessions = []Session{}
	}

	allActiveIDs := s.Hub.ActiveSessionIDs()
	sessionIDSet := make(map[string]bool, len(sessions))
	for _, sess := range sessions {
		sessionIDSet[sess.ID] = true
	}
	activeIDs := make([]string, 0)
	for _, id := range allActiveIDs {
		if sessionIDSet[id] {
			activeIDs = append(activeIDs, id)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessions":           sessions,
		"active_session_ids": activeIDs,
	})
}

// CreateSession creates a new session.
func (s *Server) CreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// If no agent specified, use the persisted default from settings
	if req.Agent == "" {
		req.Agent = s.getDefaultAgent()
	}

	sess, err := s.Store.CreateSession(req)
	if err != nil {
		log.Printf("[sessions] Failed to create session (project=%s, agent=%s): %v", req.ProjectID, req.Agent, err)
		writeError(w, http.StatusInternalServerError, "failed to create session: "+err.Error())
		return
	}

	if s.MarketplaceStats != nil {
		s.MarketplaceStats.RecordSession()
	}

	writeJSON(w, http.StatusCreated, sess)
}

// GetSession returns a session by ID.
func (s *Server) GetSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "sessionId")
	sess, err := s.Store.GetSession(id)
	if writeStoreError(w, err, "GetSession", "failed to get session") {
		return
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	// Fill in default agent if empty
	if sess.Agent == "" {
		sess.Agent = s.AgentName
	}
	writeJSON(w, http.StatusOK, sess)
}

// UpdateSession updates a session.
func (s *Server) UpdateSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "sessionId")
	var req UpdateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Model != nil {
		existing, err := s.Store.GetSession(id)
		if writeStoreError(w, err, "GetSession", "failed to load session") {
			return
		}
		if existing == nil {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		if *req.Model != existing.Model {
			backend, _ := s.ResolveAgent(existing)
			if backend != nil && backend.Capabilities().ModelSwitchMode == agent.ModelSwitchNewSessionOnly {
				messages, err := s.Store.ListMessages(id)
				if writeStoreError(w, err, "ListMessages", "failed to check session messages") {
					return
				}
				if existing.AgentSessionID != "" || len(messages) > 0 {
					writeError(w, http.StatusBadRequest, "this agent only allows choosing a model before the conversation starts")
					return
				}
			}
		}
	}
	sess, err := s.Store.UpdateSession(id, req)
	if writeStoreError(w, err, "UpdateSession", "failed to update session") {
		return
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

// AutoTitleSessionRequest is the JSON body for POST /api/sessions/{id}/auto-title.
type AutoTitleSessionRequest struct {
	// Message is the user's first message text. Used as the sole input to the
	// LLM title generation; refs/attachments are intentionally ignored to keep
	// the prompt short and cheap.
	Message string `json:"message"`
}

// AutoTitleSession generates a short, semantic title for a session based on
// the user's first message and writes it back. Workflow-created sessions whose
// names carry a WF[xxxxx] prefix keep the prefix; the LLM-generated title is
// appended after it. Customized names (anything that is not the default
// placeholder and not WF-prefixed) are left untouched.
//
// The call is best-effort: if the LLM fails or returns nothing usable, falls
// back to the same first-sentence heuristic the frontend used to apply.
func (s *Server) AutoTitleSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "sessionId")

	var req AutoTitleSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	sess, err := s.Store.GetSession(id)
	if writeStoreError(w, err, "AutoTitleSession", "failed to get session") {
		return
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	prefix, rest := splitWFPrefix(strings.TrimSpace(sess.Name))
	if prefix == "" && !isDefaultSessionName(rest) {
		// User has customized this title; respect it.
		writeJSON(w, http.StatusOK, sess)
		return
	}

	title, err := s.generateAutoTitle(r.Context(), s.sessionWorkDir(sess), msg)
	if err != nil {
		log.Printf("[auto-title] LLM failed (session=%s): %v; falling back to heuristic", id, err)
		title = heuristicTitle(msg)
	}
	if title == "" {
		title = heuristicTitle(msg)
	}

	newName := prefix + title
	updated, err := s.Store.UpdateSession(id, UpdateSessionRequest{Name: &newName})
	if writeStoreError(w, err, "AutoTitleSession", "failed to update session") {
		return
	}
	if updated == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// splitWFPrefix separates an optional "WF[xxxxx] " prefix from the rest of
// the name. Returns ("", name) when there is no prefix.
func splitWFPrefix(name string) (prefix, rest string) {
	loc := wfPrefixRegex.FindStringIndex(name)
	if loc == nil {
		return "", name
	}
	return name[:loc[1]], name[loc[1]:]
}

// isDefaultSessionName reports whether `name` looks like the auto-assigned
// placeholder ("", "New Session", "Session 12"). Anything else is assumed to
// be user-chosen and should not be overwritten by auto-title.
func isDefaultSessionName(name string) bool {
	n := strings.TrimSpace(name)
	return n == "" || n == "New Session" || sessionNNNRegex.MatchString(n)
}

// heuristicTitle mirrors the frontend's first-sentence slice. Used as a
// fallback when the LLM call fails so the user still gets something better
// than "New Session".
func heuristicTitle(message string) string {
	cleaned := strings.Join(strings.Fields(message), " ")
	if cleaned == "" {
		return "New Session"
	}
	cut := strings.IndexAny(cleaned, ".!?。！？\n")
	if cut > 0 {
		cleaned = cleaned[:cut]
	}
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "New Session"
	}
	return truncateRunes(cleaned, 48)
}

// truncateRunes returns s truncated to at most n runes (so it never splits a
// multi-byte UTF-8 codepoint mid-sequence).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// autoTitleModelCatalog is implemented by backends that expose their live
// model picker. CodexAgent serves this from its startup-warmed in-memory cache;
// when warmup failed, ListSupportedModels performs the existing on-demand
// retry.
type autoTitleModelCatalog interface {
	ListSupportedModels(context.Context) ([]agent.ModelInfo, string)
}

// resolveAutoTitleBackend selects the lightweight model used for auto-title
// generation from the configured default agent. Claude keeps its stable Haiku
// alias. Codex's live picker is ordered strongest-to-weakest, so the last model
// and its first (lowest) advertised effort are used.
func (s *Server) resolveAutoTitleBackend(ctx context.Context) (agent.Agent, string, string, string, error) {
	agentName := s.getDefaultAgent()
	a, ok := s.Agents[agentName]
	if !ok || a == nil {
		return nil, agentName, "", "", fmt.Errorf("default agent %q not available", agentName)
	}

	switch agentName {
	case "claude":
		return a, agentName, "haiku", "", nil
	case "codex":
		catalog, ok := a.(autoTitleModelCatalog)
		if !ok {
			return nil, agentName, "", "", fmt.Errorf("codex agent does not expose a model catalog")
		}
		models, _ := catalog.ListSupportedModels(ctx)
		if len(models) == 0 {
			return nil, agentName, "", "", fmt.Errorf("codex model catalog is empty")
		}
		selected := models[len(models)-1]
		if strings.TrimSpace(selected.ID) == "" {
			return nil, agentName, "", "", fmt.Errorf("codex weakest model has an empty id")
		}
		effort := ""
		if len(selected.SupportedEfforts) > 0 {
			effort = selected.SupportedEfforts[0]
		}
		return a, agentName, selected.ID, effort, nil
	default:
		return nil, agentName, "", "", fmt.Errorf("auto-title is not supported by default agent %q", agentName)
	}
}

// generateAutoTitle performs a one-shot request using the configured default
// agent. Claude uses Haiku; Codex uses the weakest cached picker model at its
// lowest advertised effort. The HTTP handler retains the existing heuristic
// fallback for catalog, agent, or response failures.
func (s *Server) generateAutoTitle(parentCtx context.Context, workDir, firstMessage string) (string, error) {
	if workDir == "" {
		workDir, _ = os.UserHomeDir()
	}

	prompt := "Summarize the following user message as a very short session title.\n\n" +
		"Rules:\n" +
		"- 4 to 8 words maximum\n" +
		"- Reply in the SAME language as the user message\n" +
		"- No punctuation, no quotes, no markdown, no emojis\n" +
		"- Capture the core intent or topic, not pleasantries\n" +
		"- Output ONLY the title text — no explanation, no preface\n" +
		"- Do NOT use any tools; just respond with the title\n\n" +
		"User message:\n" + firstMessage

	ctx, cancel := context.WithTimeout(parentCtx, 30*time.Second)
	defer cancel()

	a, agentName, model, effort, err := s.resolveAutoTitleBackend(ctx)
	if err != nil {
		return "", err
	}
	log.Printf("[auto-title] agent=%s model=%s effort=%s", agentName, model, effort)

	var textBuf strings.Builder
	_, err = a.SendMessage(ctx, agent.Request{
		ConversationID: fmt.Sprintf("auto-title-%d", time.Now().UnixNano()),
		WorkDir:        workDir,
		Model:          model,
		Effort:         effort,
		Message:        prompt,
	}, func(event agent.StreamEvent) {
		if event.Type == agent.EventTextDelta {
			textBuf.WriteString(event.Content)
		}
	})
	if err != nil {
		return "", fmt.Errorf("agent: %w", err)
	}

	title := strings.TrimSpace(textBuf.String())
	// Strip markdown fences / quotes some models add despite instructions.
	title = strings.Trim(title, "`\"' \n\r\t")
	// Collapse newlines — titles must be single-line.
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return "", fmt.Errorf("agent returned empty response")
	}
	return truncateRunes(title, 48), nil
}

// ReorderSessions sets the display order of sessions.
func (s *Server) ReorderSessions(w http.ResponseWriter, r *http.Request) {
	var req ReorderSessionsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.SessionIDs) == 0 {
		writeError(w, http.StatusBadRequest, "session_ids required")
		return
	}
	err := s.Store.ReorderSessions(req.SessionIDs)
	if writeStoreError(w, err, "ReorderSessions", "failed to reorder sessions") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ForkSessionRequest is the JSON body of POST /api/sessions/{sessionId}/fork.
type ForkSessionRequest struct {
	// MessageID is the durable Nexbox assistant message to fork through.
	// Its agent checkpoint is resolved server-side and never interpreted here.
	MessageID string `json:"message_id"`
	// Name for the new session. If empty, defaults to "<parent name> (fork)".
	Name string `json:"name,omitempty"`
}

// ForkSession creates a new session that is a fork of the session in the URL,
// branched at the given message. Backend-specific checkpoint semantics stay
// inside the selected agent adapter.
//
// On success, the new session is fully populated in the Nexbox DB (with a
// copy of the parent's messages up to the cut point) but its
// `agent_session_id` is empty: the actual CLI fork happens lazily on the
// first chat turn, when chat.go sees `forked_from_session_id != ""` and
// passes the fork flags to the agent.
func (s *Server) ForkSession(w http.ResponseWriter, r *http.Request) {
	parentID := chi.URLParam(r, "sessionId")
	var req ForkSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.MessageID == "" {
		writeError(w, http.StatusBadRequest, "message_id is required")
		return
	}
	parent, err := s.Store.GetSession(parentID)
	if writeStoreError(w, err, "ForkSession", "failed to load parent session") {
		return
	}
	if parent == nil {
		writeError(w, http.StatusNotFound, "parent session not found")
		return
	}
	if parent.DeletedAt != nil {
		// Forking from the trash would silently bring messages back into a
		// fresh session, sidestepping the trash UX. Reject.
		writeError(w, http.StatusBadRequest, "cannot fork a trashed session; restore it first")
		return
	}

	// A named-but-unregistered agent must not silently fall through to the
	// default backend (ResolveAgent's fallback): the parent's checkpoints
	// belong to whatever produced them, not to today's default.
	if parent.Agent != "" {
		if _, registered := s.Agents[parent.Agent]; !registered {
			writeError(w, http.StatusBadRequest, "unknown agent for parent session: "+parent.Agent)
			return
		}
	}
	backend, resolvedAgent := s.ResolveAgent(parent)
	if backend == nil || !backend.Capabilities().Fork {
		writeError(w, http.StatusBadRequest, "fork is not supported by this agent")
		return
	}

	// Resolve the cut point exclusively by BeeHub identity. The opaque agent
	// checkpoint stored with the message is passed through without inspection.
	atMsg, err := s.Store.GetMessage(req.MessageID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load message: "+err.Error())
		return
	}
	if atMsg == nil {
		writeError(w, http.StatusNotFound, "message not found")
		return
	}
	if atMsg.SessionID != parentID {
		writeError(w, http.StatusBadRequest, "message does not belong to parent session")
		return
	}
	if atMsg.Role != "assistant" {
		writeError(w, http.StatusBadRequest, "fork must be anchored on an assistant message")
		return
	}
	if atMsg.Metadata.AgentCheckpoint == "" {
		writeError(w, http.StatusBadRequest, "message has no fork checkpoint")
		return
	}

	// Create new session row + copy messages. Stamp the *resolved* agent on
	// the new row: the fork was capability-gated against that backend, and an
	// empty inherited column would let a later default-agent change hand this
	// checkpoint to a different backend on the first turn.
	newSess, err := s.Store.ForkSession(parentID, req.Name, atMsg.ID, atMsg.Metadata.AgentCheckpoint, resolvedAgent)
	if err != nil {
		log.Printf("[sessions] Fork failed (parent=%s, atMsg=%s): %v", parentID, atMsg.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to fork session: "+err.Error())
		return
	}

	// Eagerly create a worktree for the fork when the parent uses one. We
	// branch from the parent's current branch tip, leaving the parent's
	// worktree untouched. Errors here are logged but don't fail the fork
	// (the user can still send messages — we'll fall back to the project
	// root, matching what ensureWorktree does on add-failure).
	if parent.WorktreeMode == "worktree" && parent.WorktreePath != "" {
		s.cloneWorktreeForFork(newSess, parent)
	}

	log.Printf("[sessions] Forked %s session %s from %s at message %s",
		resolvedAgent, newSess.ID, parentID, atMsg.ID)

	if s.MarketplaceStats != nil {
		s.MarketplaceStats.RecordSession()
	}

	writeJSON(w, http.StatusCreated, newSess)
}

// cloneWorktreeForFork creates a new git worktree for `newSess` branched
// from `parent`'s current branch tip. parent.WorktreePath may be either the
// linked worktree root (legacy rows) or the project's effective subdirectory.
// Updates the new session's worktree fields on success.
//
// On failure (parent missing, detached HEAD, git error), this function logs
// and returns without updating the session — the new session is left in
// worktree mode with an empty path. ensureWorktree on the first chat turn
// will then create a worktree branched from the project root's HEAD instead
// of the parent's branch, which is a different starting point. We accept
// that drift over failing the whole fork: the user is more likely to want
// "the fork still works, just from project HEAD" than "the fork is dead".
func (s *Server) cloneWorktreeForFork(newSess *Session, parent *Session) {
	projectPrefix, err := gitRepositoryPrefix(parent.WorktreePath)
	if err != nil {
		log.Printf("[fork-worktree] failed to resolve project prefix (session=%s, path=%s): %v",
			parent.ID, parent.WorktreePath, err)
		return
	}
	// Legacy sessions stored only the worktree root, so their live prefix can be
	// empty even when the configured project is an ancestor-repository
	// subdirectory. Prefer the configured prefix when it is still available.
	if project, projectErr := s.getProjectWithLocalPath(parent.ProjectID); projectErr == nil && project != nil && project.GitRepoPath != "" {
		if configuredPrefix, prefixErr := gitRepositoryPrefix(project.GitRepoPath); prefixErr == nil {
			projectPrefix = configuredPrefix
		}
	}
	parentBranch, err := exec.Command("git", "-C", parent.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		log.Printf("[fork-worktree] failed to read parent branch (session=%s, path=%s): %v",
			parent.ID, parent.WorktreePath, err)
		return
	}
	parentBranchName := strings.TrimSpace(string(parentBranch))
	if parentBranchName == "" || parentBranchName == "HEAD" {
		log.Printf("[fork-worktree] parent worktree is detached (session=%s); skipping worktree clone", parent.ID)
		return
	}

	shortID := newSess.ID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	newWorktreeDir := filepath.Join(s.DataDir, "worktrees", shortID)
	newBranchName := "fork-" + shortID

	if err := os.MkdirAll(filepath.Join(s.DataDir, "worktrees"), 0755); err != nil {
		log.Printf("[fork-worktree] mkdir failed: %v", err)
		return
	}

	// `git worktree add -b <newBranch> <dir> <startPoint>` creates a new
	// branch starting at the parent's branch tip (a committed ref) and
	// checks it out in a fresh worktree directory. Run from inside the
	// parent worktree so git can locate the repo regardless of where we
	// are in the filesystem.
	cmd := exec.Command("git", "worktree", "add", "-b", newBranchName, newWorktreeDir, parentBranchName)
	cmd.Dir = parent.WorktreePath
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[fork-worktree] git worktree add failed (parent=%s, branch=%s): %v\n%s",
			parent.ID, parentBranchName, err, out)
		return
	}

	effectiveWorkDir, err := effectiveWorktreePath(newWorktreeDir, projectPrefix)
	if err != nil {
		log.Printf("[fork-worktree] failed to prepare project directory (session=%s): %v", newSess.ID, err)
		if removeErr := RemoveWorktree(newWorktreeDir); removeErr != nil {
			log.Printf("[fork-worktree] failed to roll back worktree %s: %v", newWorktreeDir, removeErr)
		}
		return
	}

	if err := s.Store.UpdateSessionWorktree(newSess.ID, "worktree", effectiveWorkDir); err != nil {
		log.Printf("[fork-worktree] failed to record worktree path: %v", err)
		// Fall through: we still created the worktree on disk; ensureWorktree
		// would now fail because the dir already exists, but this is rare.
		return
	}
	newSess.WorktreePath = effectiveWorkDir
	log.Printf("[fork-worktree] created worktree %s (working directory %s, branch %s) for forked session %s",
		newWorktreeDir, effectiveWorkDir, newBranchName, newSess.ID)
}

// DeleteSession soft-deletes a session (moves to trash).
func (s *Server) DeleteSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "sessionId")
	if err := s.Store.DeleteSession(id); err != nil {
		log.Printf("[sessions] Failed to trash session %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to delete session")
		return
	}
	log.Printf("[sessions] Moved session to trash: %s", id)
	w.WriteHeader(http.StatusNoContent)
}

// ListTrashedSessions returns all sessions in the trash.
func (s *Server) ListTrashedSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.Store.ListTrashedSessions()
	if writeStoreError(w, err, "ListTrashedSessions", "failed to list trashed sessions") {
		return
	}
	if sessions == nil {
		sessions = []Session{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessions": sessions,
	})
}

// RestoreSession restores a session from the trash.
func (s *Server) RestoreSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "sessionId")
	if err := s.Store.RestoreSession(id); err != nil {
		log.Printf("[sessions] Failed to restore session %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to restore session")
		return
	}
	log.Printf("[sessions] Restored session from trash: %s", id)
	sess, err := s.Store.GetSession(id)
	if writeStoreError(w, err, "RestoreSession", "failed to get restored session") {
		return
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

// PurgeTrash purges trashed sessions older than cutoff, removes their on-disk
// data (session dirs + worktrees), and runs VACUUM to reclaim space.
// Used by both the periodic 7-day cleanup goroutine and the EmptyTrash handler.
func (s *Server) PurgeTrash(cutoff time.Time) (int, error) {
	worktreePaths, _ := s.Store.GetExpiredSessionWorktrees(cutoff)

	ids, err := s.Store.PurgeExpiredSessions(cutoff)
	if err != nil {
		return 0, err
	}

	for _, id := range ids {
		dir := filepath.Join(s.DataDir, "sessions", id)
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("[trash] Failed to remove session dir %s: %v", dir, err)
		}
	}
	for _, wtPath := range worktreePaths {
		if err := RemoveWorktree(wtPath); err != nil {
			log.Printf("[trash] Failed to remove worktree %s: %v", wtPath, err)
		}
	}
	if len(ids) > 0 {
		if err := s.Store.Vacuum(); err != nil {
			log.Printf("[trash] VACUUM failed: %v", err)
		}
	}
	return len(ids), nil
}

// EmptyTrash is the HTTP handler that immediately purges all trashed sessions.
func (s *Server) EmptyTrash(w http.ResponseWriter, r *http.Request) {
	farFuture := time.Now().Add(100 * 365 * 24 * time.Hour)
	n, err := s.PurgeTrash(farFuture)
	if err != nil {
		log.Printf("[trash] Failed to empty trash: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to empty trash")
		return
	}
	log.Printf("[trash] Emptied trash: %d sessions purged", n)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"purged": n,
	})
}

// ListSessionMessages returns messages for a session.
func (s *Server) ListSessionMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")

	// Check for pagination parameters
	limitStr := r.URL.Query().Get("limit")
	beforeID := r.URL.Query().Get("before")

	if limitStr != "" || beforeID != "" {
		// Use paginated API
		limit := 20 // default
		if limitStr != "" {
			if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 {
				limit = parsedLimit
			}
		}

		messages, hasMore, err := s.Store.ListMessagesPaginated(sessionID, limit, beforeID)
		if writeStoreError(w, err, "ListMessagesPaginated", "failed to list messages") {
			return
		}
		if messages == nil {
			messages = []ChatMessage{}
		}
		active := s.Hub.IsActive(sessionID)
		resp := map[string]interface{}{
			"messages": messages,
			"has_more": hasMore,
			"active":   active,
		}
		if pending, ok := getPendingUserInput(sessionID); ok {
			resp["pending_input"] = pending
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Legacy non-paginated API
	messages, err := s.Store.ListMessages(sessionID)
	if writeStoreError(w, err, "ListMessages", "failed to list messages") {
		return
	}
	if messages == nil {
		messages = []ChatMessage{}
	}
	active := s.Hub.IsActive(sessionID)
	resp := map[string]interface{}{
		"messages": messages,
		"active":   active,
	}
	if pending, ok := getPendingUserInput(sessionID); ok {
		resp["pending_input"] = pending
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetMessageDetails returns full message parts from the local detail cache.
func (s *Server) GetMessageDetails(w http.ResponseWriter, r *http.Request) {
	messageID := chi.URLParam(r, "messageId")
	if s.DetailCache == nil {
		writeError(w, http.StatusNotFound, "detail cache not available")
		return
	}
	parts, err := s.DetailCache.Get(messageID)
	if writeStoreError(w, err, "GetMessageDetails", "failed to load details") {
		return
	}
	if parts == nil {
		writeError(w, http.StatusNotFound, "details not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"parts": parts,
	})
}
