package agent

import (
	"encoding/json"
	"strings"
)

func emitCodexItemStarted(item codexThreadItem, cb EventCallback) {
	switch item.Type {
	case "commandExecution":
		cmdJSON, _ := json.Marshal(map[string]string{"command": item.Command})
		cb(StreamEvent{
			Type:     EventToolStart,
			ToolID:   item.ID,
			ToolName: "Bash",
			Input:    string(cmdJSON),
		})

	case "mcpToolCall":
		cb(StreamEvent{
			Type:     EventToolStart,
			ToolID:   item.ID,
			ToolName: codexToolDisplayName(item),
			Input:    string(item.Arguments),
		})

	case "dynamicToolCall":
		cb(StreamEvent{
			Type:     EventToolStart,
			ToolID:   item.ID,
			ToolName: item.Tool,
			Input:    string(item.Arguments),
		})

	case "collabAgentToolCall", "collabToolCall", "subAgentActivity":
		// Collaboration bookkeeping is handled by the thread router in
		// CodexAgent.SendMessage. Rendering these protocol items directly creates
		// empty subAgentActivity cards and noisy collab/wait rows.
		return

	case "webSearch":
		toolName, inputJSON := codexWebSearchToolStart(item)
		cb(StreamEvent{
			Type:     EventToolStart,
			ToolID:   item.ID,
			ToolName: toolName,
			Input:    inputJSON,
		})

	case "fileChange":
		cb(StreamEvent{
			Type:     EventToolStart,
			ToolID:   item.ID,
			ToolName: "FileChange",
			Input:    codexFileChangeInputJSON(item),
		})

	default:
		if !codexIsNonToolItem(item.Type) {
			cb(StreamEvent{
				Type:     EventToolStart,
				ToolID:   item.ID,
				ToolName: codexGenericToolName(item),
				Input:    codexGenericToolInput(item),
			})
		}
	}
}

func emitCodexItemCompleted(item codexThreadItem, agentStreamed, reasoningStreamed bool, cb EventCallback) {
	switch item.Type {
	case "commandExecution":
		output := ""
		if item.AggregatedOutput != nil {
			output = *item.AggregatedOutput
		}
		isError := item.Status == "failed" || item.Status == "declined"
		if item.ExitCode != nil && *item.ExitCode != 0 {
			isError = true
		}
		cb(StreamEvent{
			Type:    EventToolResult,
			ToolID:  item.ID,
			Output:  output,
			IsError: isError,
		})

	case "mcpToolCall":
		isError := item.Status == "failed" || (len(item.Error) > 0 && string(item.Error) != "null")
		cb(StreamEvent{
			Type:    EventToolResult,
			ToolID:  item.ID,
			Output:  codexMCPResultText(item.Result),
			IsError: isError,
		})

	case "dynamicToolCall":
		isError := item.Status == "failed" || (item.Success != nil && !*item.Success)
		cb(StreamEvent{
			Type:    EventToolResult,
			ToolID:  item.ID,
			Output:  codexDynamicToolOutputText(item.ContentItems),
			IsError: isError,
		})

	case "collabAgentToolCall", "collabToolCall", "subAgentActivity":
		// See the matching item-start case above. A subAgentActivity item
		// completes immediately after announcing the child; the child turn's own
		// turn/completed event is the authoritative completion signal.
		return

	case "webSearch":
		cb(StreamEvent{
			Type:   EventToolResult,
			ToolID: item.ID,
			Output: codexWebSearchResultText(item),
		})

	case "fileChange":
		// codex-cli may only fill in per-file `changes[].diff` by the time the
		// item completes (item/started can fire before the patch is actually
		// written); re-emit the input so late-arriving diffs aren't lost. This
		// is a no-op-looking duplicate on servers that already populate
		// changes at item/started, which is harmless (same content, replaces
		// in place rather than appending).
		if len(item.Changes) > 0 {
			cb(StreamEvent{
				Type:   EventToolInputReplace,
				ToolID: item.ID,
				Input:  codexFileChangeInputJSON(item),
			})
		}
		isError := item.Status == "failed"
		cb(StreamEvent{
			Type:    EventToolResult,
			ToolID:  item.ID,
			IsError: isError,
		})

	case "agentMessage":
		if !agentStreamed && item.Text != "" {
			cb(StreamEvent{Type: EventTextDelta, Content: item.Text})
		}

	case "reasoning":
		if reasoningStreamed {
			return
		}
		var parts []string
		if len(item.Content) > 0 {
			parts = append(parts, strings.Join(item.Content, "\n"))
		}
		if len(item.Summary) > 0 {
			parts = append(parts, strings.Join(item.Summary, "\n"))
		}
		if len(parts) == 0 && item.Text != "" {
			parts = append(parts, item.Text)
		}
		if len(parts) > 0 {
			cb(StreamEvent{
				Type:    EventThinking,
				ToolID:  item.ID,
				Content: strings.Join(parts, "\n"),
			})
		}

	case "plan":
		if item.Text != "" {
			cb(StreamEvent{
				Type:    EventSystem,
				Content: "Plan: " + item.Text,
			})
		}

	case "contextCompaction":
		cb(StreamEvent{
			Type:    EventSystem,
			Content: "Context compacted to fit within limits",
		})

	default:
		if !codexIsNonToolItem(item.Type) {
			isError := item.Status == "failed" || item.Status == "declined"
			cb(StreamEvent{
				Type:    EventToolResult,
				ToolID:  item.ID,
				Output:  codexGenericToolOutput(item),
				IsError: isError,
			})
		}
	}
}

type codexFileChangeEntry struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	// Diff is a unified diff (git-style ---/+++/@@) for "update", or the
	// full file content for "create"/"delete" (there is no "before"/
	// "after" to diff against in those cases).
	Diff string `json:"diff,omitempty"`
}

// codexFileChangeInputJSON builds the `{"changes": [...]}` JSON blob used as
// the FileChange tool_use's input, shared between item/started and
// item/completed (see the EventToolInputReplace re-emit above).
func codexFileChangeInputJSON(item codexThreadItem) string {
	changes := make([]codexFileChangeEntry, 0, len(item.Changes))
	for _, ch := range item.Changes {
		kind := ch.Kind()
		if kind == "add" {
			kind = "create"
		}
		if kind == "" {
			kind = "update"
		}
		// codex-rs reports Update diffs as bare "@@ ... @@" hunks with no file
		// header (similar::unified_diff() without .header()); wrap them in a
		// git-style header so the frontend's diff parser can use the exact
		// same code path as pi/claude/git diffs. Add/Delete instead carry the
		// full file content with no hunks at all, so it must be turned into an
		// add-/delete-only diff rather than passed through raw — raw file
		// content would make DiffPreview misclassify normal lines starting
		// with '+'/'-' as diff markers.
		// Only used to test "is there any data at all"; the untrimmed ch.Diff
		// is what actually goes into the diff below — trimming would silently
		// eat meaningful leading/trailing blank lines or whitespace-only
		// content in a create/delete's full file content.
		hasContent := strings.TrimSpace(ch.Diff) != ""
		var diff string
		switch kind {
		case "update":
			if !hasContent {
				// no-op
			} else if looksLikeUnifiedDiff(ch.Diff) {
				diff = ch.Diff // Already has headers (older/other codex-rs builds).
			} else {
				diff = unifiedDiffFromHunks(ch.Path, []string{ch.Diff})
			}
		case "create":
			diff = unifiedDiffForFullContent(ch.Path, ch.Diff, false)
		case "delete":
			diff = unifiedDiffForFullContent(ch.Path, ch.Diff, true)
		}
		changes = append(changes, codexFileChangeEntry{
			Path: ch.Path,
			Kind: kind,
			Diff: diff,
		})
	}
	changesJSON, _ := json.Marshal(map[string]interface{}{"changes": changes})
	return string(changesJSON)
}

func codexToolDisplayName(item codexThreadItem) string {
	if item.Tool == "" {
		return "Tool"
	}
	if item.Server != "" {
		return item.Server + "/" + item.Tool
	}
	return item.Tool
}

// codexMCPResultText extracts text blocks from an MCP tool result.
func codexMCPResultText(resultRaw json.RawMessage) string {
	if len(resultRaw) == 0 || string(resultRaw) == "null" {
		return ""
	}
	var result struct {
		Content []map[string]interface{} `json:"content"`
	}
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		return string(resultRaw)
	}
	var parts []string
	for _, c := range result.Content {
		t, _ := c["type"].(string)
		if t != "text" {
			continue
		}
		txt, _ := c["text"].(string)
		if txt != "" {
			parts = append(parts, txt)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return string(resultRaw)
}

func codexDynamicToolOutputText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return string(raw)
	}
	var parts []string
	for _, it := range items {
		if strings.EqualFold(it.Type, "inputText") ||
			strings.EqualFold(it.Type, "input_text") ||
			strings.EqualFold(it.Type, "output_text") ||
			strings.EqualFold(it.Type, "text") {
			if it.Text != "" {
				parts = append(parts, it.Text)
			}
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

func codexIsNonToolItem(itemType string) bool {
	switch itemType {
	case "", "userMessage", "agentMessage", "reasoning", "plan", "contextCompaction",
		"collabAgentToolCall", "collabToolCall", "subAgentActivity":
		return true
	default:
		return false
	}
}

func codexParentedCallback(parentToolID string, cb EventCallback) EventCallback {
	if parentToolID == "" {
		return cb
	}
	return func(event StreamEvent) {
		if event.Type == EventToolStart || event.Type == EventToolResult || event.Type == EventToolInputReplace {
			event.ParentToolID = parentToolID
		}
		cb(event)
	}
}

func codexCollabReceiverThreadIDs(item codexThreadItem) []string {
	ids := make([]string, 0, len(item.ReceiverThreadIDs)+2)
	seen := make(map[string]struct{}, len(item.ReceiverThreadIDs)+2)
	for _, id := range append(append([]string(nil), item.ReceiverThreadIDs...), item.ReceiverThreadID, item.NewThreadID) {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func codexIsSpawnAgentTool(tool string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", "/", "").Replace(strings.ToLower(strings.TrimSpace(tool)))
	return normalized == "spawnagent"
}

func codexSubAgentDescription(item codexThreadItem) string {
	if prompt := strings.TrimSpace(item.Prompt); prompt != "" {
		return prompt
	}
	return codexSubAgentName(item)
}

func codexSubAgentName(item codexThreadItem) string {
	agentPath := strings.Trim(strings.TrimSpace(item.AgentPath), "/")
	if agentPath == "" {
		return "Sub-agent"
	}
	if idx := strings.LastIndex(agentPath, "/"); idx >= 0 && idx+1 < len(agentPath) {
		return agentPath[idx+1:]
	}
	return agentPath
}

func codexIsIgnorableNotification(method string) bool {
	switch method {
	// Legacy/verbose codex/event mirrors of already-handled canonical notifications.
	case "codex/event/agent_message_delta",
		"codex/event/agent_message_content_delta",
		"codex/event/agent_message",
		"codex/event/item_started",
		"codex/event/item_completed",
		"codex/event/token_count",
		"codex/event/turn_diff",
		"turn/diff/updated",
		"codex/event/task_started",
		"codex/event/task_complete",
		"codex/event/user_message",
		"codex/event/mcp_startup_update",
		"codex/event/mcp_startup_complete",
		"thread/status/changed",
		"account/rateLimits/updated",
		"thread/compacted",
		"codex/event/context_compacted",
		"codex/event/mcp_tool_call_begin",
		"codex/event/mcp_tool_call_end",
		"codex/event/view_image_tool_call",
		"item/commandExecution/outputDelta",
		"codex/event/terminal_interaction",
		"item/commandExecution/terminalInteraction":
		return true
	default:
		return false
	}
}

func codexGenericToolName(item codexThreadItem) string {
	if item.Tool != "" {
		return codexToolDisplayName(item)
	}
	if item.Type != "" {
		return item.Type
	}
	return "Tool"
}

func codexGenericToolInput(item codexThreadItem) string {
	if len(item.Arguments) > 0 && string(item.Arguments) != "null" {
		return string(item.Arguments)
	}
	if item.Command != "" {
		cmdJSON, _ := json.Marshal(map[string]string{"command": item.Command})
		return string(cmdJSON)
	}
	if item.Query != "" {
		qJSON, _ := json.Marshal(map[string]string{"query": item.Query})
		return string(qJSON)
	}
	if item.Text != "" {
		txtJSON, _ := json.Marshal(map[string]string{"text": item.Text})
		return string(txtJSON)
	}
	return ""
}

func codexGenericToolOutput(item codexThreadItem) string {
	if item.AggregatedOutput != nil && *item.AggregatedOutput != "" {
		return *item.AggregatedOutput
	}
	if len(item.Result) > 0 && string(item.Result) != "null" {
		return codexMCPResultText(item.Result)
	}
	if len(item.ContentItems) > 0 && string(item.ContentItems) != "null" {
		return codexDynamicToolOutputText(item.ContentItems)
	}
	if item.Text != "" {
		return item.Text
	}
	if len(item.Error) > 0 && string(item.Error) != "null" {
		return string(item.Error)
	}
	return ""
}

// codexWebSearchToolStart picks a frontend tool name and input shape for a
// webSearch item. Codex 0.124.0 distinguishes "search" / "openPage" /
// "findInPage" / "other" via the `action` field; we map openPage to WebFetch
// (so the UI shows the URL hostname) and everything else to WebSearch.
func codexWebSearchToolStart(item codexThreadItem) (toolName, inputJSON string) {
	action, _ := parseRawJSONMap(item.Action)
	actionType := strings.ToLower(strings.TrimSpace(firstJSONMapString(action, "type")))

	switch actionType {
	case "openpage":
		url := firstJSONMapString(action, "url")
		if url == "" {
			url = strings.TrimSpace(item.Query)
		}
		b, _ := json.Marshal(map[string]string{"url": url})
		return "WebFetch", string(b)
	case "findinpage":
		pattern := firstJSONMapString(action, "pattern")
		url := firstJSONMapString(action, "url")
		query := strings.TrimSpace(item.Query)
		if query == "" && pattern != "" && url != "" {
			query = "'" + pattern + "' in " + url
		}
		if query == "" {
			query = pattern
		}
		b, _ := json.Marshal(map[string]string{"query": query})
		return "WebSearch", string(b)
	default:
		query := codexWebSearchQuery(item)
		b, _ := json.Marshal(map[string]string{"query": query})
		return "WebSearch", string(b)
	}
}

// codexWebSearchResultText renders a short summary of what the search returned,
// so the UI shows non-empty output when the dropdown is expanded.
func codexWebSearchResultText(item codexThreadItem) string {
	action, _ := parseRawJSONMap(item.Action)
	actionType := strings.ToLower(strings.TrimSpace(firstJSONMapString(action, "type")))

	switch actionType {
	case "search":
		if rawQueries, ok := action["queries"]; ok {
			if list, ok := rawQueries.([]interface{}); ok && len(list) > 0 {
				lines := make([]string, 0, len(list))
				for _, q := range list {
					if s := strings.TrimSpace(stringifyJSONValue(q)); s != "" {
						lines = append(lines, "- "+s)
					}
				}
				if len(lines) > 0 {
					return "Searched: " + strings.Join(lines, "\n")
				}
			}
		}
	case "openpage":
		if url := firstJSONMapString(action, "url"); url != "" {
			return "Opened " + url
		}
	case "findinpage":
		pattern := firstJSONMapString(action, "pattern")
		url := firstJSONMapString(action, "url")
		if pattern != "" && url != "" {
			return "Found '" + pattern + "' in " + url
		}
	}
	if q := strings.TrimSpace(item.Query); q != "" {
		return q
	}
	return ""
}

func codexWebSearchQuery(item codexThreadItem) string {
	query := strings.TrimSpace(item.Query)
	if query != "" {
		return query
	}
	if len(item.Arguments) > 0 && string(item.Arguments) != "null" {
		if args, ok := parseRawJSONMap(item.Arguments); ok {
			if q := firstJSONMapStringDeep(args,
				"query", "searchTerm", "search_term", "term", "q", "url", "text", "input",
			); strings.TrimSpace(q) != "" {
				return strings.TrimSpace(q)
			}
		}
	}
	if strings.TrimSpace(item.Text) != "" {
		return strings.TrimSpace(item.Text)
	}
	for _, c := range item.Content {
		if strings.TrimSpace(c) != "" {
			return strings.TrimSpace(c)
		}
	}
	return ""
}
