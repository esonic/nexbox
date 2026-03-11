package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func parseRawJSONMap(raw json.RawMessage) (map[string]interface{}, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false
	}
	return m, true
}

func hasJSONMapKey(m map[string]interface{}, key string) bool {
	_, ok := m[key]
	return ok
}

func hasJSONMapKeyDeep(m map[string]interface{}, keys ...string) bool {
	for _, key := range keys {
		if _, ok := findJSONValueDeep(m, key); ok {
			return true
		}
	}
	return false
}

func firstJSONMapString(m map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if s := stringifyJSONValue(v); strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

func firstJSONMapStringDeep(m map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if v, ok := findJSONValueDeep(m, key); ok {
			if s := stringifyJSONValue(v); strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

func firstJSONMapInt(m map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch vv := v.(type) {
			case float64:
				return int(vv)
			case int:
				return vv
			case int64:
				return int(vv)
			case json.Number:
				if n, err := vv.Int64(); err == nil {
					return int(n)
				}
			case string:
				if n, err := strconv.Atoi(strings.TrimSpace(vv)); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

func firstJSONMapIntDeep(m map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		if v, ok := findJSONValueDeep(m, key); ok {
			switch vv := v.(type) {
			case float64:
				return int(vv)
			case int:
				return vv
			case int64:
				return int(vv)
			case json.Number:
				if n, err := vv.Int64(); err == nil {
					return int(n)
				}
			case string:
				if n, err := strconv.Atoi(strings.TrimSpace(vv)); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

func firstJSONMapBool(m map[string]interface{}, keys ...string) bool {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch vv := v.(type) {
			case bool:
				return vv
			case string:
				if b, err := strconv.ParseBool(strings.TrimSpace(vv)); err == nil {
					return b
				}
			}
		}
	}
	return false
}

func firstJSONMapBoolDeep(m map[string]interface{}, keys ...string) bool {
	for _, key := range keys {
		if v, ok := findJSONValueDeep(m, key); ok {
			switch vv := v.(type) {
			case bool:
				return vv
			case string:
				if b, err := strconv.ParseBool(strings.TrimSpace(vv)); err == nil {
					return b
				}
			}
		}
	}
	return false
}

func findJSONValueDeep(v interface{}, key string) (interface{}, bool) {
	switch vv := v.(type) {
	case map[string]interface{}:
		if value, ok := vv[key]; ok {
			return value, true
		}
		for _, child := range vv {
			if value, ok := findJSONValueDeep(child, key); ok {
				return value, true
			}
		}
	case []interface{}:
		for _, child := range vv {
			if value, ok := findJSONValueDeep(child, key); ok {
				return value, true
			}
		}
	}
	return nil, false
}

func stringifyJSONValue(v interface{}) string {
	switch vv := v.(type) {
	case nil:
		return ""
	case string:
		return vv
	case fmt.Stringer:
		return vv.String()
	case []byte:
		return string(vv)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func codexPatchToolStart(payload map[string]interface{}, raw json.RawMessage) (toolName string, input string, path string) {
	patchText := firstJSONMapStringDeep(payload, "patch", "diff", "input", "patch_text", "unified_diff")
	if patchText == "" {
		patchText = string(raw)
	}

	kind := strings.ToLower(strings.TrimSpace(firstJSONMapStringDeep(payload, "kind", "change_kind", "op", "operation", "action")))
	path = strings.TrimSpace(firstJSONMapStringDeep(payload, "path", "file_path", "filepath", "file", "target"))
	derivedKind, derivedPath := extractPatchChangeFromText(patchText)
	if kind == "" {
		kind = derivedKind
	}
	if path == "" {
		path = derivedPath
	}

	switch kind {
	case "add", "create", "added", "created":
		toolName = "Write"
	case "delete", "remove", "deleted", "removed":
		toolName = "Delete"
	default:
		toolName = "Edit"
	}

	if path != "" {
		pathJSON, _ := json.Marshal(map[string]string{"path": path})
		return toolName, string(pathJSON), path
	}
	patchJSON, _ := json.Marshal(map[string]string{"patch": patchText})
	return toolName, string(patchJSON), ""
}

func codexPatchResultMessage(toolName, path string) string {
	switch toolName {
	case "Write":
		return fmt.Sprintf("Created %s", path)
	case "Delete":
		return fmt.Sprintf("Deleted %s", path)
	default:
		return fmt.Sprintf("Edited %s", path)
	}
}

func extractPatchChangeFromText(patchText string) (kind string, path string) {
	lines := strings.Split(patchText, "\n")
	for _, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "*** Add File:"):
			return "add", strings.TrimSpace(strings.TrimPrefix(t, "*** Add File:"))
		case strings.HasPrefix(t, "*** Update File:"):
			return "update", strings.TrimSpace(strings.TrimPrefix(t, "*** Update File:"))
		case strings.HasPrefix(t, "*** Delete File:"):
			return "delete", strings.TrimSpace(strings.TrimPrefix(t, "*** Delete File:"))
		}
	}

	plusPath := ""
	minusPath := ""
	for _, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "+++ "):
			p := parseDiffPathToken(strings.TrimSpace(strings.TrimPrefix(t, "+++ ")))
			if p != "" {
				plusPath = p
			}
		case strings.HasPrefix(t, "--- "):
			p := parseDiffPathToken(strings.TrimSpace(strings.TrimPrefix(t, "--- ")))
			if p != "" {
				minusPath = p
			}
		}
	}
	switch {
	case plusPath != "" && minusPath == "":
		return "add", plusPath
	case minusPath != "" && plusPath == "":
		return "delete", minusPath
	case plusPath != "":
		return "update", plusPath
	case minusPath != "":
		return "update", minusPath
	default:
		return "update", ""
	}
}

func parseDiffPathToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if idx := strings.IndexAny(token, " \t"); idx >= 0 {
		token = token[:idx]
	}
	token = strings.TrimPrefix(token, "a/")
	token = strings.TrimPrefix(token, "b/")
	if token == "/dev/null" {
		return ""
	}
	return token
}

func extractPatchPathFromResultText(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "M ") ||
			strings.HasPrefix(t, "A ") ||
			strings.HasPrefix(t, "D ") ||
			strings.HasPrefix(t, "R ") {
			return strings.TrimSpace(t[2:])
		}
		if strings.HasPrefix(t, "/") || strings.Contains(t, "/") {
			// Fallback for output lines that contain only a file path.
			return strings.TrimSpace(t)
		}
	}
	return ""
}

func normalizeExecCommandText(raw string) string {
	cmd := strings.TrimSpace(raw)
	if cmd == "" || !strings.HasPrefix(cmd, "[") {
		return cmd
	}
	var parts []string
	if err := json.Unmarshal([]byte(cmd), &parts); err != nil || len(parts) == 0 {
		return cmd
	}
	if len(parts) >= 3 && strings.HasSuffix(parts[0], "bash") && parts[1] == "-lc" {
		escaped := strings.ReplaceAll(parts[2], "'", `'"'"'`)
		return fmt.Sprintf("%s -lc '%s'", parts[0], escaped)
	}
	return strings.Join(parts, " ")
}
