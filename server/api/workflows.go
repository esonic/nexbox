package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

// RegisterWorkflowRoutes registers workflow-related routes
func (s *Server) RegisterWorkflowRoutes(r chi.Router) {
	r.Route("/workflows", func(r chi.Router) {
		r.Post("/", s.createWorkflow)
		r.Get("/", s.listWorkflows)
		r.Get("/{id}", s.getWorkflow)
		r.Put("/{id}", s.updateWorkflow)
		r.Delete("/{id}", s.deleteWorkflow)

		r.Post("/{id}/execute", s.executeWorkflow)
		r.Get("/{id}/executions", s.listExecutions)
	})

	r.Route("/workflow_executions", func(r chi.Router) {
		r.Get("/{id}", s.getExecution)
		r.Get("/{id}/logs", s.getExecutionLogs)
		r.Post("/{id}/cancel", s.cancelExecution)
		r.Post("/{id}/nodes/{nodeId}/continue", s.continueHumanNode)
	})
}

// createWorkflow creates a new workflow
func (s *Server) createWorkflow(w http.ResponseWriter, r *http.Request) {
	var req store.CreateWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	userEmail := "local"
	if s.Cloud != nil {
		userEmail = s.Cloud.Info.UserName
	}
	projectID := r.URL.Query().Get("project_id")

	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Definition) == 0 {
		writeError(w, http.StatusBadRequest, "definition is required")
		return
	}

	workflow, err := s.Store.CreateWorkflow(projectID, req.Name, req.Description, userEmail, []byte(req.Definition))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, workflow)
}

// listWorkflows lists workflows for a project
func (s *Server) listWorkflows(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}

	workflows, err := s.Store.ListWorkflows(projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	for i := range workflows {
		workflows[i].IsRunning = s.isWorkflowRunning(workflows[i].ID)
	}

	writeJSON(w, http.StatusOK, workflows)
}

// getWorkflow retrieves a workflow by ID
func (s *Server) getWorkflow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	workflow, err := s.Store.GetWorkflow(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if workflow == nil {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}

	writeJSON(w, http.StatusOK, workflow)
}

// updateWorkflow updates a workflow
func (s *Server) updateWorkflow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req store.UpdateWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	workflow, err := s.Store.UpdateWorkflow(id, req)
	if err != nil {
		if err == store.ErrVersionConflict {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, workflow)
}

// deleteWorkflow deletes a workflow
func (s *Server) deleteWorkflow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if err := s.Store.DeleteWorkflow(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// executeWorkflow starts async workflow execution via the unified Execute function.
func (s *Server) executeWorkflow(w http.ResponseWriter, r *http.Request) {
	workflowID := chi.URLParam(r, "id")

	var req struct {
		TaskIDs     []string `json:"task_ids"`
		UserContext string   `json:"user_context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	wf, err := s.Store.GetWorkflow(workflowID)
	if err != nil || wf == nil {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}

	var tasks []store.Task
	for _, tid := range req.TaskIDs {
		if t, err := s.Store.GetTask(tid); err == nil && t != nil {
			tasks = append(tasks, *t)
		}
	}

	if s.isWorkflowRunning(workflowID) {
		writeError(w, http.StatusConflict, "workflow is already running")
		return
	}

	exec, err := s.Execute(ExecuteWorkflowRequest{
		WorkflowID:  workflowID,
		ProjectID:   wf.ProjectID,
		UserContext:  req.UserContext,
		Tasks:       tasks,
		TriggeredBy: "api",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, exec)
}

// cancelExecution cancels a running workflow execution.
func (s *Server) cancelExecution(w http.ResponseWriter, r *http.Request) {
	executionID := chi.URLParam(r, "id")
	if err := s.CancelExecution(executionID); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listExecutions lists executions for a workflow
func (s *Server) listExecutions(w http.ResponseWriter, r *http.Request) {
	workflowID := chi.URLParam(r, "id")

	limitStr := r.URL.Query().Get("limit")
	limit := 20
	if limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	offsetStr := r.URL.Query().Get("offset")
	offset := 0
	if offsetStr != "" {
		if parsed, err := strconv.Atoi(offsetStr); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	executions, err := s.Store.ListExecutions(workflowID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, executions)
}

// getExecution retrieves an execution by ID
func (s *Server) getExecution(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	execution, err := s.Store.GetExecution(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if execution == nil {
		writeError(w, http.StatusNotFound, "execution not found")
		return
	}

	writeJSON(w, http.StatusOK, execution)
}

// getExecutionLogs retrieves logs for an execution
func (s *Server) getExecutionLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	logs, err := s.Store.GetNodeLogs(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, logs)
}

// continueHumanNode unblocks a human node that is waiting for a continue signal.
// Accepts an optional JSON body: {"input": "..."} to pass a message to the next AI node.
func (s *Server) continueHumanNode(w http.ResponseWriter, r *http.Request) {
	executionID := chi.URLParam(r, "id")
	nodeID := chi.URLParam(r, "nodeId")

	var body struct {
		Input string `json:"input"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	if err := s.ContinueHumanNode(executionID, nodeID, body.Input); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
