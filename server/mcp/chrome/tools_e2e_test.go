package chrome

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/mark3labs/mcp-go/client"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

const toolsHTML = `<!doctype html><html><head><title>Tools fixture</title>
<style>body {margin:20px} input,textarea,select,button,[contenteditable]{display:block;margin:8px} #bottom{margin-top:1400px;background:rgb(240,20,180);width:120px;height:80px}</style></head><body>
<label>Name<input id="name" aria-label="Name"></label>
<textarea id="notes" aria-label="Notes"></textarea>
<select id="choice" aria-label="Choice"><option value="">Empty</option><option value="a">Alpha</option><option value="b">Beta</option></select>
<div contenteditable="true" role="textbox" aria-label="Editor" id="editor">initial</div>
<input type="file" aria-label="Upload" id="upload" multiple>
<button id="button" onclick="window.clicks++" ondblclick="window.doubles++" onmouseover="window.hovered=true">Action</button>
<button disabled onmouseover="window.disabledHovered=true">Disabled</button>
<input readonly aria-label="Readonly" value="locked">
<p id="status">Ready</p><p hidden>Hidden only text</p>
<div id="bottom" role="button" aria-label="Bottom">Bottom</div>
<script>/** source: 用户要求的浏览器工具回归测试; summary: 创建交互与框架受控输入测试页面 **/
window.clicks=0;window.doubles=0;window.hovered=false;window.model='';window.keyEvents=[];
const field=document.getElementById('name');
const native=Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value');let tracked='';
Object.defineProperty(field,'value',{get(){return native.get.call(this)},set(v){tracked=v;native.set.call(this,v)}});
field.addEventListener('input',()=>{if(field.value!==tracked){window.model=field.value;tracked=field.value}});
field.addEventListener('keydown',e=>{window.keyEvents.push(e.key);if(e.key==='Enter') document.getElementById('status').textContent='Submitted'});
</script></body></html>`

// These tests intentionally cross Streamable HTTP, tools/list and tools/call,
// rather than reimplementing tool behaviour with direct CDP calls.
func TestMCPToolsEndToEnd(t *testing.T) {
	requireChromeE2E(t)
	for _, external := range []bool{false, true} {
		name := "managed"
		if external {
			name = "external"
		}
		t.Run(name, func(t *testing.T) { testMCPToolsEndToEnd(t, external) })
	}
}

func testMCPToolsEndToEnd(t *testing.T, external bool) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/slow" {
			select {
			case <-time.After(31 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
		if r.URL.Path == "/load-dialog" {
			fmt.Fprint(w, `<!doctype html><script>/** source: 浏览器工具回归测试; summary: 验证导航期间的原生弹窗可处理 **/alert('Load dialog');document.title='Dialog loaded';</script>`)
			return
		}
		fmt.Fprint(w, toolsHTML)
	}))
	defer site.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	opts := BrowserOptions{Headless: true}
	if external {
		profile, err := os.MkdirTemp(".", "tools-chrome-profile-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(profile)
		profile, err = filepath.Abs(profile)
		if err != nil {
			t.Fatal(err)
		}
		owner := NewBrowserManager(BrowserOptions{Headless: true, UserDataDir: profile})
		if err := owner.Launch(ctx); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = chromedp.Cancel(owner.browserCtx); owner.Close() }()
		port, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		if err != nil {
			t.Fatal(err)
		}
		opts = BrowserOptions{BrowserURL: "http://127.0.0.1:" + strings.Split(string(port), "\n")[0]}
	}
	srv := NewServer("127.0.0.1:0", t.TempDir(), opts)
	if err := srv.EnsureStarted(ctx); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	c, err := client.NewStreamableHttpClient(srv.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Initialize(ctx, gomcp.InitializeRequest{Params: gomcp.InitializeParams{ProtocolVersion: gomcp.LATEST_PROTOCOL_VERSION, ClientInfo: gomcp.Implementation{Name: "chrome-tools-test", Version: "1"}}}); err != nil {
		t.Fatal(err)
	}
	listed, err := c.ListTools(ctx, gomcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	exercised := map[string]bool{}
	call := func(t *testing.T, name string, args map[string]any, wantError bool) string {
		t.Helper()
		exercised[name] = true
		result, err := c.CallTool(ctx, gomcp.CallToolRequest{Params: gomcp.CallToolParams{Name: name, Arguments: args}})
		if err != nil {
			t.Fatalf("%s transport: %v", name, err)
		}
		var text strings.Builder
		for _, item := range result.Content {
			if v, ok := item.(gomcp.TextContent); ok {
				text.WriteString(v.Text)
			}
		}
		if result.IsError != wantError {
			t.Fatalf("%s error=%t want=%t: %s", name, result.IsError, wantError, text.String())
		}
		return text.String()
	}
	eval := func(t *testing.T, fn string) string {
		t.Helper()
		return call(t, "evaluate_script", map[string]any{"function": toolScriptComment + fn}, false)
	}
	wantEval := func(t *testing.T, fn, expected string) {
		t.Helper()
		got := eval(t, fn)
		raw := strings.TrimSuffix(strings.TrimPrefix(got, "```json\n"), "\n```")
		var gotValue, wantValue any
		if json.Unmarshal([]byte(raw), &gotValue) != nil || json.Unmarshal([]byte(expected), &wantValue) != nil {
			t.Fatalf("invalid JSON result %s expected %s", got, expected)
		}
		gotJSON, _ := json.Marshal(gotValue)
		wantJSON, _ := json.Marshal(wantValue)
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("evaluate %s = %s want %s", fn, got, expected)
		}
	}
	snapshot := func(t *testing.T) string { t.Helper(); return call(t, "take_snapshot", nil, false) }
	uid := func(t *testing.T, snap, role, name string) string {
		t.Helper()
		id := findUID(snap, role, name)
		if id == "" {
			t.Fatalf("no UID for %s %s:\n%s", role, name, snap)
		}
		return id
	}
	navigate := func(t *testing.T) { t.Helper(); call(t, "navigate_page", map[string]any{"url": site.URL}, false) }

	t.Run("schema", func(t *testing.T) {
		if len(listed.Tools) != 17 {
			t.Fatalf("tools=%d want 17", len(listed.Tools))
		}
		for _, tool := range listed.Tools {
			if _, ok := tool.InputSchema.Properties["timeout"]; !ok {
				t.Errorf("%s missing timeout", tool.Name)
			}
			if tool.Name == "evaluate_script" {
				if _, ok := tool.InputSchema.Properties["args"]; !ok {
					t.Error("missing script args")
				}
			}
			if tool.Name == "wait_for" {
				if _, ok := tool.InputSchema.Properties["text"]; !ok {
					t.Error("missing wait text")
				}
			}
		}
	})
	navigate(t)
	t.Run("scripts", func(t *testing.T) {
		for _, fn := range []string{"() => document.title", "() => { return document.title; }", "function(){return document.title}", "async () => { await Promise.resolve(); return document.title; }"} {
			wantEval(t, fn, `"Tools fixture"`)
		}
		wantEval(t, "()=>null", "null")
		wantEval(t, "()=>({nested:[1,true,'中文']})", `{"nested":[1,true,"中文"]}`)
		if got := eval(t, "()=>{}"); got != "undefined" {
			t.Fatalf("undefined=%q", got)
		}
		for _, fn := range []string{"()=>{throw new Error('sentinel failure')}", "async()=>{throw new Error('async failure')}", "()=>{", "()=>{const x={};x.self=x;return x}", "()=>function(){}"} {
			call(t, "evaluate_script", map[string]any{"function": toolScriptComment + fn}, true)
		}
		call(t, "evaluate_script", map[string]any{"function": "(el)=>el.tagName", "args": "bad"}, true)
		snap := snapshot(t)
		name := uid(t, snap, "[textbox]", "Name")
		got := call(t, "evaluate_script", map[string]any{"function": toolScriptComment + "(el) => el.tagName", "args": []any{map[string]any{"uid": name}}}, false)
		if !strings.Contains(got, `"INPUT"`) {
			t.Fatal(got)
		}
		call(t, "evaluate_script", map[string]any{"function": toolScriptComment + "(el)=>{throw new Error('argument error')}", "args": []any{map[string]any{"uid": name}}}, true)
		old := name
		snapshot(t)
		call(t, "fill", map[string]any{"uid": old, "value": "stale"}, true)
	})
	t.Run("clear_and_unicode", func(t *testing.T) {
		navigate(t)
		snap := snapshot(t)
		name := uid(t, snap, "[textbox]", "Name")
		for _, field := range [][3]string{{"[textbox]", "Name", "initial"}, {"[textbox]", "Notes", "initial"}, {"[textbox]", "Editor", "initial"}, {"[combobox]", "Choice", "b"}} {
			id := uid(t, snap, field[0], field[1])
			call(t, "fill", map[string]any{"uid": id, "value": field[2]}, false)
			call(t, "fill", map[string]any{"uid": id, "value": ""}, false)
		}
		wantEval(t, "()=>[document.getElementById('name').value,document.getElementById('notes').value,document.getElementById('editor').textContent,document.getElementById('choice').value,window.model]", `["","","","",""]`)
		for _, text := range []string{"ąe\u0301ĂĄ", "中文🙂", "AĄB"} {
			call(t, "fill", map[string]any{"uid": name, "value": ""}, false)
			eval(t, "()=>{window.keyEvents=[]}")
			call(t, "type_text", map[string]any{"text": text}, false)
			expected, _ := json.Marshal(text)
			wantEval(t, "()=>document.getElementById('name').value", string(expected))
			wantEval(t, "()=>window.keyEvents.some(k=>k==='Control'||k==='ArrowDown')", "false")
		}
		notes := uid(t, snap, "[textbox]", "Notes")
		call(t, "fill", map[string]any{"uid": notes, "value": ""}, false)
		call(t, "type_text", map[string]any{"text": "line 1\nąe\u0301"}, false)
		wantEval(t, "()=>document.getElementById('notes').value", `"line 1\nąé"`)
	})

	t.Run("fill_and_keyboard", func(t *testing.T) {
		navigate(t)
		snap := snapshot(t)
		name := uid(t, snap, "[textbox]", "Name")
		call(t, "fill", map[string]any{"uid": name, "value": "中文🙂"}, false)
		wantEval(t, "()=>window.model", `"中文🙂"`)
		call(t, "press_key", map[string]any{"key": "Control+A"}, false)
		call(t, "press_key", map[string]any{"key": "Shift+A"}, false)
		wantEval(t, "()=>document.getElementById('name').value", `"A"`)
		call(t, "press_key", map[string]any{"key": "+"}, false)
		wantEval(t, "()=>document.getElementById('name').value", `"A+"`)
		call(t, "press_key", map[string]any{"key": "Control+A"}, false)
		call(t, "type_text", map[string]any{"text": "hello中文🙂", "submitKey": "Enter"}, false)
		wantEval(t, "()=>document.getElementById('name').value", `"hello中文🙂"`)
		wantEval(t, "()=>document.getElementById('status').innerText", `"Submitted"`)
		call(t, "press_key", map[string]any{"key": "Backspace"}, false)
		wantEval(t, "()=>document.getElementById('name').value", `"hello中文"`)
		call(t, "press_key", map[string]any{"key": "Home"}, false)
		call(t, "press_key", map[string]any{"key": "Delete"}, false)
		wantEval(t, "()=>document.getElementById('name').value", `"ello中文"`)
		call(t, "press_key", map[string]any{"key": "Tab"}, false)
		wantEval(t, "()=>document.activeElement.id", `"notes"`)
		call(t, "press_key", map[string]any{"key": "Shift+Tab"}, false)
		wantEval(t, "()=>document.activeElement.id", `"name"`)
		call(t, "press_key", map[string]any{"key": "Bogus+A"}, true)
		call(t, "press_key", map[string]any{"key": "NotAKey"}, true)
		for _, pair := range [][3]string{{"[textbox]", "Notes", "line 1\n中文"}, {"[combobox]", "Choice", "b"}, {"[textbox]", "Editor", "edited"}} {
			call(t, "fill", map[string]any{"uid": uid(t, snap, pair[0], pair[1]), "value": pair[2]}, false)
		}
		wantEval(t, "()=>[document.getElementById('notes').value,document.getElementById('choice').value,document.getElementById('editor').textContent]", `["line 1\n中文","b","edited"]`)
		call(t, "fill", map[string]any{"uid": uid(t, snap, "[combobox]", "Choice"), "value": "missing"}, true)
		call(t, "fill", map[string]any{"uid": uid(t, snap, "[textbox]", "Readonly"), "value": "change"}, true)
		call(t, "fill", map[string]any{"uid": name}, true)
		eval(t, "()=>document.activeElement.blur()")
		call(t, "type_text", map[string]any{"text": "lost"}, true)
	})
	t.Run("pointer", func(t *testing.T) {
		navigate(t)
		snap := snapshot(t)
		button := uid(t, snap, "[button]", "Action")
		call(t, "hover", map[string]any{"uid": button}, false)
		wantEval(t, "()=>window.hovered", "true")
		call(t, "click", map[string]any{"uid": button}, false)
		wantEval(t, "()=>window.clicks", "1")
		call(t, "click", map[string]any{"uid": button, "dblClick": true}, false)
		wantEval(t, "()=>window.doubles", "1")
		wantEval(t, "()=>window.clicks", "3")
		call(t, "click", map[string]any{"uid": uid(t, snap, "[button]", "Disabled")}, true)
		call(t, "hover", map[string]any{"uid": uid(t, snap, "[button]", "Disabled")}, false)
		wantEval(t, "()=>window.disabledHovered", "true")
		eval(t, "()=>{const overlay=document.createElement('div');overlay.style='position:fixed;inset:0;z-index:999;background:white';document.body.append(overlay)}")
		call(t, "click", map[string]any{"uid": button}, true)
	})
	t.Run("wait_and_timeouts", func(t *testing.T) {
		navigate(t)
		call(t, "wait_for", map[string]any{"text": []string{"Ready"}}, false)
		eval(t, "()=>{setTimeout(()=>document.getElementById('status').innerText='Delayed text',150)}")
		call(t, "wait_for", map[string]any{"text": []string{"absent", "Delayed text"}, "timeout": 2000}, false)
		for _, args := range []map[string]any{{"text": []string{}}, {"text": []string{""}}, {"text": []string{"Hidden only text"}, "timeout": 200}, {"text": []string{"never"}, "timeout": 200}} {
			start := time.Now()
			call(t, "wait_for", args, true)
			if time.Since(start) > 2*time.Second {
				t.Fatal("wait exceeded timeout")
			}
		}
		call(t, "evaluate_script", map[string]any{"function": toolScriptComment + "()=>new Promise(()=>{})", "timeout": 200}, true)
		wantEval(t, "()=>42", "42")
		call(t, "evaluate_script", map[string]any{"function": toolScriptComment + "()=>{while(true){}}", "timeout": 200}, true)
		wantEval(t, "()=>43", "43")
		snap := snapshot(t)
		id := uid(t, snap, "[textbox]", "Name")
		call(t, "evaluate_script", map[string]any{"function": toolScriptComment + "(el)=>{while(true){}}", "args": []any{map[string]any{"uid": id}}, "timeout": 200}, true)
		wantEval(t, "()=>45", "45")
		for _, timeout := range []any{0, -1, "bad", 120001, 1.5} {
			call(t, "get_page_info", map[string]any{"timeout": timeout}, true)
		}
	})
	t.Run("request_cancellation", func(t *testing.T) {
		requestCtx, cancelRequest := context.WithCancel(ctx)
		done := make(chan *gomcp.CallToolResult, 1)
		handler := srv.mcpServer.ListTools()["evaluate_script"].Handler
		go func() {
			result, _ := handler(requestCtx, gomcp.CallToolRequest{Params: gomcp.CallToolParams{Arguments: map[string]any{"function": toolScriptComment + "()=>new Promise(()=>{})"}}})
			done <- result
		}()
		deadline := time.Now().Add(time.Second)
		for srv.inFlight.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
		// A queued operation has its own timeout and cannot mutate shared state.
		call(t, "take_snapshot", map[string]any{"timeout": 50}, true)
		cancelRequest()
		select {
		case result := <-done:
			if result == nil || !result.IsError {
				t.Fatalf("cancel result: %+v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("cancellation did not interrupt script")
		}
		wantEval(t, "()=>44", "44")
	})

	t.Run("screenshots_and_snapshot_file", func(t *testing.T) {
		navigate(t)
		snap := snapshot(t)
		bottom := uid(t, snap, "[button]", "Bottom")
		for _, mode := range []string{"viewport", "full", "element"} {
			fp := filepath.Join(t.TempDir(), mode+".png")
			args := map[string]any{"filePath": fp}
			if mode == "full" {
				args["fullPage"] = true
			}
			if mode == "element" {
				args["uid"] = bottom
			}
			call(t, "take_screenshot", args, false)
			f, err := os.Open(fp)
			if err != nil {
				t.Fatal(err)
			}
			img, err := png.Decode(f)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "full" && img.Bounds().Dy() < 1400 {
				t.Fatalf("full screenshot too short: %v", img.Bounds())
			}
			if mode == "element" {
				r, g, b, _ := img.At(100, 60).RGBA()
				if r>>8 != 240 || g>>8 != 20 || b>>8 != 180 {
					t.Fatalf("wrong scrolled element crop: %v %d %d %d", img.Bounds(), r>>8, g>>8, b>>8)
				}
			}
		}
		call(t, "take_screenshot", map[string]any{"uid": bottom, "fullPage": true}, true)
		fp := filepath.Join(t.TempDir(), "snapshot.txt")
		call(t, "take_snapshot", map[string]any{"filePath": fp}, false)
		if b, err := os.ReadFile(fp); err != nil || !strings.Contains(string(b), "Name") {
			t.Fatalf("snapshot file: %s %v", b, err)
		}
	})
	t.Run("upload", func(t *testing.T) {
		navigate(t)
		snap := snapshot(t)
		id := uid(t, snap, "", "Upload")
		file, err := os.CreateTemp(".", "upload-fixture-*.txt")
		if err != nil {
			t.Fatal(err)
		}
		fp, err := filepath.Abs(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(fp)
		if _, err := file.WriteString("fixture content"); err != nil {
			t.Fatal(err)
		}
		file.Close()
		call(t, "upload_file", map[string]any{"uid": id, "files": []string{fp}}, false)
		wantEval(t, "async()=>document.getElementById('upload').files[0].text()", `"fixture content"`)
		call(t, "upload_file", map[string]any{"uid": id, "files": []string{}}, false)
		wantEval(t, "()=>document.getElementById('upload').files.length", "0")
		call(t, "upload_file", map[string]any{"uid": id, "files": []string{"relative.txt"}}, true)
		call(t, "upload_file", map[string]any{"uid": uid(t, snap, "[textbox]", "Name"), "files": []string{fp}}, true)
	})
	t.Run("dialogs", func(t *testing.T) {
		navigate(t)
		for _, action := range []string{"accept", "dismiss"} {
			done := make(chan *gomcp.CallToolResult, 1)
			go func() {
				result, err := c.CallTool(ctx, gomcp.CallToolRequest{Params: gomcp.CallToolParams{Name: "evaluate_script", Arguments: map[string]any{"function": toolScriptComment + "()=>prompt('Test prompt')", "timeout": 5000}}})
				if err != nil {
					done <- nil
				} else {
					done <- result
				}
			}()
			deadline := time.Now().Add(2 * time.Second)
			for srv.browser.CurrentDialog() == nil && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if srv.browser.CurrentDialog() == nil {
				t.Fatal("dialog did not open")
			}
			if text := call(t, "get_page_info", nil, false); !strings.Contains(text, "Test prompt") {
				t.Fatal(text)
			}
			call(t, "handle_dialog", map[string]any{"action": action, "promptText": "answer"}, false)
			select {
			case result := <-done:
				if result == nil || result.IsError {
					t.Fatalf("script after dialog: %+v", result)
				}
				b, _ := json.Marshal(result)
				if action == "accept" && !strings.Contains(string(b), "answer") {
					t.Fatal(string(b))
				}
			case <-time.After(2 * time.Second):
				t.Fatal("dialog script stayed blocked")
			}
		}
		call(t, "handle_dialog", map[string]any{"action": "accept"}, true)
		done := make(chan *gomcp.CallToolResult, 1)
		go func() {
			result, err := c.CallTool(ctx, gomcp.CallToolRequest{Params: gomcp.CallToolParams{Name: "new_page", Arguments: map[string]any{"url": site.URL + "/load-dialog", "timeout": 5000}}})
			if err != nil {
				done <- nil
			} else {
				done <- result
			}
		}()
		deadline := time.Now().Add(2 * time.Second)
		for srv.browser.CurrentDialog() == nil && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if srv.browser.CurrentDialog() == nil {
			t.Fatal("navigation dialog did not open")
		}
		call(t, "handle_dialog", map[string]any{"action": "accept"}, false)
		select {
		case result := <-done:
			if result == nil || result.IsError {
				t.Fatalf("new_page after dialog: %+v", result)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("new_page stayed blocked after dialog")
		}
		_, current, err := srv.browser.ListPages()
		if err != nil {
			t.Fatal(err)
		}
		call(t, "close_page", map[string]any{"pageId": current}, false)
	})
	t.Run("dialog_on_lazy_tab", func(t *testing.T) {
		navigate(t)
		_, original, err := srv.browser.ListPages()
		if err != nil {
			t.Fatal(err)
		}
		popup := func() PageInfo {
			eval(t, "()=>{window.open('about:blank','_blank')}")
			pages, _, err := srv.browser.ListPages()
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range pages {
				if p.ID != original {
					return p
				}
			}
			t.Fatal("popup not discovered")
			return PageInfo{}
		}
		// Even an erroneous dialog call must leave a lazily attached tab usable.
		first := popup()
		call(t, "select_page", map[string]any{"pageId": first.ID}, false)
		call(t, "handle_dialog", map[string]any{"action": "accept"}, true)
		call(t, "get_page_info", map[string]any{"timeout": 1000}, false)
		wantEval(t, "()=>41", "41")
		call(t, "close_page", map[string]any{"pageId": first.ID}, false)

		// A separate owner opens a prompt before this MCP ever attaches to it.
		second := popup()
		ownerCtx, ownerCancel := chromedp.NewContext(srv.browser.browserCtx, chromedp.WithTargetID(second.TargetID))
		defer ownerCancel()
		if err := chromedp.Run(ownerCtx); err != nil {
			t.Fatal(err)
		}
		opened := make(chan struct{}, 1)
		chromedp.ListenTarget(ownerCtx, func(event any) {
			if _, ok := event.(*page.EventJavascriptDialogOpening); ok {
				select {
				case opened <- struct{}{}:
				default:
				}
			}
		})
		if err := chromedp.Run(ownerCtx, chromedp.Evaluate(toolScriptComment+`setTimeout(()=>{window.promptReply=prompt('Existing prompt')},0)`, nil)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-opened:
		case <-time.After(2 * time.Second):
			t.Fatal("owner prompt did not open")
		}
		call(t, "select_page", map[string]any{"pageId": second.ID}, false)
		message := call(t, "handle_dialog", map[string]any{"action": "accept", "promptText": "answer", "timeout": 2000}, true)
		if !strings.Contains(message, "tab is not attached") {
			t.Fatal(message)
		}
		// Chromium keeps pending dialogs on their original Page agent. Reject
		// this unsupported attachment case without poisoning our target.
		if err := chromedp.Run(ownerCtx, page.HandleJavaScriptDialog(true).WithPromptText("answer")); err != nil {
			t.Fatal(err)
		}
		call(t, "get_page_info", map[string]any{"timeout": 1000}, false)
		wantEval(t, "()=>window.promptReply", `"answer"`)
		call(t, "close_page", map[string]any{"pageId": second.ID}, false)
	})
	if !external {
		t.Run("new_page_long_timeout", func(t *testing.T) {
			// Regression: the manager used to impose 30s even when the MCP
			// caller explicitly requested a longer deadline.
			call(t, "new_page", map[string]any{"url": site.URL + "/slow", "timeout": 40000}, false)
			_, current, err := srv.browser.ListPages()
			if err != nil {
				t.Fatal(err)
			}
			call(t, "close_page", map[string]any{"pageId": current}, false)
		})
	}

	t.Run("pages_and_history", func(t *testing.T) {
		navigate(t)
		snap := snapshot(t)
		old := uid(t, snap, "[textbox]", "Name")
		call(t, "get_page_info", nil, false)
		call(t, "navigate_page", map[string]any{"url": site.URL + "/second"}, false)
		call(t, "fill", map[string]any{"uid": old, "value": "stale after navigation"}, true)
		call(t, "navigate_page", map[string]any{"type": "back"}, false)
		wantEval(t, "()=>location.pathname", `"/"`)
		call(t, "navigate_page", map[string]any{"type": "forward"}, false)
		wantEval(t, "()=>location.pathname", `"/second"`)
		call(t, "navigate_page", map[string]any{"type": "reload"}, false)
		call(t, "new_page", map[string]any{"url": site.URL + "/new"}, false)
		call(t, "list_pages", nil, false)
		call(t, "select_page", map[string]any{"pageId": 1}, false)
		wantEval(t, "()=>location.pathname", `"/second"`)
		eval(t, "()=>{window.open('/popup','_blank')}")
		text := call(t, "list_pages", nil, false)
		deadline := time.Now().Add(2 * time.Second)
		for !strings.Contains(text, "/popup") && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
			text = call(t, "list_pages", nil, false)
		}
		if !strings.Contains(text, "/popup") {
			t.Fatal("popup not discovered: " + text)
		}
		call(t, "close_page", map[string]any{"pageId": 1}, false)
		call(t, "list_pages", nil, false)
		// Close every remaining tab and verify managed-browser recovery.
		pages, _, err := srv.browser.ListPages()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range pages {
			call(t, "close_page", map[string]any{"pageId": p.ID}, false)
		}
		if external {
			call(t, "get_page_info", nil, true)
			call(t, "new_page", nil, false)
		}
		call(t, "get_page_info", nil, false)
		wantEval(t, "()=>location.href", `"about:blank"`)
		call(t, "select_page", map[string]any{"pageId": 9999}, true)
		call(t, "close_page", map[string]any{"pageId": 9999}, true)
		call(t, "select_page", map[string]any{"pageId": 1.5}, true)
	})
	t.Run("coverage", func(t *testing.T) {
		for _, tool := range listed.Tools {
			if !exercised[tool.Name] {
				t.Errorf("tool %s was not exercised", tool.Name)
			}
		}
	})
}

// A close response can arrive before Target.getTargets stops reporting the tab.
// It must not be re-added and selected as a fresh, unusable page.
func TestClosedPageIsNotRediscoveredFromStaleTargets(t *testing.T) {
	endpoint := mockCDP(t, "", false, nil)
	bm := NewBrowserManager(BrowserOptions{BrowserURL: endpoint})
	if err := bm.Launch(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer bm.Close()
	if err := bm.ClosePage(1); err != nil {
		t.Fatal(err)
	}
	pages, current, err := bm.ListPages()
	if err != nil || len(pages) != 0 || current != 0 {
		t.Fatalf("closed tab rediscovered: pages=%v current=%d err=%v", pages, current, err)
	}
}
