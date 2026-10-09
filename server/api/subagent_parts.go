package api

import "strings"

func subAgentStatusIsError(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "stopped", "killed":
		return true
	default:
		return false
	}
}

// recordSubAgentDescription persists the readable task description on the
// launching Agent/Task tool. The final child response remains a tool_result so
// the two pieces do not overwrite each other.
func recordSubAgentDescription(parts []MessagePart, toolUseID, description string) ([]MessagePart, bool) {
	if toolUseID == "" || strings.TrimSpace(description) == "" {
		return parts, false
	}
	for i := range parts {
		if parts[i].Type == "tool_use" && parts[i].ToolCallID == toolUseID {
			if parts[i].Summary == description {
				return parts, false
			}
			parts[i].Summary = description
			return parts, true
		}
	}
	return parts, false
}

// recordSubAgentCompletion marks the Agent card terminal and replaces its
// launch receipt (when present) with the actual child result. Repeated Claude
// completion notifications update the same result rather than adding copies.
func recordSubAgentCompletion(parts []MessagePart, toolUseID, output string, isError bool) ([]MessagePart, bool) {
	if toolUseID == "" {
		return parts, false
	}

	changed := false
	wantState := "complete"
	if isError {
		wantState = "error"
	}
	toolUseIndex := -1
	for i := range parts {
		if parts[i].Type == "tool_use" && parts[i].ToolCallID == toolUseID {
			toolUseIndex = i
			if parts[i].State != wantState {
				parts[i].State = wantState
				changed = true
			}
		}
	}

	resultIndex := -1
	for i := range parts {
		if parts[i].Type == "tool_result" && parts[i].ToolCallID == toolUseID {
			resultIndex = i
		}
	}
	if resultIndex >= 0 {
		if parts[resultIndex].Output != output || parts[resultIndex].IsError != isError {
			parts[resultIndex].Output = output
			parts[resultIndex].IsError = isError
			changed = true
		}
	} else if output != "" || isError {
		result := MessagePart{
			Type:       "tool_result",
			ToolCallID: toolUseID,
			Output:     output,
			IsError:    isError,
		}
		if toolUseIndex >= 0 {
			// Keep the result in the same persisted tool group as its Agent card,
			// even if main-agent text arrived while the child was running.
			parts = append(parts, MessagePart{})
			copy(parts[toolUseIndex+2:], parts[toolUseIndex+1:])
			parts[toolUseIndex+1] = result
		} else {
			parts = append(parts, result)
		}
		changed = true
	}

	return parts, changed
}
