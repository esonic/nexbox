package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const piCapabilityCacheTTL = time.Minute
const piCapabilityProbeTimeout = 3 * time.Second

type piModelCapability struct {
	ContextWindow int
	Efforts       []string
	DefaultEffort string
	EffortsKnown  bool
}

func (a *PiAgent) nativePiEnv() []string {
	env := os.Environ()
	if a.AgentDir != "" {
		env = append(env, "PI_CODING_AGENT_DIR="+a.AgentDir)
	}
	return env
}

// Query Pi for exact capabilities of the user-scoped model catalog.
func (a *PiAgent) enrichPiModels(ctx context.Context, models []ModelInfo) {
	if len(models) == 0 {
		return
	}
	ids := make([]string, len(models))
	for i := range models {
		ids[i] = models[i].ID
	}
	key := a.CLIPath + "\x00" + a.AgentDir + "\x00" + a.piConfigVersion() + "\x00" + strings.Join(ids, "\x00")
	capabilities := a.nativePiCapabilities(ctx, key, models)
	for i := range models {
		capability, ok := capabilities[models[i].ID]
		if !ok {
			continue
		}
		if capability.ContextWindow > 0 {
			models[i].ContextWindow = capability.ContextWindow
		}
		if capability.EffortsKnown {
			models[i].SupportedEfforts = append([]string(nil), capability.Efforts...)
			defaultEffort := capability.DefaultEffort
			if defaultEffort == "" && len(capability.Efforts) > 0 {
				if slices.Contains(capability.Efforts, models[i].DefaultEffort) {
					defaultEffort = models[i].DefaultEffort
				} else {
					defaultEffort = capability.Efforts[0]
				}
			}
			models[i].DefaultEffort = defaultEffort
		}
	}
}

// Native defaults and custom model definitions can change independently of
// the enabled IDs. A settings edit must invalidate the short-lived RPC cache.
func (a *PiAgent) piConfigVersion() string {
	dir := a.AgentDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".pi", "agent")
	}
	var version strings.Builder
	for _, name := range []string{"settings.json", "models.json"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
			version.WriteString(name + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + ":" + strconv.FormatInt(info.Size(), 10) + ";")
		}
	}
	return version.String()
}

// Coalesce simultaneous probes and reuse successful results briefly. Failed
// probes retain the CLI table's data and can be retried on the next request.
func (a *PiAgent) nativePiCapabilities(ctx context.Context, key string, models []ModelInfo) map[string]piModelCapability {
	for {
		if ctx.Err() != nil {
			return nil
		}
		a.capabilityMu.Lock()
		if a.capabilityKey == key && time.Since(a.capabilityCachedAt) < piCapabilityCacheTTL {
			cached := a.capabilityCache
			a.capabilityMu.Unlock()
			return cached
		}
		if pending := a.capabilityProbe; pending != nil {
			a.capabilityMu.Unlock()
			select {
			case <-ctx.Done():
				return nil
			case <-pending:
				continue
			}
		}
		a.capabilityProbe = make(chan struct{})
		a.capabilityMu.Unlock()
		result := a.probePiCapabilities(ctx, models)
		a.capabilityMu.Lock()
		if len(result) > 0 && ctx.Err() == nil {
			a.capabilityCache, a.capabilityKey, a.capabilityCachedAt = result, key, time.Now()
		}
		close(a.capabilityProbe)
		a.capabilityProbe = nil
		a.capabilityMu.Unlock()
		return result
	}
}

func (a *PiAgent) probePiCapabilities(ctx context.Context, models []ModelInfo) map[string]piModelCapability {
	ctx, cancel := context.WithTimeout(ctx, piCapabilityProbeTimeout)
	defer cancel()
	args := []string{"--mode", "rpc", "--no-session", "--no-approve", "--no-extensions", "--no-mcp"}
	var available struct {
		Models []struct {
			Provider      string `json:"provider"`
			ID            string `json:"id"`
			ContextWindow int    `json:"contextWindow"`
			Reasoning     bool   `json:"reasoning"`
		} `json:"models"`
	}
	var client *piRPCClient
	for {
		if ctx.Err() != nil {
			return nil
		}
		client = newPiRPCClient(a.CLIPath, args, false)
		client.env = append(a.nativePiEnv(), "PI_OFFLINE=1")
		if client.start(os.TempDir()) != nil {
			return nil
		}
		if client.command(ctx, "get_available_models", nil, &available) == nil {
			break
		}
		client.close()
		client.stderrMu.Lock()
		unsupportedMCPFlag := strings.Contains(string(client.stderr), "--no-mcp")
		client.stderrMu.Unlock()
		// Older Pi releases do not accept --no-mcp. All other failures
		// fall back without repeatedly spawning an incompatible process.
		if ctx.Err() != nil || !unsupportedMCPFlag || args[len(args)-1] != "--no-mcp" {
			return nil
		}
		args = args[:len(args)-1]
	}
	defer client.close()
	wanted := make(map[string]bool, len(models))
	for _, model := range models {
		wanted[model.ID] = true
	}
	result := make(map[string]piModelCapability)
	for _, model := range available.Models {
		id := model.Provider + "/" + model.ID
		if !wanted[id] {
			continue
		}
		capability := piModelCapability{ContextWindow: model.ContextWindow, EffortsKnown: !model.Reasoning}
		result[id] = capability
		if !model.Reasoning {
			continue
		}
		if client.command(ctx, "set_model", map[string]interface{}{"provider": model.Provider, "modelId": model.ID}, nil) != nil {
			continue
		}
		var levels struct {
			Levels []string `json:"levels"`
		}
		if client.command(ctx, "get_available_thinking_levels", nil, &levels) != nil {
			continue
		}
		if levels.Levels == nil {
			continue
		}
		for _, level := range levels.Levels {
			if level != "" && !slices.Contains(capability.Efforts, level) {
				capability.Efforts = append(capability.Efforts, level)
			}
		}
		capability.EffortsKnown = true
		if len(capability.Efforts) > 0 {
			var state struct {
				ThinkingLevel string `json:"thinkingLevel"`
			}
			_ = client.command(ctx, "get_state", nil, &state)
			if slices.Contains(capability.Efforts, state.ThinkingLevel) {
				capability.DefaultEffort = state.ThinkingLevel
			}
		}
		result[id] = capability
	}
	return result
}
