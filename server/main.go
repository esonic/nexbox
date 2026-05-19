package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/cors"
	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/api"
	"github.com/esonic/nexbox/config"
	nexmcp "github.com/esonic/nexbox/mcp"
	chromemcp "github.com/esonic/nexbox/mcp/chrome"
	"github.com/esonic/nexbox/plugins/okone"
	"github.com/esonic/nexbox/store"
	"github.com/esonic/nexbox/updater"
)

// Version is injected at build time via -ldflags "-X main.Version=<timestamp>".
// Stays "dev" for local development builds.
var Version = "dev"

// configAdapter adapts config.Manager to the mcp.ConfigGetter interface.
type configAdapter struct{ mgr *config.Manager }

func (a configAdapter) GitLabToken() string { return a.mgr.Get().GitLabToken }
func (a configAdapter) ProjectIDs() []string {
	cfg := a.mgr.Get()
	ids := make([]string, len(cfg.Projects))
	for i, p := range cfg.Projects {
		ids[i] = p.RemoteID
	}
	return ids
}

func (a configAdapter) GitRepoPath(projectID string) string {
	for _, m := range a.mgr.Get().Projects {
		if m.RemoteID == projectID {
			return m.GitRepoPath
		}
	}
	return ""
}

// chromeEnablerImpl adapts ChromeManager + store to the mcp.ChromeEnabler interface.
type chromeEnablerImpl struct {
	manager *chromemcp.ChromeManager
	store   store.Store
}

func (c *chromeEnablerImpl) EnableChrome(sessionID string) (string, []string, error) {
	sess, err := c.store.GetSession(sessionID)
	if err != nil || sess == nil {
		return "", nil, fmt.Errorf("session not found: %s", sessionID)
	}
	features := "chrome_devtools"
	if sess.Features != "" {
		already := false
		for _, f := range strings.Split(sess.Features, ",") {
			if strings.TrimSpace(f) == "chrome_devtools" {
				already = true
				break
			}
		}
		if !already {
			features = sess.Features + ",chrome_devtools"
		} else {
			features = sess.Features
		}
	}
	if err := c.store.UpdateSessionFeatures(sessionID, features); err != nil {
		return "", nil, fmt.Errorf("failed to save feature: %w", err)
	}
	chromeSrv := c.manager.GetOrCreate(sessionID)
	if err := chromeSrv.EnsureStarted(context.Background()); err != nil {
		return "", nil, fmt.Errorf("failed to start Chrome MCP: %w", err)
	}
	return chromeSrv.URL(), chromeSrv.ToolNames(), nil
}

func main() {
	showVersion := flag.Bool("version", false, "Print version and exit")
	listen := flag.String("listen", "127.0.0.1", "Listen address")
	port := flag.Int("port", 9040, "Server port")
	dataDir := flag.String("data-dir", "", "Data directory (default: binary's directory)")
	flag.Parse()

	if *showVersion {
		fmt.Println(Version)
		return
	}

	// Default data directory
	if *dataDir == "" {
		execPath, err := os.Executable()
		if err == nil {
			execPath, _ = filepath.EvalSymlinks(execPath)
		}
		if err == nil {
			*dataDir = filepath.Dir(execPath)
		} else {
			*dataDir = "."
		}
	}

	// Set up rolling file logger.
	// Startup messages are written to both stdout and the log file so the
	// operator can see them in the terminal.  All other log.* calls (runtime
	// logs in goroutines, API handlers, etc.) go to the file only.
	var startupLog *log.Logger
	logFile, logErr := newRollingLog(filepath.Join(*dataDir, "logs"), "nexbox.log", 5<<20, 5)
	if logErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to init log file: %v; logging to stderr only\n", logErr)
		startupLog = log.New(os.Stdout, "", log.LstdFlags)
	} else {
		defer logFile.Close()
		log.SetOutput(logFile)
		startupLog = log.New(io.MultiWriter(os.Stdout, logFile), "", log.LstdFlags)
	}

	startupLog.Printf("Nexbox node starting...")
	startupLog.Printf("  Data directory: %s", *dataDir)

	// Detect first run before config is created
	configPath := filepath.Join(*dataDir, "config.json")
	_, statErr := os.Stat(configPath)
	isFirstRun := os.IsNotExist(statErr)

	cfgMgr, err := config.NewManager(configPath)
	if err != nil {
		startupLog.Fatalf("Failed to load config: %v", err)
	}
	log.Printf("  Config: %s", configPath)

	// Reset any stale workflow executions left over from a previous run
	// (done after store init below — see resetStaleExecutions call)

	// Always create local SQLite DB (used for detail cache in both modes)
	db, err := store.New(*dataDir)
	if err != nil {
		startupLog.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()
	detailCache := store.NewDetailCache(db)

	// Initialize the Store: use remote if token is configured, otherwise SQLite.
	// In remote (cloud) mode, sessions and messages are kept local for privacy;
	// only projects/tasks/assets/etc. go to the remote server.
	var dataStore store.Store
	var remoteStore *store.RemoteStore
	cfg := cfgMgr.Get()
	storeMode := "sqlite"
	localStore := store.NewSQLiteStore(db)

	if cfg.DisableCloud {
		dataStore = localStore
		startupLog.Printf("  Store: sqlite (cloud disabled via config)")
	} else if cfg.ServerURL == "" {
		startupLog.Fatalf("server_url is required when disable_cloud is false.\nSet server_url in config or set disable_cloud=true to use local mode.")
	} else if cfg.Token != "" {
		storeMode = "remote"
		remoteStore = store.NewRemoteStore(cfg.ServerURL, cfg.Token)
		if err := remoteStore.Ping(); err != nil {
			startupLog.Fatalf("Failed to connect to cloud store (%s): %v\nSet disable_cloud=true in config to use local mode.", cfg.ServerURL, err)
		}
		dataStore = store.NewCompositeStore(remoteStore, localStore)
		log.Printf("  Store: remote (%s)", cfg.ServerURL)
	} else {
		// No token yet — frontend will show login UI; sessions/messages go local,
		// project/task/asset ops are unreachable until ActivateCloud() is called after login.
		dataStore = store.NewCompositeStore(localStore, localStore)
		log.Printf("  Store: composite/local (not logged in, awaiting login)")
	}

	// Start MCP server for AI asset tools (auto-assign port with :0)
	mcpAddr := fmt.Sprintf("%s:%d", *listen, 0)
	mcpServer := nexmcp.NewServer(dataStore, mcpAddr)
	if err := mcpServer.Listen(); err != nil {
		startupLog.Fatalf("MCP server listen failed: %v", err)
	}
	go func() {
		if err := mcpServer.Serve(); err != nil && err.Error() != "http: Server closed" {
			log.Printf("MCP server error: %v", err)
		}
	}()

	// Per-session Chrome DevTools MCP manager for E2E testing.
	// Headless is read from config on each new-session creation, so toggling
	// the setting affects newly spawned Chrome instances without a restart.
	// Idle Chrome instances are reaped after chromeIdleTimeout; the next tool
	// call re-launches a fresh one.
	const chromeIdleTimeout = 10 * time.Minute
	chromeBaseDir := filepath.Join(*dataDir, "chrome")
	chromeManager := chromemcp.NewChromeManager(chromeBaseDir, *listen, func() bool {
		return cfgMgr.Get().ChromeHeadless
	}, chromeIdleTimeout)

	// Initialize all agent backends
	allAgents := make(map[string]agent.Agent)

	// Claude
	ca := agent.NewClaudeAgent()
	ca.MCPServerURL = mcpServer.URL()
	ca.MCPToolNames = mcpServer.ToolNames()
	ca.DataDir = *dataDir
	// Claude init handshake (~3-4s cold) pre-populates the model catalog
	// so /api/available-models returns instantly on first request. Runs
	// asynchronously so a slow CLI doesn't block server startup.
	go func() {
		warmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		ca.WarmupSupportedModels(warmCtx)
	}()
	allAgents["claude"] = ca

	// Codex
	cx := agent.NewCodexAgent()
	cx.MCPServerURL = mcpServer.URL()
	{
		warmCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cx.WarmupSupportedModels(warmCtx)
		cancel()
	}
	allAgents["codex"] = cx

	// Load default agent from config
	defaultAgentName := cfgMgr.Get().DefaultAgent
	if defaultAgentName == "" {
		defaultAgentName = "claude"
	}
	defaultAgent, ok := allAgents[defaultAgentName]
	if !ok {
		defaultAgentName = "claude"
		defaultAgent = allAgents[defaultAgentName]
	}
	log.Printf("  Default agent: %s", defaultAgentName)
	sessionManager := agent.NewSessionManager()

	// Create ChatHub for global WebSocket multiplexing
	hub := api.NewChatHub()

	// Terminal manager for run_in_terminal MCP tool
	termMgr := api.NewTerminalManager()
	mcpServer.SetTerminalManager(termMgr)

	// Config getter for MCP tools (e.g. GitLab token)
	mcpServer.SetConfigGetter(configAdapter{cfgMgr})

	// Let MCP tools broadcast data changes to connected browsers
	mcpServer.SetChangeNotifier(hub.NotifyChange)

	// SessionOrchestrator is set after api.Server creation (see below)

	// Start self-updater; notifies clients via WebSocket when a new binary is ready.
	upd := updater.New(Version, *dataDir, func(remoteVersion string) {
		data, _ := json.Marshal(map[string]interface{}{
			"type":    "update:available",
			"version": remoteVersion,
		})
		hub.BroadcastRaw(data)
	})
	upd.Start()
	defer upd.Stop()
	startupLog.Printf("  Version: %s", Version)

	// Create API server
	srv := &api.Server{
		Store:            dataStore,
		LocalStore:       localStore,
		Config:           cfgMgr,
		Agent:            defaultAgent,
		AgentName:        defaultAgentName,
		Agents:           allAgents,
		SessionManager:   sessionManager,
		MCPServerURL:     mcpServer.URL(),
		Hub:              hub,
		StoreMode:        storeMode,
		TerminalMgr:      termMgr,
		DetailCache:      detailCache,
		Cloud:            remoteStore,
		DataDir:          *dataDir,
		Updater:          upd,
		Version:          Version,
		ChromeManager:    chromeManager,
		ListenPort:       *port,
		MarketplaceStats: api.NewMarketplaceStatsBuffer(),
	}

	go api.CloneOfficialMarketplaceRepos(*dataDir, localStore)

	mcpServer.SetSessionOrchestrator(srv)
	mcpServer.SetChromeEnabler(&chromeEnablerImpl{manager: chromeManager, store: dataStore})

	// Reset stale workflow executions from previous run (pending/running → failed)
	if err := localStore.ResetStaleExecutions(); err != nil {
		log.Printf("  Warning: failed to reset stale executions: %v", err)
	} else {
		log.Printf("  Stale workflow executions reset.")
	}

	// Periodic cleanup: hard-delete trashed sessions (runs hourly).
	// TRASH_TTL_HOURS controls retention; defaults to 168h (7 days) if unset.
	trashTTL := 7 * 24 * time.Hour
	if v := os.Getenv("NEX_TRASH_TTL_HOURS"); v != "" {
		if h, err := strconv.ParseFloat(v, 64); err == nil && h > 0 {
			trashTTL = time.Duration(h * float64(time.Hour))
			log.Printf("  Trash TTL set to %.1f hours (from TRASH_TTL_HOURS).", h)
		} else {
			log.Printf("  Warning: invalid TRASH_TTL_HOURS=%q, using default 168h.", v)
		}
	}
	trashCtx, trashCancel := context.WithCancel(context.Background())
	defer trashCancel()
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cutoff := time.Now().Add(-trashTTL)
				if n, err := srv.PurgeTrash(cutoff); err != nil {
					log.Printf("[trash] Failed to purge expired sessions: %v", err)
				} else if n > 0 {
					log.Printf("[trash] Purged %d expired sessions", n)
				}
			case <-trashCtx.Done():
				return
			}
		}
	}()

	// Start workflow scheduler (checks every minute for due scheduled workflows)
	schedulerCtx, schedulerCancel := context.WithCancel(context.Background())
	defer schedulerCancel()
	go srv.StartWorkflowScheduler(schedulerCtx)

	// Start queue poller: processes pending queue items for idle sessions every 2s.
	queueCtx, queueCancel := context.WithCancel(context.Background())
	defer queueCancel()
	go srv.StartQueuePoller(queueCtx)

	// Periodic cleanup of stale message part details (keep recent 50 sessions, every hour)
	go func() {
		runCleanup := func() {
			sessions, err := dataStore.ListSessions()
			if err != nil {
				log.Printf("[detail-cache] Failed to list sessions for cleanup: %v", err)
				return
			}
			const keepRecent = 50
			keep := make([]string, 0, keepRecent)
			for i, s := range sessions {
				if i >= keepRecent {
					break
				}
				keep = append(keep, s.ID)
			}
			if err := detailCache.Cleanup(keep); err != nil {
				log.Printf("[detail-cache] Cleanup error: %v", err)
			}
		}

		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			runCleanup()
		}
	}()

	// Create router with all API routes
	router := api.NewRouter(srv)

	// OKOne change order routes
	// Use srv.Cloud so the username stays correct after a mid-session login.
	getUserName := func() string {
		if srv.Cloud != nil {
			return srv.Cloud.Info.UserName
		}
		return ""
	}
	router.Mount("/api/okone", okone.NewRouter(func() store.Store { return srv.Store }, cfgMgr, getUserName))

	// Serve embedded frontend static files as fallback (SPA support)
	if hasEmbeddedWeb() {
		log.Printf("  Serving embedded frontend")
		router.NotFound(embeddedSPAHandler())
	} else {
		startupLog.Printf("  No embedded frontend (build with build.sh to include)")
		router.NotFound(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<!DOCTYPE html><html><body>
				<h1>Nexbox API Server</h1>
				<p>Frontend not embedded. Run <code>build.sh</code> to build with embedded frontend.</p>
				<p>API available at <a href="/api/projects">/api/projects</a></p>
			</body></html>`)
		}))
	}

	// CORS for development
	c := cors.New(cors.Options{
		AllowedOrigins:   []string{"http://localhost:5173", "http://localhost:3000", "http://127.0.0.1:5173"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
		AllowCredentials: true,
	})

	handler := c.Handler(router)

	addr := fmt.Sprintf("%s:%d", *listen, *port)
	httpServer := &http.Server{Addr: addr, Handler: handler}

	// Graceful shutdown on SIGINT/SIGTERM
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Cloud-mode poll goroutine management.
	// startCloudMode / stopCloudMode are safe to call concurrently and are
	// idempotent: startCloudMode does nothing if a poll loop is already running,
	// stopCloudMode does nothing if one is not.
	var (
		cloudMu        sync.Mutex
		activePollStop chan struct{} // nil means not running
	)

	startCloudMode := func() {
		cloudMu.Lock()
		defer cloudMu.Unlock()
		if activePollStop != nil {
			return // already running
		}
		// Sync MCP server to the updated cloud store so that tool calls
		// (get_asset_content, etc.) use the remote store after login.
		mcpServer.SetStore(srv.Store)
		stop := make(chan struct{})
		larkCh := make(chan store.Change, 8)
		activePollStop = stop
		go pollChanges(srv, larkCh, stop)
		go larkChatWorker(srv, larkCh)
		log.Printf("[cloud] poll goroutines started")
	}

	stopCloudMode := func() {
		cloudMu.Lock()
		defer cloudMu.Unlock()
		if activePollStop != nil {
			// Revert MCP server to local store after logout.
			mcpServer.SetStore(localStore)
			close(activePollStop)
			activePollStop = nil
			// larkChatCh is closed by pollChanges via defer, which stops larkChatWorker
			log.Printf("[cloud] poll goroutines stopping")
		}
	}

	// Wire up callbacks so ActivateCloud / DeactivateCloud manage the goroutines.
	srv.OnCloudActivated = startCloudMode
	srv.OnCloudDeactivated = stopCloudMode

	// If already authenticated at startup, start immediately.
	if storeMode == "remote" {
		startCloudMode()
	}

	go func() {
		<-quit
		log.Printf("Shutting down...")
		queueCancel()
		stopCloudMode()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("HTTP server shutdown error: %v", err)
		}
		if err := mcpServer.Shutdown(ctx); err != nil {
			log.Printf("MCP server shutdown error: %v", err)
		}
		chromeManager.ShutdownAll()
	}()

	log.Printf("  Listening on http://%s", addr)
	appURL := fmt.Sprintf("http://127.0.0.1:%d", *port)
	_ = isFirstRun
	// if isFirstRun {
	// 	startupLog.Printf("First run detected — opening browser at %s", appURL)
	// 	startupLog.Printf("If the browser did not open, please visit: %s", appURL)
	// 	go openBrowser(appURL)
	// } else {
	startupLog.Printf("Nexbox ready. Visit in browser: %s", appURL)
	// }

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		startupLog.Fatalf("Listen failed: %v", err)
	}
	log.Printf("Server stopped.")
}

// pollChanges periodically fetches change records from the remote store
// and broadcasts them to connected browsers via the ChatHub.
// Uses adaptive polling: short interval (3s) when active, backs off to 30s when idle.
// Also polls for lark_chat messages in the same loop.
func pollChanges(srv *api.Server, larkChatCh chan<- store.Change, stop chan struct{}) {
	defer close(larkChatCh) // sender closes the channel so larkChatWorker can exit
	const (
		minInterval = 3 * time.Second
		maxInterval = 30 * time.Second
	)

	s := srv.Store
	hub := srv.Hub
	rs := srv.Cloud
	var lastID int64
	var lastLarkChatID int64
	interval := minInterval

	for {
		select {
		case <-stop:
			return
		case <-time.After(interval):
		}

		gotActivity := false

		// Poll normal changes
		changes, err := s.ListChanges(lastID, 100, "asc")
		if err != nil {
			if ae, ok := err.(*store.ApiError); ok && ae.StatusCode == http.StatusGone {
				log.Printf("[poll] cursor invalidated, resetting to 0")
				lastID = 0
				interval = minInterval
				continue
			}
			log.Printf("[poll] error fetching changes: %v", err)
			interval = maxInterval
			continue
		}

		if len(changes) > 0 {
			gotActivity = true
			for _, ch := range changes {
				if ch.ID > lastID {
					lastID = ch.ID
				}
				msg := map[string]interface{}{
					"type":           "change",
					"entity_type":    ch.EntityType,
					"entity_id":      ch.EntityID,
					"action":         ch.Action,
					"user_name":      ch.UserName,
					"entity_version": ch.EntityVersion,
				}
				if ch.Recipient != "" {
					msg["recipient"] = ch.Recipient
					msg["id"] = ch.ID
				}
				data, _ := json.Marshal(msg)
				hub.BroadcastRaw(data)
			}
		}

		// Poll lark_chat messages
		if rs != nil {
			larkMsgs, err := rs.ListLarkChat(lastLarkChatID, 10)
			if err != nil {
				log.Printf("[poll] error fetching lark_chat: %v", err)
			} else {
				for _, msg := range larkMsgs {
					if msg.ID > lastLarkChatID {
						lastLarkChatID = msg.ID
					}
					if time.Since(msg.CreatedAt) > 1*time.Minute {
						continue
					}
					gotActivity = true
					select {
					case larkChatCh <- msg:
					default:
						log.Printf("[lark-chat] queue full, dropping message id=%d", msg.ID)
						_ = rs.SendLarkReply(rs.Info.UserName, fmt.Sprintf("Queue full (%d messages pending), please wait and retry.", cap(larkChatCh)))
					}
				}
			}
		}

		// Flush marketplace stats piggyback on each poll cycle
		srv.FlushMarketplaceStats()

		if gotActivity {
			interval = minInterval
		} else {
			interval = min(interval*2, maxInterval)
		}
	}
}

// larkChatWorker is a single persistent goroutine that processes lark_chat messages sequentially.
func larkChatWorker(srv *api.Server, ch <-chan store.Change) {
	for msg := range ch {
		srv.HandleLarkChat(msg)
	}
}
