package api

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"

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
	spawnTerminal(s, w, r, legacyKey, workDir)
}

// HandleTerminalSessionWS upgrades to WebSocket and spawns a PTY shell for a session.
// Working directory follows the session's worktree path when in worktree mode.
// GET /ws/terminal/sessions/{sessionId}
func (s *Server) HandleTerminalSessionWS(w http.ResponseWriter, r *http.Request) {
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

	spawnTerminal(s, w, r, sessionID, workDir)
}

// sessionWorkDir returns the effective working directory for a session.
// Returns worktree_path when in worktree mode with a created worktree,
// otherwise falls back to the project's git_repo_path.
func (s *Server) sessionWorkDir(session *Session) string {
	if session.WorktreeMode == "worktree" && session.WorktreePath != "" {
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

// spawnTerminal does the actual PTY creation and WebSocket bridging.
func spawnTerminal(s *Server, w http.ResponseWriter, r *http.Request, terminalID, workDir string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[terminal] WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("[terminal] Failed to start PTY: %v", err)
		_ = conn.WriteMessage(websocket.TextMessage, []byte("Error: failed to start terminal: "+err.Error()))
		return
	}
	defer func() {
		if s.TerminalMgr != nil {
			s.TerminalMgr.Unregister(terminalID)
		}
		ptmx.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	if s.TerminalMgr != nil {
		s.TerminalMgr.Register(terminalID, ptmx)
	}

	log.Printf("[terminal] Started shell %s in %s id=%s (pid=%d)", shell, workDir, terminalID, cmd.Process.Pid)

	var once sync.Once
	done := make(chan struct{})
	closeDone := func() { once.Do(func() { close(done) }) }

	// PTY → WebSocket (stdout)
	go func() {
		defer closeDone()
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				return
			}
		}
	}()

	// WebSocket → PTY (stdin + resize)
	go func() {
		defer closeDone()
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}

			// Try to parse as JSON control message (resize)
			if msgType == websocket.TextMessage {
				var ctrl terminalResize
				if json.Unmarshal(msg, &ctrl) == nil && ctrl.Type == "resize" {
					if ctrl.Cols > 0 && ctrl.Rows > 0 {
						_ = pty.Setsize(ptmx, &pty.Winsize{
							Cols: ctrl.Cols,
							Rows: ctrl.Rows,
						})
					}
					continue
				}
			}

			// Otherwise treat as stdin input
			if _, err := ptmx.Write(msg); err != nil {
				return
			}
		}
	}()

	<-done
	log.Printf("[terminal] Session ended id=%s (pid=%d)", terminalID, cmd.Process.Pid)
}
