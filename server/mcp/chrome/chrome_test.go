package chrome

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const testHTML = `<!DOCTYPE html>
<html>
<head><title>E2E Test Page</title></head>
<body>
  <h1>Hello E2E</h1>
  <nav aria-label="Main Navigation">
    <a href="/about">About</a>
    <a href="/contact">Contact</a>
  </nav>
  <main>
    <form id="loginForm">
      <label for="email">Email:</label>
      <input id="email" type="email" placeholder="you@example.com" />
      <label for="pass">Password:</label>
      <input id="pass" type="password" />
      <button type="submit" id="submitBtn">Sign In</button>
    </form>
    <p id="status">Ready</p>
  </main>
  <script>
    document.getElementById('loginForm').addEventListener('submit', function(e) {
      e.preventDefault();
      document.getElementById('status').textContent =
        'Submitted: ' + document.getElementById('email').value;
    });
  </script>
</body>
</html>`

func startTestHTTP() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, testHTML)
	}))
}

// TestBrowserLaunchAndNavigate checks that Chrome starts and can load a page.
func TestBrowserLaunchAndNavigate(t *testing.T) {
	ts := startTestHTTP()
	defer ts.Close()

	bm := NewBrowserManager(BrowserOptions{Headless: true})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := bm.Launch(ctx); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer bm.Close()

	pageCtx, err := bm.CurrentPageContext()
	if err != nil {
		t.Fatalf("CurrentPageContext: %v", err)
	}

	if err := chromedp.Run(pageCtx, chromedp.Navigate(ts.URL)); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	var title string
	if err := chromedp.Run(pageCtx, chromedp.Title(&title)); err != nil {
		t.Fatalf("Title: %v", err)
	}
	if title != "E2E Test Page" {
		t.Errorf("title = %q, want 'E2E Test Page'", title)
	}

	pages, current, err := bm.ListPages()
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	t.Logf("Pages=%d current=%d title=%q", len(pages), current, title)
}

// TestSnapshotAndUID verifies accessibility tree capture and UID resolution.
func TestSnapshotAndUID(t *testing.T) {
	ts := startTestHTTP()
	defer ts.Close()

	bm := NewBrowserManager(BrowserOptions{Headless: true})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := bm.Launch(ctx); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer bm.Close()

	pageCtx, _ := bm.CurrentPageContext()
	if err := chromedp.Run(pageCtx, chromedp.Navigate(ts.URL)); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	sm := NewSnapshotManager()
	snap, err := sm.TakeSnapshot(pageCtx)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}

	t.Logf("--- Snapshot ---\n%s", snap)

	// Check structure
	checks := []string{"uid=", "[heading]", "[button]", "[textbox]"}
	for _, c := range checks {
		if !strings.Contains(snap, c) {
			t.Errorf("snapshot missing %q", c)
		}
	}

	// Find the email textbox UID
	emailUID := findUID(snap, "[textbox]", "Email")
	if emailUID == "" {
		emailUID = findUID(snap, "[textbox]", "email")
	}
	if emailUID == "" {
		t.Fatal("could not find email textbox UID")
	}
	t.Logf("Email UID: %s", emailUID)

	// Resolve UID → BackendNodeID
	nodeID, err := sm.ResolveUID(emailUID)
	if err != nil {
		t.Fatalf("ResolveUID: %v", err)
	}
	t.Logf("BackendNodeID: %d", nodeID)

	// Resolve to RemoteObject
	objID, err := sm.ResolveNodeObject(pageCtx, emailUID)
	if err != nil {
		t.Fatalf("ResolveNodeObject: %v", err)
	}
	t.Logf("RemoteObjectID: %s", objID)

	// Get element center
	x, y, err := sm.ElementCenter(pageCtx, emailUID)
	if err != nil {
		t.Fatalf("ElementCenter: %v", err)
	}
	if x <= 0 || y <= 0 {
		t.Errorf("center should be positive: (%.1f, %.1f)", x, y)
	}
	t.Logf("Center: (%.1f, %.1f)", x, y)
}

// TestFullWorkflow tests the complete E2E workflow:
// navigate → snapshot → fill → click → evaluate → screenshot
func TestFullWorkflow(t *testing.T) {
	ts := startTestHTTP()
	defer ts.Close()
	tmpDir := t.TempDir()

	bm := NewBrowserManager(BrowserOptions{Headless: true})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := bm.Launch(ctx); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer bm.Close()

	pageCtx, _ := bm.CurrentPageContext()
	sm := NewSnapshotManager()

	// Step 1: Navigate
	t.Log("Step 1: Navigate")
	if err := chromedp.Run(pageCtx, chromedp.Navigate(ts.URL)); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Step 2: Take snapshot
	t.Log("Step 2: Take snapshot")
	snap, err := sm.TakeSnapshot(pageCtx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	emailUID := findUID(snap, "[textbox]", "Email")
	if emailUID == "" {
		emailUID = findUID(snap, "[textbox]", "email")
	}
	buttonUID := findUID(snap, "[button]", "Sign In")
	if emailUID == "" || buttonUID == "" {
		t.Logf("Snapshot:\n%s", snap)
		t.Fatalf("UIDs not found: email=%q button=%q", emailUID, buttonUID)
	}
	t.Logf("Found UIDs: email=%s button=%s", emailUID, buttonUID)

	// Step 3: Fill email input
	t.Log("Step 3: Fill email input")
	objID, err := sm.ResolveNodeObject(pageCtx, emailUID)
	if err != nil {
		t.Fatalf("resolve email: %v", err)
	}
	if err := chromedp.Run(pageCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, _, err := runtime.CallFunctionOn(`function(){
			this.focus();
			this.value="test@example.com";
			this.dispatchEvent(new Event('input',{bubbles:true}));
			this.dispatchEvent(new Event('change',{bubbles:true}));
		}`).WithObjectID(objID).Do(ctx)
		return err
	})); err != nil {
		t.Fatalf("fill: %v", err)
	}

	// Step 4: Click submit button
	t.Log("Step 4: Click submit")
	bx, by, err := sm.ElementCenter(pageCtx, buttonUID)
	if err != nil {
		t.Fatalf("button center: %v", err)
	}
	if err := chromedp.Run(pageCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := input.DispatchMouseEvent(input.MouseMoved, bx, by).Do(ctx); err != nil {
			return err
		}
		if err := input.DispatchMouseEvent(input.MousePressed, bx, by).
			WithButton(input.Left).WithClickCount(1).Do(ctx); err != nil {
			return err
		}
		return input.DispatchMouseEvent(input.MouseReleased, bx, by).
			WithButton(input.Left).WithClickCount(1).Do(ctx)
	})); err != nil {
		t.Fatalf("click: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Step 5: Evaluate JS to check result
	t.Log("Step 5: Evaluate result")
	var statusText string
	if err := chromedp.Run(pageCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		res, _, err := runtime.Evaluate(`document.getElementById('status').textContent`).
			WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(res.Value), &statusText); err != nil {
			return fmt.Errorf("unmarshal status: %w", err)
		}
		return nil
	})); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	t.Logf("Status: %q", statusText)
	if !strings.Contains(statusText, "Submitted: test@example.com") {
		t.Errorf("expected 'Submitted: test@example.com', got %q", statusText)
	}

	// Step 6: Screenshot
	t.Log("Step 6: Screenshot")
	screenshotPath := filepath.Join(tmpDir, "result.png")
	var buf []byte
	if err := chromedp.Run(pageCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		buf, err = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng).Do(ctx)
		return err
	})); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if err := os.WriteFile(screenshotPath, buf, 0644); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}
	info, _ := os.Stat(screenshotPath)
	t.Logf("Screenshot: %s (%d bytes)", screenshotPath, info.Size())

	// Step 7: New page
	t.Log("Step 7: Multi-page")
	pi, err := bm.NewPage(ts.URL)
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	pages, current, _ := bm.ListPages()
	t.Logf("Pages=%d current=%d newPageID=%d", len(pages), current, pi.ID)
	if len(pages) < 2 {
		t.Error("expected >= 2 pages")
	}
}

// TestMCPServerLifecycle tests the MCP server startup and tool registration.
func TestMCPServerLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	srv := NewServer("127.0.0.1:0", tmpDir, BrowserOptions{Headless: true})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.EnsureStarted(ctx); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	defer func() { _ = srv.Shutdown(ctx) }()

	url := srv.URL()
	if url == "" || !strings.Contains(url, "/mcp") {
		t.Fatalf("bad URL: %q", url)
	}
	t.Logf("Server URL: %s", url)

	names := srv.ToolNames()
	t.Logf("Tools (%d): %v", len(names), names)

	expectedTools := []string{
		"mcp__chrome__take_snapshot",
		"mcp__chrome__navigate_page",
		"mcp__chrome__click",
		"mcp__chrome__fill",
		"mcp__chrome__take_screenshot",
		"mcp__chrome__evaluate_script",
	}
	for _, exp := range expectedTools {
		found := false
		for _, n := range names {
			if n == exp {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing tool: %s", exp)
		}
	}

	// Idempotent start
	if err := srv.EnsureStarted(ctx); err != nil {
		t.Fatalf("EnsureStarted (2nd call): %v", err)
	}
}

// findUID extracts a UID from a snapshot line matching the given role and name.
func findUID(snap, role, name string) string {
	for _, line := range strings.Split(snap, "\n") {
		if strings.Contains(line, role) && strings.Contains(line, name) {
			for _, part := range strings.Fields(line) {
				if strings.HasPrefix(part, "uid=") {
					return strings.TrimPrefix(part, "uid=")
				}
			}
		}
	}
	return ""
}
