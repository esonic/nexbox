package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

func (s *Server) ListQueuedMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	items, err := s.Store.ListQueuedMessages(sessionID)
	if writeStoreError(w, err, "ListQueuedMessages", "failed to list queue") {
		return
	}
	if items == nil {
		items = []store.QueuedMessage{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) CreateQueuedMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	var req store.CreateQueuedMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Text == "" && len(req.Images) == 0 {
		writeError(w, http.StatusBadRequest, "text or images required")
		return
	}
	m, err := s.Store.CreateQueuedMessage(sessionID, req)
	if writeStoreError(w, err, "CreateQueuedMessage", "failed to create queued message") {
		return
	}
	s.Hub.Emit(sessionID, ChatEvent{Type: "queue_updated"})
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) UpdateQueuedMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	id := chi.URLParam(r, "queueId")
	var req store.UpdateQueuedMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	err := s.Store.UpdateQueuedMessage(sessionID, id, req.Text)
	if writeStoreError(w, err, "UpdateQueuedMessage", "failed to update queued message") {
		return
	}
	s.Hub.Emit(sessionID, ChatEvent{Type: "queue_updated"})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) DeleteQueuedMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	id := chi.URLParam(r, "queueId")
	err := s.Store.DeleteQueuedMessage(sessionID, id)
	if writeStoreError(w, err, "DeleteQueuedMessage", "failed to delete queued message") {
		return
	}
	s.Hub.Emit(sessionID, ChatEvent{Type: "queue_updated"})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) HandleRunQueuedMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	queueID := chi.URLParam(r, "queueId")
	if err := s.RunQueuedNow(sessionID, queueID); err != nil {
		log.Printf("[queue] Run queued message failed: session=%s queue=%s err=%v", sessionID, queueID, err)
		writeError(w, http.StatusBadRequest, "failed to run queued message")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
