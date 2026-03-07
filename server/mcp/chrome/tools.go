package chrome

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

// ---------- take_snapshot ----------

func (s *Server) handleTakeSnapshot(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	pageCtx, err := s.getPageCtx()
	if err != nil {
		return errResult(err.Error()), nil
	}

	text, err := s.snapshot.TakeSnapshot(pageCtx)
	if err != nil {
		return errResult(fmt.Sprintf("snapshot failed: %v", err)), nil
	}

	args := req.GetArguments()
	if fp, _ := args["filePath"].(string); fp != "" {
		if err := os.WriteFile(fp, []byte(text), 0644); err != nil {
			return errResult(fmt.Sprintf("write snapshot file: %v", err)), nil
		}
		return txtResult(fmt.Sprintf("Snapshot saved to %s", fp)), nil
	}
	return txtResult(text), nil
}

// ---------- navigate_page ----------

func (s *Server) handleNavigatePage(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	navType, _ := args["type"].(string)
	if navType == "" {
		navType = "url"
	}

	pageCtx, err := s.getPageCtx()
	if err != nil {
		return errResult(err.Error()), nil
	}

	switch navType {
	case "url":
		url, _ := args["url"].(string)
		if url == "" {
			return errResult("url is required when type is 'url'"), nil
		}
		if err := chromedp.Run(pageCtx, chromedp.Navigate(url)); err != nil {
			return errResult(fmt.Sprintf("navigate to %s: %v", url, err)), nil
		}
		return txtResult(fmt.Sprintf("Navigated to %s", url)), nil

	case "back":
		if err := chromedp.Run(pageCtx, chromedp.NavigateBack()); err != nil {
			return errResult(fmt.Sprintf("navigate back: %v", err)), nil
		}
		return txtResult("Navigated back"), nil

	case "forward":
		if err := chromedp.Run(pageCtx, chromedp.NavigateForward()); err != nil {
			return errResult(fmt.Sprintf("navigate forward: %v", err)), nil
		}
		return txtResult("Navigated forward"), nil

	case "reload":
		if err := chromedp.Run(pageCtx, chromedp.Reload()); err != nil {
			return errResult(fmt.Sprintf("reload: %v", err)), nil
		}
		return txtResult("Page reloaded"), nil

	default:
		return errResult(fmt.Sprintf("unknown navigation type: %s", navType)), nil
	}
}

// ---------- click ----------

func (s *Server) handleClick(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	uid, _ := args["uid"].(string)
	if uid == "" {
		return errResult("uid is required"), nil
	}
	dblClick, _ := args["dblClick"].(bool)

	pageCtx, err := s.getPageCtx()
	if err != nil {
		return errResult(err.Error()), nil
	}

	x, y, err := s.snapshot.ElementCenter(pageCtx, uid)
	if err != nil {
		return errResult(fmt.Sprintf("resolve element %s: %v", uid, err)), nil
	}

	clickCount := 1
	if dblClick {
		clickCount = 2
	}

	if err := chromedp.Run(pageCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := input.DispatchMouseEvent(input.MouseMoved, x, y).Do(ctx); err != nil {
			return err
		}
		if err := input.DispatchMouseEvent(input.MousePressed, x, y).
			WithButton(input.Left).WithClickCount(int64(clickCount)).Do(ctx); err != nil {
			return err
		}
		return input.DispatchMouseEvent(input.MouseReleased, x, y).
			WithButton(input.Left).WithClickCount(int64(clickCount)).Do(ctx)
	})); err != nil {
		return errResult(fmt.Sprintf("click %s: %v", uid, err)), nil
	}

	time.Sleep(300 * time.Millisecond)
	action := "Clicked"
	if dblClick {
		action = "Double-clicked"
	}
	return txtResult(fmt.Sprintf("%s element uid=%s", action, uid)), nil
}

// ---------- fill ----------

func (s *Server) handleFill(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	uid, _ := args["uid"].(string)
	value, _ := args["value"].(string)
	if uid == "" {
		return errResult("uid is required"), nil
	}

	pageCtx, err := s.getPageCtx()
	if err != nil {
		return errResult(err.Error()), nil
	}

	objID, err := s.snapshot.ResolveNodeObject(pageCtx, uid)
	if err != nil {
		return errResult(fmt.Sprintf("resolve element: %v", err)), nil
	}

	if err := chromedp.Run(pageCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		valueJSON, _ := json.Marshal(value)
		_, _, err := runtime.CallFunctionOn(fmt.Sprintf(`function(){
			this.scrollIntoView({block:"center"});
			this.focus();
			this.value=%s;
			this.dispatchEvent(new Event('input',{bubbles:true}));
			this.dispatchEvent(new Event('change',{bubbles:true}));
		}`, string(valueJSON))).WithObjectID(objID).Do(ctx)
		return err
	})); err != nil {
		return errResult(fmt.Sprintf("fill %s: %v", uid, err)), nil
	}
	return txtResult(fmt.Sprintf("Filled element uid=%s with %q", uid, value)), nil
}

// ---------- type_text ----------

func (s *Server) handleTypeText(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	text, _ := args["text"].(string)
	if text == "" {
		return errResult("text is required"), nil
	}

	pageCtx, err := s.getPageCtx()
	if err != nil {
		return errResult(err.Error()), nil
	}

	if err := chromedp.Run(pageCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		for _, ch := range text {
			keyStr := string(ch)
			if err := input.DispatchKeyEvent(input.KeyDown).
				WithKey(keyStr).WithText(keyStr).Do(ctx); err != nil {
				return err
			}
			if err := input.DispatchKeyEvent(input.KeyUp).
				WithKey(keyStr).Do(ctx); err != nil {
				return err
			}
		}
		return nil
	})); err != nil {
		return errResult(fmt.Sprintf("type_text: %v", err)), nil
	}

	submitKey, _ := args["submitKey"].(string)
	if submitKey != "" {
		if err := dispatchKey(pageCtx, submitKey); err != nil {
			return errResult(fmt.Sprintf("submit key %q: %v", submitKey, err)), nil
		}
	}
	return txtResult(fmt.Sprintf("Typed %d characters", len(text))), nil
}

// ---------- press_key ----------

func (s *Server) handlePressKey(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	key, _ := args["key"].(string)
	if key == "" {
		return errResult("key is required"), nil
	}

	pageCtx, err := s.getPageCtx()
	if err != nil {
		return errResult(err.Error()), nil
	}

	if err := dispatchKey(pageCtx, key); err != nil {
		return errResult(fmt.Sprintf("press_key %q: %v", key, err)), nil
	}
	return txtResult(fmt.Sprintf("Pressed key: %s", key)), nil
}

// ---------- hover ----------

func (s *Server) handleHover(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	uid, _ := args["uid"].(string)
	if uid == "" {
		return errResult("uid is required"), nil
	}

	pageCtx, err := s.getPageCtx()
	if err != nil {
		return errResult(err.Error()), nil
	}

	x, y, err := s.snapshot.ElementCenter(pageCtx, uid)
	if err != nil {
		return errResult(fmt.Sprintf("resolve element: %v", err)), nil
	}

	if err := chromedp.Run(pageCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		return input.DispatchMouseEvent(input.MouseMoved, x, y).Do(ctx)
	})); err != nil {
		return errResult(fmt.Sprintf("hover: %v", err)), nil
	}
	return txtResult(fmt.Sprintf("Hovered over element uid=%s", uid)), nil
}

// ---------- take_screenshot ----------

func (s *Server) handleTakeScreenshot(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	fullPage, _ := args["fullPage"].(bool)
	uid, _ := args["uid"].(string)
	filePath, _ := args["filePath"].(string)

	pageCtx, err := s.getPageCtx()
	if err != nil {
		return errResult(err.Error()), nil
	}

	if filePath == "" {
		if err := os.MkdirAll(s.screenshotDir, 0755); err != nil {
			return errResult(fmt.Sprintf("create screenshot dir: %v", err)), nil
		}
		filePath = filepath.Join(s.screenshotDir,
			fmt.Sprintf("screenshot_%d.png", time.Now().UnixMilli()))
	}

	var buf []byte
	if uid != "" {
		objID, err := s.snapshot.ResolveNodeObject(pageCtx, uid)
		if err != nil {
			return errResult(fmt.Sprintf("resolve element: %v", err)), nil
		}
		if err := chromedp.Run(pageCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			res, _, err := runtime.CallFunctionOn(`function(){
				this.scrollIntoView({block:"center",inline:"center"});
				var r=this.getBoundingClientRect();
				return {x:r.x,y:r.y,width:r.width,height:r.height};
			}`).WithObjectID(objID).WithReturnByValue(true).Do(ctx)
			if err != nil {
				return err
			}
			var clip struct{ X, Y, Width, Height float64 }
			if err := parseJSON(res.Value, &clip); err != nil {
				return err
			}
			data, err := page.CaptureScreenshot().
				WithFormat(page.CaptureScreenshotFormatPng).
				WithClip(&page.Viewport{
					X: clip.X, Y: clip.Y,
					Width: clip.Width, Height: clip.Height,
					Scale: 1,
				}).Do(ctx)
			if err != nil {
				return err
			}
			buf = data
			return nil
		})); err != nil {
			return errResult(fmt.Sprintf("screenshot element: %v", err)), nil
		}
	} else if fullPage {
		if err := chromedp.Run(pageCtx, chromedp.FullScreenshot(&buf, 90)); err != nil {
			return errResult(fmt.Sprintf("full screenshot: %v", err)), nil
		}
	} else {
		if err := chromedp.Run(pageCtx, chromedp.CaptureScreenshot(&buf)); err != nil {
			return errResult(fmt.Sprintf("screenshot: %v", err)), nil
		}
	}

	if err := os.WriteFile(filePath, buf, 0644); err != nil {
		return errResult(fmt.Sprintf("save screenshot: %v", err)), nil
	}
	return txtResult(fmt.Sprintf("Screenshot saved to %s (%d bytes)", filePath, len(buf))), nil
}

// ---------- evaluate_script ----------

func (s *Server) handleEvaluateScript(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	fn, _ := args["function"].(string)
	if fn == "" {
		return errResult("function is required"), nil
	}

	pageCtx, err := s.getPageCtx()
	if err != nil {
		return errResult(err.Error()), nil
	}

	var uidArgs []struct {
		UID string `json:"uid"`
	}
	if rawArgs, ok := args["args"]; ok {
		b, _ := json.Marshal(rawArgs)
		_ = json.Unmarshal(b, &uidArgs)
	}

	var resultJSON string
	if err := chromedp.Run(pageCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		if len(uidArgs) == 0 {
			res, _, err := runtime.Evaluate(fn + "()").
				WithAwaitPromise(true).
				WithReturnByValue(true).Do(ctx)
			if err != nil {
				return err
			}
			resultJSON = string(res.Value)
			return nil
		}

		callArgs := make([]*runtime.CallArgument, 0, len(uidArgs))
		for _, ua := range uidArgs {
			objID, err := s.snapshot.ResolveNodeObject(pageCtx, ua.UID)
			if err != nil {
				return fmt.Errorf("resolve arg uid %s: %w", ua.UID, err)
			}
			callArgs = append(callArgs, &runtime.CallArgument{ObjectID: objID})
		}

		windowObj, _, err := runtime.Evaluate("window").Do(ctx)
		if err != nil {
			return err
		}
		res, _, err := runtime.CallFunctionOn(fn).
			WithObjectID(windowObj.ObjectID).
			WithArguments(callArgs).
			WithAwaitPromise(true).
			WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		resultJSON = string(res.Value)
		return nil
	})); err != nil {
		return errResult(fmt.Sprintf("evaluate: %v", err)), nil
	}

	return txtResult(fmt.Sprintf("```json\n%s\n```", resultJSON)), nil
}

// ---------- wait_for ----------

func (s *Server) handleWaitFor(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	var texts []string
	if raw, ok := args["text"]; ok {
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &texts)
	}
	if len(texts) == 0 {
		return errResult("text array is required"), nil
	}

	timeoutMs := 30000.0
	if v, ok := args["timeout"].(float64); ok && v > 0 {
		timeoutMs = v
	}

	pageCtx, err := s.getPageCtx()
	if err != nil {
		return errResult(err.Error()), nil
	}

	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var foundText string
	for {
		var body string
		if err := chromedp.Run(pageCtx, chromedp.TextContent("body", &body, chromedp.ByQuery)); err == nil {
			for _, t := range texts {
				if strings.Contains(body, t) {
					foundText = t
					break
				}
			}
		}
		if foundText != "" {
			break
		}
		if time.Now().After(deadline) {
			return errResult(fmt.Sprintf("timeout after %dms waiting for text: %v", int(timeoutMs), texts)), nil
		}
		<-ticker.C
	}

	snap, _ := s.snapshot.TakeSnapshot(pageCtx)
	return txtResult(fmt.Sprintf("Found text %q on page.\n\nPage snapshot:\n%s", foundText, snap)), nil
}

// ---------- list_pages ----------

func (s *Server) handleListPages(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	if err := s.ensureBrowser(); err != nil {
		return errResult(err.Error()), nil
	}
	pages, current, err := s.browser.ListPages()
	if err != nil {
		return errResult(err.Error()), nil
	}

	var sb strings.Builder
	sb.WriteString("Open pages:\n")
	for _, p := range pages {
		marker := "  "
		if p.ID == current {
			marker = "▶ "
		}
		sb.WriteString(fmt.Sprintf("%sid=%d  %s  %s\n", marker, p.ID, p.URL, p.Title))
	}
	return txtResult(sb.String()), nil
}

// ---------- select_page ----------

func (s *Server) handleSelectPage(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	if err := s.ensureBrowser(); err != nil {
		return errResult(err.Error()), nil
	}
	args := req.GetArguments()
	pageID, ok := args["pageId"].(float64)
	if !ok {
		return errResult("pageId is required"), nil
	}
	if err := s.browser.SelectPage(int(pageID)); err != nil {
		return errResult(err.Error()), nil
	}
	return txtResult(fmt.Sprintf("Switched to page %d", int(pageID))), nil
}

// ---------- new_page ----------

func (s *Server) handleNewPage(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	if err := s.ensureBrowser(); err != nil {
		return errResult(err.Error()), nil
	}
	args := req.GetArguments()
	url, _ := args["url"].(string)
	pi, err := s.browser.NewPage(url)
	if err != nil {
		return errResult(err.Error()), nil
	}
	return txtResult(fmt.Sprintf("Created new page id=%d, navigated to %s", pi.ID, pi.URL)), nil
}

// ---------- get_page_info ----------

func (s *Server) handleGetPageInfo(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	pageCtx, err := s.getPageCtx()
	if err != nil {
		return errResult(err.Error()), nil
	}
	var url, title string
	if err := chromedp.Run(pageCtx,
		chromedp.Location(&url),
		chromedp.Title(&title),
	); err != nil {
		return errResult(fmt.Sprintf("get page info: %v", err)), nil
	}
	return txtResult(fmt.Sprintf("URL: %s\nTitle: %s", url, title)), nil
}

// ---------- helpers ----------

func dispatchKey(pageCtx context.Context, combo string) error {
	return chromedp.Run(pageCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		parts := strings.Split(combo, "+")
		key := parts[len(parts)-1]
		modifiers := parts[:len(parts)-1]

		var mod int64
		for _, m := range modifiers {
			switch strings.ToLower(m) {
			case "control", "ctrl":
				mod |= 1 << 1
			case "shift":
				mod |= 1 << 3
			case "alt":
				mod |= 1
			case "meta", "command", "cmd":
				mod |= 1 << 2
			}
		}

		for _, m := range modifiers {
			if err := input.DispatchKeyEvent(input.KeyDown).WithKey(normalizeKeyName(m)).Do(ctx); err != nil {
				return err
			}
		}
		if err := input.DispatchKeyEvent(input.KeyDown).
			WithKey(normalizeKeyName(key)).WithModifiers(input.Modifier(mod)).Do(ctx); err != nil {
			return err
		}
		if err := input.DispatchKeyEvent(input.KeyUp).
			WithKey(normalizeKeyName(key)).WithModifiers(input.Modifier(mod)).Do(ctx); err != nil {
			return err
		}
		for i := len(modifiers) - 1; i >= 0; i-- {
			if err := input.DispatchKeyEvent(input.KeyUp).WithKey(normalizeKeyName(modifiers[i])).Do(ctx); err != nil {
				return err
			}
		}
		return nil
	}))
}

func normalizeKeyName(name string) string {
	switch strings.ToLower(name) {
	case "enter", "return":
		return "Enter"
	case "tab":
		return "Tab"
	case "escape", "esc":
		return "Escape"
	case "backspace":
		return "Backspace"
	case "delete":
		return "Delete"
	case "space":
		return " "
	case "arrowup", "up":
		return "ArrowUp"
	case "arrowdown", "down":
		return "ArrowDown"
	case "arrowleft", "left":
		return "ArrowLeft"
	case "arrowright", "right":
		return "ArrowRight"
	case "control", "ctrl":
		return "Control"
	case "shift":
		return "Shift"
	case "alt":
		return "Alt"
	case "meta", "command", "cmd":
		return "Meta"
	case "home":
		return "Home"
	case "end":
		return "End"
	case "pageup":
		return "PageUp"
	case "pagedown":
		return "PageDown"
	default:
		if len(name) == 1 {
			return name
		}
		return name
	}
}

func parseJSON(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

func txtResult(text string) *gomcp.CallToolResult {
	return &gomcp.CallToolResult{
		Content: []gomcp.Content{
			gomcp.TextContent{Type: "text", Text: text},
		},
	}
}

func errResult(msg string) *gomcp.CallToolResult {
	return &gomcp.CallToolResult{
		Content: []gomcp.Content{
			gomcp.TextContent{Type: "text", Text: "Error: " + msg},
		},
		IsError: true,
	}
}

// imageResult returns an MCP result with a base64-encoded image.
// Unused for now (we save to file), but kept for future use.
func imageResult(data []byte, mimeType string) *gomcp.CallToolResult {
	return &gomcp.CallToolResult{
		Content: []gomcp.Content{
			gomcp.ImageContent{
				Type:     "image",
				MIMEType: mimeType,
				Data:     base64.StdEncoding.EncodeToString(data),
			},
		},
	}
}

// Keep compiler happy
var _ = imageResult
var _ = log.Printf
