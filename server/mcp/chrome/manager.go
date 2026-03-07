package chrome

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// ChromeManager creates and manages per-session Chrome MCP servers.
// Each session gets its own Chrome instance with an isolated user data dir.
type ChromeManager struct {
	mu         sync.Mutex
	sessions   map[string]*Server
	baseDir    string // base directory for per-session data
	listenHost string // host part for MCP servers (e.g. "127.0.0.1")
	headless   bool   // whether to launch Chrome headless
}

// NewChromeManager creates a manager. baseDir is the root for per-session
// Chrome profiles and screenshots (e.g. "data/chrome").
func NewChromeManager(baseDir, listenHost string, headless bool) *ChromeManager {
	return &ChromeManager{
		sessions:   make(map[string]*Server),
		baseDir:    baseDir,
		listenHost: listenHost,
		headless:   headless,
	}
}

// GetOrCreate returns the Chrome MCP server for the given session, creating
// one on first call. The server is lazily started (call EnsureStarted after).
func (m *ChromeManager) GetOrCreate(sessionID string) *Server {
	m.mu.Lock()
	defer m.mu.Unlock()

	if srv, ok := m.sessions[sessionID]; ok {
		return srv
	}

	sessionDir := filepath.Join(m.baseDir, sessionID)
	screenshotDir := filepath.Join(sessionDir, "screenshots")
	if err := os.MkdirAll(screenshotDir, 0755); err != nil {
		log.Printf("[chrome-manager] Failed to create screenshot dir %s: %v", screenshotDir, err)
	}

	browserOpts := BrowserOptions{
		Headless: m.headless,
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
