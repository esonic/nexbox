package agent

// End-to-end KimiAgent test against a real `kimi acp`.
//
// With the user's own Kimi login:
//
//	KIMI_INTEGRATION=1 go test -v -run TestKimiAgentIntegration -timeout 600s ./agent/
//
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/draw"
	pngenc "image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// kimiTestMCPServer serves a minimal streamable-HTTP MCP server with one
// tool, and reports the X-Test header seen on tools/call.
func kimiTestMCPServer(t *testing.T) (string, func() string) {
	var mu sync.Mutex
	header := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if len(request.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result interface{}
		switch request.Method {
		case "initialize":
			result = map[string]interface{}{"protocolVersion": "2025-03-26", "capabilities": map[string]interface{}{"tools": map[string]interface{}{}}, "serverInfo": map[string]interface{}{"name": "nexbox_test", "version": "1"}}
		case "tools/list":
			result = map[string]interface{}{"tools": []interface{}{map[string]interface{}{"name": "magic_number", "description": "Returns the magic number.", "inputSchema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}}}}
		case "tools/call":
			mu.Lock()
			header = r.Header.Get("X-Test")
			mu.Unlock()
			result = map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "text", "text": "4817"}}}
		default:
			result = map[string]interface{}{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	return server.URL + "/mcp", func() string {
		mu.Lock()
		defer mu.Unlock()
		return header
	}
}

type kimiRecorder struct {
	mu     sync.Mutex
	events []StreamEvent
}

func (r *kimiRecorder) callback(event StreamEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *kimiRecorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var sb strings.Builder
	for _, event := range r.events {
		if event.Type == EventTextDelta {
			sb.WriteString(event.Content)
		}
	}
	return sb.String()
}

func (r *kimiRecorder) find(kind string, match func(StreamEvent) bool) *StreamEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.events {
		if r.events[i].Type == kind && (match == nil || match(r.events[i])) {
			return &r.events[i]
		}
	}
	return nil
}

func TestKimiAgentIntegration(t *testing.T) {
	if os.Getenv("KIMI_INTEGRATION") == "" {
		t.Skip("set KIMI_INTEGRATION=1 (requires an authenticated kimi CLI)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()

	a := NewKimiAgent()
	a.Verbose = false
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("hello\nworld\n"), 0644); err != nil {
		t.Fatal(err)
	}

	models, source := a.ListSupportedModels(ctx)
	t.Logf("models (%s): %+v", source, models)

	first := &kimiRecorder{}
	r1, err := a.SendMessage(ctx, Request{
		WorkDir:       workDir,
		Effort:        "low",
		StaticContext: "[Test Context]",
		Message:       "Read a.txt, use the Edit tool to change 'world' to 'kimi', then run `cat a.txt` with Bash. Reply with one short sentence.",
	}, first.callback)
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	t.Logf("turn 1: %+v text=%q", r1, first.text())
	if r1.SessionID == "" || !strings.HasPrefix(r1.Checkpoint, kimiCheckpointPrefix) {
		t.Fatalf("turn 1 result missing session/checkpoint: %+v", r1)
	}
	for _, name := range []string{"Read", "Edit", "Bash"} {
		if first.find(EventToolStart, func(e StreamEvent) bool { return e.ToolName == name }) == nil {
			t.Errorf("no %s tool start", name)
		}
	}
	if edit := first.find(EventToolResult, func(e StreamEvent) bool { return e.ToolName == "Edit" }); edit == nil || !strings.Contains(edit.Diff, "+kimi") {
		t.Errorf("edit result has no diff: %+v", edit)
	}
	if first.find(EventContextUsage, nil) == nil {
		t.Errorf("no context usage event")
	}
	if done := first.find(EventDone, nil); done == nil || done.Metadata["outputTokens"].(int) == 0 {
		t.Errorf("done event lacks usage: %+v", done)
	}

	second := &kimiRecorder{}
	var asked string
	r2, err := a.SendMessage(ctx, Request{
		WorkDir:   workDir,
		SessionID: r1.SessionID,
		Message:   "Use the AskUserQuestion tool to ask my favorite color with the options Red and Blue, then repeat my answer.",
		UserInputHandler: func(requestID, toolName, input string) (string, error) {
			asked = toolName + " " + input
			return `{"answers":{"What is your favorite color?":"Blue"}}`, nil
		},
	}, second.callback)
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	t.Logf("turn 2: asked=%s text=%q", asked, second.text())
	if !strings.HasPrefix(asked, kimiAskUserTool) || !strings.Contains(second.text(), "Blue") {
		t.Errorf("question not answered through the handler: asked=%q", asked)
	}

	if _, err := a.SendMessage(ctx, Request{WorkDir: workDir, Fork: &ForkContext{SessionID: r1.SessionID, Checkpoint: r1.Checkpoint}, Message: "hi"}, func(StreamEvent) {}); err == nil || !strings.Contains(err.Error(), "latest turn") {
		t.Errorf("fork from an older turn should be rejected, got %v", err)
	}
	forked := &kimiRecorder{}
	r3, err := a.SendMessage(ctx, Request{WorkDir: workDir, Fork: &ForkContext{SessionID: r1.SessionID, Checkpoint: r2.Checkpoint}, Message: "Which color did I pick earlier? Answer with one word."}, forked.callback)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if r3.SessionID == r1.SessionID || !strings.Contains(forked.text(), "Blue") {
		t.Errorf("fork lost context or reused the parent: %+v text=%q", r3, forked.text())
	}

	compacted := &kimiRecorder{}
	if _, err := a.SendMessage(ctx, Request{WorkDir: workDir, SessionID: r3.SessionID, Message: "/compact"}, compacted.callback); err != nil {
		t.Fatalf("/compact: %v", err)
	}
	if compacted.find(EventSystem, func(e StreamEvent) bool { return strings.HasPrefix(e.Content, "Compaction completed.") }) == nil {
		t.Errorf("/compact did not finish before the turn ended: %+v", compacted.events)
	}

	if _, err := a.SendMessage(ctx, Request{WorkDir: workDir, SessionID: "session_00000000-0000-0000-0000-000000000000", Message: "hi"}, func(StreamEvent) {}); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("unknown session should expire, got %v", err)
	}

	mcpURL, mcpHeader := kimiTestMCPServer(t)
	viaMCP := &kimiRecorder{}
	if _, err := a.SendMessage(ctx, Request{
		WorkDir:         workDir,
		ConversationID:  "conv-1",
		Message:         "Call the magic_number tool from the nexbox_test MCP server and reply with only the number it returns.",
		ExtraMCPServers: []MCPServerEntry{{Key: "nexbox_test", Type: "http", URL: mcpURL, Headers: map[string]string{"X-Test": "yes"}}},
	}, viaMCP.callback); err != nil {
		t.Fatalf("mcp turn: %v", err)
	}
	t.Logf("mcp turn text=%q header=%q", viaMCP.text(), mcpHeader())
	if !strings.Contains(viaMCP.text(), "4817") || mcpHeader() != "yes" {
		t.Errorf("MCP server was not attached: text=%q header=%q", viaMCP.text(), mcpHeader())
	}

	imagePath := filepath.Join(t.TempDir(), "swatch.png")
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{R: 255, A: 255}}, image.Point{}, draw.Src)
	var png bytes.Buffer
	if err := pngenc.Encode(&png, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imagePath, png.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	seen := &kimiRecorder{}
	if _, err := a.SendMessage(ctx, Request{WorkDir: workDir, Images: []string{imagePath}, Message: "What single color fills the attached image? Answer with one word, without using any tools."}, seen.callback); err != nil {
		t.Fatalf("image turn: %v", err)
	}
	if !strings.Contains(strings.ToLower(seen.text()), "red") {
		t.Errorf("image not seen: %q", seen.text())
	}

	cancelCtx, stop := context.WithCancel(ctx)
	started := make(chan struct{}, 1)
	go func() {
		<-started
		stop()
	}()
	begin := time.Now()
	_, err = a.SendMessage(cancelCtx, Request{WorkDir: workDir, SessionID: r3.SessionID, Message: "Run `sleep 60` with Bash, then say done."}, func(e StreamEvent) {
		if e.Type == EventToolStart {
			select {
			case started <- struct{}{}:
			default:
			}
		}
	})
	t.Logf("cancelled after %s: %v", time.Since(begin), err)
	if !errors.Is(err, context.Canceled) || time.Since(begin) > 50*time.Second {
		t.Errorf("cancel: err=%v after %s", err, time.Since(begin))
	}
}
