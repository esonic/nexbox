package api

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/config"
	"github.com/esonic/nexbox/gitrepo"
	chromemcp "github.com/esonic/nexbox/mcp/chrome"
	"github.com/esonic/nexbox/store"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Server holds all API dependencies
type Server struct {
	Store             *store.SQLiteStore     // unified data access (always sqlite in local-only mode)
	Config            *config.Manager        // local config
	Auth              *Authenticator         // JWT signer + token parser
	Agent             agent.Agent            // Default agent (from config)
	AgentName         string                 // Default agent name
	Agents            map[string]agent.Agent // All available agents by name
	SessionManager    *agent.SessionManager
	MCPServerURL      string
	Hub               *ChatHub                 // Hub for session chat messages
	TerminalMgr       *TerminalManager         // tracks active PTY for run_in_terminal tool
	DetailCache       *store.DetailCache       // local cache for full message part details
	DataDir           string                   // data directory for file storage (images, etc.)
	Version           string                   // build-time version string
	ChromeManager     *chromemcp.ChromeManager // Per-session Chrome DevTools MCP manager for E2E testing
	ListenPort        int                      // actual TCP port the HTTP server is listening on
	MarketplaceStats  *MarketplaceStatsBuffer  // In-memory stats accumulator
	GitRepositories   *gitrepo.Resolver        // discovers and resolves repository scope within a workspace
	OnMCPToolsChanged func()                   // called after MCP tool category settings change
	WeChat            *WeChatJSSDK             // optional WeChat JS-SDK share signer (nil when unconfigured)

	mcpRuntimeTools sync.Map // installation identity -> immutable MCP tool aliases

	// humanGates: executionID+":"+nodeID → channel carrying user input for human node continuation
	humanGatesMu sync.Mutex
	humanGates   map[string]chan string

	// interjectQ serializes interjections per session in arrival order while
	// keeping them off the WS read loop (see interjectQueue).
	interjectQ interjectQueue
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
		// Public auth endpoints (RequireAuth lets these through via isPublicPath).
		r.Post("/login", s.HandleLogin)
		r.Post("/logout", s.HandleLogout)
		r.Get("/auth/me", s.HandleAuthMe)

		// Everything else under /api requires a valid JWT.
		r.Group(func(r chi.Router) {
			r.Use(s.RequireAuth)
			registerAuthedAPIRoutes(s, r)
		})
	})

	// MCP reverse proxy (top-level so the URL is /mcp-proxy/{name}). The MCP
	// auth path is its own scheme (MCP servers are launched as agent
	// subprocesses) so we do NOT gate this with RequireAuth.
	r.HandleFunc("/mcp-proxy/{name}/*", s.HandleMCPProxy)
	r.HandleFunc("/mcp-proxy/{name}", s.HandleMCPProxy)

	// App reverse proxy: forwards a path-prefixed route to a locally-running
	// side app (e.g. the portfolio tracker on 127.0.0.1:3001) so it is served
	// through the same public entrypoint. Outside RequireAuth — the proxied
	// app handles its own auth. See app_proxy.go.
	r.HandleFunc(appProxyPrefix(), s.HandleAppProxy)
	r.HandleFunc(appProxyPrefix()+"/*", s.HandleAppProxy)

	// Public read-only access to shared HTML files. Intentionally outside
	// the /api group and the RequireAuth middleware: anyone with the link
	// can view the file. Returns 404 if the share id is unknown or the
	// underlying file has been deleted.
	r.Get("/web_share/{id}", s.ServeWebShare)

	// Public WeChat JS-SDK config signer, used by shared pages to enable the
	// in-app "分享给朋友" rich card. Outside RequireAuth like /web_share.
	r.Get("/wx/js-config", s.HandleWeChatJSConfig)

	// WebSocket endpoints. They expect the JWT via either the cookie or an
	// `?auth=<token>` query parameter (cookies aren't reliable on WS upgrades
	// across all browsers). Auth is enforced inline in each handler.
	r.Get("/ws", s.HandleGlobalWS)
	r.Get("/ws/terminal/sessions/{sessionId}", s.HandleTerminalSessionWS)
	r.Get("/ws/terminal/{projectId}", s.HandleTerminalWS)

	return r
}

func registerAuthedAPIRoutes(s *Server, r chi.Router) {
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

			r.Put("/worktree-mode", s.SetWorktreeMode)
			r.Get("/worktree-status", s.GetWorktreeStatus)

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
			r.Get("/git-repositories", s.GetGitRepositories)
			r.Get("/git-workspace-status", s.GetGitWorkspaceStatus)
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
			r.Get("/git-file-head", s.GetProjectFileHead)

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
			r.Post("/web-shares", s.CreateWebShare)

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

	r.Route("/reactions/{targetType}/{targetId}", func(r chi.Router) {
		r.Get("/", s.GetReactions)
		r.Post("/", s.AddReaction)
		r.Delete("/", s.RemoveReaction)
	})

	r.Route("/projects/{projectId}/assets", func(r chi.Router) {
		r.Get("/", s.ListAssets)
		r.Post("/", s.CreateAsset)
	})
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

	r.Get("/messages/{messageId}/details", s.GetMessageDetails)
	r.Get("/changes", s.ListChanges)

	// Utility
	r.Post("/validate-path", s.ValidatePath)
	r.Get("/browse-dir", s.BrowseDir)
	r.Get("/info", s.GetServerInfo)
	r.Get("/available-agents", s.ListAvailableAgents)
	r.Get("/available-models", s.ListAvailableModels)
	r.Put("/default-agent", s.SetDefaultAgent)
	r.Get("/settings", s.GetSettings)
	r.Put("/settings", s.UpdateSettings)
	r.Get("/prompt-templates", s.ListPromptTemplates)
	r.Put("/prompt-templates", s.ReplacePromptTemplates)

	s.RegisterWorkflowRoutes(r)
	s.RegisterRalphRoutes(r)
	s.RegisterMarketplaceRoutes(r)
}
