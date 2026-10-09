import { useMemo, useState, useCallback, useRef, useEffect, memo, type ReactNode } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { resolveLang, ensureLangRegistered, highlightCode } from "@/lib/highlight";
import { Loader2, X, Copy, Check, FileText, FileCode, FlaskConical, TestTube, ScrollText, GitBranch, GitFork, ChevronDown, ChevronRight, ChevronUp, FileEdit, Sparkles, ArrowRight, CornerDownRight } from "lucide-react";
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from "@/components/ui/collapsible";
import { ThinkingIndicator } from "@/components/chat/ThinkingIndicator";
import { cn } from "@/lib/utils";
import { ToolCallCard, TODO_TOOLS, HIDDEN_TOOLS } from "@/components/chat/ToolCallCard";
import { ThinkingBlock } from "@/components/chat/ThinkingBlock";
import { WidgetBlock } from "@/components/chat/WidgetBlock";
import { DiffPreview, DiffStats } from "@/components/chat/DiffPreview";
import { IssueRow } from "@/plugins/task/IssueRow";
import { messagesApi, sessionsApi } from "@/lib/api";
import { toast } from "sonner";
import { useAppStore } from "@/stores/app";
import { usePreferences, CHAT_FONT_SIZE_PX } from "@/stores/preferences";
import { useSmoothText } from "@/hooks/useSmoothText";
import { useThemeMode } from "@/hooks/useThemeMode";
import { BorderBeam } from "@/components/ui/border-beam";
import { formatDuration } from "@/lib/format";
import type { ChatMessage as ChatMessageType, MessagePart, Task, AssetType, NestedTool, ToolUseState } from "@/types";

const ASSET_TYPE_CONFIG: Record<string, { label: string; color: string; icon: typeof FileText }> = {
  requirement_doc: { label: "Requirement", color: "text-blue-400", icon: ScrollText },
  design_doc:      { label: "Design",      color: "text-violet-400", icon: FileText },
  code_change:     { label: "Code",        color: "text-emerald-400", icon: FileCode },
  unit_test:       { label: "Unit Test",   color: "text-amber-400", icon: FlaskConical },
  test_case:       { label: "Test Case",   color: "text-rose-400", icon: TestTube },
};

interface ChatMessageProps {
  message: ChatMessageType;
}

interface ToolsGroup {
  type: "tools";
  parts: MessagePart[];
}

interface ThinkingGroup {
  type: "thinking";
  parts: MessagePart[];
}

interface TextGroup {
  type: "text";
  parts: MessagePart[];
}

interface ImagesGroup {
  type: "images";
  parts: MessagePart[];
}

interface WidgetGroup {
  type: "widget";
  parts: MessagePart[];
}

interface StepsGroup {
  type: "steps";
  subGroups: (ThinkingGroup | ToolsGroup)[];
  toolCallCount: number;
  thinkingCount: number;
}

type PartGroup = TextGroup | ThinkingGroup | ToolsGroup | ImagesGroup | WidgetGroup | StepsGroup;

function groupParts(parts: MessagePart[]): PartGroup[] {
  const groups: PartGroup[] = [];
  let currentToolGroup: MessagePart[] = [];
  let currentImageGroup: MessagePart[] = [];

  function flushTools() {
    if (currentToolGroup.length > 0) {
      groups.push({ type: "tools", parts: [...currentToolGroup] });
      currentToolGroup = [];
    }
  }

  function flushImages() {
    if (currentImageGroup.length > 0) {
      groups.push({ type: "images", parts: [...currentImageGroup] });
      currentImageGroup = [];
    }
  }

  for (const part of parts) {
    if (part.type === "tool_use" || part.type === "tool_result" || part.type === "tool_group") {
      flushImages();
      currentToolGroup.push(part);
    } else if (part.type === "image") {
      flushTools();
      currentImageGroup.push(part);
    } else if (part.type === "widget") {
      flushTools();
      flushImages();
      groups.push({ type: "widget", parts: [part] });
    } else {
      flushTools();
      flushImages();
      if (part.type === "thinking") {
        groups.push({ type: "thinking", parts: [part] });
      } else if (part.type === "text" || part.type === "task_ref" || part.type === "asset_ref" || part.type === "workflow_ref") {
        groups.push({ type: "text", parts: [part] });
      }
    }
  }
  flushTools();
  flushImages();
  return groups;
}

// Collapse a consecutive run of thinking/tools groups into a steps group
// when the run contains more than 1 tool call in total. This is the original
// per-batch "2 tool calls" collapse, preserved as-is; the only addition is the
// outer "Worked for Xs" wrapper produced by splitTurn below.
function collapseSteps(groups: PartGroup[]): PartGroup[] {
  type ToolUsePart = Extract<MessagePart, { type: "tool_use" }>;

  const result: PartGroup[] = [];
  let i = 0;

  while (i < groups.length) {
    const g = groups[i];
    if (g.type !== "thinking" && g.type !== "tools") {
      result.push(g);
      i++;
      continue;
    }

    // Scan forward for consecutive thinking/tools groups
    let j = i;
    while (j < groups.length && (groups[j].type === "thinking" || groups[j].type === "tools")) {
      j++;
    }

    const subGroups = groups.slice(i, j) as (ThinkingGroup | ToolsGroup)[];
    let toolCallCount = 0;
    let thinkingCount = 0;
    for (const sg of subGroups) {
      if (sg.type === "tools") {
        toolCallCount += sg.parts.filter(
          (p): p is ToolUsePart => p.type === "tool_use" && !TODO_TOOLS.has((p as ToolUsePart).toolName) && !HIDDEN_TOOLS.has((p as ToolUsePart).toolName)
        ).length;
      } else {
        thinkingCount += 1;
      }
    }

    // Collapse when there are 2+ tool calls in the run — one tool call doesn't need hiding.
    if (toolCallCount > 1) {
      result.push({ type: "steps", subGroups, toolCallCount, thinkingCount });
    } else {
      for (let k = i; k < j; k++) {
        result.push(groups[k]);
      }
    }
    i = j;
  }

  return result;
}

// Split a completed assistant turn into the work that led up to the answer and
// the answer itself. Everything before the final text group (intermediate text,
// thinking, and tool calls) becomes the collapsible "Worked for Xs" block; the
// last text group and anything after it stays visible. The work portion still
// carries its inner per-batch collapses (steps groups) untouched.
function splitTurn(groups: PartGroup[]): { worked: PartGroup[] | null; final: PartGroup[] } {
  let lastTextIdx = -1;
  for (let i = groups.length - 1; i >= 0; i--) {
    if (groups[i].type === "text") {
      lastTextIdx = i;
      break;
    }
  }

  // No final text (e.g. the turn ended on a tool call) → fold the whole turn.
  const preEnd = lastTextIdx === -1 ? groups.length : lastTextIdx;
  const pre = groups.slice(0, preEnd);

  // Only collapse when there's actual work to hide — a plain answer with no
  // preceding steps shouldn't get a "Worked for" header.
  const hasWork = pre.some((g) => g.type === "tools" || g.type === "thinking" || g.type === "steps");
  if (!hasWork) return { worked: null, final: groups };

  return { worked: pre, final: groups.slice(preEnd) };
}

// Fallback header text for turns whose duration wasn't recorded.
function summarizeWork(groups: PartGroup[]): string {
  type ToolUsePart = Extract<MessagePart, { type: "tool_use" }>;
  let tools = 0;
  let thoughts = 0;
  for (const g of groups) {
    if (g.type === "steps") {
      tools += g.toolCallCount;
      thoughts += g.thinkingCount;
    } else if (g.type === "tools") {
      tools += g.parts.filter(
        (p): p is ToolUsePart => p.type === "tool_use" && !TODO_TOOLS.has((p as ToolUsePart).toolName) && !HIDDEN_TOOLS.has((p as ToolUsePart).toolName)
      ).length;
    } else if (g.type === "thinking") {
      thoughts += 1;
    }
  }
  const parts: string[] = [];
  if (tools > 0) parts.push(`${tools} tool call${tools > 1 ? "s" : ""}`);
  if (thoughts > 0) parts.push(`${thoughts} thought${thoughts > 1 ? "s" : ""}`);
  return parts.length ? `Worked · ${parts.join(" · ")}` : "Worked";
}

function ImageLightbox({ src, onClose }: { src: string; onClose: () => void }) {
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
      onClick={onClose}
    >
      <button
        onClick={onClose}
        className="absolute top-4 right-4 text-white/80 hover:text-white z-50"
      >
        <X className="h-6 w-6" />
      </button>
      <img
        src={src}
        alt=""
        className="max-w-[90vw] max-h-[90vh] object-contain rounded-lg shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      />
    </div>
  );
}

function ImageThumbnails({ parts }: { parts: MessagePart[] }) {
  const [lightboxSrc, setLightboxSrc] = useState<string | null>(null);

  return (
    <>
      <div className="flex gap-2 flex-wrap">
        {parts.map((p, i) =>
          p.type === "image" ? (
            <button
              key={i}
              onClick={() => setLightboxSrc(p.url)}
              className="rounded-lg overflow-hidden border hover:ring-2 hover:ring-ring transition-all"
            >
              <img src={p.url} alt="" className="h-20 w-20 object-cover" />
            </button>
          ) : null
        )}
      </div>
      {lightboxSrc && (
        <ImageLightbox src={lightboxSrc} onClose={() => setLightboxSrc(null)} />
      )}
    </>
  );
}

function CollapsibleToolSteps({ parts }: { parts: MessagePart[] }) {
  const resultMap = useMemo(() => {
    const m: Record<string, { output: string; isError?: boolean; diff?: string }> = {};
    for (const p of parts) {
      if (p.type === "tool_result") {
        m[p.toolCallId] = { output: p.output ?? "", isError: p.isError, diff: p.diff };
      }
    }
    return m;
  }, [parts]);

  type ToolUsePart = Extract<MessagePart, { type: "tool_use" }>;
  const toolUses = parts.filter((p): p is ToolUsePart => p.type === "tool_use" && !TODO_TOOLS.has((p as ToolUsePart).toolName) && !HIDDEN_TOOLS.has((p as ToolUsePart).toolName));

  if (toolUses.length === 0) return null;

  return (
    <div className="my-0.5">
      {toolUses.map((part) => (
        <ToolCallCard
          key={part.toolCallId}
          toolName={part.toolName}
          toolCallId={part.toolCallId}
          input={part.input}
          summary={part.summary}
          state={part.state}
          result={resultMap[part.toolCallId]}
          nestedTools={part.nestedTools}
          workflowProgress={part.workflowProgress}
        />
      ))}
    </div>
  );
}

function StepsGroupBlock({
  group,
  isStreaming,
  onLoadDetails,
}: {
  group: StepsGroup;
  isStreaming?: boolean;
  onLoadDetails?: () => Promise<boolean>;
}) {
  // userExpanded: manual toggle state (default collapsed). isStreaming forces open.
  const [userExpanded, setUserExpanded] = useState(false);
  // Reset to collapsed exactly when streaming ends. Done during render (React's
  // "adjust state when a prop changes" pattern) rather than in an effect, which
  // would cascade an extra render. https://react.dev/learn/you-might-not-need-an-effect
  const [wasStreaming, setWasStreaming] = useState(isStreaming);
  if (wasStreaming !== isStreaming) {
    setWasStreaming(isStreaming);
    if (!isStreaming) setUserExpanded(false);
  }
  const expanded = !!isStreaming || userExpanded;

  // Only the last thinking subGroup is actively streaming — all earlier ones are complete.
  const lastThinkingSubGroupIdx = isStreaming
    ? group.subGroups.reduce((last, sg, idx) => (sg.type === "thinking" ? idx : last), -1)
    : -1;

  const summaryParts: string[] = [];
  if (group.toolCallCount > 0) {
    summaryParts.push(`${group.toolCallCount} tool call${group.toolCallCount > 1 ? "s" : ""}`);
  }
  if (group.thinkingCount > 0) {
    summaryParts.push(`${group.thinkingCount} thought${group.thinkingCount > 1 ? "s" : ""}`);
  }
  const summary = summaryParts.join(" · ");

  return (
    <Collapsible open={expanded} onOpenChange={setUserExpanded} className="my-0.5">
      <CollapsibleTrigger className="flex items-center gap-1.5 w-full text-left py-0.5 text-xs text-muted-foreground hover:text-foreground transition-colors outline-none">
        {expanded ? (
          <ChevronDown className="h-3 w-3 shrink-0" />
        ) : (
          <ChevronRight className="h-3 w-3 shrink-0" />
        )}
        <span className="font-medium">{summary}</span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="ml-3 pl-2 border-l border-border/30">
          {group.subGroups.map((sg, idx) => {
            if (sg.type === "thinking") {
              const isThisSubGroupStreaming = isStreaming && idx === lastThinkingSubGroupIdx;
              return sg.parts.map((part, pi) =>
                part.type === "thinking" ? (
                  <ThinkingBlock
                    key={`${idx}-${pi}`}
                    content={part.content || ""}
                    isStreaming={isThisSubGroupStreaming}
                    onLoadDetails={isThisSubGroupStreaming ? undefined : onLoadDetails}
                  />
                ) : null
              );
            }
            if (sg.type === "tools") {
              return (
                <CollapsibleToolSteps key={idx} parts={sg.parts} />
              );
            }
            return null;
          })}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}

// Renders a single completed (non-streaming) group: text, thinking, a single
// tool batch, a collapsed multi-tool "steps" block, images, or widget. Used for
// both the visible final answer and the contents of the "Worked for Xs" block.
function StaticGroup({
  group,
  loadDetails,
}: {
  group: PartGroup;
  loadDetails?: () => Promise<boolean>;
}) {
  switch (group.type) {
    case "text":
      return (
        <div className="markdown-content">
          {group.parts.map((part, pi) =>
            part.type === "text" ? <TextBlock key={pi} content={part.content} /> : null
          )}
        </div>
      );
    case "thinking":
      return (
        <>
          {group.parts.map((part, pi) =>
            part.type === "thinking" ? (
              <ThinkingBlock
                key={pi}
                content={part.content || ""}
                isStreaming={false}
                onLoadDetails={loadDetails}
              />
            ) : null
          )}
        </>
      );
    case "images":
      return (
        <div className="my-1">
          <ImageThumbnails parts={group.parts} />
        </div>
      );
    case "widget":
      return (
        <>
          {group.parts.map((part, pi) =>
            part.type === "widget" ? (
              <WidgetBlock
                key={pi}
                title={part.title}
                widgetCode={part.widgetCode}
                loadingMessages={part.loadingMessages}
              />
            ) : null
          )}
        </>
      );
    case "tools":
      return <CollapsibleToolSteps parts={group.parts} />;
    case "steps":
      return <StepsGroupBlock group={group} onLoadDetails={loadDetails} />;
    default:
      return null;
  }
}

// Collapses everything a completed turn did before its final answer into a
// single "Worked for Xs ›" header, followed by a divider — the answer renders
// below it. Mirrors the Codex client's turn summary.
function WorkedBlock({
  groups,
  durationMs,
  loadDetails,
}: {
  groups: PartGroup[];
  durationMs?: number;
  loadDetails?: () => Promise<boolean>;
}) {
  const [expanded, setExpanded] = useState(false);
  const label =
    durationMs != null && durationMs > 0
      ? `Worked for ${formatDuration(durationMs)}`
      : summarizeWork(groups);

  return (
    <div className="mb-2.5">
      <Collapsible open={expanded} onOpenChange={setExpanded}>
        <CollapsibleTrigger className="flex items-center gap-1.5 text-left py-0.5 text-xs text-muted-foreground hover:text-foreground transition-colors outline-none">
          <span className="font-medium">{label}</span>
          {expanded ? (
            <ChevronDown className="h-3 w-3 shrink-0" />
          ) : (
            <ChevronRight className="h-3 w-3 shrink-0" />
          )}
        </CollapsibleTrigger>
        <CollapsibleContent>
          <div className="mt-1 ml-3 pl-2 border-l border-border/30">
            {groups.map((g, gi) => (
              <StaticGroup key={gi} group={g} loadDetails={loadDetails} />
            ))}
          </div>
          {/* Second collapse control at the tail of the expanded steps, so the
              user can fold the block in place after scrolling to the bottom
              instead of scrolling back up to the header. Lives inside
              CollapsibleContent so it only exists while expanded. */}
          <button
            type="button"
            onClick={() => setExpanded(false)}
            className="mt-1.5 flex w-full items-center gap-1.5 py-0.5 text-xs text-muted-foreground/70 hover:text-foreground transition-colors outline-none"
            title="Collapse"
          >
            <ChevronUp className="h-3 w-3 shrink-0" />
            <span className="font-medium">Collapse</span>
          </button>
        </CollapsibleContent>
      </Collapsible>
      <div className="border-b border-border/40 mt-2" />
    </div>
  );
}

// One fenced code block: lazily registers its language with highlight.js core
// (per-language dynamic import keeps the bundle small), renders the highlighted
// HTML, and shows a single copy button on hover. Falls back to plain text until
// the grammar loads, and for languages we don't bundle.
function ChatCodeBlock({ code, lang }: { code: string; lang: string }) {
  const resolved = useMemo(() => resolveLang(lang), [lang]);
  const [ready, setReady] = useState(false);
  useEffect(() => {
    if (!resolved) return;
    let cancelled = false;
    ensureLangRegistered(resolved).then((ok) => {
      if (ok && !cancelled) setReady(true);
    });
    return () => {
      cancelled = true;
    };
  }, [resolved]);
  const html = useMemo(
    () => (resolved && ready ? highlightCode(code, resolved) : ""),
    [resolved, ready, code],
  );
  return (
    <div className="relative group/code">
      <CopyButton
        text={code}
        className="absolute right-1.5 top-1.5 bg-background/70 backdrop-blur-sm opacity-0 group-hover/code:opacity-100"
      />
      <pre>
        <code className={resolved ? `language-${resolved}` : undefined}>
          {html ? <span dangerouslySetInnerHTML={{ __html: html }} /> : code}
        </code>
      </pre>
    </div>
  );
}

// Renders inline code or a syntax-highlighted fenced block.
function MdCode({ className, children }: { className?: string; children?: ReactNode }) {
  const text = String(children ?? "").replace(/\n$/, "");
  const lang = /language-([\w#+.-]+)/.exec(className || "")?.[1] ?? "";
  const isBlock = !!className || text.includes("\n");
  if (!isBlock) return <code>{children}</code>;
  return <ChatCodeBlock code={text} lang={lang} />;
}

const MD_COMPONENTS: Components = {
  // pre is unwrapped — MdCode (rendered from `code`) owns the <pre>.
  pre: ({ children }) => <>{children}</>,
  code: MdCode,
  // Wide tables overflow narrow viewports — give every rendered table its own
  // horizontal scroll container so it stays in the message column on phones
  // instead of pushing the chat wider than the screen.
  table: ({ children, ...props }) => (
    <div className="overflow-x-auto -mx-1 my-2">
      <table {...props}>{children}</table>
    </div>
  ),
  a: ({ href, children }) => (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      className="text-primary underline underline-offset-2 decoration-primary/40 hover:decoration-primary transition-colors"
    >
      {children}
    </a>
  ),
};

const TextBlock = memo(function TextBlock({ content, isStreaming }: { content: string; isStreaming?: boolean }) {
  // Hide the trailing <next-actions> block (and any partial one mid-stream) —
  // it's surfaced separately as quick-reply buttons, not shown as raw text.
  const visible = useSmoothText(stripNextActions(content), !!isStreaming);
  // Build (and re-parse) the markdown only when `visible` actually changes — not
  // on every token-driven re-render of this block. Combined with useSmoothText's
  // adaptive render throttle, a long streaming reply parses fewer times/sec.
  return useMemo(() => {
    if (!visible) return null;
    return (
    <ReactMarkdown
      remarkPlugins={[[remarkGfm, { singleTilde: false }]]}
      components={MD_COMPONENTS}
    >
      {visible}
    </ReactMarkdown>
    );
  }, [visible]);
});

export function CopyButton({ text, className }: { text: string; className?: string }) {
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback(() => {
    if (!text) return;
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  }, [text]);

  return (
    <button
      onClick={handleCopy}
      className={cn("p-1 rounded-md text-muted-foreground/60 hover:text-foreground hover:bg-muted transition-colors", className)}
      title="Copy"
    >
      {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
    </button>
  );
}

// ForkButton lets the user create a new session branched from a specific
// assistant message. Renders nothing when fork is not applicable (user
// message, still streaming, unsupported backend, or no durable backend
// anchor to identify the cut point).
export function ForkButton({ message, supported }: { message: ChatMessageType; supported: boolean }) {
  const currentSession = useAppStore((s) => s.currentSession);
  const sessions = useAppStore((s) => s.sessions);
  const setSessions = useAppStore((s) => s.setSessions);
  const setActiveSessionId = useAppStore((s) => s.setActiveSessionId);
  const [busy, setBusy] = useState(false);

  const hasForkAnchor = !!message.metadata?.agentCheckpoint;
  const canFork =
    supported &&
    message.role === "assistant" &&
    !message.isStreaming &&
    hasForkAnchor;

  const onClick = useCallback(async () => {
    if (busy || !currentSession) return;
    setBusy(true);
    try {
      if (!hasForkAnchor) return;
      // serverId is the durable DB row id learned at turn end; fall back to id
      // for messages loaded straight from the API (where id already is it).
      const newSess = await sessionsApi.fork(currentSession.id, message.serverId ?? message.id);
      setSessions([newSess, ...sessions.filter((s) => s.id !== newSess.id)]);
      setActiveSessionId(newSess.id);
    } catch (err) {
      console.error("Fork failed:", err);
      toast.error("Failed to fork session", {
        description: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setBusy(false);
    }
  }, [busy, currentSession, hasForkAnchor, message.serverId, message.id, sessions, setSessions, setActiveSessionId]);

  if (!canFork) return null;

  return (
    <button
      onClick={onClick}
      disabled={busy}
      className="p-1 rounded-md text-muted-foreground/60 hover:text-foreground hover:bg-muted transition-colors disabled:opacity-50"
      title="Fork conversation from this point"
    >
      {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <GitFork className="h-3.5 w-3.5" />}
    </button>
  );
}

// Shared helper imported by ChatPanel; intentionally co-located with the chat
// components, so this file exports a non-component alongside them.
// eslint-disable-next-line react-refresh/only-export-components
export function extractPlainText(parts: MessagePart[]): string {
  return parts
    .filter((p) => p.type === "text")
    .map((p) => (p as { type: "text"; content: string }).content)
    .join("");
}

type FileChangeKind = "create" | "update" | "delete";
interface FileChangeEntry {
  path: string;
  // The kind of the *last* operation that touched this path this turn.
  kind: FileChangeKind;
  // Unified diff(s) for this file, in chronological order, when the agent
  // backend could derive one per operation; each is independently parseable.
  diffs: string[];
}

// File-write tool names, normalized to lowercase so both PascalCase (codex/
// claude) and lowercase (crush/pi) names are recognized.
const FILE_WRITE_TOOLS = new Set(["edit", "write", "multiedit", "strreplace", "delete"]);

function parseJSONSafe(s: string): Record<string, unknown> {
  try { return JSON.parse(s); } catch { return {}; }
}

function kindFromToolName(name: string): FileChangeKind {
  const n = name.toLowerCase();
  if (n === "write") return "create";
  if (n === "delete") return "delete";
  return "update";
}

type FileTouchingCall = {
  toolName: string;
  input: string;
  toolCallId: string;
  result?: { output: string; isError?: boolean; diff?: string };
};

// Flattens top-level tool_use/tool_result pairs *and* nested sub-agent tool
// calls (which carry their own result inline, see NestedTool) into one
// chronological list, so the "N files changed" summary reflects every file
// the turn touched — not just the main agent's own direct calls.
function flattenFileTouchingCalls(parts: MessagePart[]): FileTouchingCall[] {
  const resultByToolCallId = new Map<string, { output: string; isError?: boolean; diff?: string }>();
  for (const part of parts) {
    if (part.type === "tool_result") {
      resultByToolCallId.set(part.toolCallId, { output: part.output ?? "", isError: part.isError, diff: part.diff });
    }
  }

  const calls: FileTouchingCall[] = [];
  for (const part of parts) {
    if (part.type !== "tool_use") continue;
    calls.push({ toolName: part.toolName, input: part.input, toolCallId: part.toolCallId, result: resultByToolCallId.get(part.toolCallId) });
    for (const nested of part.nestedTools ?? []) {
      calls.push({ toolName: nested.toolName, input: nested.input, toolCallId: nested.toolCallId, result: nested.result });
    }
  }
  return calls;
}

function extractFileChanges(parts: MessagePart[]): FileChangeEntry[] {
  const kindByPath = new Map<string, FileChangeKind>();
  const diffsByPath = new Map<string, string[]>();

  const record = (path: string, kind: FileChangeKind, diff: string | undefined) => {
    kindByPath.set(path, kind);
    if (diff && diff.trim() !== "") {
      const arr = diffsByPath.get(path) ?? [];
      arr.push(diff);
      diffsByPath.set(path, arr);
    }
  };

  for (const call of flattenFileTouchingCalls(parts)) {
    const { toolName, input, result } = call;
    // A failed edit/write didn't actually change anything — don't count it
    // (and don't let it clobber an earlier successful op's kind/diff).
    if (result?.isError) continue;

    if (toolName === "FileChange") {
      const parsed = parseJSONSafe(input);
      const changes = parsed.changes as Array<{ path?: string; kind?: string; diff?: string }> | undefined;
      if (!Array.isArray(changes)) continue;
      for (const c of changes) {
        if (!c?.path) continue;
        const k = (c.kind === "create" || c.kind === "delete") ? c.kind : "update";
        record(c.path, k, c.diff);
      }
      continue;
    }

    if (FILE_WRITE_TOOLS.has(toolName.toLowerCase())) {
      const parsed = parseJSONSafe(input);
      const path = (parsed.file_path as string) || (parsed.path as string) || "";
      if (!path) continue;
      record(path, kindFromToolName(toolName), result?.diff);
    }
  }

  return Array.from(kindByPath.entries()).map(([path, kind]) => ({ path, kind, diffs: diffsByPath.get(path) ?? [] }));
}

function toRelativePath(absPath: string, projectRoot: string | undefined): string {
  if (!projectRoot) return absPath;
  const root = projectRoot.endsWith("/") ? projectRoot : projectRoot + "/";
  if (absPath === projectRoot) return ".";
  if (absPath.startsWith(root)) return absPath.slice(root.length);
  return absPath;
}

// The model is asked (when the setting is on) to end replies with a
// <next-actions> block of dash-prefixed suggestions. parseNextActions extracts
// them (only once the closing tag is present, i.e. the reply is complete);
// stripNextActions removes the block from the rendered text — including a
// dangling, not-yet-closed block during streaming so the raw tag never flashes.
//
// A tag inside markdown code — a fenced block or an inline span — is the model
// QUOTING the tag (say, while explaining this very feature), not emitting a
// block. Those must be skipped: a quoted opening tag otherwise pairs with the
// real block's closing tag far below, and everything in between disappears from
// the message while its dash-prefixed lines turn into bogus chips.

const NEXT_ACTIONS_OPEN = "<next-actions>";

interface NextActionsBlock {
  start: number; // start of the removable range (leading blank lines included)
  end: number;
  innerStart: number; // start of the block's dash-prefixed content
  innerEnd: number;
  closed: boolean; // false for a block still being streamed
}

// Ranges of markdown code in `text`: fenced blocks first, then inline spans in
// what's left. An unterminated inline span doesn't count — during streaming the
// text routinely ends mid-span, and treating that as code would hide the real
// block at the end of the reply.
function codeRegions(text: string): Array<{ s: number; e: number }> {
  const regions: Array<{ s: number; e: number }> = [];
  const covered = (i: number) => regions.some((r) => i >= r.s && i < r.e);

  // Fenced blocks: a ``` / ~~~ line opens, the next fence line using the same
  // character (and at least as long) closes. An unclosed fence runs to the end.
  const fenceLine = /^[ \t]{0,3}(`{3,}|~{3,})[^\n]*$/gm;
  let open: RegExpExecArray | null = null;
  let m: RegExpExecArray | null;
  while ((m = fenceLine.exec(text)) !== null) {
    if (!open) {
      open = m;
    } else if (m[1][0] === open[1][0] && m[1].length >= open[1].length) {
      regions.push({ s: open.index, e: m.index + m[0].length });
      open = null;
    }
  }
  if (open) regions.push({ s: open.index, e: text.length });

  // Inline spans outside those fences. The content may wrap lines but not span
  // a blank line, so one stray backtick can't mask the rest of the message.
  const inlineSpan = /(`+)(?:[^\n]|\n(?![ \t]*\n))*?\1/g;
  while ((m = inlineSpan.exec(text)) !== null) {
    if (covered(m.index)) continue;
    regions.push({ s: m.index, e: m.index + m[0].length });
  }
  return regions;
}

// Locate the genuine <next-actions> blocks in `text`.
function nextActionsBlocks(text: string): NextActionsBlock[] {
  if (!/<next-actions>/i.test(text)) return [];

  const code = codeRegions(text);
  const inCode = (i: number) => code.some((r) => i >= r.s && i < r.e);

  const blocks: NextActionsBlock[] = [];
  const re = /(\n*)<next-actions>([\s\S]*?)(<\/next-actions>|$)/gi;
  let m: RegExpExecArray | null;
  while ((m = re.exec(text)) !== null) {
    const tagAt = m.index + m[1].length;
    if (inCode(tagAt)) {
      // Quoted, not emitted. Resume scanning just past the tag so a real block
      // further down the message is still found.
      re.lastIndex = tagAt + NEXT_ACTIONS_OPEN.length;
      continue;
    }
    const innerStart = tagAt + NEXT_ACTIONS_OPEN.length;
    blocks.push({
      start: m.index,
      end: m.index + m[0].length,
      innerStart,
      innerEnd: innerStart + m[2].length,
      closed: m[3] !== "",
    });
    if (m.index === re.lastIndex) re.lastIndex++;
  }
  return blocks;
}

function cutBlocks(text: string, blocks: NextActionsBlock[]): string {
  let out = "";
  let at = 0;
  for (const b of blocks) {
    out += text.slice(at, b.start);
    at = b.end;
  }
  return out + text.slice(at);
}

function parseNextActions(text: string): string[] {
  // A kept-open turn (background Agent/Workflow) can fold multiple turns into one
  // message, so an early "wrap-up" <next-actions> may precede the real final one.
  // Take the LAST complete block — that's the answer's true suggestions.
  const closed = nextActionsBlocks(text).filter((b) => b.closed);
  if (closed.length === 0) return [];
  const last = closed[closed.length - 1];
  return text
    .slice(last.innerStart, last.innerEnd)
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l.startsWith("-"))
    .map((l) => l.replace(/^-+\s*/, "").trim())
    .filter(Boolean)
    .slice(0, 3);
}

function stripNextActions(text: string): string {
  const blocks = nextActionsBlocks(text);
  // Only trim when we actually removed a block, so messages without one render
  // byte-for-byte as before.
  if (blocks.length === 0) return text;
  return cutBlocks(text, blocks.slice(0, 1)).trimEnd();
}

// Strip EVERY <next-actions> block from a plain-text rendition of a message.
// A single message can contain several: each time the model considers its reply
// finished (a background-task pause, a context compaction) it emits one, and a
// kept-open turn folds all of those into one message. They're surfaced as chips,
// never as text — so copying a message must not paste them either.
// eslint-disable-next-line react-refresh/only-export-components
export function stripAllNextActions(text: string): string {
  const blocks = nextActionsBlocks(text);
  if (blocks.length === 0) return text;
  return cutBlocks(text, blocks).trim();
}

// Strip <next-actions> blocks that may be SPLIT across several text parts. During
// a kept-open turn (background Agent/Workflow), the model's wrap-up text — which
// ends with a <next-actions> block — gets interleaved with tool calls, so the
// block (even its opening tag, e.g. "…<next" | "-actions>…") lands in separate,
// non-adjacent text parts. Per-part stripping can't see the whole tag, so the
// raw fragments leak. This operates on the concatenation (where the tag is whole
// again), finds every block, then maps the removed ranges back onto each text
// part. Fully-emptied text parts are dropped so they don't render blank blocks.
function stripNextActionsAcrossParts(parts: MessagePart[]): MessagePart[] {
  const spanStart: number[] = new Array(parts.length);
  let full = "";
  parts.forEach((p, i) => {
    if (p.type === "text") {
      const c = (p as { content: string }).content || "";
      spanStart[i] = full.length;
      full += c;
    }
  });
  const ranges = nextActionsBlocks(full).map((b) => ({ s: b.start, e: b.end }));
  if (ranges.length === 0) return parts;
  const inRange = (g: number) => ranges.some((r) => g >= r.s && g < r.e);
  const out: MessagePart[] = [];
  parts.forEach((p, i) => {
    if (p.type !== "text") {
      out.push(p);
      return;
    }
    const c = (p as { content: string }).content || "";
    const base = spanStart[i];
    let kept = "";
    for (let k = 0; k < c.length; k++) {
      if (!inRange(base + k)) kept += c[k];
    }
    kept = kept.trimEnd();
    if (kept !== "") out.push({ ...p, content: kept } as MessagePart);
  });
  return out;
}

// Re-nest a sub-agent's tool calls under their parent Agent tool_use card. The
// live store nests them (via WS parent_tool_id); persisted/reloaded parts store
// them FLAT (with parentToolCallId), interleaved with the main agent's streaming
// text. Rendered flat, they fragment the main text — and split a <next-actions>
// block — into many top-level parts. Nesting removes them from the linear flow so
// the main text stays coherent (matching the live view). Idempotent on already-
// nested (live) parts, which carry no top-level parentToolCallId tool.
function nestSubagentTools(parts: MessagePart[]): MessagePart[] {
  const result: MessagePart[] = [];
  const topIdx = new Map<string, number>(); // top-level tool_use toolCallId → index in result
  const nestedParent = new Map<string, number>(); // nested toolCallId → parent index in result
  for (const part of parts) {
    if (part.type === "tool_use" && part.parentToolCallId && topIdx.has(part.parentToolCallId)) {
      const pIdx = topIdx.get(part.parentToolCallId)!;
      const parent = result[pIdx];
      if (parent.type === "tool_use") {
        const nt: NestedTool = { toolCallId: part.toolCallId, toolName: part.toolName, input: part.input, state: part.state };
        result[pIdx] = { ...parent, nestedTools: [...(parent.nestedTools || []), nt] };
        nestedParent.set(part.toolCallId, pIdx);
        continue;
      }
    }
    if (part.type === "tool_result" && nestedParent.has(part.toolCallId)) {
      const pIdx = nestedParent.get(part.toolCallId)!;
      const parent = result[pIdx];
      if (parent.type === "tool_use" && parent.nestedTools) {
        const ni = parent.nestedTools.findIndex((n) => n.toolCallId === part.toolCallId);
        if (ni >= 0) {
          const updated = parent.nestedTools.slice();
          updated[ni] = {
            ...updated[ni],
            state: (part.isError ? "error" : "complete") as ToolUseState,
            result: { output: part.output, isError: part.isError, diff: part.diff },
          };
          result[pIdx] = { ...parent, nestedTools: updated };
        }
      }
      continue;
    }
    if (part.type === "tool_use") topIdx.set(part.toolCallId, result.length);
    result.push(part);
  }
  return result;
}

// Merge consecutive text parts into one. After nestSubagentTools() removes the
// interleaved sub-agent tools, the main agent's text runs (previously split by
// them) become adjacent — merging reunites them so prose and a <next-actions>
// block render as one coherent block. Text separated by the MAIN agent's own
// tool calls stays split (correct).
function mergeAdjacentText(parts: MessagePart[]): MessagePart[] {
  const out: MessagePart[] = [];
  for (const p of parts) {
    const last = out[out.length - 1];
    if (p.type === "text" && last && last.type === "text") {
      out[out.length - 1] = { type: "text", content: last.content + p.content };
    } else {
      out.push(p);
    }
  }
  return out;
}

function NextActions({ actions, sessionId }: { actions: string[]; sessionId: string }) {
  if (actions.length === 0) return null;
  return (
    <div className="mt-2 flex flex-wrap items-start gap-1.5">
      <span className="inline-flex items-center gap-1 mt-1.5 text-[11px] font-medium text-muted-foreground mr-0.5">
        <Sparkles className="h-3 w-3" /> Next
      </span>
      {actions.map((a, i) => (
        <button
          key={i}
          type="button"
          title={a}
          onClick={() =>
            window.dispatchEvent(new CustomEvent("nexbox:set-composer", { detail: { sessionId, text: a } }))
          }
          className="group inline-flex items-center gap-1.5 rounded-full border border-border/60 bg-muted/40 px-2.5 py-1 text-xs text-foreground/80 hover:bg-accent hover:text-foreground transition-colors max-w-full"
        >
          <ArrowRight className="h-3 w-3 shrink-0 mt-[3px] self-start text-muted-foreground group-hover:text-foreground transition-colors" />
          <span className="text-left break-words">{a}</span>
        </button>
      ))}
    </div>
  );
}

function FileChangeRow({ file, projectRoot }: { file: FileChangeEntry; projectRoot?: string }) {
  const [open, setOpen] = useState(false);
  const kindIcon: Record<FileChangeKind, string> = { create: "+", update: "~", delete: "-" };
  const kindClass: Record<FileChangeKind, string> = {
    create: "text-green-500",
    update: "text-yellow-500",
    delete: "text-red-500",
  };
  const combinedDiff = file.diffs.length > 0 ? file.diffs.join("\n") : undefined;
  const hasDiff = !!combinedDiff;

  return (
    <Collapsible open={open && hasDiff} onOpenChange={hasDiff ? setOpen : undefined}>
      <CollapsibleTrigger disabled={!hasDiff} className={cn("flex items-center gap-2 w-full text-left text-[11px] font-mono", hasDiff && "hover:text-foreground")}>
        {hasDiff ? (
          open ? <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground" /> : <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground" />
        ) : (
          <span className="w-3 shrink-0" />
        )}
        <span className={cn("w-3 text-center shrink-0", kindClass[file.kind])}>{kindIcon[file.kind]}</span>
        <span className="break-all text-muted-foreground flex-1 min-w-0" title={file.path}>
          {toRelativePath(file.path, projectRoot)}
        </span>
        {combinedDiff && <DiffStats diff={combinedDiff} />}
      </CollapsibleTrigger>
      {hasDiff && (
        <CollapsibleContent>
          <div className="mt-1 mb-0.5">
            <DiffPreview diff={combinedDiff!} />
          </div>
        </CollapsibleContent>
      )}
    </Collapsible>
  );
}

function FileChangesSummary({ files, projectRoot }: { files: FileChangeEntry[]; projectRoot?: string }) {
  const [expanded, setExpanded] = useState(true);
  if (files.length === 0) return null;

  const summary = `${files.length} file${files.length === 1 ? "" : "s"} changed`;

  return (
    <Collapsible open={expanded} onOpenChange={setExpanded} className="mt-2 rounded-lg border border-border/50 bg-muted/30 overflow-hidden">
      <CollapsibleTrigger className="flex items-center gap-2 w-full text-left px-3 py-2 text-xs hover:bg-muted/50 transition-colors outline-none">
        {expanded ? <ChevronDown className="h-3 w-3 shrink-0" /> : <ChevronRight className="h-3 w-3 shrink-0" />}
        <FileEdit className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <span className="font-medium">{summary}</span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="px-3 pb-2 space-y-1 border-t border-border/30 pt-2">
          {files.map((f, i) => (
            <FileChangeRow key={i} file={f} projectRoot={projectRoot} />
          ))}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}

export const ChatMessage = memo(function ChatMessage({ message }: ChatMessageProps) {
  const isUser = message.role === "user";
  const [loadedParts, setLoadedParts] = useState<MessagePart[] | null>(null);
  const loadingRef = useRef(false);
  const projectRoot = useAppStore((s) => s.currentProject?.git_repo_path);
  const chatFontSize = usePreferences((s) => s.chatFontSize);
  const themeMode = useThemeMode();

  const effectiveParts = loadedParts || message.parts;
  // Prepare parts for rendering: (1) re-nest sub-agent tool calls under their
  // Agent card so they don't fragment the main text, (2) merge the now-adjacent
  // main-agent text runs, (3) strip <next-actions>. Chips are still parsed from
  // the original text below (plainText). This mirrors the live-stream view for
  // persisted/reloaded flat parts.
  const displayParts = useMemo(
    () => stripNextActionsAcrossParts(mergeAdjacentText(nestSubagentTools(effectiveParts))),
    [effectiveParts],
  );
  const groups = useMemo(() => groupParts(displayParts), [displayParts]);
  // For a completed turn, keep the original per-batch "X tool calls" collapse,
  // then fold everything before the final answer into the outer "Worked for Xs"
  // block. Streaming turns stay fully expanded (raw groups, no collapse).
  const turn = useMemo(
    () => (message.isStreaming ? null : splitTurn(collapseSteps(groups))),
    [groups, message.isStreaming],
  );
  // A thinking group is actively streaming only if it's the very last group in the array
  // (meaning nothing has come after it yet). Once tool calls or text appear after it, it's done.
  const activeThinkingGroupIdx = message.isStreaming && groups.length > 0 && groups[groups.length - 1].type === "thinking"
    ? groups.length - 1
    : -1;
  const plainText = useMemo(() => extractPlainText(effectiveParts), [effectiveParts]);
  const fileChanges = useMemo(
    () => (isUser ? [] : extractFileChanges(effectiveParts)),
    [isUser, effectiveParts],
  );
  const nextActions = useMemo(
    () => (isUser ? [] : parseNextActions(plainText)),
    [isUser, plainText],
  );

  const loadDetails = useCallback(async (): Promise<boolean> => {
    if (loadedParts || loadingRef.current) return !!loadedParts;
    loadingRef.current = true;
    try {
      const { parts } = await messagesApi.getDetails(message.id);
      setLoadedParts(parts);
      return true;
    } catch {
      return false;
    } finally {
      loadingRef.current = false;
    }
  }, [message.id, loadedParts]);

  const interjected = isUser && !!message.metadata?.interjected;

  const userImages = isUser
    ? message.parts.filter((p) => p.type === "image")
    : [];

  const taskRefs = isUser
    ? (message.parts.filter((p) => p.type === "task_ref" && (p as { task?: Task }).task) as Array<{ type: "task_ref"; taskId: string; task: Task }>)
    : [];

  const assetRefs = isUser
    ? message.parts.filter((p) => p.type === "asset_ref") as Array<{ type: "asset_ref"; assetId: string; title: string; assetType: AssetType }>
    : [];

  const workflowRefs = isUser
    ? message.parts.filter((p) => p.type === "workflow_ref") as Array<{ type: "workflow_ref"; workflowId: string; name: string; nodeCount?: number; edgeCount?: number }>
    : [];

  return (
    <div
      className={cn(
        "flex flex-row w-full",
        isUser ? "justify-end gap-4" : "gap-0",
      )}
    >
      <div
        className={cn(
          "flex flex-col gap-1 leading-relaxed min-w-0",
          isUser
            ? "max-w-[80%]"
            : "w-full",
        )}
        style={{ fontSize: CHAT_FONT_SIZE_PX[chatFontSize] }}
      >
        {isUser ? (
          <div>
            <div className="inline-block max-w-full space-y-2">
              {(plainText || userImages.length > 0) && (
                <div className="flex items-center justify-end gap-2">
                  {interjected && (
                    <span
                      className="flex shrink-0 items-center gap-1 text-[10px] font-medium uppercase tracking-wide text-muted-foreground/50"
                      title="Sent while the assistant was working"
                    >
                      <CornerDownRight className="h-3 w-3" />
                      Interjected
                    </span>
                  )}
                  <div className="rounded-2xl px-4 py-2.5 bg-muted overflow-hidden min-w-0">
                    {userImages.length > 0 && (
                      <div className="mb-2">
                        <ImageThumbnails parts={userImages} />
                      </div>
                    )}
                    {plainText && (
                      <div className="whitespace-pre-wrap break-words text-sm leading-relaxed">
                        {plainText}
                      </div>
                    )}
                  </div>
                </div>
              )}
              {taskRefs.length > 0 && (
                <div className="rounded-xl border border-border/40 bg-muted/30 overflow-hidden">
                  {taskRefs.map((ref) => (
                    <IssueRow key={ref.taskId} task={ref.task} compact />
                  ))}
                </div>
              )}
              {assetRefs.length > 0 && (
                <div className="rounded-xl border border-border/40 bg-muted/30 overflow-hidden">
                  {assetRefs.map((ref) => {
                    const cfg = ASSET_TYPE_CONFIG[ref.assetType] || ASSET_TYPE_CONFIG.design_doc;
                    const Icon = cfg.icon;
                    return (
                      <div
                        key={ref.assetId}
                        className="flex items-center gap-2.5 px-3 py-2 text-sm"
                      >
                        <Icon className={cn("h-4 w-4 shrink-0", cfg.color)} />
                        <span className="truncate font-medium">{ref.title || "Untitled"}</span>
                        <span className={cn("text-[10px] px-1.5 py-0.5 rounded-full bg-muted shrink-0", cfg.color)}>
                          {cfg.label}
                        </span>
                      </div>
                    );
                  })}
                </div>
              )}
              {workflowRefs.length > 0 && (
                <div className="rounded-xl border border-border/40 bg-muted/30 overflow-hidden">
                  {workflowRefs.map((ref) => (
                    <div
                      key={ref.workflowId}
                      className="flex items-center gap-2.5 px-3 py-2 text-sm"
                    >
                      <GitBranch className="h-4 w-4 shrink-0 text-cyan-400" />
                      <span className="truncate font-medium">{ref.name || "Workflow"}</span>
                      <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-muted shrink-0 text-cyan-400">
                        Workflow
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        ) : (
          <div>
            <div className="relative isolate overflow-hidden rounded-2xl bg-muted/50 max-w-full">
              <div className="relative z-0 px-4 py-3 max-w-full">
                {message.isStreaming ? (
                <>
                  {groups.length === 0 && <ThinkingIndicator />}
                  {groups.map((group, gi) => {
                    const isLastGroup = gi === groups.length - 1;
                    const textIsStreaming = isLastGroup && group.type === "text";
                    switch (group.type) {
                      case "text":
                        return (
                          <div key={gi} className="markdown-content">
                            {group.parts.map((part, pi) => {
                              if (part.type !== "text") return null;
                              const isLastTextPart = pi === group.parts.length - 1;
                              return (
                                <TextBlock
                                  key={pi}
                                  content={part.content}
                                  isStreaming={textIsStreaming && isLastTextPart}
                                />
                              );
                            })}
                          </div>
                        );
                      case "thinking":
                        return group.parts.map((part, pi) =>
                          part.type === "thinking" ? (
                            <ThinkingBlock
                              key={`${gi}-${pi}`}
                              content={part.content || ""}
                              isStreaming={gi === activeThinkingGroupIdx}
                              onLoadDetails={gi === activeThinkingGroupIdx ? undefined : loadDetails}
                            />
                          ) : null
                        );
                      case "images":
                        return (
                          <div key={gi} className="my-1">
                            <ImageThumbnails parts={group.parts} />
                          </div>
                        );
                      case "widget":
                        return group.parts.map((part, pi) =>
                          part.type === "widget" ? (
                            <WidgetBlock
                              key={`${gi}-${pi}`}
                              title={part.title}
                              widgetCode={part.widgetCode}
                              loadingMessages={part.loadingMessages}
                            />
                          ) : null
                        );
                      case "tools":
                        return <CollapsibleToolSteps key={gi} parts={group.parts} />;
                      default:
                        return null;
                    }
                  })}
                  {groups.length > 0 && (
                    // Text groups carry their own trailing margin, so mt-1 reads fine there;
                    // tool-call / thinking / media groups don't, so give the indicator more
                    // breathing room after them.
                    <ThinkingIndicator className={groups[groups.length - 1].type === "text" ? "mt-1" : "mt-3"} />
                  )}
                </>
              ) : (
                <>
                  {turn?.worked && turn.worked.length > 0 && (
                    <WorkedBlock
                      groups={turn.worked}
                      durationMs={message.metadata?.durationMs}
                      loadDetails={loadDetails}
                    />
                  )}
                  {(turn?.final ?? groups).map((group, gi) => (
                    <StaticGroup key={gi} group={group} loadDetails={loadDetails} />
                  ))}
                  <NextActions actions={nextActions} sessionId={message.session_id} />
                  {fileChanges.length > 0 && (
                    <FileChangesSummary files={fileChanges} projectRoot={projectRoot} />
                  )}
                </>
              )}
              </div>
              {/*
                Keep the paint-heavy moving masks on a fixed-height overlay.
                Wrapping the message itself makes the animated raster area grow
                with every streamed paragraph/tool card and eventually starves
                scrolling and input of frame time.

                BorderBeam's generated CSS sets its root to position:relative,
                so position:absolute must be inline to win the cascade.
              */}
              <BorderBeam
                size="line"
                theme={themeMode}
                active={message.isStreaming}
                duration={6}
                borderRadius={16}
                aria-hidden="true"
                className="inset-x-0 bottom-0 z-10 h-[128px] w-full pointer-events-none"
                style={{ position: "absolute" }}
              >
                <div className="h-full w-full" />
              </BorderBeam>
            </div>
          </div>
        )}
      </div>
    </div>
  );
});
