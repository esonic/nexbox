package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (s *Server) ListReviewComments(w http.ResponseWriter, r *http.Request) {
	assetID := chi.URLParam(r, "assetId")
	comments, err := s.Store.ListReviewComments(assetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if comments == nil {
		comments = []ReviewComment{}
	}
	writeJSON(w, http.StatusOK, comments)
}

func (s *Server) CreateReviewComment(w http.ResponseWriter, r *http.Request) {
	assetID := chi.URLParam(r, "assetId")
	var req CreateReviewCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	comment, err := s.Store.CreateReviewComment(assetID, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Notify asset creator and @mentions
	actor := s.currentUser(r)
	if asset, aerr := s.Store.GetAsset(assetID); aerr == nil && asset != nil {
		mentions := parseMentions(req.Content)
		candidates := append([]string{asset.CreatedBy}, mentions...)
		recipient := buildRecipient(actor, candidates...)
		preview := req.Content
		if runes := []rune(preview); len(runes) > 100 {
			preview = string(runes[:100]) + "..."
		}
		if err := s.sendNotification(asset.ProjectID, assetID, asset.Title,
			actor+" commented on your asset", recipient, "", assetID, comment.ID, preview, req.LineNumber); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to send notification: "+err.Error())
			return
		}
	}

	writeJSON(w, http.StatusCreated, comment)
}

func (s *Server) UpdateReviewComment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "commentId")
	var req UpdateReviewCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	comment, err := s.Store.UpdateReviewComment(id, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if comment == nil {
		writeError(w, http.StatusNotFound, "comment not found")
		return
	}
	writeJSON(w, http.StatusOK, comment)
}

func (s *Server) DeleteReviewComment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "commentId")
	err := s.Store.DeleteReviewComment(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
