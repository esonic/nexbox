import { useState, useRef, useEffect, useLayoutEffect, useCallback } from "react";
import { toast } from "sonner";
import { Send, StopCircle, Command, FileText, ChevronDown, Check, X, GitBranch, ImagePlus, Play, Pencil, Workflow, MessageSquare, CircleHelp } from "lucide-react";
import { cn } from "@/lib/utils";
import { agentLabel } from "@/lib/agents";
import { useIsMobile } from "@/hooks/useIsMobile";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import {
  QuestionCard,
  parseAskQuestionInput,
  formatAnswersMessage,
  OTHER_OPTION_ID,
  type QuestionAnswer,
} from "@/components/chat/QuestionCard";
import { TodoPanel } from "@/components/chat/TodoPanel";
import { imageApi, filesApi, promptTemplatesApi, sessionsApi, type DiffResponse, type FileEntry, type PromptTemplate } from "@/lib/api";
import { createPortal } from "react-dom";
import { useAppStore } from "@/stores/app";
import { STATUS_ICONS } from "@/plugins/task/task-icons";
import { normalizeStatus, shortId, STATUS_MAP } from "@/plugins/task/task-config";
import type { Task, ModelInfo } from "@/types";
import type { PickedAsset, PickedWorkflow, InlineComment, RichQueuedMessage } from "@/stores/app";
import type { QueuedMessage } from "@/hooks/useChatSession";
import { ContextUsageIndicator } from "@/components/chat/ContextUsageIndicator";
import { WorktreeSelector, type WorktreeSelectorProps } from "@/components/chat/WorktreeSelector";

export interface AttachedImage {
  file: File;
  preview: string;
  filename?: string;
  url?: string;
}

const DRAFT_KEY_PREFIX = "nexbox-draft:";

export const SLASH_COMMANDS = [
  {
    name: "browser",
    label: "Enable Browser Control",
    description: "Enable browser control, or connect to an existing Chrome",
    prompt: "You now have Chrome DevTools MCP tools available for browser control. " +
      "Use take_snapshot to see the page structure (accessibility tree with UIDs), then interact with elements using their UIDs. " +
      "Typical workflow: navigate_page -> take_snapshot -> click/fill/type_text -> take_screenshot to verify. " +
      "Always call take_snapshot before interacting to get fresh UIDs.",
    features: ["chrome_devtools"],
    needsInput: true,
    inputHint: "What should the browser do, or connect http://127.0.0.1:9222 [task]...",
  },
] as const;

type SlashCommand = typeof SLASH_COMMANDS[number];

const ASSET_TYPE_COLORS: Record<string, string> = {
  requirement_doc: "text-blue-400",
  design_doc: "text-violet-400",
  code_change: "text-emerald-400",
  unit_test: "text-amber-400",
  test_case: "text-rose-400",
};

function QueuedItem({
  item,
  onSendNow,
  onRemove,
  onEdit,
}: {
  item: QueuedMessage;
  onSendNow: () => void;
  onRemove: () => void;
  onEdit: (text: string) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(item.text);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (editing) inputRef.current?.focus();
  }, [editing]);

  const commitEdit = () => {
    const trimmed = draft.trim();
    if (trimmed || (item.images && item.images.length > 0)) {
      onEdit(trimmed);
    }
    setEditing(false);
  };

  return (
    <div className="group/q flex items-center gap-1.5 rounded-lg bg-muted px-3 py-1.5 text-sm">
      {editing ? (
        <input
          ref={inputRef}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.nativeEvent.isComposing) commitEdit();
            if (e.key === "Escape") { setDraft(item.text); setEditing(false); }
          }}
          onBlur={commitEdit}
          className="flex-1 min-w-0 bg-transparent outline-none text-sm"
        />
      ) : (
        <span className="flex-1 min-w-0 truncate text-muted-foreground">
          {item.images && item.images.length > 0 && (
            <span className="text-xs mr-1">[{item.images.length} img]</span>
          )}
          {item.text || <span className="italic text-xs">Image only</span>}
        </span>
      )}
      <div className="flex items-center shrink-0">
        {editing ? (
          <button onClick={commitEdit} className="p-1 rounded hover:bg-accent text-primary">
            <Check className="h-3 w-3" />
          </button>
        ) : (
          <button onClick={() => { setDraft(item.text); setEditing(true); }} className="p-1 rounded hover:bg-accent text-muted-foreground">
            <Pencil className="h-3 w-3" />
          </button>
        )}
        <button onClick={onRemove} className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-destructive">
          <X className="h-3 w-3" />
        </button>
      </div>
    </div>
  );
}

function RichQueuedItem({
  item,
  onSendNow,
  onRemove,
  onEdit,
  canSendNow,
  isFront,
}: {
  item: RichQueuedMessage;
  onSendNow: () => void;
  onRemove: () => void;
  onEdit: (text: string) => void;
  // When true, show an "interject" button that jumps this message into the
  // running turn through the agent's advertised control capability.
  canSendNow: boolean;
  // The front (oldest) queued item is also the target of the Enter shortcut,
  // so its button advertises it.
  isFront: boolean;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(item.text);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (editing) inputRef.current?.focus();
  }, [editing]);

  const commitEdit = () => {
    const trimmed = draft.trim();
    onEdit(trimmed);
    setEditing(false);
  };

  const hasRefs = item.pickedTasks.length > 0 || item.pickedAssets.length > 0 ||
    item.pickedWorkflows.length > 0 || item.pickedComments.length > 0;

  return (
    <div className="rounded-lg bg-muted px-3 py-1.5 text-sm space-y-1">
      <div className="flex items-center gap-1.5">
        {editing ? (
          <input
            ref={inputRef}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.nativeEvent.isComposing) commitEdit();
              if (e.key === "Escape") { setDraft(item.text); setEditing(false); }
            }}
            onBlur={commitEdit}
            className="flex-1 min-w-0 bg-transparent outline-none text-sm"
          />
        ) : (
          <span className="flex-1 min-w-0 truncate text-muted-foreground">
            {item.images && item.images.length > 0 && (
              <span className="text-xs mr-1">[{item.images.length} img]</span>
            )}
            {item.text || (!hasRefs && <span className="italic text-xs">Image only</span>)}
          </span>
        )}
        <div className="flex items-center shrink-0">
          {!editing && canSendNow && (
            <button
              onClick={onSendNow}
              title={isFront ? "Interject now (Enter)" : "Interject now"}
              className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-primary"
            >
              <Play className="h-3 w-3" />
            </button>
          )}
          {editing ? (
            <button onClick={commitEdit} className="p-1 rounded hover:bg-accent text-primary">
              <Check className="h-3 w-3" />
            </button>
          ) : (
            <button onClick={() => { setDraft(item.text); setEditing(true); }} className="p-1 rounded hover:bg-accent text-muted-foreground">
              <Pencil className="h-3 w-3" />
            </button>
          )}
          <button onClick={onRemove} className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-destructive">
            <X className="h-3 w-3" />
          </button>
        </div>
      </div>
      {hasRefs && (
        <div className="flex flex-wrap gap-1">
          {item.pickedWorkflows.map((w) => (
            <span key={w.id} className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium bg-purple-500/10 text-purple-400">
              <Workflow className="h-2.5 w-2.5" />
              <Play className="h-2 w-2" />
              <span className="truncate max-w-[80px]">{w.name}</span>
            </span>
          ))}
          {item.pickedTasks.map((t) => {
            const st = normalizeStatus(t.status);
            const sc = STATUS_MAP.get(st)!;
            const StIcon = STATUS_ICONS[st];
            return (
              <span key={t.id} className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium bg-muted/80 border border-border/60 text-foreground/70">
                <StIcon className={cn("h-2.5 w-2.5", sc.color)} />
                <span className="font-mono text-[9px] text-muted-foreground">{shortId(t.id)}</span>
                <span className="truncate max-w-[80px]">{t.title}</span>
              </span>
            );
          })}
          {item.pickedAssets.map((a) => (
            <span key={a.id} className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium bg-muted/80 border border-border/60 text-foreground/70">
              <FileText className={cn("h-2.5 w-2.5", ASSET_TYPE_COLORS[a.assetType] || "text-muted-foreground")} />
              <span className="truncate max-w-[80px]">{a.title}</span>
            </span>
          ))}
          {item.pickedComments.length > 0 && (
            <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium bg-blue-500/10 text-blue-400">
              <MessageSquare className="h-2.5 w-2.5" />
              {item.pickedComments.length} comment{item.pickedComments.length !== 1 ? "s" : ""}
            </span>
          )}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// AskUserQuestion support — parse + convert to QuestionCard format
// ---------------------------------------------------------------------------

interface ParsedUserQuestion {
  question: string; // full question text (used as answer key)
  header: string;   // short label for display
  multiSelect: boolean;
  options: { label: string; description?: string }[];
}

function parseUserInputQuestions(input: string): ParsedUserQuestion[] | null {
  try {
    const parsed = JSON.parse(input);
    // AskUserQuestion format: {questions: [{question, header, multiSelect, options: [{label, description}]}]}
    const rawQuestions = parsed.questions;
    if (!Array.isArray(rawQuestions) || rawQuestions.length === 0) return null;
    return rawQuestions.map((q: Record<string, unknown>) => ({
      question: (q.question as string) || (q.header as string) || "",
      header: (q.header as string) || (q.prompt as string) || "",
      multiSelect: (q.multiSelect as boolean) ?? (q.allow_multiple as boolean) ?? false,
      options: Array.isArray(q.options)
        ? (q.options as Record<string, unknown>[]).map((o) => ({
            label: (o.label as string) || "",
            description: (o.description as string) || undefined,
          }))
        : [],
    }));
  } catch {
    return null;
  }
}

interface ConvertedQuestionData {
  title?: string;
  questions: { id: string; prompt: string; options: { id: string; label: string }[]; allow_multiple?: boolean }[];
  optionLabelMap: Record<string, string>;
  questionTextMap: Record<string, string>;
}

function convertToQuestionCardFormat(input: string): ConvertedQuestionData | null {
  const parsed = parseUserInputQuestions(input);
  if (!parsed || parsed.length === 0) return null;

  let title: string | undefined;
  try {
    const raw = JSON.parse(input);
    title = raw.question as string | undefined;
  } catch { /* ignore */ }

  const optionLabelMap: Record<string, string> = {};
  const questionTextMap: Record<string, string> = {};

  const questions = parsed.map((q, qi) => {
    const qId = `q_${qi}`;
    questionTextMap[qId] = q.question;
    return {
      id: qId,
      prompt: q.header,
      options: q.options.map((o, oi) => {
        const oId = `q${qi}_o${oi}`;
        optionLabelMap[oId] = o.label;
        return {
          id: oId,
          label: o.description ? `${o.label} — ${o.description}` : o.label,
        };
      }),
      allow_multiple: q.multiSelect || undefined,
    };
  });

  return { title, questions, optionLabelMap, questionTextMap };
}

function UserInputPanel({
  input,
  onSubmit,
  onSkip,
}: {
  input: string;
  onSubmit: (response: string) => void;
  onSkip: () => void;
}) {
  const questionCardData = convertToQuestionCardFormat(input);
  const [freeText, setFreeText] = useState("");
  const inputRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    if (!questionCardData) inputRef.current?.focus();
  }, [questionCardData]);

  // Structured questions → use QuestionCard
  if (questionCardData) {
    return (
      <div className="pb-3 border-b">
        <QuestionCard
          title={questionCardData.title}
          questions={questionCardData.questions}
          onSubmit={(answers: QuestionAnswer[]) => {
            const answersMap: Record<string, string> = {};
            for (const ans of answers) {
              const questionText = questionCardData.questionTextMap[ans.question_id];
              if (!questionText) continue;
              const selectedLabels = ans.selected_options
                .map((optId) =>
                  optId === OTHER_OPTION_ID
                    ? (ans.custom_text || "Other")
                    : (questionCardData.optionLabelMap[optId] || optId)
                )
                .join(", ");
              answersMap[questionText] = selectedLabels;
            }
            onSubmit(JSON.stringify({ answers: answersMap }));
          }}
          onSkip={onSkip}
        />
      </div>
    );
  }

  // Fallback: free text input (for non-structured questions)
  const questionText = (() => {
    try {
      const parsed = JSON.parse(input);
      return (parsed.question as string) || input;
    } catch {
      return input;
    }
  })();

  const handleTextSubmit = () => {
    const trimmed = freeText.trim();
    if (!trimmed) return;
    onSubmit(trimmed);
    setFreeText("");
  };

  return (
    <div className="pb-3 border-b">
      <div className="flex items-start gap-2 px-1">
        <CircleHelp className="h-4 w-4 text-blue-400 mt-0.5 shrink-0" />
        <div className="flex-1 min-w-0 space-y-2">
          <p className="text-sm text-foreground whitespace-pre-wrap">{questionText}</p>
          <div className="flex gap-2">
            <Textarea
              ref={inputRef}
              value={freeText}
              onChange={(e) => setFreeText(e.target.value)}
              onKeyDown={(e) => {
                if (e.nativeEvent.isComposing) return;
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  handleTextSubmit();
                }
              }}
              placeholder="Type your response..."
              className="flex-1 min-h-[36px] max-h-[120px] resize-none text-sm"
              rows={1}
            />
            <div className="flex flex-col gap-1 shrink-0">
              <Button size="sm" onClick={handleTextSubmit} disabled={!freeText.trim()} className="h-7 text-xs">
                <Send className="h-3 w-3 mr-1" />
                Send
              </Button>
              <Button size="sm" variant="ghost" onClick={onSkip} className="h-7 text-xs text-muted-foreground">
                Skip
              </Button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------

interface ChatInputProps {
  sessionId: string;
  isConnected: boolean;
  isStreaming: boolean;
  agentName: string;
  canInterject: boolean;
  modelName: string;
  effort: string;
  availableAgents: string[];
  availableModels: string[];
  availableModelInfos: ModelInfo[];
  sessionHasMessages: boolean;
  canChangeModel: boolean;
  worktree?: WorktreeSelectorProps;
  pickedTasks: Task[];
  pickedAssets: PickedAsset[];
  pickedWorkflows: PickedWorkflow[];
  pendingQuestion: ReturnType<typeof parseAskQuestionInput>;
  pendingTodo?: { todos: Array<{ id: string; content: string; status: string; blockedBy?: string[] }>; isPending: boolean; title?: string } | null;
  pendingUserInput?: { toolUseId: string; toolName: string; input: string } | null;
  onSendToolResponse?: (toolUseId: string, response: string, skip?: boolean) => void;
  queue: QueuedMessage[];
  gitDiffStats?: DiffResponse | null;
  onSend: (message: string, images?: AttachedImage[], features?: string[]) => void;
  onStop: () => void;
  onChangeAgent: (agent: string) => void;
  onChangeModel: (model: string) => void;
  onChangeEffort: (effort: string) => void;
  onRemovePickedTask: (taskId: string) => void;
  onRemovePickedAsset: (assetId: string) => void;
  onRemovePickedWorkflow: (wfId: string) => void;
  onOpenGit?: () => void;
  onQueueSendNow: (id: string) => void;
  onQueueRemove: (id: string) => void;
  onQueueEdit: (id: string, text: string) => void;
  richQueue: RichQueuedMessage[];
  onRichQueueSendNow: (id: string) => void;
  onRichQueueRemove: (id: string) => void;
  onRichQueueEdit: (id: string, text: string) => void;
  pickedComments: InlineComment[];
  onClearPickedComments: () => void;
}

export function ChatInput({
  sessionId,
  isConnected,
  isStreaming,
  agentName,
  canInterject,
  modelName,
  effort,
  availableAgents,
  availableModels,
  availableModelInfos,
  sessionHasMessages,
  canChangeModel,
  worktree,
  pickedTasks,
  pickedAssets,
  pickedWorkflows,
  pendingQuestion,
  pendingTodo,
  pendingUserInput,
  onSendToolResponse,
  queue,
  gitDiffStats,
  onSend,
  onStop,
  onChangeAgent,
  onChangeModel,
  onChangeEffort,
  onRemovePickedTask,
  onRemovePickedAsset,
  onRemovePickedWorkflow,
  onOpenGit,
  onQueueSendNow,
  onQueueRemove,
  onQueueEdit,
  richQueue,
  onRichQueueSendNow,
  onRichQueueRemove,
  onRichQueueEdit,
  pickedComments,
  onClearPickedComments,
}: ChatInputProps) {
  const [input, setInput] = useState(() => {
    try { return localStorage.getItem(DRAFT_KEY_PREFIX + sessionId) || ""; }
    catch { return ""; }
  });
  const [showSlashMenu, setShowSlashMenu] = useState(false);
  const [slashFilter, setSlashFilter] = useState("");
  const [selectedCommandIndex, setSelectedCommandIndex] = useState(0);
  const [activeCommand, setActiveCommand] = useState<SlashCommand | null>(null);
  const [promptTemplates, setPromptTemplates] = useState<PromptTemplate[]>([]);
  const [showAgentMenu, setShowAgentMenu] = useState(false);
  const agentMenuRef = useRef<HTMLDivElement>(null);
  const [showModelMenu, setShowModelMenu] = useState(false);
  const modelMenuRef = useRef<HTMLDivElement>(null);

  // Image attachment state
  const [attachedImages, setAttachedImages] = useState<AttachedImage[]>([]);
  const [uploading, setUploading] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const addImageFiles = useCallback(async (files: File[]) => {
    const imageFiles = files.filter(f => f.type.startsWith("image/"));
    if (imageFiles.length === 0) return;

    const newImages: AttachedImage[] = imageFiles.map(f => ({
      file: f,
      preview: URL.createObjectURL(f),
    }));
    setAttachedImages(prev => [...prev, ...newImages]);

    setUploading(true);
    try {
      for (const img of newImages) {
        const result = await imageApi.upload(sessionId, img.file);
        setAttachedImages(prev =>
          prev.map(i => i.preview === img.preview ? { ...i, filename: result.filename, url: result.url } : i)
        );
      }
    } catch (e) {
      console.error("Failed to upload image:", e);
      toast.error("Failed to upload image");
    } finally {
      setUploading(false);
    }
  }, [sessionId]);

  const removeImage = useCallback((preview: string) => {
    setAttachedImages(prev => {
      const removed = prev.find(i => i.preview === preview);
      if (removed) URL.revokeObjectURL(removed.preview);
      return prev.filter(i => i.preview !== preview);
    });
  }, []);

  const saveDraft = useCallback((value: string) => {
    try {
      if (value) localStorage.setItem(DRAFT_KEY_PREFIX + sessionId, value);
      else localStorage.removeItem(DRAFT_KEY_PREFIX + sessionId);
    } catch { /* ignore */ }
  }, [sessionId]);

  useEffect(() => {
    try { setInput(localStorage.getItem(DRAFT_KEY_PREFIX + sessionId) || ""); }
    catch { setInput(""); }
  }, [sessionId]);

  const currentProjectId = useAppStore((s) => s.currentSession?.project_id);

  // @ file picker state
  const [filePickerOpen, setFilePickerOpen] = useState(false);
  const [filePickerQuery, setFilePickerQuery] = useState("");
  const [filePickerAnchor, setFilePickerAnchor] = useState(0);
  const [filePickerFiles, setFilePickerFiles] = useState<FileEntry[]>([]);
  const [filePickerSelected, setFilePickerSelected] = useState(0);

  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const filePickerMenuRef = useRef<HTMLDivElement>(null);
  const slashMenuRef = useRef<HTMLDivElement>(null);

  // Fill the composer when a "Next" quick-reply button is clicked in this
  // session's chat (see NextActions in ChatMessage). It only fills the box —
  // the user still chooses to send or edit.
  useEffect(() => {
    const handler = (e: Event) => {
      const detail = (e as CustomEvent<{ sessionId: string; text: string }>).detail;
      if (!detail || detail.sessionId !== sessionId || !detail.text) return;
      setInput(detail.text);
      saveDraft(detail.text);
      requestAnimationFrame(() => textareaRef.current?.focus());
    };
    window.addEventListener("nexbox:set-composer", handler);
    return () => window.removeEventListener("nexbox:set-composer", handler);
  }, [sessionId, saveDraft]);

  const isMobile = useIsMobile();
  // Auto-growing textarea: starts at 1 line on mobile / 3 lines on desktop
  // and grows with content up to 20 lines, after which the textarea scrolls
  // internally. The min/max include vertical padding (py-2 = 16px) on top of
  // text-sm's 20px line-height.
  const TEXTAREA_LINE_HEIGHT = 20;
  const TEXTAREA_VERTICAL_PADDING = 16;
  const textareaMinHeight =
    TEXTAREA_LINE_HEIGHT * (isMobile ? 1 : 3) + TEXTAREA_VERTICAL_PADDING;
  const textareaMaxHeight = TEXTAREA_LINE_HEIGHT * 19 + TEXTAREA_VERTICAL_PADDING;

  const resizeTextarea = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    const next = Math.min(textareaMaxHeight, Math.max(textareaMinHeight, el.scrollHeight));
    el.style.height = `${next}px`;
  }, [textareaMinHeight, textareaMaxHeight]);

  useLayoutEffect(() => {
    resizeTextarea();
  }, [input, resizeTextarea]);

  const filteredCommands = SLASH_COMMANDS.filter((cmd) =>
    cmd.name.toLowerCase().includes(slashFilter.toLowerCase()) ||
    cmd.label.toLowerCase().includes(slashFilter.toLowerCase())
  );
  const filteredTemplates = promptTemplates.filter((t) => {
    const q = slashFilter.toLowerCase();
    return t.name.toLowerCase().includes(q) || t.content.toLowerCase().includes(q);
  });
  const slashItemCount = filteredCommands.length + filteredTemplates.length;

  // Refetch on every open so edits made in Settings show up immediately.
  useEffect(() => {
    if (!showSlashMenu) return;
    promptTemplatesApi.list().then(setPromptTemplates).catch(() => {});
  }, [showSlashMenu]);

  const filteredPickerFiles = filePickerFiles
    .filter((f) => !filePickerQuery || f.path.toLowerCase().includes(filePickerQuery.toLowerCase()))
    .slice(0, 10);

  // Load file list lazily when picker first opens.
  useEffect(() => {
    if (!filePickerOpen || !currentProjectId) return;
    if (filePickerFiles.length > 0) return;

    filesApi.flat(currentProjectId, sessionId)
      .then((entries) => setFilePickerFiles(entries || []))
      .catch(() => {});
  }, [filePickerOpen, currentProjectId, sessionId, filePickerFiles.length]);

  useEffect(() => {
    setFilePickerFiles([]);
  }, [currentProjectId, sessionId]);

  useEffect(() => {
    if (!filePickerOpen) return;
    const handler = (e: MouseEvent) => {
      if (filePickerMenuRef.current?.contains(e.target as Node)) return;
      if (textareaRef.current?.contains(e.target as Node)) return;
      setFilePickerOpen(false);
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [filePickerOpen]);

  // Close agent menu on outside click
  useEffect(() => {
    if (!showAgentMenu) return;
    const handler = (e: MouseEvent) => {
      if (agentMenuRef.current && !agentMenuRef.current.contains(e.target as Node)) {
        setShowAgentMenu(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [showAgentMenu]);

  useEffect(() => {
    if (!showModelMenu) return;
    const handler = (e: MouseEvent) => {
      if (modelMenuRef.current && !modelMenuRef.current.contains(e.target as Node)) {
        setShowModelMenu(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [showModelMenu]);

  // Whether the composer has anything to send/queue right now.
  const hasComposerContent = () =>
    !!(input.trim() || attachedImages.length > 0 || pickedTasks.length > 0 || pickedAssets.length > 0 || pickedWorkflows.length > 0 || pickedComments.length > 0 || activeCommand);

  const canInterjectFront = canInterject && richQueue.length > 0;

  const browserConfigPending = useRef(false);
  const [savingBrowserConfig, setSavingBrowserConfig] = useState(false);

  const handleSend = async () => {
    if (browserConfigPending.current) return;
    const hasContent = hasComposerContent();
    if (!hasContent) return;
    if (attachedImages.length > 0 && !attachedImages.every(i => i.filename)) return;

    let message = input;
    let features: string[] | undefined;

    const inlineBrowser = !activeCommand && /^\/browser(?:\s|$)/.test(input.trim());
    const command = activeCommand ?? (inlineBrowser ? SLASH_COMMANDS[0] : null);
    if (command) {
      let userInput = inlineBrowser ? input.trim().replace(/^\/browser\s*/, "") : input.trim();
      let browserPrompt: string = command.prompt;
      if (/^connect(?:\s|$)/.test(userInput)) {
        const match = /^connect\s+(\S+)(?:\s+([\s\S]*))?$/.exec(userInput);
        if (!match) {
          toast.error("Use /browser connect http://127.0.0.1:9222 [task]");
          return;
        }
        const browserURL = match[1];
        browserConfigPending.current = true;
        setSavingBrowserConfig(true);
        try {
          await sessionsApi.update(sessionId, { chrome_browser_url: browserURL });
        } catch (err) {
          toast.error(err instanceof Error ? err.message : "Failed to save Chrome address");
          return;
        } finally {
          browserConfigPending.current = false;
          setSavingBrowserConfig(false);
        }
        userInput = match[2]?.trim() ?? "List the existing tabs and wait for my next instruction.";
        browserPrompt += `\nConnect to my externally managed Chrome at ${browserURL}. Start by listing existing tabs. Chrome's lifecycle is managed by me.`;
      }
      message = userInput
        ? `${browserPrompt}\n\nUser request: ${userInput}`
        : browserPrompt;
      features = [...command.features];
      setActiveCommand(null);
    }

    onSend(message, attachedImages.length > 0 ? attachedImages : undefined, features);
    setInput("");
    saveDraft("");
    setAttachedImages([]);
  };

  const handleInputChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const value = e.target.value;
    const cursorPos = e.target.selectionStart || 0;
    setInput(value);
    saveDraft(value);

    // Slash command detection
    if (value.startsWith("/")) {
      setShowSlashMenu(true);
      setSlashFilter(value.slice(1));
      setSelectedCommandIndex(0);
      setFilePickerOpen(false);
    } else {
      setShowSlashMenu(false);
      setSlashFilter("");
    }

    // @ file picker detection
    const textBeforeCursor = value.slice(0, cursorPos);
    if (!currentProjectId) {
      setFilePickerOpen(false);
      return;
    }
    const atIdx = textBeforeCursor.lastIndexOf("@");
    if (atIdx !== -1 && !/\s/.test(textBeforeCursor.slice(atIdx + 1))) {
      setFilePickerQuery(textBeforeCursor.slice(atIdx + 1));
      setFilePickerAnchor(atIdx);
      setFilePickerOpen(true);
      setFilePickerSelected(0);
      return;
    }
    setFilePickerOpen(false);
  };

  const executeSlashCommand = (cmd: SlashCommand) => {
    setShowSlashMenu(false);
    setSlashFilter("");
    setInput("");

    if ("needsInput" in cmd && cmd.needsInput) {
      setActiveCommand(cmd);
      setTimeout(() => textareaRef.current?.focus(), 0);
      return;
    }

    const features = "features" in cmd ? ([...cmd.features] as string[]) : undefined;
    onSend(cmd.prompt, undefined, features);
  };

  const applyTemplate = (tpl: PromptTemplate) => {
    setShowSlashMenu(false);
    setSlashFilter("");
    setInput(tpl.content);
    saveDraft(tpl.content);
    setTimeout(() => {
      const el = textareaRef.current;
      if (!el) return;
      el.focus();
      el.setSelectionRange(tpl.content.length, tpl.content.length);
    }, 0);
  };

  const selectSlashItem = (index: number) => {
    if (index < filteredCommands.length) executeSlashCommand(filteredCommands[index]);
    else applyTemplate(filteredTemplates[index - filteredCommands.length]);
  };

  useEffect(() => {
    if (!showSlashMenu || slashItemCount === 0) return;
    const container = slashMenuRef.current;
    if (!container) return;
    const item = container.querySelector(`[data-cmd-index="${selectedCommandIndex}"]`);
    if (item) item.scrollIntoView({ block: "nearest" });
  }, [selectedCommandIndex, showSlashMenu, slashItemCount]);

  const insertFileRef = useCallback((file: FileEntry) => {
    const el = textareaRef.current;
    const cursor = el?.selectionStart ?? (filePickerAnchor + filePickerQuery.length + 1);
    const before = input.slice(0, filePickerAnchor);
    const after = input.slice(cursor);
    const ref = file.path || file.name;
    const newInput = `${before}@${ref} ${after}`;
    setInput(newInput);
    saveDraft(newInput);
    setFilePickerOpen(false);

    setTimeout(() => {
      if (!el) return;
      el.focus();
      const pos = before.length + ref.length + 2;
      el.setSelectionRange(pos, pos);
    }, 0);
  }, [input, filePickerAnchor, filePickerQuery.length, saveDraft]);

  /** Generic handler for popup-menu keyboard navigation (ArrowUp/Down, Enter/Tab, Escape). */
  const navigateMenu = (
    e: React.KeyboardEvent,
    count: number,
    selectedIndex: number,
    setIndex: React.Dispatch<React.SetStateAction<number>>,
    onSelect: () => void,
    onDismiss: () => void,
  ): boolean => {
    switch (e.key) {
      case "ArrowDown": e.preventDefault(); setIndex(selectedIndex < count - 1 ? selectedIndex + 1 : 0); return true;
      case "ArrowUp":   e.preventDefault(); setIndex(selectedIndex > 0 ? selectedIndex - 1 : count - 1); return true;
      case "Enter":
      case "Tab":        e.preventDefault(); onSelect(); return true;
      case "Escape":     e.preventDefault(); onDismiss(); return true;
      default: return false;
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.nativeEvent.isComposing) return;

    if (filePickerOpen && filteredPickerFiles.length > 0) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setFilePickerSelected((i) => Math.min(i + 1, filteredPickerFiles.length - 1));
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        setFilePickerSelected((i) => Math.max(i - 1, 0));
        return;
      }
      if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault();
        insertFileRef(filteredPickerFiles[filePickerSelected]);
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        setFilePickerOpen(false);
        return;
      }
    }

    if (showSlashMenu && slashItemCount > 0) {
      if (navigateMenu(e, slashItemCount, selectedCommandIndex, setSelectedCommandIndex,
        () => selectSlashItem(selectedCommandIndex),
        () => { setShowSlashMenu(false); setInput(""); },
      )) return;
    }
    if (e.key === "Escape" && activeCommand && !showSlashMenu && !filePickerOpen) {
      e.preventDefault();
      setActiveCommand(null);
      return;
    }
    // On mobile, Enter inserts a newline (matches WeChat/IME behavior). Use
    // the Send button to submit. On desktop, Enter sends and Shift+Enter is
    // a newline.
    if (!isMobile && e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      // With an empty composer, Enter acts as the "interject" shortcut: send the
      // front (oldest) queued message into the active reply. With
      // content, it sends/queues that content as usual.
      if (!hasComposerContent() && canInterjectFront) {
        onRichQueueSendNow(richQueue[0].id);
        return;
      }
      handleSend();
    }
  };

  const handleAgentChange = (agent: string) => {
    setShowAgentMenu(false);
    onChangeAgent(agent);
  };

  const handleModelChange = (model: string) => {
    setShowModelMenu(false);
    onChangeModel(model);
  };

  const handleEffortChange = (lvl: string) => {
    setShowModelMenu(false);
    onChangeEffort(lvl);
  };

  const selectedModel = modelName;
  const currentModelInfo = availableModelInfos.find((m) => m.id === selectedModel);
  const displayModelLabel = (info?: ModelInfo, fallback?: string) => {
    const raw = info?.label || fallback || "";
    const hay = `${info?.id || ""} ${raw}`.toLowerCase();
    if (hay.includes("haiku")) return "Haiku";
    if (hay.includes("opus")) return "Opus";
    if (hay.includes("sonnet")) return "Sonnet";
    return raw || "Model";
  };
  const currentModelLabel = selectedModel ? displayModelLabel(currentModelInfo, selectedModel) : "Select model";
  const visibleModels = availableModels.filter((m) => m.toLowerCase() !== "default");
  const supportedEfforts = currentModelInfo?.supported_efforts || [];
  const currentEffort = effort || currentModelInfo?.default_effort || "";
  const effortLabel = currentEffort ? currentEffort[0].toUpperCase() + currentEffort.slice(1) : "";

  return (
    <div className="shrink-0">
      <div className="max-w-4xl mx-auto px-2 sm:px-4 pb-2 sm:pb-3">
        {pendingTodo && (
          <TodoPanel todos={pendingTodo.todos} title={pendingTodo.title} />
        )}

        {/* Question panel */}
        {pendingQuestion && (
          <div className="pb-3 border-b">
            <QuestionCard
              title={pendingQuestion.title}
              questions={pendingQuestion.questions}
              onSubmit={(answers: QuestionAnswer[]) => {
                const msg = formatAnswersMessage(pendingQuestion.questions, answers);
                onSend(msg);
              }}
              onSkip={() => {
                onSend("Skip this question, please continue.");
              }}
            />
          </div>
        )}

        {/* Interactive user input request (Claude CLI AskUserQuestion) */}
        {pendingUserInput && onSendToolResponse && (
          <UserInputPanel
            input={pendingUserInput.input}
            onSubmit={(response) => onSendToolResponse(pendingUserInput.toolUseId, response)}
            onSkip={() => onSendToolResponse(pendingUserInput.toolUseId, "", true)}
          />
        )}

        {/* Unified input box */}
        <div
          className="relative rounded-xl border border-input bg-background shadow-sm focus-within:ring-2 focus-within:ring-ring focus-within:ring-offset-2 ring-offset-background transition-shadow"
          onDragOver={(e) => { e.preventDefault(); e.stopPropagation(); }}
          onDrop={(e) => {
            e.preventDefault();
            e.stopPropagation();
            const files = Array.from(e.dataTransfer.files);
            if (files.length > 0) addImageFiles(files);
          }}
        >
          {/* Git diff bar */}
          {gitDiffStats && (gitDiffStats.insertions > 0 || gitDiffStats.deletions > 0 || gitDiffStats.total_files > 0) && (
            <div className="flex items-center justify-between px-3 py-1.5 border-b border-border/60 text-xs">
              <div className="flex items-center gap-2 text-muted-foreground">
                <GitBranch className="h-3 w-3" />
                {gitDiffStats.repository_count && gitDiffStats.repository_count > 1 && (
                  <span>{gitDiffStats.repository_count} repositories</span>
                )}
                <span>{gitDiffStats.total_files} file{gitDiffStats.total_files !== 1 ? "s" : ""} changed</span>
                {gitDiffStats.stats_included && (
                  <>
                    <span className="text-green-500 font-medium">+{gitDiffStats.insertions}</span>
                    <span className="text-red-500 font-medium">-{gitDiffStats.deletions}</span>
                  </>
                )}
              </div>
              {onOpenGit && (
                <button
                  onClick={onOpenGit}
                  className="flex items-center gap-1 px-2 py-0.5 rounded-md text-xs font-medium text-primary hover:bg-primary/10 transition-colors"
                >
                  <GitBranch className="h-3 w-3" />
                  Review
                </button>
              )}
            </div>
          )}
          {/* Queued messages (rich client-side + legacy server-side) */}
          {(richQueue.length > 0 || queue.length > 0) && (
            <div className="px-3 pt-3 space-y-1">
              {richQueue.map((item, i) => (
                <RichQueuedItem
                  key={item.id}
                  item={item}
                  canSendNow={canInterject}
                  isFront={i === 0}
                  onSendNow={() => onRichQueueSendNow(item.id)}
                  onRemove={() => onRichQueueRemove(item.id)}
                  onEdit={(text) => onRichQueueEdit(item.id, text)}
                />
              ))}
              {queue.map((item) => (
                <QueuedItem
                  key={item.id}
                  item={item}
                  onSendNow={() => onQueueSendNow(item.id)}
                  onRemove={() => onQueueRemove(item.id)}
                  onEdit={(text) => onQueueEdit(item.id, text)}
                />
              ))}
            </div>
          )}
          {/* Slash command menu */}
          {showSlashMenu && slashItemCount > 0 && (
            <div ref={slashMenuRef} className="absolute bottom-full left-0 right-0 mb-2 bg-background border rounded-lg shadow-lg overflow-hidden z-10 max-h-64 overflow-y-auto">
              {filteredCommands.length > 0 && (
                <div className="p-2 text-xs text-muted-foreground border-b">
                  <Command className="inline h-3 w-3 mr-1" />
                  Commands
                </div>
              )}
              {filteredCommands.map((cmd, index) => (
                <button
                  key={cmd.name}
                  data-cmd-index={index}
                  onClick={() => executeSlashCommand(cmd)}
                  className={`w-full text-left px-3 py-2 flex items-start gap-3 hover:bg-accent transition-colors ${
                    index === selectedCommandIndex ? "bg-accent" : ""
                  }`}
                >
                  <span className="font-mono text-sm text-primary w-24 shrink-0">/{cmd.name}</span>
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-medium">{cmd.label}</div>
                    <div className="text-xs text-muted-foreground truncate">{cmd.description}</div>
                  </div>
                </button>
              ))}
              {filteredTemplates.length > 0 && (
                <div className="p-2 text-xs text-muted-foreground border-b border-t">
                  <FileText className="inline h-3 w-3 mr-1" />
                  Prompt templates
                </div>
              )}
              {filteredTemplates.map((tpl, i) => {
                const index = filteredCommands.length + i;
                return (
                  <button
                    key={tpl.name}
                    data-cmd-index={index}
                    onClick={() => applyTemplate(tpl)}
                    className={`w-full text-left px-3 py-2 hover:bg-accent transition-colors ${
                      index === selectedCommandIndex ? "bg-accent" : ""
                    }`}
                  >
                    <div className="text-sm font-medium truncate">{tpl.name}</div>
                    <div className="text-xs text-muted-foreground truncate">
                      {tpl.content.slice(0, 200).replace(/\s+/g, " ")}
                    </div>
                  </button>
                );
              })}
            </div>
          )}
          {/* Active command chip */}
          {activeCommand && (
            <div className="flex items-center gap-1.5 px-3 pt-2">
              <span
                className={cn(
                  "inline-flex items-center gap-1.5 pl-2 pr-1 py-0.5 rounded-md text-xs font-medium transition-colors",
                  "bg-primary/10 border border-primary/25 text-primary"
                )}
              >
                <Command className="h-3 w-3 shrink-0" />
                <span className="font-mono">/{activeCommand.name}</span>
                <span className="text-muted-foreground font-normal">{activeCommand.label}</span>
                <button
                  onClick={() => setActiveCommand(null)}
                  className="ml-0.5 p-0.5 rounded hover:bg-primary/15 text-primary/50 hover:text-primary transition-colors"
                >
                  <X className="h-2.5 w-2.5" />
                </button>
              </span>
            </div>
          )}

          {/* Picked task, asset & workflow badges */}
          {(pickedTasks.length > 0 || pickedAssets.length > 0 || pickedWorkflows.length > 0 || pickedComments.length > 0) && (
            <div className="flex flex-wrap gap-1.5 px-3 pt-2">
              {pickedWorkflows.map((w) => (
                <span
                  key={w.id}
                  className={cn(
                    "inline-flex items-center gap-1 px-2 py-0.5 rounded-md border text-xs font-medium transition-colors",
                    "bg-purple-500/10 border-purple-500/30 text-purple-400 hover:bg-purple-500/15"
                  )}
                  title={`Run workflow: ${w.name}`}
                >
                  <Workflow className="h-3 w-3 shrink-0" />
                  <Play className="h-2.5 w-2.5 shrink-0" />
                  <span className="truncate max-w-[120px]">{w.name}</span>
                  <button
                    onClick={() => onRemovePickedWorkflow(w.id)}
                    className="ml-0.5 p-0.5 rounded hover:bg-accent text-muted-foreground/50 hover:text-foreground transition-colors"
                  >
                    <X className="h-2.5 w-2.5" />
                  </button>
                </span>
              ))}
              {pickedTasks.map((t) => {
                const st = normalizeStatus(t.status);
                const sc = STATUS_MAP.get(st)!;
                const StIcon = STATUS_ICONS[st];
                return (
                  <span
                    key={t.id}
                    className={cn(
                      "inline-flex items-center gap-1 px-2 py-0.5 rounded-md border text-xs font-medium transition-colors",
                      "bg-muted/50 border-border/60 text-foreground/80 hover:bg-muted"
                    )}
                    title={t.title}
                  >
                    <StIcon className={cn("h-3 w-3 shrink-0", sc.color)} />
                    <span className="font-mono text-[10px] text-muted-foreground">{shortId(t.id)}</span>
                    <span className="truncate max-w-[120px]">{t.title}</span>
                    <button
                      onClick={() => onRemovePickedTask(t.id)}
                      className="ml-0.5 p-0.5 rounded hover:bg-accent text-muted-foreground/50 hover:text-foreground transition-colors"
                    >
                      <X className="h-2.5 w-2.5" />
                    </button>
                  </span>
                );
              })}
              {pickedAssets.map((a) => (
                <span
                  key={a.id}
                  className={cn(
                    "inline-flex items-center gap-1 px-2 py-0.5 rounded-md border text-xs font-medium transition-colors",
                    "bg-muted/50 border-border/60 text-foreground/80 hover:bg-muted"
                  )}
                  title={a.title}
                >
                  <FileText className={cn("h-3 w-3 shrink-0", ASSET_TYPE_COLORS[a.assetType] || "text-muted-foreground")} />
                  <span className="font-mono text-[10px] text-muted-foreground">{shortId(a.id)}</span>
                  <span className="truncate max-w-[120px]">{a.title}</span>
                  <button
                    onClick={() => onRemovePickedAsset(a.id)}
                    className="ml-0.5 p-0.5 rounded hover:bg-accent text-muted-foreground/50 hover:text-foreground transition-colors"
                  >
                    <X className="h-2.5 w-2.5" />
                  </button>
                </span>
              ))}
              {pickedComments.length > 0 && (
                <span
                  className={cn(
                    "inline-flex items-center gap-1 px-2 py-0.5 rounded-md border text-xs font-medium transition-colors",
                    "bg-blue-500/10 border-blue-500/25 text-blue-400 hover:bg-blue-500/15"
                  )}
                >
                  <MessageSquare className="h-3 w-3 shrink-0" />
                  <span>{pickedComments.length} comment{pickedComments.length !== 1 ? "s" : ""}</span>
                  <button
                    onClick={onClearPickedComments}
                    className="ml-0.5 p-0.5 rounded hover:bg-accent text-blue-400/50 hover:text-blue-400 transition-colors"
                    title="Clear all comments"
                  >
                    <X className="h-2.5 w-2.5" />
                  </button>
                </span>
              )}
            </div>
          )}

          {/* Attached images preview */}
          {attachedImages.length > 0 && (
            <div className="flex gap-2 px-3 pt-3 flex-wrap">
              {attachedImages.map((img) => (
                <div key={img.preview} className="relative group/img">
                  <img src={img.preview} alt="" className="h-16 w-16 rounded-lg object-cover border" />
                  {!img.filename && (
                    <div className="absolute inset-0 flex items-center justify-center bg-background/60 rounded-lg">
                      <div className="h-3 w-3 border-2 border-primary border-t-transparent rounded-full animate-spin" />
                    </div>
                  )}
                  <button
                    onClick={() => removeImage(img.preview)}
                    className="absolute -top-1.5 -right-1.5 h-4 w-4 rounded-full bg-destructive text-destructive-foreground flex items-center justify-center opacity-0 group-hover/img:opacity-100 transition-opacity"
                  >
                    <X className="h-2.5 w-2.5" />
                  </button>
                </div>
              ))}
            </div>
          )}

          {/* Hidden file input for image picker */}
          <input
            ref={fileInputRef}
            type="file"
            accept="image/*"
            multiple
            className="hidden"
            onChange={(e) => {
              const files = Array.from(e.target.files || []);
              if (files.length > 0) addImageFiles(files);
              e.target.value = "";
            }}
          />

          {/* Textarea */}
          <Textarea
            ref={textareaRef}
            value={input}
            onChange={handleInputChange}
            onKeyDown={handleKeyDown}
            onPaste={(e) => {
              const items = Array.from(e.clipboardData.items);
              const imageFiles = items
                .filter(item => item.type.startsWith("image/"))
                .map(item => item.getAsFile())
                .filter((f): f is File => f !== null);
              if (imageFiles.length > 0) {
                e.preventDefault();
                addImageFiles(imageFiles);
              }
            }}
            placeholder={!isConnected ? "Connecting..." : activeCommand && "inputHint" in activeCommand ? activeCommand.inputHint : "/ for commands, @ for files..."}
            disabled={!isConnected || savingBrowserConfig}
            className="resize-none border-0 bg-transparent shadow-none focus-visible:ring-0 focus-visible:ring-offset-0 min-h-0 overflow-y-auto text-[15px]"
            rows={1}
          />

          {/* Bottom toolbar */}
          <div className="flex items-center justify-between gap-2 px-2 sm:px-3 pb-2 pt-1">
            <div className="flex items-center gap-1.5 sm:gap-2 flex-wrap min-w-0">
              <span
                className={`inline-block h-2 w-2 rounded-full shrink-0 ${isConnected ? "bg-green-500" : "bg-red-500"}`}
                title={isConnected ? "Connected" : "Disconnected"}
                aria-label={isConnected ? "Connected" : "Disconnected"}
              />
              <span className="sr-only">{isConnected ? "Connected" : "Disconnected"}</span>
              {worktree && (
                <>
                  <span className="text-muted-foreground/40 text-xs hidden sm:inline">·</span>
                  <WorktreeSelector {...worktree} />
                </>
              )}
              {agentName && (
                <>
                  <span className="text-muted-foreground/40 text-xs hidden sm:inline">·</span>
                  <div className="relative shrink-0" ref={agentMenuRef}>
                    <button
                      onClick={() => {
                        if (!sessionHasMessages && availableAgents.length > 1) setShowAgentMenu((v) => !v);
                      }}
                      className={cn(
                        "text-[10px] font-medium px-1.5 py-0.5 rounded-sm flex items-center gap-1 whitespace-nowrap transition-colors",
                        sessionHasMessages
                          ? "bg-blue-500/15 text-blue-400 cursor-default"
                          : "bg-blue-500/15 text-blue-400 hover:bg-blue-500/25 cursor-pointer"
                      )}
                      title={sessionHasMessages ? "Agent locked after first message" : "Click to change agent"}
                    >
                      {agentLabel(agentName)}
                      {!sessionHasMessages && availableAgents.length > 1 && (
                        <ChevronDown className="h-2.5 w-2.5" />
                      )}
                    </button>

                    {showAgentMenu && (
                      <div className="absolute left-0 bottom-full z-50 mb-1 w-40 bg-popover border border-border rounded-lg shadow-lg overflow-hidden">
                        {availableAgents.map((ag) => (
                          <button
                            key={ag}
                            onClick={() => handleAgentChange(ag)}
                            className={cn(
                              "w-full flex items-center justify-between px-3 py-1.5 text-xs hover:bg-accent transition-colors",
                              agentName === ag && "bg-accent/50 font-medium"
                            )}
                          >
                            <span>{agentLabel(ag)}</span>
                            {agentName === ag && <Check className="h-3 w-3 text-primary" />}
                          </button>
                        ))}
                      </div>
                    )}
                  </div>
                </>
              )}
              {availableModels.length > 0 && (
                <>
                  <span className="text-muted-foreground/40 text-xs hidden sm:inline">·</span>
                  <div className="relative shrink-0" ref={modelMenuRef}>
                    <button
                      onClick={() => canChangeModel && setShowModelMenu((v) => !v)}
                      className={cn(
                        "max-w-[180px] text-[10px] font-medium px-1.5 py-0.5 rounded-sm flex items-center gap-1 transition-colors bg-violet-500/15 text-violet-400",
                        canChangeModel ? "hover:bg-violet-500/25 cursor-pointer" : "opacity-60 cursor-not-allowed"
                      )}
                      title={canChangeModel ? "Click to change model" : "This agent only allows choosing a model before the conversation starts"}
                    >
                      <span className="min-w-0 truncate">{currentModelLabel}</span>
                      {effortLabel && (
                        <span className="text-violet-400/60 font-normal">· {effortLabel}</span>
                      )}
                      <ChevronDown className="h-2.5 w-2.5" />
                    </button>

                    {showModelMenu && canChangeModel && (
                      <div className="absolute left-0 bottom-full z-50 mb-1 w-64 bg-popover border border-border rounded-lg shadow-lg overflow-hidden">
                        <div className="px-3 pt-1.5 pb-0.5 text-[10px] uppercase tracking-wider text-muted-foreground/60">
                          Models
                        </div>
                        <div className="max-h-64 overflow-y-auto">
                        {visibleModels.map((model) => {
                          const info = availableModelInfos.find((m) => m.id === model);
                          return (
                            <button
                              key={model}
                              onClick={() => handleModelChange(model)}
                              className={cn(
                                "w-full flex flex-col items-start gap-0 px-3 py-1.5 text-xs hover:bg-accent transition-colors",
                                selectedModel === model && "bg-accent/50 font-medium"
                              )}
                              title={info?.description || model}
                            >
                              <div className="w-full flex items-center justify-between">
                                <span className="truncate">{displayModelLabel(info, model)}</span>
                                {selectedModel === model && <Check className="h-3 w-3 text-primary" />}
                              </div>
                            </button>
                          );
                        })}
                        </div>
                        {supportedEfforts.length > 0 && (
                          <>
                            <div className="border-t border-border my-1" />
                            <div className="px-3 pt-1.5 pb-0.5 text-[10px] uppercase tracking-wider text-muted-foreground/60">
                              Effort
                            </div>
                            {supportedEfforts.map((lvl) => (
                              <button
                                key={lvl}
                                onClick={() => handleEffortChange(lvl)}
                                className={cn(
                                  "w-full flex items-center justify-between px-3 py-1.5 text-xs hover:bg-accent transition-colors",
                                  currentEffort === lvl && "bg-accent/50 font-medium"
                                )}
                              >
                                <span className="truncate">{lvl[0].toUpperCase() + lvl.slice(1)}</span>
                                {currentEffort === lvl && <Check className="h-3 w-3 text-primary" />}
                              </button>
                            ))}
                          </>
                        )}
                      </div>
                    )}
                  </div>
                  <div className="flex shrink-0">
                    <ContextUsageIndicator
                      sessionId={sessionId}
                      fallbackContextWindow={currentModelInfo?.context_window || 0}
                    />
                  </div>
                </>
              )}
            </div>
            <div className="flex items-center gap-1 shrink-0">
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7 rounded-md shrink-0 text-muted-foreground hover:text-foreground"
                onClick={() => fileInputRef.current?.click()}
                disabled={!isConnected || uploading}
                title="Attach image"
              >
                <ImagePlus className="h-3.5 w-3.5" />
              </Button>
              {(() => {
                const hasContentToSend = !!(input.trim() || activeCommand || pickedTasks.length > 0 || pickedAssets.length > 0 || pickedWorkflows.length > 0 || pickedComments.length > 0 || attachedImages.length > 0);
                if (isStreaming && !hasContentToSend) {
                  return (
                    <Button
                      onClick={onStop}
                      size="icon"
                      variant="outline"
                      className="h-7 w-7 rounded-md shrink-0"
                      title="Stop"
                    >
                      <StopCircle className="h-3.5 w-3.5" />
                    </Button>
                  );
                }
                return (
                  <Button
                    onClick={handleSend}
                    disabled={!isConnected || uploading || !hasContentToSend}
                    size="icon"
                    className="h-7 w-7 rounded-md shrink-0"
                    title={isStreaming ? "Queue message" : "Send message"}
                  >
                    <Send className="h-3.5 w-3.5" />
                  </Button>
                );
              })()}
            </div>
          </div>
        </div>
      </div>
      {filePickerOpen && filteredPickerFiles.length > 0 && textareaRef.current && createPortal(
        <div
          ref={filePickerMenuRef}
          style={{
            position: "fixed",
            bottom: window.innerHeight - textareaRef.current.getBoundingClientRect().top + 4,
            left: textareaRef.current.getBoundingClientRect().left,
            width: textareaRef.current.getBoundingClientRect().width,
            zIndex: 9999,
          }}
          className="bg-popover border rounded-lg shadow-lg overflow-hidden max-h-52 overflow-y-auto"
        >
          {filteredPickerFiles.map((file, index) => (
            <div
              key={file.path}
              onMouseDown={(e) => { e.preventDefault(); insertFileRef(file); }}
              onMouseEnter={() => setFilePickerSelected(index)}
              className={cn(
                "flex items-center gap-2 px-3 py-1.5 cursor-pointer text-sm",
                index === filePickerSelected ? "bg-accent" : "hover:bg-accent/50",
              )}
            >
              <FileText className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
              <span className="font-mono text-xs truncate">{file.name}</span>
              <span className="text-xs text-muted-foreground truncate flex-1">{file.path}</span>
            </div>
          ))}
        </div>,
        document.body,
      )}
    </div>
  );
}
