import { useMemo, useState, useCallback, useRef, useEffect } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Loader2, Sparkles, X, Copy, Check, FileText, FileCode, FlaskConical, TestTube, ScrollText, GitBranch, ChevronDown, ChevronRight } from "lucide-react";
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from "@/components/ui/collapsible";
import { cn } from "@/lib/utils";
import { ToolCallCard, TODO_TOOLS, parseTodoOutput, parseTodoInput, StandaloneTodoList } from "@/components/chat/ToolCallCard";
import { ThinkingBlock } from "@/components/chat/ThinkingBlock";
import { IssueRow } from "@/plugins/task/IssueRow";
import { messagesApi } from "@/lib/api";
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

interface StepsGroup {
  type: "steps";
  subGroups: (ThinkingGroup | ToolsGroup)[];
  toolCallCount: number;
  thinkingCount: number;
}

type PartGroup = TextGroup | ThinkingGroup | ToolsGroup | ImagesGroup | StepsGroup;

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
// when the run contains more than 1 tool call in total.
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
          (p): p is ToolUsePart => p.type === "tool_use" && !TODO_TOOLS.has((p as ToolUsePart).toolName)
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
        m[p.toolCallId] = { output: p.output, isError: p.isError };
      }
    }
    return m;
  }, [parts]);

  type ToolUsePart = Extract<MessagePart, { type: "tool_use" }>;
  const toolUses = parts.filter((p): p is ToolUsePart => p.type === "tool_use" && !TODO_TOOLS.has((p as ToolUsePart).toolName));

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
  const expanded = !!isStreaming || userExpanded;

  // When streaming ends, reset to collapsed so the group auto-collapses.
  useEffect(() => {
    if (!isStreaming) setUserExpanded(false);
  }, [isStreaming]);

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

function TextBlock({ content }: { content: string }) {
  if (!content) return null;
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      components={{
        pre: ({ children }) => (
          <pre className="bg-muted rounded-md p-3 overflow-x-auto text-[13px] my-2 font-mono">
            {children}
          </pre>
        ),
        code: ({ className, children, ...props }) => {
          const isInline = !className;
          if (isInline) {
            return (
              <code className="bg-muted px-1.5 py-0.5 rounded text-[13px] font-mono" {...props}>
                {children}
              </code>
            );
          }
          return (
            <code className={className} {...props}>
              {children}
            </code>
          );
        },
        a: ({ href, children, ...props }) => (
          <a
            href={href}
            target="_blank"
            rel="noopener noreferrer"
            className="text-primary underline underline-offset-2 decoration-primary/40 hover:decoration-primary transition-colors"
            {...props}
          >
            {children}
          </a>
        ),
      }}
    >
      {content}
    </ReactMarkdown>
  );
}

function CopyButton({ text }: { text: string }) {
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
      className="p-1 rounded-md text-muted-foreground/60 hover:text-foreground hover:bg-muted transition-colors"
      title="Copy"
    >
      {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
    </button>
  );
}

function extractPlainText(parts: MessagePart[]): string {
  return parts
    .filter((p) => p.type === "text")
    .map((p) => (p as { type: "text"; content: string }).content)
    .join("");
}

export function ChatMessage({ message }: ChatMessageProps) {
  const isUser = message.role === "user";
  const [loadedParts, setLoadedParts] = useState<MessagePart[] | null>(null);
  const loadingRef = useRef(false);

  const effectiveParts = loadedParts || message.parts;
  const groups = useMemo(
    () => message.isStreaming ? groupParts(effectiveParts) : collapseSteps(groupParts(effectiveParts)),
    [effectiveParts, message.isStreaming],
  );
  // A thinking group is actively streaming only if it's the very last group in the array
  // (meaning nothing has come after it yet). Once tool calls or text appear after it, it's done.
  const activeThinkingGroupIdx = message.isStreaming && groups.length > 0 && groups[groups.length - 1].type === "thinking"
    ? groups.length - 1
    : -1;
  const plainText = useMemo(() => extractPlainText(effectiveParts), [effectiveParts]);

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

  const userImages = isUser
    ? message.parts.filter((p) => p.type === "image")
    : [];

  const taskRefs = isUser
    ? (message.parts.filter((p) => p.type === "task_ref" && (p as any).task) as Array<{ type: "task_ref"; taskId: string; task: Task }>)
    : [];

  const assetRefs = isUser
    ? message.parts.filter((p) => p.type === "asset_ref") as Array<{ type: "asset_ref"; assetId: string; title: string; assetType: AssetType }>
    : [];

  const workflowRefs = isUser
    ? message.parts.filter((p) => p.type === "workflow_ref") as Array<{ type: "workflow_ref"; workflowId: string; name: string; nodeCount?: number; edgeCount?: number }>
    : [];

  const todoData = useMemo(() => {
    if (isUser) return null;
    type TU = Extract<MessagePart, { type: "tool_use" }>;
    type TR = Extract<MessagePart, { type: "tool_result" }>;
    const todoCalls = effectiveParts.filter((p): p is TU => p.type === "tool_use" && TODO_TOOLS.has((p as TU).toolName));
    if (todoCalls.length === 0) return null;
    const last = todoCalls[todoCalls.length - 1];
    const result = effectiveParts.find((p): p is TR => p.type === "tool_result" && (p as TR).toolCallId === last.toolCallId);
    const todos = result ? parseTodoOutput(result.output) : parseTodoInput(last.input);
    const isPending = last.state === "streaming" || last.state === "running";
    return { todos, isPending };
  }, [isUser, effectiveParts]);

  return (
    <div
      className={cn(
        "group/msg flex flex-row gap-4 w-full",
        isUser && "justify-end",
      )}
    >
      {!isUser && (
        <div className="size-8 flex items-center justify-center rounded-full ring-1 ring-border bg-background shrink-0">
          <Sparkles className="size-4 text-foreground" />
        </div>
      )}

      <div
        className={cn(
          "flex flex-col gap-1 text-sm leading-relaxed",
          isUser
            ? "max-w-[80%]"
            : "w-full",
        )}
      >
        {isUser ? (
          <div className="relative">
            <div className="inline-block max-w-full space-y-2">
              {(plainText || userImages.length > 0) && (
                <div className="rounded-2xl px-4 py-2.5 bg-muted overflow-hidden">
                  {userImages.length > 0 && (
                    <div className="mb-2">
                      <ImageThumbnails parts={userImages} />
                    </div>
                  )}
                  {plainText && (
                    <div className="markdown-content break-words [&>*:last-child]:mb-0">
                      <TextBlock content={plainText} />
                    </div>
                  )}
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
            {plainText && (
              <div className="absolute -left-8 top-1 opacity-0 group-hover/msg:opacity-100 transition-opacity">
                <CopyButton text={plainText} />
              </div>
            )}
          </div>
        ) : (
          <div className="relative">
            <div className="rounded-2xl px-4 py-3 bg-muted/50 max-w-full">
              {groups.length === 0 && message.isStreaming && (
                <div className="flex items-center gap-2 text-muted-foreground">
                  <Loader2 className="size-3 animate-spin" />
                  <span className="text-sm">Thinking...</span>
                </div>
              )}
              {groups.map((group, gi) => {
                switch (group.type) {
                  case "text":
                    return (
                      <div key={gi} className="markdown-content">
                        {group.parts.map((part, pi) =>
                          part.type === "text" ? (
                            <TextBlock key={pi} content={part.content} />
                          ) : null
                        )}
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
                  case "tools":
                    return (
                      <CollapsibleToolSteps
                        key={gi}
                        parts={group.parts}
                      />
                    );
                  case "steps":
                    return (
                      <StepsGroupBlock
                        key={gi}
                        group={group}
                        isStreaming={message.isStreaming}
                        onLoadDetails={message.isStreaming ? undefined : loadDetails}
                      />
                    );
                  default:
                    return null;
                }
              })}
              {todoData && todoData.todos.length > 0 && (
                <StandaloneTodoList todos={todoData.todos} isPending={todoData.isPending} />
              )}
              {message.isStreaming && groups.length > 0 && (
                <span className="inline-flex items-center gap-1 text-muted-foreground mt-1">
                  <Loader2 className="size-3 animate-spin" />
                </span>
              )}
            </div>
            {plainText && !message.isStreaming && (
              <div className="absolute -right-8 top-1 opacity-0 group-hover/msg:opacity-100 transition-opacity">
                <CopyButton text={plainText} />
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
