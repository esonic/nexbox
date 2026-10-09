package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/esonic/nexbox/store"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// TerminalWriter abstracts writing commands to a session's terminal.
type TerminalWriter interface {
	// WriteCommand sends a command to the terminal for the given session.
	WriteCommand(sessionID, command string) error
	// WaitReady blocks until a terminal for the session is active or the context expires.
	WaitReady(ctx context.Context, sessionID string) error
}

// ConfigGetter abstracts reading local config values.
type ConfigGetter interface {
	ProjectIDs() []string
	// GitRepoPath returns the local git repo path for a project (by remote ID).
	// Returns empty string if not found.
	GitRepoPath(projectID string) string
}

// MCP tool categories that can be toggled in Settings. Categories are opt-in.
const MCPToolCategoryTaskManagement = "task_management"

var mcpToolCategories = map[string]string{
	"create_task": MCPToolCategoryTaskManagement,
	"get_task":    MCPToolCategoryTaskManagement,
	"list_tasks":  MCPToolCategoryTaskManagement,
	"update_task": MCPToolCategoryTaskManagement,
}

// CategoryChecker reports whether a configurable tool category is enabled.
type CategoryChecker func(category string) bool

// ChangeNotifier is called when MCP tools modify data (assets, tasks, etc.)
// so the main server can broadcast updates to connected browsers.
type ChangeNotifier func(entityType, entityID, action string)

// SessionOrchestrator allows the MCP server to create and manage agent sessions
// for workflow execution. Implemented by the api.Server.
type SessionOrchestrator interface {
	OrchestrateCreateSession(name, projectID, agent string) (sessionID string, err error)
	OrchestrateSendMessage(sessionID, message string) error
	OrchestrateQueueMessage(sessionID, message string) error
	OrchestrateWaitDone(ctx context.Context, sessionID string) (output string, err error)
	OrchestrateResolveSession(fromSessionID, ref string) (id, name string, err error)
	OrchestrateSendAndWait(ctx context.Context, fromSessionID, targetSessionID, message string) (string, error)
}

// ChromeEnabler allows the MCP server to enable per-session browser control.
type ChromeEnabler interface {
	// EnableChrome adds chrome_devtools to the session's features and starts
	// the Chrome MCP server for that session. Returns the Chrome MCP URL and
	// tool names on success.
	EnableChrome(sessionID string) (mcpURL string, toolNames []string, err error)
}

// nexboxSessionKey is the context key used to carry the nexbox session ID
// through MCP HTTP requests.
type nexboxSessionKey struct{}

// Server wraps the mcp-go MCP server with NexBox-specific tools
type Server struct {
	mcpServer       *server.MCPServer
	httpServer      *server.StreamableHTTPServer
	store           store.Store
	addr            string
	actualAddr      string
	listener        net.Listener
	httpSrv         *http.Server
	toolNames       []string
	terminalMgr     TerminalWriter
	configGetter    ConfigGetter
	categoryEnabled CategoryChecker
	changeNotifier  ChangeNotifier
	orchestrator    SessionOrchestrator
	chromeEnabler   ChromeEnabler
}

// NewServer creates a new MCP server with asset management tools
func NewServer(s store.Store, addr string) *Server {
	srv := &Server{
		store: s,
		addr:  addr,
	}

	// Create MCP server
	srv.mcpServer = server.NewMCPServer(
		"Nexbox Asset Manager",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithToolFilter(srv.filterTools),
		server.WithRecovery(),
	)

	// Register tools
	srv.registerTools()

	// Create HTTP transport
	srv.httpServer = server.NewStreamableHTTPServer(srv.mcpServer)

	return srv
}

// Listen binds to the configured address and resolves the actual port.
// Use addr "127.0.0.1:0" for automatic port assignment.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("mcp listen on %s: %w", s.addr, err)
	}
	s.listener = ln
	s.actualAddr = ln.Addr().String()
	log.Printf("[mcp] Bound to %s", s.actualAddr)
	return nil
}

// Serve starts serving MCP requests on the listener from Listen(). Blocks.
func (s *Server) Serve() error {
	if s.listener == nil {
		return fmt.Errorf("mcp: call Listen() before Serve()")
	}
	mux := http.NewServeMux()
	// Wrap the MCP handler to inject the nexbox session ID (passed via ?session=
	// query param by the agent) into the request context so tool handlers can
	// access it without requiring the AI to pass it explicitly.
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sid := r.URL.Query().Get("session"); sid != "" {
			r = r.WithContext(context.WithValue(r.Context(), nexboxSessionKey{}, sid))
		}
		s.httpServer.ServeHTTP(w, r)
	}))
	s.httpSrv = &http.Server{Handler: mux}
	return s.httpSrv.Serve(s.listener)
}

// Start is a convenience that calls Listen then Serve (blocking).
func (s *Server) Start() error {
	if err := s.Listen(); err != nil {
		return err
	}
	return s.Serve()
}

// Shutdown gracefully shuts down the MCP HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpSrv != nil {
		return s.httpSrv.Shutdown(ctx)
	}
	return nil
}

// SetTerminalManager sets the terminal manager for the run_in_terminal tool.
func (s *Server) SetTerminalManager(tm TerminalWriter) {
	s.terminalMgr = tm
}

// SetConfigGetter sets the config getter for reading local settings (e.g. GitLab token).
func (s *Server) SetConfigGetter(cg ConfigGetter) {
	s.configGetter = cg
}

// SetCategoryChecker installs the callback that decides whether a configurable
// tool category is exposed. With no checker, configurable tools are hidden.
func (s *Server) SetCategoryChecker(fn CategoryChecker) {
	s.categoryEnabled = fn
}

// NotifyToolListChanged tells connected MCP clients to refresh their tool list
// after a category setting changes.
func (s *Server) NotifyToolListChanged() {
	s.mcpServer.SendNotificationToAllClients(gomcp.MethodNotificationToolsListChanged, nil)
}

func (s *Server) toolEnabled(name string) bool {
	category, configurable := mcpToolCategories[name]
	if !configurable {
		return true
	}
	return s.categoryEnabled != nil && s.categoryEnabled(category)
}

func (s *Server) filterTools(_ context.Context, tools []gomcp.Tool) []gomcp.Tool {
	filtered := make([]gomcp.Tool, 0, len(tools))
	for _, tool := range tools {
		if s.toolEnabled(tool.Name) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

// SetChangeNotifier sets the callback for broadcasting data changes to browsers.
func (s *Server) SetChangeNotifier(fn ChangeNotifier) {
	s.changeNotifier = fn
}

// SetSessionOrchestrator sets the session orchestrator for workflow execution.
func (s *Server) SetSessionOrchestrator(orch SessionOrchestrator) {
	s.orchestrator = orch
}

// SetStore updates the store used by MCP tool handlers.
// Called when cloud mode is activated or deactivated so that asset/task/project
// tools use the correct (remote or local) store without requiring a restart.
func (s *Server) SetStore(st store.Store) {
	s.store = st
}

// SetChromeEnabler sets the chrome enabler for the enable_browser_control tool.
func (s *Server) SetChromeEnabler(ce ChromeEnabler) {
	s.chromeEnabler = ce
}

func (s *Server) notifyChange(entityType, entityID, action string) {
	if s.changeNotifier != nil {
		s.changeNotifier(entityType, entityID, action)
	}
}

// Addr returns the configured listen address
func (s *Server) Addr() string {
	return s.addr
}

// URL returns the full MCP endpoint URL suitable for client connections.
// After Listen(), this returns the actual bound address (important for port 0).
func (s *Server) URL() string {
	host := s.actualAddr
	if host == "" {
		host = s.addr
	}
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	} else if strings.HasPrefix(host, "0.0.0.0:") {
		host = "127.0.0.1" + host[len("0.0.0.0"):]
	}
	return fmt.Sprintf("http://%s/mcp", host)
}

// ToolNames returns the registered tool names with the "mcp__nexbox__" prefix,
// suitable for passing directly to Claude's allowed_tools list.
func (s *Server) ToolNames() []string {
	out := make([]string, len(s.toolNames))
	copy(out, s.toolNames)
	return out
}

func (s *Server) registerTools() {
	// save_asset: Create or update an asset in a project (disabled)
	/*
		saveAssetTool := gomcp.NewTool("save_asset",
			gomcp.WithDescription(
				"Save a document/asset to the Nexbox platform. "+
					"If an asset with the same title already exists in the project, "+
					"a new version is created. Otherwise, a new asset is created. "+
					"Use this tool whenever you produce a document such as a requirement doc, "+
					"design doc, test case, or any written artifact that should be persisted."),
			gomcp.WithString("project_id",
				gomcp.Required(),
				gomcp.Description("The project ID to save the asset to"),
			),
			gomcp.WithString("type",
				gomcp.Required(),
				gomcp.Description("Asset type: requirement_doc, design_doc, code_change, unit_test, or test_case"),
			),
			gomcp.WithString("title",
				gomcp.Required(),
				gomcp.Description("Title of the asset"),
			),
			gomcp.WithString("content",
				gomcp.Required(),
				gomcp.Description("Full content of the asset (Markdown supported)"),
			),
		)
	*/

	// get_asset_content: Retrieve the latest content of an asset by ID (disabled)
	/*
		getAssetContentTool := gomcp.NewTool("get_asset_content",
			gomcp.WithDescription(
				"Retrieve the latest content of a specific asset by its ID. "+
					"Use this when you need to read or reference an existing document."),
			gomcp.WithString("asset_id",
				gomcp.Required(),
				gomcp.Description("The asset ID"),
			),
		)
	*/

	// list_assets: List assets in a project (top 20, most recent first) (disabled)
	/*
		listAssetsTool := gomcp.NewTool("list_assets",
			gomcp.WithDescription(
				"List assets/documents in a project. "+
					"Returns top 20 assets sorted by most recently updated. "+
					"Returns asset metadata (id, type, title, version, status) without content."),
			gomcp.WithString("project_id",
				gomcp.Required(),
				gomcp.Description("The project ID"),
			),
		)
	*/

	// list_asset_comments: List all review comments for an asset (disabled)
	/*
		listAssetCommentsTool := gomcp.NewTool("list_asset_comments",
			gomcp.WithDescription(
				"List all review comments for a specific asset. "+
					"Review comments may include line numbers for inline comments or be general comments. "+
					"Use this tool to see user feedback on a document and address their concerns."),
			gomcp.WithString("asset_id",
				gomcp.Required(),
				gomcp.Description("The asset ID to get comments for"),
			),
		)
	*/

	// ask_question: Present questions to the user and wait for their response
	askQuestionTool := gomcp.NewTool("ask_question",
		gomcp.WithDescription(
			"Present one or more multiple-choice questions to the user via an interactive UI. "+
				"Use this tool when you need clarification or user decisions before proceeding. "+
				"IMPORTANT: After calling this tool, you MUST stop and wait for the user's response "+
				"in the next message. Do NOT continue working until you receive the answers."),
		gomcp.WithString("title",
			gomcp.Description("Optional title for the question form"),
		),
		gomcp.WithString("questions",
			gomcp.Required(),
			gomcp.Description(
				"JSON array of question objects. Each question has: "+
					"id (string, unique identifier), "+
					"prompt (string, the question text), "+
					"options (array of {id, label} with at least 2 items), "+
					"allow_multiple (boolean, optional, default false for single-select). "+
					"Example: [{\"id\":\"q1\",\"prompt\":\"Which approach?\",\"options\":[{\"id\":\"a\",\"label\":\"Option A\"},{\"id\":\"b\",\"label\":\"Option B\"}]}]"),
		),
	)

	// create_task: Create a new task
	createTaskTool := gomcp.NewTool("create_task",
		gomcp.WithDescription(
			"Create a new task (like a Jira ticket) in Nexbox. "+
				"Use this when the user asks to create a task, track a piece of work. "+
				"If you know the current project_id (from Nexbox context), pass it; otherwise omit it."),
		gomcp.WithString("project_id",
			gomcp.Description("Optional project ID to associate the task with"),
		),
		gomcp.WithString("title",
			gomcp.Required(),
			gomcp.Description("Title of the task"),
		),
		gomcp.WithString("description",
			gomcp.Description("Detailed description of the task (Markdown supported)"),
		),
		gomcp.WithString("priority",
			gomcp.Description("Priority: low, medium, high, or critical. Defaults to medium."),
		),
		gomcp.WithString("assignee",
			gomcp.Description("Person assigned to this task"),
		),
		gomcp.WithString("parent_id",
			gomcp.Description("Parent task ID to create this as a subtask"),
		),
	)

	// get_task: Get full details of a task by ID
	getTaskTool := gomcp.NewTool("get_task",
		gomcp.WithDescription(
			"Retrieve full details of a specific task by its ID. "+
				"Returns the complete task including its full description. "+
				"Use this when you need to read the task details before implementing."),
		gomcp.WithString("task_id",
			gomcp.Required(),
			gomcp.Description("The task ID"),
		),
	)

	// list_tasks: List tasks (top 20, most recent first)
	listTasksTool := gomcp.NewTool("list_tasks",
		gomcp.WithDescription(
			"List tasks in Nexbox. Returns top 20 tasks sorted by most recently updated. "+
				"Returns task metadata (id, title, status, priority, assignee). "+
				"Use this to see existing tasks or find tasks to reference."),
		gomcp.WithString("status",
			gomcp.Description("Optional status filter: open, closed"),
		),
		gomcp.WithString("project_id",
			gomcp.Description("Optional project ID to filter tasks belonging to a specific project"),
		),
	)

	updateTaskTool := gomcp.NewTool("update_task",
		gomcp.WithDescription(
			"Update an existing task. You can update title, description, status, priority, and assignee. "+
				"Only provided fields are updated; omitted fields remain unchanged."),
		gomcp.WithString("task_id",
			gomcp.Required(),
			gomcp.Description("The task ID"),
		),
		gomcp.WithString("title",
			gomcp.Description("New title for the task"),
		),
		gomcp.WithString("description",
			gomcp.Description("New description for the task (Markdown supported)"),
		),
		gomcp.WithString("status",
			gomcp.Description("New status: open or closed"),
		),
		gomcp.WithString("priority",
			gomcp.Description("New priority: low, medium, high, or critical"),
		),
		gomcp.WithString("assignee",
			gomcp.Description("New assignee for the task"),
		),
	)

	// run_in_terminal (disabled)
	/*
		runInTerminalTool := gomcp.NewTool("run_in_terminal",
			gomcp.WithDescription(
				"Send a command to the integrated terminal for execution. "+
					"The command is sent asynchronously — like pasting it into the terminal. "+
					"Output is NOT captured; the user will see the output in their terminal panel."),
			gomcp.WithString("command",
				gomcp.Required(),
				gomcp.Description("The shell command to execute in the terminal"),
			),
		)
	*/

	addTool := func(tool gomcp.Tool, handler server.ToolHandlerFunc) {
		name := tool.Name
		wrapped := func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
			if !s.toolEnabled(name) {
				return errorResult(fmt.Sprintf("%s MCP tools are disabled in Settings", mcpToolCategories[name])), nil
			}
			args := req.GetArguments()
			if len(args) > 0 {
				sanitized := make(map[string]interface{}, len(args))
				for k, v := range args {
					if s, ok := v.(string); ok && len(s) > 200 {
						sanitized[k] = s[:200] + "...(truncated)"
					} else {
						sanitized[k] = v
					}
				}
				if b, err := json.Marshal(sanitized); err == nil {
					log.Printf("[mcp] >> %s args=%s", name, string(b))
				}
			} else {
				log.Printf("[mcp] >> %s (no args)", name)
			}
			return handler(ctx, req)
		}
		s.mcpServer.AddTool(tool, wrapped)
		s.toolNames = append(s.toolNames, "mcp__nexbox__"+name)
	}

	// --- Browser control tool ---

	enableBrowserControlTool := gomcp.NewTool("enable_browser_control",
		gomcp.WithDescription(
			"Enable Chrome browser control for the current session. "+
				"Call this tool only when browser control is needed and mcp__chrome__ tools are absent from your current tool list. "+
				"Newly enabled tools become available in the next message if they are absent from the current turn."),
	)

	// --- Cross-session messaging ---

	sendToSessionTool := gomcp.NewTool("send_to_session",
		gomcp.WithDescription(
			"Send a message to the AI in ANOTHER NexBox session and wait for its answer.\n"+
				"The message appears in that session exactly as if its user had typed it. This tool "+
				"blocks until that session's AI finishes replying and returns its final visible answer. "+
				"Use the short NexBox ID badge beside the session title or the full session UUID; "+
				"do not guess an ID — ask the user for it. If the target is busy, the message waits "+
				"until its current turn finishes."),
		gomcp.WithString("session_id",
			gomcp.Required(),
			gomcp.Description("Target session's full UUID or short NexBox ID badge."),
		),
		gomcp.WithString("message",
			gomcp.Required(),
			gomcp.Description("Self-contained message delivered verbatim to the other session."),
		),
	)

	// --- Visualize tools ---

	visualizeReadMeTool := gomcp.NewTool("visualize_read_me",
		gomcp.WithDescription(
			"Returns required context for visualize_show_widget (CSS variables, colors, typography, layout rules, examples). "+
				"Call once before your first visualize_show_widget call. "+
				"Do NOT mention or narrate this call to the user — it is an internal setup step. "+
				"Call it silently and proceed directly to the visualization in your response."),
		gomcp.WithArray("modules",
			gomcp.Description("Which module(s) to load. Pick all that fit: diagram, mockup, interactive, data_viz, art, chart"),
		),
	)

	visualizeShowWidgetTool := gomcp.NewTool("visualize_show_widget",
		gomcp.WithDescription(
			"Show visual content — SVG graphics, diagrams, charts, or interactive HTML widgets — that renders inline alongside your text response.\n"+
				"Use for flowcharts, architecture diagrams, dashboards, forms, calculators, data tables, games, illustrations, or any visual content.\n"+
				"The code is auto-detected: starts with <svg = SVG mode, otherwise HTML mode.\n"+
				"A global sendPrompt(text) function is available — it sends a message to chat as if the user typed it.\n"+
				"IMPORTANT: Call visualize_read_me once before your first visualize_show_widget call, then set 'i_have_seen_read_me: true'. "+
				"Do NOT narrate or mention the visualize_read_me call to the user — call it silently, then respond as if you went straight to building the visualization.\n\n"+
				"This tool renders an interactive UI in the chat. Prefer it over text output when displaying data from other tools."),
		gomcp.WithBoolean("i_have_seen_read_me",
			gomcp.Required(),
			gomcp.Description("Confirm whether you have already called visualize_read_me in this conversation."),
		),
		gomcp.WithArray("loading_messages",
			gomcp.Required(),
			gomcp.Description("1–4 loading messages shown to the user while the visual renders, each roughly 5 words long. Write them in the same language the user is using."),
		),
		gomcp.WithString("title",
			gomcp.Required(),
			gomcp.Description("Short snake_case identifier for this visual. Must be specific and disambiguating."),
		),
		gomcp.WithString("widget_code",
			gomcp.Required(),
			gomcp.Description("SVG or HTML code to render. For SVG: raw SVG code starting with <svg> tag. For HTML: raw HTML content, do NOT include DOCTYPE, <html>, <head>, or <body> tags. Use CSS variables for theming. Keep background transparent."),
		),
	)

	// addTool(saveAssetTool, s.handleSaveAsset) // disabled
	// addTool(getAssetContentTool, s.handleGetAssetContent) // disabled
	// addTool(listAssetsTool, s.handleListAssets) // disabled
	// addTool(listAssetCommentsTool, s.handleListAssetComments) // disabled
	addTool(askQuestionTool, s.handleAskQuestion)
	addTool(createTaskTool, s.handleCreateTask)
	addTool(getTaskTool, s.handleGetTask)
	addTool(listTasksTool, s.handleListTasks)
	addTool(updateTaskTool, s.handleUpdateTask)
	// addTool(runInTerminalTool, s.handleRunInTerminal) // disabled
	addTool(enableBrowserControlTool, s.handleEnableBrowserControl)
	addTool(sendToSessionTool, s.handleSendToSession)
	addTool(visualizeReadMeTool, s.handleVisualizeReadMe)
	addTool(visualizeShowWidgetTool, s.handleVisualizeShowWidget)
}

func (s *Server) handleSaveAsset(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()

	projectID, _ := args["project_id"].(string)
	assetType, _ := args["type"].(string)
	title, _ := args["title"].(string)
	content, _ := args["content"].(string)

	if projectID == "" || title == "" || content == "" {
		return errorResult("project_id, title, and content are required"), nil
	}

	if s.configGetter != nil {
		ids := s.configGetter.ProjectIDs()
		if len(ids) > 0 {
			found := false
			for _, id := range ids {
				if id == projectID {
					found = true
					break
				}
			}
			if !found {
				return errorResult(fmt.Sprintf("project_id %q not found", projectID)), nil
			}
		}
	}

	// Validate asset type
	validTypes := map[string]bool{
		"requirement_doc": true,
		"design_doc":      true,
		"code_change":     true,
		"unit_test":       true,
		"test_case":       true,
	}
	if !validTypes[assetType] {
		return errorResult(fmt.Sprintf("invalid asset type %q; must be one of: requirement_doc, design_doc, code_change, unit_test, test_case", assetType)), nil
	}

	// Check if asset with same title exists in this project
	existing, err := s.store.FindAssetByTitle(projectID, title)
	if err != nil {
		log.Printf("[mcp] FindAssetByTitle error: %v", err)
		return errorResult(fmt.Sprintf("database error: %v", err)), nil
	}

	if existing != nil {
		updated, err := s.store.UpdateAsset(existing.ID, store.UpdateAssetRequest{
			Content: &content,
		})
		if err != nil {
			log.Printf("[mcp] UpdateAsset error: %v", err)
			return errorResult(fmt.Sprintf("failed to update asset: %v", err)), nil
		}
		log.Printf("[mcp] Updated asset %q (id=%s) to v%d", title, updated.ID, updated.CurrentVersion)
		s.notifyChange("asset", updated.ID, "updated")
		return textResult(fmt.Sprintf("Asset updated successfully.\nID: %s\nTitle: %s\nVersion: %d\nType: %s",
			updated.ID, updated.Title, updated.CurrentVersion, updated.Type)), nil
	}

	asset, err := s.store.CreateAsset(projectID, store.CreateAssetRequest{
		Type:    assetType,
		Title:   title,
		Content: content,
	})
	if err != nil {
		log.Printf("[mcp] CreateAsset error: %v", err)
		return errorResult(fmt.Sprintf("failed to create asset: %v", err)), nil
	}

	log.Printf("[mcp] Created asset %q (id=%s) in project %s", title, asset.ID, projectID)
	s.notifyChange("asset", asset.ID, "created")
	return textResult(fmt.Sprintf("Asset created successfully.\nID: %s\nTitle: %s\nVersion: 1\nType: %s",
		asset.ID, asset.Title, asset.Type)), nil
}

func (s *Server) handleGetAssetContent(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()

	assetID, _ := args["asset_id"].(string)
	if assetID == "" {
		return errorResult("asset_id is required"), nil
	}

	content, err := s.store.GetLatestAssetContent(assetID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get content: %v", err)), nil
	}
	if content == "" {
		return errorResult(fmt.Sprintf("asset %s not found or has no content", assetID)), nil
	}

	return textResult(content), nil
}

func (s *Server) handleListAssets(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()

	projectID, _ := args["project_id"].(string)
	if projectID == "" {
		return errorResult("project_id is required"), nil
	}

	assets, err := s.store.ListAssets(projectID, 20, 0, "")
	if err != nil {
		return errorResult(fmt.Sprintf("database error: %v", err)), nil
	}

	if len(assets) == 0 {
		return textResult("No assets found in this project."), nil
	}

	type assetInfo struct {
		ID        string `json:"id"`
		ProjectID string `json:"project_id"`
		Type      string `json:"type"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		CreatedBy string `json:"created_by,omitempty"`
		UpdatedAt string `json:"updated_at"`
	}

	items := make([]assetInfo, 0, len(assets))
	for _, a := range assets {
		items = append(items, assetInfo{
			ID:        a.ID,
			ProjectID: a.ProjectID,
			Type:      a.Type,
			Title:     a.Title,
			Status:    a.Status,
			CreatedBy: a.CreatedBy,
			UpdatedAt: a.UpdatedAt.Format(time.RFC3339),
		})
	}

	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return errorResult(fmt.Sprintf("failed to marshal assets: %v", err)), nil
	}

	return textResult(string(data)), nil
}

func (s *Server) handleListAssetComments(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()

	assetID, _ := args["asset_id"].(string)
	if assetID == "" {
		return errorResult("asset_id is required"), nil
	}

	comments, err := s.store.ListReviewComments(assetID)
	if err != nil {
		return errorResult(fmt.Sprintf("database error: %v", err)), nil
	}

	// Filter to only open comments
	type commentInfo struct {
		ID         string `json:"id"`
		LineNumber *int   `json:"line_number,omitempty"`
		Content    string `json:"content"`
		Author     string `json:"author"`
		CreatedAt  string `json:"created_at"`
	}

	var items []commentInfo
	for _, c := range comments {
		if c.Status == "open" {
			items = append(items, commentInfo{
				ID:         c.ID,
				LineNumber: c.LineNumber,
				Content:    c.Content,
				Author:     c.Author,
				CreatedAt:  c.CreatedAt.Format("2006-01-02 15:04:05"),
			})
		}
	}

	if len(items) == 0 {
		return textResult("No open review comments found for this asset. All comments have been resolved."), nil
	}

	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return errorResult(fmt.Sprintf("failed to marshal comments: %v", err)), nil
	}

	return textResult(string(data)), nil
}

// Question/option structures for validation
type questionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type questionDef struct {
	ID            string           `json:"id"`
	Prompt        string           `json:"prompt"`
	Options       []questionOption `json:"options"`
	AllowMultiple bool             `json:"allow_multiple"`
}

func (s *Server) handleAskQuestion(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()

	questionsJSON, _ := args["questions"].(string)
	if questionsJSON == "" {
		return errorResult("questions parameter is required"), nil
	}

	// Validate the questions JSON
	var questions []questionDef
	if err := json.Unmarshal([]byte(questionsJSON), &questions); err != nil {
		return errorResult(fmt.Sprintf("invalid questions JSON: %v", err)), nil
	}

	if len(questions) == 0 {
		return errorResult("questions array must contain at least one question"), nil
	}

	for i, q := range questions {
		if q.ID == "" {
			return errorResult(fmt.Sprintf("question[%d]: id is required", i)), nil
		}
		if q.Prompt == "" {
			return errorResult(fmt.Sprintf("question[%d]: prompt is required", i)), nil
		}
		if len(q.Options) < 2 {
			return errorResult(fmt.Sprintf("question[%d]: at least 2 options are required", i)), nil
		}
		for j, opt := range q.Options {
			if opt.ID == "" || opt.Label == "" {
				return errorResult(fmt.Sprintf("question[%d].options[%d]: both id and label are required", i, j)), nil
			}
		}
	}

	title, _ := args["title"].(string)

	// Build a summary for the tool result
	var sb strings.Builder
	sb.WriteString("Questions have been presented to the user.\n")
	if title != "" {
		sb.WriteString(fmt.Sprintf("Title: %s\n", title))
	}
	sb.WriteString(fmt.Sprintf("Number of questions: %d\n", len(questions)))
	for _, q := range questions {
		sb.WriteString(fmt.Sprintf("- [%s] %s (%d options", q.ID, q.Prompt, len(q.Options)))
		if q.AllowMultiple {
			sb.WriteString(", multi-select")
		}
		sb.WriteString(")\n")
	}
	sb.WriteString("\nSTOP HERE. Wait for the user's response in the next message before continuing.")

	log.Printf("[mcp] ask_question: presented %d questions to user", len(questions))
	return textResult(sb.String()), nil
}

func (s *Server) handleCreateTask(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()

	projectID, _ := args["project_id"].(string)
	title, _ := args["title"].(string)
	description, _ := args["description"].(string)
	priority, _ := args["priority"].(string)
	assigner, _ := args["assignee"].(string)
	parentID, _ := args["parent_id"].(string)

	if title == "" {
		return errorResult("title is required"), nil
	}

	// Validate priority
	if priority == "" {
		priority = "medium"
	}
	validPriorities := map[string]bool{"low": true, "medium": true, "high": true, "critical": true}
	if !validPriorities[priority] {
		return errorResult(fmt.Sprintf("invalid priority %q; must be one of: low, medium, high, critical", priority)), nil
	}

	// Local store — validate parent hierarchy (max 1 level)
	if parentID != "" {
		parent, err := s.store.GetTask(parentID)
		if err != nil || parent == nil {
			return errorResult("parent task not found"), nil
		}
		if parent.ParentID != "" {
			return errorResult("cannot create sub-task under another sub-task (max 1 level)"), nil
		}
	}

	task, err := s.store.CreateTask(store.CreateTaskRequest{
		ProjectID:   projectID,
		ParentID:    parentID,
		Title:       title,
		Description: description,
		Priority:    priority,
		Assigner:    assigner,
	})
	if err != nil {
		log.Printf("[mcp] CreateTask error: %v", err)
		return errorResult(fmt.Sprintf("failed to create task: %v", err)), nil
	}

	s.notifyChange("task", task.ID, "created")

	var rb strings.Builder
	rb.WriteString("Task created successfully:\n")
	rb.WriteString(fmt.Sprintf("- ID: %s\n- Title: %s\n- Status: %s\n- Priority: %s", task.ID, task.Title, task.Status, task.Priority))
	if task.Assigner != "" {
		rb.WriteString(fmt.Sprintf("\n- Assignee: %s", task.Assigner))
	}
	if task.ProjectID != "" {
		rb.WriteString(fmt.Sprintf("\n- Project ID: %s", task.ProjectID))
	}
	if task.ParentID != "" {
		rb.WriteString(fmt.Sprintf("\n- Parent ID: %s", task.ParentID))
	}
	result := rb.String()
	log.Printf("[mcp] create_task: created %q (id=%s)", task.Title, task.ID)
	return textResult(result), nil
}

func (s *Server) handleGetTask(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()

	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return errorResult("task_id is required"), nil
	}

	task, err := s.store.GetTask(taskID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get task: %v", err)), nil
	}
	if task == nil {
		return errorResult(fmt.Sprintf("task %s not found", taskID)), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("ID: %s\n", task.ID))
	sb.WriteString(fmt.Sprintf("Title: %s\n", task.Title))
	sb.WriteString(fmt.Sprintf("Status: %s\n", task.Status))
	sb.WriteString(fmt.Sprintf("Priority: %s\n", task.Priority))
	if task.ParentID != "" {
		sb.WriteString(fmt.Sprintf("Parent ID: %s\n", task.ParentID))
	}
	if task.Assigner != "" {
		sb.WriteString(fmt.Sprintf("Assignee: %s\n", task.Assigner))
	}
	if task.ProjectID != "" {
		sb.WriteString(fmt.Sprintf("Project ID: %s\n", task.ProjectID))
	}
	if task.CreatedBy != "" {
		sb.WriteString(fmt.Sprintf("Created By: %s\n", task.CreatedBy))
	}
	sb.WriteString(fmt.Sprintf("Created: %s\n", task.CreatedAt.Format("2006-01-02 15:04:05")))
	sb.WriteString(fmt.Sprintf("Updated: %s\n", task.UpdatedAt.Format("2006-01-02 15:04:05")))
	if task.ClosedAt != nil {
		sb.WriteString(fmt.Sprintf("Closed: %s\n", task.ClosedAt.Format("2006-01-02 15:04:05")))
	}
	if len(task.Labels) > 0 {
		sb.WriteString(fmt.Sprintf("Labels: %s\n", strings.Join(task.Labels, ", ")))
	}
	if task.Description != "" {
		sb.WriteString(fmt.Sprintf("\nDescription:\n%s\n", task.Description))
	}

	comments, err := s.store.ListTaskComments(taskID)
	if err != nil {
		log.Printf("[mcp] get_task: failed to list comments for %s: %v", taskID, err)
	} else if len(comments) > 0 {
		sb.WriteString(fmt.Sprintf("\nComments (%d):\n", len(comments)))
		for _, c := range comments {
			sb.WriteString(fmt.Sprintf("  [%s] %s (%s):\n    %s\n",
				c.ID, c.Author, c.CreatedAt.Format("2006-01-02 15:04"), c.Body))
		}
	}

	log.Printf("[mcp] get_task: returned task %q (id=%s)", task.Title, task.ID)
	return textResult(sb.String()), nil
}

func (s *Server) handleUpdateTask(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()

	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return errorResult("task_id is required"), nil
	}

	req := store.UpdateTaskRequest{}
	if title, ok := args["title"].(string); ok && title != "" {
		req.Title = &title
	}
	if desc, ok := args["description"].(string); ok && desc != "" {
		req.Description = &desc
	}
	if status, ok := args["status"].(string); ok && status != "" {
		validStatuses := map[string]bool{"open": true, "closed": true}
		if !validStatuses[status] {
			return errorResult(fmt.Sprintf("invalid status %q; must be one of: open, closed", status)), nil
		}
		req.Status = &status
	}
	if priority, ok := args["priority"].(string); ok && priority != "" {
		validPriorities := map[string]bool{"low": true, "medium": true, "high": true, "critical": true}
		if !validPriorities[priority] {
			return errorResult(fmt.Sprintf("invalid priority %q; must be one of: low, medium, high, critical", priority)), nil
		}
		req.Priority = &priority
	}
	if assignee, ok := args["assignee"].(string); ok && assignee != "" {
		req.Assigner = &assignee
	}

	task, err := s.store.UpdateTask(taskID, req)
	if err != nil {
		log.Printf("[mcp] UpdateTask error: %v", err)
		return errorResult(fmt.Sprintf("failed to update task: %v", err)), nil
	}

	s.notifyChange("task", task.ID, "updated")

	var sb strings.Builder
	sb.WriteString("Task updated successfully:\n")
	sb.WriteString(fmt.Sprintf("- ID: %s\n- Title: %s\n- Status: %s\n- Priority: %s", task.ID, task.Title, task.Status, task.Priority))
	if task.Assigner != "" {
		sb.WriteString(fmt.Sprintf("\n- Assignee: %s", task.Assigner))
	}
	if task.ProjectID != "" {
		sb.WriteString(fmt.Sprintf("\n- Project ID: %s", task.ProjectID))
	}

	log.Printf("[mcp] update_task: updated %q (id=%s)", task.Title, task.ID)
	return textResult(sb.String()), nil
}

func (s *Server) handleListTasks(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()
	statusFilter, _ := args["status"].(string)
	projectID, _ := args["project_id"].(string)

	var projectIDs []string
	if projectID != "" {
		projectIDs = []string{projectID}
	}
	tasks, err := s.store.ListTasks(20, 0, projectIDs, "")
	if err != nil {
		log.Printf("[mcp] ListTasks error: %v", err)
		return errorResult(fmt.Sprintf("failed to list tasks: %v", err)), nil
	}

	// Filter by status if requested
	var filtered []store.Task
	for _, t := range tasks {
		if statusFilter != "" && t.Status != statusFilter {
			continue
		}
		filtered = append(filtered, t)
	}

	if len(filtered) == 0 {
		return textResult("No tasks found."), nil
	}

	type taskInfo struct {
		ID           string `json:"id"`
		ProjectID    string `json:"project_id,omitempty"`
		ParentID     string `json:"parent_id,omitempty"`
		Title        string `json:"title"`
		Description  string `json:"description,omitempty"`
		Status       string `json:"status"`
		Priority     string `json:"priority"`
		Assigner     string `json:"assignee,omitempty"`
		CreatedBy    string `json:"created_by,omitempty"`
		CommentCount int    `json:"comment_count"`
		UpdatedAt    string `json:"updated_at"`
	}

	items := make([]taskInfo, 0, len(filtered))
	for _, t := range filtered {
		desc := t.Description
		if len(desc) > 100 {
			desc = desc[:100] + "...(truncated)"
		}
		items = append(items, taskInfo{
			ID:           t.ID,
			ProjectID:    t.ProjectID,
			ParentID:     t.ParentID,
			Title:        t.Title,
			Description:  desc,
			Status:       t.Status,
			Priority:     t.Priority,
			Assigner:     t.Assigner,
			CreatedBy:    t.CreatedBy,
			CommentCount: t.CommentCount,
			UpdatedAt:    t.UpdatedAt.Format(time.RFC3339),
		})
	}

	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return errorResult(fmt.Sprintf("failed to marshal tasks: %v", err)), nil
	}

	log.Printf("[mcp] list_tasks: returned %d tasks (project_id=%s)", len(filtered), projectID)
	return textResult(string(data)), nil
}

func (s *Server) handleRunInTerminal(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()
	command, _ := args["command"].(string)
	if command == "" {
		return errorResult("command is required"), nil
	}

	if s.terminalMgr == nil {
		return errorResult("no open terminal found"), nil
	}

	// Get the session ID from context (injected via ?session= query param).
	sessionID, _ := ctx.Value(nexboxSessionKey{}).(string)

	// Notify the frontend to open this session's terminal panel.
	if s.changeNotifier != nil {
		s.changeNotifier("terminal", sessionID, "open")
	}

	if err := s.terminalMgr.WriteCommand(sessionID, command); err != nil {
		// No active PTY yet — wait for the frontend to connect.
		waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if waitErr := s.terminalMgr.WaitReady(waitCtx, sessionID); waitErr != nil {
			log.Printf("[mcp] run_in_terminal: timed out waiting for terminal: %v", waitErr)
			return errorResult("timed out waiting for terminal to open"), nil
		}
		if err = s.terminalMgr.WriteCommand(sessionID, command); err != nil {
			log.Printf("[mcp] run_in_terminal error: %v", err)
			return errorResult(err.Error()), nil
		}
	}

	log.Printf("[mcp] run_in_terminal: sent command %q", command)
	return textResult(fmt.Sprintf("Command sent to terminal: %s", command)), nil
}

func (s *Server) handleEnableBrowserControl(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	sessionID, _ := ctx.Value(nexboxSessionKey{}).(string)
	if sessionID == "" {
		return errorResult("session ID not found in request context; ensure the agent is configured correctly"), nil
	}

	if s.chromeEnabler == nil {
		return errorResult("browser control is not available on this server"), nil
	}

	alreadyEnabled := false
	if s.store != nil {
		sess, err := s.store.GetSession(sessionID)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to read browser control state: %v", err)), nil
		}
		if sess != nil {
			for _, feature := range strings.Split(sess.Features, ",") {
				if strings.TrimSpace(feature) == "chrome_devtools" {
					alreadyEnabled = true
					break
				}
			}
		}
	}

	_, _, err := s.chromeEnabler.EnableChrome(sessionID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to enable browser control: %v", err)), nil
	}

	log.Printf("[mcp] enable_browser_control: enabled chrome_devtools for session %s", sessionID)
	if alreadyEnabled {
		return textResult(
			"Browser control is already enabled for this session. " +
				"Use the mcp__chrome__ tools in your current tool list to continue the user's task now. " +
				"Only if those tools are absent from your current tool list, explain that they will be available in the next message.",
		), nil
	}
	return textResult(
		"Browser control has been enabled for this session. " +
			"If mcp__chrome__ tools are already in your current tool list, use them to continue the user's task now. " +
			"Otherwise, inform the user that the Chrome DevTools MCP tools will be available starting from the next message.",
	), nil
}

// mcpKeepAliveInterval keeps long blocking streamable-HTTP calls below client
// header and idle timeouts.
const mcpKeepAliveInterval = 20 * time.Second

// keepCallAlive upgrades a blocking MCP response to an SSE stream and sends
// periodic progress notifications until the handler returns.
func keepCallAlive(ctx context.Context, request gomcp.CallToolRequest, label string) (stop func()) {
	srv := server.ServerFromContext(ctx)
	if srv == nil {
		return func() {}
	}
	var token gomcp.ProgressToken
	if request.Params.Meta != nil {
		token = request.Params.Meta.ProgressToken
	}

	done := make(chan struct{})
	go func() {
		started := time.Now()
		ticker := time.NewTicker(mcpKeepAliveInterval)
		defer ticker.Stop()

		for progress := 0; ; progress++ {
			params := map[string]any{
				"progress": progress,
				"message":  fmt.Sprintf("%s (%s)", label, time.Since(started).Round(time.Second)),
			}
			if token != nil {
				params["progressToken"] = token
			}
			if err := srv.SendNotificationToClient(ctx, "notifications/progress", params); err != nil {
				log.Printf("[mcp] keep-alive notification failed: %v", err)
				return
			}
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// handleSendToSession delivers a normal turn into another session and blocks
// until that session's agent has produced its final visible answer.
func (s *Server) handleSendToSession(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	if s.orchestrator == nil {
		return errorResult("cross-session messaging is not available on this server"), nil
	}

	args := request.GetArguments()
	ref, _ := args["session_id"].(string)
	message, _ := args["message"].(string)
	if strings.TrimSpace(ref) == "" {
		return errorResult("session_id is required — ask the user for the target session's ID badge"), nil
	}
	if strings.TrimSpace(message) == "" {
		return errorResult("message is required"), nil
	}

	fromSessionID, _ := ctx.Value(nexboxSessionKey{}).(string)
	if fromSessionID == "" {
		return errorResult("calling session ID is missing from the MCP request"), nil
	}
	targetID, targetName, err := s.orchestrator.OrchestrateResolveSession(fromSessionID, ref)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	if targetName == "" {
		targetName = "Untitled"
	}

	log.Printf("[mcp] send_to_session: %s → %s (%s), %d bytes", fromSessionID, targetID, targetName, len(message))
	stopKeepAlive := keepCallAlive(ctx, request, fmt.Sprintf("waiting for session %q to reply", targetName))
	defer stopKeepAlive()

	reply, err := s.orchestrator.OrchestrateSendAndWait(ctx, fromSessionID, targetID, message)
	if err != nil {
		log.Printf("[mcp] send_to_session: %s → %s failed: %v", fromSessionID, targetID, err)
		return errorResult(fmt.Sprintf("relay to session %q (%s) failed: %v", targetName, targetID, err)), nil
	}
	log.Printf("[mcp] send_to_session: %s → %s answered (%d bytes)", fromSessionID, targetID, len(reply))
	if reply == "" {
		return textResult(fmt.Sprintf("Session %q (%s) finished the turn but produced no text reply.", targetName, targetID)), nil
	}
	return textResult(fmt.Sprintf("Reply from session %q (%s):\n\n%s", targetName, targetID, reply)), nil
}

// Helper to create a text result
func textResult(text string) *gomcp.CallToolResult {
	return &gomcp.CallToolResult{
		Content: []gomcp.Content{
			gomcp.TextContent{
				Type: "text",
				Text: text,
			},
		},
	}
}

// Helper to create an error result
func errorResult(msg string) *gomcp.CallToolResult {
	return &gomcp.CallToolResult{
		Content: []gomcp.Content{
			gomcp.TextContent{
				Type: "text",
				Text: "Error: " + msg,
			},
		},
		IsError: true,
	}
}
