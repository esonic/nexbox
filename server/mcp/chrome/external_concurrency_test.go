package chrome

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/gorilla/websocket"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

// mockCDP supplies tab discovery and optionally stalls/disconnects a command.
func mockCDP(t *testing.T, interrupt string, disconnect bool, reached chan struct{}) string {
	t.Helper()
	up := websocket.Upgrader{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var req struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			if req.Method == interrupt {
				close(reached)
				if disconnect {
					return
				}
				continue // No reply: parent cancellation must interrupt the wait.
			}
			result := map[string]any{}
			if req.Method == "Target.getTargets" {
				result["targetInfos"] = []map[string]any{{"targetId": "original", "type": "page", "title": "original", "url": "about:blank", "attached": false}}
			}
			if err := conn.WriteJSON(map[string]any{"id": req.ID, "result": result}); err != nil {
				return
			}
		}
	}))
	t.Cleanup(ts.Close)
	return "ws" + strings.TrimPrefix(ts.URL, "http") + "/devtools/browser/test"
}

func TestExternalBrowserInterruptsLockedOperations(t *testing.T) {
	for _, operation := range []string{"attach", "new_page"} {
		for _, interruption := range []string{"cancel", "disconnect", "timeout"} {
			if operation == "new_page" && interruption == "timeout" {
				continue // Both operations use the same deadline helper.
			}
			t.Run(operation+"/"+interruption, func(t *testing.T) {
				disconnect := interruption == "disconnect"
				reached := make(chan struct{})
				method := "Target.attachToTarget"
				if operation == "new_page" {
					method = "Target.createTarget"
				}
				endpoint := mockCDP(t, method, disconnect, reached)
				parent, cancelParent := context.WithCancel(context.Background())
				defer cancelParent()
				bm := NewBrowserManager(BrowserOptions{BrowserURL: endpoint})
				if err := bm.Launch(parent); err != nil {
					t.Fatal(err)
				}
				// On a regression, this escape hatch avoids leaking a blocked goroutine.
				bm.mu.Lock()
				escapeRoot, escapeCancel := context.WithCancel(bm.browserCtx)
				bm.browserCtx = escapeRoot
				if operation == "attach" {
					bm.pages[bm.currentPage].ctx = escapeRootWithPage(bm.pages[bm.currentPage].ctx, escapeRoot)
				}
				bm.mu.Unlock()
				defer escapeCancel()
				done := make(chan error, 1)
				go func() {
					if operation == "attach" {
						_, err := bm.EnsureActivePage()
						done <- err
					} else {
						_, err := bm.NewPage("about:blank")
						done <- err
					}
				}()
				select {
				case <-reached:
				case <-time.After(2 * time.Second):
					escapeCancel()
					t.Fatal("CDP operation was not reached")
				}
				if interruption == "cancel" {
					cancelParent()
				}
				wait := 2 * time.Second
				if interruption == "timeout" {
					wait = 7 * time.Second
				}
				select {
				case err := <-done:
					if err == nil {
						t.Error("interrupted operation succeeded")
					}
				case <-time.After(wait):
					escapeCancel()
					<-done
					t.Error("CDP operation remained blocked after interruption")
				}
				bm.Close()
			})
		}
	}
}

// Preserve the page's chromedp Context while supplying a test escape signal.
func escapeRootWithPage(page context.Context, escape context.Context) context.Context {
	ctx, cancel := context.WithCancel(page)
	go func() {
		select {
		case <-escape.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx
}

func TestConcurrentBrowserInitializationPreservesUIDs(t *testing.T) {
	endpoint := mockCDP(t, "", false, nil)
	srv := NewServer("127.0.0.1:0", t.TempDir(), BrowserOptions{BrowserURL: endpoint})
	if err := srv.EnsureStarted(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	const count = 32
	start := make(chan struct{})
	errors := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if err := srv.ensureBrowser(); err != nil {
				errors <- err
				return
			}
			// Model a UID published by a completed first-use tool call. Other
			// callers on the same connection must not invalidate that mapping.
			srv.snapshot.mu.Lock()
			srv.snapshot.uidToNode[fmt.Sprintf("uid-%d", i)] = cdp.BackendNodeID(i + 1)
			srv.snapshot.mu.Unlock()
		}(i)
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	for i := 0; i < count; i++ {
		if node, err := srv.snapshot.ResolveUID(fmt.Sprintf("uid-%d", i)); err != nil || node != cdp.BackendNodeID(i+1) {
			t.Errorf("first-use UID lost: node=%d err=%v", node, err)
		}
	}
	if !srv.ReleaseBrowser(time.Now().Add(time.Second)) {
		t.Fatal("connection was not released")
	}
	if _, err := srv.snapshot.ResolveUID("uid-0"); err == nil {
		t.Fatal("old UID survived a connection release")
	}
}

// Request cancellation must interrupt target setup without assigning the target
// event loop to that request. In external mode only the connection is released.
func TestBrowserSetupHonorsRequestCancellation(t *testing.T) {
	for _, operation := range []string{"new_page", "get_page_info"} {
		for _, interruption := range []string{"timeout", "cancel"} {
			t.Run(operation+"/"+interruption, func(t *testing.T) {
				method := "Target.createTarget"
				if operation == "get_page_info" {
					method = "Target.attachToTarget"
				}
				reached := make(chan struct{})
				endpoint := mockCDP(t, method, false, reached)
				srv := NewServer("127.0.0.1:0", t.TempDir(), BrowserOptions{BrowserURL: endpoint})
				if err := srv.EnsureStarted(context.Background()); err != nil {
					t.Fatal(err)
				}
				defer srv.Shutdown(context.Background())
				if err := srv.ensureBrowser(); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				args := map[string]any{}
				if interruption == "timeout" {
					args["timeout"] = 100
				}
				done := make(chan error, 1)
				go func() {
					result, err := srv.mcpServer.ListTools()[operation].Handler(ctx, gomcp.CallToolRequest{Params: gomcp.CallToolParams{Name: operation, Arguments: args}})
					if err == nil && (result == nil || !result.IsError) {
						err = fmt.Errorf("interrupted tool succeeded: %+v", result)
					}
					done <- err
				}()
				select {
				case <-reached:
				case <-time.After(time.Second):
					t.Fatal("target setup was not reached")
				}
				if interruption == "cancel" {
					cancel()
				}
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("request cancellation did not interrupt target setup")
				}
			})
		}
	}
}
