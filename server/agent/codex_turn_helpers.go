package agent

import (
	"fmt"
	"strings"
)

func formatCodexPlanUpdate(n codexTurnPlanUpdatedNotification) string {
	var lines []string
	if n.Explanation != "" {
		lines = append(lines, n.Explanation)
	}
	for _, step := range n.Plan {
		lines = append(lines, fmt.Sprintf("- [%s] %s", step.Status, step.Step))
	}
	return strings.Join(lines, "\n")
}

func isAppToolApprovalRequest(questions []struct {
	ID       string `json:"id"`
	Header   string `json:"header"`
	Question string `json:"question"`
	Options  []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"options"`
}) bool {
	if len(questions) == 0 {
		return false
	}
	for _, q := range questions {
		header := strings.ToLower(strings.TrimSpace(q.Header))
		prompt := strings.ToLower(strings.TrimSpace(q.Question))
		if strings.Contains(header, "approve app tool call") ||
			(strings.Contains(prompt, "allow this action") && strings.Contains(prompt, "tool")) {
			return true
		}
	}
	return false
}

func pickApprovalAnswerLabel(options []struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}) string {
	if len(options) == 0 {
		return ""
	}
	preferred := []string{
		"approve once",
		"approve this session",
		"allow",
		"yes",
	}
	for _, p := range preferred {
		for _, opt := range options {
			label := strings.TrimSpace(opt.Label)
			if label == "" {
				continue
			}
			lower := strings.ToLower(label)
			if lower == p || strings.Contains(lower, p) {
				return label
			}
		}
	}
	// Fallback to first non-empty option.
	for _, opt := range options {
		if label := strings.TrimSpace(opt.Label); label != "" {
			return label
		}
	}
	return ""
}
