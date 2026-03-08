package main

import (
	"context"
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
	"syscall"
	"time"

	"github.com/rs/cors"
	"gitlab.okg.com/sre_ai/nexbox/agent"
	"gitlab.okg.com/sre_ai/nexbox/api"
	"gitlab.okg.com/sre_ai/nexbox/config"
	nexmcp "gitlab.okg.com/sre_ai/nexbox/mcp"
	chromemcp "gitlab.okg.com/sre_ai/nexbox/mcp/chrome"
	"gitlab.okg.com/sre_ai/nexbox/store"
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
	logFile, logErr := newRollingLog(filepath.Join(*dataDir, "logs"), "beehub.log", 5<<20, 5)
	if logErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to init log file: %v; logging to stderr only\n", logErr)
		startupLog = log.New(os.Stdout, "", log.LstdFlags)
	} else {
		defer logFile.Close()
		log.SetOutput(logFile)
		startupLog = log.New(io.MultiWriter(os.Stdout, logFile), "", log.LstdFlags)
	}

	startupLog.Printf("BeeHub node starting...")
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

	// Initialize store based on config.
	cfg := cfgMgr.Get()
	var dataStore store.Store
	var localStore *store.SQLiteStore
	var detailCache *store.DetailCache

	if cfg.StoreType == "mysql" && cfg.MySQLDSN != "" {
		mysqlStore, err := store.NewMySQLStore(cfg.MySQLDSN)
		if err != nil {
			startupLog.Fatalf("Failed to connect to MySQL: %v", err)
		}
		defer mysqlStore.Close()
		dataStore = mysqlStore
		startupLog.Printf("  Store: mysql")

		// SQLite still needed for DetailCache (local message part caching).
		db, err := store.New(*dataDir)
		if err != nil {
			startupLog.Fatalf("Failed to initialize local SQLite (for cache): %v", err)
		}
		defer db.Close()
		detailCache = store.NewDetailCache(db.DB)
		localStore = store.NewSQLiteStore(db.DB)
	} else {
		db, err := store.New(*dataDir)
		if err != nil {
			startupLog.Fatalf("Failed to initialize database: %v", err)
		}
		defer db.Close()
		detailCache = store.NewDetailCache(db.DB)
		localStore = store.NewSQLiteStore(db.DB)
		dataStore = localStore
		startupLog.Printf("  Store: sqlite")
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

	// Per-session Chrome DevTools MCP manager for E2E testing
	chromeBaseDir := filepath.Join(*dataDir, "chrome")
	chromeManager := chromemcp.NewChromeManager(chromeBaseDir, *listen, false)

	// Initialize all agent backends
	allAgents := make(map[string]agent.Agent)

	// Claude
	ca := agent.NewClaudeAgent()
	ca.MCPServerURL = mcpServer.URL()
	ca.MCPToolNames = mcpServer.ToolNames()
	ca.DataDir = *dataDir
	allAgents["claude"] = ca

	// Codex
	cx := agent.NewCodexAgent()
	cx.MCPServerURL = mcpServer.URL()
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
		TerminalMgr:      termMgr,
		DetailCache:      detailCache,
		DataDir:          *dataDir,
		Version:          Version,
		ChromeManager:    chromeManager,
	}

	// Wire up Docker container isolation if enabled.
	if cfg.DockerEnabled {
		dockerImage := cfg.DockerImage
		if dockerImage == "" {
			dockerImage = "nexbox-agent:latest"
		}
		containerMgr := agent.NewContainerManager(cfg.DockerImage)
		containerMgr.RecoverRunningContainers()
		srv.ContainerMgr = containerMgr
		ca.ContainerMgr = containerMgr
		log.Printf("  Docker isolation enabled (image: %s)", dockerImage)
	}

	// Wire up SessionOrchestrator so MCP execute_workflow can create sessions
	mcpServer.SetSessionOrchestrator(srv)
	mcpServer.SetWorkflowExecutor(srv)
	mcpServer.SetChromeEnabler(&chromeEnablerImpl{manager: chromeManager, store: dataStore})

	// Reset stale workflow executions from previous run (pending/running → failed)
	if err := localStore.ResetStaleExecutions(); err != nil {
		log.Printf("  Warning: failed to reset stale executions: %v", err)
	} else {
		log.Printf("  Stale workflow executions reset.")
	}

	// Periodic cleanup: hard-delete trashed sessions (runs hourly).
	// TRASH_TTL_HOURS controls retention; defaults to 168h (7 days) if unset.
	trashTTL := 24 * time.Hour
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
		runTrashCleanup := func() {
			cutoff := time.Now().Add(-trashTTL)

			// Collect worktree paths before purging so we can clean them up.
			worktreePaths, _ := dataStore.GetExpiredSessionWorktrees(cutoff)

			ids, err := dataStore.PurgeExpiredSessions(cutoff)
			if err != nil {
				log.Printf("[trash] Failed to purge expired sessions: %v", err)
				return
			}
			for _, id := range ids {
				dir := filepath.Join(*dataDir, "sessions", id)
				if err := os.RemoveAll(dir); err != nil {
					log.Printf("[trash] Failed to remove session dir %s: %v", dir, err)
				} else {
					log.Printf("[trash] Purged expired session %s", id)
				}
			}
			// Remove git worktrees for purged sessions.
			for _, wtPath := range worktreePaths {
				if err := api.RemoveWorktree(wtPath); err != nil {
					log.Printf("[trash] Failed to remove worktree %s: %v", wtPath, err)
				} else {
					log.Printf("[trash] Removed worktree %s", wtPath)
				}
			}

			// Purge changes older than 7 days
			changesCutoff := time.Now().Add(-7 * 24 * time.Hour)
			if n, err := dataStore.PurgeOldChanges(changesCutoff); err != nil {
				log.Printf("[cleanup] Failed to purge old changes: %v", err)
			} else if n > 0 {
				log.Printf("[cleanup] Purged %d changes older than 7 days", n)
			}
		}
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				runTrashCleanup()
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

	// Periodic message trimming: keep only the latest 100 messages per session (runs hourly).
	go func() {
		const maxMessagesPerSession = 100
		trimMessages := func() {
			sessions, err := dataStore.ListSessions()
			if err != nil {
				log.Printf("[trim] Failed to list sessions: %v", err)
				return
			}
			var totalDeleted int64
			for _, sess := range sessions {
				n, err := dataStore.TrimSessionMessages(sess.ID, maxMessagesPerSession)
				if err != nil {
					log.Printf("[trim] Failed to trim session %s: %v", sess.ID, err)
					continue
				}
				totalDeleted += n
			}
			if totalDeleted > 0 {
				log.Printf("[trim] Removed %d old messages across %d sessions.", totalDeleted, len(sessions))
			}
		}
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				trimMessages()
			case <-trashCtx.Done():
				return
			}
		}
	}()

	// T3.3: Idle container reaper — stop containers inactive for over 1 hour (checks every 10 min).
	if srv.ContainerMgr != nil {
		go func() {
			const idleTimeout = 1 * time.Hour
			ticker := time.NewTicker(10 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if n := srv.ContainerMgr.StopIdleContainers(idleTimeout); n > 0 {
						log.Printf("[container] reaped %d idle container(s)", n)
					}
				case <-trashCtx.Done():
					return
				}
			}
		}()
	}

	// T9.5: Periodic SQLite maintenance (WAL checkpoint + VACUUM), runs daily.
	if localStore != nil {
		go func() {
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if err := localStore.Maintain(); err != nil {
						log.Printf("[sqlite] maintenance failed: %v", err)
					} else {
						log.Printf("[sqlite] maintenance completed (WAL checkpoint + VACUUM)")
					}
				case <-trashCtx.Done():
					return
				}
			}
		}()
	}

	// Create router with all API routes
	router := api.NewRouter(srv)

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
				<h1>BeeHub API Server</h1>
				<p>Frontend not embedded. Run <code>build.sh</code> to build with embedded frontend.</p>
				<p>API available at <a href="/api/projects">/api/projects</a></p>
			</body></html>`)
		}))
	}

	// CORS — allowed origins are read from config (cors_origins); falls back
	// to built-in dev defaults when the config field is empty.
	c := cors.New(cors.Options{
		AllowedOrigins:   srv.CORSOrigins(),
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

	go func() {
		<-quit
		log.Printf("Shutting down...")
		queueCancel()
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
	startupLog.Printf("BeeHub ready. Visit in browser: %s", appURL)
	// }

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		startupLog.Fatalf("Listen failed: %v", err)
	}
	log.Printf("Server stopped.")
}

