package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func piCapabilitiesFixture(t *testing.T, prefix string) *PiAgent {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "pi")
	script := `#!/bin/sh
case "$*" in
 *--list-models*) printf '%s\n' 'provider model context max-out thinking images' 'openrouter deepseek/deepseek-v4.1-flash 128K 8K yes no' 'openrouter plain 8K 8K no no'; exit ;;
esac
printf '%s\n' "$*" >> "$0.starts"
[ "$PI_CODING_AGENT_DIR" = "${0%/*}" ] || exit 2
` + prefix + `
while IFS= read -r line; do
 printf '%s\n' "$line" >> "$0.calls"
 id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
 case "$line" in
 *'"type":"get_available_models"'*) printf '{"id":"%s","type":"response","success":true,"data":{"models":[{"provider":"openrouter","id":"deepseek/deepseek-v4.1-flash","contextWindow":262144,"reasoning":true},{"provider":"openrouter","id":"plain","contextWindow":8192,"reasoning":false}]}}\n' "$id" ;;
 *'"type":"set_model"'*) printf '{"id":"%s","type":"response","success":true}\n' "$id" ;;
 *'"type":"get_available_thinking_levels"'*) printf '{"id":"%s","type":"response","success":true,"data":{"levels":["off","low","high","high",""]}}\n' "$id" ;;
 *'"type":"get_state"'*) printf '{"id":"%s","type":"response","success":true,"data":{"thinkingLevel":"high"}}\n' "$id" ;;
 esac
done
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return &PiAgent{CLIPath: path, AgentDir: dir}
}

func TestPiNativeCapabilitiesMergeLegacyEffortEntries(t *testing.T) {
	a := piCapabilitiesFixture(t, "")
	settings := `{"defaultProvider":"openrouter","defaultModel":"plain","enabledModels":["openrouter/deepseek/deepseek-v4.1-flash","openrouter/plain","openrouter/deepseek/deepseek-v4.1-flash:high"]}`
	if err := os.WriteFile(filepath.Join(a.AgentDir, "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	models, source := a.ListSupportedModels(context.Background())
	if source != "configured" || len(models) != 2 {
		t.Fatalf("%s: %+v", source, models)
	}
	if models[0].ID != "openrouter/plain" || !models[0].IsDefault || models[1].ID != "openrouter/deepseek/deepseek-v4.1-flash" {
		t.Fatalf("scope or order changed: %+v", models)
	}
	if models[1].ContextWindow != 262144 || models[1].DefaultEffort != "high" || !reflect.DeepEqual(models[1].SupportedEfforts, []string{"off", "low", "high"}) {
		t.Fatalf("native capabilities: %+v", models[1])
	}
	calls, err := os.ReadFile(a.CLIPath + ".calls")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), `"type":"set_model"`) != 1 {
		t.Fatalf("legacy alias caused duplicate probing: %s", calls)
	}
}

func TestPiNativeCapabilitiesCoalesceAndDoNotLeakMutableSlices(t *testing.T) {
	a := piCapabilitiesFixture(t, "")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			models := []ModelInfo{{ID: "openrouter/deepseek/deepseek-v4.1-flash"}}
			a.enrichPiModels(context.Background(), models)
			if len(models[0].SupportedEfforts) != 3 {
				t.Errorf("capabilities missing: %+v", models)
			} else {
				models[0].SupportedEfforts[0] = "mutated"
			}
		}()
	}
	wg.Wait()
	starts, err := os.ReadFile(a.CLIPath + ".starts")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(starts), "--mode rpc") != 1 {
		t.Fatalf("concurrent callers spawned multiple probes: %s", starts)
	}
	models := []ModelInfo{{ID: "openrouter/deepseek/deepseek-v4.1-flash"}}
	a.enrichPiModels(context.Background(), models)
	if models[0].SupportedEfforts[0] != "off" {
		t.Fatal("cached capabilities were mutated by a caller")
	}
}

func TestPiNativeCapabilitiesRetryLegacyFlag(t *testing.T) {
	a := piCapabilitiesFixture(t, `case "$*" in *--no-mcp*) echo 'unknown option --no-mcp' >&2; exit 2;; esac`)
	models := []ModelInfo{{ID: "openrouter/deepseek/deepseek-v4.1-flash"}}
	a.enrichPiModels(context.Background(), models)
	if models[0].ContextWindow != 262144 {
		t.Fatalf("legacy fallback lost capabilities: %+v", models)
	}
	starts, _ := os.ReadFile(a.CLIPath + ".starts")
	if strings.Count(string(starts), "--mode rpc") != 2 {
		t.Fatalf("unexpected fallback attempts: %s", starts)
	}
}

func TestPiNativeCapabilitiesRefreshAfterConfigurationChanges(t *testing.T) {
	a := piCapabilitiesFixture(t, "")
	query := func() {
		a.enrichPiModels(context.Background(), []ModelInfo{{ID: "openrouter/deepseek/deepseek-v4.1-flash"}})
	}
	query()
	if err := os.WriteFile(filepath.Join(a.AgentDir, "settings.json"), []byte(`{"defaultThinkingLevel":"low"}`), 0600); err != nil {
		t.Fatal(err)
	}
	query()
	if err := os.WriteFile(filepath.Join(a.AgentDir, "models.json"), []byte(`{"providers":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	query()
	starts, _ := os.ReadFile(a.CLIPath + ".starts")
	if strings.Count(string(starts), "--mode rpc") != 3 {
		t.Fatalf("configuration changes reused stale capabilities: %s", starts)
	}
}

func TestPiNativeCapabilitiesTimeoutPreservesFallback(t *testing.T) {
	a := piCapabilitiesFixture(t, `while IFS= read -r line; do :; done; exit`)
	models := []ModelInfo{{ID: "openrouter/deepseek/deepseek-v4.1-flash", ContextWindow: 128000, SupportedEfforts: []string{"low", "medium"}, DefaultEffort: "medium"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	a.enrichPiModels(ctx, models)
	if time.Since(started) > time.Second {
		t.Fatal("canceled probe did not stop promptly")
	}
	if models[0].ContextWindow != 128000 || models[0].DefaultEffort != "medium" || len(models[0].SupportedEfforts) != 2 {
		t.Fatalf("fallback changed: %+v", models)
	}
}

func TestPiNativeCapabilitiesPreserveUnknownModelsAndClearPlainModelEffort(t *testing.T) {
	a := piCapabilitiesFixture(t, "")
	models := []ModelInfo{
		{ID: "openrouter/plain", ContextWindow: 4000, SupportedEfforts: []string{"high"}, DefaultEffort: "high"},
		{ID: "openrouter/missing", ContextWindow: 9000, SupportedEfforts: []string{"low"}, DefaultEffort: "low"},
	}
	a.enrichPiModels(context.Background(), models)
	if models[0].ContextWindow != 8192 || len(models[0].SupportedEfforts) != 0 || models[0].DefaultEffort != "" {
		t.Fatalf("plain model retained unsupported thinking: %+v", models[0])
	}
	if models[1].ContextWindow != 9000 || models[1].DefaultEffort != "low" || !reflect.DeepEqual(models[1].SupportedEfforts, []string{"low"}) {
		t.Fatalf("missing model lost fallback information: %+v", models[1])
	}
}

// This launches the installed CLI against temporary native configuration.
// Capability discovery must not call a model or rewrite the user's defaults.
func TestPiNativeCapabilityCLIIntegration(t *testing.T) {
	if os.Getenv("PI_CAPABILITIES_INTEGRATION") != "1" {
		t.Skip("set PI_CAPABILITIES_INTEGRATION=1 for the installed Pi CLI")
	}
	cli, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("Pi CLI is not installed")
	}
	dir := t.TempDir()
	t.Setenv("PI_CAPABILITIES_FIXTURE_KEY", "local-fixture-only")
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "discovery must not invoke a model", 500)
	}))
	defer upstream.Close()
	config := map[string]any{"providers": map[string]any{"fixture-openrouter": map[string]any{
		"baseUrl": upstream.URL + "/v1", "api": "openai-completions", "apiKey": "$PI_CAPABILITIES_FIXTURE_KEY",
		"models": []map[string]any{{"id": "deepseek/flash", "name": "Native fixture", "contextWindow": 262144, "maxTokens": 8192, "reasoning": true, "input": []string{"text"}, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}},
	}}}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "models.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	settings := []byte(`{"defaultProvider":"fixture-openrouter","defaultModel":"deepseek/flash","defaultThinkingLevel":"low","enabledModels":["fixture-openrouter/deepseek/flash:high"]}`)
	if err = os.WriteFile(filepath.Join(dir, "settings.json"), settings, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a := &PiAgent{CLIPath: cli, AgentDir: dir}
	models, source := a.ListSupportedModels(ctx)
	if source != "configured" || len(models) != 1 || models[0].ID != "fixture-openrouter/deepseek/flash" || models[0].ContextWindow != 262144 || models[0].DefaultEffort != "low" || !slices.Contains(models[0].SupportedEfforts, "low") {
		t.Fatalf("native discovery: %s %+v", source, models)
	}
	a.capabilityMu.Lock()
	queried := len(a.capabilityCache) > 0
	a.capabilityMu.Unlock()
	if !queried {
		t.Fatal("native RPC discovery silently fell back to the CLI table")
	}
	if requests.Load() != 0 {
		t.Fatal("capability discovery invoked a model")
	}
	stored, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil || string(stored) != string(settings) {
		t.Fatalf("native defaults were rewritten: %v", err)
	}
}
