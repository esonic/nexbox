package api

import (
	"log"
	"net/http"
	_ "net/http/pprof" // registers pprof handlers on DefaultServeMux
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"gitlab.okg.com/sre_ai/nexbox/agent"
	"gitlab.okg.com/sre_ai/nexbox/config"
	chromemcp "gitlab.okg.com/sre_ai/nexbox/mcp/chrome"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

// ---------------------------------------------------------------------------
// IP-based rate limiter (stdlib only, no external dependencies)
// ---------------------------------------------------------------------------

// ipRateLimiter tracks per-IP request counts within a sliding window.
type ipRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*rateBucket
	limit   int           // max requests per window
	window  time.Duration // window duration
}

type rateBucket struct {
	count    int
	windowAt time.Time // start of current window
}

func newIPRateLimiter(limit int, window time.Duration) *ipRateLimiter {
	rl := &ipRateLimiter{
		buckets: make(map[string]*rateBucket),
		limit:   limit,
		window:  window,
	}
	// Periodically clean up stale buckets to avoid unbounded memory growth.
	go func() {
		ticker := time.NewTicker(window * 10)
		defer ticker.Stop()
		for range ticker.C {
			rl.mu.Lock()
			cutoff := time.Now().Add(-rl.window)
			for ip, b := range rl.buckets {
				if b.windowAt.Before(cutoff) {
					delete(rl.buckets, ip)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

// allow returns true if the request from ip is within the rate limit.
func (rl *ipRateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	b, ok := rl.buckets[ip]
	if !ok || now.Sub(b.windowAt) >= rl.window {
		rl.buckets[ip] = &rateBucket{count: 1, windowAt: now}
		return true
	}
	if b.count >= rl.limit {
		return false
	}
	b.count++
	return true
}

// registerRateLimit returns a middleware that enforces the given rate limiter,
// extracting the client IP from RemoteAddr (best-effort; no proxy headers).
func registerRateLimit(rl *ipRateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := r.RemoteAddr
			// Strip port if present.
			if h, _, err := splitHostPort(ip); err == nil {
				ip = h
			}
			if !rl.allow(ip) {
				http.Error(w, "rate limit exceeded, try again later", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// splitHostPort is a minimal host:port splitter to avoid importing "net" for
// a trivial operation (net is already an indirect dep via stdlib, so this is
// fine; we just inline the split to keep imports clean).
func splitHostPort(addr string) (host, port string, err error) {
	// Walk backwards to find the last colon.
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i], addr[i+1:], nil
		}
	}
	return "", "", &net_addrErr{addr}
}

type net_addrErr struct{ addr string }

func (e *net_addrErr) Error() string { return "missing port in address: " + e.addr }

// Server holds all API dependencies
type Server struct {
	Store            store.Store            // unified data access (CompositeStore / SQLiteStore)
	LocalStore       *store.SQLiteStore     // always-on local store; used by ActivateCloud / DeactivateCloud
	Config           *config.Manager        // local config (default_agent, server_url, token, etc.)
	Agent            agent.Agent            // Default agent (from --agent flag)
	AgentName        string                 // Default agent name
	Agents           map[string]agent.Agent // All available agents by name
	SessionManager   *agent.SessionManager
	MCPServerURL     string
	Hub              *ChatHub                 // Hub for session chat messages
	StoreMode        string                   // "sqlite" or "remote"
	TerminalMgr      *TerminalManager         // tracks active PTY for run_in_terminal tool
	DetailCache      *store.DetailCache       // local cache for full message part details
	Cloud            *store.RemoteStore       // non-nil in cloud mode; for lark chat, change polling, etc.
	DataDir          string                   // data directory for file storage (images, etc.)
	Version          string                   // build-time version string
	ChromeManager    *chromemcp.ChromeManager // Per-session Chrome DevTools MCP manager for E2E testing
	MarketplaceStats *MarketplaceStatsBuffer  // In-memory stats accumulator; flushed by pollChanges
	ContainerMgr     *agent.ContainerManager  // Optional: Docker container isolation per project

	// OnCloudActivated is called by ActivateCloud to start cloud-mode goroutines
	// (e.g. pollChanges). Set by main during startup.
	OnCloudActivated func()
	// OnCloudDeactivated is called by DeactivateCloud to stop cloud-mode goroutines.
	OnCloudDeactivated func()

	// humanGates: executionID+":"+nodeID → channel carrying user input for human node continuation
	humanGatesMu sync.Mutex
	humanGates   map[string]chan string
}

// ResolveAgent returns the agent for a given session, falling back to the default.
func (s *Server) ResolveAgent(session *Session) (agent.Agent, string) {
	if session.Agent != "" {
		if a, ok := s.Agents[session.Agent]; ok {
			return a, session.Agent
		}
	}
	return s.Agent, s.AgentName
}

// errorOnlyLogger logs HTTP requests only when the response status is non-2xx.
func errorOnlyLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		if ww.Status() >= 500 {
			log.Printf("%q %s %d %dB %s",
				r.Method+" "+r.RequestURI, r.Proto, ww.Status(),
				ww.BytesWritten(), time.Since(start))
		}
	})
}

// NewRouter creates the Chi router with all API routes registered.
func NewRouter(s *Server) *chi.Mux {
	r := chi.NewRouter()

	r.Use(errorOnlyLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	// Rate limiter for the registration endpoint: max 5 requests per IP per minute.
	registerRL := newIPRateLimiter(5, time.Minute)

	r.Route("/api", func(r chi.Router) {
		// ── Public routes (no auth required) ──
		r.Get("/info", s.GetServerInfo)
		r.With(registerRateLimit(registerRL)).Post("/auth/register", s.HandleRegister)
		r.Post("/auth/login", s.HandleLogin)
		r.Post("/auth/send-verification", s.HandleSendVerification)

		// ── All routes requiring authentication ──
		r.Group(func(r chi.Router) {
			r.Use(s.AuthMiddleware)

			// Auth
			r.Get("/auth/me", s.HandleMe)
			r.Post("/auth/logout", s.HandleAuthLogout)

			// Balance
			r.Get("/balance", s.HandleGetBalance)

			// Admin only
			r.Group(func(r chi.Router) {
				r.Use(AdminMiddleware)
				r.Get("/admin/users", s.HandleAdminListUsers)
				r.Patch("/admin/users/{userID}/features", s.HandleAdminUpdateUserFeatures)
				r.Post("/admin/recharge", s.HandleAdminRecharge)
			})

			// Sessions
			r.Route("/sessions", func(r chi.Router) {
				r.Get("/", s.ListSessions)
				r.Post("/", s.CreateSession)
				r.Put("/reorder", s.ReorderSessions)
				r.Get("/trash", s.ListTrashedSessions)
				r.Route("/{sessionId}", func(r chi.Router) {
					r.Get("/", s.GetSession)
					r.Put("/", s.UpdateSession)
					r.Delete("/", s.DeleteSession)
					r.Post("/restore", s.RestoreSession)
					r.Get("/messages", s.ListSessionMessages)
					r.Post("/upload-image", s.UploadImage)
					r.Get("/image/{filename}", s.ServeImage)

					// Worktree
					r.Put("/worktree-mode", s.SetWorktreeMode)
					r.Get("/worktree-status", s.GetWorktreeStatus)

					// Message queue
					r.Get("/queue", s.ListQueuedMessages)
					r.Post("/queue", s.CreateQueuedMessage)
					r.Put("/queue/{queueId}", s.UpdateQueuedMessage)
					r.Delete("/queue/{queueId}", s.DeleteQueuedMessage)
					r.Post("/queue/{queueId}/run", s.HandleRunQueuedMessage)
				})
			})

			// Projects
			r.Route("/projects", func(r chi.Router) {
				r.Get("/", s.ListProjects)
				r.Post("/", s.CreateProject)
				r.Get("/lookup-by-dir", s.LookupProjectByDir)
				r.Get("/by-git-dir", s.CheckProjectByGitDir)
				r.Route("/{projectId}", func(r chi.Router) {
					r.Get("/", s.GetProject)
					r.Put("/", s.UpdateProject)
					r.Delete("/", s.DeleteProject)

					// Git operations (project-level)
					r.Get("/git-status", s.GetGitStatus)
					r.Post("/git-commit", s.GitCommit)
					r.Post("/git-discard", s.GitDiscard)
					r.Get("/git-log", s.GitLog)
					r.Get("/git-commit-files", s.GitCommitFiles)
					r.Get("/git-commit-diff", s.GitCommitDiff)
					r.Post("/generate-commit-message", s.GenerateCommitMessage)
					r.Get("/git-branches", s.GetGitBranches)
					r.Post("/git-checkout", s.GitCheckout)
					r.Post("/git-stage", s.GitStage)
					r.Post("/git-unstage", s.GitUnstage)
					r.Post("/git-push", s.GitPush)
					r.Post("/git-pull", s.GitPull)
					r.Get("/diff", s.GetProjectDiff)
					r.Get("/diff-stats", s.GetProjectDiffStats)
					r.Get("/file-diff", s.GetProjectFileDiff)

					// File browser
					r.Get("/files", s.ListProjectFiles)
					r.Get("/file-content", s.ReadProjectFile)
					r.Get("/file-search", s.SearchProjectFiles)
					r.Post("/files/upload", s.UploadProjectFile)
					r.Get("/files/download", s.DownloadProjectFile)

					// Labels (project-scoped)
					r.Get("/labels", s.ListLabels)
					r.Post("/labels", s.CreateLabel)
					r.Delete("/labels/{labelId}", s.DeleteLabel)

					// Assets under project
					r.Route("/assets", func(r chi.Router) {
						r.Get("/", s.ListAssets)
						r.Post("/", s.CreateAsset)
					})
				})
			})

			// Tasks
			r.Route("/tasks", func(r chi.Router) {
				r.Get("/", s.ListTasks)
				r.Post("/", s.CreateTask)
				r.Route("/{taskId}", func(r chi.Router) {
					r.Get("/", s.GetTask)
					r.Put("/", s.UpdateTask)
					r.Delete("/", s.DeleteTask)
					r.Post("/ref-count", s.IncrementTaskRefCount)
					r.Route("/assets", func(r chi.Router) {
						r.Get("/", s.ListTaskAssets)
						r.Post("/", s.LinkTaskAsset)
						r.Delete("/{assetId}", s.UnlinkTaskAsset)
					})
					r.Route("/comments", func(r chi.Router) {
						r.Get("/", s.ListTaskComments)
						r.Post("/", s.CreateTaskComment)
					})
					r.Route("/labels", func(r chi.Router) {
						r.Post("/", s.AddLabelToTask)
						r.Delete("/{labelName}", s.RemoveLabelFromTask)
					})
				})
			})
			r.Put("/task-comments/{commentId}", s.UpdateTaskComment)
			r.Delete("/task-comments/{commentId}", s.DeleteTaskComment)

			// Reactions (generic: task, task_comment)
			r.Route("/reactions/{targetType}/{targetId}", func(r chi.Router) {
				r.Get("/", s.GetReactions)
				r.Post("/", s.AddReaction)
				r.Delete("/", s.RemoveReaction)
			})

			// Assets direct access by ID
			r.Route("/assets/{assetId}", func(r chi.Router) {
				r.Get("/", s.GetAsset)
				r.Put("/", s.UpdateAsset)
				r.Delete("/", s.DeleteAsset)
				r.Get("/versions", s.ListAssetVersions)
				r.Get("/diff", s.DiffAssetVersions)
				r.Route("/comments", func(r chi.Router) {
					r.Get("/", s.ListReviewComments)
					r.Post("/", s.CreateReviewComment)
				})
			})
			r.Put("/comments/{commentId}", s.UpdateReviewComment)
			r.Delete("/comments/{commentId}", s.DeleteReviewComment)

			// Message part details (lazy loading)
			r.Get("/messages/{messageId}/details", s.GetMessageDetails)

			// Changes (audit trail)
			r.Get("/changes", s.ListChanges)

			// Utility
			r.Post("/validate-path", s.ValidatePath)
			r.Get("/browse-dir", s.BrowseDir)
			r.Get("/available-agents", s.ListAvailableAgents)
			r.Put("/default-agent", s.SetDefaultAgent)
			r.Get("/settings", s.GetSettings)
			r.Put("/settings", s.UpdateSettings)

			// Workflows
			s.RegisterWorkflowRoutes(r)

			// Marketplace (MCP & Skill registry)
			s.RegisterMarketplaceRoutes(r)
		})
	})

	// pprof – goroutine dumps, heap profiles, etc.
	// Protected: admin role required.
	// Usage: curl -H "Authorization: Bearer <admin-token>" http://localhost:9040/debug/pprof/goroutine?debug=1
	r.Group(func(r chi.Router) {
		r.Use(s.AuthMiddleware)
		r.Use(AdminMiddleware)
		r.Mount("/debug", http.DefaultServeMux)
	})

	// WebSocket routes — authenticated via JWT in Authorization header or
	// ?token=<jwt> query parameter (browsers cannot set headers on WS upgrades).
	r.Group(func(r chi.Router) {
		r.Use(s.WSAuthMiddleware)

		// Global multiplexed WebSocket
		r.Get("/ws", s.HandleGlobalWS)

		// Terminal PTY WebSocket (session-scoped; must be registered before the legacy project route)
		r.Get("/ws/terminal/sessions/{sessionId}", s.HandleTerminalSessionWS)
		// Legacy project-scoped terminal (kept for backward compatibility)
		r.Get("/ws/terminal/{projectId}", s.HandleTerminalWS)
	})

	return r
}

// CORSOrigins returns the list of allowed CORS origins from config, falling
// back to local-dev defaults when the config field is empty.
func (s *Server) CORSOrigins() []string {
	origins := s.Config.Get().CORSOrigins
	if len(origins) == 0 {
		return []string{
			"http://localhost:5173",
			"http://localhost:3000",
			"http://127.0.0.1:5173",
		}
	}
	return origins
}
