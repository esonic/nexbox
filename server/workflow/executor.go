package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"gitlab.okg.com/sre_ai/nexbox/store"
)

// NodeExecutor defines the interface for executing different node types
type NodeExecutor interface {
	Execute(ctx context.Context, node *store.WorkflowNode, execCtx *ExecutionContext) (interface{}, error)
}

// ExecutionContext holds the context for a workflow execution
type ExecutionContext struct {
	ExecutionID string
	WorkflowID  string
	Input       map[string]interface{}
	NodeOutputs map[string]interface{}
	Store       store.WorkflowStore
	mu          sync.RWMutex // Mutex for thread-safe access to NodeOutputs
}

// SetNodeOutput safely sets a node's output
func (ec *ExecutionContext) SetNodeOutput(nodeID string, output interface{}) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.NodeOutputs[nodeID] = output
}

// GetNodeOutput safely gets a node's output
func (ec *ExecutionContext) GetNodeOutput(nodeID string) (interface{}, bool) {
	ec.mu.RLock()
	defer ec.mu.RUnlock()
	output, ok := ec.NodeOutputs[nodeID]
	return output, ok
}

// ReplaceVariables replaces template variables in a string
// Supports: {{input.key}}, {{node-id.output}}, {{node-id.output.nested.key}}
func ReplaceVariables(template string, execCtx *ExecutionContext) string {
	re := regexp.MustCompile(`\{\{([^}]+)\}\}`)

	result := re.ReplaceAllStringFunc(template, func(match string) string {
		// Extract variable path (e.g., "input.key" or "node-1.output")
		varPath := strings.TrimSpace(match[2 : len(match)-2])
		parts := strings.Split(varPath, ".")

		if len(parts) == 0 {
			return match
		}

		// Handle {{input.key}}
		if parts[0] == "input" {
			if len(parts) == 1 {
				// {{input}} - return entire input as JSON
				jsonBytes, _ := json.Marshal(execCtx.Input)
				return string(jsonBytes)
			}
			// Navigate nested keys
			value := execCtx.Input
			for _, key := range parts[1:] {
				if v, ok := value[key]; ok {
					if nested, ok := v.(map[string]interface{}); ok {
						value = nested
					} else {
						return fmt.Sprintf("%v", v)
					}
				} else {
					return match // Key not found
				}
			}
			jsonBytes, _ := json.Marshal(value)
			return string(jsonBytes)
		}

		// Handle {{node-id.output}} or {{node-id}}
		nodeID := parts[0]
		if output, ok := execCtx.GetNodeOutput(nodeID); ok {
			if len(parts) == 1 {
				// {{node-id}} - return entire output as JSON
				jsonBytes, _ := json.Marshal(output)
				return string(jsonBytes)
			}

			// Navigate nested keys in output
			outputMap, ok := output.(map[string]interface{})
			if !ok {
				return fmt.Sprintf("%v", output)
			}

			value := outputMap
			for _, key := range parts[1:] {
				if v, ok := value[key]; ok {
					if nested, ok := v.(map[string]interface{}); ok {
						value = nested
					} else {
						return fmt.Sprintf("%v", v)
					}
				} else {
					return match // Key not found
				}
			}
			jsonBytes, _ := json.Marshal(value)
			return string(jsonBytes)
		}

		return match // Node output not found
	})

	return result
}

// ---------------------------------------------------------------------------
// PassThrough Node Executor (start, human, etc.)
// ---------------------------------------------------------------------------

type PassThroughNodeExecutor struct{}

func (e *PassThroughNodeExecutor) Execute(_ context.Context, node *store.WorkflowNode, _ *ExecutionContext) (interface{}, error) {
	return map[string]interface{}{"type": node.Type, "status": "passed"}, nil
}

// ---------------------------------------------------------------------------
// LLM Node Executor
// ---------------------------------------------------------------------------

type LLMNodeExecutor struct{}

func (e *LLMNodeExecutor) Execute(ctx context.Context, node *store.WorkflowNode, execCtx *ExecutionContext) (interface{}, error) {
	config, ok := node.Data["config"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid node config")
	}

	// Get configuration
	model, _ := config["model"].(string)
	if model == "" {
		model = "claude-3-opus"
	}

	promptTemplate, _ := config["prompt"].(string)
	if promptTemplate == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	// Replace variables in prompt
	prompt := ReplaceVariables(promptTemplate, execCtx)

	// TODO: Integrate with actual LLM API (e.g., Claude via MCP)
	// For now, return a mock response
	return map[string]interface{}{
		"text":   fmt.Sprintf("LLM response for prompt: %s", prompt),
		"model":  model,
		"tokens": 100,
	}, nil
}

// ---------------------------------------------------------------------------
// HTTP Node Executor
// ---------------------------------------------------------------------------

type HTTPNodeExecutor struct{}

func (e *HTTPNodeExecutor) Execute(ctx context.Context, node *store.WorkflowNode, execCtx *ExecutionContext) (interface{}, error) {
	config, ok := node.Data["config"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid node config")
	}

	// Get configuration
	method, _ := config["method"].(string)
	if method == "" {
		method = "GET"
	}

	urlTemplate, _ := config["url"].(string)
	if urlTemplate == "" {
		return nil, fmt.Errorf("url is required")
	}

	bodyTemplate, _ := config["body"].(string)

	// Replace variables
	url := ReplaceVariables(urlTemplate, execCtx)
	body := ReplaceVariables(bodyTemplate, execCtx)

	// Create HTTP request
	var req *http.Request
	var err error

	if method == "GET" || body == "" {
		req, err = http.NewRequestWithContext(ctx, method, url, nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	}

	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	if headers, ok := config["headers"].(map[string]interface{}); ok {
		for k, v := range headers {
			req.Header.Set(k, fmt.Sprintf("%v", v))
		}
	}

	// Execute request
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// Try to parse as JSON, fallback to string
	var jsonResp interface{}
	if err := json.Unmarshal(respBody, &jsonResp); err == nil {
		return map[string]interface{}{
			"status": resp.StatusCode,
			"body":   jsonResp,
		}, nil
	}

	return map[string]interface{}{
		"status": resp.StatusCode,
		"body":   string(respBody),
	}, nil
}

// ---------------------------------------------------------------------------
// Condition Node Executor
// ---------------------------------------------------------------------------

type ConditionNodeExecutor struct{}

func (e *ConditionNodeExecutor) Execute(ctx context.Context, node *store.WorkflowNode, execCtx *ExecutionContext) (interface{}, error) {
	config, ok := node.Data["config"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid node config")
	}

	// Get condition expression
	condition, _ := config["condition"].(string)
	if condition == "" {
		return nil, fmt.Errorf("condition is required")
	}

	// Replace variables
	expression := ReplaceVariables(condition, execCtx)

	// TODO: Implement expression evaluation (e.g., using github.com/Knetic/govaluate)
	// For now, simple string comparison
	result := strings.Contains(expression, "true")

	return map[string]interface{}{
		"result":     result,
		"expression": expression,
	}, nil
}

// ---------------------------------------------------------------------------
// Transform Node Executor
// ---------------------------------------------------------------------------

type TransformNodeExecutor struct{}

func (e *TransformNodeExecutor) Execute(ctx context.Context, node *store.WorkflowNode, execCtx *ExecutionContext) (interface{}, error) {
	config, ok := node.Data["config"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid node config")
	}

	// Get transform script
	scriptTemplate, _ := config["script"].(string)
	if scriptTemplate == "" {
		return nil, fmt.Errorf("script is required")
	}

	// Replace variables
	script := ReplaceVariables(scriptTemplate, execCtx)

	// TODO: Implement data transformation (JSONPath, JQ, etc.)
	// For now, just return the script result as-is
	var result interface{}
	if err := json.Unmarshal([]byte(script), &result); err != nil {
		// If not JSON, return as string
		result = script
	}

	return map[string]interface{}{
		"output": result,
	}, nil
}

// ---------------------------------------------------------------------------
// Map Node Executor — pass-through that collects upstream outputs for distribution
// ---------------------------------------------------------------------------

type MapNodeExecutor struct{}

func (e *MapNodeExecutor) Execute(_ context.Context, node *store.WorkflowNode, execCtx *ExecutionContext) (interface{}, error) {
	execCtx.mu.RLock()
	collected := make(map[string]interface{}, len(execCtx.NodeOutputs))
	for id, out := range execCtx.NodeOutputs {
		collected[id] = out
	}
	execCtx.mu.RUnlock()
	return map[string]interface{}{"type": "map", "upstream": collected}, nil
}

// ---------------------------------------------------------------------------
// Reduce Node Executor — pass-through that merges parallel branch outputs
// ---------------------------------------------------------------------------

type ReduceNodeExecutor struct{}

func (e *ReduceNodeExecutor) Execute(_ context.Context, node *store.WorkflowNode, execCtx *ExecutionContext) (interface{}, error) {
	execCtx.mu.RLock()
	collected := make(map[string]interface{}, len(execCtx.NodeOutputs))
	for id, out := range execCtx.NodeOutputs {
		collected[id] = out
	}
	execCtx.mu.RUnlock()
	return map[string]interface{}{"type": "reduce", "merged": collected}, nil
}

// ---------------------------------------------------------------------------
// Code Node Executor (JavaScript sandbox)
// ---------------------------------------------------------------------------

type CodeNodeExecutor struct{}

func (e *CodeNodeExecutor) Execute(ctx context.Context, node *store.WorkflowNode, execCtx *ExecutionContext) (interface{}, error) {
	config, ok := node.Data["config"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid node config")
	}

	// Get code
	code, _ := config["code"].(string)
	if code == "" {
		return nil, fmt.Errorf("code is required")
	}

	// TODO: Implement sandboxed code execution
	// Options:
	// 1. goja (JavaScript engine for Go)
	// 2. Call external Python interpreter
	// 3. WebAssembly sandbox
	//
	// For now, return error indicating not implemented
	return nil, fmt.Errorf("code execution not yet implemented - requires sandboxing")
}
