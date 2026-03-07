package api

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
)

// ---------------------------------------------------------------------------
// Task Comments
// ---------------------------------------------------------------------------

func (s *Server) ListTaskComments(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	comments, err := s.Store.ListTaskComments(taskID)
	if writeStoreError(w, err, "ListTaskComments", "failed to list comments") {
		return
	}
	if comments == nil {
		comments = []TaskComment{}
	}
	writeJSON(w, http.StatusOK, comments)
}

func (s *Server) CreateTaskComment(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	var req CreateTaskCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Body == "" {
		writeError(w, http.StatusBadRequest, "body is required")
		return
	}
	author := s.currentUser()
	comment, err := s.Store.CreateTaskComment(taskID, req, author)
	if writeStoreError(w, err, "CreateTaskComment", "failed to create comment") {
		return
	}

	// Notify assigner, creator, and @mentions
	if task, terr := s.Store.GetTask(taskID); terr == nil && task != nil {
		mentions := parseMentions(req.Body)
		candidates := append([]string{task.Assigner, task.CreatedBy}, mentions...)
		recipient := buildRecipient(author, candidates...)
		preview := req.Body
		if runes := []rune(preview); len(runes) > 100 {
			preview = string(runes[:100]) + "..."
		}
		if err := s.sendNotification(task.ProjectID, taskID, task.Title,
			author+" commented on a task", recipient, taskID, "", comment.ID, preview, nil); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to send notification: "+err.Error())
			return
		}
	}

	writeJSON(w, http.StatusCreated, comment)
}

func (s *Server) UpdateTaskComment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "commentId")
	var req UpdateTaskCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	comment, err := s.Store.UpdateTaskComment(id, req)
	if writeStoreError(w, err, "UpdateTaskComment", "failed to update comment") {
		return
	}
	if comment == nil {
		writeError(w, http.StatusNotFound, "comment not found")
		return
	}
	writeJSON(w, http.StatusOK, comment)
}

func (s *Server) DeleteTaskComment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "commentId")
	err := s.Store.DeleteTaskComment(id)
	if writeStoreError(w, err, "DeleteTaskComment", "failed to delete comment") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Labels
// ---------------------------------------------------------------------------

func (s *Server) ListLabels(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	labels, err := s.Store.ListLabels(projectID)
	if writeStoreError(w, err, "ListLabels", "failed to list labels") {
		return
	}
	if labels == nil {
		labels = []Label{}
	}
	writeJSON(w, http.StatusOK, labels)
}

var labelNameRegexp = regexp.MustCompile(`^[a-zA-Z0-9_-]{2,10}$`)

func (s *Server) CreateLabel(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	var req CreateLabelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !labelNameRegexp.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, "label name must be 2-10 chars of [a-zA-Z0-9_-]")
		return
	}
	label, err := s.Store.CreateLabel(projectID, req)
	if writeStoreError(w, err, "CreateLabel", "failed to create label") {
		return
	}
	writeJSON(w, http.StatusCreated, label)
}

func (s *Server) DeleteLabel(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	id := chi.URLParam(r, "labelId")
	err := s.Store.DeleteLabel(projectID, id)
	if writeStoreError(w, err, "DeleteLabel", "failed to delete label") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AddLabelToTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.Store.AddLabelToTask(taskID, body.Name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) RemoveLabelFromTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	labelName := chi.URLParam(r, "labelName")
	err := s.Store.RemoveLabelFromTask(taskID, labelName)
	if writeStoreError(w, err, "RemoveLabelFromTask", "failed to remove label") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Reactions
// ---------------------------------------------------------------------------

func (s *Server) GetReactions(w http.ResponseWriter, r *http.Request) {
	targetType := chi.URLParam(r, "targetType")
	targetID := chi.URLParam(r, "targetId")
	user := s.currentUser()
	summary, err := s.Store.GetReactionSummary(targetType, targetID, user)
	if writeStoreError(w, err, "GetReactions", "failed to get reactions") {
		return
	}
	if summary == nil {
		summary = []ReactionSummary{}
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) AddReaction(w http.ResponseWriter, r *http.Request) {
	targetType := chi.URLParam(r, "targetType")
	targetID := chi.URLParam(r, "targetId")
	var body struct {
		Emoji string `json:"emoji"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Emoji == "" {
		writeError(w, http.StatusBadRequest, "emoji is required")
		return
	}
	author := s.currentUser()
	err := s.Store.AddReaction(targetType, targetID, author, body.Emoji)
	if writeStoreError(w, err, "AddReaction", "failed to add reaction") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) RemoveReaction(w http.ResponseWriter, r *http.Request) {
	targetType := chi.URLParam(r, "targetType")
	targetID := chi.URLParam(r, "targetId")
	emoji := r.URL.Query().Get("emoji")
	if emoji == "" {
		writeError(w, http.StatusBadRequest, "emoji query param is required")
		return
	}
	author := s.currentUser()
	err := s.Store.RemoveReaction(targetType, targetID, author, emoji)
	if writeStoreError(w, err, "RemoveReaction", "failed to remove reaction") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (s *Server) currentUser() string {
	if s.Cloud != nil && s.Cloud.Info.UserName != "" {
		return s.Cloud.Info.UserName
	}
	return "local"
}
