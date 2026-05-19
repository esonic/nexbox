package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/esonic/nexbox/gitlab"
	"github.com/esonic/nexbox/store"
)

// migratingProjects prevents concurrent migrations for the same project.
var migratingProjects sync.Map

// deleteTaskIgnoreNotFound calls DeleteTask and treats 404 as success
// (task already gone from DB — which is what we want).
func (s *Server) deleteTaskIgnoreNotFound(id string) error {
	err := s.Store.DeleteTask(id)
	if err != nil {
		var apiErr *store.ApiError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return nil
		}
	}
	return err
}

// ---------------------------------------------------------------------------
// Migration status check
// ---------------------------------------------------------------------------

type migrationStatusResponse struct {
	NeedsMigration bool   `json:"needs_migration"`
	TaskCount      int    `json:"task_count"`
	Reason         string `json:"reason,omitempty"`
}

// GetMigrationStatus checks if a project needs task migration to GitLab.
// Conditions (all must be true):
//  1. user has gitlab token
//  2. project has GitLabRepoURL
//  3. DB still has local tasks (deleted_at IS NULL)
func (s *Server) GetMigrationStatus(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")

	project, err := s.Store.GetProject(projectID)
	if err != nil || project == nil {
		writeJSON(w, http.StatusOK, migrationStatusResponse{Reason: "project not found"})
		return
	}

	if project.GitLabRepoURL == "" {
		writeJSON(w, http.StatusOK, migrationStatusResponse{Reason: "not a gitlab project"})
		return
	}

	token := s.Config.Get().GitLabToken
	if token == "" {
		writeJSON(w, http.StatusOK, migrationStatusResponse{Reason: "no gitlab token configured"})
		return
	}

	// Store.ListTasks already filters deleted_at IS NULL
	tasks, err := s.Store.ListTasks(0, 0, []string{projectID}, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check tasks: "+err.Error())
		return
	}

	if len(tasks) == 0 {
		writeJSON(w, http.StatusOK, migrationStatusResponse{Reason: "no local tasks"})
		return
	}

	writeJSON(w, http.StatusOK, migrationStatusResponse{
		NeedsMigration: true,
		TaskCount:      len(tasks),
	})
}

// ---------------------------------------------------------------------------
// Migration execution
// ---------------------------------------------------------------------------

type migrateTasksResponse struct {
	Migrated int      `json:"migrated"`
	Failed   int      `json:"failed"`
	Errors   []string `json:"errors,omitempty"`
}

// MigrateTasks is the HTTP handler for POST /projects/{projectId}/migrate-tasks.
func (s *Server) MigrateTasks(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")

	info := s.resolveGitLabProject(projectID)
	if info == nil {
		writeError(w, http.StatusBadRequest, "not a gitlab project or gitlab token not configured")
		return
	}

	// Prevent concurrent migrations for the same project
	if _, loaded := migratingProjects.LoadOrStore(projectID, true); loaded {
		writeError(w, http.StatusConflict, "migration already in progress for this project")
		return
	}
	defer migratingProjects.Delete(projectID)

	result := s.doMigrateTasks(r.Context(), info, projectID)
	writeJSON(w, http.StatusOK, result)
}

// doMigrateTasks is the core migration logic.
// - Tasks with parent_id="" → GitLab Issue (top-level)
// - Tasks with parent_id!="" → GitLab Task (child work item)
// Parent Issues are created first. Each task is deleted from DB immediately
// after successful GitLab creation to prevent duplicates on re-run.
func (s *Server) doMigrateTasks(ctx context.Context, info *gitlabProjectInfo, projectID string) migrateTasksResponse {
	tasks, err := s.Store.ListTasks(0, 0, []string{projectID}, "")
	if err != nil {
		log.Printf("[migrate] failed to list tasks for %s: %v", projectID, err)
		return migrateTasksResponse{Errors: []string{"failed to list tasks: " + err.Error()}}
	}
	if len(tasks) == 0 {
		return migrateTasksResponse{}
	}

	// Separate into parents (Issues) and children (Tasks)
	var parents []store.Task
	var children []store.Task
	for _, t := range tasks {
		if t.ParentID == "" {
			parents = append(parents, t)
		} else {
			children = append(children, t)
		}
	}

	client := gitlab.NewClient(info.Token)
	result := migrateTasksResponse{}

	// Map: local task ID → GitLab task ID in gl: format
	localToGitLabID := make(map[string]string)

	// ── Phase 1: Migrate parent tasks as GitLab Issues ──
	for _, task := range parents {
		req := taskToCreateRequest(task, "")
		created, err := s.gitlabCreateTask(ctx, info, projectID, req)
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Sprintf("issue %q: %v", task.Title, err))
			log.Printf("[migrate] failed to create gitlab issue for %s (%s): %v", task.ID, task.Title, err)
			continue
		}

		localToGitLabID[task.ID] = created.ID

		// Migrate comments
		if created.ExternalID != "" {
			iid, _ := strconv.Atoi(created.ExternalID)
			s.migrateTaskComments(ctx, client, info.ProjectPath, task.ID, iid)
		}

		// Delete from DB immediately after successful creation
		if err := s.deleteTaskIgnoreNotFound(task.ID); err != nil {
			log.Printf("[migrate] failed to delete task %s: %v", task.ID, err)
		}

		result.Migrated++
	}

	// ── Phase 2: Migrate child tasks as GitLab Tasks (work items) ──
	for _, task := range children {
		parentGitLabID := localToGitLabID[task.ParentID]

		req := taskToCreateRequest(task, parentGitLabID)
		_, err := s.gitlabCreateTask(ctx, info, projectID, req)
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Sprintf("task %q: %v", task.Title, err))
			log.Printf("[migrate] failed to create child task for %s (%s): %v", task.ID, task.Title, err)
			continue
		}

		// Delete from DB immediately after successful creation
		if err := s.deleteTaskIgnoreNotFound(task.ID); err != nil {
			log.Printf("[migrate] failed to delete task %s: %v", task.ID, err)
		}

		result.Migrated++
	}

	s.Hub.NotifyChange("task", projectID, "migrated")
	return result
}

// taskToCreateRequest converts a local store.Task to a CreateTaskRequest.
func taskToCreateRequest(task store.Task, parentID string) store.CreateTaskRequest {
	dueDate := ""
	if task.DueDate != nil {
		dueDate = task.DueDate.Format("2006-01-02")
	}
	return store.CreateTaskRequest{
		ProjectID:   task.ProjectID,
		ParentID:    parentID,
		Title:       task.Title,
		Description: task.Description,
		Status:      task.Status,
		Priority:    task.Priority,
		Assigner:    task.Assigner,
		DueDate:     dueDate,
		Labels:      task.Labels,
	}
}

// migrateTaskComments migrates all local comments for a task to a GitLab issue.
func (s *Server) migrateTaskComments(ctx context.Context, client *gitlab.Client, projectPath string, localTaskID string, gitlabIID int) {
	comments, err := s.Store.ListTaskComments(localTaskID)
	if err != nil {
		log.Printf("[migrate] failed to list comments for task %s: %v", localTaskID, err)
		return
	}
	for _, c := range comments {
		body := c.Body
		if c.Author != "" && c.Author != "local" {
			body = fmt.Sprintf("**@%s** commented:\n\n%s", c.Author, c.Body)
		}
		if _, err := client.CreateIssueComment(ctx, projectPath, gitlabIID, body); err != nil {
			log.Printf("[migrate] failed to migrate comment %s for task %s: %v", c.ID, localTaskID, err)
		}
	}
}
