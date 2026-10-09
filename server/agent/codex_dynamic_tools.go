package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type codexDynamicToolCall struct {
	ItemID    string
	ThreadID  string
	TurnID    string
	ToolName  string
	Arguments json.RawMessage
}

func parseCodexDynamicToolCall(params json.RawMessage) (codexDynamicToolCall, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(params, &raw); err != nil {
		return codexDynamicToolCall{}, fmt.Errorf("decode item/tool/call params: %w", err)
	}

	call := codexDynamicToolCall{
		ItemID:   firstJSONString(raw, "itemId", "item_id", "id", "toolCallId", "tool_call_id"),
		ThreadID: firstJSONString(raw, "threadId", "thread_id"),
		TurnID:   firstJSONString(raw, "turnId", "turn_id"),
		ToolName: firstJSONString(raw, "toolName", "tool_name", "tool", "name"),
	}

	call.Arguments = firstJSON(raw, "arguments", "args", "input")
	if len(call.Arguments) == 0 || string(call.Arguments) == "null" {
		// Fallback: some payloads inline args at top-level; strip envelope keys.
		inline := map[string]interface{}{}
		for k, v := range raw {
			switch k {
			case "itemId", "item_id", "id", "toolCallId", "tool_call_id", "threadId", "thread_id", "turnId", "turn_id", "toolName", "tool_name", "tool", "name", "arguments", "args", "input":
				continue
			default:
				var any interface{}
				if err := json.Unmarshal(v, &any); err == nil {
					inline[k] = any
				}
			}
		}
		if len(inline) > 0 {
			buf, _ := json.Marshal(inline)
			call.Arguments = buf
		}
	}
	if call.ToolName == "" {
		return codexDynamicToolCall{}, fmt.Errorf("missing tool name in item/tool/call")
	}
	return call, nil
}

func buildCodexDynamicToolResponse(success bool, output string) map[string]interface{} {
	return map[string]interface{}{
		"success": success,
		"contentItems": []map[string]interface{}{
			{
				"type": "inputText",
				"text": output,
			},
		},
	}
}

func runCodexDynamicToolCall(ctx context.Context, workDir, toolName string, argsRaw json.RawMessage) (string, error) {
	args, err := decodeCodexToolArgs(argsRaw)
	if err != nil {
		return "", err
	}
	switch normalizeCodexToolName(toolName) {
	case "read":
		return codexToolRead(workDir, args)
	case "edit", "strreplace":
		return codexToolEdit(workDir, args)
	case "write":
		return codexToolWrite(workDir, args)
	case "multiedit":
		return codexToolMultiEdit(workDir, args)
	case "delete":
		return codexToolDelete(workDir, args)
	case "ls":
		return codexToolLS(workDir, args)
	case "glob":
		return codexToolGlob(ctx, workDir, args)
	case "grep", "semanticsearch":
		return codexToolGrep(ctx, workDir, args)
	case "webfetch":
		return codexToolWebFetch(ctx, args)
	case "shell", "bash":
		return codexToolShell(ctx, workDir, args)
	default:
		return "", fmt.Errorf("unsupported dynamic tool: %s", toolName)
	}
}

func decodeCodexToolArgs(raw json.RawMessage) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}

	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj, nil
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		asString = strings.TrimSpace(asString)
		if asString == "" {
			return out, nil
		}
		if json.Unmarshal([]byte(asString), &obj) == nil {
			return obj, nil
		}
		out["input"] = asString
		return out, nil
	}

	return nil, fmt.Errorf("unsupported tool args format: %s", string(raw))
}

func normalizeCodexToolName(name string) string {
	name = strings.TrimSpace(name)
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "_", "")
	name = strings.ReplaceAll(name, "-", "")
	return name
}

func codexToolRead(workDir string, args map[string]interface{}) (string, error) {
	path := firstString(args, "path", "file_path", "filepath")
	abs, _, err := resolveToolPath(workDir, path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	content := string(data)

	startLine := intFromAny(args["start_line"])
	endLine := intFromAny(args["end_line"])
	if startLine > 0 || endLine > 0 {
		lines := strings.Split(content, "\n")
		if startLine <= 0 {
			startLine = 1
		}
		if endLine <= 0 || endLine > len(lines) {
			endLine = len(lines)
		}
		if startLine > len(lines) {
			return "", nil
		}
		if startLine > endLine {
			startLine = endLine
		}
		content = strings.Join(lines[startLine-1:endLine], "\n")
	}

	offset := intFromAny(args["offset"])
	limit := intFromAny(args["limit"])
	if offset > 0 || limit > 0 {
		if offset < 0 {
			offset = 0
		}
		if offset >= len(content) {
			content = ""
		} else {
			content = content[offset:]
			if limit > 0 && limit < len(content) {
				content = content[:limit]
			}
		}
	}

	return trimOutput(content), nil
}

func codexToolWrite(workDir string, args map[string]interface{}) (string, error) {
	path := firstString(args, "path", "file_path", "filepath")
	content := firstString(args, "content", "text", "input")
	appendMode := boolFromAny(args["append"])

	abs, display, err := resolveToolPath(workDir, path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		return "", err
	}
	flags := os.O_WRONLY | os.O_CREATE
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(abs, flags, 0644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.WriteString(f, content); err != nil {
		return "", err
	}
	if appendMode {
		return fmt.Sprintf("Appended %d bytes to %s", len(content), display), nil
	}
	return fmt.Sprintf("Wrote %d bytes to %s", len(content), display), nil
}

func codexToolEdit(workDir string, args map[string]interface{}) (string, error) {
	path := firstString(args, "path", "file_path", "filepath")
	oldText := firstString(args, "old_str", "oldText", "old")
	newText := firstString(args, "new_str", "newText", "new")
	replaceAll := boolFromAny(args["replace_all"])

	abs, display, err := resolveToolPath(workDir, path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	content := string(data)
	count := strings.Count(content, oldText)
	if oldText == "" {
		return "", fmt.Errorf("old text is empty")
	}
	if count == 0 {
		return "", fmt.Errorf("target text not found in %s", display)
	}
	if replaceAll {
		content = strings.ReplaceAll(content, oldText, newText)
	} else {
		content = strings.Replace(content, oldText, newText, 1)
		count = 1
	}
	if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("Edited %s (%d replacement%s)", display, count, plural(count)), nil
}

func codexToolMultiEdit(workDir string, args map[string]interface{}) (string, error) {
	path := firstString(args, "path", "file_path", "filepath")
	editsRaw, ok := args["edits"].([]interface{})
	if !ok || len(editsRaw) == 0 {
		return "", fmt.Errorf("edits is empty")
	}
	abs, display, err := resolveToolPath(workDir, path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	content := string(data)
	applied := 0
	for _, e := range editsRaw {
		edit, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		oldText := firstString(edit, "old_str", "oldText", "old")
		newText := firstString(edit, "new_str", "newText", "new")
		replaceAll := boolFromAny(edit["replace_all"])
		if oldText == "" {
			continue
		}
		if !strings.Contains(content, oldText) {
			continue
		}
		if replaceAll {
			content = strings.ReplaceAll(content, oldText, newText)
		} else {
			content = strings.Replace(content, oldText, newText, 1)
		}
		applied++
	}
	if applied == 0 {
		return "", fmt.Errorf("no edits were applied to %s", display)
	}
	if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("Applied %d edit%s to %s", applied, plural(applied), display), nil
}

func codexToolDelete(workDir string, args map[string]interface{}) (string, error) {
	path := firstString(args, "path", "file_path", "filepath")
	abs, display, err := resolveToolPath(workDir, path)
	if err != nil {
		return "", err
	}
	if err := os.RemoveAll(abs); err != nil {
		return "", err
	}
	return fmt.Sprintf("Deleted %s", display), nil
}

func codexToolLS(workDir string, args map[string]interface{}) (string, error) {
	path := firstString(args, "path")
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	abs, _, err := resolveToolPath(workDir, path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", err
	}
	limit := intFromAny(args["limit"])
	if limit <= 0 {
		limit = 500
	}
	lines := make([]string, 0, minInt(len(entries), limit))
	for i, e := range entries {
		if i >= limit {
			break
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}
	return strings.Join(lines, "\n"), nil
}

func codexToolGlob(ctx context.Context, workDir string, args map[string]interface{}) (string, error) {
	pattern := firstString(args, "pattern", "globPattern")
	if strings.TrimSpace(pattern) == "" {
		return "", fmt.Errorf("glob pattern is empty")
	}
	path := firstString(args, "path")
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	baseAbs, _, err := resolveToolPath(workDir, path)
	if err != nil {
		return "", err
	}
	limit := intFromAny(args["limit"])
	if limit <= 0 {
		limit = 200
	}

	if out, err := runCommand(ctx, baseAbs, "rg", "--files", "-g", pattern); err == nil {
		lines := splitNonEmptyLines(out)
		if len(lines) > limit {
			lines = lines[:limit]
		}
		return strings.Join(lines, "\n"), nil
	}

	matches, err := filepath.Glob(filepath.Join(baseAbs, pattern))
	if err != nil {
		return "", err
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	lines := make([]string, 0, len(matches))
	for _, m := range matches {
		if workDir != "" {
			if rel, err := filepath.Rel(workDir, m); err == nil {
				lines = append(lines, rel)
				continue
			}
		}
		lines = append(lines, m)
	}
	return strings.Join(lines, "\n"), nil
}

func codexToolGrep(ctx context.Context, workDir string, args map[string]interface{}) (string, error) {
	pattern := firstString(args, "pattern", "query", "searchTerm")
	if strings.TrimSpace(pattern) == "" {
		return "", fmt.Errorf("grep pattern is empty")
	}
	path := firstString(args, "path")
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	baseAbs, _, err := resolveToolPath(workDir, path)
	if err != nil {
		return "", err
	}
	cmdArgs := []string{"-n", "--no-heading", "--color", "never", pattern, "."}
	if boolFromAny(args["ignoreCase"]) {
		cmdArgs = append([]string{"-i"}, cmdArgs...)
	}
	out, err := runCommand(ctx, baseAbs, "rg", cmdArgs...)
	if err != nil {
		return "", err
	}
	lines := splitNonEmptyLines(out)
	limit := intFromAny(args["limit"])
	if limit <= 0 {
		limit = 200
	}
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return strings.Join(lines, "\n"), nil
}

func codexToolWebFetch(ctx context.Context, args map[string]interface{}) (string, error) {
	url := firstString(args, "url")
	if strings.TrimSpace(url) == "" {
		return "", fmt.Errorf("url is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 200*1024))
	if err != nil {
		return "", err
	}
	return trimOutput(string(body)), nil
}

func codexToolShell(ctx context.Context, workDir string, args map[string]interface{}) (string, error) {
	cmdText := firstString(args, "command", "cmd", "input")
	if strings.TrimSpace(cmdText) == "" {
		return "", fmt.Errorf("command is empty")
	}
	cmd := exec.CommandContext(ctx, "/bin/bash", "-lc", cmdText)
	if strings.TrimSpace(workDir) != "" {
		cmd.Dir = workDir
	}
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	combined := out.String()
	if stderr.Len() > 0 {
		if combined != "" && !strings.HasSuffix(combined, "\n") {
			combined += "\n"
		}
		combined += stderr.String()
	}
	combined = trimOutput(combined)
	if err != nil {
		return combined, fmt.Errorf("shell command failed: %w", err)
	}
	return combined, nil
}

func resolveToolPath(workDir, rawPath string) (string, string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", "", fmt.Errorf("path is empty")
	}
	var abs string
	if filepath.IsAbs(rawPath) {
		abs = filepath.Clean(rawPath)
	} else {
		if strings.TrimSpace(workDir) == "" {
			wd, err := os.Getwd()
			if err != nil {
				return "", "", err
			}
			workDir = wd
		}
		abs = filepath.Clean(filepath.Join(workDir, rawPath))
	}
	abs, err := filepath.Abs(abs)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(workDir) != "" {
		wdAbs, err := filepath.Abs(workDir)
		if err != nil {
			return "", "", err
		}
		if abs != wdAbs && !strings.HasPrefix(abs, wdAbs+string(os.PathSeparator)) {
			return "", "", fmt.Errorf("path escapes workdir: %s", rawPath)
		}
		if rel, err := filepath.Rel(wdAbs, abs); err == nil {
			return abs, rel, nil
		}
	}
	return abs, abs, nil
}

func runCommand(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s %v failed: %s", name, args, msg)
	}
	return out.String(), nil
}

func trimOutput(s string) string {
	const max = 200 * 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...[truncated]..."
}

func splitNonEmptyLines(s string) []string {
	raw := strings.Split(s, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func firstString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch vv := v.(type) {
			case string:
				if strings.TrimSpace(vv) != "" {
					return vv
				}
			case json.RawMessage:
				var s string
				if json.Unmarshal(vv, &s) == nil && strings.TrimSpace(s) != "" {
					return s
				}
			}
		}
	}
	return ""
}

func firstJSONString(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func firstJSON(m map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, k := range keys {
		if v, ok := m[k]; ok && len(v) > 0 && string(v) != "null" {
			return v
		}
	}
	return nil
}

func intFromAny(v interface{}) int {
	switch vv := v.(type) {
	case float64:
		return int(vv)
	case int:
		return vv
	case int64:
		return int(vv)
	case json.Number:
		n, _ := vv.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(vv))
		return n
	default:
		return 0
	}
}

func boolFromAny(v interface{}) bool {
	switch vv := v.(type) {
	case bool:
		return vv
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(vv))
		return b
	default:
		return false
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
