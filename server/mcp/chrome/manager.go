package chrome

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ChromeManager creates and manages per-session Chrome MCP servers.
// Each session gets its own Chrome instance with an isolated user data dir.
type ChromeManager struct {
	mu          sync.Mutex
	sessions    map[string]*Server
	baseDir     string                      // base directory for per-session data
	listenHost  string                      // host part for MCP servers (e.g. "127.0.0.1")
	headlessFn  func() bool                 // resolved at session-create time so toggling the setting affects new sessions
	isActiveFn  func(sessionID string) bool // reports whether a session has a running turn (a live CLI holding the MCP URL)
	idleTimeout time.Duration
	reaperStop  chan struct{}
	reaperDone  chan struct{}
}

// NewChromeManager creates a manager. baseDir is the root for per-session
// Chrome profiles and screenshots (e.g. "data/chrome"). headlessFn is called
// each time a new session's Chrome server is constructed; the value is captured
// at that point and applied on Chrome's lazy first launch. isActiveFn reports
// whether a session currently has a running turn (a live CLI still connected to
// its MCP URL); the reaper uses it to decide whether it may drop the endpoint.
// If idleTimeout > 0, a background reaper frees idle Chrome processes: sessions
// with a running turn keep their MCP server (and port) so the CLI can reconnect
// and relaunch Chrome lazily, while idle sessions with no running turn are torn
// down entirely and re-created lazily on the next turn.
func NewChromeManager(baseDir, listenHost string, headlessFn func() bool, isActiveFn func(sessionID string) bool, idleTimeout time.Duration) *ChromeManager {
	if headlessFn == nil {
		headlessFn = func() bool { return false }
	}
	if isActiveFn == nil {
		isActiveFn = func(string) bool { return false }
	}
	m := &ChromeManager{
		sessions:    make(map[string]*Server),
		baseDir:     baseDir,
		listenHost:  listenHost,
		headlessFn:  headlessFn,
		isActiveFn:  isActiveFn,
		idleTimeout: idleTimeout,
	}
	if idleTimeout > 0 {
		m.reaperStop = make(chan struct{})
		m.reaperDone = make(chan struct{})
		go m.runReaper()
	}
	return m
}

// GetOrCreate returns the Chrome MCP server for the given session, creating
// one on first call. The server is lazily started (call EnsureStarted after).
func (m *ChromeManager) GetOrCreate(sessionID string) *Server {
	m.mu.Lock()
	defer m.mu.Unlock()

	if srv, ok := m.sessions[sessionID]; ok {
		srv.Touch()
		return srv
	}

	sessionDir := filepath.Join(m.baseDir, sessionID)
	screenshotDir := filepath.Join(sessionDir, "screenshots")
	if err := os.MkdirAll(screenshotDir, 0755); err != nil {
		log.Printf("[chrome-manager] Failed to create screenshot dir %s: %v", screenshotDir, err)
	}

	browserOpts := BrowserOptions{
		Headless: m.headlessFn(),
	}
	if execPath := os.Getenv("NEX_CHROME_EXEC_PATH"); execPath != "" {
		browserOpts.ExecPath = execPath
	}
	if dataDir := os.Getenv("NEX_CHROME_DATA_DIR"); dataDir != "" {
		browserOpts.UserDataDir = dataDir
	} else {
		userDataDir := filepath.Join(sessionDir, "chrome-profile")
		if err := os.MkdirAll(userDataDir, 0755); err != nil {
			log.Printf("[chrome-manager] Failed to create user data dir %s: %v", userDataDir, err)
		}
		browserOpts.UserDataDir = userDataDir
	}

	srv := NewServer(m.listenHost+":0", screenshotDir, browserOpts)
	m.sessions[sessionID] = srv
	log.Printf("[chrome-manager] Created Chrome MCP for session %s", sessionID)
	return srv
}

// Remove shuts down and removes the Chrome MCP for a session.
func (m *ChromeManager) Remove(sessionID string) {
	m.mu.Lock()
	srv, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()

	if ok {
		if err := srv.Shutdown(context.Background()); err != nil {
			log.Printf("[chrome-manager] Error shutting down Chrome MCP for session %s: %v", sessionID, err)
		}
		log.Printf("[chrome-manager] Removed Chrome MCP for session %s", sessionID)
	}
}

// ShutdownAll stops all Chrome instances. Called on server exit.
func (m *ChromeManager) ShutdownAll() {
	if m.reaperStop != nil {
		close(m.reaperStop)
		<-m.reaperDone
		m.reaperStop = nil
	}

	m.mu.Lock()
	all := make(map[string]*Server, len(m.sessions))
	for k, v := range m.sessions {
		all[k] = v
	}
	m.sessions = make(map[string]*Server)
	m.mu.Unlock()

	for sid, srv := range all {
		if err := srv.Shutdown(context.Background()); err != nil {
			log.Printf("[chrome-manager] Error shutting down Chrome MCP for session %s: %v", sid, err)
		}
		log.Printf("[chrome-manager] Shut down Chrome MCP for session %s", sid)
	}
}

// ActiveSessions returns the number of sessions with Chrome instances.
func (m *ChromeManager) ActiveSessions() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// runReaper periodically frees Chrome instances that have been idle longer
// than idleTimeout. See reapIdle for the keep-alive-vs-teardown policy.
func (m *ChromeManager) runReaper() {
	defer close(m.reaperDone)
	// Tick at min(timeout/4, 1min). With a 30m timeout this is ~7m30s; with a
	// 4m timeout it's 1m. Bounded so very short timeouts still get checked.
	interval := m.idleTimeout / 4
	if interval > time.Minute {
		interval = time.Minute
	}
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-m.reaperStop:
			return
		case <-t.C:
			m.reapIdle()
		}
	}
}

// reapIdle frees Chrome instances idle longer than idleTimeout. The heavy
// resource (the Chrome process) is always released to reclaim memory; whether
// the lightweight MCP HTTP server is also torn down depends on liveness:
//
//   - Session has a running turn (isActiveFn true): its claude CLI is still
//     connected to this MCP URL and can't be re-pointed, so the HTTP server (and
//     its port) is kept alive and only Chrome is stopped. The next Chrome tool
//     call relaunches Chrome via ensureBrowser and the CLI reconnects seamlessly.
//   - No running turn: no CLI holds the URL, so the server is shut down fully and
//     dropped from the map. A future turn lazily creates a fresh one.
func (m *ChromeManager) reapIdle() {
	cutoff := time.Now().Add(-m.idleTimeout)

	m.mu.Lock()
	var idle []string
	for sid, srv := range m.sessions {
		if srv.LastUsed().Before(cutoff) {
			idle = append(idle, sid)
		}
	}
	m.mu.Unlock()

	for _, sid := range idle {
		if m.isActiveFn(sid) {
			// Turn still running — free Chrome but keep the endpoint alive.
			// ReleaseBrowser re-checks idle + in-flight under the server's own
			// lock, so a call that started since our scan isn't cut off.
			m.mu.Lock()
			srv := m.sessions[sid]
			m.mu.Unlock()
			if srv != nil {
				srv.ReleaseBrowser(cutoff)
			}
			continue
		}

		// No running turn — fully reclaim the server. Re-check idle under the
		// lock so a turn that grabbed this server between the scan and now
		// (GetOrCreate touches it) is not torn down out from under its
		// freshly-spawned CLI.
		m.mu.Lock()
		srv, ok := m.sessions[sid]
		if !ok || !srv.LastUsed().Before(cutoff) {
			m.mu.Unlock()
			continue
		}
		delete(m.sessions, sid)
		m.mu.Unlock()

		if err := srv.Shutdown(context.Background()); err != nil {
			log.Printf("[chrome-manager] Error shutting down idle Chrome MCP for session %s: %v", sid, err)
		}
		log.Printf("[chrome-manager] Reaped idle Chrome MCP for session %s (idle > %s)", sid, m.idleTimeout)
	}
}
