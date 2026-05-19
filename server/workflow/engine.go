package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/looplab/fsm"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

// Engine is the workflow execution engine
type Engine struct {
	store     store.WorkflowStore
	executors map[string]NodeExecutor
}

// NewEngine creates a new workflow engine
func NewEngine(s store.WorkflowStore) *Engine {
	e := &Engine{
		store:     s,
		executors: make(map[string]NodeExecutor),
	}

	// Register default node executors
	e.RegisterExecutor("llm", &LLMNodeExecutor{})
	e.RegisterExecutor("http", &HTTPNodeExecutor{})
	e.RegisterExecutor("condition", &ConditionNodeExecutor{})
	e.RegisterExecutor("transform", &TransformNodeExecutor{})
	e.RegisterExecutor("code", &CodeNodeExecutor{})
	e.RegisterExecutor("map", &MapNodeExecutor{})
	e.RegisterExecutor("reduce", &ReduceNodeExecutor{})
	e.RegisterExecutor("start", &PassThroughNodeExecutor{})
	e.RegisterExecutor("ai", &LLMNodeExecutor{})
	e.RegisterExecutor("human", &PassThroughNodeExecutor{})

	return e
}

// RegisterExecutor registers a custom node executor
func (e *Engine) RegisterExecutor(nodeType string, executor NodeExecutor) {
	e.executors[nodeType] = executor
}

// Execute executes a workflow
func (e *Engine) Execute(ctx context.Context, workflowID string, input map[string]interface{}, triggeredBy string) (*store.WorkflowExecution, error) {
	// 1. Load workflow definition
	workflow, err := e.store.GetWorkflow(workflowID)
	if err != nil {
		return nil, fmt.Errorf("load workflow: %w", err)
	}
	if workflow == nil {
		return nil, fmt.Errorf("workflow not found: %s", workflowID)
	}

	var def store.WorkflowDefinition
	if err := json.Unmarshal(workflow.Definition, &def); err != nil {
		return nil, fmt.Errorf("parse definition: %w", err)
	}

	// 2. Validate DAG
	if err := ValidateDAG(&def); err != nil {
		return nil, fmt.Errorf("validate DAG: %w", err)
	}

	// 3. Create execution record
	exec, err := e.store.CreateExecution(store.CreateExecutionRequest{
		WorkflowID:         workflowID,
		WorkflowVersion:    workflow.Version,
		DefinitionSnapshot: string(workflow.Definition),
		TriggeredBy:        triggeredBy,
	})
	if err != nil {
		return nil, fmt.Errorf("create execution: %w", err)
	}

	// 4. Build execution context
	execCtx := &ExecutionContext{
		ExecutionID: exec.ID,
		WorkflowID:  workflowID,
		Input:       input,
		NodeOutputs: make(map[string]interface{}),
		Store:       e.store,
	}

	// 5. Update execution status to running
	startTime := time.Now()
	if err = e.store.UpdateExecutionStatus(exec.ID, "running", "", nil); err != nil {
		return nil, fmt.Errorf("update execution status: %w", err)
	}

	// 6. Execute nodes with parallel support
	executeErr := e.executeParallel(ctx, &def, execCtx)

	// 7. Complete execution
	duration := time.Since(startTime).Milliseconds()

	if executeErr != nil {
		e.store.UpdateExecutionStatus(exec.ID, "failed", executeErr.Error(), &duration)
		exec.Status = "failed"
		exec.Error = executeErr.Error()
		exec.DurationMs = &duration
		completedAt := time.Now()
		exec.CompletedAt = &completedAt
		exec.StartedAt = &startTime
		return exec, executeErr
	}

	e.store.UpdateExecutionStatus(exec.ID, "completed", "", &duration)

	exec.Status = "completed"
	exec.DurationMs = &duration
	exec.StartedAt = &startTime
	completedAt := time.Now()
	exec.CompletedAt = &completedAt

	return exec, nil
}

// executeParallel executes nodes in parallel when possible
func (e *Engine) executeParallel(ctx context.Context, def *store.WorkflowDefinition, execCtx *ExecutionContext) error {
	// Build adjacency list and in-degree map
	graph := make(map[string][]string)
	inDegree := make(map[string]int)
	nodeMap := make(map[string]*store.WorkflowNode)

	// Initialize
	for i := range def.Nodes {
		node := &def.Nodes[i]
		nodeMap[node.ID] = node
		graph[node.ID] = []string{}
		inDegree[node.ID] = 0
	}

	// Build edges
	for _, edge := range def.Edges {
		graph[edge.Source] = append(graph[edge.Source], edge.Target)
		inDegree[edge.Target]++
	}

	// Find nodes ready to execute (in-degree = 0)
	readyNodes := make(chan string, len(def.Nodes))
	for nodeID, degree := range inDegree {
		if degree == 0 {
			readyNodes <- nodeID
		}
	}

	// Track completed nodes
	completed := make(map[string]bool)
	completedChan := make(chan string, len(def.Nodes))
	errorChan := make(chan error, 1)

	// Track active goroutines
	activeCount := 0
	var lastErr error

	for len(completed) < len(def.Nodes) {
		// Drain errors first (non-blocking) to prevent starting downstream
		// nodes when an upstream node has already failed.
		select {
		case err := <-errorChan:
			if lastErr == nil {
				lastErr = err
			}
		default:
		}

		if lastErr != nil && activeCount == 0 {
			return lastErr
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errorChan:
			if lastErr == nil {
				lastErr = err
			}
			if activeCount == 0 {
				return lastErr
			}
		case nodeID := <-readyNodes:
			if lastErr != nil {
				completed[nodeID] = true
				continue
			}

			activeCount++
			go func(nid string) {
				node := nodeMap[nid]
				if err := e.executeNode(ctx, node, execCtx); err != nil {
					select {
					case errorChan <- fmt.Errorf("node %s failed: %w", nid, err):
					default:
					}
				}
				completedChan <- nid
			}(nodeID)

		case nodeID := <-completedChan:
			activeCount--
			completed[nodeID] = true

			if lastErr != nil && activeCount == 0 {
				return lastErr
			}

			for _, neighbor := range graph[nodeID] {
				inDegree[neighbor]--
				if inDegree[neighbor] == 0 {
					readyNodes <- neighbor
				}
			}
		}
	}

	return lastErr
}

// executeNode executes a single node
func (e *Engine) executeNode(ctx context.Context, node *store.WorkflowNode, execCtx *ExecutionContext) error {
	// Get executor for node type
	executor, ok := e.executors[node.Type]
	if !ok {
		return fmt.Errorf("no executor registered for node type: %s", node.Type)
	}

	// Create node log
	logID := uuid.New().String()
	nodeLog := &store.WorkflowNodeLog{
		ID:          logID,
		ExecutionID: execCtx.ExecutionID,
		NodeID:      node.ID,
		NodeType:    node.Type,
		Status:      "running",
		CreatedAt:   time.Now(),
	}

	now := time.Now()
	nodeLog.StartedAt = &now

	if err := e.store.CreateNodeLog(nodeLog); err != nil {
		return fmt.Errorf("create node log: %w", err)
	}

	// Execute node
	startTime := time.Now()
	output, err := executor.Execute(ctx, node, execCtx)
	duration := time.Since(startTime).Milliseconds()

	if err != nil {
		e.store.UpdateNodeLog(logID, "failed", "", "", err.Error(), duration)
		return err
	}

	// Save node output (thread-safe)
	execCtx.SetNodeOutput(node.ID, output)
	outputJSON, _ := json.Marshal(output)
	e.store.UpdateNodeLog(logID, "completed", "", string(outputJSON), "", duration)

	return nil
}

// ExecuteWithFSM executes a workflow using FSM (alternative implementation)
// This is a more complex version using looplab/fsm for explicit state management
func (e *Engine) ExecuteWithFSM(ctx context.Context, workflowID string, input map[string]interface{}, triggeredBy string) (*store.WorkflowExecution, error) {
	// Load workflow
	workflow, err := e.store.GetWorkflow(workflowID)
	if err != nil {
		return nil, fmt.Errorf("load workflow: %w", err)
	}

	var def store.WorkflowDefinition
	if err := json.Unmarshal(workflow.Definition, &def); err != nil {
		return nil, fmt.Errorf("parse definition: %w", err)
	}

	// Validate and sort
	if err := ValidateDAG(&def); err != nil {
		return nil, fmt.Errorf("validate DAG: %w", err)
	}

	nodeOrder, err := TopologicalSort(&def)
	if err != nil {
		return nil, fmt.Errorf("topological sort: %w", err)
	}

	// Create execution
	exec, err := e.store.CreateExecution(store.CreateExecutionRequest{
		WorkflowID:         workflowID,
		WorkflowVersion:    workflow.Version,
		DefinitionSnapshot: string(workflow.Definition),
		TriggeredBy:        triggeredBy,
	})
	if err != nil {
		return nil, fmt.Errorf("create execution: %w", err)
	}

	// Build execution context
	execCtx := &ExecutionContext{
		ExecutionID: exec.ID,
		WorkflowID:  workflowID,
		Input:       input,
		NodeOutputs: make(map[string]interface{}),
		Store:       e.store,
	}

	// Build FSM
	machine := e.buildFSM(&def, nodeOrder, execCtx)

	// Start execution
	startTime := time.Now()
	e.store.UpdateExecutionStatus(exec.ID, "running", "", nil)

	// Run FSM: fire "start" then each node's "execute_" event in order
	if err := machine.Event(ctx, "start"); err != nil {
		duration := time.Since(startTime).Milliseconds()
		e.store.UpdateExecutionStatus(exec.ID, "failed", err.Error(), &duration)
		return exec, err
	}
	for _, nodeID := range nodeOrder {
		if err := machine.Event(ctx, "execute_"+nodeID); err != nil {
			duration := time.Since(startTime).Milliseconds()
			e.store.UpdateExecutionStatus(exec.ID, "failed", err.Error(), &duration)
			return exec, err
		}
	}

	// Complete
	duration := time.Since(startTime).Milliseconds()
	e.store.UpdateExecutionStatus(exec.ID, "completed", "", &duration)

	exec.Status = "completed"
	exec.DurationMs = &duration

	return exec, nil
}

// buildFSM builds a finite state machine for workflow execution
func (e *Engine) buildFSM(def *store.WorkflowDefinition, nodeOrder []string, execCtx *ExecutionContext) *fsm.FSM {
	// Build node map
	nodeMap := make(map[string]*store.WorkflowNode)
	for i := range def.Nodes {
		nodeMap[def.Nodes[i].ID] = &def.Nodes[i]
	}

	// Define FSM events
	events := []fsm.EventDesc{}

	// Add start event
	if len(nodeOrder) > 0 {
		events = append(events, fsm.EventDesc{
			Name: "start",
			Src:  []string{"idle"},
			Dst:  "node_" + nodeOrder[0],
		})
	}

	// Add node transition events
	for i := 0; i < len(nodeOrder); i++ {
		currentNode := nodeOrder[i]
		nextState := "done"
		if i < len(nodeOrder)-1 {
			nextState = "node_" + nodeOrder[i+1]
		}

		events = append(events, fsm.EventDesc{
			Name: "execute_" + currentNode,
			Src:  []string{"node_" + currentNode},
			Dst:  nextState,
		})
	}

	// Define callbacks
	callbacks := make(map[string]fsm.Callback)

	// Add callbacks for each node
	for _, nodeID := range nodeOrder {
		node := nodeMap[nodeID]

		// Callback to execute node
		eventName := "execute_" + nodeID
		callbacks["before_"+eventName] = e.createNodeCallback(node, execCtx)
	}

	// Create FSM
	machine := fsm.NewFSM(
		"idle",
		events,
		callbacks,
	)

	return machine
}

// createNodeCallback creates a callback function for a node
func (e *Engine) createNodeCallback(node *store.WorkflowNode, execCtx *ExecutionContext) fsm.Callback {
	return func(ctx context.Context, event *fsm.Event) {
		if err := e.executeNode(ctx, node, execCtx); err != nil {
			event.Cancel(err)
		}
	}
}
