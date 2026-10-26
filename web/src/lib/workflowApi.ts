const API_BASE = "/api";

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    headers: {
      "Content-Type": "application/json",
      ...options?.headers,
    },
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }));
    throw new Error(err.error || res.statusText);
  }
  if (res.status === 204) return undefined as T;
  return res.json();
}


// Workflow types
export interface WorkflowNode {
  id: string;
  type: string; // 'llm' | 'http' | 'condition' | 'transform' | 'code'
  position: { x: number; y: number };
  data: {
    label: string;
    config?: Record<string, any>;
  };
}

export interface WorkflowEdge {
  id: string;
  source: string;
  target: string;
  label?: string;
}

// HookConfig configures the post-reply hook for an AI node.
// trigger: currently only "on_reply_done"
// prompt: what the hook agent should check
// reset: if true, clear the underlying agent session before re-sending (no --resume)
export interface HookConfig {
  trigger: 'on_reply_done';
  prompt: string;
  reset?: boolean;
}

export interface WorkflowDefinition {
  nodes: WorkflowNode[];
  edges: WorkflowEdge[];
  max_turns?: number; // workflow-level turn budget; default 100
}

// WorkflowSchedule defines automated execution timing.
// type "interval": run every interval_hours (1–24) after last completion.
// type "weekly": run on specified days (0=Sun,1=Mon…6=Sat) at hour:minute.
export interface WorkflowSchedule {
  type: 'interval' | 'weekly';
  interval_hours?: number;
  days?: number[];
  hour?: number;
  minute?: number;
}

export interface Workflow {
  id: string;
  project_id: string;
  namespace_id: string;
  name: string;
  description: string;
  definition: WorkflowDefinition;
  version: number;
  status: 'draft' | 'active' | 'archived';
  created_by: string;
  created_at: string;
  updated_at: string;
  schedule?: string;    // JSON-encoded WorkflowSchedule, empty = no schedule
  // worktree_mode controls per-run isolation. "local" (default) runs in the
  // project's repo. "worktree" creates a fresh git worktree per run; all
  // sessions in that run share it, and parallel runs of the same workflow
  // are allowed (each gets its own worktree/branch).
  worktree_mode?: 'local' | 'worktree';
  is_running?: boolean; // computed by server, not stored in DB
}

export interface WorkflowExecution {
  id: string;
  workflow_id: string;
  workflow_version: number;
  definition_snapshot?: string; // JSON snapshot of workflow definition at execution time
  user_context?: string;
  status: 'pending' | 'running' | 'completed' | 'failed' | 'cancelled';
  output?: string;
  error?: string;
  started_at?: string;
  completed_at?: string;
  duration_ms?: number;
  triggered_by: string;
  created_at: string;
  worktree_path?: string; // populated when the workflow's worktree_mode is "worktree"
}

export interface WorkflowNodeLog {
  id: string;
  execution_id: string;
  node_id: string;
  node_type: string;
  node_label?: string;
  level?: number;
  status: 'pending' | 'running' | 'completed' | 'failed' | 'skipped' | 'waiting';
  input?: string;
  output?: string;
  error?: string;
  hook_retries?: number;
  hook_result?: 'ok' | 'ok_after_retries' | 'agent_error' | 'parse_error';
  started_at?: string;
  completed_at?: string;
  duration_ms?: number;
  created_at: string;
}

// API functions
export const workflowApi = {
  // Create a new workflow
  create: (projectId: string, name: string, description: string, definition: WorkflowDefinition): Promise<Workflow> =>
    request(`/workflows?project_id=${projectId}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, description, definition }),
    }),

  // List workflows for a project
  list: (projectId: string): Promise<Workflow[]> =>
    request(`/workflows?project_id=${projectId}`),

  // Get a single workflow
  get: (id: string): Promise<Workflow> =>
    request(`/workflows/${id}`),

  // Update a workflow
  update: (id: string, data: Partial<{ name: string; description: string; definition: WorkflowDefinition; status: string; schedule: string; worktree_mode: 'local' | 'worktree' }>): Promise<Workflow> =>
    request(`/workflows/${id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(data),
    }),

  // Delete a workflow
  delete: (id: string): Promise<void> =>
    request(`/workflows/${id}`, { method: 'DELETE' }),

  // Execute a workflow (async — returns immediately with execution record).
  // `env` is a map of user-supplied variables referenced as {{env_<name>}} in
  // node prompts. Reserved name `prev_node_output` is auto-populated and
  // cannot be shadowed.
  execute: (id: string, opts?: { env?: Record<string, string> }): Promise<WorkflowExecution> =>
    request(`/workflows/${id}/execute`, {
      method: 'POST',
      body: JSON.stringify(opts || {}),
    }),

  // Cancel a running execution
  cancelExecution: (executionId: string): Promise<void> =>
    request(`/workflow_executions/${executionId}/cancel`, { method: 'POST' }),

  // List executions for a workflow
  listExecutions: (workflowId: string, limit = 20, offset = 0): Promise<WorkflowExecution[]> =>
    request(`/workflows/${workflowId}/executions?limit=${limit}&offset=${offset}`),

  // Get execution details
  getExecution: (executionId: string): Promise<WorkflowExecution> =>
    request(`/workflow_executions/${executionId}`),

  // Get execution logs
  getExecutionLogs: (executionId: string): Promise<WorkflowNodeLog[]> =>
    request(`/workflow_executions/${executionId}/logs`),

  // Continue a human node that is waiting at a checkpoint
  continueNode: (executionId: string, nodeId: string, input?: string): Promise<void> =>
    request(`/workflow_executions/${executionId}/nodes/${nodeId}/continue`, {
      method: 'POST',
      body: JSON.stringify({ input: input || '' }),
    }),
};

// ---------------------------------------------------------------------------
// RALPH (parallel task pool)
// ---------------------------------------------------------------------------

export interface RalphRun {
  id: string;
  project_id: string;
  workflow_id: string;
  workers: number;
  status: 'running' | 'completed' | 'stopped';
  total_tasks: number;
  total_completed: number;
  total_failed: number;
  created_at: string;
  completed_at?: string;
}

export interface RalphWorkerLog {
  id: string;
  worker_index: number;
  task_id: string;
  task_title: string;
  session_id: string;
  status: 'running' | 'completed' | 'failed';
  started_at: string;
  completed_at?: string;
}

export const ralphApi = {
  start: (projectId: string, workflowId: string, workers: number): Promise<{ id: string; status: string; workers: number; total_tasks: number }> =>
    request('/ralph/start', {
      method: 'POST',
      body: JSON.stringify({ project_id: projectId, workflow_id: workflowId, workers }),
    }),

  stop: (id: string): Promise<void> =>
    request(`/ralph/runs/${id}/stop`, { method: 'POST' }),

  listRuns: (projectId?: string): Promise<RalphRun[]> =>
    request(`/ralph/runs${projectId ? `?project_id=${projectId}` : ''}`),

  getRun: (id: string): Promise<{ id: string; status: string; workers?: any[]; worker_logs?: RalphWorkerLog[]; total_tasks?: number; total_completed?: number; total_failed?: number }> =>
    request(`/ralph/runs/${id}`),
};
