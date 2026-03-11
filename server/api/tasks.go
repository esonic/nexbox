package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"gitlab.okg.com/sre_ai/boxnex/store"
)

func (s *Server) ListTasks(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	projectID := r.URL.Query().Get("project_id")
	search := r.URL.Query().Get("q")

	var projectIDs []string
	if projectID != "" {
		// Validate the requesting user can access this project.
		if GetUserRole(r) != "admin" {
			project, err := s.Store.GetProject(projectID)
			if err != nil || project == nil || project.OwnerID != GetUserID(r) {
				writeError(w, http.StatusForbidden, "access denied")
				return
			}
		}
		projectIDs = []string{projectID}
	} else if GetUserRole(r) != "admin" {
		// Scope to the user's own projects so tasks from other users are never leaked.
		allProjects, err := s.Store.ListProjects()
		if err == nil {
			userID := GetUserID(r)
			for _, p := range allProjects {
				if p.OwnerID == userID {
					projectIDs = append(projectIDs, p.ID)
				}
			}
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
	// Validate project ownership so users can only create tasks in their own projects.
	if req.ProjectID != "" && GetUserRole(r) != "admin" {
		project, err := s.Store.GetProject(req.ProjectID)
		if err != nil || project == nil || project.OwnerID != GetUserID(r) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
	}
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
		actor := s.currentUser()
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

	// Capture old assigner before update to detect changes
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

	// Notify new assigner if changed (only when we successfully read the old task)
	if gotOldTask && req.Assigner != nil && *req.Assigner != "" && *req.Assigner != oldAssigner {
		actor := s.currentUser()
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

func (s *Server) IncrementTaskRefCount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "taskId")
	err := s.Store.IncrementTaskRefCount(id)
	if writeStoreError(w, err, "IncrementTaskRefCount", "failed to increment ref count") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ListTaskAssets(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	assets, err := s.Store.ListTaskAssets(taskID)
	if writeStoreError(w, err, "ListTaskAssets", "failed to list task assets") {
		return
	}
	if assets == nil {
		assets = []Asset{}
	}
	writeJSON(w, http.StatusOK, assets)
}

func (s *Server) LinkTaskAsset(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	var body struct {
		AssetID string `json:"asset_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AssetID == "" {
		writeError(w, http.StatusBadRequest, "asset_id is required")
		return
	}
	ta, err := s.Store.LinkTaskAsset(taskID, body.AssetID)
	if writeStoreError(w, err, "LinkTaskAsset", "failed to link asset") {
		return
	}
	writeJSON(w, http.StatusCreated, ta)
}

func (s *Server) UnlinkTaskAsset(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	assetID := chi.URLParam(r, "assetId")
	err := s.Store.UnlinkTaskAsset(taskID, assetID)
	if writeStoreError(w, err, "UnlinkTaskAsset", "failed to unlink asset") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
