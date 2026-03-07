package api

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
)

var mentionRe = regexp.MustCompile(`@([\w.\-]+)`)

// parseMentions extracts @username mentions from text.
func parseMentions(text string) []string {
	matches := mentionRe.FindAllStringSubmatch(text, -1)
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		u := m[1]
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// buildRecipient builds a comma-separated recipient string from candidates,
// deduplicating entries.
func buildRecipient(actor string, candidates ...string) string {
	seen := map[string]bool{}
	var out []string
	for _, c := range candidates {
		if c == "" || c == actor || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return strings.Join(out, ",")
}

// notificationDetail encodes a notification detail JSON string.
func notificationDetail(text, recipient, taskID, assetID, commentID, commentPreview string, lineNumber *int) string {
	m := map[string]interface{}{
		"text":      text,
		"recipient": recipient,
	}
	if taskID != "" {
		m["task_id"] = taskID
	}
	if assetID != "" {
		m["asset_id"] = assetID
	}
	if commentID != "" {
		m["comment_id"] = commentID
	}
	if commentPreview != "" {
		m["comment_preview"] = commentPreview
	}
	if lineNumber != nil {
		m["line_number"] = fmt.Sprintf("%d", *lineNumber)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// sendNotification inserts a notification change record via the remote store.
// Returns nil when s.Cloud is nil (local mode) or recipient is empty.
func (s *Server) sendNotification(projectID, entityID, summary, text, recipient, taskID, assetID, commentID, commentPreview string, lineNumber *int) error {
	if s.Cloud == nil || recipient == "" {
		return nil
	}
	if projectID == "" {
		log.Printf("[notify] skipping notification: entityID=%s has no project_id", entityID)
		return nil
	}
	actor := s.currentUser()
	detail := notificationDetail(text, recipient, taskID, assetID, commentID, commentPreview, lineNumber)
	v := 1
	if err := s.Cloud.InsertChange(projectID, "notification", entityID, "created", summary, detail, actor, &v); err != nil {
		log.Printf("[notify] InsertChange failed: %v", err)
		return err
	}
	return nil
}
