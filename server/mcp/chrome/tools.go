package chrome

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

const toolScriptComment = "/** source: NexBox 用户授权的浏览器操作; summary: 执行浏览器工具的页面交互或检查 **/\n"

// Keep the tab's first Run on its long-lived context. Only subsequent actions
// use a cancellable context, so a tool timeout cannot destroy the tab's event loop.
func (s *Server) toolPageContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	pageCtx, err := s.getPageCtx(ctx)
	if err != nil {
		return nil, nil, err
	}
	merged, cancel := operationContext(pageCtx, ctx)
	return merged, cancel, nil
}

func (s *Server) handleTakeSnapshot(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	pageCtx, cancel, err := s.toolPageContext(ctx)
	if err != nil {
		return errResult(err.Error()), nil
	}
	defer cancel()
	text, err := s.snapshot.TakeSnapshot(pageCtx)
	if err != nil {
		return errResult(fmt.Sprintf("snapshot failed: %v", err)), nil
	}
	if fp, _ := req.GetArguments()["filePath"].(string); fp != "" {
		if err := os.WriteFile(fp, []byte(text), 0644); err != nil {
			return errResult(fmt.Sprintf("write snapshot file: %v", err)), nil
		}
		return txtResult(fmt.Sprintf("Snapshot saved to %s", fp)), nil
	}
	return txtResult(text), nil
}

func (s *Server) handleNavigatePage(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	navType, _ := args["type"].(string)
	if navType == "" {
		navType = "url"
	}
	var action chromedp.Action
	switch navType {
	case "url":
		url, _ := args["url"].(string)
		if url == "" {
			return errResult("url is required when type is 'url'"), nil
		}
		action = chromedp.Navigate(url)
	case "back":
		action = chromedp.ActionFunc(func(ctx context.Context) error { return navigateHistory(ctx, -1) })
	case "forward":
		action = chromedp.ActionFunc(func(ctx context.Context) error { return navigateHistory(ctx, 1) })
	case "reload":
		action = chromedp.Reload()
	default:
		return errResult(fmt.Sprintf("unknown navigation type: %s", navType)), nil
	}
	pageCtx, cancel, err := s.toolPageContext(ctx)
	if err != nil {
		return errResult(err.Error()), nil
	}
	defer cancel()
	s.snapshot.Reset()
	if err := chromedp.Run(pageCtx, action); err != nil {
		return errResult(fmt.Sprintf("navigate %s: %v", navType, err)), nil
	}
	return s.pageInfo(pageCtx)
}

// Chrome's back/forward cache restores a page without the usual load events.
// Wait for a frame navigation instead of relying solely on a load lifecycle event.
func navigateHistory(ctx context.Context, delta int64) error {
	current, entries, err := page.GetNavigationHistory().Do(ctx)
	if err != nil {
		return err
	}
	next := current + delta
	if next < 0 || next >= int64(len(entries)) {
		return fmt.Errorf("no history entry in that direction")
	}
	tree, err := page.GetFrameTree().Do(ctx)
	if err != nil {
		return err
	}
	eventCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	navigated := make(chan struct{}, 1)
	chromedp.ListenTarget(eventCtx, func(ev any) {
		match := false
		switch e := ev.(type) {
		case *page.EventFrameNavigated:
			match = e.Frame.ID == tree.Frame.ID
		case *page.EventNavigatedWithinDocument:
			match = e.FrameID == tree.Frame.ID
		}
		if match {
			select {
			case navigated <- struct{}{}:
			default:
			}
		}
	})
	if err := page.NavigateToHistoryEntry(entries[next].ID).Do(ctx); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-navigated:
	}
	for {
		var ready bool
		err := chromedp.Evaluate(toolScriptComment+`document.readyState !== 'loading'`, &ready).Do(ctx)
		if err == nil && ready {
			return nil
		}
		if err != nil && !strings.Contains(err.Error(), "Cannot find context") && !strings.Contains(err.Error(), "Execution context was destroyed") {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *Server) handleClick(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	uid, _ := args["uid"].(string)
	if uid == "" {
		return errResult("uid is required"), nil
	}
	dbl, _ := args["dblClick"].(bool)
	pageCtx, cancel, err := s.toolPageContext(ctx)
	if err != nil {
		return errResult(err.Error()), nil
	}
	defer cancel()
	x, y, err := s.snapshot.ElementCenter(pageCtx, uid)
	if err != nil {
		return errResult(fmt.Sprintf("resolve element %s: %v", uid, err)), nil
	}
	actions := []chromedp.Action{input.DispatchMouseEvent(input.MouseMoved, x, y), chromedp.MouseClickXY(x, y)}
	if dbl {
		actions = append(actions, chromedp.MouseClickXY(x, y, chromedp.ClickCount(2)))
	}
	if err := chromedp.Run(pageCtx, actions...); err != nil {
		return errResult(fmt.Sprintf("click %s: %v", uid, err)), nil
	}
	return txtResult(fmt.Sprintf("Clicked element uid=%s (double=%t). Take a fresh snapshot to inspect the result.", uid, dbl)), nil
}

func (s *Server) handleFill(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	uid, _ := args["uid"].(string)
	value, ok := args["value"].(string)
	if uid == "" || !ok {
		return errResult("uid and string value are required"), nil
	}
	pageCtx, cancel, err := s.toolPageContext(ctx)
	if err != nil {
		return errResult(err.Error()), nil
	}
	defer cancel()
	objID, err := s.snapshot.ResolveNodeObject(pageCtx, uid)
	if err != nil {
		return errResult(err.Error()), nil
	}
	// Use the native prototype setter so framework-controlled inputs receive
	// a real value change instead of updating only their internal value tracker.
	// Wrap the value: this cdproto version omits a primitive empty-string
	// CallArgument value, turning it into JavaScript undefined.
	fn := toolScriptComment + `function({value}) {
  const win = this.ownerDocument.defaultView;
  if (!this.isConnected || this.matches(':disabled') || this.readOnly) throw new Error('Element is detached, disabled or readonly');
  this.scrollIntoView({block:'center', inline:'center', behavior:'instant'});
  this.focus();
  if (this.tagName === 'SELECT') {
   if (![...this.options].some(o => o.value === value && !o.disabled && !o.parentElement.disabled)) throw new Error('No enabled option with value ' + value);
   Object.getOwnPropertyDescriptor(win.HTMLSelectElement.prototype,'value').set.call(this,value);
  } else if (this.tagName === 'INPUT' || this.tagName === 'TEXTAREA') {
   if (this.tagName === 'INPUT' && ['file','checkbox','radio','button','submit','reset','image','hidden'].includes(this.type)) throw new Error('Use click for toggles/buttons or upload_file for files');
   const proto = this.tagName === 'INPUT' ? win.HTMLInputElement.prototype : win.HTMLTextAreaElement.prototype;
   Object.getOwnPropertyDescriptor(proto,'value').set.call(this,value);
   if (this.value !== value) throw new Error('Input rejected or normalized the supplied value: ' + this.value);
  } else if (this.isContentEditable) {
   this.textContent = value;
  } else { throw new Error('Element is not editable'); }
  this.dispatchEvent(new win.Event('input',{bubbles:true,composed:true}));
  this.dispatchEvent(new win.Event('change',{bubbles:true}));
 }`
	if err := chromedp.Run(pageCtx, chromedp.CallFunctionOn(fn, nil, func(p *runtime.CallFunctionOnParams) *runtime.CallFunctionOnParams { return p.WithObjectID(objID) }, map[string]string{"value": value})); err != nil {
		return errResult(fmt.Sprintf("fill %s: %v", uid, err)), nil
	}
	return txtResult(fmt.Sprintf("Filled element uid=%s", uid)), nil
}

func (s *Server) handleTypeText(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	text, _ := req.GetArguments()["text"].(string)
	if text == "" {
		return errResult("text is required"), nil
	}
	pageCtx, cancel, err := s.toolPageContext(ctx)
	if err != nil {
		return errResult(err.Error()), nil
	}
	defer cancel()
	var editable bool
	if err := chromedp.Run(pageCtx, chromedp.Evaluate(toolScriptComment+`(() => {
      let el=document.activeElement;
      while(el && el.shadowRoot && el.shadowRoot.activeElement) el=el.shadowRoot.activeElement;
      return !!el && !el.disabled && !el.readOnly && (el.isContentEditable || el.tagName==='TEXTAREA' || (el.tagName==='INPUT' && !['file','checkbox','radio','button','submit','reset','image','hidden'].includes(el.type)));
    })()`, &editable)); err != nil {
		return errResult(err.Error()), nil
	}
	if !editable {
		return errResult("No editable element is focused; click or fill an editable element first"), nil
	}
	if err := chromedp.Run(pageCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		for _, ch := range text {
			// kb uses some Unicode code points as private representations of
			// special keys (e.g. U+0105 is Control). Literal non-ASCII text
			// must use native text insertion instead of that key encoder.
			var action chromedp.Action = input.InsertText(string(ch))
			if (ch >= ' ' && ch <= '~') || ch == '\n' || ch == '\r' {
				action = chromedp.KeyEvent(string(ch))
			}
			if err := action.Do(ctx); err != nil {
				return err
			}
		}
		return nil
	})); err != nil {
		return errResult(fmt.Sprintf("type_text: %v", err)), nil
	}
	if key, _ := req.GetArguments()["submitKey"].(string); key != "" {
		if err := dispatchKey(pageCtx, key); err != nil {
			return errResult(fmt.Sprintf("submit key: %v", err)), nil
		}
	}
	return txtResult(fmt.Sprintf("Typed %d characters", utf8.RuneCountInString(text))), nil
}

func (s *Server) handlePressKey(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	key, _ := req.GetArguments()["key"].(string)
	if key == "" {
		return errResult("key is required"), nil
	}
	pageCtx, cancel, err := s.toolPageContext(ctx)
	if err != nil {
		return errResult(err.Error()), nil
	}
	defer cancel()
	if err := dispatchKey(pageCtx, key); err != nil {
		return errResult(fmt.Sprintf("press_key: %v", err)), nil
	}
	return txtResult("Pressed key: " + key), nil
}

func (s *Server) handleHover(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	uid, _ := req.GetArguments()["uid"].(string)
	if uid == "" {
		return errResult("uid is required"), nil
	}
	pageCtx, cancel, err := s.toolPageContext(ctx)
	if err != nil {
		return errResult(err.Error()), nil
	}
	defer cancel()
	x, y, err := s.snapshot.ElementCenter(pageCtx, uid, true)
	if err != nil {
		return errResult(fmt.Sprintf("resolve element: %v", err)), nil
	}
	if err := chromedp.Run(pageCtx, input.DispatchMouseEvent(input.MouseMoved, x, y)); err != nil {
		return errResult(fmt.Sprintf("hover: %v", err)), nil
	}
	return txtResult("Hovered over element uid=" + uid), nil
}

func (s *Server) handleTakeScreenshot(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	full, _ := args["fullPage"].(bool)
	uid, _ := args["uid"].(string)
	fp, _ := args["filePath"].(string)
	if full && uid != "" {
		return errResult("uid and fullPage are mutually exclusive"), nil
	}
	pageCtx, cancel, err := s.toolPageContext(ctx)
	if err != nil {
		return errResult(err.Error()), nil
	}
	defer cancel()
	if fp == "" {
		if err := os.MkdirAll(s.screenshotDir, 0755); err != nil {
			return errResult(err.Error()), nil
		}
		// Unique names also avoid overwrites between concurrent sessions.
		f, err := os.CreateTemp(s.screenshotDir, "screenshot_*.png")
		if err != nil {
			return errResult(err.Error()), nil
		}
		fp = f.Name()
		f.Close()
	}
	var buf []byte
	if uid != "" {
		obj, err := s.snapshot.ResolveNodeObject(pageCtx, uid)
		if err != nil {
			return errResult(err.Error()), nil
		}
		var clip struct{ X, Y, Width, Height float64 }
		err = chromedp.Run(pageCtx, chromedp.CallFunctionOn(toolScriptComment+`function(){
   this.scrollIntoView({block:'center',inline:'center',behavior:'instant'});
   const r=this.getBoundingClientRect();
   if(r.width<=0 || r.height<=0) throw new Error('Element has no visible bounds');
   return {x:r.x+window.scrollX,y:r.y+window.scrollY,width:r.width,height:r.height};
  }`, &clip, func(p *runtime.CallFunctionOnParams) *runtime.CallFunctionOnParams { return p.WithObjectID(obj) }), chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			buf, err = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng).WithCaptureBeyondViewport(true).WithClip(&page.Viewport{X: clip.X, Y: clip.Y, Width: clip.Width, Height: clip.Height, Scale: 1}).Do(ctx)
			return err
		}))
		if err != nil {
			return errResult(fmt.Sprintf("screenshot element: %v", err)), nil
		}
	} else if full {
		err = chromedp.Run(pageCtx, chromedp.FullScreenshot(&buf, 100))
	} else {
		err = chromedp.Run(pageCtx, chromedp.CaptureScreenshot(&buf))
	}
	if err != nil {
		return errResult(fmt.Sprintf("screenshot: %v", err)), nil
	}
	if err := os.WriteFile(fp, buf, 0644); err != nil {
		return errResult(fmt.Sprintf("save screenshot: %v", err)), nil
	}
	return txtResult(fmt.Sprintf("Screenshot saved to %s (%d bytes)", fp, len(buf))), nil
}

func (s *Server) handleEvaluateScript(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	fn, _ := args["function"].(string)
	if strings.TrimSpace(fn) == "" {
		return errResult("function is required"), nil
	}
	var uidArgs []struct {
		UID string `json:"uid"`
	}
	if raw, ok := args["args"]; ok {
		b, err := json.Marshal(raw)
		if err != nil {
			return errResult("invalid args"), nil
		}
		if err := json.Unmarshal(b, &uidArgs); err != nil || string(b) == "null" {
			return errResult("args must be an array of {uid: string}"), nil
		}
		for _, a := range uidArgs {
			if a.UID == "" {
				return errResult("each arg requires a non-empty uid"), nil
			}
		}
	}
	pageCtx, cancel, err := s.toolPageContext(ctx)
	if err != nil {
		return errResult(err.Error()), nil
	}
	defer cancel()
	// CallFunctionOn has no CDP execution-timeout option. Interrupt evaluation
	// on cancellation, and finish that recovery before allowing the next tool
	// to execute, so a timed-out busy loop cannot wedge the renderer or kill a
	// subsequent script.
	recovered := make(chan struct{})
	stopRecovery := context.AfterFunc(ctx, func() {
		defer close(recovered)
		recoveryCtx, recoveryCancel := context.WithTimeout(context.WithoutCancel(pageCtx), time.Second)
		defer recoveryCancel()
		_ = chromedp.Run(recoveryCtx, runtime.TerminateExecution())
	})
	defer func() {
		if !stopRecovery() {
			<-recovered
		}
	}()
	var result *runtime.RemoteObject
	if len(uidArgs) == 0 {
		err = chromedp.Run(pageCtx, chromedp.Evaluate(toolScriptComment+"("+fn+"\n)()", &result, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			p = p.WithAwaitPromise(true).WithReturnByValue(true).WithUserGesture(true)
			if deadline, ok := ctx.Deadline(); ok {
				p = p.WithTimeout(runtime.TimeDelta(time.Until(deadline).Milliseconds()))
			}
			return p
		}))
	} else {
		callArgs := make([]*runtime.CallArgument, 0, len(uidArgs))
		for _, a := range uidArgs {
			obj, resolveErr := s.snapshot.ResolveNodeObject(pageCtx, a.UID)
			if resolveErr != nil {
				return errResult(resolveErr.Error()), nil
			}
			callArgs = append(callArgs, &runtime.CallArgument{ObjectID: obj})
		}
		// Execute in the argument element's realm (also supports same-origin frames).
		err = chromedp.Run(pageCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			var exp *runtime.ExceptionDetails
			var err error
			result, exp, err = runtime.CallFunctionOn(toolScriptComment + fn).WithObjectID(callArgs[0].ObjectID).WithArguments(callArgs).WithAwaitPromise(true).WithReturnByValue(true).WithUserGesture(true).Do(ctx)
			if err != nil {
				return err
			}
			if exp != nil {
				return exp
			}
			return nil
		}))
	}
	if err != nil {
		return errResult(fmt.Sprintf("evaluate: %v", err)), nil
	}
	if result == nil {
		return errResult("evaluate returned no result"), nil
	}
	if result.Type == runtime.TypeUndefined {
		return txtResult("undefined"), nil
	}
	if result.UnserializableValue != "" {
		return txtResult(fmt.Sprintf("JavaScript %s: %s", result.Type, result.UnserializableValue)), nil
	}
	if result.Type == runtime.TypeFunction || result.Type == runtime.TypeSymbol || result.Subtype == "node" || !json.Valid(result.Value) {
		return errResult(fmt.Sprintf("result of type %s is not JSON-serializable; return plain data", result.Type)), nil
	}
	return txtResult(fmt.Sprintf("```json\n%s\n```", result.Value)), nil
}

func (s *Server) handleWaitFor(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	var texts []string
	b, _ := json.Marshal(req.GetArguments()["text"])
	if err := json.Unmarshal(b, &texts); err != nil || len(texts) == 0 {
		return errResult("text must be a non-empty array of strings"), nil
	}
	for _, t := range texts {
		if t == "" {
			return errResult("text entries must not be empty"), nil
		}
	}
	pageCtx, cancel, err := s.toolPageContext(ctx)
	if err != nil {
		return errResult(err.Error()), nil
	}
	defer cancel()
	textJSON, _ := json.Marshal(texts)
	var found string
	expression := toolScriptComment + `(() => {
        const body=document.body;if(!body) return '';
        const texts=` + string(textJSON) + `;return texts.find(text=>body.innerText.includes(text)) || '';
    })()`
	for {
		if err := chromedp.Run(pageCtx, chromedp.Evaluate(expression, &found)); err != nil {
			return errResult(fmt.Sprintf("wait_for: %v", err)), nil
		}
		if found != "" {
			break
		}
		select {
		case <-pageCtx.Done():
			return errResult(fmt.Sprintf("wait_for: %v", pageCtx.Err())), nil
		case <-time.After(100 * time.Millisecond):
		}
	}
	snap, err := s.snapshot.TakeSnapshot(pageCtx)
	if err != nil {
		return errResult(fmt.Sprintf("text found but snapshot failed: %v", err)), nil
	}
	return txtResult(fmt.Sprintf("Found text %q on page.\n\nPage snapshot:\n%s", found, snap)), nil
}

func (s *Server) handleListPages(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	if err := ctx.Err(); err != nil {
		return errResult(err.Error()), nil
	}
	if err := s.ensureBrowser(); err != nil {
		return errResult(err.Error()), nil
	}
	pages, current, err := s.browser.ListPages(ctx)
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
		fmt.Fprintf(&sb, "%sid=%d  %s  %s\n", marker, p.ID, p.URL, p.Title)
	}
	return txtResult(sb.String()), nil
}

func pageIDArg(req gomcp.CallToolRequest) (int, error) {
	b, _ := json.Marshal(req.GetArguments()["pageId"])
	var id float64
	if err := json.Unmarshal(b, &id); err != nil || id < 1 || math.Trunc(id) != id || id > float64(1<<31-1) {
		return 0, fmt.Errorf("pageId must be a positive integer")
	}
	return int(id), nil
}

func (s *Server) handleSelectPage(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	id, err := pageIDArg(req)
	if err != nil {
		return errResult(err.Error()), nil
	}
	if err := s.ensureBrowser(); err != nil {
		return errResult(err.Error()), nil
	}
	if _, _, err := s.browser.ListPages(ctx); err != nil {
		return errResult(err.Error()), nil
	}
	if err := s.browser.SelectPage(id); err != nil {
		return errResult(err.Error()), nil
	}
	s.snapshot.Reset()
	return txtResult(fmt.Sprintf("Switched to page %d; take_snapshot to get fresh element UIDs", id)), nil
}

func (s *Server) handleNewPage(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	if err := s.ensureBrowser(); err != nil {
		return errResult(err.Error()), nil
	}
	url, _ := req.GetArguments()["url"].(string)
	pi, err := s.browser.NewPageContext(ctx, url)
	if err != nil {
		return errResult(err.Error()), nil
	}
	s.snapshot.Reset()
	return txtResult(fmt.Sprintf("Created and selected page id=%d, URL=%s", pi.ID, pi.URL)), nil
}

func (s *Server) handleClosePage(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	id, err := pageIDArg(req)
	if err != nil {
		return errResult(err.Error()), nil
	}
	if err := s.ensureBrowser(); err != nil {
		return errResult(err.Error()), nil
	}
	if err := s.browser.ClosePage(id, ctx); err != nil {
		return errResult(err.Error()), nil
	}
	s.snapshot.Reset()
	return txtResult(fmt.Sprintf("Closed page %d", id)), nil
}

func (s *Server) handleGetPageInfo(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	pageCtx, cancel, err := s.toolPageContext(ctx)
	if err != nil {
		return errResult(err.Error()), nil
	}
	defer cancel()
	return s.pageInfo(pageCtx)
}

func (s *Server) pageInfo(ctx context.Context) (*gomcp.CallToolResult, error) {
	if dialog := s.browser.CurrentDialog(); dialog != nil {
		return txtResult(fmt.Sprintf("Open %s dialog: %s\nUse handle_dialog to accept or dismiss it.", dialog.Type, dialog.Message)), nil
	}
	var url, title string
	if err := chromedp.Run(ctx, chromedp.Location(&url), chromedp.Title(&title)); err != nil {
		return errResult(fmt.Sprintf("get page info: %v", err)), nil
	}
	return txtResult(fmt.Sprintf("URL: %s\nTitle: %s", url, title)), nil
}

func (s *Server) handleUploadFile(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	uid, _ := req.GetArguments()["uid"].(string)
	var files []string
	b, _ := json.Marshal(req.GetArguments()["files"])
	if uid == "" || json.Unmarshal(b, &files) != nil || string(b) == "null" {
		return errResult("uid and files array are required"), nil
	}
	for _, f := range files {
		if !filepath.IsAbs(f) {
			return errResult("file paths must be absolute on the Chrome host"), nil
		}
	}
	pageCtx, cancel, err := s.toolPageContext(ctx)
	if err != nil {
		return errResult(err.Error()), nil
	}
	defer cancel()
	obj, err := s.snapshot.ResolveNodeObject(pageCtx, uid)
	if err != nil {
		return errResult(err.Error()), nil
	}
	err = chromedp.Run(pageCtx, chromedp.CallFunctionOn(toolScriptComment+`function(count){
  if(this.tagName!=='INPUT'||this.type!=='file'||this.matches(':disabled')) throw new Error('Element is not an enabled file input');
  if(count>1&&!this.multiple) throw new Error('File input does not allow multiple files');
        if(count===0){
            this.value='';
            const win=this.ownerDocument.defaultView;
            this.dispatchEvent(new win.Event('input',{bubbles:true}));
            this.dispatchEvent(new win.Event('change',{bubbles:true}));
        }
 }`, nil, func(p *runtime.CallFunctionOnParams) *runtime.CallFunctionOnParams { return p.WithObjectID(obj) }, len(files)), chromedp.ActionFunc(func(ctx context.Context) error {
		if len(files) == 0 {
			return nil
		}
		return dom.SetFileInputFiles(files).WithObjectID(obj).Do(ctx)
	}))
	if err != nil {
		return errResult(fmt.Sprintf("upload_file: %v", err)), nil
	}
	return txtResult(fmt.Sprintf("Selected %d file(s) for uid=%s", len(files), uid)), nil
}

func (s *Server) handleDialog(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	action, _ := req.GetArguments()["action"].(string)
	if action != "accept" && action != "dismiss" {
		return errResult("action must be accept or dismiss"), nil
	}
	text, _ := req.GetArguments()["promptText"].(string)
	if err := s.browser.HandleDialog(ctx, action == "accept", text); err != nil {
		return errResult(fmt.Sprintf("handle_dialog: %v", err)), nil
	}
	return txtResult("Dialog " + action + "ed"), nil
}

func dispatchKey(ctx context.Context, combo string) error {
	parts := strings.Split(combo, "+")
	key := normalizeKeyName(parts[len(parts)-1])
	modifiers := parts[:len(parts)-1]
	if strings.HasSuffix(combo, "+") {
		key = "+"
		prefix := strings.TrimSuffix(strings.TrimSuffix(combo, "+"), "+")
		modifiers = nil
		if prefix != "" {
			modifiers = strings.Split(prefix, "+")
		}
	}
	var mods []input.Modifier
	for _, m := range modifiers {
		switch strings.ToLower(m) {
		case "ctrl", "control":
			mods = append(mods, input.ModifierCtrl)
		case "shift":
			mods = append(mods, input.ModifierShift)
		case "alt":
			mods = append(mods, input.ModifierAlt)
		case "meta", "cmd", "command":
			mods = append(mods, input.ModifierMeta)
		default:
			return fmt.Errorf("unsupported modifier %q", m)
		}
	}
	named := map[string]string{"Enter": kb.Enter, "Tab": kb.Tab, "Escape": kb.Escape, "Backspace": kb.Backspace, "Delete": kb.Delete, "ArrowUp": kb.ArrowUp, "ArrowDown": kb.ArrowDown, "ArrowLeft": kb.ArrowLeft, "ArrowRight": kb.ArrowRight, "Home": kb.Home, "End": kb.End, "PageUp": kb.PageUp, "PageDown": kb.PageDown}
	for i, r := range []string{kb.F1, kb.F2, kb.F3, kb.F4, kb.F5, kb.F6, kb.F7, kb.F8, kb.F9, kb.F10, kb.F11, kb.F12} {
		named[fmt.Sprintf("F%d", i+1)] = r
	}
	if r, ok := named[key]; ok {
		key = r
	} else if utf8.RuneCountInString(key) != 1 {
		return fmt.Errorf("unsupported key %q", key)
	}
	if len(mods) > 0 {
		shift := false
		for _, m := range mods {
			if m == input.ModifierShift {
				shift = true
			}
		}
		if shift {
			shifted := map[string]string{"1": "!", "2": "@", "3": "#", "4": "$", "5": "%", "6": "^", "7": "&", "8": "*", "9": "(", "0": ")", "-": "_", "=": "+", "[": "{", "]": "}", ";": ":", "'": "\"", ",": "<", ".": ">", "/": "?", "`": "~", "\\": "|"}
			if v, ok := shifted[key]; ok {
				key = v
			} else {
				key = strings.ToUpper(key)
			}
		} else {
			key = strings.ToLower(key)
		}
	}
	return chromedp.Run(ctx, chromedp.KeyEvent(key, chromedp.KeyModifiers(mods...)))
}

func normalizeKeyName(name string) string {
	for _, key := range []string{"Enter", "Tab", "Escape", "Backspace", "Delete", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End", "PageUp", "PageDown"} {
		if strings.EqualFold(name, key) {
			return key
		}
	}
	switch strings.ToLower(name) {
	case "return":
		return "Enter"
	case "esc":
		return "Escape"
	case "space":
		return " "
	case "up":
		return "ArrowUp"
	case "down":
		return "ArrowDown"
	case "left":
		return "ArrowLeft"
	case "right":
		return "ArrowRight"
	}
	return name
}

func txtResult(text string) *gomcp.CallToolResult { return gomcp.NewToolResultText(text) }
func errResult(msg string) *gomcp.CallToolResult  { return gomcp.NewToolResultError(msg) }
