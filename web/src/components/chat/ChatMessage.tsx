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
import { IssueRow } from "@/plugins/task/IssueRow";
import { messagesApi, sessionsApi } from "@/lib/api";
import { toast } from "sonner";
import { useAppStore } from "@/stores/app";
import { usePreferences, CHAT_FONT_SIZE_PX } from "@/stores/preferences";
import { useSmoothText } from "@/hooks/useSmoothText";
import { formatDuration } from "@/lib/format";
import type { ChatMessage as ChatMessageType, MessagePart, Task, AssetType } from "@/types";

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
    const m: Record<string, { output: string; isError?: boolean }> = {};
    for (const p of parts) {
      if (p.type === "tool_result") {
        m[p.toolCallId] = { output: p.output ?? "", isError: p.isError };
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
      const newSess = await sessionsApi.fork(currentSession.id, message.id);
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
  }, [busy, currentSession, hasForkAnchor, message.id, sessions, setSessions, setActiveSessionId]);

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
  kind: FileChangeKind;
}

const FILE_WRITE_TOOLS = new Set(["Edit", "Write", "MultiEdit", "StrReplace", "Delete"]);

function parseJSONSafe(s: string): Record<string, unknown> {
  try { return JSON.parse(s); } catch { return {}; }
}

function kindFromToolName(name: string): FileChangeKind {
  if (name === "Write") return "create";
  if (name === "Delete") return "delete";
  return "update";
}

function extractFileChanges(parts: MessagePart[]): FileChangeEntry[] {
  const map = new Map<string, FileChangeKind>();
  const rank: Record<FileChangeKind, number> = { update: 0, create: 1, delete: 2 };

  for (const part of parts) {
    if (part.type !== "tool_use") continue;
    const { toolName, input } = part;

    if (toolName === "FileChange") {
      const parsed = parseJSONSafe(input);
      const changes = parsed.changes as Array<{ path?: string; kind?: string }> | undefined;
      if (!Array.isArray(changes)) continue;
      for (const c of changes) {
        if (!c?.path) continue;
        const k = (c.kind === "create" || c.kind === "delete") ? c.kind : "update";
        const prev = map.get(c.path);
        if (prev == null || rank[k] > rank[prev]) map.set(c.path, k);
      }
      continue;
    }

    if (FILE_WRITE_TOOLS.has(toolName)) {
      const parsed = parseJSONSafe(input);
      const path = (parsed.file_path as string) || (parsed.path as string) || "";
      if (!path) continue;
      const k = kindFromToolName(toolName);
      const prev = map.get(path);
      if (prev == null || rank[k] > rank[prev]) map.set(path, k);
    }
  }

  return Array.from(map.entries()).map(([path, kind]) => ({ path, kind }));
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
const NEXT_ACTIONS_RE = /<next-actions>([\s\S]*?)<\/next-actions>/i;

function parseNextActions(text: string): string[] {
  const m = text.match(NEXT_ACTIONS_RE);
  if (!m) return [];
  return m[1]
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l.startsWith("-"))
    .map((l) => l.replace(/^-+\s*/, "").trim())
    .filter(Boolean)
    .slice(0, 3);
}

function stripNextActions(text: string): string {
  const stripped = text.replace(/\n*<next-actions>[\s\S]*?(?:<\/next-actions>|$)/i, "");
  // Only trim when we actually removed a block, so messages without one render
  // byte-for-byte as before.
  return stripped === text ? text : stripped.trimEnd();
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

function FileChangesSummary({ files, projectRoot }: { files: FileChangeEntry[]; projectRoot?: string }) {
  const [expanded, setExpanded] = useState(true);
  if (files.length === 0) return null;

  const summary = `${files.length} file${files.length === 1 ? "" : "s"} changed`;
  const kindIcon: Record<FileChangeKind, string> = { create: "+", update: "~", delete: "-" };
  const kindClass: Record<FileChangeKind, string> = {
    create: "text-green-500",
    update: "text-yellow-500",
    delete: "text-red-500",
  };

  return (
    <Collapsible open={expanded} onOpenChange={setExpanded} className="mt-2 rounded-lg border border-border/50 bg-muted/30 overflow-hidden">
      <CollapsibleTrigger className="flex items-center gap-2 w-full text-left px-3 py-2 text-xs hover:bg-muted/50 transition-colors outline-none">
        {expanded ? <ChevronDown className="h-3 w-3 shrink-0" /> : <ChevronRight className="h-3 w-3 shrink-0" />}
        <FileEdit className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <span className="font-medium">{summary}</span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="px-3 pb-2 space-y-0.5 border-t border-border/30 pt-2">
          {files.map((f, i) => (
            <div key={i} className="flex items-center gap-2 text-[11px] font-mono">
              <span className={cn("w-3 text-center shrink-0", kindClass[f.kind])}>{kindIcon[f.kind]}</span>
              <span className="break-all text-muted-foreground" title={f.path}>
                {toRelativePath(f.path, projectRoot)}
              </span>
            </div>
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

  const effectiveParts = loadedParts || message.parts;
  const groups = useMemo(() => groupParts(effectiveParts), [effectiveParts]);
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
            <div className="rounded-2xl px-4 py-3 bg-muted/50 max-w-full">
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
          </div>
        )}
      </div>
    </div>
  );
});
