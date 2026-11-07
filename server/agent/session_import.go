package agent

import (
	"bufio"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/esonic/nexbox/store"
)

// ErrImportNotFound is returned when the requested native session no longer
// exists (or does not belong to the given working directory).
var ErrImportNotFound = errors.New("native session not found")

// ImportableSession is a native agent session discovered on disk. It is the
// cheap listing form: nothing beyond the first prompt is parsed.
type ImportableSession struct {
	ID        string    `json:"agent_session_id"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updated_at"`
	Size      int64     `json:"size"`
}

// ListOptions selects a page of importable sessions.
type ListOptions struct {
	// Before, when non-zero, keeps only sessions last modified strictly before
	// it (the UpdatedAt of the previous page's last item).
	Before time.Time
	// Limit is the page size; 0 means unlimited.
	Limit int
	// Skip drops sessions NexBox already has, before they are opened.
	Skip func(id string) bool
}

// listCandidate is a session file known only from a directory listing.
type listCandidate struct {
	path string
	id   string
	mod  time.Time
	size int64
}

// pageCandidates walks candidates newest-first and opens each only until the
// page is full. probe returns ok=false for files that are not importable
// conversations (helper calls, other projects, empty).
func pageCandidates(cands []listCandidate, opts ListOptions, probe func(c listCandidate) (title string, ok bool)) ([]ImportableSession, bool) {
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
	var out []ImportableSession
	for _, c := range cands {
		if !opts.Before.IsZero() && !c.mod.Before(opts.Before) {
			continue
		}
		if opts.Skip != nil && opts.Skip(c.id) {
			continue
		}
		title, ok := probe(c)
		if !ok {
			continue
		}
		out = append(out, ImportableSession{ID: c.id, Title: title, UpdatedAt: c.mod, Size: c.size})
		if opts.Limit > 0 && len(out) > opts.Limit {
			return out[:opts.Limit], true
		}
	}
	return out, false
}

// ImportedMessage is one NexBox chat message reconstructed from a native log.
type ImportedMessage struct {
	Role      string
	Parts     []store.MessagePart
	Metadata  store.MessageMetadata
	CreatedAt time.Time
}

// ImportedSession is a native session converted to NexBox's message shape.
type ImportedSession struct {
	Title    string
	Model    string
	Messages []ImportedMessage
}

// SessionImporter reads an agent CLI's own on-disk session store.
type SessionImporter interface {
	// ListSessions returns one page of the native sessions recorded for
	// workDir, newest first. Only the page (plus one probe) is opened, so cost
	// does not grow with the size of the CLI's history.
	ListSessions(workDir string, opts ListOptions) (page []ImportableSession, hasMore bool, err error)
	// LoadSession parses one native session into NexBox messages.
	LoadSession(workDir, id string) (*ImportedSession, error)
}

// SessionImporterFor returns the importer for a supported native CLI.
func SessionImporterFor(agentName string) SessionImporter {
	switch agentName {
	case "claude":
		return &ClaudeSessionImporter{}
	case "codex":
		return &CodexSessionImporter{}
	}
	return nil
}

// userMessageMarker separates NexBox's injected context header from the
// user's own text in prompts NexBox sent to the CLI.
const userMessageMarker = "[User Message]\n"

// stripPlatformContext returns the user's own text from a prompt NexBox wrapped
// with platform/project context. Prompts typed directly into a CLI have no
// marker and are returned unchanged.
func stripPlatformContext(text string) string {
	if i := strings.Index(text, userMessageMarker); i >= 0 &&
		(strings.HasPrefix(text, "[Platform Context]") || strings.HasPrefix(text, "[NexBox Platform Context]") || strings.HasPrefix(text, "[BeeHub Platform Context]") || strings.HasPrefix(text, "[Project:") || i == 0) {
		return strings.TrimSpace(text[i+len(userMessageMarker):])
	}
	return text
}

// isPlatformUtilityPrompt reports whether the raw first prompt is one of NexBox's
// own one-shot helper calls (title / commit-message generation). Those begin
// with the bare marker and no project header, and are never real chats.
func isPlatformUtilityPrompt(raw string) bool {
	return strings.HasPrefix(raw, userMessageMarker)
}

func listingTitle(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(t) > 80 {
		t = string([]rune(t)[:80])
	}
	return t
}

// forEachJSONLine streams a JSONL file, calling fn for each non-empty line.
// Lines may be arbitrarily long (tool outputs, encrypted reasoning blobs), so
// this avoids bufio.Scanner's token limit. fn returns false to stop early.
func forEachJSONLine(path string, fn func(line []byte) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 256*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if trimmed := strings.TrimSpace(string(line)); trimmed != "" {
				if !fn(line) {
					return nil
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// importTurn accumulates one assistant message while a log is replayed.
type importTurn struct {
	parts      []store.MessagePart
	meta       store.MessageMetadata
	start, end time.Time
}

func (t *importTurn) empty() bool { return len(t.parts) == 0 }

func (t *importTurn) appendText(text string) {
	if text == "" {
		return
	}
	if n := len(t.parts); n > 0 && t.parts[n-1].Type == "text" {
		t.parts[n-1].Content += text
		return
	}
	t.parts = append(t.parts, store.MessagePart{Type: "text", Content: text})
}

func (t *importTurn) appendThinking(text string) {
	if text == "" {
		return
	}
	if n := len(t.parts); n > 0 && t.parts[n-1].Type == "thinking" {
		t.parts[n-1].Content += text
		return
	}
	t.parts = append(t.parts, store.MessagePart{Type: "thinking", Content: text})
}

func (t *importTurn) addToolUse(id, name, input, parent string) {
	t.meta.ToolCalls++
	t.parts = append(t.parts, store.MessagePart{
		Type:         "tool_use",
		ToolCallID:   id,
		ToolName:     name,
		Input:        input,
		ParentToolID: parent,
		State:        "complete",
	})
}

func (t *importTurn) addToolResult(id, output, diff string, isError bool) {
	if isError {
		for i := range t.parts {
			if t.parts[i].Type == "tool_use" && t.parts[i].ToolCallID == id {
				t.parts[i].State = "error"
			}
		}
	}
	t.parts = append(t.parts, store.MessagePart{
		Type:       "tool_result",
		ToolCallID: id,
		Output:     output,
		Diff:       diff,
		IsError:    isError,
	})
}

func (t *importTurn) touch(ts time.Time) {
	if ts.IsZero() {
		return
	}
	if t.start.IsZero() {
		t.start = ts
	}
	t.end = ts
}

// flushTurn appends the accumulated assistant message (if any) to out.
func flushTurn(out *[]ImportedMessage, t *importTurn) {
	if t.empty() {
		*t = importTurn{}
		return
	}
	if !t.start.IsZero() && !t.end.IsZero() {
		t.meta.DurationMs = t.end.Sub(t.start).Milliseconds()
	}
	*out = append(*out, ImportedMessage{
		Role:      "assistant",
		Parts:     t.parts,
		Metadata:  t.meta,
		CreatedAt: t.start,
	})
	*t = importTurn{}
}
