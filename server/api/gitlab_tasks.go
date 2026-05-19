package api

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"gitlab.okg.com/sre_ai/nexbox/gitlab"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

// ---------------------------------------------------------------------------
// GitLab project resolution helpers
// ---------------------------------------------------------------------------

// gitlabProjectInfo holds the resolved GitLab context for a project.
type gitlabProjectInfo struct {
	ProjectPath string // e.g. "sre_ai/nexbox"
	Token       string // GitLab personal access token
}

// resolveGitLabProject checks if a project is GitLab-backed and returns its context.
// Returns nil if the project is not a GitLab project or token is missing.
func (s *Server) resolveGitLabProject(projectID string) *gitlabProjectInfo {
	if projectID == "" {
		return nil
	}
	project, err := s.Store.GetProject(projectID)
	if err != nil || project == nil {
		return nil
	}
	if project.GitLabRepoURL == "" {
		return nil
	}
	token := s.Config.Get().GitLabToken
	if token == "" {
		return nil
	}
	projectPath, err := gitlab.ProjectPathFromURL(project.GitLabRepoURL)
	if err != nil {
		log.Printf("[gitlab_tasks] failed to extract project path from %s: %v", project.GitLabRepoURL, err)
		return nil
	}
	return &gitlabProjectInfo{
		ProjectPath: projectPath,
		Token:       token,
	}
}

// resolveGitLabTask resolves a GitLab task ID to its project info and IID.
func (s *Server) resolveGitLabTask(taskID string) (*gitlabProjectInfo, int, error) {
	projectID, iid, err := gitlab.ParseTaskID(taskID)
	if err != nil {
		return nil, 0, err
	}
	info := s.resolveGitLabProject(projectID)
	if info == nil {
		return nil, 0, fmt.Errorf("cannot resolve gitlab project for task %s", taskID)
	}
	return info, iid, nil
}

// ---------------------------------------------------------------------------
// GitLab issue ↔ store.Task conversion
// ---------------------------------------------------------------------------

func gitlabIssueToTask(issue *gitlab.Issue, projectID string) store.Task {
	status := gitlab.MapGitLabStatus(issue.State)
	priority := gitlab.PriorityFromWeight(issue.Weight)
	labels := issue.Labels

	t := store.Task{
		ID:           gitlab.MakeTaskID(projectID, issue.IID),
		ProjectID:    projectID,
		Title:        issue.Title,
		Description:  issue.Description,
		Status:       status,
		Priority:     priority,
		CreatedBy:    issue.Author.Username,
		Version:      1,
		CreatedAt:    issue.CreatedAt,
		UpdatedAt:    issue.UpdatedAt,
		ClosedAt:     issue.ClosedAt,
		Labels:       labels,
		Source:       "gitlab",
		ExternalID:   strconv.Itoa(issue.IID),
		ExternalURL:  issue.WebURL,
		ExternalType: "Issue", // REST API only returns Issues; overridden by GraphQL when available
	}

	if issue.Assignee != nil {
		t.Assigner = issue.Assignee.Username
	}

	if issue.DueDate != "" {
		if parsed, err := time.Parse("2006-01-02", issue.DueDate); err == nil {
			t.DueDate = &parsed
		}
	}

	return t
}

func gitlabChildToTask(child gitlab.WorkItemChild, projectID, parentTaskID string) store.Task {
	childIID, _ := strconv.Atoi(child.IID)
	return store.Task{
		ID:           gitlab.MakeTaskID(projectID, childIID),
		ProjectID:    projectID,
		ParentID:     parentTaskID,
		Title:        child.Title,
		Status:       gitlab.MapGitLabStatus(strings.ToLower(child.State)),
		Priority:     "medium",
		Version:      1,
		Source:       "gitlab",
		ExternalID:   child.IID,
		ExternalType: child.Type,
		Labels:       []string{},
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
}

func gitlabCommentToTaskComment(c *gitlab.IssueComment, taskID string) store.TaskComment {
	return store.TaskComment{
		ID:        fmt.Sprintf("glc:%s:%d", taskID, c.ID),
		TaskID:    taskID,
		Author:    c.Author.Username,
		Body:      c.Body,
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}

// ---------------------------------------------------------------------------
// GitLab task CRUD (called from API handlers)
// ---------------------------------------------------------------------------

func (s *Server) gitlabListTasks(ctx context.Context, info *gitlabProjectInfo, projectID string, limit, offset int, search string) ([]store.Task, error) {
	client := gitlab.NewClient(info.Token)

	// Use GraphQL workItems query to get all items (Issue + Task) with hierarchy in one call
	entries, err := client.ListWorkItems(ctx, info.ProjectPath, limit, offset, search)
	if err != nil {
		// Fallback to REST API if GraphQL fails
		log.Printf("[gitlab_tasks] ListWorkItems failed, falling back to REST: %v", err)
		return s.gitlabListTasksREST(ctx, info, projectID, limit, offset, search)
	}

	tasks := make([]store.Task, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		iid, _ := strconv.Atoi(entry.IID)
		taskID := gitlab.MakeTaskID(projectID, iid)
		seen[taskID] = true
		status := gitlab.MapGitLabStatus(strings.ToLower(entry.State))

		labels := entry.Labels
		if labels == nil {
			labels = []string{}
		}

		t := store.Task{
			ID:           taskID,
			ProjectID:    projectID,
			Title:        entry.Title,
			Status:       status,
			Priority:     gitlab.PriorityFromWeight(entry.Weight),
			Version:      1,
			CreatedAt:    entry.CreatedAt,
			UpdatedAt:    entry.UpdatedAt,
			Labels:       labels,
			Source:       "gitlab",
			ExternalID:   entry.IID,
			ExternalType: entry.TypeName,
			ChildCount:   len(entry.Children),
		}

		// Set parent if this is a child item
		if entry.ParentIID != "" {
			parentIID, _ := strconv.Atoi(entry.ParentIID)
			t.ParentID = gitlab.MakeTaskID(projectID, parentIID)
		}

		tasks = append(tasks, t)
	}

	// Flatten children from hierarchy widgets so child Tasks also appear in the list
	for _, entry := range entries {
		if len(entry.Children) == 0 {
			continue
		}
		parentIID, _ := strconv.Atoi(entry.IID)
		parentTaskID := gitlab.MakeTaskID(projectID, parentIID)
		for _, child := range entry.Children {
			ct := gitlabChildToTask(child, projectID, parentTaskID)
			if seen[ct.ID] {
				continue // already in the list as a top-level entry
			}
			seen[ct.ID] = true
			tasks = append(tasks, ct)
		}
	}

	return tasks, nil
}

// gitlabListTasksREST is the fallback using REST API when GraphQL is unavailable.
func (s *Server) gitlabListTasksREST(ctx context.Context, info *gitlabProjectInfo, projectID string, limit, offset int, search string) ([]store.Task, error) {
	client := gitlab.NewClient(info.Token)
	issues, err := client.ListIssues(ctx, info.ProjectPath, limit, offset, search)
	if err != nil {
		return nil, err
	}

	tasks := make([]store.Task, 0, len(issues))
	for _, issue := range issues {
		t := gitlabIssueToTask(&issue, projectID)
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func (s *Server) gitlabGetTask(ctx context.Context, info *gitlabProjectInfo, projectID string, iid int) (*store.Task, error) {
	client := gitlab.NewClient(info.Token)
	issue, err := client.GetIssue(ctx, info.ProjectPath, iid)
	if err != nil {
		return nil, err
	}
	t := gitlabIssueToTask(issue, projectID)

	// Fetch hierarchy (parent + children) via GraphQL
	hierarchy, err := client.FetchWorkItemHierarchy(ctx, info.ProjectPath, iid)
	if err != nil {
		log.Printf("[gitlab_tasks] warning: failed to fetch hierarchy for %s#%d: %v", info.ProjectPath, iid, err)
	} else {
		// Set type
		if hierarchy.TypeName != "" {
			t.ExternalType = hierarchy.TypeName
		}
		// Set parent
		if hierarchy.ParentIID != "" {
			parentIID, _ := strconv.Atoi(hierarchy.ParentIID)
			t.ParentID = gitlab.MakeTaskID(projectID, parentIID)
		}
		// Set children
		if len(hierarchy.Children) > 0 {
			t.ChildCount = len(hierarchy.Children)
			for _, child := range hierarchy.Children {
				t.Children = append(t.Children, gitlabChildToTask(child, projectID, t.ID))
			}
		}
	}

	return &t, nil
}

func (s *Server) gitlabCreateTask(ctx context.Context, info *gitlabProjectInfo, projectID string, req store.CreateTaskRequest) (*store.Task, error) {
	client := gitlab.NewClient(info.Token)

	// If ParentID is set, create as a child Task work item
	if req.ParentID != "" && gitlab.IsGitLabTaskID(req.ParentID) {
		_, parentIID, err := gitlab.ParseTaskID(req.ParentID)
		if err != nil {
			return nil, fmt.Errorf("invalid parent task id: %w", err)
		}

		parentWI, err := client.GetWorkItemInfo(ctx, info.ProjectPath, parentIID)
		if err != nil {
			return nil, fmt.Errorf("get parent work item: %w", err)
		}

		taskTypeID, err := client.GetTaskTypeID(ctx, info.ProjectPath)
		if err != nil {
			return nil, fmt.Errorf("get task type id: %w", err)
		}

		childResult, err := client.CreateChildTask(ctx, info.ProjectPath,
			req.Title, req.Description, parentWI.GID, taskTypeID)
		if err != nil {
			return nil, fmt.Errorf("create child task: %w", err)
		}

		// Set labels on child work item via REST
		if len(req.Labels) > 0 {
			if _, err := client.UpdateIssue(ctx, info.ProjectPath, childResult.IID, map[string]any{
				"labels": strings.Join(req.Labels, ","),
			}); err != nil {
				log.Printf("[gitlab_tasks] failed to set labels on child task #%d: %v", childResult.IID, err)
			}
		}

		// Set weight (priority) on the child work item
		if req.Priority != "" {
			weight := gitlab.WeightFromPriority(req.Priority)
			if err := client.UpdateWorkItemWeight(ctx, childResult.GID, weight); err != nil {
				log.Printf("[gitlab_tasks] failed to set weight on child task #%d: %v", childResult.IID, err)
			}
		}

		// Close if needed
		if req.Status == "closed" {
			if _, err := client.UpdateIssue(ctx, info.ProjectPath, childResult.IID, map[string]any{
				"state_event": "close",
			}); err != nil {
				log.Printf("[gitlab_tasks] failed to close child task #%d: %v", childResult.IID, err)
			}
		}

		now := time.Now()
		labels := req.Labels
		if labels == nil {
			labels = []string{}
		}
		t := store.Task{
			ID:           gitlab.MakeTaskID(projectID, childResult.IID),
			ProjectID:    projectID,
			ParentID:     req.ParentID,
			Title:        childResult.Title,
			Status:       "open",
			Priority:     req.Priority,
			Source:       "gitlab",
			ExternalType: "Task",
			Labels:       labels,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if t.Priority == "" {
			t.Priority = "medium"
		}
		return &t, nil
	}

	// No parent — create a top-level Issue via REST API
	issue, err := client.CreateIssue(ctx, info.ProjectPath,
		req.Title, req.Description, req.Assigner, req.Priority, req.Labels, req.DueDate)
	if err != nil {
		return nil, err
	}

	// If status is "closed", close the issue immediately
	if req.Status == "closed" {
		updated, err := client.UpdateIssue(ctx, info.ProjectPath, issue.IID, map[string]any{
			"state_event": "close",
		})
		if err == nil {
			issue = updated
		}
	}

	t := gitlabIssueToTask(issue, projectID)
	return &t, nil
}

func (s *Server) gitlabUpdateTask(ctx context.Context, info *gitlabProjectInfo, projectID string, iid int, req store.UpdateTaskRequest) (*store.Task, error) {
	client := gitlab.NewClient(info.Token)
	updates := map[string]any{}

	if req.Title != nil {
		updates["title"] = *req.Title
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.Status != nil {
		if event := gitlab.MapStatusToGitLab(*req.Status); event != "" {
			updates["state_event"] = event
		}
	}
	if req.Priority != nil {
		updates["weight"] = gitlab.WeightFromPriority(*req.Priority)
	}
	if req.Assigner != nil {
		if *req.Assigner == "" {
			updates["assignee_ids"] = []int{}
		} else if uid, err := client.GetUserIDByUsername(ctx, *req.Assigner); err == nil && uid > 0 {
			updates["assignee_ids"] = []int{uid}
		}
	}
	if req.DueDate != nil {
		if *req.DueDate == "" {
			updates["due_date"] = nil
		} else {
			updates["due_date"] = *req.DueDate
		}
	}

	if len(updates) == 0 {
		return s.gitlabGetTask(ctx, info, projectID, iid)
	}

	_, err := client.UpdateIssue(ctx, info.ProjectPath, iid, updates)
	if err != nil {
		return nil, err
	}
	// Re-fetch full task (includes hierarchy + ExternalType from GraphQL)
	return s.gitlabGetTask(ctx, info, projectID, iid)
}

func (s *Server) gitlabDeleteTask(ctx context.Context, info *gitlabProjectInfo, iid int) error {
	client := gitlab.NewClient(info.Token)
	err := client.DeleteIssue(ctx, info.ProjectPath, iid)
	if err != nil && strings.Contains(err.Error(), "403") {
		return fmt.Errorf("you don't have the permission to delete the task. Please contact the repo maintainer/owner.")
	}
	return err
}

// ---------------------------------------------------------------------------
// GitLab task comments
// ---------------------------------------------------------------------------

func (s *Server) gitlabListComments(ctx context.Context, info *gitlabProjectInfo, taskID string, iid int) ([]store.TaskComment, error) {
	client := gitlab.NewClient(info.Token)
	comments, err := client.ListIssueComments(ctx, info.ProjectPath, iid)
	if err != nil {
		return nil, err
	}

	result := make([]store.TaskComment, 0, len(comments))
	for _, c := range comments {
		result = append(result, gitlabCommentToTaskComment(&c, taskID))
	}
	return result, nil
}

func (s *Server) gitlabCreateComment(ctx context.Context, info *gitlabProjectInfo, taskID string, iid int, body string) (*store.TaskComment, error) {
	client := gitlab.NewClient(info.Token)
	comment, err := client.CreateIssueComment(ctx, info.ProjectPath, iid, body)
	if err != nil {
		return nil, err
	}
	tc := gitlabCommentToTaskComment(comment, taskID)
	return &tc, nil
}

// ---------------------------------------------------------------------------
// GitLab set parent-child (Work Items API)
// ---------------------------------------------------------------------------

// gitlabSetParent sets a GitLab work item (Issue or Task) as a child of another.
func (s *Server) gitlabSetParent(ctx context.Context, childTaskID, parentTaskID string) error {
	parentInfo, parentIID, err := s.resolveGitLabTask(parentTaskID)
	if err != nil {
		return fmt.Errorf("resolve parent: %w", err)
	}
	childInfo, childIID, err := s.resolveGitLabTask(childTaskID)
	if err != nil {
		return fmt.Errorf("resolve child: %w", err)
	}

	client := gitlab.NewClient(parentInfo.Token)

	parentWI, err := client.GetWorkItemInfo(ctx, parentInfo.ProjectPath, parentIID)
	if err != nil {
		return fmt.Errorf("get parent work item info: %w", err)
	}

	childWI, err := client.GetWorkItemInfo(ctx, childInfo.ProjectPath, childIID)
	if err != nil {
		return fmt.Errorf("get child work item info: %w", err)
	}

	return client.SetWorkItemParent(ctx, childWI.GID, parentWI.GID)
}
