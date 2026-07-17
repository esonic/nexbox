package chrome

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
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
	Headless    bool   // Run in headless mode (no GUI); default false
	ExecPath    string // Custom Chrome binary path; empty = auto-detect
	UserDataDir string // Chrome user data directory; empty = temp dir
	Width       int    // Window width; default 1280
	Height      int    // Window height; default 720
}

// BrowserManager manages a Chrome browser instance and its pages.
type BrowserManager struct {
	mu          sync.Mutex
	opts        BrowserOptions
	allocCtx    context.Context
	allocCancel context.CancelFunc
	browserCtx  context.Context
	pages       map[int]*pageState
	currentPage int
	nextPageID  int
	launched    bool
	generation  int             // bumped each Launch; ties an exit watcher to its instance
	parentCtx   context.Context // long-lived parent for re-launch
}

type pageState struct {
	info   PageInfo
	ctx    context.Context
	cancel context.CancelFunc
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
		opts:  o,
		pages: make(map[int]*pageState),
	}
}

// Launch starts a new headed Chrome browser instance.
func (bm *BrowserManager) Launch(parentCtx context.Context) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.launched {
		return nil
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
		probe := exec.CommandContext(parentCtx, execPath, "--version")
		if out, err := probe.CombinedOutput(); err != nil {
			log.Printf("[chrome] WARNING: Chrome --version failed: %v, output: %s", err, strings.TrimSpace(string(out)))
		} else {
			log.Printf("[chrome] Chrome version probe: %s", strings.TrimSpace(string(out)))
		}
	}

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
			diagCmd := exec.CommandContext(parentCtx, execPath, diagArgs...)
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

	// Register the initial tab.
	targets, err := chromedp.Targets(bm.browserCtx)
	if err == nil {
		for _, t := range targets {
			if t.Type == "page" {
				bm.nextPageID++
				ps := &pageState{
					info: PageInfo{ID: bm.nextPageID, TargetID: t.TargetID, URL: t.URL, Title: t.Title},
					ctx:  bm.browserCtx,
				}
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

// Close shuts down the Chrome browser.
func (bm *BrowserManager) Close() {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	for _, ps := range bm.pages {
		if ps.cancel != nil {
			ps.cancel()
		}
	}
	bm.pages = make(map[int]*pageState)
	if bm.allocCancel != nil {
		bm.allocCancel()
	}
	bm.launched = false
	log.Printf("[chrome] Browser closed")
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
func (bm *BrowserManager) EnsureActivePage() (context.Context, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if !bm.launched {
		return nil, fmt.Errorf("browser not launched")
	}

	checkCtx, cancel := context.WithTimeout(bm.browserCtx, 3*time.Second)
	defer cancel()
	targets, err := chromedp.Targets(checkCtx)
	if err != nil {
		return nil, fmt.Errorf("list targets: %w", err)
	}

	liveTargets := make(map[string]bool)
	for _, t := range targets {
		if t.Type == "page" {
			liveTargets[string(t.TargetID)] = true
		}
	}

	// Check if the current page's target is still alive.
	if ps, ok := bm.pages[bm.currentPage]; ok {
		if liveTargets[string(ps.info.TargetID)] {
			return ps.ctx, nil
		}
		log.Printf("[chrome] Current page %d (target %s) no longer exists, cleaning up", bm.currentPage, ps.info.TargetID)
		if ps.cancel != nil {
			ps.cancel()
		}
		delete(bm.pages, bm.currentPage)
	}

	// Remove any other stale pages.
	for id, ps := range bm.pages {
		if !liveTargets[string(ps.info.TargetID)] {
			if ps.cancel != nil {
				ps.cancel()
			}
			delete(bm.pages, id)
		}
	}

	// If any live pages remain in our map, switch to one.
	for id, ps := range bm.pages {
		bm.currentPage = id
		log.Printf("[chrome] Switched to surviving page %d", id)
		return ps.ctx, nil
	}

	// No pages left — create a fresh tab.
	log.Printf("[chrome] All tabs closed, creating new blank tab")
	newCtx, newCancel := chromedp.NewContext(bm.browserCtx)
	if err := chromedp.Run(newCtx, chromedp.Navigate("about:blank")); err != nil {
		newCancel()
		return nil, fmt.Errorf("create recovery tab: %w", err)
	}
	info := chromedp.FromContext(newCtx)
	bm.nextPageID++
	pi := PageInfo{ID: bm.nextPageID, TargetID: info.Target.TargetID, URL: "about:blank"}
	bm.pages[bm.nextPageID] = &pageState{info: pi, ctx: newCtx, cancel: newCancel}
	bm.currentPage = bm.nextPageID
	log.Printf("[chrome] Recovery tab created: page %d", bm.nextPageID)
	return newCtx, nil
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
func (bm *BrowserManager) ListPages() ([]PageInfo, int, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	targets, err := chromedp.Targets(bm.browserCtx)
	if err != nil {
		return nil, 0, fmt.Errorf("list targets: %w", err)
	}

	targetMap := make(map[target.ID]*target.Info, len(targets))
	for _, t := range targets {
		targetMap[t.TargetID] = t
	}

	var pages []PageInfo
	for _, ps := range bm.pages {
		if t, ok := targetMap[ps.info.TargetID]; ok {
			ps.info.URL = t.URL
			ps.info.Title = t.Title
		}
		pages = append(pages, ps.info)
	}
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
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if url == "" {
		url = "about:blank"
	}

	newCtx, cancel := chromedp.NewContext(bm.browserCtx)
	if err := chromedp.Run(newCtx, chromedp.Navigate(url)); err != nil {
		cancel()
		return PageInfo{}, fmt.Errorf("new page: %w", err)
	}

	info := chromedp.FromContext(newCtx)
	bm.nextPageID++
	pi := PageInfo{ID: bm.nextPageID, TargetID: info.Target.TargetID, URL: url}
	ps := &pageState{info: pi, ctx: newCtx, cancel: cancel}
	bm.pages[bm.nextPageID] = ps
	bm.currentPage = bm.nextPageID
	log.Printf("[chrome] New page id=%d url=%s", pi.ID, url)
	return pi, nil
}

// ClosePage closes the tab with the given ID.
func (bm *BrowserManager) ClosePage(pageID int) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	ps, ok := bm.pages[pageID]
	if !ok {
		return fmt.Errorf("page %d not found", pageID)
	}
	if ps.cancel != nil {
		ps.cancel()
	}
	delete(bm.pages, pageID)
	if bm.currentPage == pageID {
		for id := range bm.pages {
			bm.currentPage = id
			break
		}
	}
	return nil
}
