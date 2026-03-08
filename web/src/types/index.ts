export interface Project {
  id: string;
  namespace_id?: string;
  name: string;
  description: string;
  git_repo_path: string;
  gitlab_repo_url?: string;
  created_by?: string;
  created_at: string;
  updated_at: string;
}

export interface Task {
  id: string;
  project_id?: string;
  parent_id: string;
  title: string;
  description: string;
  status: "open" | "in_progress" | "done" | "closed";
  priority: "low" | "medium" | "high" | "critical";
  assigner?: string;
  ref_count: number;
  comment_count: number;
  created_by?: string;
  created_at: string;
  updated_at: string;
  closed_at?: string;
  labels?: string[];
}

export interface TaskLabel {
  id: string;
  name: string;
  color: string;
  description: string;
  created_at: string;
}

export interface TaskComment {
  id: string;
  task_id: string;
  author: string;
  body: string;
  created_at: string;
  updated_at: string;
}

export interface ReactionSummary {
  emoji: string;
  count: number;
  user_reacted: boolean;
}

export interface Session {
  id: string;
  name: string;
  project_id: string;
  agent: string; // claude | codex | cursor (empty = server default)
  agent_session_id: string;
  stream_id: string;
  sort_order: number;
  worktree_mode: "local" | "worktree";
  worktree_path: string; // empty until created on first message
  created_at: string;
  updated_at: string;
  deleted_at?: string;
}

export interface TaskAsset {
  id: string;
  task_id: string;
  asset_id: string;
  created_at: string;
}

export type AssetType =
  | "requirement_doc"
  | "design_doc"
  | "code_change"
  | "unit_test"
  | "test_case";

export type AssetStatus = "draft" | "reviewing" | "approved";

export interface Asset {
  id: string;
  project_id: string;
  type: AssetType;
  title: string;
  current_version: number;
  status: AssetStatus;
  created_by?: string;
  created_at: string;
  updated_at: string;
  content?: string;
}

export interface AssetVersion {
  id: string;
  asset_id: string;
  version: number;
  content: string;
  created_at: string;
}

export interface ReviewComment {
  id: string;
  asset_id: string;
  asset_version: number;
  line_number: number | null;
  content: string;
  author: string;
  status: "open" | "resolved";
  created_at: string;
}

export interface DiffLine {
  type: "equal" | "insert" | "delete";
  content: string;
}

export interface DiffResult {
  asset_id: string;
  version1: number;
  version2: number;
  diffs: DiffLine[];
  old_text: string;
  new_text: string;
}

// --- WebSocket event types ---

export interface ExecutionStats {
  duration_ms: number;
  tool_calls: number;
  input_tokens?: number;
  output_tokens?: number;
  total_cost_usd?: number;
}

export interface ChatEvent {
  type:
    | "text_delta"
    | "tool_start"
    | "tool_input_delta"
    | "tool_result"
    | "thinking_delta"
    | "done"
    | "error"
    | "system"
    | "queue_updated"
    | "user_message";
  session_id?: string;
  content?: string;
  tool?: string;
  tool_id?: string;
  input?: string;
  output?: string;
  is_error?: boolean;
  message?: string;
  stats?: ExecutionStats;
}

// --- Structured message parts ---

export type ToolUseState = "streaming" | "running" | "complete" | "error";

export type MessagePart =
  | { type: "text"; content: string }
  | { type: "tool_use"; toolCallId: string; toolName: string; input: string; summary?: string; state: ToolUseState }
  | { type: "tool_result"; toolCallId: string; output: string; isError?: boolean }
  | { type: "thinking"; content?: string }
  | { type: "tool_group"; count: number }
  | { type: "task_ref"; taskId: string; task: Task }
  | { type: "asset_ref"; assetId: string; title: string; assetType: AssetType }
  | { type: "workflow_ref"; workflowId: string; name: string; nodeCount?: number; edgeCount?: number }
  | { type: "image"; url: string; filename: string };

export interface MessageMetadata {
  sessionId?: string;
  sdkMessageUuid?: string;
  inputTokens?: number;
  outputTokens?: number;
  totalCostUsd?: number;
  durationMs?: number;
  toolCalls?: number;
}

export interface ChatMessage {
  id: string;
  session_id: string;
  role: "user" | "assistant" | "system";
  parts: MessagePart[];
  metadata: MessageMetadata;
  created_at: string;
  // Local UI states (not persisted)
  isStreaming?: boolean;
}
