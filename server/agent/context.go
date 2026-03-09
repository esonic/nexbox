package agent

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Lightweight types for context building (no external dependencies)
// ---------------------------------------------------------------------------

// ProjectInfo holds project data needed for agent context.
type ProjectInfo struct {
	ID          string
	Name        string
	Description string
}

// TaskRef holds task data needed for agent context.
type TaskRef struct {
	ID          string
	Title       string
	Description string
	Status      string
	Priority    string
}

// AssetRef holds asset data needed for agent context.
type AssetRef struct {
	ID     string
	Type   string
	Title  string
	Status string
}

// WorkflowRef holds workflow data needed for agent context.
type WorkflowRef struct {
	ID        string
	Name      string
	Status    string
	NodeCount int
	EdgeCount int
}

// MindmapRef holds mindmap data needed for agent context.
type MindmapRef struct {
	Title string
}

// BuildStaticContext constructs the fixed part of the prompt.
// This is only sent once when creating a new session.
func BuildStaticContext(project ProjectInfo, sessionID string) string {
	var sb strings.Builder

	sb.WriteString("You are working within the BoxNex AI platform.\n\n")

	sb.WriteString(fmt.Sprintf("Project: %s\n", project.Name))
	if project.Description != "" {
		sb.WriteString(fmt.Sprintf("Project Description: %s\n", project.Description))
	}
	sb.WriteString(fmt.Sprintf("Project ID: %s\n", project.ID))

	return sb.String()
}

// BuildDynamicContext constructs the variable part of the prompt.
// This is sent with every message to reflect current session references.
// When project is non-nil, a compact reminder of key project params is included
// so the model never loses track of project_id or working directory.
func BuildDynamicContext(project *ProjectInfo, refTasks []TaskRef, refAssets []AssetRef, refWorkflows []WorkflowRef) string {
	if project == nil && len(refTasks) == 0 && len(refAssets) == 0 && len(refWorkflows) == 0 {
		return ""
	}

	var sb strings.Builder

	if project != nil {
		sb.WriteString(fmt.Sprintf("[Project: %s | ID: %s]\n", project.Name, project.ID))
	}

	if len(refTasks) == 0 && len(refAssets) == 0 && len(refWorkflows) == 0 {
		return sb.String()
	}

	sb.WriteString("[Session References]\n")

	if len(refTasks) > 0 {
		sb.WriteString("Tasks:\n")
		for _, t := range refTasks {
			sb.WriteString(fmt.Sprintf("- [ID: %s] %s (priority: %s, status: %s)\n", t.ID, t.Title, t.Priority, t.Status))
			if t.Description != "" {
				sb.WriteString(fmt.Sprintf("  Description: %s\n", t.Description))
			}
		}
		sb.WriteString("\n")
	}

	if len(refAssets) > 0 {
		sb.WriteString("Assets:\n")
		for _, a := range refAssets {
			sb.WriteString(fmt.Sprintf("- [ID: %s, Type: %s] %s (status: %s)\n", a.ID, a.Type, a.Title, a.Status))
		}
		sb.WriteString("\n")
	}

	if len(refWorkflows) > 0 {
		if len(refTasks) > 0 {
			sb.WriteString("Referenced workflow for execution:\n")
			for _, w := range refWorkflows {
				sb.WriteString(fmt.Sprintf("- [ID: %s] %s (%d nodes, %d edges)\n", w.ID, w.Name, w.NodeCount, w.EdgeCount))
			}

			var taskIDList []string
			for _, t := range refTasks {
				taskIDList = append(taskIDList, t.ID)
			}
			allIDs := strings.Join(taskIDList, ",")

			sb.WriteString("\nWorkflow Execution Instructions:\n")
			sb.WriteString(fmt.Sprintf("All %d tasks above should be executed together in a SINGLE workflow run.\n", len(refTasks)))
			sb.WriteString("1. Call execute_workflow with:\n")
			sb.WriteString("   - workflow_id: the workflow ID above\n")
			sb.WriteString(fmt.Sprintf("   - task_ids: \"%s\" (ALL task IDs, comma-separated)\n", allIDs))
			sb.WriteString("   - project_id: the current project ID\n")
			sb.WriteString("Do NOT call execute_workflow once per task. Pass all task IDs in a single call.\n")
		} else {
			sb.WriteString("Currently editing workflow:\n")
			for _, w := range refWorkflows {
				sb.WriteString(fmt.Sprintf("- [ID: %s] %s (status: %s, %d nodes, %d edges)\n", w.ID, w.Name, w.Status, w.NodeCount, w.EdgeCount))
			}
			sb.WriteString("Use get_workflow / update_workflow MCP tools to read or modify workflow definitions.\n")
		}
	}

	return sb.String()
}

// BuildPrompt constructs the full prompt for an agent request.
// For new sessions: staticContext + dynamicContext + message
// For resumed sessions: dynamicContext + message
// If images are provided, they are referenced as file paths for agents to read.
func BuildPrompt(staticContext, dynamicContext, message string, isNewSession bool, images []string) string {
	var parts []string

	if isNewSession && staticContext != "" {
		parts = append(parts, staticContext)
	}
	if dynamicContext != "" {
		parts = append(parts, dynamicContext)
	}
	if len(images) > 0 {
		var sb strings.Builder
		sb.WriteString("[Attached Images]\n")
		sb.WriteString("The user attached following image(s). Use the Read tool to view them.\n")
		for i, img := range images {
			sb.WriteString(fmt.Sprintf("  Image %d: %s\n", i+1, img))
		}
		parts = append(parts, sb.String())
	}
	parts = append(parts, "[User Message]\n"+message)

	return strings.Join(parts, "\n")
}
