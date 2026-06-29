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
	GitRepoPath string
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

// MindmapRef holds mindmap data needed for agent context.
type MindmapRef struct {
	Title string
}

// BuildStaticContext constructs the fixed part of the prompt.
// This is only sent once when creating a new session.
func BuildStaticContext(project ProjectInfo, sessionID string) string {
	var sb strings.Builder

	sb.WriteString("[Platform Context]\n")
	sb.WriteString("You are working within the Nexbox AI platform.\n\n")

	sb.WriteString(fmt.Sprintf("Project: %s\n", project.Name))
	if project.Description != "" {
		sb.WriteString(fmt.Sprintf("Project Description: %s\n", project.Description))
	}
	sb.WriteString(fmt.Sprintf("Project ID: %s\n", project.ID))
	sb.WriteString(fmt.Sprintf("Working Directory: %s\n", project.GitRepoPath))

	return sb.String()
}

// BuildDynamicContext constructs the variable part of the prompt.
// This is sent with every message to reflect current session references.
// When project is non-nil, a compact reminder of key project params is included
// so the model never loses track of project_id or working directory.
func BuildDynamicContext(project *ProjectInfo, refTasks []TaskRef, refAssets []AssetRef) string {
	if project == nil && len(refTasks) == 0 && len(refAssets) == 0 {
		return ""
	}

	var sb strings.Builder

	if project != nil {
		sb.WriteString(fmt.Sprintf("[Project: %s | ID: %s | Dir: %s]\n", project.Name, project.ID, project.GitRepoPath))
	}

	if len(refTasks) == 0 && len(refAssets) == 0 {
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

	return sb.String()
}

// BuildPrompt constructs the full prompt for an agent request.
// For new sessions: staticContext + dynamicContext + message
// For resumed sessions: dynamicContext + message
// If images are provided, they are referenced as file paths for agents to read.
//
// When the message begins with a slash command (e.g. "/review"), we bypass
// context/image wrapping so the underlying CLI (Claude/Codex) sees the command
// as the raw first character and can route it through its own slash-command
// handler instead of treating the Nexbox context preamble as plain text.
func BuildPrompt(staticContext, dynamicContext, message string, isNewSession bool, images []string) string {
	if isSlashCommand(message) {
		return message
	}

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
		sb.WriteString("The user has attached the following image(s) for analysis. Use the Read tool to view them.\n")
		for i, img := range images {
			sb.WriteString(fmt.Sprintf("  Image %d: %s\n", i+1, img))
		}
		parts = append(parts, sb.String())
	}
	parts = append(parts, "[User Message]\n"+message)

	return strings.Join(parts, "\n")
}

// BuildInterjectPrompt frames a mid-turn interjection so the model understands
// it arrived while it was already working. It mirrors the Claude Code TUI's
// wrapper (the stream-json interface itself adds none): finish the current task,
// then address the new message.
func BuildInterjectPrompt(message string, images []string) string {
	var sb strings.Builder
	sb.WriteString("The user sent a new message while you were working:\n\n")
	if len(images) > 0 {
		sb.WriteString("[Attached Images]\n")
		sb.WriteString("The user has attached the following image(s) for analysis. Use the Read tool to view them.\n")
		for i, img := range images {
			sb.WriteString(fmt.Sprintf("  Image %d: %s\n", i+1, img))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(message)
	sb.WriteString("\n\nIMPORTANT: After completing your current task, address the message above.")
	return sb.String()
}

// isSlashCommand reports whether the message is a bare slash command that
// should be forwarded to the agent CLI verbatim. It returns true when the
// first non-whitespace character is '/' and the second character is a letter
// (so absolute paths like "/home/..." are not misinterpreted).
func isSlashCommand(message string) bool {
	trimmed := strings.TrimLeft(message, " \t\r\n")
	if len(trimmed) < 2 || trimmed[0] != '/' {
		return false
	}
	c := trimmed[1]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

