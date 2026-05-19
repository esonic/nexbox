package api

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"gitlab.okg.com/sre_ai/nexbox/store"
)

var hexPrefixRe = regexp.MustCompile(`^[0-9a-fA-F]{8}$`)

// HandleLarkChat processes a lark_chat change record:
// resolves project, finds/creates session, injects message, waits for AI, sends reply.
func (s *Server) HandleLarkChat(ch store.Change) {
	rs := s.Cloud
	if rs == nil {
		return
	}
	userName := rs.Info.UserName

	// Silently ignore messages not belonging to this user
	if ch.EntityID != userName {
		return
	}

	text := strings.TrimSpace(ch.Detail)
	if text == "" {
		return
	}

	log.Printf("[lark-chat] processing message id=%d: %s", ch.ID, truncate(text, 80))

	projectID, message := s.parseLarkChatMessage(text)
	if projectID == "" {
		return // error already sent via lark-reply
	}

	// Special command: create a new session (same as clicking "New Session" in the UI).
	// Subsequent messages will naturally land on this session since it becomes the most recently updated one.
	if message == "new-session" {
		sess, err := s.Store.CreateSession(CreateSessionRequest{
			Name:      "Lark Chat",
			ProjectID: projectID,
		})
		if err != nil {
			s.larkReply(rs, userName, fmt.Sprintf("Failed to create session: %s", err))
			return
		}
		s.larkReply(rs, userName, fmt.Sprintf("New session created: %s", sess.ID[:8]))
		return
	}

	sessionID, err := s.findOrCreateSession(projectID)
	if err != nil {
		log.Printf("[lark-chat] failed to find/create session: %v", err)
		s.larkReply(rs, userName, fmt.Sprintf("Failed to find/create session: %s", err))
		return
	}

	log.Printf("[lark-chat] injecting into session %s, project %s", sessionID, projectID[:8])

	if s.SessionManager.IsActive(sessionID) {
		s.larkReply(rs, userName, "A request is already in progress in this session. Please wait.")
		return
	}

	s.handleUserMessage(sessionID, message, nil, nil, nil)

	// Wait for agent to finish (poll with timeout)
	const maxWait = 30 * time.Minute
	const pollInterval = 10 * time.Second
	deadline := time.Now().Add(maxWait)

	// Give the agent a moment to start
	time.Sleep(1 * time.Second)

	for time.Now().Before(deadline) {
		if !s.SessionManager.IsActive(sessionID) {
			break
		}
		time.Sleep(pollInterval)
	}

	if s.SessionManager.IsActive(sessionID) {
		s.larkReply(rs, userName, "Agent timed out.")
		return
	}

	reply := s.extractLatestAssistantReply(sessionID)
	if reply == "" {
		reply = "(No response from agent)"
	}

	const maxLarkLen = 14000
	if len(reply) > maxLarkLen {
		reply = reply[:maxLarkLen] + "\n\n...(truncated, full response available in BeeHub)"
	}

	if err := rs.SendLarkReply(userName, reply); err != nil {
		log.Printf("[lark-chat] failed to send lark reply: %v", err)
	} else {
		log.Printf("[lark-chat] reply sent to %s (%d chars)", userName, len(reply))
	}
}

// parseLarkChatMessage extracts optional project prefix and message content.
// Returns (projectID, message). If project cannot be resolved, sends error via lark-reply and returns ("", "").
func (s *Server) parseLarkChatMessage(text string) (string, string) {
	rs := s.Cloud
	userName := rs.Info.UserName

	projects := s.Config.Get().Projects
	log.Printf("[lark-chat] user=%s, local projects=%d", userName, len(projects))
	if len(projects) == 0 {
		s.larkReply(rs, userName, "No projects configured locally. Please add a project in BeeHub first.")
		return "", ""
	}

	parts := strings.SplitN(text, " ", 2)
	firstWord := parts[0]

	// Try matching first word as 8-char hex project prefix
	if hexPrefixRe.MatchString(firstWord) {
		prefix := strings.ToLower(firstWord)
		for _, p := range projects {
			if strings.HasPrefix(strings.ToLower(p.RemoteID), prefix) {
				msg := ""
				if len(parts) > 1 {
					msg = parts[1]
				}
				if strings.TrimSpace(msg) == "" {
					s.larkReply(rs, userName, "Message content is empty.")
					return "", ""
				}
				return p.RemoteID, strings.TrimSpace(msg)
			}
		}
		s.replyProjectList(rs, userName, fmt.Sprintf("Prefix `%s` did not match any project.", prefix))
		return "", ""
	}

	// No prefix provided
	if len(projects) == 1 {
		log.Printf("[lark-chat] auto-matched project %s", projects[0].RemoteID[:8])
		return projects[0].RemoteID, text
	}

	// Multiple projects, no prefix → reply with list
	s.replyProjectList(rs, userName, "Multiple projects found. Please specify a project prefix:")
	return "", ""
}

// larkReply sends a message to the user via Lark, logging errors.
func (s *Server) larkReply(rs *store.RemoteStore, userName, msg string) {
	if err := rs.SendLarkReply(userName, msg); err != nil {
		log.Printf("[lark-chat] SendLarkReply failed for %s: %v", userName, err)
	}
}

func (s *Server) replyProjectList(rs *store.RemoteStore, userName, header string) {
	projects := s.Config.Get().Projects
	var sb strings.Builder
	sb.WriteString(header + "\n\n")
	for _, p := range projects {
		name := p.RemoteID
		if proj, err := s.Store.GetProject(p.RemoteID); err == nil && proj != nil {
			name = proj.Name
		}
		id8 := p.RemoteID
		if len(id8) > 8 {
			id8 = id8[:8]
		}
		sb.WriteString(fmt.Sprintf("* `%s` — %s\n", id8, name))
	}
	sb.WriteString("\nUsage: `nex-chat <8-char-prefix> <message>`")
	s.larkReply(rs, userName, sb.String())
}

// findOrCreateSession returns the most recently updated session for the project,
// or creates a new one if none exists.
func (s *Server) findOrCreateSession(projectID string) (string, error) {
	sessions, err := s.Store.ListSessions()
	if err != nil {
		return "", err
	}

	// Sessions are returned ordered by updated_at DESC
	for _, sess := range sessions {
		if sess.ProjectID == projectID {
			return sess.ID, nil
		}
	}

	// Create new session
	newSess, err := s.Store.CreateSession(CreateSessionRequest{
		Name:      "Lark Chat",
		ProjectID: projectID,
	})
	if err != nil {
		return "", err
	}
	return newSess.ID, nil
}

// extractLatestAssistantReply reads the latest assistant message and formats it for Lark.
// Text parts are included as-is; tool calls are summarized as brief step titles;
// thinking and tool_result parts are omitted.
func (s *Server) extractLatestAssistantReply(sessionID string) string {
	messages, err := s.Store.ListMessages(sessionID)
	if err != nil || len(messages) == 0 {
		return ""
	}

	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}

		var out []string
		var toolSteps []string

		for _, part := range messages[i].Parts {
			switch part.Type {
			case "text":
				if len(toolSteps) > 0 {
					out = append(out, formatToolSteps(toolSteps))
					toolSteps = nil
				}
				if strings.TrimSpace(part.Content) != "" {
					out = append(out, part.Content)
				}
			case "tool_use":
				name := part.ToolName
				if name == "" {
					name = "step"
				}
				toolSteps = append(toolSteps, name)
			}
		}
		if len(toolSteps) > 0 {
			out = append(out, formatToolSteps(toolSteps))
		}

		return strings.Join(out, "\n")
	}
	return ""
}

func formatToolSteps(steps []string) string {
	if len(steps) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*(%d steps: ", len(steps)))
	for i, s := range steps {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(s)
	}
	sb.WriteString(")*")
	return sb.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
