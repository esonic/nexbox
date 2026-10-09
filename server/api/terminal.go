package api

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// terminalResize is sent from the client to resize the PTY.
type terminalResize struct {
	Type string `json:"type"` // "resize"
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// HandleTerminalWS is the legacy project-scoped terminal endpoint.
// New code should use HandleTerminalSessionWS (/ws/terminal/sessions/{sessionId}).
// GET /ws/terminal/{projectId}
func (s *Server) HandleTerminalWS(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeWS(w, r); !ok {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	workDir := project.GitRepoPath
	if workDir == "" {
		workDir, _ = os.UserHomeDir()
	}

	legacyKey := "project:" + projectID
	serveTerminal(s, w, r, legacyKey, workDir)
}

// HandleTerminalSessionWS upgrades to WebSocket and spawns a PTY shell for a session.
// Working directory follows the session's worktree path when in worktree mode.
// GET /ws/terminal/sessions/{sessionId}
func (s *Server) HandleTerminalSessionWS(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeWS(w, r); !ok {
		return
	}
	sessionID := chi.URLParam(r, "sessionId")
	session, err := s.Store.GetSession(sessionID)
	if err != nil || session == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	workDir := s.sessionWorkDir(session)
	if workDir == "" {
		workDir, _ = os.UserHomeDir()
	}

	serveTerminal(s, w, r, sessionID, workDir)
}

// sessionWorkDir returns the effective working directory for a session.
// Returns worktree_path when in worktree mode with a created worktree,
// otherwise falls back to the project's git_repo_path.
func (s *Server) sessionWorkDir(session *Session) string {
	if session.WorktreeMode == "worktree" && session.WorktreePath != "" {
		if session.ProjectID != "" {
			project, err := s.getProjectWithLocalPath(session.ProjectID)
			if err == nil && project != nil && project.GitRepoPath != "" {
				if effectiveDir, normalizeErr := projectPathInWorktree(session.WorktreePath, project.GitRepoPath); normalizeErr == nil {
					return effectiveDir
				}
			}
		}
		return session.WorktreePath
	}
	if session.ProjectID != "" {
		project, err := s.getProjectWithLocalPath(session.ProjectID)
		if err == nil && project != nil && project.GitRepoPath != "" {
			return project.GitRepoPath
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// PTY session registry — allows a brief WebSocket drop to reattach to the
// same shell instead of killing it. On weak/lossy networks this prevents the
// terminal from being torn down every time a packet is delayed.
// ---------------------------------------------------------------------------

// terminalReattachGrace is how long we keep a detached PTY alive waiting for
// the client to reconnect. After this window expires with no client, the
// shell is killed.
const terminalReattachGrace = 60 * time.Second

// terminalScrollbackBytes is the size of the per-session output ring buffer
// retained for replay when a new WebSocket attaches. Sized to hold a few
// screens of output so the reattached terminal shows recent context.
const terminalScrollbackBytes = 64 * 1024

// termSession is a long-lived PTY/shell pair with at most one active WS
// attached at a time. When the WS drops we keep the PTY running and let
// another WS for the same terminalID reattach within terminalReattachGrace.
type termSession struct {
	id      string
	ptmx    *os.File
	cmd     *exec.Cmd
	workDir string

	mu        sync.Mutex
	conn      *websocket.Conn // nil when detached
	writeMu   sync.Mutex      // serialises conn.WriteMessage; held only while conn is set
	scrollbuf []byte          // ring-style: tail is most recent
	killTimer *time.Timer
	closed    bool
	exited    chan struct{} // closed when PTY/cmd is fully gone
}

var (
	termRegistryMu sync.Mutex
	termRegistry   = map[string]*termSession{}
)

func getOrCreateTermSession(id, workDir string) (*termSession, bool, error) {
	termRegistryMu.Lock()
	defer termRegistryMu.Unlock()

	if existing, ok := termRegistry[id]; ok {
		existing.mu.Lock()
		if !existing.closed {
			if existing.killTimer != nil {
				existing.killTimer.Stop()
				existing.killTimer = nil
			}
			existing.mu.Unlock()
			return existing, true, nil
		}
		existing.mu.Unlock()
		delete(termRegistry, id)
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, false, err
	}

	ts := &termSession{
		id:      id,
		ptmx:    ptmx,
		cmd:     cmd,
		workDir: workDir,
		exited:  make(chan struct{}),
	}
	termRegistry[id] = ts

	go ts.readPTY()
	go func() {
		_ = cmd.Wait()
		close(ts.exited)
		ts.shutdown()
	}()

	return ts, false, nil
}

// readPTY pumps PTY output to the scrollback buffer + active conn, forever
// (until the PTY is closed).
func (t *termSession) readPTY() {
	buf := make([]byte, 4096)
	for {
		n, err := t.ptmx.Read(buf)
		if n > 0 {
			t.appendScrollback(buf[:n])
			t.writeToConn(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (t *termSession) appendScrollback(data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.scrollbuf = append(t.scrollbuf, data...)
	if len(t.scrollbuf) > terminalScrollbackBytes {
		t.scrollbuf = t.scrollbuf[len(t.scrollbuf)-terminalScrollbackBytes:]
	}
}

func (t *termSession) writeToConn(data []byte) {
	// writeMu also guards swapping t.conn (in attach/detach), so reading and
	// writing under the same lock keeps reattach output strictly ordered.
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	conn := t.conn
	if conn == nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		// Write failure → drop the conn; the read loop on the WS side will
		// see this and detach. We don't kill the PTY here.
		_ = conn.Close()
	}
}

// attach binds the new conn to this session and replays buffered output to
// catch the client up. Any previous conn is closed. Holds writeMu across
// the swap+replay so concurrent PTY output can't be written ahead of the
// replay (which would scramble the terminal on reattach).
func (t *termSession) attach(conn *websocket.Conn) {
	t.mu.Lock()
	replay := append([]byte(nil), t.scrollbuf...)
	t.mu.Unlock()

	t.writeMu.Lock()
	prev := t.conn
	t.conn = conn
	if len(replay) > 0 {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = conn.WriteMessage(websocket.BinaryMessage, replay)
	}
	t.writeMu.Unlock()

	if prev != nil {
		_ = prev.Close()
	}
}

// detach is called when the active conn drops. We keep the PTY alive and
// arm a kill timer; if a new client reattaches before it fires, the timer
// is cancelled in getOrCreateTermSession.
func (t *termSession) detach(conn *websocket.Conn) {
	t.writeMu.Lock()
	if t.conn == conn {
		t.conn = nil
	}
	t.writeMu.Unlock()

	t.mu.Lock()
	if t.conn == nil && t.killTimer == nil && !t.closed {
		t.killTimer = time.AfterFunc(terminalReattachGrace, t.shutdown)
	}
	t.mu.Unlock()
}

func (t *termSession) shutdown() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	if t.killTimer != nil {
		t.killTimer.Stop()
		t.killTimer = nil
	}
	t.mu.Unlock()

	t.writeMu.Lock()
	conn := t.conn
	t.conn = nil
	t.writeMu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	_ = t.ptmx.Close()
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}

	termRegistryMu.Lock()
	if termRegistry[t.id] == t {
		delete(termRegistry, t.id)
	}
	termRegistryMu.Unlock()
}

// serveTerminal upgrades the request, attaches it to a new or existing PTY
// session for terminalID, and pumps WS-side input until the connection
// drops. On drop the PTY is left running with a grace timer so the client
// can reattach.
func serveTerminal(s *Server, w http.ResponseWriter, r *http.Request, terminalID, workDir string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[terminal] WebSocket upgrade error: %v", err)
		return
	}

	ts, reattached, err := getOrCreateTermSession(terminalID, workDir)
	if err != nil {
		log.Printf("[terminal] Failed to start PTY: %v", err)
		_ = conn.WriteMessage(websocket.TextMessage, []byte("Error: failed to start terminal: "+err.Error()))
		_ = conn.Close()
		return
	}

	if !reattached && s.TerminalMgr != nil {
		s.TerminalMgr.Register(terminalID, ts.ptmx)
		// On shutdown of this session, unregister too. We piggy-back on the
		// exited channel since shutdown is what closes the PTY.
		go func() {
			<-ts.exited
			if s.TerminalMgr != nil {
				s.TerminalMgr.Unregister(terminalID)
			}
		}()
	}

	ts.attach(conn)
	if reattached {
		log.Printf("[terminal] Reattached id=%s (pid=%d)", terminalID, ts.cmd.Process.Pid)
	} else {
		log.Printf("[terminal] Started shell in %s id=%s (pid=%d)", workDir, terminalID, ts.cmd.Process.Pid)
	}

	// Pump WS input → PTY for this conn. Loop returns when this specific WS
	// drops; the PTY keeps running.
	for {
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if msgType == websocket.TextMessage {
			var ctrl terminalResize
			if json.Unmarshal(msg, &ctrl) == nil && ctrl.Type == "resize" {
				if ctrl.Cols > 0 && ctrl.Rows > 0 {
					_ = pty.Setsize(ts.ptmx, &pty.Winsize{
						Cols: ctrl.Cols,
						Rows: ctrl.Rows,
					})
				}
				continue
			}
		}
		if _, err := ts.ptmx.Write(msg); err != nil {
			break
		}
	}

	_ = conn.Close()
	ts.detach(conn)
	log.Printf("[terminal] WS detached id=%s (grace %.0fs)", terminalID, terminalReattachGrace.Seconds())
}
