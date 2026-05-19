export interface Project {
  id: string;
  namespace_id?: string;
  name: string;
  description: string;
  git_repo_path: string;
  owner_user_id?: number;
  created_by?: string;
  created_at: string;
  updated_at: string;
}

export interface User {
  user_id: number;
  username: string;
}

export interface Task {
  id: string;
  project_id?: string;
  parent_id: string;
  title: string;
  description: string;
  status: "open" | "closed";
  priority: "low" | "medium" | "high" | "critical";
  assigner?: string;
  comment_count: number;
  created_by?: string;
  created_at: string;
  updated_at: string;
  closed_at?: string;
  labels?: string[];
  source?: string;
  external_id?: string;
  external_url?: string;
  external_type?: string;
  children?: Task[];
  child_count?: number;
  due_date?: string;
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

export interface Session {
  id: string;
  name: string;
  project_id: string;
  agent: string; // claude | codex | cursor (empty = server default)
  model: string;
  // Reasoning effort for the session. Valid values come from the active
  // model's ModelInfo.supported_efforts (Claude: low/medium/high/xhigh/max;
  // Codex: none/minimal/low/medium/high/xhigh). Empty = backend default.
  effort: string;
  agent_session_id: string;
  stream_id: string;
  sort_order: number;
  worktree_mode: "local" | "worktree";
  worktree_path: string; // empty until created on first message
  // Fork lineage. Set when this session was created via POST /sessions/:id/fork.
  forked_from_session_id?: string;
  forked_from_message_id?: string;
  forked_at_agent_msg_uuid?: string;
  // Hidden scratch session: per-project staging surface produced by
  // GET /api/projects/:id/scratch-session. Filtered out of the visible
  // session list and count, and promoted (is_hidden -> false) by the
  // backend as soon as the user sends the first message.
  is_hidden?: boolean;
  created_at: string;
  updated_at: string;
  deleted_at?: string;
}

export type AssetType =
  | "requirement_doc"
  | "design_doc"
  | "code_change"
  | "unit_test"
  | "test_case"
  | "lark_doc";

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
  lark_doc_url?: string;
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
  cache_read_input_tokens?: number;
  cache_creation_input_tokens?: number;
  total_cost_usd?: number;
}

export interface ContextUsage {
  model?: string;
  used_tokens: number;
  input_tokens?: number;
  output_tokens?: number;
  cached_input_tokens?: number;
  reasoning_output_tokens?: number;
  context_window?: number;
  max_output_tokens?: number;
}

export interface SessionInfo {
  model?: string;
  permission_mode?: string;
  cwd?: string;
  version?: string;
  output_style?: string;
}

export interface ModelInfo {
  id: string;
  label: string;
  description?: string;
  supported_efforts?: string[]; // empty / missing = model doesn't support effort
  default_effort?: string;
  context_window?: number;
  is_default?: boolean;
  hidden?: boolean;
  capabilities?: Record<string, boolean>;
}

export interface ChatEvent {
  type:
    | "text_delta"
    | "tool_start"
    | "tool_input_delta"
    | "tool_result"
    | "thinking_delta"
    | "subtask_start"
    | "subtask_progress"
    | "subtask_done"
    | "done"
    | "error"
    | "system"
    | "queue_updated"
    | "user_message"
    | "user_input_request"
    | "widget"
    | "context_usage"
    | "session_info";
  session_id?: string;
  content?: string;
  tool?: string;
  tool_id?: string;
  task_id?: string;
  // On tool_start / tool_result events from a sub-agent, the tool_use_id
  // of the parent Task tool. Lets the UI nest sub-agent calls under the
  // right Agent card even when several sub-agents run in parallel.
  parent_tool_id?: string;
  input?: string;
  output?: string;
  is_error?: boolean;
  message?: string;
  // Widget (visualize_show_widget)
  widget_title?: string;
  widget_code?: string;
  loading_messages?: string[];
  stats?: ExecutionStats;
  context_usage?: ContextUsage;
  session_info?: SessionInfo;
  // CLI-side message uuid for the just-finished assistant turn (sent on
  // "done"). Used to stamp the in-memory ChatMessage so fork-button
  // visibility doesn't have to wait for a page refresh.
  sdk_message_uuid?: string;
}

// --- Structured message parts ---

export type ToolUseState = "streaming" | "running" | "complete" | "error";

export interface NestedTool {
  toolCallId: string;
  toolName: string;
  input: string;
  state: ToolUseState;
  result?: { output: string; isError?: boolean };
}

export type MessagePart =
  | { type: "text"; content: string }
  | { type: "tool_use"; toolCallId: string; toolName: string; input: string; summary?: string; state: ToolUseState; nestedTools?: NestedTool[]; parentToolCallId?: string }
  | { type: "tool_result"; toolCallId: string; output: string; isError?: boolean }
  | { type: "thinking"; content?: string }
  | { type: "tool_group"; count: number }
  | { type: "task_ref"; taskId: string; task: Task }
  | { type: "asset_ref"; assetId: string; title: string; assetType: AssetType }
  | { type: "workflow_ref"; workflowId: string; name: string; nodeCount?: number; edgeCount?: number }
  | { type: "image"; url: string; filename: string }
  | { type: "widget"; title: string; widgetCode: string; loadingMessages?: string[] };

export interface MessageMetadata {
  sessionId?: string;
  sdkMessageUuid?: string;
  inputTokens?: number;
  outputTokens?: number;
  cacheReadInputTokens?: number;
  cacheCreationInputTokens?: number;
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
