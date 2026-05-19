package chrome

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Server is a Chrome DevTools MCP server backed by chromedp.
// It exposes browser automation tools for E2E testing.
type Server struct {
	mcpServer     *server.MCPServer
	httpTransport *server.StreamableHTTPServer

	browser  *BrowserManager
	snapshot *SnapshotManager

	addr          string
	actualAddr    string
	listener      net.Listener
	httpSrv       *http.Server
	screenshotDir string
	toolNames     []string

	lastUsedNano atomic.Int64

	mu        sync.Mutex
	started   bool
	parentCtx context.Context
	bgCancel  context.CancelFunc
}

// NewServer creates a Chrome MCP server. Call EnsureStarted() to launch Chrome
// and start serving. Use addr "127.0.0.1:0" for automatic port assignment.
func NewServer(addr, screenshotDir string, browserOpts ...BrowserOptions) *Server {
	s := &Server{
		addr:          addr,
		screenshotDir: screenshotDir,
		browser:       NewBrowserManager(browserOpts...),
		snapshot:      NewSnapshotManager(),
	}
	s.lastUsedNano.Store(time.Now().UnixNano())
	return s
}

// Touch resets the idle clock — call when the session is observed to be active
// (e.g. the manager handed out the server for a new chat message).
func (s *Server) Touch() {
	s.lastUsedNano.Store(time.Now().UnixNano())
}

// LastUsed returns the most recent time a tool was invoked or the server was
// touched. Used by the manager's idle reaper.
func (s *Server) LastUsed() time.Time {
	return time.Unix(0, s.lastUsedNano.Load())
}

// EnsureStarted starts the MCP HTTP server if not already done.
// Chrome itself is NOT launched here — it starts lazily on the first tool call
// (via getPageCtx), so sessions that never use browser tools don't open Chrome.
func (s *Server) EnsureStarted(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return nil
	}

	bgCtx, bgCancel := context.WithCancel(context.Background())
	s.parentCtx = bgCtx
	s.bgCancel = bgCancel

	s.mcpServer = server.NewMCPServer(
		"Chrome DevTools",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)
	s.registerTools()
	s.httpTransport = server.NewStreamableHTTPServer(s.mcpServer)

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		bgCancel()
		return fmt.Errorf("chrome mcp: listen %s: %w", s.addr, err)
	}
	s.listener = ln
	s.actualAddr = ln.Addr().String()

	mux := http.NewServeMux()
	mux.Handle("/mcp", s.httpTransport)
	s.httpSrv = &http.Server{Handler: mux}

	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[chrome-mcp] server error: %v", err)
		}
	}()

	s.started = true
	log.Printf("[chrome-mcp] MCP server started on %s (%d tools, Chrome deferred)", s.actualAddr, len(s.toolNames))
	return nil
}

// URL returns the full MCP endpoint URL.
func (s *Server) URL() string {
	host := s.actualAddr
	if host == "" {
		return ""
	}
	if host[0] == ':' {
		host = "127.0.0.1" + host
	}
	return fmt.Sprintf("http://%s/mcp", host)
}

// ToolNames returns tool names with the mcp__chrome__ prefix for AllowedTools.
func (s *Server) ToolNames() []string {
	out := make([]string, len(s.toolNames))
	copy(out, s.toolNames)
	return out
}

// IsStarted reports whether the server is running.
func (s *Server) IsStarted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}


// Shutdown stops the MCP server and closes Chrome.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return nil
	}
	s.browser.Close()
	if s.httpSrv != nil {
		if err := s.httpSrv.Shutdown(ctx); err != nil {
			return err
		}
	}
	if s.bgCancel != nil {
		s.bgCancel()
	}
	s.started = false
	log.Printf("[chrome-mcp] Shut down")
	return nil
}

// getPageCtx returns the current page's chromedp context.
// It handles three cases:
//  1. Chrome not yet launched → lazy first launch
//  2. Chrome process dead/hung → kill and restart entirely
//  3. All tabs closed but Chrome alive → create a new tab
func (s *Server) getPageCtx() (context.Context, error) {
	if err := s.ensureBrowser(); err != nil {
		return nil, err
	}

	ctx, err := s.browser.EnsureActivePage()
	if err != nil {
		log.Printf("[chrome-mcp] EnsureActivePage failed after launch: %v", err)
		return nil, err
	}
	return ctx, nil
}

// ensureBrowser guarantees Chrome is running. Called lazily on first tool use.
// If Chrome is not launched, it starts it. If it's unhealthy, it restarts it.
func (s *Server) ensureBrowser() error {
	s.mu.Lock()
	if s.parentCtx == nil {
		s.mu.Unlock()
		return fmt.Errorf("chrome MCP not initialized")
	}
	parentCtx := s.parentCtx
	s.mu.Unlock()

	if s.browser.IsLaunched() && s.browser.IsHealthy() {
		return nil
	}

	if s.browser.IsLaunched() {
		log.Printf("[chrome-mcp] Chrome unhealthy, restarting...")
		s.browser.Close()
		s.snapshot = NewSnapshotManager()
	}

	log.Printf("[chrome-mcp] Launching Chrome...")
	if err := s.browser.Launch(parentCtx); err != nil {
		return fmt.Errorf("launch chrome: %w", err)
	}
	log.Printf("[chrome-mcp] Chrome launched successfully")
	return nil
}

func (s *Server) registerTools() {
	add := func(tool gomcp.Tool, handler server.ToolHandlerFunc) {
		name := tool.Name
		wrapped := func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
			s.Touch()
			log.Printf("[chrome-mcp] >> %s", name)
			return handler(ctx, req)
		}
		s.mcpServer.AddTool(tool, wrapped)
		s.toolNames = append(s.toolNames, "mcp__chrome__"+name)
	}

	// Snapshot
	add(gomcp.NewTool("take_snapshot",
		gomcp.WithDescription("Capture a text snapshot of the page's accessibility tree. "+
			"Returns elements with UIDs that can be used in click, fill, hover, and other tools. "+
			"Call this tool first to understand the page structure before interacting."),
		gomcp.WithString("filePath", gomcp.Description("Optional file path to save snapshot instead of returning inline")),
	), s.handleTakeSnapshot)

	// Navigation
	add(gomcp.NewTool("navigate_page",
		gomcp.WithDescription("Navigate the current page. Use type='url' to go to a URL, "+
			"'back'/'forward' for history, or 'reload' to refresh."),
		gomcp.WithString("type", gomcp.Description("Navigation type: url, back, forward, reload (default: url)")),
		gomcp.WithString("url", gomcp.Description("Target URL (required when type is 'url')")),
	), s.handleNavigatePage)

	// Input
	add(gomcp.NewTool("click",
		gomcp.WithDescription("Click an element identified by uid from take_snapshot."),
		gomcp.WithString("uid", gomcp.Required(), gomcp.Description("Element UID from page snapshot")),
		gomcp.WithBoolean("dblClick", gomcp.Description("Double-click instead of single-click")),
	), s.handleClick)

	add(gomcp.NewTool("fill",
		gomcp.WithDescription("Clear and fill an input, textarea, or select element with a value. "+
			"Identified by uid from take_snapshot."),
		gomcp.WithString("uid", gomcp.Required(), gomcp.Description("Element UID from page snapshot")),
		gomcp.WithString("value", gomcp.Required(), gomcp.Description("Value to fill")),
	), s.handleFill)

	add(gomcp.NewTool("type_text",
		gomcp.WithDescription("Type text character by character into the currently focused element. "+
			"Use fill for replacing entire input values; use type_text for simulating real typing."),
		gomcp.WithString("text", gomcp.Required(), gomcp.Description("Text to type")),
		gomcp.WithString("submitKey", gomcp.Description("Key to press after typing, e.g. 'Enter'")),
	), s.handleTypeText)

	add(gomcp.NewTool("press_key",
		gomcp.WithDescription("Press a key or key combination. "+
			"Supports modifiers: Control+A, Shift+Tab, Meta+C, Alt+F4, etc."),
		gomcp.WithString("key", gomcp.Required(),
			gomcp.Description("Key or combo, e.g. 'Enter', 'Control+A', 'Shift+Tab'")),
	), s.handlePressKey)

	add(gomcp.NewTool("hover",
		gomcp.WithDescription("Move the mouse over an element identified by uid from take_snapshot."),
		gomcp.WithString("uid", gomcp.Required(), gomcp.Description("Element UID from page snapshot")),
	), s.handleHover)

	// Debug / Inspect
	add(gomcp.NewTool("take_screenshot",
		gomcp.WithDescription("Capture a screenshot and save to file. "+
			"Returns the file path. Supports viewport, full-page, or single element."),
		gomcp.WithBoolean("fullPage", gomcp.Description("Capture the entire scrollable page")),
		gomcp.WithString("uid", gomcp.Description("Element UID to screenshot (mutually exclusive with fullPage)")),
		gomcp.WithString("filePath", gomcp.Description("Path to save the screenshot (auto-generated if omitted)")),
	), s.handleTakeScreenshot)

	add(gomcp.NewTool("evaluate_script",
		gomcp.WithDescription("Execute a JavaScript function in the page context. "+
			"Pass a function declaration like '() => document.title' or '(el) => el.innerText'. "+
			"If args are provided, UIDs are resolved to elements and passed as arguments."),
		gomcp.WithString("function", gomcp.Required(),
			gomcp.Description("JS function declaration, e.g. '() => document.title'")),
	), s.handleEvaluateScript)

	add(gomcp.NewTool("wait_for",
		gomcp.WithDescription("Wait until specific text appears on the page. "+
			"Resolves as soon as any of the given texts is found. Returns a snapshot."),
	), s.handleWaitFor)

	// Page management
	add(gomcp.NewTool("list_pages",
		gomcp.WithDescription("List all open browser tabs/pages with their IDs and URLs."),
	), s.handleListPages)

	add(gomcp.NewTool("select_page",
		gomcp.WithDescription("Switch to a different browser tab by page ID (from list_pages)."),
		gomcp.WithNumber("pageId", gomcp.Required(), gomcp.Description("Page ID to switch to")),
	), s.handleSelectPage)

	add(gomcp.NewTool("new_page",
		gomcp.WithDescription("Open a new browser tab, optionally navigating to a URL."),
		gomcp.WithString("url", gomcp.Description("URL to navigate to (default: about:blank)")),
	), s.handleNewPage)

	add(gomcp.NewTool("get_page_info",
		gomcp.WithDescription("Get the current page's URL and title."),
	), s.handleGetPageInfo)
}
