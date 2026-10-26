package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestKimiModelInfoFollowsACPThinkingRules(t *testing.T) {
	tests := []struct {
		name        string
		alias       string
		entry       string
		wantEfforts []string
		wantDefault string
	}{
		{"declared efforts on an always-thinking model", "kimi-code/k3", `{"model":"k3","maxContextSize":1048576,"capabilities":["always_thinking"],"displayName":"K3","supportEfforts":["low","high","max"],"defaultEffort":"max"}`, []string{"low", "high", "max"}, "max"},
		{"always-thinking boolean model has no picker", "kimi-code/kimi-for-coding", `{"model":"kimi-for-coding","maxContextSize":262144,"capabilities":["thinking","always_thinking"]}`, nil, ""},
		{"toggleable alias", "kimi-for-coding", `{"model":"plain-model","maxContextSize":262144}`, []string{"off", "on"}, "on"},
		{"name heuristic matches the alias", "my-reasoner", `{"model":"plain-model","maxContextSize":1}`, []string{"off", "on"}, "on"},
		{"not the upstream model name", "plain", `{"model":"deep-reasoner","maxContextSize":1}`, nil, ""},
		{"efforts without a declared default use the middle one", "m", `{"model":"m","maxContextSize":1,"capabilities":["thinking"],"supportEfforts":["low","medium","high"]}`, []string{"off", "low", "medium", "high"}, "medium"},
		{"overrides win", "m", `{"model":"m","maxContextSize":1,"capabilities":["thinking"],"supportEfforts":["low"],"overrides":{"supportEfforts":["high","max"],"defaultEffort":"high"}}`, []string{"off", "high", "max"}, "high"},
		{"no thinking", "plain", `{"model":"plain","maxContextSize":1}`, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var entry kimiModelAlias
			if err := json.Unmarshal([]byte(tt.entry), &entry); err != nil {
				t.Fatal(err)
			}
			info := kimiModelInfo(tt.alias, entry, false)
			if !reflect.DeepEqual(info.SupportedEfforts, tt.wantEfforts) || info.DefaultEffort != tt.wantDefault {
				t.Fatalf("efforts=%v default=%q, want %v %q", info.SupportedEfforts, info.DefaultEffort, tt.wantEfforts, tt.wantDefault)
			}
		})
	}
}

func TestKimiDefaultModelReadsTopLevelScalar(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", home)
	config := "# comment\ndefault_model = \"kimi-code/k3\"\n\n[models.\"x\"]\ndefault_model = \"nested\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if got := kimiDefaultModel(); got != "kimi-code/k3" {
		t.Fatalf("default model = %q", got)
	}
}

func TestUnifiedDiffFromTexts(t *testing.T) {
	if got := unifiedDiffFromTexts("/w/a.txt", "world", "kimi"); got != "--- a//w/a.txt\n+++ b//w/a.txt\n@@ -1,1 +1,1 @@\n-world\n+kimi\n" {
		t.Fatalf("snippet diff:\n%s", got)
	}
	var before, after []string
	for i := 1; i <= 20; i++ {
		line := "line" + strings.Repeat("x", i)
		before = append(before, line)
		if i == 2 {
			line = "changed"
		}
		if i == 18 {
			continue
		}
		after = append(after, line)
	}
	got := unifiedDiffFromTexts("f", strings.Join(before, "\n")+"\n", strings.Join(after, "\n")+"\n")
	if strings.Count(got, "@@ -") != 2 || !strings.Contains(got, "@@ -1,5 +1,5 @@\n linex\n-linexx\n+changed\n") || !strings.Contains(got, "@@ -15,6 +15,5 @@\n") {
		t.Fatalf("multi-hunk diff:\n%s", got)
	}
	if got := unifiedDiffFromTexts("f", "", "new\n"); got != "--- /dev/null\n+++ b/f\n@@ -1,0 +1,1 @@\n+new\n" {
		t.Fatalf("creation diff:\n%s", got)
	}
}

func kimiTestTurn(events *[]StreamEvent) *kimiTurn {
	return newKimiTurn(func(e StreamEvent) { *events = append(*events, e) }, "/w")
}

func kimiUpdate(t *testing.T, turn *kimiTurn, update string) {
	t.Helper()
	turn.handleUpdate(json.RawMessage(`{"sessionId":"s","update":` + update + `}`))
}

func TestKimiTurnMapsStreamedToolLifecycle(t *testing.T) {
	var events []StreamEvent
	turn := kimiTestTurn(&events)
	// Recorded from kimi 2.2.0: lazy create, argument deltas, dispatch, result.
	kimiUpdate(t, turn, `{"sessionUpdate":"tool_call","toolCallId":"0:e","title":"Edit","kind":"edit","status":"pending","content":[{"type":"content","content":{"type":"text","text":""}}]}`)
	kimiUpdate(t, turn, `{"sessionUpdate":"tool_call_update","toolCallId":"0:e","status":"in_progress","content":[{"type":"content","content":{"type":"text","text":"{\"path\": \"a"}}]}`)
	kimiUpdate(t, turn, `{"sessionUpdate":"tool_call_update","toolCallId":"0:e","title":"Editing a.txt","kind":"edit","status":"in_progress","rawInput":{"path":"a.txt","old_string":"world","new_string":"kimi"},"content":[{"type":"diff","path":"/w/a.txt","oldText":"world","newText":"kimi"},{"type":"content","content":{"type":"text","text":"{}"}}]}`)
	kimiUpdate(t, turn, `{"sessionUpdate":"tool_call_update","toolCallId":"0:e","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Replaced 1 occurrence in a.txt"}}],"rawOutput":"Replaced 1 occurrence in a.txt"}`)

	if len(events) != 3 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Type != EventToolStart || events[0].ToolName != "Edit" || events[0].Input != "" {
		t.Fatalf("start = %+v", events[0])
	}
	if events[1].Type != EventToolInputReplace || events[1].Input != `{"new_string":"kimi","old_string":"world","path":"a.txt"}` {
		t.Fatalf("input replace = %+v", events[1])
	}
	result := events[2]
	if result.Type != EventToolResult || result.ToolName != "Edit" || result.Output != "Replaced 1 occurrence in a.txt" || !strings.Contains(result.Diff, "-world\n+kimi") || result.IsError {
		t.Fatalf("result = %+v", result)
	}
}

func TestKimiTurnNamesUnstreamedToolsAndDiffsWrites(t *testing.T) {
	var events []StreamEvent
	turn := kimiTestTurn(&events)
	kimiUpdate(t, turn, `{"sessionUpdate":"tool_call","toolCallId":"1:w","title":"Writing b.txt","kind":"edit","status":"in_progress","rawInput":{"path":"b.txt","content":"x\ny\n"}}`)
	kimiUpdate(t, turn, `{"sessionUpdate":"tool_call_update","toolCallId":"1:w","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Wrote 4 bytes to b.txt"}}]}`)
	kimiUpdate(t, turn, `{"sessionUpdate":"tool_call","toolCallId":"1:b","title":"Running: false","kind":"execute","status":"in_progress","rawInput":{"command":"false"}}`)
	kimiUpdate(t, turn, `{"sessionUpdate":"tool_call_update","toolCallId":"1:b","status":"failed","rawOutput":"exit 1"}`)

	if events[0].ToolName != "Write" || events[1].Diff != "--- /dev/null\n+++ b//w/b.txt\n@@ -1,0 +1,2 @@\n+x\n+y\n" {
		t.Fatalf("write events = %+v", events[:2])
	}
	if events[2].ToolName != "Bash" || events[3].Output != "exit 1" || !events[3].IsError {
		t.Fatalf("bash events = %+v", events[2:])
	}
}

func TestKimiTurnMapsTextThinkingCompactionAndUsage(t *testing.T) {
	var events []StreamEvent
	turn := kimiTestTurn(&events)
	turn.setModel("kimi-code/k3")
	kimiUpdate(t, turn, `{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hmm"}}`)
	kimiUpdate(t, turn, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Compacting conversation context…"}}`)
	compacting := turn.isCompacting()
	kimiUpdate(t, turn, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello"}}`)
	kimiUpdate(t, turn, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Compaction completed.\n- Messages compacted: 3\n- Tokens before: 9\n- Tokens after: 2"}}`)
	kimiUpdate(t, turn, `{"sessionUpdate":"usage_update","used":23451,"size":200000}`)

	want := []StreamEvent{
		{Type: EventThinking, ToolName: "Thinking", Content: "hmm"},
		{Type: EventSystem, Content: "Compacting conversation context…"},
		{Type: EventTextDelta, Content: "Hello"},
		{Type: EventSystem, Content: "Compaction completed.\n- Messages compacted: 3\n- Tokens before: 9\n- Tokens after: 2"},
		{Type: EventContextUsage, Metadata: map[string]interface{}{"usedTokens": 23451, "contextWindow": 200000, "model": "kimi-code/k3"}},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %+v", events)
	}
	if !compacting || turn.isCompacting() || len(turn.compacted) != 1 {
		t.Fatalf("compaction not tracked")
	}
	// A later compaction must not be satisfied by the earlier one's signal.
	kimiUpdate(t, turn, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Compacting conversation context…"}}`)
	if !turn.isCompacting() || len(turn.compacted) != 0 {
		t.Fatalf("stale compaction signal survived a new compaction")
	}
	if !turn.producedOutput() || len(turn.usage) != 1 {
		t.Fatalf("output/usage signals not recorded")
	}
}

func TestKimiElicitationUsesQuestionCardAndMapsAnswers(t *testing.T) {
	var events []StreamEvent
	turn := kimiTestTurn(&events)
	kimiUpdate(t, turn, `{"sessionUpdate":"tool_call","toolCallId":"0:q","title":"AskUserQuestion","kind":"other","status":"pending"}`)
	kimiUpdate(t, turn, `{"sessionUpdate":"tool_call_update","toolCallId":"0:q","status":"in_progress","rawInput":{"questions":[{"question":"Which areas?","header":"Areas","multiSelect":true,"options":[{"label":"Auth"},{"label":"Uploads, files"}]},{"question":"Ship it?","options":[{"label":"Yes"},{"label":"No"}]}]}}`)
	params := `{"sessionId":"s","toolCallId":"0:q","mode":"form","message":"Which areas?\nShip it?","requestedSchema":{"type":"object","properties":{
		"q0":{"type":"array","title":"Areas","minItems":1,"items":{"anyOf":[{"const":"Auth","title":"Auth"},{"const":"Uploads, files","title":"Uploads, files"}]}},
		"q1":{"type":"string","title":"Ship it?","oneOf":[{"const":"Yes","title":"Yes"},{"const":"No","title":"No","description":"not yet"}]}},"required":["q0","q1"]}}`

	var gotTool, gotInput string
	response := turn.handleElicitation(context.Background(), func(id, tool, input string) (string, error) {
		gotTool, gotInput = tool, input
		return `{"answers":{"Which areas?":"Auth, Uploads, files","Ship it?":"No"}}`, nil
	}, json.RawMessage(params))

	if gotTool != kimiAskUserTool || !strings.Contains(gotInput, `"question":"Which areas?","header":"Areas","multiSelect":true`) || !strings.Contains(gotInput, `{"label":"No","description":"not yet"}`) {
		t.Fatalf("question card input = %s", gotInput)
	}
	want := map[string]interface{}{"action": "accept", "content": map[string]interface{}{"q0": []string{"Auth", "Uploads, files"}, "q1": "No"}}
	if !reflect.DeepEqual(response, want) {
		t.Fatalf("response = %#v", response)
	}

	skipped := turn.handleElicitation(context.Background(), func(string, string, string) (string, error) {
		return "", context.Canceled
	}, json.RawMessage(params))
	if !reflect.DeepEqual(skipped, kimiCancelledElicitation) {
		t.Fatalf("skip = %#v", skipped)
	}
}

func TestKimiPermissionAutoApprovesAndAsksQuestions(t *testing.T) {
	var events []StreamEvent
	turn := kimiTestTurn(&events)
	approval := `{"sessionId":"s","options":[{"optionId":"reject","name":"Reject","kind":"reject_once"},{"optionId":"always","name":"Always","kind":"allow_always"},{"optionId":"once","name":"Allow","kind":"allow_once"}],"toolCall":{"toolCallId":"0:b","title":"Bash: rm -rf build"}}`
	if got := turn.handlePermission(context.Background(), nil, json.RawMessage(approval)); !reflect.DeepEqual(got, kimiSelectedPermission("once")) {
		t.Fatalf("approval = %#v", got)
	}

	question := `{"sessionId":"s","options":[{"optionId":"q0_opt_0","name":"Red","kind":"allow_once"},{"optionId":"q0_opt_1","name":"Blue","kind":"allow_once"},{"optionId":"q0_skip","name":"Skip","kind":"reject_once"}],"toolCall":{"toolCallId":"0:q","title":"AskUserQuestion","content":[{"type":"content","content":{"type":"text","text":"Favorite color?"}}]}}`
	got := turn.handlePermission(context.Background(), func(id, tool, input string) (string, error) {
		return `{"answers":{"Favorite color?":"Blue"}}`, nil
	}, json.RawMessage(question))
	if !reflect.DeepEqual(got, kimiSelectedPermission("q0_opt_1")) {
		t.Fatalf("question = %#v", got)
	}
}

// writeKimiSession lays out a session the way Kimi persists it.
func writeKimiSession(t *testing.T, home, sessionID, cwd string, wire string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "wd_001_test", sessionID)
	if err := os.MkdirAll(filepath.Join(dir, "agents", "main"), 0700); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(map[string]string{"id": sessionID, "cwd": cwd})
	if err := os.WriteFile(filepath.Join(dir, "state.json"), state, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agents", "main", "wire.jsonl"), []byte(wire), 0600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "agents", "main", "wire.jsonl")
}

const kimiTestWire = `{"type":"metadata","protocol_version":"1"}
{"type":"turn.prompt","agentId":"main","turnId":0}
{"type":"usage.record","agentId":"main","model":"m","usage":{"inputOther":3,"output":86,"inputCacheRead":0,"inputCacheCreation":22982},"usageScope":"turn"}
{"type":"turn.ended","agentId":"main","turnId":0,"reason":"completed"}
{"turnId":0,"outcome":"completed","type":"agent.turn.ended","kind":"event"}
`

func TestScanKimiWireReportsTurnsUsageAndFailures(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", home)
	wire := writeKimiSession(t, home, "session_a", "/w", kimiTestWire)
	first := scanKimiWire(kimiWirePath("session_a"), 0)
	if !first.ok || first.turnsEnded != 1 || first.failure != "" || first.usage != (kimiUsage{InputOther: 3, Output: 86, InputCacheCreation: 22982}) {
		t.Fatalf("first scan = %+v", first)
	}

	appended := `{"type":"usage.record","agentId":"main","usage":{"inputOther":1,"output":2,"inputCacheRead":5,"inputCacheCreation":0},"usageScope":"turn"}
{"type":"turn.ended","agentId":"main","turnId":0,"reason":"failed","error":{"code":"provider.api_error","message":"404 not found"}}
{"type":"turn.ended","agentId":"main","tur`
	file, err := os.OpenFile(wire, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString(appended)
	file.Close()
	next := scanKimiWire(wire, first.size)
	if next.turnsEnded != 1 || next.failure != "[provider.api_error] 404 not found" || next.usage != (kimiUsage{InputOther: 1, Output: 2, InputCacheRead: 5}) {
		t.Fatalf("incremental scan = %+v", next)
	}
	if missing := scanKimiWire(filepath.Join(home, "nope"), 0); missing.ok {
		t.Fatalf("missing log reported ok")
	}
}

func TestKimiForkPreflight(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", home)
	cwd := t.TempDir()
	writeKimiSession(t, home, "session_p", cwd, kimiTestWire)
	writeKimiSession(t, home, "session_busy", cwd, kimiTestWire+`{"type":"turn.prompt","agentId":"main","turnId":1}`+"\n")

	if err := kimiForkPreflight(&ForkContext{SessionID: "session_p", Checkpoint: kimiCheckpointPrefix + "1"}, cwd); err != nil {
		t.Fatalf("latest-turn fork rejected: %v", err)
	}
	for name, tt := range map[string]struct {
		fork    ForkContext
		workDir string
		want    string
	}{
		"parent mid-turn":    {ForkContext{SessionID: "session_busy", Checkpoint: kimiCheckpointPrefix + "1"}, cwd, "still running a turn"},
		"older turn":         {ForkContext{SessionID: "session_p", Checkpoint: kimiCheckpointPrefix + "0"}, cwd, "latest turn"},
		"other directory":    {ForkContext{SessionID: "session_p", Checkpoint: kimiCheckpointPrefix + "1"}, t.TempDir(), "parent session's directory"},
		"foreign checkpoint": {ForkContext{SessionID: "session_p", Checkpoint: "pi-leaf-v1:x"}, cwd, "invalid checkpoint"},
		"missing parent":     {ForkContext{SessionID: "session_gone", Checkpoint: kimiCheckpointPrefix + "1"}, cwd, "not found"},
	} {
		err := kimiForkPreflight(&tt.fork, tt.workDir)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tt.want)
		}
	}
}

func TestKimiMCPServersIncludeNexBoxChromeAndExtras(t *testing.T) {
	a := &KimiAgent{MCPServerURL: "http://127.0.0.1:1/mcp"}
	servers := a.mcpServers(Request{
		ConversationID: "c1",
		Features:       []string{"chrome_devtools"},
		ChromeMCPURL:   "http://127.0.0.1:2/mcp",
		ExtraMCPServers: []MCPServerEntry{
			{Key: "gh", Type: "http", URL: "https://example.test/mcp", Headers: map[string]string{"B": "2", "A": "1"}, AppendSessionURL: true},
			{Key: "fs", Type: "stdio", Cmd: "mcp-fs", Args: []string{"--root", "/"}, Env: map[string]string{"K": "V"}},
		},
	})
	data, _ := json.Marshal(servers)
	want := `[{"headers":[],"name":"nexbox","type":"http","url":"http://127.0.0.1:1/mcp?session=c1"},` +
		`{"headers":[],"name":"chrome","type":"http","url":"http://127.0.0.1:2/mcp"},` +
		`{"headers":[{"name":"A","value":"1"},{"name":"B","value":"2"}],"name":"gh","type":"http","url":"https://example.test/mcp?session=c1"},` +
		`{"args":["--root","/"],"command":"mcp-fs","env":[{"name":"K","value":"V"}],"name":"fs"}]`
	if string(data) != want {
		t.Fatalf("servers = %s", data)
	}
}
