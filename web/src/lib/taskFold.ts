// Reconstructs a live task/todo list from a stream of tool calls.
//
// Newer Claude Code replaced the single `TodoWrite` tool (one call carried the
// entire `todos` array) with granular per-task tools: `TaskCreate`,
// `TaskUpdate`, `TaskList`, `TaskGet`. The full list state is therefore spread
// across many calls and must be folded back together here.
//
// Crucial detail learned empirically (see server/agent/claude_agent.go): the
// frontend receives tool *inputs* as JSON but tool *outputs* as PLAIN TEXT, e.g.
//   TaskCreate output:  "Task #1 created successfully: <subject>"   (id only here)
//   TaskUpdate input:   {"taskId":"1","status":"in_progress"}
//   TaskUpdate output:  "Updated task #1 status"
//   TaskList   output:  "#1 [in_progress] <subject> [blocked by #2, #3]"  (one line/task)
// So TaskList output is the authoritative snapshot; create/update events keep the
// list live in between TaskList calls. Legacy TodoWrite-style JSON is also folded.

export type TaskStatus = "pending" | "in_progress" | "completed" | "cancelled" | "deleted";

export interface FoldedTask {
  id: string;
  content: string; // the task subject
  status: TaskStatus;
  blockedBy: string[]; // dependency task ids (filtered to still-open deps in the result)
  activeForm?: string; // present-continuous label, only present on TaskCreate/TaskUpdate input
}

export interface TaskCall {
  toolName: string;
  input: string; // raw JSON string of the tool input
  output?: string; // raw text output of the tool result, if it has completed
}

const CREATE_TOOLS = new Set(["TaskCreate"]);
const UPDATE_TOOLS = new Set(["TaskUpdate"]);
const LIST_TOOLS = new Set(["TaskList"]);
// Tools whose payload is a whole-list snapshot (older Claude / Cursor / Codex).
const LEGACY_SNAPSHOT_TOOLS = new Set([
  "TodoWrite", "UpdateTodos", "ReadTodos",
  "updateTodosToolCall", "readTodosToolCall", "todoWriteToolCall",
  "todo_list",
]);

const CREATE_RE = /Task #(\S+) created successfully:\s*(.*)$/;
const LIST_LINE_RE = /^#(\S+)\s+\[([^\]]+)\]\s+(.*)$/;
const BLOCKED_SUFFIX_RE = /\s+\[blocked by ([^\]]+)\]\s*$/;

function safeParse(s: string | undefined): any {
  if (!s) return null;
  try { return JSON.parse(s); } catch { return null; }
}

function uniq(a: string[]): string[] {
  return Array.from(new Set(a));
}

function normStatus(s: unknown): TaskStatus {
  switch (String(s ?? "").toLowerCase()) {
    case "in_progress":
    case "todo_status_in_progress":
      return "in_progress";
    case "completed":
    case "done":
    case "todo_status_completed":
      return "completed";
    case "cancelled":
    case "canceled":
    case "todo_status_cancelled":
      return "cancelled";
    case "deleted":
      return "deleted";
    default:
      return "pending";
  }
}

function parseCreatedTask(output: string): { id: string; content: string } | null {
  const m = CREATE_RE.exec(output.trim());
  if (!m) return null;
  return { id: m[1].replace(/^#/, ""), content: m[2].trim() };
}

/** Parse the authoritative `TaskList` text snapshot into rows. */
export function parseTaskListOutput(output: string): FoldedTask[] {
  const rows: FoldedTask[] = [];
  for (const raw of output.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const m = LIST_LINE_RE.exec(line);
    if (!m) continue;
    const id = m[1].replace(/^#/, "");
    const status = normStatus(m[2].trim());
    let content = m[3];
    let blockedBy: string[] = [];
    const dep = BLOCKED_SUFFIX_RE.exec(content);
    if (dep) {
      content = content.replace(BLOCKED_SUFFIX_RE, "");
      blockedBy = dep[1].split(",").map((d) => d.trim().replace(/^#/, "")).filter(Boolean);
    }
    rows.push({ id, content: content.trim(), status, blockedBy });
  }
  return rows;
}

function parseLegacyTodos(call: TaskCall): { id: string; content: string; status: string }[] {
  let arr: any[] | null = null;
  const out = safeParse(call.output);
  if (out) arr = out?.success?.todos ?? out?.todos ?? null;
  if (!Array.isArray(arr)) {
    const inp = safeParse(call.input);
    arr = Array.isArray(inp?.todos) ? inp.todos : null;
  }
  if (!Array.isArray(arr)) return [];
  return arr.map((t, i) => ({
    id: String(t?.id ?? i),
    content: String(t?.content ?? t?.subject ?? ""),
    status: String(t?.status ?? "pending"),
  }));
}

/**
 * Fold an ordered list of task tool calls into the current list state.
 * Insertion order is preserved (creation order, matching the #N ids).
 */
export function foldTasks(calls: TaskCall[]): FoldedTask[] {
  const map = new Map<string, FoldedTask>();
  const ensure = (id: string): FoldedTask => {
    let t = map.get(id);
    if (!t) {
      t = { id, content: "", status: "pending", blockedBy: [] };
      map.set(id, t);
    }
    return t;
  };

  for (const call of calls) {
    const { toolName } = call;

    if (LIST_TOOLS.has(toolName)) {
      if (!call.output) continue;
      for (const row of parseTaskListOutput(call.output)) {
        const t = ensure(row.id);
        t.content = row.content;
        t.status = row.status;
        t.blockedBy = row.blockedBy; // authoritative: already only open deps
      }
      continue;
    }

    if (CREATE_TOOLS.has(toolName)) {
      const created = call.output ? parseCreatedTask(call.output) : null;
      if (!created) continue; // the id is only present in the output text
      const inp = safeParse(call.input);
      const t = ensure(created.id);
      t.content = (typeof inp?.subject === "string" && inp.subject) || created.content || t.content;
      if (typeof inp?.activeForm === "string") t.activeForm = inp.activeForm;
      continue;
    }

    if (UPDATE_TOOLS.has(toolName)) {
      const inp = safeParse(call.input);
      const id = inp?.taskId != null ? String(inp.taskId) : undefined;
      if (!id) continue;
      const t = ensure(id);
      if (typeof inp.status === "string") t.status = normStatus(inp.status);
      if (typeof inp.subject === "string") t.content = inp.subject;
      if (typeof inp.activeForm === "string") t.activeForm = inp.activeForm;
      if (Array.isArray(inp.addBlockedBy)) {
        t.blockedBy = uniq([...t.blockedBy, ...inp.addBlockedBy.map(String)]);
      }
      if (Array.isArray(inp.addBlocks)) {
        for (const other of inp.addBlocks.map(String)) {
          const o = ensure(other);
          o.blockedBy = uniq([...o.blockedBy, id]);
        }
      }
      continue;
    }

    if (LEGACY_SNAPSHOT_TOOLS.has(toolName)) {
      for (const td of parseLegacyTodos(call)) {
        const t = ensure(td.id);
        t.content = td.content;
        t.status = normStatus(td.status);
      }
      continue;
    }
  }

  const isOpen = (id: string) => {
    const s = map.get(id)?.status;
    return s !== "completed" && s !== "cancelled" && s !== "deleted";
  };

  const out: FoldedTask[] = [];
  for (const t of map.values()) {
    if (t.status === "deleted") continue;
    // Drop deps that have since finished so "blocked by" only shows live blockers.
    out.push({ ...t, blockedBy: t.blockedBy.filter((d) => (map.has(d) ? isOpen(d) : true)) });
  }
  return out;
}

/** Label for the panel header: the in-progress task's activeForm (or its subject). */
export function activeTaskLabel(tasks: FoldedTask[]): string | undefined {
  const active = tasks.find((t) => t.status === "in_progress");
  if (!active) return undefined;
  return active.activeForm || active.content || undefined;
}

/** True once every (non-deleted) task is completed or cancelled. */
export function allTasksDone(tasks: FoldedTask[]): boolean {
  return tasks.length > 0 && tasks.every((t) => t.status === "completed" || t.status === "cancelled");
}
