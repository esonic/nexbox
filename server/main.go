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

	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/api"
	"github.com/esonic/nexbox/config"
	nexmcp "github.com/esonic/nexbox/mcp"
	chromemcp "github.com/esonic/nexbox/mcp/chrome"
	"github.com/esonic/nexbox/store"
	"github.com/rs/cors"
)

// Version is injected at build time via -ldflags "-X main.Version=<timestamp>".
// Stays "dev" for local development builds.
var Version = "dev"

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
	// CLI subcommands (`nexbox add-user`, `list-users`, etc.) are dispatched
	// before flag parsing so they can take positional args without colliding
	// with the server's --flag options. The dispatcher also handles `-h` /
	// `--help` / `help` by printing combined usage.
	if handled, code := runCLISubcommand(defaultDataDir()); handled {
		os.Exit(code)
	}

	flag.Usage = printUsage
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

	configPath := filepath.Join(*dataDir, "config.json")
	cfgMgr, err := config.NewManager(configPath)
	if err != nil {
		startupLog.Fatalf("Failed to load config: %v", err)
	}
	log.Printf("  Config: %s", configPath)

	// Always-local sqlite store.
	db, err := store.New(*dataDir)
	if err != nil {
		startupLog.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()
	detailCache := store.NewDetailCache(db)
	dataStore := store.NewSQLiteStore(db)
	log.Printf("  Store: sqlite")

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

	// Created before the Chrome manager so the reaper can consult it (below).
	sessionManager := agent.NewSessionManager()

	// Per-session Chrome DevTools MCP manager. Headless is read from config on
	// each new-session creation, so toggling the setting affects newly spawned
	// Chrome instances without a restart.
	// After chromeIdleTimeout the reaper frees the idle Chrome process; a
	// session with a running turn keeps its MCP endpoint (so the live CLI can
	// reconnect and relaunch Chrome), while one with no running turn is torn
	// down fully and re-created lazily on the next turn.
	const chromeIdleTimeout = 10 * time.Minute
	chromeBaseDir := filepath.Join(*dataDir, "chrome")
	chromeManager := chromemcp.NewChromeManager(chromeBaseDir, *listen, func() bool {
		return cfgMgr.Get().ChromeHeadless
	}, sessionManager.IsActive, chromeIdleTimeout)

	// Initialize all agent backends
	allAgents := make(map[string]agent.Agent)

	ca := agent.NewClaudeAgent()
	ca.MCPServerURL = mcpServer.URL()
	ca.MCPToolNames = mcpServer.ToolNames()
	ca.DataDir = *dataDir
	go func() {
		warmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		ca.WarmupSupportedModels(warmCtx)
	}()
	allAgents["claude"] = ca

	cx := agent.NewCodexAgent()
	cx.MCPServerURL = mcpServer.URL()
	// Same async warmup as Claude above: pre-populate the model catalog
	// without letting a slow `codex app-server` spawn block startup.
	// 15s, not 5s: cold app-server initialize routinely exceeds 5s (see
	// "model/list failed: context deadline exceeded" in past startup logs).
	go func() {
		warmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cx.WarmupSupportedModels(warmCtx)
	}()
	allAgents["codex"] = cx

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

	hub := api.NewChatHub()
	// Resolve session → project → owner for the hub, so per-session events
	// only fan out to the owning user's WS connections. Without this, every
	// user's browser receives every other user's streaming text and "session
	// complete" notifications.
	hub.SessionOwnerLookup = func(sessionID string) int64 {
		sess, err := dataStore.GetSession(sessionID)
		if err != nil || sess == nil || sess.ProjectID == "" {
			return 0
		}
		proj, err := dataStore.GetProject(sess.ProjectID)
		if err != nil || proj == nil {
			return 0
		}
		return proj.OwnerUserID
	}

	termMgr := api.NewTerminalManager()
	mcpServer.SetTerminalManager(termMgr)

	mcpServer.SetChangeNotifier(hub.NotifyChange)

	startupLog.Printf("  Version: %s", Version)

	srv := &api.Server{
		Store:            dataStore,
		Config:           cfgMgr,
		Auth:             api.NewAuthenticator(),
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
		ListenPort:       *port,
		MarketplaceStats: api.NewMarketplaceStatsBuffer(),
		WeChat:           api.NewWeChatJSSDK(os.Getenv("WECHAT_APPID"), os.Getenv("WECHAT_APPSECRET")),
	}
	if srv.WeChat != nil {
		log.Printf("  WeChat JS-SDK share: enabled (appID from env)")
	}

	go api.CloneOfficialMarketplaceRepos(*dataDir, dataStore)

	mcpServer.SetSessionOrchestrator(srv)
	mcpServer.SetChromeEnabler(&chromeEnablerImpl{manager: chromeManager, store: dataStore})

	if err := dataStore.ResetStaleExecutions(); err != nil {
		log.Printf("  Warning: failed to reset stale executions: %v", err)
	} else {
		log.Printf("  Stale workflow executions reset.")
	}

	// Periodic cleanup: hard-delete trashed sessions (runs hourly).
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

	schedulerCtx, schedulerCancel := context.WithCancel(context.Background())
	defer schedulerCancel()
	go srv.StartWorkflowScheduler(schedulerCtx)

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
				<h1>Nexbox API Server</h1>
				<p>Frontend not embedded. Run <code>build.sh</code> to build with embedded frontend.</p>
				<p>API available at <a href="/api/projects">/api/projects</a></p>
			</body></html>`)
		}))
	}

	c := cors.New(cors.Options{
		AllowedOrigins:   []string{"http://localhost:5173", "http://localhost:3000", "http://127.0.0.1:5173"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
		AllowCredentials: true,
	})

	handler := c.Handler(router)

	addr := fmt.Sprintf("%s:%d", *listen, *port)
	httpServer := &http.Server{Addr: addr, Handler: handler}

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
	startupLog.Printf("Nexbox ready. Visit in browser: %s", appURL)

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		startupLog.Fatalf("Listen failed: %v", err)
	}
	log.Printf("Server stopped.")
}
