package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/esonic/nexbox/store"
)

// CodexSessionImporter reads Codex's rollout logs under
// $CODEX_HOME/sessions/YYYY/MM/DD/rollout-*-<thread-id>.jsonl.
type CodexSessionImporter struct{}

func codexSessionsDir() string {
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return filepath.Join(dir, "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

type codexLogLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type string `json:"type"`

	// session_meta
	ID     string          `json:"id"`
	CWD    string          `json:"cwd"`
	Source json.RawMessage `json:"source"`

	// task_started / task_complete / turn_context
	TurnID string `json:"turn_id"`
	Model  string `json:"model"`

	// user_message
	Message string `json:"message"`

	// response_item message / reasoning
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`

	// function_call / custom_tool_call (+ outputs)
	Name      string          `json:"name"`
	Namespace string          `json:"namespace"`
	Arguments string          `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	CallID    string          `json:"call_id"`
	Output    json.RawMessage `json:"output"`

	// web_search_call
	Action json.RawMessage `json:"action"`

	// patch_apply_end
	Changes map[string]struct {
		Type        string `json:"type"`
		UnifiedDiff string `json:"unified_diff"`
		Content     string `json:"content"`
	} `json:"changes"`
	Success *bool `json:"success"`

	// token_count
	Info *struct {
		Last struct {
			Input  int `json:"input_tokens"`
			Cached int `json:"cached_input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"last_token_usage"`
	} `json:"info"`

	// item_completed
	Item *struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"item"`

	// task_complete
	DurationMs int64 `json:"duration_ms"`
}

// codexSessionMeta reads the header line of a rollout.
func codexSessionMeta(path string) (id, cwd string, isSubagent bool, ok bool) {
	_ = forEachJSONLine(path, func(line []byte) bool {
		var l codexLogLine
		if json.Unmarshal(line, &l) != nil || l.Type != "session_meta" {
			return false
		}
		var p codexPayload
		if json.Unmarshal(l.Payload, &p) != nil {
			return false
		}
		id, cwd, ok = p.ID, p.CWD, true
		// Sub-agent threads carry an object source; they are internal to their
		// parent session rather than standalone conversations.
		isSubagent = len(p.Source) > 0 && p.Source[0] == '{'
		return false
	})
	return
}

// codexRolloutFiles lists rollout files with their stat info (no content reads).
func codexRolloutFiles() []listCandidate {
	base := codexSessionsDir()
	if base == "" {
		return nil
	}
	var files []listCandidate
	_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		stem := strings.TrimSuffix(d.Name(), ".jsonl")
		// rollout-<timestamp>-<36-char thread id>
		id := ""
		if len(stem) > 36 {
			id = stem[len(stem)-36:]
		}
		files = append(files, listCandidate{path: path, id: id, mod: info.ModTime(), size: info.Size()})
		return nil
	})
	return files
}

// codexFirstPrompt returns the first user prompt of a rollout, from either the
// user_message event or (newer rollouts) the UserMessage item.
func codexFirstPrompt(path string) (prompt string) {
	_ = forEachJSONLine(path, func(line []byte) bool {
		var l codexLogLine
		if json.Unmarshal(line, &l) != nil || l.Type != "event_msg" {
			return true
		}
		var p codexPayload
		if json.Unmarshal(l.Payload, &p) != nil {
			return true
		}
		switch {
		case p.Type == "user_message" && strings.TrimSpace(p.Message) != "":
			prompt = p.Message
		case p.Type == "item_completed" && p.Item != nil && p.Item.Type == "UserMessage":
			var texts []string
			for _, c := range p.Item.Content {
				if c.Type == "text" {
					texts = append(texts, c.Text)
				}
			}
			prompt = strings.Join(texts, "\n")
		}
		return strings.TrimSpace(prompt) == ""
	})
	return prompt
}

func (*CodexSessionImporter) ListSessions(workDir string, opts ListOptions) ([]ImportableSession, bool, error) {
	if workDir == "" {
		return nil, false, nil
	}
	want := filepath.Clean(workDir)
	page, more := pageCandidates(codexRolloutFiles(), opts, func(c listCandidate) (string, bool) {
		id, cwd, isSub, ok := codexSessionMeta(c.path)
		if !ok || id == "" || isSub || filepath.Clean(cwd) != want {
			return "", false
		}
		prompt := codexFirstPrompt(c.path)
		if strings.TrimSpace(prompt) == "" || isPlatformUtilityPrompt(prompt) {
			return "", false
		}
		return listingTitle(stripPlatformContext(prompt)), true
	})
	return page, more, nil
}

// codexSkippedTools are internal collaboration/bookkeeping calls the live
// adapter also hides (or that patch_apply_end renders better).
var codexSkippedTools = map[string]bool{
	"wait": true, "wait_agent": true, "spawn_agent": true, "send_message": true,
	"list_agents": true, "close_agent": true, "update_plan": true, "apply_patch": true,
}

var codexExecCommandRe = regexp.MustCompile(`^\s*(?:const\s+\w+\s*=\s*)?(?:await\s+)?tools\.exec_command\((\{.*\})\)\s*;`)

// codexToolCall maps a rollout function/custom tool call to NexBox's tool card
// vocabulary. ok=false means the call is not worth showing.
func codexToolCall(p codexPayload) (name, input string, ok bool) {
	if codexSkippedTools[p.Name] {
		return "", "", false
	}
	args := p.Arguments
	if args == "" && len(p.Input) > 0 {
		var s string
		if json.Unmarshal(p.Input, &s) == nil {
			args = s
		} else {
			args = string(p.Input)
		}
	}
	switch {
	case p.Name == "exec_command":
		var a struct {
			Cmd string `json:"cmd"`
		}
		if json.Unmarshal([]byte(args), &a) == nil && a.Cmd != "" {
			b, _ := json.Marshal(map[string]string{"command": a.Cmd})
			return "Bash", string(b), true
		}
	case p.Name == "exec" && p.Arguments == "":
		// Code-mode wrapper: a single tools.exec_command({...}) call is just a shell command.
		if m := codexExecCommandRe.FindStringSubmatch(args); m != nil {
			var a struct {
				Cmd string `json:"cmd"`
			}
			if json.Unmarshal([]byte(m[1]), &a) == nil && a.Cmd != "" {
				b, _ := json.Marshal(map[string]string{"command": a.Cmd})
				return "Bash", string(b), true
			}
		}
		b, _ := json.Marshal(map[string]string{"code": args})
		return "exec", string(b), true
	}
	name = p.Name
	if strings.HasPrefix(p.Namespace, "mcp__") {
		name = strings.TrimPrefix(p.Namespace, "mcp__") + "/" + p.Name
	}
	if args == "" {
		args = "{}"
	}
	return name, args, true
}

func codexOutputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Output string `json:"output"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Output != "" {
		return obj.Output
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

func codexFileChangeParts(callID string, p codexPayload) (input, diffs string) {
	type entry struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
		Diff string `json:"diff"`
	}
	paths := make([]string, 0, len(p.Changes))
	for path := range p.Changes {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	entries := make([]entry, 0, len(paths))
	for _, path := range paths {
		ch := p.Changes[path]
		kind := ch.Type
		var diff string
		switch kind {
		case "add":
			kind = "create"
			diff = unifiedDiffForFullContent(path, ch.Content, false)
		case "delete":
			diff = unifiedDiffForFullContent(path, ch.Content, true)
		default:
			kind = "update"
			if strings.TrimSpace(ch.UnifiedDiff) != "" {
				if looksLikeUnifiedDiff(ch.UnifiedDiff) {
					diff = ch.UnifiedDiff
				} else {
					diff = unifiedDiffFromHunks(path, []string{ch.UnifiedDiff})
				}
			}
		}
		entries = append(entries, entry{Path: path, Kind: kind, Diff: diff})
	}
	b, _ := json.Marshal(map[string]any{"changes": entries})
	if len(entries) == 1 {
		diffs = entries[0].Diff
	}
	return string(b), diffs
}

func (*CodexSessionImporter) LoadSession(workDir, id string) (*ImportedSession, error) {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return nil, ErrImportNotFound
	}
	var path string
	for _, f := range codexRolloutFiles() {
		if f.id == id {
			path = f.path
			break
		}
	}
	if path == "" {
		return nil, ErrImportNotFound
	}
	if _, cwd, _, ok := codexSessionMeta(path); !ok || filepath.Clean(cwd) != filepath.Clean(workDir) {
		return nil, ErrImportNotFound
	}

	res := &ImportedSession{}
	var cur importTurn
	var turnID string
	var completed bool
	// token_count carries cumulative totals; a turn's usage is the delta.
	var lastIn, lastCached, lastOut int
	var lastUserText string
	skipped := map[string]bool{} // call_ids whose outputs we drop

	flush := func() {
		cur.meta.SessionID = id
		if completed {
			cur.meta.AgentCheckpoint = turnID
		}
		// Match the live adapter: usage of the turn's last model call.
		cur.meta.InputTokens = lastIn
		cur.meta.CacheReadInputTokens = lastCached
		cur.meta.OutputTokens = lastOut
		flushTurn(&res.Messages, &cur)
		completed = false
	}

	// Some rollouts carry both a user_message event and a UserMessage item for
	// the same prompt; drop the second when nothing happened in between.
	addUser := func(raw string, ts time.Time) {
		text := strings.TrimSpace(raw)
		if text == "" || (text == lastUserText && cur.empty()) {
			return
		}
		flush()
		lastUserText = text
		res.Messages = append(res.Messages, ImportedMessage{
			Role:      "user",
			Parts:     []store.MessagePart{{Type: "text", Content: stripPlatformContext(text)}},
			CreatedAt: ts,
		})
	}

	err := forEachJSONLine(path, func(line []byte) bool {
		var l codexLogLine
		if json.Unmarshal(line, &l) != nil {
			return true
		}
		var p codexPayload
		if json.Unmarshal(l.Payload, &p) != nil {
			return true
		}
		ts := parseTimestamp(l.Timestamp)

		switch l.Type {
		case "turn_context":
			if p.Model != "" {
				res.Model = p.Model
			}
		case "event_msg":
			switch p.Type {
			case "task_started":
				flush()
				turnID = p.TurnID
				cur.touch(ts)
			case "user_message":
				addUser(p.Message, ts)
			case "item_completed":
				// Newer rollouts record the prompt only as a UserMessage item.
				if p.Item != nil && p.Item.Type == "UserMessage" {
					var texts []string
					for _, c := range p.Item.Content {
						if c.Type == "text" {
							texts = append(texts, c.Text)
						}
					}
					addUser(strings.Join(texts, "\n"), ts)
				}
			case "token_count":
				if p.Info != nil {
					lastIn, lastCached, lastOut = p.Info.Last.Input, p.Info.Last.Cached, p.Info.Last.Output
				}
			case "task_complete":
				cur.touch(ts)
				completed = true
				if p.TurnID != "" {
					turnID = p.TurnID
				}
				flush()
			case "turn_aborted":
				cur.touch(ts)
				flush()
			case "patch_apply_end":
				input, diff := codexFileChangeParts(p.CallID, p)
				cur.touch(ts)
				cur.addToolUse(p.CallID, "FileChange", input, "")
				cur.addToolResult(p.CallID, "", diff, p.Success != nil && !*p.Success)
			}
		case "response_item":
			cur.touch(ts)
			switch p.Type {
			case "message":
				if p.Role != "assistant" {
					return true
				}
				for _, c := range p.Content {
					if c.Type == "output_text" {
						cur.appendText(c.Text)
					}
				}
			case "reasoning":
				for _, s := range p.Summary {
					cur.appendThinking(s.Text)
				}
				for _, c := range p.Content {
					if c.Type == "reasoning_text" {
						cur.appendThinking(c.Text)
					}
				}
			case "function_call", "custom_tool_call":
				name, input, ok := codexToolCall(p)
				if !ok {
					skipped[p.CallID] = true
					return true
				}
				cur.addToolUse(p.CallID, name, input, "")
			case "function_call_output", "custom_tool_call_output":
				if skipped[p.CallID] {
					return true
				}
				cur.addToolResult(p.CallID, codexOutputText(p.Output), "", false)
			case "web_search_call":
				var a struct {
					Type  string `json:"type"`
					Query string `json:"query"`
					URL   string `json:"url"`
				}
				_ = json.Unmarshal(p.Action, &a)
				if a.Type == "open_page" || a.Type == "openPage" {
					b, _ := json.Marshal(map[string]string{"url": a.URL})
					cur.addToolUse(p.ID, "WebFetch", string(b), "")
				} else {
					b, _ := json.Marshal(map[string]string{"query": a.Query})
					cur.addToolUse(p.ID, "WebSearch", string(b), "")
				}
			}
		}
		return true
	})
	if err != nil {
		return nil, fmt.Errorf("read codex session: %w", err)
	}
	flush()
	if len(res.Messages) == 0 {
		return nil, ErrImportNotFound
	}
	for _, m := range res.Messages {
		if m.Role == "user" && len(m.Parts) > 0 {
			res.Title = listingTitle(m.Parts[0].Content)
			break
		}
	}
	return res, nil
}
