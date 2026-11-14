package api

import (
	"encoding/json"
	"strings"
)

// compactChatMessage returns the content visible outside the work block.
// Full parts remain in the message table and are fetched on expansion.
func compactChatMessage(m ChatMessage) ChatMessage {
	_, count := fileChangeParts(m.Parts)
	m.FileChangeCount = count
	original := dropSubagentTools(m.Parts)
	// Match the renderer: nest sub-agent calls, then merge adjacent text before
	// stripping next actions. Otherwise whitespace at part boundaries is lost.
	var merged []MessagePart
	for _, p := range original {
		if p.Type == "text" && len(merged) > 0 && merged[len(merged)-1].Type == "text" {
			merged[len(merged)-1].Content += p.Content
		} else {
			merged = append(merged, p)
		}
	}
	cleaned := stripNextActionsAcrossParts(merged)
	last := -1
	for i := len(cleaned) - 1; i >= 0; i-- {
		switch cleaned[i].Type {
		case "text", "task_ref", "asset_ref", "workflow_ref":
			last = i
		}
		if last >= 0 {
			break
		}
	}
	start := last
	if last >= 0 && cleaned[last].Type == "text" {
		for start > 0 && cleaned[start-1].Type == "text" {
			start--
		}
	}
	if start < 0 {
		start = len(cleaned)
	}
	hasWork := false
	for _, p := range cleaned[:start] {
		if p.Type == "tool_use" || p.Type == "tool_result" || p.Type == "tool_group" || p.Type == "thinking" {
			hasWork = true
			break
		}
	}
	if !hasWork {
		m.Parts, m.LazyFiles = compactFileDetails(m.Parts)
		return m
	}
	m.LazyWork = true
	// A marker makes the message visible to the existing chat renderer even
	// when the turn contains work but no final answer.
	result := []MessagePart{{Type: "tool_group", Count: 1}}
	// Restore nested calls for Agent cards that remain outside the work block.
	nested := map[string][]MessagePart{}
	nestedIDs := map[string]string{}
	topIDs := map[string]bool{}
	for _, p := range m.Parts {
		if p.Type == "tool_use" {
			if p.ParentToolID != "" && topIDs[p.ParentToolID] {
				nestedIDs[p.ToolCallID] = p.ParentToolID
				nested[p.ParentToolID] = append(nested[p.ParentToolID], p)
			} else {
				topIDs[p.ToolCallID] = true
			}
		} else if p.Type == "tool_result" {
			if parent := nestedIDs[p.ToolCallID]; parent != "" {
				nested[parent] = append(nested[parent], p)
			}
		}
	}
	for _, p := range cleaned[start:] {
		result = append(result, p)
		if p.Type == "tool_use" {
			result = append(result, nested[p.ToolCallID]...)
		}
	}
	// Preserve the last suggestion block, including when its tags span parts.
	var all strings.Builder
	for _, p := range original {
		if p.Type == "text" {
			all.WriteString(p.Content)
		}
	}
	text := all.String()
	ranges := nextActionsRanges(text)
	if len(ranges) > 0 {
		block := text[ranges[len(ranges)-1].start:ranges[len(ranges)-1].end]
		added := false
		for i := len(result) - 1; i >= 1; i-- {
			if result[i].Type == "text" {
				result[i].Content += block
				added = true
				break
			}
		}
		if !added {
			result = append(result, MessagePart{Type: "text", Content: block})
		}
	}
	m.Parts, m.LazyFiles = compactFileDetails(result)
	return m
}

// Retain the file tool's visible identity while deferring its input and diff.
// Clicking the tool itself can still retrieve the complete message.
func compactFileDetails(parts []MessagePart) ([]MessagePart, bool) {
	fileIDs := map[string]bool{}
	for _, p := range parts {
		name := strings.ToLower(p.ToolName)
		if p.Type == "tool_use" && (p.ToolName == "FileChange" || name == "edit" || name == "write" || name == "multiedit" || name == "strreplace" || name == "delete") {
			fileIDs[p.ToolCallID] = true
		}
	}
	if len(fileIDs) == 0 {
		return parts, false
	}
	out := make([]MessagePart, len(parts))
	for i, p := range parts {
		if fileIDs[p.ToolCallID] {
			if p.Type == "tool_use" {
				p.LazyDetails = true
				var raw []byte
				if str, ok := p.Input.(string); ok {
					raw = []byte(str)
				} else {
					raw, _ = json.Marshal(p.Input)
				}
				var data map[string]json.RawMessage
				_ = json.Unmarshal(raw, &data)
				if p.ToolName == "FileChange" {
					var changes []struct {
						Path string `json:"path"`
						Kind string `json:"kind"`
					}
					_ = json.Unmarshal(data["changes"], &changes)
					input, _ := json.Marshal(map[string]interface{}{"changes": changes})
					p.Input = string(input)
				} else {
					var path string
					_ = json.Unmarshal(data["file_path"], &path)
					if path == "" {
						_ = json.Unmarshal(data["path"], &path)
					}
					input, _ := json.Marshal(map[string]string{"path": path})
					p.Input = string(input)
				}
			} else if p.Type == "tool_result" {
				p.Output = ""
				p.Diff = ""
			}
		}
		out[i] = p
	}
	return out, true
}

// fileChangeParts returns only file-writing calls and their results. The count
// matches the renderer's unique-path summary without sending the diffs.
func fileChangeParts(parts []MessagePart) ([]MessagePart, int) {
	results := map[string]MessagePart{}
	for _, p := range parts {
		if p.Type == "tool_result" {
			results[p.ToolCallID] = p
		}
	}
	paths := map[string]bool{}
	var filtered []MessagePart
	for _, p := range parts {
		if p.Type != "tool_use" {
			continue
		}
		name := strings.ToLower(p.ToolName)
		if p.ToolName != "FileChange" && name != "edit" && name != "write" && name != "multiedit" && name != "strreplace" && name != "delete" {
			continue
		}
		if result, ok := results[p.ToolCallID]; ok && result.IsError {
			continue
		}
		var raw []byte
		if str, ok := p.Input.(string); ok {
			raw = []byte(str)
		} else {
			raw, _ = json.Marshal(p.Input)
		}
		var data map[string]json.RawMessage
		if json.Unmarshal(raw, &data) != nil {
			continue
		}
		if p.ToolName == "FileChange" {
			var changes []struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(data["changes"], &changes)
			for _, c := range changes {
				if c.Path != "" {
					paths[c.Path] = true
				}
			}
			input, _ := json.Marshal(map[string]json.RawMessage{"changes": data["changes"]})
			p.Input = string(input)
		} else {
			var path string
			_ = json.Unmarshal(data["file_path"], &path)
			if path == "" {
				_ = json.Unmarshal(data["path"], &path)
			}
			if path != "" {
				paths[path] = true
			}
			input, _ := json.Marshal(map[string]string{"path": path})
			p.Input = string(input)
		}
		filtered = append(filtered, MessagePart{Type: "tool_use", ToolCallID: p.ToolCallID, ToolName: p.ToolName, Input: p.Input})
		if result, ok := results[p.ToolCallID]; ok {
			filtered = append(filtered, MessagePart{Type: "tool_result", ToolCallID: result.ToolCallID, Diff: result.Diff})
		}
	}
	return filtered, len(paths)
}
