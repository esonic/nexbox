package api

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// Session relay — deliver a message from one session's agent into another
// session and block until that session's agent has answered.
//
// The message lands in the target session exactly as if its user had typed it:
// same persisted user message, same live stream to every connected browser. The
// caller gets back the *final visible answer* — the text that stays on screen
// once the collapsible "Worked for …" block is folded away.

const (
	// relayIdlePoll is how often a pending relay re-checks whether the target
	// session has gone idle.
	relayIdlePoll = 300 * time.Millisecond
	// relayErrorGrace is how long we keep waiting for `done` after the target
	// session emitted an `error`. A turn routinely reports an agent error and
	// then finishes normally (the error text is appended to the reply), so an
	// error alone is not proof the turn is over.
	relayErrorGrace = 30 * time.Second
	// relayLivenessPoll / relayLivenessStrikes form the safety net for a turn
	// that ends without emitting either `done` or `error`. `done` is emitted
	// immediately after the active flag is cleared, so a few consecutive
	// "inactive and still nothing" observations mean the turn really is gone.
	relayLivenessPoll    = 2 * time.Second
	relayLivenessStrikes = 3
)

// ---------------------------------------------------------------------------
// Relay bookkeeping — one in-flight relay per target, no cycles
// ---------------------------------------------------------------------------

var (
	relayMu sync.Mutex
	// relayWaits maps a waiting session to the sessions it is blocked on — a
	// session can have several relays in flight, since agents call tools in
	// parallel. Used to reject A→B→A style loops, which would deadlock both.
	relayWaits = make(map[string][]string)
	// relaySlots serialises concurrent relays aimed at the same target so two
	// senders can't have their replies crossed.
	relaySlots = make(map[string]chan struct{})
)

// registerRelayWait records that `from` is about to block on `target`, refusing
// the relay if that would close a wait cycle.
func registerRelayWait(from, target string) error {
	relayMu.Lock()
	defer relayMu.Unlock()

	// Walk who-waits-on-whom outward from the target. Reaching `from` means it
	// is already (transitively) blocked on us.
	seen := map[string]bool{}
	var blocksOnCaller func(at string) bool
	blocksOnCaller = func(at string) bool {
		if at == from {
			return true
		}
		if seen[at] {
			return false
		}
		seen[at] = true
		for _, next := range relayWaits[at] {
			if blocksOnCaller(next) {
				return true
			}
		}
		return false
	}
	if blocksOnCaller(target) {
		return fmt.Errorf("refusing to relay: session %s is already waiting on %s, which would deadlock both sessions", target, from)
	}

	relayWaits[from] = append(relayWaits[from], target)
	return nil
}

// unregisterRelayWait drops one from→target wait, leaving that session's other
// in-flight relays registered.
func unregisterRelayWait(from, target string) {
	relayMu.Lock()
	defer relayMu.Unlock()

	targets := relayWaits[from]
	for i, t := range targets {
		if t == target {
			targets = append(targets[:i], targets[i+1:]...)
			break
		}
	}
	if len(targets) == 0 {
		delete(relayWaits, from)
		return
	}
	relayWaits[from] = targets
}

// acquireRelaySlot takes the target's single relay slot, blocking until it is
// free or ctx is done. The returned function releases it.
func acquireRelaySlot(ctx context.Context, target string) (func(), error) {
	relayMu.Lock()
	slot, ok := relaySlots[target]
	if !ok {
		slot = make(chan struct{}, 1)
		relaySlots[target] = slot
	}
	relayMu.Unlock()

	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ---------------------------------------------------------------------------
// Reply extraction
// ---------------------------------------------------------------------------

const (
	nextActionsOpen  = "<next-actions>"
	nextActionsClose = "</next-actions>"
)

// byteRange is a half-open [start, end) span of a string.
type byteRange struct{ start, end int }

func inRanges(ranges []byteRange, offset int) bool {
	for _, r := range ranges {
		if offset >= r.start && offset < r.end {
			return true
		}
	}
	return false
}

// nextActionsRanges locates the genuine <next-actions> blocks in text, each
// span including the blank lines before its opening tag and running to the
// closing tag — or to the end of the text for a block still being streamed.
//
// A tag inside markdown code (a fenced block or an inline span) is the model
// QUOTING the tag rather than emitting a block, and must be skipped: pairing a
// quoted opening tag with a real closing tag further down swallows everything
// in between. Mirrors nextActionsBlocks() in ChatMessage.tsx.
func nextActionsRanges(text string) []byteRange {
	lower := strings.ToLower(text)
	if !strings.Contains(lower, nextActionsOpen) {
		return nil
	}
	code := codeRegions(text)

	var out []byteRange
	for at := 0; at < len(lower); {
		rel := strings.Index(lower[at:], nextActionsOpen)
		if rel < 0 {
			break
		}
		tagAt := at + rel
		if inRanges(code, tagAt) {
			at = tagAt + len(nextActionsOpen)
			continue
		}

		end := len(text)
		body := tagAt + len(nextActionsOpen)
		if rel := strings.Index(lower[body:], nextActionsClose); rel >= 0 {
			end = body + rel + len(nextActionsClose)
		}
		start := tagAt
		for start > 0 && text[start-1] == '\n' {
			start--
		}

		out = append(out, byteRange{start, end})
		at = end
	}
	return out
}

// codeRegions returns the spans of markdown code in text: fenced blocks first,
// then inline spans outside them.
func codeRegions(text string) []byteRange {
	var regions []byteRange

	// Fenced blocks: a ``` / ~~~ line opens, the next fence line using the same
	// character (and at least as long) closes. An unclosed fence runs to the end.
	openStart, openMark := -1, ""
	for _, line := range textLines(text) {
		mark := fenceMarker(text[line.start:line.end])
		if mark == "" {
			continue
		}
		if openStart < 0 {
			openStart, openMark = line.start, mark
			continue
		}
		if mark[0] == openMark[0] && len(mark) >= len(openMark) {
			regions = append(regions, byteRange{openStart, line.end})
			openStart, openMark = -1, ""
		}
	}
	if openStart >= 0 {
		regions = append(regions, byteRange{openStart, len(text)})
	}

	// Inline spans outside those fences. A span may wrap lines but not cross a
	// blank line, so one stray backtick can't mask the rest of the message.
	fenced := regions
	for i := 0; i < len(text); {
		if text[i] != '`' || inRanges(fenced, i) {
			i++
			continue
		}
		run := 1
		for i+run < len(text) && text[i+run] == '`' {
			run++
		}
		if end := closingBackticks(text, i+run, run); end > 0 {
			regions = append(regions, byteRange{i, end})
			i = end
			continue
		}
		i += run
	}
	return regions
}

// closingBackticks finds the end offset of a run of exactly n backticks at or
// after `from`, giving up at a blank line. Returns -1 when the span never
// closes — an unterminated span is literal text, not code.
func closingBackticks(text string, from, n int) int {
	for i := from; i < len(text); {
		switch text[i] {
		case '\n':
			j := i + 1
			for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
				j++
			}
			if j >= len(text) || text[j] == '\n' {
				return -1
			}
			i++
		case '`':
			run := 1
			for i+run < len(text) && text[i+run] == '`' {
				run++
			}
			if run == n {
				return i + run
			}
			i += run
		default:
			i++
		}
	}
	return -1
}

// fenceMarker returns the fence characters when line opens or closes a fenced
// code block (``` or ~~~, up to three leading spaces), else "".
func fenceMarker(line string) string {
	i := 0
	for i < 3 && i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i >= len(line) || (line[i] != '`' && line[i] != '~') {
		return ""
	}
	c := line[i]
	run := 0
	for i+run < len(line) && line[i+run] == c {
		run++
	}
	if run < 3 {
		return ""
	}
	return line[i : i+run]
}

// textLines splits text into line spans, excluding the newline itself.
func textLines(text string) []byteRange {
	var lines []byteRange
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			lines = append(lines, byteRange{start, i})
			start = i + 1
		}
	}
	return append(lines, byteRange{start, len(text)})
}

// stripNextActionsBlocks removes every <next-actions> block from a reply. They
// are UI affordances rendered as suggestion chips, never part of the answer.
// Mirrors stripAllNextActions() in web/src/components/chat/ChatMessage.tsx.
func stripNextActionsBlocks(text string) string {
	ranges := nextActionsRanges(text)
	if len(ranges) == 0 {
		return text
	}
	var sb strings.Builder
	at := 0
	for _, r := range ranges {
		sb.WriteString(text[at:r.start])
		at = r.end
	}
	sb.WriteString(text[at:])
	return strings.TrimSpace(sb.String())
}

// finalAnswerFromParts returns what a reader sees with the "Worked for …" block
// collapsed: the last unbroken run of text parts in the turn. Everything before
// it — intermediate commentary, thinking, tool calls — is folded away by
// splitTurn() in web/src/components/chat/ChatMessage.tsx, so it is not part of
// the answer.
//
// The two normalisation passes match the render pipeline in ChatMessage.tsx
// (nestSubagentTools → mergeAdjacentText → stripNextActionsAcrossParts) and are
// what make the trailing run correct: without them a sub-agent tool call or a
// tag split across parts would cut the answer short or leak a tag fragment.
func finalAnswerFromParts(parts []MessagePart) string {
	parts = stripNextActionsAcrossParts(dropSubagentTools(parts))

	last := -1
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i].Type == "text" {
			last = i
			break
		}
	}
	if last < 0 {
		return ""
	}
	first := last
	for first > 0 && parts[first-1].Type == "text" {
		first--
	}

	var sb strings.Builder
	for _, p := range parts[first : last+1] {
		sb.WriteString(p.Content)
	}
	return sb.String()
}

// dropSubagentTools removes a sub-agent's tool calls from the linear part list.
// Persisted turns store them flat, interleaved with the main agent's text; the
// UI nests them inside their parent Agent card so they don't fragment that text.
// Left in place here they would truncate the answer at the first sub-agent call.
func dropSubagentTools(parts []MessagePart) []MessagePart {
	topLevel := make(map[string]bool)
	nested := make(map[string]bool)

	out := make([]MessagePart, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "tool_use":
			if p.ParentToolID != "" && topLevel[p.ParentToolID] {
				nested[p.ToolCallID] = true
				continue
			}
			topLevel[p.ToolCallID] = true
		case "tool_result":
			if nested[p.ToolCallID] {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

// stripNextActionsAcrossParts removes <next-actions> blocks that may be split
// across several text parts — during a kept-open turn the model's wrap-up text
// is interleaved with tool calls, so even the opening tag can land in two
// separate parts. Working on the concatenation makes the tag whole again; the
// removed ranges are then mapped back onto each part, and emptied text parts
// are dropped. Port of stripNextActionsAcrossParts() in ChatMessage.tsx.
func stripNextActionsAcrossParts(parts []MessagePart) []MessagePart {
	spanStart := make([]int, len(parts))
	var joined strings.Builder
	for i, p := range parts {
		if p.Type == "text" {
			spanStart[i] = joined.Len()
			joined.WriteString(p.Content)
		}
	}

	ranges := nextActionsRanges(joined.String())
	if len(ranges) == 0 {
		return parts
	}

	out := make([]MessagePart, 0, len(parts))
	for i, p := range parts {
		if p.Type != "text" {
			out = append(out, p)
			continue
		}
		var kept strings.Builder
		for k := 0; k < len(p.Content); k++ {
			if !inRanges(ranges, spanStart[i]+k) {
				kept.WriteByte(p.Content[k])
			}
		}
		trimmed := strings.TrimRight(kept.String(), " \t\r\n")
		if trimmed == "" {
			continue
		}
		p.Content = trimmed
		out = append(out, p)
	}
	return out
}

// ---------------------------------------------------------------------------
// Following a single relayed turn
// ---------------------------------------------------------------------------

// relayAttempt follows one dispatched turn on the target session.
type relayAttempt struct {
	mu     sync.Mutex
	turnID string          // assistant message ID of our turn
	live   strings.Builder // fallback answer, reset at every step boundary
	errMsg string

	done   chan string
	failed chan struct{}
}

func newRelayAttempt() *relayAttempt {
	return &relayAttempt{
		done:   make(chan string, 1),
		failed: make(chan struct{}, 1),
	}
}

func (a *relayAttempt) setTurn(assistantMsgID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.turnID = assistantMsgID
}

// handle consumes the target session's event stream. It keeps a running copy of
// the trailing text run so a reply is still available if the persisted message
// can't be read back.
func (a *relayAttempt) handle(event ChatEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()

	switch event.Type {
	case "text_delta":
		a.live.WriteString(event.Content)

	case "tool_start", "thinking_delta", "widget", "subtask_start":
		// A step ends the current text run — only what follows the last step
		// survives the "Worked for …" collapse.
		a.live.Reset()

	case "error":
		if a.errMsg == "" {
			a.errMsg = event.Message
			select {
			case a.failed <- struct{}{}:
			default:
			}
		}

	case "done":
		// Ignore a `done` belonging to some other turn on this session.
		if a.turnID != "" && event.MessageID != "" && event.MessageID != a.turnID {
			return
		}
		select {
		case a.done <- event.MessageID:
		default:
		}
	}
}

func (a *relayAttempt) snapshot() (liveText, errMsg string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.live.String(), a.errMsg
}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

// relaySessionOwnerID resolves the user boundary for a session through its
// project. Owner 0 is deliberately rejected: the MCP endpoint has no browser
// auth context, so only a concrete project owner can establish identity.
func (s *Server) relaySessionOwnerID(sessionID string) (int64, error) {
	session, err := s.Store.GetSession(sessionID)
	if err != nil {
		return 0, fmt.Errorf("load session %s: %w", sessionID, err)
	}
	if session == nil || session.ProjectID == "" {
		return 0, fmt.Errorf("session %s is not attached to a user-owned project", sessionID)
	}
	project, err := s.Store.GetProject(session.ProjectID)
	if err != nil {
		return 0, fmt.Errorf("load project for session %s: %w", sessionID, err)
	}
	if project == nil || project.OwnerUserID <= 0 {
		return 0, fmt.Errorf("session %s is not attached to a user-owned project", sessionID)
	}
	return project.OwnerUserID, nil
}

func (s *Server) requireRelaySameOwner(fromSessionID, targetSessionID string) error {
	fromOwner, err := s.relaySessionOwnerID(fromSessionID)
	if err != nil {
		return fmt.Errorf("calling %w", err)
	}
	targetOwner, err := s.relaySessionOwnerID(targetSessionID)
	if err != nil {
		return fmt.Errorf("target session is unavailable to the calling session")
	}
	if fromOwner != targetOwner {
		return fmt.Errorf("target session is unavailable to the calling session")
	}
	return nil
}

// SendToSessionAndWait types `message` into targetSessionID on behalf of
// fromSessionID, waits for that session's agent to finish, and returns its final
// visible reply. It has no timeout of its own: it returns when the target
// session finishes or fails, or when ctx is cancelled.
func (s *Server) SendToSessionAndWait(ctx context.Context, fromSessionID, targetSessionID, message string) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("message is empty")
	}
	if targetSessionID == "" {
		return "", fmt.Errorf("target session id is empty")
	}
	if fromSessionID == "" {
		return "", fmt.Errorf("calling session id is empty")
	}
	if fromSessionID == targetSessionID {
		return "", fmt.Errorf("session %s cannot send a message to itself", targetSessionID)
	}
	if err := s.requireRelaySameOwner(fromSessionID, targetSessionID); err != nil {
		return "", err
	}

	if err := registerRelayWait(fromSessionID, targetSessionID); err != nil {
		return "", err
	}
	defer unregisterRelayWait(fromSessionID, targetSessionID)

	// One relay at a time per target, so concurrent senders don't collect each
	// other's replies.
	release, err := acquireRelaySlot(ctx, targetSessionID)
	if err != nil {
		return "", fmt.Errorf("waiting for session %s: %w", targetSessionID, err)
	}
	defer release()

	for {
		if err := s.waitSessionIdle(ctx, targetSessionID); err != nil {
			return "", fmt.Errorf("waiting for session %s to become idle: %w", targetSessionID, err)
		}
		// Re-check after the wait in case either session was moved to a project
		// owned by someone else while the target was busy.
		if err := s.requireRelaySameOwner(fromSessionID, targetSessionID); err != nil {
			return "", err
		}

		// Subscribe before dispatching so no event of our turn is missed.
		attempt := newRelayAttempt()
		unsub := s.Hub.SubscribeSession(targetSessionID, attempt.handle)

		outcome, derr := s.dispatchUserMessage(targetSessionID, message, nil, nil, nil, dispatchOpts{
			noQueue:     true,
			onTurnStart: attempt.setTurn,
		})
		if outcome != dispatchStarted {
			unsub()
			if outcome == dispatchFailed {
				return "", derr
			}
			// The session got busy between the idle check and the dispatch.
			// noQueue means nothing was persisted, so retrying is safe.
			log.Printf("[relay] %s → %s: target became busy, retrying", fromSessionID, targetSessionID)
			continue
		}

		log.Printf("[relay] %s → %s: message delivered, waiting for reply", fromSessionID, targetSessionID)
		answer, werr := s.awaitRelayTurn(ctx, targetSessionID, attempt)
		unsub()
		return answer, werr
	}
}

// waitSessionIdle blocks until the session has no turn running and nothing left
// on its queue, so a relayed message runs as its own turn rather than being
// parked behind existing work.
func (s *Server) waitSessionIdle(ctx context.Context, sessionID string) error {
	logged := false
	for {
		if !s.SessionManager.IsActive(sessionID) {
			queued, err := s.Store.ListQueuedMessages(sessionID)
			if err != nil {
				return fmt.Errorf("list queued messages: %w", err)
			}
			if len(queued) == 0 {
				return nil
			}
		}
		if !logged {
			log.Printf("[relay] session %s is busy, waiting for it to finish", sessionID)
			logged = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(relayIdlePoll):
		}
	}
}

// awaitRelayTurn blocks until the dispatched turn produces a reply, fails, or
// the caller's context is cancelled.
func (s *Server) awaitRelayTurn(ctx context.Context, sessionID string, attempt *relayAttempt) (string, error) {
	var grace <-chan time.Time
	liveness := time.NewTicker(relayLivenessPoll)
	defer liveness.Stop()
	strikes := 0

	// A reply that has already arrived always wins over a timer that fired in
	// the same instant — select picks at random between ready cases, and a
	// delivered answer must never lose that coin flip to a giving-up branch.
	replied := func() (string, bool) {
		select {
		case msgID := <-attempt.done:
			return msgID, true
		default:
			return "", false
		}
	}

	for {
		select {
		case msgID := <-attempt.done:
			return s.relayAnswer(sessionID, msgID, attempt), nil

		case <-attempt.failed:
			// Hold on briefly: most errors are reported mid-turn and are
			// followed by a normal `done` carrying the full reply.
			grace = time.After(relayErrorGrace)

		case <-grace:
			if msgID, ok := replied(); ok {
				return s.relayAnswer(sessionID, msgID, attempt), nil
			}
			_, errMsg := attempt.snapshot()
			return "", fmt.Errorf("session %s failed: %s", sessionID, errMsg)

		case <-liveness.C:
			if msgID, ok := replied(); ok {
				return s.relayAnswer(sessionID, msgID, attempt), nil
			}
			if s.SessionManager.IsActive(sessionID) {
				strikes = 0
				continue
			}
			strikes++
			if strikes < relayLivenessStrikes {
				continue
			}
			if _, errMsg := attempt.snapshot(); errMsg != "" {
				return "", fmt.Errorf("session %s failed: %s", sessionID, errMsg)
			}
			return "", fmt.Errorf("session %s stopped without answering", sessionID)

		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// relayAnswer reads the finished turn back out of the store and extracts the
// visible reply, falling back to the streamed text if the message can't be read.
func (s *Server) relayAnswer(sessionID, msgID string, attempt *relayAttempt) string {
	if msgID != "" {
		msg, err := s.Store.GetMessage(msgID)
		if err != nil {
			log.Printf("[relay] failed to read back message %s of session %s: %v", msgID, sessionID, err)
		} else if msg != nil {
			if answer := strings.TrimSpace(stripNextActionsBlocks(finalAnswerFromParts(msg.Parts))); answer != "" {
				return answer
			}
		}
	}
	live, _ := attempt.snapshot()
	return strings.TrimSpace(stripNextActionsBlocks(live))
}

// ---------------------------------------------------------------------------
// Session lookup by ID prefix
// ---------------------------------------------------------------------------

// relayMinPrefixLen is the shortest ID prefix accepted when addressing a
// session, matching the 6-character badge shown in the chat header.
const relayMinPrefixLen = 6

// ResolveSessionRefFrom applies NexBox's per-user project boundary while
// resolving a target. A ref belonging to another owner behaves exactly like an
// unknown ref, so neither its ID nor title is disclosed to the caller.
func (s *Server) ResolveSessionRefFrom(fromSessionID, ref string) (id, name string, err error) {
	fromOwner, err := s.relaySessionOwnerID(fromSessionID)
	if err != nil {
		return "", "", fmt.Errorf("calling %w", err)
	}

	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("session id is empty")
	}
	if session, getErr := s.Store.GetSession(ref); getErr != nil {
		return "", "", fmt.Errorf("get session: %w", getErr)
	} else if session != nil {
		owner, ownerErr := s.relaySessionOwnerID(session.ID)
		if ownerErr == nil && owner == fromOwner {
			return session.ID, session.Name, nil
		}
		return "", "", fmt.Errorf("no session matches %q", ref)
	}

	norm := normalizeSessionRef(ref)
	if len(norm) < relayMinPrefixLen {
		return "", "", fmt.Errorf("session id %q is too short — use at least %d characters", ref, relayMinPrefixLen)
	}
	sessions, err := s.Store.ListSessions()
	if err != nil {
		return "", "", fmt.Errorf("list sessions: %w", err)
	}
	owned := make([]Session, 0, len(sessions))
	for _, session := range sessions {
		if !strings.HasPrefix(normalizeSessionRef(session.ID), norm) {
			continue
		}
		owner, ownerErr := s.relaySessionOwnerID(session.ID)
		if ownerErr == nil && owner == fromOwner {
			owned = append(owned, session)
		}
	}
	return matchSessionByPrefix(owned, ref, norm)
}

// matchSessionByPrefix resolves a normalized ID prefix against a session list,
// insisting the match be unique. ref is carried through only for error text.
func matchSessionByPrefix(sessions []Session, ref, norm string) (id, name string, err error) {
	var matches []Session
	for _, session := range sessions {
		if strings.HasPrefix(normalizeSessionRef(session.ID), norm) {
			matches = append(matches, session)
		}
	}

	switch len(matches) {
	case 0:
		return "", "", fmt.Errorf("no session matches %q", ref)
	case 1:
		return matches[0].ID, matches[0].Name, nil
	default:
		var sb strings.Builder
		for i, m := range matches {
			if i > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, "%s (%s)", m.ID, m.Name)
		}
		return "", "", fmt.Errorf("%q matches %d sessions: %s — use a longer prefix", ref, len(matches), sb.String())
	}
}

func normalizeSessionRef(ref string) string {
	return strings.ToLower(strings.ReplaceAll(ref, "-", ""))
}

// ---------------------------------------------------------------------------
// SessionOrchestrator surface used by the send_to_session MCP tool
// ---------------------------------------------------------------------------

func (s *Server) OrchestrateResolveSession(fromSessionID, ref string) (id, name string, err error) {
	return s.ResolveSessionRefFrom(fromSessionID, ref)
}

func (s *Server) OrchestrateSendAndWait(ctx context.Context, fromSessionID, targetSessionID, message string) (string, error) {
	return s.SendToSessionAndWait(ctx, fromSessionID, targetSessionID, message)
}
