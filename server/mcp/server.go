package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	gitlabpkg "gitlab.okg.com/sre_ai/boxnex/gitlab"
	"gitlab.okg.com/sre_ai/boxnex/store"
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
	GitLabToken() string
	ProjectIDs() []string
	// GitRepoPath returns the local git repo path for a project (by remote ID).
	// Returns empty string if not found.
	GitRepoPath(projectID string) string
}

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
}

// WorkflowExecutor provides the unified Execute entry point for workflow execution.
type WorkflowExecutor interface {
	ExecuteWorkflow(workflowID, projectID, userContext, triggeredBy string, taskIDs []string) (executionID string, err error)
}

// ChromeEnabler allows the MCP server to enable per-session browser control.
type ChromeEnabler interface {
	// EnableChrome adds chrome_devtools to the session's features and starts
	// the Chrome MCP server for that session. Returns the Chrome MCP URL and
	// tool names on success.
	EnableChrome(sessionID string) (mcpURL string, toolNames []string, err error)
}

// boxnexSessionKey is the context key used to carry the boxnex session ID
// through MCP HTTP requests.
type boxnexSessionKey struct{}

// Server wraps the mcp-go MCP server with BoxNex-specific tools
type Server struct {
	mcpServer        *server.MCPServer
	httpServer       *server.StreamableHTTPServer
	store            store.Store
	addr             string
	actualAddr       string
	listener         net.Listener
	httpSrv          *http.Server
	toolNames        []string
	terminalMgr      TerminalWriter
	configGetter     ConfigGetter
	changeNotifier   ChangeNotifier
	orchestrator     SessionOrchestrator
	workflowExecutor WorkflowExecutor
	chromeEnabler    ChromeEnabler
}

// NewServer creates a new MCP server with asset management tools
func NewServer(s store.Store, addr string) *Server {
	srv := &Server{
		store: s,
		addr:  addr,
	}

	// Create MCP server
	srv.mcpServer = server.NewMCPServer(
		"BoxNex",
		"1.0.0",
		server.WithToolCapabilities(true),
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
	// Wrap the MCP handler to inject the boxnex session ID (passed via ?session=
	// query param by the agent) into the request context so tool handlers can
	// access it without requiring the AI to pass it explicitly.
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sid := r.URL.Query().Get("session"); sid != "" {
			r = r.WithContext(context.WithValue(r.Context(), boxnexSessionKey{}, sid))
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

// SetChangeNotifier sets the callback for broadcasting data changes to browsers.
func (s *Server) SetChangeNotifier(fn ChangeNotifier) {
	s.changeNotifier = fn
}

// SetSessionOrchestrator sets the session orchestrator for workflow execution.
func (s *Server) SetSessionOrchestrator(orch SessionOrchestrator) {
	s.orchestrator = orch
}

// SetWorkflowExecutor sets the workflow executor for the execute_workflow tool.
func (s *Server) SetWorkflowExecutor(exec WorkflowExecutor) {
	s.workflowExecutor = exec
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

// ToolNames returns the registered tool names with the "mcp__boxnex__" prefix,
// suitable for passing directly to Claude's allowed_tools list.
func (s *Server) ToolNames() []string {
	out := make([]string, len(s.toolNames))
	copy(out, s.toolNames)
	return out
}

func (s *Server) registerTools() {
	// save_asset: Create or update an asset in a project
	saveAssetTool := gomcp.NewTool("save_asset",
		gomcp.WithDescription(
			"Save a document/asset to the platform. "+
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

	// get_asset_content: Retrieve the latest content of an asset by ID
	getAssetContentTool := gomcp.NewTool("get_asset_content",
		gomcp.WithDescription(
			"Retrieve the latest content of a specific asset by its ID. "+
				"Use this when you need to read or reference an existing document."),
		gomcp.WithString("asset_id",
			gomcp.Required(),
			gomcp.Description("The asset ID"),
		),
	)

	// list_assets: List assets in a project (top 20, most recent first)
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

	// list_asset_comments: List all review comments for an asset
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
			"Create a new task (like a Jira ticket). "+
				"Use this when the user asks to create a task, track a piece of work. "+
				"If you know the current project_id (from context), pass it; otherwise omit it."),
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
			"List tasks. Returns top 20 tasks sorted by most recently updated. "+
				"Returns task metadata (id, title, status, priority, assignee). "+
				"Use this to see existing tasks or find tasks to reference."),
		gomcp.WithString("status",
			gomcp.Description("Optional status filter: open, in_progress, done, closed"),
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
			gomcp.Description("New status: open, in_progress, done, or closed"),
		),
		gomcp.WithString("priority",
			gomcp.Description("New priority: low, medium, high, or critical"),
		),
		gomcp.WithString("assignee",
			gomcp.Description("New assignee for the task"),
		),
	)

	getGitlabMRCommentsTool := gomcp.NewTool("get_gitlab_mr_comments",
		gomcp.WithDescription(
			"Fetch review comments from a GitLab Merge Request. "+
				"Returns structured JSON comments including file paths, line numbers, content, and author. "+
				"If this tool returns a 'token not configured' error, immediately tell the user to configure their GitLab personal access token in Settings. Do NOT attempt to find or set the token yourself."),
		gomcp.WithString("mr_url",
			gomcp.Required(),
			gomcp.Description("The full GitLab MR URL, e.g. https://gitlab.okg.com/group/project/-/merge_requests/123"),
		),
	)

	createGitlabMRTool := gomcp.NewTool("create_gitlab_mr",
		gomcp.WithDescription(
			"Create a GitLab Merge Request for a project. "+
				"Pushes the current branch to origin and opens an MR against the default branch. "+
				"Returns the MR iid, URL, title, and state. "+
				"If this tool returns a 'token not configured' error, immediately tell the user to configure their GitLab personal access token in Settings. Do NOT attempt to find or set the token yourself."),
		gomcp.WithString("project_id",
			gomcp.Required(),
			gomcp.Description("The project ID"),
		),
		gomcp.WithString("title",
			gomcp.Required(),
			gomcp.Description("MR title"),
		),
		gomcp.WithString("desc",
			gomcp.Description("MR description (optional)"),
		),
	)

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

	addTool := func(tool gomcp.Tool, handler server.ToolHandlerFunc) {
		name := tool.Name
		wrapped := func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
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
		s.toolNames = append(s.toolNames, "mcp__boxnex__"+name)
	}

	// --- Workflow tools ---

	listWorkflowsTool := gomcp.NewTool("list_workflows",
		gomcp.WithDescription(
			"List workflows in a project. Returns workflow metadata (id, name, status, node/edge counts)."),
		gomcp.WithString("project_id",
			gomcp.Required(),
			gomcp.Description("The project ID"),
		),
	)

	getWorkflowTool := gomcp.NewTool("get_workflow",
		gomcp.WithDescription(
			"Get full details of a workflow including its definition (nodes and edges). "+
				"Use this to understand the workflow structure before making modifications."),
		gomcp.WithString("workflow_id",
			gomcp.Required(),
			gomcp.Description("The workflow ID"),
		),
	)

	executeWorkflowTool := gomcp.NewTool("execute_workflow",
		gomcp.WithDescription(
			"Execute a workflow by triggering its server-side state machine. "+
				"Each AI node in the workflow will create a new agent session and execute its prompt with full task context. "+
				"Multiple tasks can be submitted to a single workflow execution — pass ALL task IDs together. "+
				"Use this after reading tasks and workflow to start the automated development pipeline."),
		gomcp.WithString("workflow_id",
			gomcp.Required(),
			gomcp.Description("The workflow ID to execute"),
		),
		gomcp.WithString("task_ids",
			gomcp.Description("Comma-separated task IDs to inject as context into workflow node prompts (e.g. 'id1,id2,id3')"),
		),
		gomcp.WithString("project_id",
			gomcp.Description("Project ID for creating new sessions"),
		),
		gomcp.WithString("user_context",
			gomcp.Description("Additional user context or instructions to prepend to the first node's prompt"),
		),
	)

	// --- Browser control tool ---

	enableBrowserControlTool := gomcp.NewTool("enable_browser_control",
		gomcp.WithDescription(
			"Enable Chrome browser control for the current session. "+
				"Call this tool when you need to perform end-to-end browser testing, "+
				"UI verification, or any task that requires controlling a real browser. "+
				"After calling this tool, the Chrome DevTools MCP tools will be available in the next message."),
	)

	addTool(saveAssetTool, s.handleSaveAsset)
	addTool(getAssetContentTool, s.handleGetAssetContent)
	addTool(listAssetsTool, s.handleListAssets)
	addTool(listAssetCommentsTool, s.handleListAssetComments)
	addTool(askQuestionTool, s.handleAskQuestion)
	addTool(createTaskTool, s.handleCreateTask)
	addTool(getTaskTool, s.handleGetTask)
	addTool(listTasksTool, s.handleListTasks)
	addTool(updateTaskTool, s.handleUpdateTask)
	addTool(runInTerminalTool, s.handleRunInTerminal)
	addTool(getGitlabMRCommentsTool, s.handleGetGitlabMRComments)
	addTool(createGitlabMRTool, s.handleCreateGitlabMR)
	addTool(listWorkflowsTool, s.handleListWorkflows)
	addTool(getWorkflowTool, s.handleGetWorkflow)
	addTool(executeWorkflowTool, s.handleExecuteWorkflow)
	addTool(enableBrowserControlTool, s.handleEnableBrowserControl)
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

	linkedAssets, err := s.store.ListTaskAssets(taskID)
	if err != nil {
		log.Printf("[mcp] get_task: failed to list linked assets for %s: %v", taskID, err)
	} else if len(linkedAssets) > 0 {
		sb.WriteString(fmt.Sprintf("\nLinked Assets (%d):\n", len(linkedAssets)))
		for _, a := range linkedAssets {
			sb.WriteString(fmt.Sprintf("  - [%s] %s (type: %s, version: %d, status: %s)\n",
				a.ID, a.Title, a.Type, a.CurrentVersion, a.Status))
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
		validStatuses := map[string]bool{"open": true, "in_progress": true, "done": true, "closed": true}
		if !validStatuses[status] {
			return errorResult(fmt.Sprintf("invalid status %q; must be one of: open, in_progress, done, closed", status)), nil
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

func (s *Server) handleGetGitlabMRComments(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()
	mrURL, _ := args["mr_url"].(string)

	if mrURL == "" {
		return errorResult("mr_url is required"), nil
	}

	if s.configGetter == nil {
		return errorResult("config is not available"), nil
	}
	token := s.configGetter.GitLabToken()
	if token == "" {
		return errorResult("GitLab personal access token is not configured. Please add it in Settings > GitLab Token."), nil
	}

	host, projectPath, mrIID, err := parseMRURL(mrURL)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	result, err := fetchMRComments(host, projectPath, mrIID, token)
	if err != nil {
		log.Printf("[mcp] get_gitlab_mr_comments error: %v", err)
		return errorResult(fmt.Sprintf("Failed to fetch MR comments: %v", err)), nil
	}

	return textResult(result), nil
}

func (s *Server) handleCreateGitlabMR(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()
	projectID, _ := args["project_id"].(string)
	title, _ := args["title"].(string)
	desc, _ := args["desc"].(string)
	if projectID == "" {
		return errorResult("project_id is required"), nil
	}
	if title == "" {
		return errorResult("title is required"), nil
	}

	if s.configGetter == nil {
		return errorResult("config is not available"), nil
	}
	token := s.configGetter.GitLabToken()
	if token == "" {
		return errorResult("GitLab personal access token is not configured. Please add it in Settings > GitLab Token."), nil
	}

	project, err := s.store.GetProject(projectID)
	if err != nil || project == nil {
		return errorResult(fmt.Sprintf("project not found: %v", err)), nil
	}
	if project.GitRepoPath == "" {
		project.GitRepoPath = s.configGetter.GitRepoPath(projectID)
	}
	if project.GitRepoPath == "" {
		return errorResult("project has no git repo path configured"), nil
	}

	mr, err := gitlabpkg.CreateMRForProject(ctx, project.GitRepoPath, title, desc, token)
	if err != nil {
		log.Printf("[mcp] create_gitlab_mr error: %v", err)
		return errorResult(err.Error()), nil
	}

	result, _ := json.Marshal(map[string]interface{}{
		"iid":     mr.IID,
		"web_url": mr.WebURL,
		"title":   mr.Title,
		"state":   mr.State,
	})
	log.Printf("[mcp] create_gitlab_mr: created MR !%d: %s", mr.IID, mr.WebURL)
	return textResult(string(result)), nil
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
	sessionID, _ := ctx.Value(boxnexSessionKey{}).(string)

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

// ---------------------------------------------------------------------------
// Workflow handlers
// ---------------------------------------------------------------------------

func (s *Server) handleListWorkflows(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()
	projectID, _ := args["project_id"].(string)
	if projectID == "" {
		return errorResult("project_id is required"), nil
	}

	workflows, err := s.store.ListWorkflows(projectID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list workflows: %v", err)), nil
	}

	if len(workflows) == 0 {
		return textResult("No workflows found in this project."), nil
	}

	type wfInfo struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Status      string `json:"status"`
		Version     int    `json:"version"`
		NodeCount   int    `json:"node_count"`
		EdgeCount   int    `json:"edge_count"`
		UpdatedAt   string `json:"updated_at"`
	}

	items := make([]wfInfo, 0, len(workflows))
	for _, wf := range workflows {
		nc, ec := 0, 0
		var def store.WorkflowDefinition
		if err := json.Unmarshal(wf.Definition, &def); err == nil {
			nc = len(def.Nodes)
			ec = len(def.Edges)
		}
		items = append(items, wfInfo{
			ID:          wf.ID,
			Name:        wf.Name,
			Description: wf.Description,
			Status:      wf.Status,
			Version:     wf.Version,
			NodeCount:   nc,
			EdgeCount:   ec,
			UpdatedAt:   wf.UpdatedAt.Format(time.RFC3339),
		})
	}

	data, _ := json.MarshalIndent(items, "", "  ")
	return textResult(string(data)), nil
}

func (s *Server) handleGetWorkflow(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()
	workflowID, _ := args["workflow_id"].(string)
	if workflowID == "" {
		return errorResult("workflow_id is required"), nil
	}

	wf, err := s.store.GetWorkflow(workflowID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get workflow: %v", err)), nil
	}
	if wf == nil {
		return errorResult(fmt.Sprintf("workflow %s not found", workflowID)), nil
	}

	type fullWorkflow struct {
		ID          string          `json:"id"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Status      string          `json:"status"`
		Version     int             `json:"version"`
		Definition  json.RawMessage `json:"definition"`
		CreatedBy   string          `json:"created_by,omitempty"`
		CreatedAt   string          `json:"created_at"`
		UpdatedAt   string          `json:"updated_at"`
	}

	out := fullWorkflow{
		ID:          wf.ID,
		Name:        wf.Name,
		Description: wf.Description,
		Status:      wf.Status,
		Version:     wf.Version,
		Definition:  wf.Definition,
		CreatedBy:   wf.CreatedBy,
		CreatedAt:   wf.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   wf.UpdatedAt.Format(time.RFC3339),
	}

	data, _ := json.MarshalIndent(out, "", "  ")
	log.Printf("[mcp] get_workflow: returned %q (id=%s)", wf.Name, wf.ID)
	return textResult(string(data)), nil
}

// ---------------------------------------------------------------------------
// Workflow execution handler
// ---------------------------------------------------------------------------

func (s *Server) handleExecuteWorkflow(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	if s.workflowExecutor == nil {
		return errorResult("workflow execution not available (executor not configured)"), nil
	}

	args := request.GetArguments()
	workflowID, _ := args["workflow_id"].(string)
	taskIDsRaw, _ := args["task_ids"].(string)
	projectID, _ := args["project_id"].(string)
	userContext, _ := args["user_context"].(string)

	if workflowID == "" {
		return errorResult("workflow_id is required"), nil
	}

	// Auto-resolve project_id from workflow if not provided
	if projectID == "" {
		wf, err := s.store.GetWorkflow(workflowID)
		if err != nil || wf == nil {
			return errorResult(fmt.Sprintf("workflow %s not found", workflowID)), nil
		}
		projectID = wf.ProjectID
	}

	var taskIDs []string
	if taskIDsRaw != "" {
		for _, id := range strings.Split(taskIDsRaw, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				taskIDs = append(taskIDs, id)
			}
		}
	}

	execID, err := s.workflowExecutor.ExecuteWorkflow(workflowID, projectID, userContext, "mcp", taskIDs)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to start workflow: %v", err)), nil
	}

	log.Printf("[mcp] execute_workflow: triggered %s (exec=%s, tasks=%d)", workflowID, execID, len(taskIDs))

	return textResult(fmt.Sprintf(
		"Workflow execution started (execution_id: %s, tasks: %d). "+
			"Each AI node will run in a new session visible in the sidebar. "+
			"This call returns immediately — do not wait for completion.",
		execID, len(taskIDs),
	)), nil
}

func (s *Server) handleEnableBrowserControl(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	sessionID, _ := ctx.Value(boxnexSessionKey{}).(string)
	if sessionID == "" {
		return errorResult("session ID not found in request context; ensure the agent is configured correctly"), nil
	}

	if s.chromeEnabler == nil {
		return errorResult("browser control is not available on this server"), nil
	}

	_, _, err := s.chromeEnabler.EnableChrome(sessionID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to enable browser control: %v", err)), nil
	}

	log.Printf("[mcp] enable_browser_control: enabled chrome_devtools for session %s", sessionID)
	return textResult(
		"Browser control has been enabled for this session. " +
			"The Chrome DevTools MCP tools will be available starting from the next message.\n\n" +
			"Please inform the user and ask them to send a new message to start the browser testing task.",
	), nil
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
