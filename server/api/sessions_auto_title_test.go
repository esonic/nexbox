package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/esonic/nexbox/agent"
	"github.com/esonic/nexbox/config"
)

type autoTitleTestAgent struct {
	models       []agent.ModelInfo
	response     string
	catalogCalls int
	sendCalls    int
	lastRequest  agent.Request
}

func (a *autoTitleTestAgent) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}

func (a *autoTitleTestAgent) ListSupportedModels(context.Context) ([]agent.ModelInfo, string) {
	a.catalogCalls++
	return append([]agent.ModelInfo(nil), a.models...), "dynamic"
}

func (a *autoTitleTestAgent) SendMessage(_ context.Context, req agent.Request, cb agent.EventCallback) (agent.Result, error) {
	a.sendCalls++
	a.lastRequest = req
	cb(agent.StreamEvent{Type: agent.EventTextDelta, Content: a.response})
	return agent.Result{}, nil
}

func newAutoTitleTestServer(t *testing.T, defaultAgent string, agents map[string]agent.Agent) *Server {
	t.Helper()
	cfg, err := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	if err := cfg.Update(func(c *config.Config) { c.DefaultAgent = defaultAgent }); err != nil {
		t.Fatalf("set default agent: %v", err)
	}
	return &Server{
		Config:    cfg,
		AgentName: defaultAgent,
		Agents:    agents,
	}
}

func TestGenerateAutoTitleClaudeKeepsHaiku(t *testing.T) {
	backend := &autoTitleTestAgent{
		response: "`修复登录问题`",
		models: []agent.ModelInfo{
			{ID: "should-not-be-read"},
		},
	}
	s := newAutoTitleTestServer(t, "claude", map[string]agent.Agent{"claude": backend})

	title, err := s.generateAutoTitle(context.Background(), t.TempDir(), "登录失败了")
	if err != nil {
		t.Fatalf("generateAutoTitle: %v", err)
	}
	if title != "修复登录问题" {
		t.Fatalf("title = %q, want %q", title, "修复登录问题")
	}
	if backend.lastRequest.Model != "haiku" || backend.lastRequest.Effort != "" {
		t.Fatalf("request model/effort = %q/%q, want haiku/empty", backend.lastRequest.Model, backend.lastRequest.Effort)
	}
	if backend.catalogCalls != 0 {
		t.Fatalf("Claude model catalog calls = %d, want 0", backend.catalogCalls)
	}
}

func TestGenerateAutoTitleCodexUsesWeakestModelAndLowestEffort(t *testing.T) {
	backend := &autoTitleTestAgent{
		response: "优化模型选择",
		models: []agent.ModelInfo{
			{ID: "gpt-5.6-sol", SupportedEfforts: []string{"low", "medium", "high"}},
			{ID: "gpt-5.6-terra", SupportedEfforts: []string{"low", "medium", "high"}},
			{ID: "gpt-5.4-mini", SupportedEfforts: []string{"low", "medium", "high"}},
		},
	}
	s := newAutoTitleTestServer(t, "codex", map[string]agent.Agent{"codex": backend})

	title, err := s.generateAutoTitle(context.Background(), t.TempDir(), "优化自动标题的模型选择")
	if err != nil {
		t.Fatalf("generateAutoTitle: %v", err)
	}
	if title != "优化模型选择" {
		t.Fatalf("title = %q, want %q", title, "优化模型选择")
	}
	if backend.lastRequest.Model != "gpt-5.4-mini" {
		t.Fatalf("request model = %q, want weakest picker model", backend.lastRequest.Model)
	}
	if backend.lastRequest.Effort != "low" {
		t.Fatalf("request effort = %q, want first supported effort", backend.lastRequest.Effort)
	}
	if backend.catalogCalls != 1 || backend.sendCalls != 1 {
		t.Fatalf("catalog/send calls = %d/%d, want 1/1", backend.catalogCalls, backend.sendCalls)
	}
}

func TestGenerateAutoTitleCodexEmptyCatalogReturnsError(t *testing.T) {
	backend := &autoTitleTestAgent{response: "unused"}
	s := newAutoTitleTestServer(t, "codex", map[string]agent.Agent{"codex": backend})

	if _, err := s.generateAutoTitle(context.Background(), t.TempDir(), "hello"); err == nil {
		t.Fatal("generateAutoTitle succeeded with an empty Codex model catalog")
	}
	if backend.sendCalls != 0 {
		t.Fatalf("send calls = %d, want 0", backend.sendCalls)
	}
}
