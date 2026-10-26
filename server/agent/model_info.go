package agent

// ModelInfo is the unified per-model metadata the UI consumes. Fields are
// populated from whichever CLI is serving the session (Claude's
// initialize control_response or Codex's `model/list` RPC) — both expose
// this information directly, so we do NOT maintain a hand-curated catalog.
type ModelInfo struct {
	// ID is the slug passed to the backend CLI. For Claude this is the
	// alias or full model ID (e.g. "sonnet", "opus[1m]", "default",
	// "claude-opus-4-7"). For Codex it's the model name (e.g. "gpt-5.4").
	ID string `json:"id"`

	// Label is the human-readable name returned by the CLI
	// (Claude: `displayName`; Codex: `displayName`).
	Label string `json:"label"`

	// Description is the CLI-provided description (may be empty).
	Description string `json:"description,omitempty"`

	// SupportedEfforts is the set of reasoning-effort levels the CLI says
	// this model accepts. Empty means the model doesn't support effort
	// (e.g. Claude Haiku 4.5).
	SupportedEfforts []string `json:"supported_efforts,omitempty"`

	// DefaultEffort is the effort level pre-selected for the model.
	// Codex returns it explicitly (defaultReasoningEffort); Claude's
	// initialize response does not, so ClaudeAgent picks `medium` as a
	// safe default (falling back to `high` then `low`), and never
	// auto-selects the opt-in `xhigh` / `max` modes.
	DefaultEffort string `json:"default_effort,omitempty"`

	// ContextWindow is the nominal input-token capacity. When 0, the UI
	// waits for the first `modelUsage.contextWindow` value reported at
	// the end of a turn before rendering a usage percentage.
	ContextWindow int `json:"context_window,omitempty"`

	// IsDefault marks the model the CLI will pick if no `--model` flag
	// is passed.
	IsDefault bool `json:"is_default,omitempty"`

	// Hidden is true when the CLI marks the model as hidden from the
	// default picker. Kept in the response so admin UIs can opt in.
	Hidden bool `json:"hidden,omitempty"`

	// Capabilities carries optional CLI-specific flags (e.g.
	// `supportsAdaptiveThinking`, `supportsAutoMode`, `supportsFastMode`
	// for Claude). The UI surfaces them only when set.
	Capabilities map[string]bool `json:"capabilities,omitempty"`
}
