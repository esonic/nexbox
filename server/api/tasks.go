package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/esonic/nexbox/store"
)

func (s *Server) ListTasks(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	projectID := r.URL.Query().Get("project_id")
	search := r.URL.Query().Get("q")

	var projectIDs []string
	if projectID != "" {
		projectIDs = []string{projectID}
	} else if joined := s.joinedProjectIDs(); joined != nil {
		projectIDs = make([]string, 0, len(joined))
		for id := range joined {
			projectIDs = append(projectIDs, id)
		}
	}

	tasks, err := s.Store.ListTasks(limit, offset, projectIDs, search)
	if writeStoreError(w, err, "ListTasks", "failed to list tasks") {
		return
	}
	if tasks == nil {
		tasks = []Task{}
	}

	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) CreateTask(w http.ResponseWriter, r *http.Request) {
	var req CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}

	// Validate parent (max 1 level)
	if req.ParentID != "" {
		parent, err := s.Store.GetTask(req.ParentID)
		if err != nil || parent == nil {
			writeError(w, http.StatusBadRequest, "parent task not found")
			return
		}
		if parent.ParentID != "" {
			writeError(w, http.StatusBadRequest, "cannot create sub-task under another sub-task (max 1 level)")
			return
		}
	}
	task, err := s.Store.CreateTask(req)
	if writeStoreError(w, err, "CreateTask", "failed to create task") {
		return
	}
	s.Hub.NotifyChange("task", task.ID, "created")

	// Notify assigner
	if req.Assigner != "" {
		actor := s.currentUser(r)
		recipient := buildRecipient(actor, req.Assigner)
		if err := s.sendNotification(task.ProjectID, task.ID, task.Title,
			actor+" assigned you to a task", recipient, task.ID, "", "", "", nil); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to send notification: "+err.Error())
			return
		}
	}

	writeJSON(w, http.StatusCreated, task)
}

func (s *Server) GetTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "taskId")
	task, err := s.Store.GetTask(id)
	if writeStoreError(w, err, "GetTask", "failed to get task") {
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) UpdateTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "taskId")
	var req UpdateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	var oldAssigner string
	var gotOldTask bool
	if req.Assigner != nil {
		if old, err := s.Store.GetTask(id); err == nil && old != nil {
			oldAssigner = old.Assigner
			gotOldTask = true
		}
	}

	task, err := s.Store.UpdateTask(id, req)
	if err != nil {
		if errors.Is(err, store.ErrVersionConflict) {
			writeError(w, http.StatusConflict, "version conflict: task was modified by another client")
			return
		}
		_ = writeStoreError(w, err, "UpdateTask", "failed to update task")
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	s.Hub.NotifyChange("task", id, "updated")

	if gotOldTask && req.Assigner != nil && *req.Assigner != "" && *req.Assigner != oldAssigner {
		actor := s.currentUser(r)
		recipient := buildRecipient(actor, *req.Assigner)
		if err := s.sendNotification(task.ProjectID, task.ID, task.Title,
			actor+" assigned you to a task", recipient, task.ID, "", "", "", nil); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to send notification: "+err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, task)
}

func (s *Server) DeleteTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "taskId")
	err := s.Store.DeleteTask(id)
	if writeStoreError(w, err, "DeleteTask", "failed to delete task") {
		return
	}
	s.Hub.NotifyChange("task", id, "deleted")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) SetTaskParent(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")

	var req store.SetParentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.ParentID == "" {
		writeError(w, http.StatusBadRequest, "parent_id is required")
		return
	}

	parentID := req.ParentID
	_, err := s.Store.UpdateTask(taskID, store.UpdateTaskRequest{ParentID: &parentID})
	if err != nil {
		if store.IsValidationError(err) {
			writeError(w, http.StatusBadRequest, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "failed to set parent: "+err.Error())
		}
		return
	}
	s.Hub.NotifyChange("task", taskID, "updated")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) RemoveTaskParent(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	emptyParent := ""
	_, err := s.Store.UpdateTask(taskID, store.UpdateTaskRequest{ParentID: &emptyParent})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove parent: "+err.Error())
		return
	}
	s.Hub.NotifyChange("task", taskID, "updated")
	w.WriteHeader(http.StatusNoContent)
}
