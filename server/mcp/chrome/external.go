package chrome

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// ValidateBrowserURL accepts a CDP HTTP endpoint or browser websocket URL.
// An empty URL selects NexBox-managed Chrome.
func ValidateBrowserURL(endpoint string) error {
	if endpoint == "" {
		return nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("Chrome address must be an HTTP(S) endpoint or a browser WS(S) URL")
	}
	switch u.Scheme {
	case "http", "https":
	case "ws", "wss":
		if !strings.HasPrefix(u.Path, "/devtools/browser/") || strings.TrimPrefix(u.Path, "/devtools/browser/") == "" {
			return fmt.Errorf("Chrome websocket address must include /devtools/browser/<id>")
		}
	default:
		return fmt.Errorf("Chrome address must use http, https, ws or wss")
	}
	return nil
}

func resolveBrowserWebSocket(ctx context.Context, endpoint string) (string, error) {
	if err := ValidateBrowserURL(endpoint); err != nil {
		return "", err
	}
	u, _ := url.Parse(endpoint)
	if u.Scheme == "ws" || u.Scheme == "wss" {
		return endpoint, nil
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/json/version") {
		u.Path += "/json/version"
	}
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("discover Chrome CDP endpoint: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Chrome CDP discovery returned HTTP %d", resp.StatusCode)
	}
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&version); err != nil {
		return "", fmt.Errorf("decode Chrome CDP discovery: %w", err)
	}
	ws, err := url.Parse(version.WebSocketDebuggerURL)
	if err != nil || ws.Hostname() == "" || (ws.Scheme != "ws" && ws.Scheme != "wss") {
		return "", fmt.Errorf("Chrome CDP discovery did not return a browser websocket URL")
	}
	// Use the caller's reachable host (important for SSH tunnels), retaining
	// Chrome's browser ID. Chrome may advertise localhost from its own machine.
	ws.Host = u.Host
	if u.Scheme == "https" {
		ws.Scheme = "wss"
	}
	return ws.String(), ValidateBrowserURL(ws.String())
}

// attachLocked uses a separately cancellable CDP transport rather than the
// v0.14.2 RemoteAllocator: that allocator closes attached tabs during cleanup.
// Closing the transport before page contexts guarantees a detach-only teardown.
func (bm *BrowserManager) attachLocked(parent context.Context) error {
	wsURL, err := resolveBrowserWebSocket(parent, bm.opts.BrowserURL)
	if err != nil {
		return fmt.Errorf("connect external Chrome: %w", err)
	}
	transportCtx, transportCancel := context.WithCancel(context.Background())
	remote, err := chromedp.NewBrowser(transportCtx, wsURL, chromedp.WithDialTimeout(5*time.Second))
	if err != nil {
		transportCancel()
		return fmt.Errorf("connect external Chrome: %w", err)
	}
	// No parent cancellation may reach a page until the transport is closed.
	// Supplying an existing Browser also prevents any ExecAllocator launch.
	root, rootCancel := chromedp.NewContext(context.WithoutCancel(parent), func(c *chromedp.Context) {
		c.Browser = remote
	})
	// Both the exit watcher and explicit Close may cancel this root. Its
	// chromedp cancel function waits on a one-shot allocator channel.
	rootCancel = sync.OnceFunc(rootCancel)
	bm.browserCtx, bm.allocCtx = root, root
	bm.allocCancel = rootCancel
	bm.transportCancel, bm.remoteBrowser = transportCancel, remote
	if err := bm.syncExternalPagesLocked(); err != nil {
		bm.closeLocked()
		return fmt.Errorf("connect external Chrome: %w", err)
	}
	bm.parentCtx = parent
	bm.launched = true
	bm.generation++
	gen := bm.generation
	go func() {
		select {
		case <-parent.Done():
		case <-remote.LostConnection:
		}
		// Unblock CDP operations before waiting for the manager lock: an
		// attach/navigation may be holding it while awaiting a response.
		// The socket must be gone before cancelling any attached page.
		transportCancel()
		<-remote.LostConnection
		rootCancel()
		bm.mu.Lock()
		defer bm.mu.Unlock()
		if bm.generation == gen && bm.launched {
			bm.closeLocked()
		}
	}()
	log.Printf("[chrome] Connected to external Chrome at %s", bm.opts.BrowserURL)
	return nil
}

// syncExternalPagesLocked discovers user-opened tabs without navigating or
// creating any tabs. Contexts attach lazily when a page is actually used.
func (bm *BrowserManager) syncExternalPagesLocked() error {
	ctx, cancel := context.WithTimeout(bm.browserCtx, 3*time.Second)
	defer cancel()
	targets, err := chromedp.Targets(ctx)
	if err != nil {
		return fmt.Errorf("list external Chrome tabs: %w", err)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].TargetID < targets[j].TargetID })
	live := make(map[target.ID]*target.Info)
	known := make(map[target.ID]bool)
	for _, t := range targets {
		if t.Type == "page" {
			live[t.TargetID] = t
		}
	}
	for id, ps := range bm.pages {
		if t, ok := live[ps.info.TargetID]; ok {
			ps.info.URL, ps.info.Title = t.URL, t.Title
			known[t.TargetID] = true
		} else {
			if ps.cancel != nil {
				ps.cancel() // target is already gone
			}
			delete(bm.pages, id)
		}
	}
	for _, t := range targets {
		if t.Type != "page" || known[t.TargetID] {
			continue
		}
		pageCtx, pageCancel := chromedp.NewContext(bm.browserCtx, chromedp.WithTargetID(t.TargetID))
		bm.nextPageID++
		bm.pages[bm.nextPageID] = &pageState{
			info: PageInfo{ID: bm.nextPageID, TargetID: t.TargetID, URL: t.URL, Title: t.Title},
			ctx:  pageCtx, cancel: pageCancel,
		}
	}
	if _, ok := bm.pages[bm.currentPage]; !ok {
		bm.currentPage = 0
		for id := range bm.pages {
			if bm.currentPage == 0 || id < bm.currentPage {
				bm.currentPage = id
			}
		}
	}
	return nil
}

func (bm *BrowserManager) closeExternalPageLocked(ps *pageState) error {
	ctx, cancel := context.WithTimeout(bm.browserCtx, 3*time.Second)
	defer cancel()
	if err := target.CloseTarget(ps.info.TargetID).Do(cdp.WithExecutor(ctx, bm.remoteBrowser)); err != nil {
		return fmt.Errorf("close external Chrome tab: %w", err)
	}
	if ps.cancel != nil {
		ps.cancel()
	}
	return nil
}

// runPageLocked bounds external operations without giving the first Run a
// short-lived context: chromedp ties the target's event loop to that context.
// On timeout, dropping the transport lets the exit watcher cancel the root
// before acquiring bm.mu, preserving tabs and unblocking the operation.
func (bm *BrowserManager) runPageLocked(ctx context.Context, timeout time.Duration, actions ...chromedp.Action) error {
	if bm.opts.BrowserURL == "" {
		return chromedp.Run(ctx, actions...)
	}
	timer := time.AfterFunc(timeout, bm.transportCancel)
	defer timer.Stop()
	return chromedp.Run(ctx, actions...)
}
