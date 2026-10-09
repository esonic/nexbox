package chrome

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestExternalBrowserEndpointDiscovery(t *testing.T) {
	for _, endpoint := range []string{"", "http://127.0.0.1:9222", "https://chrome.example/cdp", "ws://localhost:9222/devtools/browser/id"} {
		if err := ValidateBrowserURL(endpoint); err != nil {
			t.Errorf("valid endpoint %q: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"localhost:9222", "file:///tmp/chrome", "http://", "http://user:pass@localhost:9222", "ws://localhost:9222/devtools/page/id", "ws://localhost/devtools/browser/"} {
		if err := ValidateBrowserURL(endpoint); err == nil {
			t.Errorf("invalid endpoint %q accepted", endpoint)
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			t.Errorf("discovery path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"webSocketDebuggerUrl":"ws://localhost:9999/devtools/browser/test-id"}`)
	}))
	defer ts.Close()
	for _, endpoint := range []string{ts.URL, ts.URL + "/", ts.URL + "/json/version"} {
		ws, err := resolveBrowserWebSocket(context.Background(), endpoint)
		want := "ws" + strings.TrimPrefix(ts.URL, "http") + "/devtools/browser/test-id"
		if err != nil || ws != want {
			t.Fatalf("resolve %s = %s, %v; want %s", endpoint, ws, err, want)
		}
	}
}

// TestExternalBrowserLifecycle uses an independently owned Chrome. Tool
// teardown must preserve its process, existing tabs and newly opened tabs.
func TestExternalBrowserLifecycle(t *testing.T) {
	execPath := os.Getenv("NEX_CHROME_EXEC_PATH")
	if execPath == "" {
		for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
			if path, err := exec.LookPath(name); err == nil {
				execPath = path
				break
			}
		}
	}
	if execPath == "" {
		t.Skip("Chrome is not installed")
	}
	owner, ownerCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer ownerCancel()
	// A workspace directory also works with snap Chromium's private /tmp.
	profile, err := os.MkdirTemp(".", "external-chrome-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(profile) })
	profile, err = filepath.Abs(profile)
	if err != nil {
		t.Fatal(err)
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(owner,
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(execPath),
			chromedp.UserDataDir(profile), chromedp.Flag("remote-debugging-port", "0"))...)
	defer cancelAlloc()
	ownerPage, cancelOwnerPage := chromedp.NewContext(alloc)
	defer func() {
		// Let the test-owned Chrome flush its profile before directory cleanup.
		_ = chromedp.Cancel(ownerPage)
		cancelOwnerPage()
	}()
	ts := startTestHTTP()
	defer ts.Close()
	if err := chromedp.Run(ownerPage, chromedp.Navigate(ts.URL)); err != nil {
		t.Fatal(err)
	}
	portFile, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://127.0.0.1:" + strings.Split(string(portFile), "\n")[0]
	original := chromedp.FromContext(ownerPage).Target.TargetID
	assertOriginalAlive := func() {
		t.Helper()
		var title string
		if err := chromedp.Run(ownerPage, chromedp.Title(&title)); err != nil || title != "E2E Test Page" {
			t.Fatalf("owner's tab changed/closed: title=%q err=%v", title, err)
		}
	}

	srv := NewServer("127.0.0.1:0", t.TempDir(), BrowserOptions{BrowserURL: endpoint})
	if err := srv.EnsureStarted(owner); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	pageCtx, err := srv.getPageCtx()
	if err != nil {
		t.Fatal(err)
	}
	if chromedp.FromContext(pageCtx).Target.TargetID != original {
		t.Fatal("external connection did not attach to the existing tab")
	}
	assertOriginalAlive()
	var attachedTitle string
	if err := chromedp.Run(pageCtx, chromedp.Title(&attachedTitle)); err != nil || attachedTitle != "E2E Test Page" {
		t.Fatalf("attached page is not usable after initialization: title=%q err=%v", attachedTitle, err)
	}
	// The user opens another tab after attachment: list_pages must discover it.
	userTab, userTabCancel := chromedp.NewContext(ownerPage)
	defer userTabCancel()
	if err := chromedp.Run(userTab, chromedp.Navigate("about:blank")); err != nil {
		t.Fatal(err)
	}
	pages, _, err := srv.browser.ListPages()
	if err != nil || len(pages) != 2 {
		t.Fatalf("user-opened tab not discovered: pages=%v err=%v", pages, err)
	}
	if !srv.ReleaseBrowser(time.Now().Add(time.Second)) {
		t.Fatal("idle connection was not released")
	}
	assertOriginalAlive()
	if _, err := srv.getPageCtx(); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	// Explicit close_page must still close the selected tab.
	created, err := srv.browser.NewPage("about:blank")
	if err != nil {
		t.Fatal(err)
	}
	createdCtx, err := srv.browser.CurrentPageContext()
	if err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(createdCtx, chromedp.Title(&attachedTitle)); err != nil {
		t.Fatalf("new page is not usable after navigation: %v", err)
	}
	if err := srv.browser.ClosePage(created.ID); err != nil {
		t.Fatal(err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertOriginalAlive()
	targets, err := chromedp.Targets(ownerPage)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, target := range targets {
		if target.Type == "page" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("shutdown changed tabs: count=%d, want 2", count)
	}

	// Parent cancellation must close the connection before page cleanup too.
	parent, cancelParent := context.WithCancel(owner)
	bm := NewBrowserManager(BrowserOptions{BrowserURL: endpoint})
	if err := bm.Launch(parent); err != nil {
		t.Fatal(err)
	}
	defer bm.Close()
	if _, err := bm.EnsureActivePage(); err != nil {
		t.Fatal(err)
	}
	cancelParent()
	deadline := time.Now().Add(5 * time.Second)
	for bm.IsLaunched() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if bm.IsLaunched() {
		t.Fatal("parent cancellation did not release external connection")
	}
	assertOriginalAlive()
}

func TestExternalBrowserUnavailableDoesNotLaunchChrome(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not a CDP endpoint", http.StatusServiceUnavailable)
	}))
	defer ts.Close()
	bm := NewBrowserManager(BrowserOptions{BrowserURL: ts.URL, ExecPath: "/must-never-launch"})
	if err := bm.Launch(context.Background()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected discovery failure, got %v", err)
	}
	if bm.IsLaunched() {
		t.Fatal("unavailable external browser reported as connected")
	}
}
