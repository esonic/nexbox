package chrome

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// PageInfo represents an open browser tab/page.
type PageInfo struct {
	ID       int
	TargetID target.ID
	URL      string
	Title    string
}

// BrowserOptions configures the Chrome browser instance.
type BrowserOptions struct {
	BrowserURL  string // CDP HTTP/WS endpoint; non-empty means externally managed Chrome
	Headless    bool   // Run in headless mode (no GUI); default false
	ExecPath    string // Custom Chrome binary path; empty = auto-detect
	UserDataDir string // Chrome user data directory; empty = temp dir
	Width       int    // Window width; default 1280
	Height      int    // Window height; default 720
}

// BrowserManager manages a Chrome browser instance and its pages.
type BrowserManager struct {
	mu              sync.Mutex
	opts            BrowserOptions
	allocCtx        context.Context
	allocCancel     context.CancelFunc
	transportCancel context.CancelFunc
	remoteBrowser   *chromedp.Browser
	browserCtx      context.Context
	pages           map[int]*pageState
	closedTargets   map[target.ID]bool
	currentPage     int
	nextPageID      int
	launched        bool
	generation      int             // bumped each Launch; ties an exit watcher to its instance
	parentCtx       context.Context // long-lived parent for re-launch
}

type pageState struct {
	info     PageInfo
	ctx      context.Context
	cancel   context.CancelFunc
	dialogMu sync.Mutex
	dialog   *page.EventJavascriptDialogOpening
}

func NewBrowserManager(opts ...BrowserOptions) *BrowserManager {
	var o BrowserOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.Width == 0 {
		o.Width = 1280
	}
	if o.Height == 0 {
		o.Height = 720
	}
	return &BrowserManager{
		opts:          o,
		pages:         make(map[int]*pageState),
		closedTargets: make(map[target.ID]bool),
	}
}

// Launch starts managed Chrome or attaches to the configured external browser.
func (bm *BrowserManager) Launch(parentCtx context.Context) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.launched {
		return nil
	}
	if bm.opts.BrowserURL != "" {
		return bm.attachLocked(parentCtx)
	}

	chromeOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", bm.opts.Headless),
		chromedp.Flag("disable-gpu", bm.opts.Headless),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.WindowSize(bm.opts.Width, bm.opts.Height),
	)
	execPath := bm.opts.ExecPath
	if execPath == "" {
		execPath = detectSystemChrome()
	}
	if execPath != "" {
		chromeOpts = append(chromeOpts, chromedp.ExecPath(execPath))
	}
	if bm.opts.UserDataDir != "" {
		chromeOpts = append(chromeOpts, chromedp.UserDataDir(bm.opts.UserDataDir))
		lockFile := filepath.Join(bm.opts.UserDataDir, "SingletonLock")
		if err := os.Remove(lockFile); err == nil {
			log.Printf("[chrome] Removed stale SingletonLock from %s", bm.opts.UserDataDir)
		}
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(parentCtx, chromeOpts...)
	bm.allocCtx, bm.allocCancel = allocCtx, allocCancel
	bm.browserCtx, _ = chromedp.NewContext(allocCtx)

	// Log the resolved exec path for diagnostics.
	resolvedExec := execPath
	if resolvedExec == "" {
		resolvedExec = "(chromedp auto-detect)"
	}
	log.Printf("[chrome] Launching: exec=%s headless=%v user-data-dir=%s window=%dx%d",
		resolvedExec, bm.opts.Headless, bm.opts.UserDataDir, bm.opts.Width, bm.opts.Height)

	// Pre-flight check: verify Chrome binary is executable.
	if execPath != "" {
		if info, err := os.Stat(execPath); err != nil {
			log.Printf("[chrome] WARNING: Chrome binary not accessible: %v", err)
		} else {
			log.Printf("[chrome] Chrome binary: size=%d mode=%s", info.Size(), info.Mode())
		}
		// Quick probe: run --version to verify the binary works at all.
		probeCtx, probeCancel := context.WithTimeout(parentCtx, 3*time.Second)
		defer probeCancel()
		probe := exec.CommandContext(probeCtx, execPath, "--version")
		if out, err := probe.CombinedOutput(); err != nil {
			log.Printf("[chrome] WARNING: Chrome --version failed: %v, output: %s", err, strings.TrimSpace(string(out)))
		} else {
			log.Printf("[chrome] Chrome version probe: %s", strings.TrimSpace(string(out)))
		}
	}

	// Bound startup without tying the persistent tab context to a deadline.
	launchTimer := time.AfterFunc(15*time.Second, allocCancel)
	defer launchTimer.Stop()
	// Navigate the initial tab to about:blank to trigger Chrome launch.
	if err := chromedp.Run(bm.browserCtx, chromedp.Navigate("about:blank")); err != nil {
		bm.allocCancel()
		log.Printf("[chrome] FAILED to start Chrome (exec=%s): %v", resolvedExec, err)
		// Try to get more detail by running Chrome with CDP flags directly.
		if execPath != "" {
			diagArgs := []string{
				"--no-first-run", "--no-default-browser-check",
				"--remote-debugging-port=0",
				"--headless=new",
			}
			if bm.opts.UserDataDir != "" {
				diagArgs = append(diagArgs, "--user-data-dir="+bm.opts.UserDataDir)
			}
			diagArgs = append(diagArgs, "about:blank")
			diagCtx, diagCancel := context.WithTimeout(parentCtx, 3*time.Second)
			defer diagCancel()
			diagCmd := exec.CommandContext(diagCtx, execPath, diagArgs...)
			if diagOut, diagErr := diagCmd.CombinedOutput(); diagErr != nil {
				log.Printf("[chrome] Diagnostic launch also failed: %v\n  args: %v\n  output: %s",
					diagErr, diagArgs, strings.TrimSpace(string(diagOut)))
			} else {
				log.Printf("[chrome] Diagnostic launch succeeded (output: %s) — issue is likely with chromedp flags",
					strings.TrimSpace(string(diagOut)))
			}
		}
		return fmt.Errorf("launch chrome (exec=%s): %w", resolvedExec, err)
	}

	launchTimer.Stop()

	// Register the initial tab.
	targets, err := chromedp.Targets(bm.browserCtx)
	if err == nil {
		for _, t := range targets {
			if t.Type == "page" && t.TargetID == chromedp.FromContext(bm.browserCtx).Target.TargetID {
				bm.nextPageID++
				ps := &pageState{
					info: PageInfo{ID: bm.nextPageID, TargetID: t.TargetID, URL: t.URL, Title: t.Title},
					ctx:  bm.browserCtx,
				}
				bm.watchPage(ps)
				bm.pages[bm.nextPageID] = ps
				bm.currentPage = bm.nextPageID
				break
			}
		}
	}

	bm.parentCtx = parentCtx
	bm.launched = true
	bm.generation++
	gen := bm.generation

	// Log Chrome binary and version info.
	bm.logBrowserInfo()

	// Watch for unexpected Chrome exit (e.g. user closes the window, or the idle
	// reaper's Close). Bound to this launch's allocCtx and generation so that a
	// Close immediately followed by a relaunch can't have the old watcher stomp
	// the new instance's launched/pages state.
	go func() {
		<-allocCtx.Done()
		bm.mu.Lock()
		defer bm.mu.Unlock()
		if bm.generation != gen || !bm.launched {
			return // superseded by a newer launch, or already torn down
		}
		bm.launched = false
		bm.pages = make(map[int]*pageState)
		bm.currentPage = 0
		log.Printf("[chrome] Browser process exited unexpectedly")
	}()

	return nil
}

// Close shuts down managed Chrome or disconnects from external Chrome.
func (bm *BrowserManager) Close() {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	bm.closeLocked()
}

func (bm *BrowserManager) closeLocked() {
	// v0.14.2 cancels attached tabs before RemoteAllocator closes its socket.
	// Close our independently owned CDP transport first so context cleanup
	// cannot send Target.closeTarget to the external Chrome.
	if bm.transportCancel != nil {
		bm.transportCancel()
		if bm.remoteBrowser != nil {
			<-bm.remoteBrowser.LostConnection
		}
		bm.transportCancel = nil
		bm.remoteBrowser = nil
	}
	// Cancel the root first in external mode; page cleanup then runs concurrently.
	if bm.opts.BrowserURL != "" && bm.allocCancel != nil {
		bm.allocCancel()
		bm.allocCancel = nil
	}
	for _, ps := range bm.pages {
		if ps.cancel != nil {
			ps.cancel()
		}
	}
	bm.pages = make(map[int]*pageState)
	bm.closedTargets = make(map[target.ID]bool)
	if bm.allocCancel != nil {
		bm.allocCancel()
		bm.allocCancel = nil
	}
	bm.currentPage = 0
	bm.launched = false
	log.Printf("[chrome] Browser connection released")
}

// IsHealthy checks if Chrome is running and responsive (3s timeout).
func (bm *BrowserManager) IsHealthy() bool {
	bm.mu.Lock()
	if !bm.launched {
		bm.mu.Unlock()
		return false
	}
	ctx := bm.browserCtx
	bm.mu.Unlock()

	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := chromedp.Targets(checkCtx)
	return err == nil
}

// EnsureActivePage verifies the current page target still exists in Chrome.
// If all tabs have been closed by the user, it creates a fresh about:blank tab.
// Returns the usable page context.
func (bm *BrowserManager) EnsureActivePage(requests ...context.Context) (context.Context, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	request := requestContext(requests)
	if err := request.Err(); err != nil {
		return nil, err
	}

	if !bm.launched {
		return nil, fmt.Errorf("browser not launched")
	}
	if err := bm.syncPagesLocked(request); err != nil {
		return nil, err
	}
	if ps, ok := bm.pages[bm.currentPage]; ok {
		if err := bm.runPageLocked(ps.ctx, request, 5*time.Second); err != nil {
			bm.closeLocked()
			return nil, fmt.Errorf("attach to Chrome tab: %w", err)
		}
		return ps.ctx, nil
	}
	if bm.opts.BrowserURL != "" {
		return nil, fmt.Errorf("external Chrome has no open pages; open a tab in Chrome or use new_page")
	}
	ps, err := bm.newPageLocked(request)
	if err != nil {
		return nil, err
	}
	return ps.ctx, nil
}

// detectSystemChrome returns the system Chrome path on macOS if it exists.
func detectSystemChrome() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	p := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// logBrowserInfo logs the Chrome binary path, version, and key launch flags.
// Must be called while bm.mu is held and browserCtx is valid.
func (bm *BrowserManager) logBrowserInfo() {
	// Resolve which binary was used.
	execPath := bm.opts.ExecPath
	if execPath == "" {
		for _, name := range []string{
			"google-chrome", "google-chrome-stable",
			"chromium-browser", "chromium",
			"chrome",
		} {
			if p, err := exec.LookPath(name); err == nil {
				execPath = p
				break
			}
		}
		if execPath == "" {
			execPath = "(auto-detected by chromedp)"
		}
	}

	// Get version from CDP.
	var product, userAgent string
	if err := chromedp.Run(bm.browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, prod, _, ua, _, err := browser.GetVersion().Do(ctx)
		product = prod
		userAgent = ua
		return err
	})); err != nil {
		log.Printf("[chrome] Warning: could not get browser version: %v", err)
	}

	var flags []string
	if bm.opts.Headless {
		flags = append(flags, "headless")
	} else {
		flags = append(flags, "headed")
	}
	flags = append(flags, fmt.Sprintf("window-size=%dx%d", bm.opts.Width, bm.opts.Height))
	if bm.opts.UserDataDir != "" {
		flags = append(flags, "user-data-dir="+bm.opts.UserDataDir)
	}

	log.Printf("[chrome] ======== Browser Started ========")
	log.Printf("[chrome]   Binary:    %s", execPath)
	log.Printf("[chrome]   Product:   %s", product)
	log.Printf("[chrome]   UA:        %s", userAgent)
	log.Printf("[chrome]   Flags:     %s", strings.Join(flags, ", "))
	log.Printf("[chrome]   Page ID:   %d", bm.currentPage)
	log.Printf("[chrome] ==================================")
}

// IsLaunched reports whether Chrome has been started.
func (bm *BrowserManager) IsLaunched() bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return bm.launched
}

// CurrentPageContext returns the chromedp context for the currently selected page.
func (bm *BrowserManager) CurrentPageContext() (context.Context, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	ps, ok := bm.pages[bm.currentPage]
	if !ok {
		return nil, fmt.Errorf("no current page")
	}
	return ps.ctx, nil
}

// ListPages returns info for all open pages, refreshing URL/title from Chrome.
func (bm *BrowserManager) ListPages(requests ...context.Context) ([]PageInfo, int, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if err := bm.syncPagesLocked(requestContext(requests)); err != nil {
		return nil, 0, err
	}

	var pages []PageInfo
	for _, ps := range bm.pages {
		pages = append(pages, ps.info)
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].ID < pages[j].ID })
	return pages, bm.currentPage, nil
}

// SelectPage switches to the page with the given ID.
func (bm *BrowserManager) SelectPage(pageID int) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if _, ok := bm.pages[pageID]; !ok {
		return fmt.Errorf("page %d not found", pageID)
	}
	bm.currentPage = pageID
	log.Printf("[chrome] Switched to page %d", pageID)
	return nil
}

// NewPage creates a new browser tab and optionally navigates to a URL.
func (bm *BrowserManager) NewPage(url string) (PageInfo, error) {
	return bm.NewPageContext(context.Background(), url)
}

func (bm *BrowserManager) NewPageContext(ctx context.Context, url string) (PageInfo, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return PageInfo{}, err
	}
	bm.mu.Lock()
	if err := bm.syncPagesLocked(ctx); err != nil {
		bm.mu.Unlock()
		return PageInfo{}, err
	}
	ps, err := bm.newPageLocked(ctx)
	bm.mu.Unlock()
	if err != nil {
		return PageInfo{}, err
	}
	if url == "" {
		url = "about:blank"
	}
	// A navigation can open a native dialog. Keep the manager unlocked so
	// handle_dialog can reach this tab while the navigation is pending.
	actionCtx, actionCancel := operationContext(ps.ctx, ctx)
	defer actionCancel()
	if err := chromedp.Run(actionCtx, chromedp.Navigate(url)); err != nil {
		return PageInfo{}, fmt.Errorf("new page id=%d created but navigation failed: %w", ps.info.ID, err)
	}
	bm.mu.Lock()
	ps.info.URL = url
	info := ps.info
	bm.mu.Unlock()
	return info, nil
}

func (bm *BrowserManager) newPageLocked(ctx context.Context) (*pageState, error) {
	var options []chromedp.ContextOption
	if len(bm.pages) == 0 {
		// After the last tab closes, Chrome may have no window left. Explicitly
		// create a window rather than asking Chrome for a tab in a missing window.
		opCtx, opCancel := context.WithTimeout(bm.browserCtx, 5*time.Second)
		stop := context.AfterFunc(ctx, opCancel)
		browser := chromedp.FromContext(bm.browserCtx).Browser
		id, err := target.CreateTarget("about:blank").WithNewWindow(true).Do(cdp.WithExecutor(opCtx, browser))
		stop()
		opCancel()
		if err != nil {
			return nil, fmt.Errorf("create browser window: %w", err)
		}
		options = append(options, chromedp.WithTargetID(id))
	}
	newCtx, cancel := chromedp.NewContext(bm.browserCtx, options...)
	ps := &pageState{ctx: newCtx, cancel: cancel}
	bm.watchPage(ps)
	// The first Run owns the target event loop; initialise it on the long-lived context.
	if err := bm.runPageLocked(newCtx, ctx, 5*time.Second); err != nil {
		cancel()
		return nil, fmt.Errorf("new page: %w", err)
	}
	bm.nextPageID++
	ps.info = PageInfo{ID: bm.nextPageID, TargetID: chromedp.FromContext(newCtx).Target.TargetID, URL: "about:blank"}
	bm.pages[ps.info.ID] = ps
	bm.currentPage = ps.info.ID
	return ps, nil
}

// watchPage records native dialogs without holding the browser-manager lock in
// the synchronous CDP listener. This lets another tool dismiss a blocking dialog.
func (bm *BrowserManager) watchPage(ps *pageState) {
	chromedp.ListenTarget(ps.ctx, func(ev any) {
		switch e := ev.(type) {
		case *page.EventJavascriptDialogOpening:
			ps.dialogMu.Lock()
			ps.dialog = e
			ps.dialogMu.Unlock()
		case *page.EventJavascriptDialogClosed:
			ps.dialogMu.Lock()
			ps.dialog = nil
			ps.dialogMu.Unlock()
		}
	})
}

func (bm *BrowserManager) CurrentDialog() *page.EventJavascriptDialogOpening {
	bm.mu.Lock()
	ps := bm.pages[bm.currentPage]
	bm.mu.Unlock()
	if ps == nil {
		return nil
	}
	ps.dialogMu.Lock()
	defer ps.dialogMu.Unlock()
	if ps.dialog == nil {
		return nil
	}
	copy := *ps.dialog
	return &copy
}

// HandleDialog requires a target already attached on its persistent context.
// Opening a new attachment here can block behind an existing dialog, and a
// request-scoped first Run would permanently cancel that target's event loop.
func (bm *BrowserManager) HandleDialog(ctx context.Context, accept bool, prompt string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	bm.mu.Lock()
	ps, ok := bm.pages[bm.currentPage]
	if !ok {
		bm.mu.Unlock()
		return fmt.Errorf("no current page")
	}
	executor := chromedp.FromContext(ps.ctx).Target
	bm.mu.Unlock()
	if executor == nil {
		return fmt.Errorf("tab is not attached; attach with take_snapshot before opening a dialog. If a dialog is already open, dismiss it in the browser or the debugger that opened it")
	}
	actionCtx, cancel := operationContext(ps.ctx, ctx)
	defer cancel()
	return page.HandleJavaScriptDialog(accept).WithPromptText(prompt).Do(cdp.WithExecutor(actionCtx, executor))
}

// ClosePage closes the tab with the given ID.
func (bm *BrowserManager) ClosePage(pageID int, requests ...context.Context) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	ps, ok := bm.pages[pageID]
	if !ok {
		return fmt.Errorf("page %d not found", pageID)
	}
	if err := bm.closePageLocked(ps, requestContext(requests)); err != nil {
		return err
	}
	bm.closedTargets[ps.info.TargetID] = true
	delete(bm.pages, pageID)
	if bm.currentPage == pageID {
		bm.currentPage = 0
		for id := range bm.pages {
			if bm.currentPage == 0 || id < bm.currentPage {
				bm.currentPage = id
			}
		}
	}
	return nil
}
