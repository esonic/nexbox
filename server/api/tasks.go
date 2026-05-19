package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"gitlab.okg.com/sre_ai/nexbox/gitlab"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

func (s *Server) ListTasks(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	projectID := r.URL.Query().Get("project_id")
	search := r.URL.Query().Get("q")
	source := r.URL.Query().Get("source") // "local" to force DB read (used during migration)

	// GitLab project → fetch from GitLab API (unless source=local is requested)
	if projectID != "" && source != "local" {
		if info := s.resolveGitLabProject(projectID); info != nil {
			tasks, err := s.gitlabListTasks(r.Context(), info, projectID, limit, offset, search)
			if err != nil {
				writeError(w, http.StatusBadGateway, "gitlab: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, tasks)
			return
		}
	}

	// Local store
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

	// GitLab project → create via GitLab API
	if info := s.resolveGitLabProject(req.ProjectID); info != nil {
		task, err := s.gitlabCreateTask(r.Context(), info, req.ProjectID, req)
		if err != nil {
			writeError(w, http.StatusBadGateway, "gitlab: "+err.Error())
			return
		}
		s.Hub.NotifyChange("task", task.ID, "created")
		writeJSON(w, http.StatusCreated, task)
		return
	}

	// Local store — validate parent (max 1 level)
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

	// GitLab task
	if gitlab.IsGitLabTaskID(id) {
		info, iid, err := s.resolveGitLabTask(id)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		projectID, _, _ := gitlab.ParseTaskID(id)
		task, err := s.gitlabGetTask(r.Context(), info, projectID, iid)
		if err != nil {
			writeError(w, http.StatusBadGateway, "gitlab: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, task)
		return
	}

	// Local store
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

	// GitLab task
	if gitlab.IsGitLabTaskID(id) {
		info, iid, err := s.resolveGitLabTask(id)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		projectID, _, _ := gitlab.ParseTaskID(id)
		task, err := s.gitlabUpdateTask(r.Context(), info, projectID, iid, req)
		if err != nil {
			writeError(w, http.StatusBadGateway, "gitlab: "+err.Error())
			return
		}
		s.Hub.NotifyChange("task", id, "updated")
		writeJSON(w, http.StatusOK, task)
		return
	}

	// Local store
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

	// GitLab task
	if gitlab.IsGitLabTaskID(id) {
		info, iid, err := s.resolveGitLabTask(id)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.gitlabDeleteTask(r.Context(), info, iid); err != nil {
			writeError(w, http.StatusBadGateway, "gitlab: "+err.Error())
			return
		}
		s.Hub.NotifyChange("task", id, "deleted")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Local store
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

	// Both GitLab → use Work Items API
	if gitlab.IsGitLabTaskID(taskID) && gitlab.IsGitLabTaskID(req.ParentID) {
		if err := s.gitlabSetParent(r.Context(), taskID, req.ParentID); err != nil {
			writeError(w, http.StatusBadGateway, "gitlab: "+err.Error())
			return
		}
		s.Hub.NotifyChange("task", taskID, "updated")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Mixed → not supported
	if gitlab.IsGitLabTaskID(taskID) || gitlab.IsGitLabTaskID(req.ParentID) {
		writeError(w, http.StatusBadRequest, "cannot set parent-child between GitLab and local tasks")
		return
	}

	// Local/remote store — use UpdateTask with parent_id
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

	// GitLab task — remove parent via Work Items API
	if gitlab.IsGitLabTaskID(taskID) {
		info, iid, err := s.resolveGitLabTask(taskID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		client := gitlab.NewClient(info.Token)
		wi, err := client.GetWorkItemInfo(r.Context(), info.ProjectPath, iid)
		if err != nil {
			writeError(w, http.StatusBadGateway, "gitlab: "+err.Error())
			return
		}
		if err := client.RemoveWorkItemParent(r.Context(), wi.GID); err != nil {
			writeError(w, http.StatusBadGateway, "gitlab: "+err.Error())
			return
		}
		s.Hub.NotifyChange("task", taskID, "updated")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Local/remote store — use UpdateTask with empty parent_id
	emptyParent := ""
	_, err := s.Store.UpdateTask(taskID, store.UpdateTaskRequest{ParentID: &emptyParent})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove parent: "+err.Error())
		return
	}
	s.Hub.NotifyChange("task", taskID, "updated")
	w.WriteHeader(http.StatusNoContent)
}
