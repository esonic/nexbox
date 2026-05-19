package api

import (
	"log"
	"net/http"
	_ "net/http/pprof" // registers pprof handlers on DefaultServeMux
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/config"
	chromemcp "github.com/esonic/nexbox/mcp/chrome"
	"github.com/esonic/nexbox/store"
	"github.com/esonic/nexbox/updater"
)

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
	Updater          *updater.Updater         // self-update manager (nil in dev mode)
	Version          string                   // build-time version string
	ChromeManager    *chromemcp.ChromeManager // Per-session Chrome DevTools MCP manager for E2E testing
	ListenPort       int                      // actual TCP port the HTTP server is listening on (for OAuth callbacks, MCP proxy URLs)
	MarketplaceStats *MarketplaceStatsBuffer  // In-memory stats accumulator; flushed by pollChanges

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
		if ww.Status() >= 400 {
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

	r.Route("/api", func(r chi.Router) {
		// Sessions
		r.Route("/sessions", func(r chi.Router) {
			r.Get("/", s.ListSessions)
			r.Post("/", s.CreateSession)
			r.Put("/reorder", s.ReorderSessions)
			r.Get("/trash", s.ListTrashedSessions)
			r.Delete("/trash", s.EmptyTrash)
			r.Route("/{sessionId}", func(r chi.Router) {
				r.Get("/", s.GetSession)
				r.Put("/", s.UpdateSession)
				r.Delete("/", s.DeleteSession)
				r.Post("/restore", s.RestoreSession)
				r.Post("/fork", s.ForkSession)
				r.Post("/auto-title", s.AutoTitleSession)
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
				r.Get("/scratch-session", s.GetOrCreateScratchSession)

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
				r.Get("/files-flat", s.ListProjectFilesFlat)
				r.Get("/file-content", s.ReadProjectFile)
				r.Put("/file-content", s.SaveProjectFile)
				r.Delete("/file-content", s.DeleteProjectFile)
				r.Get("/file-search", s.SearchProjectFiles)
				r.Post("/file-upload", s.UploadProjectFile)
				r.Get("/file-download", s.DownloadProjectFile)
				r.Get("/file-serve", s.ServeProjectFile)
				r.Post("/file-move", s.MoveProjectFile)

				// GitLab
				r.Post("/create-mr", s.CreateMR)

				// Task migration (local DB → GitLab)
				r.Get("/migration-status", s.GetMigrationStatus)
				r.Post("/migrate-tasks", s.MigrateTasks)

				// Members
				r.Get("/members", s.ListProjectMembers)
				r.Post("/members", s.AddProjectMember)
				r.Delete("/members/me", s.LeaveProject)
				r.Delete("/members/{userName}", s.RemoveProjectMember)

				// Labels (project-scoped)
				r.Get("/labels", s.ListLabels)
				r.Post("/labels", s.CreateLabel)
				r.Delete("/labels/{labelId}", s.DeleteLabel)
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
				r.Put("/parent", s.SetTaskParent)
				r.Delete("/parent", s.RemoveTaskParent)
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

		r.Route("/projects/{projectId}/assets", func(r chi.Router) {
			r.Get("/", s.ListAssets)
			r.Post("/", s.CreateAsset)
		})
		r.Post("/assets/lark-fetch", s.FetchLarkDoc)
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
			r.Post("/lark-fetch", s.FetchLarkDoc)
			r.Post("/publish-to-lark", s.PublishLarkDoc)
r.Put("/lark-doc", s.SetLarkDoc)
		})
		r.Put("/comments/{commentId}", s.UpdateReviewComment)
		r.Delete("/comments/{commentId}", s.DeleteReviewComment)

		// Message part details (lazy loading)
		r.Get("/messages/{messageId}/details", s.GetMessageDetails)

		// Changes (audit trail)
		r.Get("/changes", s.ListChanges)

		// Members search (namespace-level fuzzy search)
		r.Get("/members", s.SearchMembers)

		// Utility
		r.Post("/validate-path", s.ValidatePath)
		r.Get("/browse-dir", s.BrowseDir)
		r.Get("/info", s.GetServerInfo)
		r.Get("/available-agents", s.ListAvailableAgents)
		r.Get("/available-models", s.ListAvailableModels)
		r.Put("/default-agent", s.SetDefaultAgent)
		r.Get("/settings", s.GetSettings)
		r.Put("/settings", s.UpdateSettings)

		// Self-update
		r.Get("/update/status", s.GetUpdateStatus)
		r.Post("/update/restart", s.TriggerRestart)

		// Auth (cloud login/logout)
		r.Get("/auth/login-url", s.GetAuthLoginURL)
		r.Get("/auth/callback", s.HandleAuthCallback)
		r.Post("/auth/logout", s.HandleAuthLogout)

		// Workflows
		s.RegisterWorkflowRoutes(r)

		// RALPH (parallel task pool)
		s.RegisterRalphRoutes(r)

		// Marketplace (MCP & Skill registry)
		s.RegisterMarketplaceRoutes(r)
	})

	// MCP reverse proxy (top-level so the URL is /mcp-proxy/{name}, not /api/mcp-proxy/{name})
	r.HandleFunc("/mcp-proxy/{name}/*", s.HandleMCPProxy)
	r.HandleFunc("/mcp-proxy/{name}", s.HandleMCPProxy)

	// pprof – goroutine dumps, heap profiles, etc. for debugging hangs.
	// Usage: curl http://localhost:9040/debug/pprof/goroutine?debug=1
	r.Mount("/debug", http.DefaultServeMux)

	// Global multiplexed WebSocket
	r.Get("/ws", s.HandleGlobalWS)

	// Terminal PTY WebSocket (session-scoped; must be registered before the legacy project route)
	r.Get("/ws/terminal/sessions/{sessionId}", s.HandleTerminalSessionWS)
	// Legacy project-scoped terminal (kept for backward compatibility)
	r.Get("/ws/terminal/{projectId}", s.HandleTerminalWS)

	return r
}
