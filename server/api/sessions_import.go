package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/store"
)

const (
	defaultImportPageSize = 20
	maxImportPageSize     = 100
)

// importMu serializes imports so the already-imported check and the insert are
// atomic with respect to each other.
var importMu sync.Mutex

// importedAgentSessionIDs returns the native session ids NexBox already has a
// session for (live or in the trash).
func (s *Server) importedAgentSessionIDs() (map[string]bool, error) {
	live, err := s.Store.ListSessions()
	if err != nil {
		return nil, err
	}
	trashed, err := s.Store.ListTrashedSessions()
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(live)+len(trashed))
	for _, list := range [][]Session{live, trashed} {
		for _, sess := range list {
			if sess.AgentSessionID != "" {
				ids[sess.AgentSessionID] = true
			}
		}
	}
	return ids, nil
}

// ListImportableSessions lists agent-native sessions (created in the agent's
// own CLI) for a project's directory that NexBox does not have yet.
//
// Paged newest-first: pass the last item's updated_at (as returned) as
// `before` to fetch the next page.
//
// GET /api/sessions/importable?project_id=...&agent=claude|codex[&limit=20][&before=<updated_at>]
func (s *Server) ListImportableSessions(w http.ResponseWriter, r *http.Request) {
	user := s.requireAuthUser(w, r)
	if user == nil {
		return
	}
	importer := agent.SessionImporterFor(r.URL.Query().Get("agent"))
	if importer == nil {
		writeError(w, http.StatusBadRequest, "importing sessions is not supported for this agent")
		return
	}
	project, err := s.getProjectWithLocalPath(r.URL.Query().Get("project_id"))
	if writeStoreError(w, err, "ListImportableSessions", "failed to load project") {
		return
	}
	if project != nil && project.OwnerUserID != user.UserID {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if project == nil || project.GitRepoPath == "" {
		writeError(w, http.StatusBadRequest, "project has no local directory")
		return
	}
	known, err := s.importedAgentSessionIDs()
	if writeStoreError(w, err, "ListImportableSessions", "failed to list sessions") {
		return
	}
	limit := defaultImportPageSize
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = min(n, maxImportPageSize)
	}
	var before time.Time
	if t, err := time.Parse(time.RFC3339Nano, r.URL.Query().Get("before")); err == nil {
		before = t
	}
	page, hasMore, err := importer.ListSessions(project.GitRepoPath, agent.ListOptions{
		Before: before,
		Limit:  limit,
		Skip:   func(id string) bool { return known[id] },
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to scan sessions: "+err.Error())
		return
	}
	if page == nil {
		page = []agent.ImportableSession{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": page, "has_more": hasMore})
}

type importSessionRequest struct {
	ProjectID      string `json:"project_id"`
	Agent          string `json:"agent"`
	AgentSessionID string `json:"agent_session_id"`
}

// ImportSession registers a native agent session as a NexBox session.
//
// POST /api/sessions/import
func (s *Server) ImportSession(w http.ResponseWriter, r *http.Request) {
	user := s.requireAuthUser(w, r)
	if user == nil {
		return
	}
	var req importSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	importer := agent.SessionImporterFor(req.Agent)
	if importer == nil || req.AgentSessionID == "" {
		writeError(w, http.StatusBadRequest, "importing sessions is not supported for this agent")
		return
	}
	if _, ok := s.Agents[req.Agent]; !ok {
		writeError(w, http.StatusBadRequest, "unknown agent: "+req.Agent)
		return
	}
	project, err := s.getProjectWithLocalPath(req.ProjectID)
	if writeStoreError(w, err, "ImportSession", "failed to load project") {
		return
	}
	if project != nil && project.OwnerUserID != user.UserID {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if project == nil || project.GitRepoPath == "" {
		writeError(w, http.StatusBadRequest, "project has no local directory")
		return
	}

	importMu.Lock()
	defer importMu.Unlock()
	known, err := s.importedAgentSessionIDs()
	if writeStoreError(w, err, "ImportSession", "failed to list sessions") {
		return
	}
	if known[req.AgentSessionID] {
		writeError(w, http.StatusConflict, "session already exists in NexBox")
		return
	}

	imported, err := importer.LoadSession(project.GitRepoPath, req.AgentSessionID)
	if errors.Is(err, agent.ErrImportNotFound) {
		writeError(w, http.StatusNotFound, "agent session not found for this project")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read agent session: "+err.Error())
		return
	}

	model := imported.Model
	msgs := make([]store.ImportSessionMessage, len(imported.Messages))
	for i, m := range imported.Messages {
		msgs[i] = store.ImportSessionMessage{Role: m.Role, Parts: m.Parts, Metadata: m.Metadata, CreatedAt: m.CreatedAt}
	}
	sess, err := s.Store.ImportSession(store.ImportSessionRequest{
		Name:           imported.Title,
		ProjectID:      project.ID,
		Agent:          req.Agent,
		Model:          model,
		AgentSessionID: req.AgentSessionID,
		Messages:       msgs,
	})
	if writeStoreError(w, err, "ImportSession", "failed to import session") {
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}
