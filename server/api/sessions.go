package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

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
	var activeIDs []string
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
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"messages": messages,
			"has_more": hasMore,
			"active":   active,
		})
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
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"messages": messages,
		"active":   active,
	})
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
