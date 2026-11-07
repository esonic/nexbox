package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/esonic/nexbox/store"
)

// ClaudeSessionImporter reads Claude Code's own transcripts under
// $CLAUDE_CONFIG_DIR/projects/<encoded cwd>/<session-id>.jsonl.
type ClaudeSessionImporter struct{}

func claudeProjectsDir() string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return filepath.Join(dir, "projects")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// claudeEncodeProjectDir mirrors Claude Code's directory naming: every
// non-alphanumeric rune of the cwd becomes '-'.
func claudeEncodeProjectDir(workDir string) string {
	var b strings.Builder
	for _, r := range workDir {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

type claudeLogEntry struct {
	Type          string          `json:"type"`
	UUID          string          `json:"uuid"`
	Subtype       string          `json:"subtype"`
	IsSidechain   bool            `json:"isSidechain"`
	IsMeta        bool            `json:"isMeta"`
	IsCompact     bool            `json:"isCompactSummary"`
	Timestamp     string          `json:"timestamp"`
	CWD           string          `json:"cwd"`
	AITitle       string          `json:"aiTitle"`
	CustomTitle   string          `json:"customTitle"`
	ParentToolUse string          `json:"parentToolUseID"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	Message       struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   struct {
			InputTokens         int `json:"input_tokens"`
			OutputTokens        int `json:"output_tokens"`
			CacheReadTokens     int `json:"cache_read_input_tokens"`
			CacheCreationTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// userPrompt extracts the text of a real user prompt, or ok=false if this user
// entry is protocol noise (tool results, meta, command echoes, interrupts).
func (e *claudeLogEntry) userPrompt() (string, bool) {
	if e.IsMeta || e.IsCompact {
		return "", false
	}
	var text string
	var asStr string
	if json.Unmarshal(e.Message.Content, &asStr) == nil {
		text = asStr
	} else {
		var blocks []claudeBlock
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			return "", false
		}
		var parts []string
		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				return "", false
			case "text":
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, "\n")
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	for _, noise := range []string{"<task-notification", "<local-command", "<command-name>", "<command-message>", "Caveat: The messages below", "[Request interrupted by user"} {
		if strings.HasPrefix(trimmed, noise) {
			return "", false
		}
	}
	return text, true
}

func (*ClaudeSessionImporter) ListSessions(workDir string, opts ListOptions) ([]ImportableSession, bool, error) {
	base := claudeProjectsDir()
	if base == "" || workDir == "" {
		return nil, false, nil
	}
	dir := filepath.Join(base, claudeEncodeProjectDir(filepath.Clean(workDir)))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var cands []listCandidate
	for _, de := range entries {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".jsonl") || strings.HasPrefix(name, "agent-") {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		cands = append(cands, listCandidate{
			path: filepath.Join(dir, name),
			id:   strings.TrimSuffix(name, ".jsonl"),
			mod:  info.ModTime(),
			size: info.Size(),
		})
	}
	page, more := pageCandidates(cands, opts, func(c listCandidate) (string, bool) {
		prompt, cwd := claudeFirstPrompt(c.path)
		if prompt == "" || isPlatformUtilityPrompt(prompt) {
			return "", false
		}
		if cwd != "" && filepath.Clean(cwd) != filepath.Clean(workDir) {
			return "", false // directory name collision from the lossy encoding
		}
		return listingTitle(stripPlatformContext(prompt)), true
	})
	return page, more, nil
}

// claudeFirstPrompt reads just enough of a transcript to find its first real
// user prompt and the session cwd.
func claudeFirstPrompt(path string) (prompt, cwd string) {
	_ = forEachJSONLine(path, func(line []byte) bool {
		var e claudeLogEntry
		if json.Unmarshal(line, &e) != nil {
			return true
		}
		if e.CWD != "" && cwd == "" {
			cwd = e.CWD
		}
		if e.Type == "user" && !e.IsSidechain {
			if text, ok := e.userPrompt(); ok {
				prompt = text
				return false
			}
		}
		return true
	})
	return prompt, cwd
}

func (*ClaudeSessionImporter) LoadSession(workDir, id string) (*ImportedSession, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return nil, ErrImportNotFound
	}
	base := claudeProjectsDir()
	if base == "" {
		return nil, ErrImportNotFound
	}
	path := filepath.Join(base, claudeEncodeProjectDir(filepath.Clean(workDir)), id+".jsonl")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrImportNotFound
		}
		return nil, err
	}

	res := &ImportedSession{}
	var cur importTurn
	seen := map[string]bool{}
	// Usage repeats on every content-block line of one API message; keep the
	// last value per message id, then sum when the turn flushes.
	usage := map[string]claudeUsage{}
	var usageOrder []string
	var lastAssistantUUID, aiTitle, customTitle string

	flush := func() {
		var u claudeUsage
		for _, mid := range usageOrder {
			u.add(usage[mid])
		}
		cur.meta.InputTokens = u.input
		cur.meta.OutputTokens = u.output
		cur.meta.CacheReadInputTokens = u.cacheRead
		cur.meta.CacheCreationInputTokens = u.cacheCreate
		cur.meta.SessionID = id
		cur.meta.AgentCheckpoint = lastAssistantUUID
		flushTurn(&res.Messages, &cur)
		usage = map[string]claudeUsage{}
		usageOrder = nil
		lastAssistantUUID = ""
	}

	err := forEachJSONLine(path, func(line []byte) bool {
		var e claudeLogEntry
		if json.Unmarshal(line, &e) != nil {
			return true
		}
		switch e.Type {
		case "ai-title":
			aiTitle = e.AITitle
			return true
		case "custom-title":
			customTitle = e.CustomTitle
			return true
		case "user", "assistant":
		default:
			return true
		}
		if e.IsSidechain {
			return true
		}
		// A resumed/forked transcript can repeat history lines verbatim.
		if e.UUID != "" {
			if seen[e.UUID] {
				return true
			}
			seen[e.UUID] = true
		}
		ts := parseTimestamp(e.Timestamp)

		if e.Type == "user" {
			if text, ok := e.userPrompt(); ok {
				flush()
				res.Messages = append(res.Messages, ImportedMessage{
					Role:      "user",
					Parts:     []store.MessagePart{{Type: "text", Content: stripPlatformContext(text)}},
					CreatedAt: ts,
				})
				return true
			}
			var blocks []claudeBlock
			if json.Unmarshal(e.Message.Content, &blocks) != nil {
				return true
			}
			results := 0
			for _, b := range blocks {
				if b.Type == "tool_result" {
					results++
				}
			}
			for _, b := range blocks {
				if b.Type != "tool_result" {
					continue
				}
				var diff string
				if results == 1 {
					diff = claudeToolResultDiff(e.ToolUseResult)
				}
				isErr := b.IsError
				out := claudeResultText(b.Content)
				if isErr && isBenignToolError(out) {
					isErr = false
				}
				cur.addToolResult(b.ToolUseID, out, diff, isErr)
				cur.touch(ts)
			}
			return true
		}

		// assistant entry
		var blocks []claudeBlock
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			return true
		}
		cur.touch(ts)
		if e.UUID != "" {
			lastAssistantUUID = e.UUID
		}
		if mid := e.Message.ID; mid != "" {
			if _, ok := usage[mid]; !ok {
				usageOrder = append(usageOrder, mid)
			}
			usage[mid] = claudeUsage{
				input:       e.Message.Usage.InputTokens,
				output:      e.Message.Usage.OutputTokens,
				cacheRead:   e.Message.Usage.CacheReadTokens,
				cacheCreate: e.Message.Usage.CacheCreationTokens,
			}
		}
		if e.Message.Model != "" && e.Message.Model != "<synthetic>" {
			res.Model = e.Message.Model
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				cur.appendText(b.Text)
			case "thinking":
				cur.appendThinking(b.Thinking)
			case "tool_use":
				input := string(b.Input)
				if input == "" {
					input = "{}"
				}
				cur.addToolUse(b.ID, b.Name, input, "")
			}
		}
		return true
	})
	if err != nil {
		return nil, fmt.Errorf("read claude session: %w", err)
	}
	flush()
	if len(res.Messages) == 0 {
		return nil, ErrImportNotFound
	}

	switch {
	case customTitle != "":
		res.Title = customTitle
	case aiTitle != "":
		res.Title = aiTitle
	default:
		for _, m := range res.Messages {
			if m.Role == "user" && len(m.Parts) > 0 {
				res.Title = listingTitle(m.Parts[0].Content)
				break
			}
		}
	}
	res.Model = claudeModelAlias(res.Model)
	return res, nil
}

type claudeUsage struct{ input, output, cacheRead, cacheCreate int }

func (u *claudeUsage) add(o claudeUsage) {
	u.input += o.input
	u.output += o.output
	u.cacheRead += o.cacheRead
	u.cacheCreate += o.cacheCreate
}

// claudeResultText flattens a tool_result content (string or block list) the
// same way the live adapter does.
func claudeResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []map[string]any
	if json.Unmarshal(raw, &blocks) != nil {
		return string(raw)
	}
	var parts []string
	for _, b := range blocks {
		switch b["type"] {
		case "text":
			if t, ok := b["text"].(string); ok {
				parts = append(parts, t)
			}
		case "image":
			if src, ok := b["source"].(map[string]any); ok {
				mt, _ := src["media_type"].(string)
				parts = append(parts, fmt.Sprintf("[Image: %s]", mt))
			} else {
				parts = append(parts, "[Image]")
			}
		default:
			if t, ok := b["text"].(string); ok {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// claudeModelAlias maps a concrete model id to the alias the direct `claude`
// agent's picker uses; unknown models yield "" so the UI picks a default.
func claudeModelAlias(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		return "opus"
	case strings.Contains(m, "sonnet"):
		return "sonnet"
	case strings.Contains(m, "haiku"):
		return "haiku"
	}
	return ""
}
