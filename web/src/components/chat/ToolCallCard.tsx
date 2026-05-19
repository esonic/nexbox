import { memo, useState, useEffect, useRef, type ReactNode } from "react";
import {
  FileText,
  FilePen,
  Pencil,
  Terminal,
  Search,
  FolderSearch,
  FolderOpen,
  Globe,
  Sparkles,
  ChevronRight,
  ChevronDown,
  XCircle,
  Loader2,
  CircleHelp,
  Trash2,
  ListChecks,
  BookOpen,
  ToggleRight,
  Bug,
  Circle,
  CircleDot,
  CheckCircle2,
  CircleDashed,
  Ban,
  Save,
  List,
  MessageSquareMore,
  NotebookPen,
  GitPullRequestArrow,
  SquareTerminal,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from "@/components/ui/collapsible";
import type { ToolUseState, NestedTool } from "@/types";

// ── Helpers ──

const COMMAND_PREVIEW_MAX = 90;

function asText(value: unknown): string {
  if (typeof value === "string") return value;
  if (value == null) return "";
  return String(value);
}

function parseJSON(s: string | undefined): Record<string, unknown> {
  if (!s) return {};
  try { return JSON.parse(s); } catch { return {}; }
}

function baseName(p: string): string {
  return p ? (p.split(/[/\\]/).pop() || p) : "";
}

function truncate(s: string | undefined, max: number): string {
  const text = asText(s);
  return text.length > max ? text.slice(0, max - 3) + "..." : text;
}

function isShellProgram(program: string): boolean {
  const base = program.split(/[/\\]/).pop() || program;
  return ["bash", "sh", "zsh", "dash", "fish"].includes(base);
}

function unquoteShellWord(value: string): string {
  const trimmed = value.trim();
  if (trimmed.length < 2) return trimmed;

  if (trimmed.startsWith("'") && trimmed.endsWith("'")) {
    return trimmed.slice(1, -1).replace(/'"'"'/g, "'");
  }

  if (trimmed.startsWith('"') && trimmed.endsWith('"')) {
    return trimmed.slice(1, -1).replace(/\\(["\\$`])/g, "$1");
  }

  return trimmed;
}

function unwrapShellCommand(raw: string): string {
  const command = raw.trim();
  if (!command) return command;

  try {
    const parsed = JSON.parse(command);
    if (Array.isArray(parsed) && parsed.every((part) => typeof part === "string")) {
      if (parsed.length >= 3 && isShellProgram(parsed[0]) && (parsed[1] === "-lc" || parsed[1] === "-c")) {
        return (parsed[2] as string).trim();
      }
      if (parsed.length >= 4 && parsed[0] === "/usr/bin/env" && isShellProgram(parsed[1]) && (parsed[2] === "-lc" || parsed[2] === "-c")) {
        return (parsed[3] as string).trim();
      }
      return parsed.join(" ").trim();
    }
  } catch {
    // Not a JSON argv array; fall through to the rendered command form.
  }

  const shellMatch = command.match(/^(?:\/[^\s'"]+\/)?(?:bash|sh|zsh|dash|fish)\s+-(?:lc|c)\s+([\s\S]+)$/);
  if (shellMatch) {
    return unquoteShellWord(shellMatch[1]);
  }

  const envShellMatch = command.match(/^\/usr\/bin\/env\s+(?:\/[^\s'"]+\/)?(?:bash|sh|zsh|dash|fish)\s+-(?:lc|c)\s+([\s\S]+)$/);
  if (envShellMatch) {
    return unquoteShellWord(envShellMatch[1]);
  }

  return command;
}

function commandFromInput(input: string | undefined): string {
  const parsed = parseJSON(input);
  const raw = ((parsed.command as string) || (parsed.cmd as string) || (parsed.input as string) || (Object.keys(parsed).length === 0 ? asText(input) : ""))
    .replace(/\\\s*\n\s*/g, " ")
    .trim();
  return unwrapShellCommand(raw);
}

function pathField(field: string) {
  return (input: string) => baseName((parseJSON(input)[field] as string) || "");
}
function textField(field: string, max = 50) {
  return (input: string) => truncate((parseJSON(input)[field] as string) || "", max);
}
function tense(done: string, active: string) {
  return (s: ToolUseState) => (s === "complete" ? done : active);
}

// ── Reusable render blocks ──

function CommandBlock({ input }: { input: string }) {
  const cmd = commandFromInput(input) || input;
  return (
    <pre className="text-[11px] font-mono bg-neutral-900 text-neutral-200 dark:bg-neutral-950 rounded p-2 overflow-x-auto max-h-[120px] overflow-y-auto whitespace-pre-wrap break-all">
      <span className="text-neutral-500 select-none">$ </span>{cmd}
    </pre>
  );
}

function TerminalOutput({ output, isError }: { output?: string; isError?: boolean }) {
  const safeOutput = asText(output);
  const text = safeOutput.length > 2000 ? safeOutput.slice(0, 2000) + "\n... (truncated)" : safeOutput;
  return (
    <pre className={cn(
      "text-[11px] font-mono rounded p-2 overflow-x-auto max-h-[200px] overflow-y-auto whitespace-pre-wrap break-all",
      isError
        ? "bg-red-950/50 text-red-300 dark:bg-red-950/30"
        : "bg-neutral-900 text-neutral-300 dark:bg-neutral-950"
    )}>
      {text || (isError ? "(command failed)" : "(empty)")}
    </pre>
  );
}

function FilePathBlock({ input }: { input: string }) {
  const parsed = parseJSON(input);
  const path = (parsed.path as string) || (parsed.file_path as string) || "";
  if (!path) return <RawJSON input={input} />;
  const extra: string[] = [];
  if (parsed.offset != null) extra.push(`offset: ${parsed.offset}`);
  if (parsed.limit != null) extra.push(`limit: ${parsed.limit}`);
  return (
    <div className="text-[11px] font-mono text-muted-foreground bg-muted/30 rounded px-2 py-1 overflow-x-auto">
      <span className="break-all">{path}</span>
      {extra.length > 0 && <span className="ml-2 opacity-50">({extra.join(", ")})</span>}
    </div>
  );
}

function SearchBlock({ input, fields }: { input: string; fields: string[] }) {
  const parsed = parseJSON(input);
  let query = "";
  for (const f of fields) {
    const v = parsed[f] as string;
    if (v) { query = v; break; }
  }
  if (!query) return <RawJSON input={input} />;
  const extra: string[] = [];
  const path = (parsed.path as string) || (parsed.target_directory as string) || "";
  if (path) extra.push(path);
  const glob = parsed.glob as string;
  if (glob) extra.push(`glob: ${glob}`);
  return (
    <div className="text-[11px] font-mono text-muted-foreground bg-muted/30 rounded px-2 py-1 overflow-x-auto">
      <span className="text-foreground/80 break-all">{query}</span>
      {extra.length > 0 && (
        <div className="opacity-50 mt-0.5">{extra.join("  ")}</div>
      )}
    </div>
  );
}

function URLBlock({ input }: { input: string }) {
  const url = (parseJSON(input).url as string) || "";
  if (!url) return <RawJSON input={input} />;
  return (
    <div className="text-[11px] font-mono text-blue-500/80 bg-muted/30 rounded px-2 py-1 overflow-x-auto truncate">
      {url}
    </div>
  );
}

function RawJSON({ input }: { input: string }) {
  let formatted: string;
  try { formatted = JSON.stringify(JSON.parse(input), null, 2); } catch { formatted = input; }
  return (
    <pre className="text-[11px] bg-background/50 rounded p-1.5 overflow-x-auto max-h-[160px] overflow-y-auto whitespace-pre-wrap break-all text-muted-foreground">
      {formatted}
    </pre>
  );
}

function RawOutput({ output, isError }: { output?: string; isError?: boolean }) {
  const safeOutput = asText(output);
  const text = safeOutput.length > 2000 ? safeOutput.slice(0, 2000) + "\n... (truncated)" : safeOutput;
  return (
    <pre className={cn(
      "text-[11px] rounded p-1.5 overflow-x-auto max-h-[160px] overflow-y-auto whitespace-pre-wrap break-all",
      isError ? "bg-red-500/5 text-red-700 dark:text-red-400" : "bg-background/50 text-muted-foreground"
    )}>
      {text || (isError ? "(failed with no output)" : "(empty)")}
    </pre>
  );
}

// ── Todo rendering ──

interface TodoItem { id: string; content: string; status: string }

const TODO_ICONS: Record<string, { icon: React.ComponentType<{ className?: string }>; color: string }> = {
  TODO_STATUS_COMPLETED: { icon: CheckCircle2, color: "text-green-500" },
  completed:             { icon: CheckCircle2, color: "text-green-500" },
  TODO_STATUS_IN_PROGRESS: { icon: CircleDot, color: "text-blue-500" },
  in_progress:             { icon: CircleDot, color: "text-blue-500" },
  TODO_STATUS_PENDING: { icon: Circle, color: "text-muted-foreground/50" },
  pending:             { icon: Circle, color: "text-muted-foreground/50" },
  TODO_STATUS_CANCELLED: { icon: Ban, color: "text-muted-foreground/30" },
  cancelled:             { icon: Ban, color: "text-muted-foreground/30" },
};

export function parseTodoOutput(output: string): TodoItem[] {
  try {
    const parsed = JSON.parse(output);
    const todos = parsed?.success?.todos;
    if (Array.isArray(todos)) return todos;
  } catch { /* ignore */ }
  return [];
}

export function parseTodoInput(input: string): TodoItem[] {
  const parsed = parseJSON(input);
  return (Array.isArray(parsed.todos) ? parsed.todos : []) as TodoItem[];
}

export function StandaloneTodoList({ todos, isPending }: { todos: TodoItem[]; isPending: boolean }) {
  if (todos.length === 0) return null;
  return (
    <div className="space-y-px py-0.5">
      {todos.map((t) => {
        const { icon: S, color } = TODO_ICONS[t.status] ?? { icon: CircleDashed, color: "text-muted-foreground/40" };
        const done = t.status === "TODO_STATUS_COMPLETED" || t.status === "completed";
        return (
          <div key={t.id} className="flex items-start gap-1.5 py-px text-[11px]">
            <S className={cn("h-3.5 w-3.5 shrink-0 mt-px", color)} />
            <span className={cn("min-w-0", done && "line-through text-muted-foreground/50")}>{t.content}</span>
          </div>
        );
      })}
      {isPending && <Loader2 className="h-3 w-3 animate-spin text-blue-500 mt-0.5" />}
    </div>
  );
}

// ── Ask Question rendering ──

interface AskQuestion { id: string; prompt: string; options: { id: string; label: string }[]; allow_multiple?: boolean }

function parseQuestions(input: string): { title?: string; questions: AskQuestion[] } | null {
  try {
    const parsed = parseJSON(input);
    const title = parsed.title as string | undefined;
    let questions: AskQuestion[];
    if (typeof parsed.questions === "string") {
      questions = JSON.parse(parsed.questions as string);
    } else if (Array.isArray(parsed.questions)) {
      questions = parsed.questions as AskQuestion[];
    } else {
      return null;
    }
    if (!Array.isArray(questions) || questions.length === 0) return null;
    return { title, questions };
  } catch { return null; }
}

function QuestionPreview({ input }: { input: string }) {
  const data = parseQuestions(input);
  if (!data) return <RawJSON input={input} />;
  return (
    <div className="space-y-2">
      {data.questions.map((q, qi) => (
        <div key={q.id || qi} className="text-[11px]">
          <div className="font-medium mb-0.5">{qi + 1}. {q.prompt}{q.allow_multiple ? <span className="text-muted-foreground ml-1">(multi)</span> : null}</div>
          <div className="flex flex-wrap gap-1 ml-3">
            {q.options.map((o) => (
              <span key={o.id} className="px-1.5 py-0.5 rounded bg-muted text-muted-foreground">{o.label}</span>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

// ── Tool registry ──

interface ToolMeta {
  icon: React.ComponentType<{ className?: string }>;
  label: (state: ToolUseState) => string;
  subtitle?: (input: string) => string;
  renderInput?: (input: string) => ReactNode;
  renderOutput?: (output: string, isError?: boolean) => ReactNode;
}

const toolRegistry: Record<string, ToolMeta> = {
  // ── File operations ──
  Read: {
    icon: FileText, label: tense("Read", "Reading"),
    subtitle: (i) => baseName((parseJSON(i).path as string) || (parseJSON(i).file_path as string) || ""),
    renderInput: (i) => <FilePathBlock input={i} />,
  },
  Edit: {
    icon: Pencil, label: tense("Edited", "Editing"),
    subtitle: (i) => baseName((parseJSON(i).path as string) || (parseJSON(i).file_path as string) || ""),
    renderInput: (i) => <FilePathBlock input={i} />,
  },
  StrReplace: {
    icon: Pencil, label: tense("Edited", "Editing"),
    subtitle: (i) => baseName((parseJSON(i).path as string) || (parseJSON(i).file_path as string) || ""),
    renderInput: (i) => <FilePathBlock input={i} />,
  },
  Write: {
    icon: FilePen, label: tense("Created", "Creating"),
    subtitle: pathField("file_path"),
    renderInput: (i) => <FilePathBlock input={i} />,
  },
  MultiEdit: {
    icon: Pencil, label: tense("Multi-edited", "Multi-editing"),
    subtitle: pathField("file_path"),
    renderInput: (i) => <FilePathBlock input={i} />,
  },
  Delete: {
    icon: Trash2, label: tense("Deleted", "Deleting"),
    subtitle: pathField("path"),
    renderInput: (i) => <FilePathBlock input={i} />,
  },
  LS: {
    icon: FolderOpen, label: tense("Listed", "Listing"),
    subtitle: pathField("path"),
    renderInput: (i) => <FilePathBlock input={i} />,
  },

  // ── Shell ──
  Shell: {
    icon: Terminal, label: () => "",
    subtitle: (i) => {
      return truncate(commandFromInput(i), COMMAND_PREVIEW_MAX);
    },
    renderInput: (i) => <CommandBlock input={i} />,
    renderOutput: (o, err) => <TerminalOutput output={o} isError={err} />,
  },
  Bash: {
    icon: Terminal, label: () => "",
    subtitle: (i) => {
      return truncate(commandFromInput(i), COMMAND_PREVIEW_MAX);
    },
    renderInput: (i) => <CommandBlock input={i} />,
    renderOutput: (o, err) => <TerminalOutput output={o} isError={err} />,
  },
  WriteShellStdin: {
    icon: Terminal, label: tense("Wrote stdin", "Writing stdin"),
    renderOutput: (o, err) => <TerminalOutput output={o} isError={err} />,
  },

  // ── Search ──
  Grep: {
    icon: Search, label: tense("Searched", "Searching"),
    subtitle: textField("pattern"),
    renderInput: (i) => <SearchBlock input={i} fields={["pattern"]} />,
  },
  Glob: {
    icon: FolderSearch, label: tense("Found files", "Finding files"),
    subtitle: (i) => textField("pattern")(i) || textField("globPattern")(i),
    renderInput: (i) => <SearchBlock input={i} fields={["pattern", "globPattern"]} />,
  },
  SemanticSearch: {
    icon: BookOpen, label: tense("Semantic search", "Searching"),
    subtitle: textField("query"),
    renderInput: (i) => <SearchBlock input={i} fields={["query"]} />,
  },
  WebSearch: {
    icon: Search, label: tense("Searched web", "Searching web"),
    subtitle: (i) => textField("searchTerm")(i) || textField("query")(i),
    renderInput: (i) => <SearchBlock input={i} fields={["searchTerm", "query"]} />,
  },

  // ── Web ──
  WebFetch: {
    icon: Globe, label: tense("Fetched", "Fetching"),
    subtitle: (i) => {
      const url = (parseJSON(i).url as string) || "";
      try { return new URL(url).hostname.replace("www.", ""); } catch { return truncate(url, 40); }
    },
    renderInput: (i) => <URLBlock input={i} />,
  },

  // ── Task / planning ──
  Task:       { icon: Sparkles,   label: tense("Agent", "Agent"), subtitle: textField("description") },
  Agent:      { icon: Sparkles,   label: tense("Agent", "Agent"), subtitle: textField("description") },
  CreatePlan: { icon: ListChecks, label: tense("Created plan", "Creating plan"),          subtitle: textField("name") },

  // ── Mode / diagnostics ──
  SwitchMode: { icon: ToggleRight, label: tense("Switched mode", "Switching mode"), subtitle: textField("targetModeId") },
  ReadLints:  { icon: Bug,         label: tense("Read lints", "Reading lints") },

  // ── Notebook ──
  NotebookRead: { icon: FileText,     label: tense("Read notebook", "Reading notebook"),   subtitle: pathField("notebook") },
  NotebookEdit: { icon: NotebookPen,  label: tense("Edited notebook", "Editing notebook"), subtitle: pathField("notebook") },
  EditNotebook: { icon: NotebookPen,  label: tense("Edited notebook", "Editing notebook"), subtitle: (i) => baseName((parseJSON(i).target_notebook as string) || "") },

  // ── MCP generic ──
  ListMcpResources: { icon: FolderSearch, label: tense("Listed MCP resources", "Listing MCP resources"), subtitle: textField("server") },
  ReadMcpResource:  { icon: FileText,     label: tense("Read MCP resource", "Reading MCP resource"),     subtitle: textField("uri") },
  FetchMcpResource: { icon: FileText,     label: tense("Fetched MCP resource", "Fetching MCP resource"), subtitle: textField("uri") },
  AskQuestion:      { icon: CircleHelp, label: tense("Asked question", "Asking question"), subtitle: textField("title"), renderInput: (i) => <QuestionPreview input={i} /> },
  AskUserQuestion:  { icon: CircleHelp, label: tense("Asked question", "Asking question"), subtitle: textField("question"), renderInput: (i) => <QuestionPreview input={i} /> },

  // ── Codex file changes ──
  FileChange: {
    icon: FilePen,
    label: tense("Modified files", "Modifying files"),
    subtitle: (i) => {
      const changes = (parseJSON(i).changes as Array<{ path: string; kind: string }>) || [];
      if (changes.length === 0) return "";
      if (changes.length === 1) return baseName(changes[0].path);
      return `${changes.length} files`;
    },
    renderInput: (i) => {
      const changes = (parseJSON(i).changes as Array<{ path: string; kind: string }>) || [];
      if (changes.length === 0) return null;
      const kindLabel: Record<string, string> = { create: "+", update: "~", delete: "-" };
      return (
        <div className="space-y-0.5">
          {changes.map((c, idx) => (
            <div key={idx} className="flex items-center gap-1.5 text-[11px] font-mono text-muted-foreground bg-muted/30 rounded px-2 py-0.5">
              <span className={c.kind === "create" ? "text-green-500" : c.kind === "delete" ? "text-red-500" : "text-yellow-500"}>
                {kindLabel[c.kind] ?? "~"}
              </span>
              <span className="break-all">{c.path}</span>
            </div>
          ))}
        </div>
      );
    },
  },

  // ── NexBox MCP tools (mcp__nexbox__ prefix from Claude) ──
  "mcp__nexbox__save_asset":            { icon: Save,              label: tense("Saved asset", "Saving asset"), subtitle: (i) => {
    const p = parseJSON(i); const t = (p.title as string) || ""; const ty = (p.type as string) || "";
    return t ? `${t}${ty ? ` (${ty})` : ""}` : "";
  }},
  "mcp__nexbox__get_asset_content":     { icon: BookOpen,          label: tense("Read asset", "Reading asset"),             subtitle: textField("title") },
  "mcp__nexbox__list_assets":           { icon: List,              label: tense("Listed assets", "Listing assets") },
  "mcp__nexbox__list_asset_comments":   { icon: MessageSquareMore, label: tense("Listed comments", "Listing comments"),     subtitle: textField("asset_id") },
  "mcp__nexbox__ask_question":          { icon: CircleHelp,        label: tense("Asked question", "Asking question"),       subtitle: textField("title"), renderInput: (i) => <QuestionPreview input={i} /> },
  "mcp__nexbox__get_task":              { icon: BookOpen,          label: tense("Read task", "Reading task"),                subtitle: textField("task_id") },
  "mcp__nexbox__list_tasks":            { icon: List,              label: tense("Listed tasks", "Listing tasks"),            subtitle: textField("status") },
  "mcp__nexbox__create_task":           { icon: Sparkles,          label: tense("Created task", "Creating task"),            subtitle: textField("title") },
  "mcp__nexbox__run_in_terminal":       { icon: SquareTerminal,    label: tense("Ran in terminal", "Running in terminal"),   subtitle: textField("command"), renderInput: (i) => <CommandBlock input={i} />, renderOutput: (o, err) => <TerminalOutput output={o} isError={err} /> },
  "mcp__nexbox__get_gitlab_mr_comments": { icon: GitPullRequestArrow, label: tense("Fetched MR comments", "Fetching MR comments"), subtitle: textField("mr_url") },

  // ── NexBox MCP tools (nexbox/ prefix from Codex/Cursor) ──
  "nexbox/save_asset":            { icon: Save,              label: tense("Saved asset", "Saving asset"), subtitle: (i) => {
    const p = parseJSON(i); const t = (p.title as string) || ""; const ty = (p.type as string) || "";
    return t ? `${t}${ty ? ` (${ty})` : ""}` : "";
  }},
  "nexbox/get_asset_content":     { icon: BookOpen,          label: tense("Read asset", "Reading asset"),             subtitle: textField("title") },
  "nexbox/list_assets":           { icon: List,              label: tense("Listed assets", "Listing assets") },
  "nexbox/list_asset_comments":   { icon: MessageSquareMore, label: tense("Listed comments", "Listing comments"),     subtitle: textField("asset_id") },
  "nexbox/ask_question":          { icon: CircleHelp,        label: tense("Asked question", "Asking question"),       subtitle: textField("title"), renderInput: (i) => <QuestionPreview input={i} /> },
  "nexbox/get_task":              { icon: BookOpen,          label: tense("Read task", "Reading task"),                subtitle: textField("task_id") },
  "nexbox/list_tasks":            { icon: List,              label: tense("Listed tasks", "Listing tasks"),            subtitle: textField("status") },
  "nexbox/create_task":           { icon: Sparkles,          label: tense("Created task", "Creating task"),            subtitle: textField("title") },
  "nexbox/run_in_terminal":       { icon: SquareTerminal,    label: tense("Ran in terminal", "Running in terminal"),   subtitle: textField("command"), renderInput: (i) => <CommandBlock input={i} />, renderOutput: (o, err) => <TerminalOutput output={o} isError={err} /> },
  "nexbox/get_gitlab_mr_comments": { icon: GitPullRequestArrow, label: tense("Fetched MR comments", "Fetching MR comments"), subtitle: textField("mr_url") },
};

function normalizeToolName(toolName: string): string {
  // "mcp__nexbox__save_asset" → "save_asset"
  const mcpMatch = toolName.match(/^mcp__\w+__(.+)$/);
  if (mcpMatch) return mcpMatch[1];
  // "nexbox/save_asset" or "provider/tool" → "save_asset"
  const slashIdx = toolName.indexOf("/");
  if (slashIdx >= 0) return toolName.slice(slashIdx + 1);
  return toolName;
}

function getToolMeta(toolName: string): ToolMeta {
  if (toolRegistry[toolName]) return toolRegistry[toolName];

  // Try normalized form: strip mcp__nexbox__ or nexbox/ prefix
  const normalized = normalizeToolName(toolName);
  if (normalized !== toolName) {
    // Try "mcp__nexbox__<normalized>" and "nexbox/<normalized>" forms
    const mcpKey = `mcp__nexbox__${normalized}`;
    if (toolRegistry[mcpKey]) return toolRegistry[mcpKey];
    const slashKey = `nexbox/${normalized}`;
    if (toolRegistry[slashKey]) return toolRegistry[slashKey];
    // Try normalized as direct key (e.g. "save_asset" might not exist, but "Read" would)
    if (toolRegistry[normalized]) return toolRegistry[normalized];
  }

  // Fallback: try matching by the last segment split only by "/"
  const lastSegment = toolName.split("/").pop() || toolName;
  if (lastSegment !== toolName && toolRegistry[lastSegment]) {
    return toolRegistry[lastSegment];
  }

  return { icon: Sparkles, label: () => toolName };
}

// ── Card component ──

function ToolIcon({ state, icon: Icon }: { state: ToolUseState; icon: React.ComponentType<{ className?: string }> }) {
  switch (state) {
    case "streaming":
    case "running":
      return <Loader2 className="h-3 w-3 animate-spin text-blue-500 shrink-0" />;
    case "error":
      return <XCircle className="h-3 w-3 text-red-500 shrink-0" />;
    case "complete":
      return <Icon className="h-3 w-3 text-muted-foreground shrink-0" />;
  }
}

interface ToolCallCardProps {
  toolName: string;
  toolCallId: string;
  input: string;
  summary?: string;
  state: ToolUseState;
  result?: { output: string; isError?: boolean };
  nestedTools?: NestedTool[];
}

export const TODO_TOOLS = new Set([
  "TodoWrite", "UpdateTodos", "ReadTodos",
  "updateTodosToolCall", "readTodosToolCall", "todoWriteToolCall",
]);

/** Tools whose UI is rendered by dedicated components (not as a tool card). */
export const HIDDEN_TOOLS = new Set([
  "mcp__nexbox__visualize_show_widget",
  "mcp__nexbox__visualize_read_me",
  "nexbox/visualize_show_widget",
  "nexbox/visualize_read_me",
]);

export const ToolCallCard = memo(function ToolCallCard({
  toolName,
  input,
  summary,
  state,
  result,
  nestedTools,
}: ToolCallCardProps) {
  const isAgentTool = toolName === "Agent" || toolName === "Task";
  const [expanded, setExpanded] = useState(isAgentTool && state === "running");
  // displayState lags behind state prop by 1s when transitioning from running→complete/error
  const [displayState, setDisplayState] = useState<ToolUseState>(state);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Auto-expand agent card when nested tools start arriving; auto-collapse when done.
  useEffect(() => {
    if (isAgentTool && nestedTools && nestedTools.length > 0 && state === "running") {
      setExpanded(true);
    } else if (isAgentTool && state === "complete") {
      setExpanded(false);
    }
  }, [isAgentTool, nestedTools?.length, state]);

  useEffect(() => {
    // Delay visual transition to complete/error by 1s to prevent flicker.
    // Tools always start as "running", so complete/error only arrives after running.
    if (state === "complete" || state === "error") {
      timerRef.current = setTimeout(() => {
        setDisplayState(state);
      }, 1000);
      return () => {
        if (timerRef.current) clearTimeout(timerRef.current);
      };
    }
    setDisplayState(state);
    return undefined;
  }, [state]);

  const isPending = displayState === "streaming" || displayState === "running";

  if (TODO_TOOLS.has(toolName)) {
    const todos = result ? parseTodoOutput(result.output) : parseTodoInput(input);
    return <StandaloneTodoList todos={todos} isPending={isPending} />;
  }

  const meta = getToolMeta(toolName);
  const label = meta.label(displayState);
  const subtitle = summary || (meta.subtitle ? meta.subtitle(input) : "");
  const hasDetails = !!(input || result || (nestedTools && nestedTools.length > 0));

  return (
    <Collapsible open={expanded} onOpenChange={hasDetails ? setExpanded : undefined}>
      <CollapsibleTrigger
        disabled={!hasDetails}
        className={cn(
          "group flex items-center gap-1.5 w-full text-left py-0.5 text-xs text-muted-foreground outline-none",
          hasDetails && "hover:text-foreground transition-colors",
          isPending && "text-blue-600 dark:text-blue-400"
        )}
      >
        <ToolIcon state={displayState} icon={meta.icon} />
        {label && <span className="font-medium">{label}</span>}
        {subtitle && <span className={cn("truncate min-w-0", label ? "opacity-60" : "font-mono")}>{subtitle}</span>}
        {hasDetails && (
          <span className="ml-auto shrink-0 opacity-0 group-hover:opacity-60 transition-opacity">
            {expanded ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
          </span>
        )}
      </CollapsibleTrigger>

      <CollapsibleContent>
        <div className="ml-5 pl-3 border-l border-border/40 mt-0.5 mb-1 space-y-1.5">
          {input && (meta.renderInput ? meta.renderInput(input) : <RawJSON input={input} />)}
          {nestedTools && nestedTools.length > 0 && (
            <div className="space-y-0">
              {nestedTools.map((nt) => (
                <ToolCallCard
                  key={nt.toolCallId}
                  toolName={nt.toolName}
                  toolCallId={nt.toolCallId}
                  input={nt.input}
                  state={nt.state}
                  result={nt.result}
                />
              ))}
            </div>
          )}
          {result && (meta.renderOutput
            ? meta.renderOutput(result.output ?? "", result.isError)
            : <RawOutput output={result.output ?? ""} isError={result.isError} />
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
});
