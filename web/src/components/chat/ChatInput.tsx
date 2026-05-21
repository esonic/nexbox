import { useState, useRef, useEffect, useCallback } from "react";
import { toast } from "sonner";
import { Send, StopCircle, Command, FileText, ChevronDown, Check, X, GitBranch, ImagePlus, Play, Pencil, Workflow, MessageSquare, CircleHelp } from "lucide-react";
import { cn } from "@/lib/utils";
import { useIsMobile } from "@/hooks/useIsMobile";
import { INPUT_HEIGHT_MIN, INPUT_HEIGHT_MAX } from "@/lib/constants";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import {
  QuestionCard,
  parseAskQuestionInput,
  formatAnswersMessage,
  type QuestionAnswer,
} from "@/components/chat/QuestionCard";
import { TodoPanel } from "@/components/chat/TodoPanel";
import { imageApi, filesApi, type DiffResponse, type FileEntry } from "@/lib/api";
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

const AGENT_LABELS: Record<string, string> = {
  claude: "Claude Code",
  codex: "Codex",
};

export const SLASH_COMMANDS = [
  {
    name: "design",
    label: "Write Design Doc",
    description: "Generate a design document for the project",
    prompt: "Based on the project context, write a detailed design document. If anything is ambiguous, use the ask_question tool to clarify with user before proceeding. Save the final document using the save_asset tool when done.",
  },
  {
    name: "revise",
    label: "Address Comments",
    description: "Revise documents based on review comments",
    prompt: "If session references include assets, use list_asset_comments on those assets, address the feedback, and save via save_asset. If no assets are referenced, call list_assets to get recent ones, then use ask_question to ask the user which asset to revise before proceeding.",
  },
  {
    name: "code",
    label: "Implement Code",
    description: "Write code based on design doc or task",
    prompt: "If session references include assets or tasks, use get_asset_content or get_task as the spec and implement the code. If nothing is referenced, call list_tasks to get recent ones, then use ask_question to ask the user which task to implement before proceeding.",
  },
  {
    name: "review",
    label: "Code Review",
    description: "Review code or git diff using Prism rules",
    prompt: "Review the code based on the user's request. " +
      "If the user specifies rule group(s), call get_code_review_rules with those IDs to fetch the rules. " +
      "If no rule group is specified, proceed with general best practices. " +
      "Get the code via git diff, git show, or file read as appropriate. " +
      "Report issues grouped by severity (error → warning → info) with file, line, rule name, and suggestion.",
    needsInput: true,
    inputHint: "What to review (e.g. 'git diff', 'git diff HEAD~1 using rule group 5')...",
  },
  {
    name: "e2e_test",
    label: "E2E Browser Test",
    description: "Run end-to-end tests with Chrome browser",
    prompt: "You now have Chrome DevTools MCP tools available for end-to-end browser testing. " +
      "Use take_snapshot to see the page structure (accessibility tree with UIDs), then interact with elements using their UIDs. " +
      "Typical workflow: navigate_page -> take_snapshot -> click/fill/type_text -> take_screenshot to verify. " +
      "Always call take_snapshot before interacting to get fresh UIDs.",
    features: ["chrome_devtools"],
    needsInput: true,
    inputHint: "Describe what to test (e.g. URL, flow, expected behavior)...",
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
}: {
  item: RichQueuedMessage;
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
      <div className="py-3 border-b">
        <QuestionCard
          title={questionCardData.title}
          questions={questionCardData.questions}
          onSubmit={(answers: QuestionAnswer[]) => {
            const answersMap: Record<string, string> = {};
            for (const ans of answers) {
              const questionText = questionCardData.questionTextMap[ans.question_id];
              if (!questionText) continue;
              const selectedLabels = ans.selected_options
                .map((optId) => questionCardData.optionLabelMap[optId] || optId)
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
    <div className="py-3 border-b">
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
  modelName: string;
  effort: string;
  availableAgents: string[];
  availableModels: string[];
  availableModelInfos: ModelInfo[];
  sessionHasMessages: boolean;
  worktree?: WorktreeSelectorProps;
  pickedTasks: Task[];
  pickedAssets: PickedAsset[];
  pickedWorkflows: PickedWorkflow[];
  pendingQuestion: ReturnType<typeof parseAskQuestionInput>;
  pendingTodo?: { todos: Array<{ id: string; content: string; status: string }>; isPending: boolean } | null;
  pendingUserInput?: { toolUseId: string; toolName: string; input: string } | null;
  onSendToolResponse?: (toolUseId: string, response: string) => void;
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
  modelName,
  effort,
  availableAgents,
  availableModels,
  availableModelInfos,
  sessionHasMessages,
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

  const isMobile = useIsMobile();
  // Input area resize. Mobile defaults to one row of text + Textarea's
  // built-in vertical padding (py-2 = 16px) so the message area above gets
  // back the vertical space the desktop layout wastes. The textarea is
  // internally scrollable; on desktop the user can drag the handle to grow
  // it.
  const [inputHeight, setInputHeight] = useState(() => (isMobile ? 36 : 100));
  const isInputResizing = useRef(false);
  const inputResizeStartY = useRef(0);
  const inputResizeStartH = useRef(0);

  const handleInputResizeMouseDown = useCallback((e: React.MouseEvent) => {
    e.preventDefault();
    isInputResizing.current = true;
    inputResizeStartY.current = e.clientY;
    inputResizeStartH.current = inputHeight;

    const onMouseMove = (ev: MouseEvent) => {
      if (!isInputResizing.current) return;
      const delta = inputResizeStartY.current - ev.clientY;
      setInputHeight(Math.min(INPUT_HEIGHT_MAX, Math.max(INPUT_HEIGHT_MIN, inputResizeStartH.current + delta)));
    };
    const onMouseUp = () => {
      isInputResizing.current = false;
      document.removeEventListener("mousemove", onMouseMove);
      document.removeEventListener("mouseup", onMouseUp);
    };
    document.addEventListener("mousemove", onMouseMove);
    document.addEventListener("mouseup", onMouseUp);
  }, [inputHeight]);

  const filteredCommands = SLASH_COMMANDS.filter((cmd) =>
    cmd.name.toLowerCase().includes(slashFilter.toLowerCase()) ||
    cmd.label.toLowerCase().includes(slashFilter.toLowerCase())
  );
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

  const handleSend = () => {
    const hasContent = input.trim() || attachedImages.length > 0 || pickedTasks.length > 0 || pickedAssets.length > 0 || pickedWorkflows.length > 0 || pickedComments.length > 0 || activeCommand;
    if (!hasContent) return;
    if (attachedImages.length > 0 && !attachedImages.every(i => i.filename)) return;

    let message = input;
    let features: string[] | undefined;

    if (activeCommand) {
      const userInput = input.trim();
      message = userInput
        ? `${activeCommand.prompt}\n\nUser request: ${userInput}`
        : activeCommand.prompt;
      features = "features" in activeCommand ? ([...activeCommand.features] as string[]) : undefined;
      setActiveCommand(null);
    }

    onSend(message, attachedImages.length > 0 ? attachedImages : undefined, features);
    setInput("");
    saveDraft("");
    setAttachedImages([]);
    if (textareaRef.current) {
      textareaRef.current.style.height = "auto";
    }
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

  useEffect(() => {
    if (!showSlashMenu || filteredCommands.length === 0) return;
    const container = slashMenuRef.current;
    if (!container) return;
    const item = container.querySelector(`[data-cmd-index="${selectedCommandIndex}"]`);
    if (item) item.scrollIntoView({ block: "nearest" });
  }, [selectedCommandIndex, showSlashMenu, filteredCommands.length]);

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

    if (showSlashMenu && filteredCommands.length > 0) {
      if (navigateMenu(e, filteredCommands.length, selectedCommandIndex, setSelectedCommandIndex,
        () => executeSlashCommand(filteredCommands[selectedCommandIndex]),
        () => { setShowSlashMenu(false); setInput(""); },
      )) return;
    }
    if (e.key === "Escape" && activeCommand && !showSlashMenu && !filePickerOpen) {
      e.preventDefault();
      setActiveCommand(null);
      return;
    }
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
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

  const selectedModel = modelName || availableModels[0] || "";
  const currentModelInfo = availableModelInfos.find((m) => m.id === selectedModel);
  const displayModelLabel = (info?: ModelInfo, fallback?: string) => {
    const raw = info?.label || fallback || "";
    const hay = `${info?.id || ""} ${raw}`.toLowerCase();
    if (hay.includes("haiku")) return "Haiku";
    if (hay.includes("opus")) return "Opus";
    if (hay.includes("sonnet")) return "Sonnet";
    return raw || "Model";
  };
  const currentModelLabel = displayModelLabel(currentModelInfo, selectedModel);
  const visibleModels = availableModels.filter((m) => m.toLowerCase() !== "default");
  const supportedEfforts = currentModelInfo?.supported_efforts || [];
  const currentEffort = effort || currentModelInfo?.default_effort || "";
  const effortLabel = currentEffort ? currentEffort[0].toUpperCase() + currentEffort.slice(1) : "";

  return (
    <div className="shrink-0">
      {/* Resize handle — drag-resize doesn't apply on touch screens. */}
      {!isMobile && (
        <div
          className="h-1 cursor-row-resize hover:bg-primary/20 active:bg-primary/40 transition-colors"
          onMouseDown={handleInputResizeMouseDown}
        />
      )}
      <div className="max-w-4xl mx-auto px-2 sm:px-4 pb-2 sm:pb-3">
        {pendingTodo && (
          <TodoPanel todos={pendingTodo.todos} isPending={pendingTodo.isPending} />
        )}

        {/* Question panel */}
        {pendingQuestion && (
          <div className="py-3 border-b">
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
            onSkip={() => onSendToolResponse(pendingUserInput.toolUseId, "Skip this question, please continue.")}
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
                <span>{gitDiffStats.total_files} file{gitDiffStats.total_files !== 1 ? "s" : ""} changed</span>
                <span className="text-green-500 font-medium">+{gitDiffStats.insertions}</span>
                <span className="text-red-500 font-medium">-{gitDiffStats.deletions}</span>
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
              {richQueue.map((item) => (
                <RichQueuedItem
                  key={item.id}
                  item={item}
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
          {showSlashMenu && filteredCommands.length > 0 && (
            <div ref={slashMenuRef} className="absolute bottom-full left-0 right-0 mb-2 bg-background border rounded-lg shadow-lg overflow-hidden z-10 max-h-64 overflow-y-auto">
              <div className="p-2 text-xs text-muted-foreground border-b">
                <Command className="inline h-3 w-3 mr-1" />
                Commands
              </div>
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
            placeholder={!isConnected ? "Connecting..." : activeCommand && "inputHint" in activeCommand ? activeCommand.inputHint : "Type a message, / for commands, @ for files..."}
            disabled={!isConnected}
            className="resize-none border-0 bg-transparent shadow-none focus-visible:ring-0 focus-visible:ring-offset-0 min-h-0"
            style={{ height: inputHeight }}
            rows={isMobile ? 1 : 3}
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
                      {AGENT_LABELS[agentName] || agentName}
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
                            <span>{AGENT_LABELS[ag] || ag}</span>
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
                      onClick={() => setShowModelMenu((v) => !v)}
                      className="text-[10px] font-medium px-1.5 py-0.5 rounded-sm flex items-center gap-1 whitespace-nowrap transition-colors bg-violet-500/15 text-violet-400 hover:bg-violet-500/25 cursor-pointer"
                      title="Click to change model"
                    >
                      <span>{currentModelLabel}</span>
                      {effortLabel && (
                        <span className="text-violet-400/60 font-normal">· {effortLabel}</span>
                      )}
                      <ChevronDown className="h-2.5 w-2.5" />
                    </button>

                    {showModelMenu && (
                      <div className="absolute left-0 bottom-full z-50 mb-1 w-52 bg-popover border border-border rounded-lg shadow-lg overflow-hidden">
                        <div className="px-3 pt-1.5 pb-0.5 text-[10px] uppercase tracking-wider text-muted-foreground/60">
                          Models
                        </div>
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
                              title={info?.description || undefined}
                            >
                              <div className="w-full flex items-center justify-between">
                                <span className="truncate">{displayModelLabel(info, model)}</span>
                                {selectedModel === model && <Check className="h-3 w-3 text-primary" />}
                              </div>
                            </button>
                          );
                        })}
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
