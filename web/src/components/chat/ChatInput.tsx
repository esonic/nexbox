import { useState, useRef, useEffect, useCallback } from "react";
import { toast } from "sonner";
import { Send, StopCircle, Command, AtSign, ListTodo, FileText, ChevronDown, Check, X, GitBranch, UserPen, UserCheck, ImagePlus, Play, Pencil, Workflow, MessageSquare } from "lucide-react";
import { cn } from "@/lib/utils";
import { INPUT_HEIGHT_MIN, INPUT_HEIGHT_MAX } from "@/lib/constants";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import {
  QuestionCard,
  parseAskQuestionInput,
  formatAnswersMessage,
  type QuestionAnswer,
} from "@/components/chat/QuestionCard";
import { projectsApi, tasksApi, assetsApi, imageApi, type DiffResponse } from "@/lib/api";
import { workflowApi } from "@/lib/workflowApi";
import { useAppStore } from "@/stores/app";
import { STATUS_ICONS } from "@/plugins/task/task-icons";
import { normalizeStatus, shortId, STATUS_MAP } from "@/plugins/task/task-config";
import { formatRelativeDate } from "@/lib/utils";
import type { Task, AssetType } from "@/types";
import type { PickedAsset, PickedWorkflow, InlineComment, RichQueuedMessage } from "@/stores/app";
import type { QueuedMessage } from "@/hooks/useChatSession";

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

const AT_ASSET_TYPE_LABELS: Record<string, string> = {
  requirement_doc: "Requirement", design_doc: "Design", code_change: "Code Change",
  unit_test: "Unit Test", test_case: "Test Case",
};
const AT_ASSET_TYPE_BADGE: Record<string, string> = {
  requirement_doc: "bg-blue-500/15 text-blue-400", design_doc: "bg-violet-500/15 text-violet-400",
  code_change: "bg-emerald-500/15 text-emerald-400", unit_test: "bg-amber-500/15 text-amber-400",
  test_case: "bg-rose-500/15 text-rose-400",
};
const AT_ASSET_TYPE_ICON_COLOR: Record<string, string> = {
  requirement_doc: "text-blue-400", design_doc: "text-violet-400", code_change: "text-emerald-400",
  unit_test: "text-amber-400", test_case: "text-rose-400",
};

type AtItem = {
  id: string; type: "task" | "asset" | "workflow"; name: string;
  status?: string; priority?: string; createdBy?: string; assigner?: string; updatedAt?: string;
  projectName?: string; assetType?: string; projectId?: string;
};

function AtPickerTaskRow({ item }: { item: AtItem }) {
  const st = normalizeStatus(item.status || "open");
  const StIcon = STATUS_ICONS[st];
  const sc = STATUS_MAP.get(st);
  return (
    <>
      <ListTodo className="h-3.5 w-3.5 shrink-0 text-muted-foreground/50" />
      <span className="shrink-0 text-[10px] text-muted-foreground font-mono tabular-nums">{shortId(item.id)}</span>
      <StIcon className={cn("h-3.5 w-3.5 shrink-0", sc?.color)} />
      <span className="text-[13px] truncate">{item.name}</span>
      {item.projectName && (
        <span className="shrink-0 text-[10px] text-violet-500/80 bg-violet-500/10 px-1.5 py-0.5 rounded font-medium truncate max-w-[100px]">{item.projectName}</span>
      )}
      <div className="flex items-center gap-1.5 ml-auto shrink-0">
        {item.createdBy && item.createdBy !== "local" && (
          <span className="flex items-center gap-0.5 text-[10px] text-blue-400" title={`Creator: ${item.createdBy}`}>
            <UserPen className="h-2.5 w-2.5" /><span className="max-w-[50px] truncate">{item.createdBy}</span>
          </span>
        )}
        {item.assigner && (
          <span className="flex items-center gap-0.5 text-[10px] text-amber-500" title={`Assignee: ${item.assigner}`}>
            <UserCheck className="h-2.5 w-2.5" /><span className="max-w-[50px] truncate">{item.assigner}</span>
          </span>
        )}
        {item.updatedAt && (
          <span className="text-[10px] text-muted-foreground/60 tabular-nums">{formatRelativeDate(item.updatedAt)}</span>
        )}
      </div>
    </>
  );
}

function AtPickerAssetRow({ item }: { item: AtItem }) {
  const iconColor = AT_ASSET_TYPE_ICON_COLOR[item.assetType || ""] || "text-muted-foreground";
  const badgeColor = AT_ASSET_TYPE_BADGE[item.assetType || ""] || "bg-muted text-muted-foreground";
  const typeLabel = AT_ASSET_TYPE_LABELS[item.assetType || ""] || item.assetType || "";
  return (
    <>
      <FileText className={cn("h-3.5 w-3.5 shrink-0", iconColor)} />
      <span className="shrink-0 text-[10px] text-muted-foreground font-mono tabular-nums">{shortId(item.id)}</span>
      <span className="text-[13px] truncate">{item.name}</span>
      {typeLabel && (
        <span className={cn("shrink-0 text-[10px] font-medium px-1.5 py-0.5 rounded", badgeColor)}>{typeLabel}</span>
      )}
      {item.projectName && (
        <span className="shrink-0 text-[10px] text-violet-500/80 bg-violet-500/10 px-1.5 py-0.5 rounded font-medium truncate max-w-[100px]">{item.projectName}</span>
      )}
      <div className="flex items-center gap-1.5 ml-auto shrink-0">
        {item.createdBy && item.createdBy !== "local" && (
          <span className="flex items-center gap-0.5 text-[10px] text-blue-400" title={`Creator: ${item.createdBy}`}>
            <UserPen className="h-2.5 w-2.5" /><span className="max-w-[50px] truncate">{item.createdBy}</span>
          </span>
        )}
        {item.updatedAt && (
          <span className="text-[10px] text-muted-foreground/60 tabular-nums">{formatRelativeDate(item.updatedAt)}</span>
        )}
      </div>
    </>
  );
}

function AtPickerWorkflowRow({ item }: { item: AtItem }) {
  return (
    <>
      <Workflow className="h-3.5 w-3.5 shrink-0 text-purple-400" />
      <Play className="h-3 w-3 shrink-0 text-purple-400/60" />
      <span className="text-[13px] truncate">{item.name}</span>
      <span className="shrink-0 text-[10px] font-medium px-1.5 py-0.5 rounded bg-purple-500/10 text-purple-400">workflow</span>
      {item.projectName && (
        <span className="shrink-0 text-[10px] text-violet-500/80 bg-violet-500/10 px-1.5 py-0.5 rounded font-medium truncate max-w-[100px]">{item.projectName}</span>
      )}
    </>
  );
}

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
        <button onClick={onSendNow} className="p-1 rounded hover:bg-accent text-primary">
          <Play className="h-3 w-3 fill-current" />
        </button>
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
          <button onClick={onSendNow} className="p-1 rounded hover:bg-accent text-primary">
            <Play className="h-3 w-3 fill-current" />
          </button>
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

interface ChatInputProps {
  sessionId: string;
  isConnected: boolean;
  isStreaming: boolean;
  agentName: string;
  availableAgents: string[];
  sessionHasMessages: boolean;
  pickedTasks: Task[];
  pickedAssets: PickedAsset[];
  pickedWorkflows: PickedWorkflow[];
  pendingQuestion: ReturnType<typeof parseAskQuestionInput>;
  queue: QueuedMessage[];
  gitDiffStats?: DiffResponse | null;
  onSend: (message: string, images?: AttachedImage[], features?: string[]) => void;
  onStop: () => void;
  onChangeAgent: (agent: string) => void;
  onRemovePickedTask: (taskId: string) => void;
  onAddPickedTask: (task: Task) => void;
  onRemovePickedAsset: (assetId: string) => void;
  onAddPickedAsset: (asset: PickedAsset) => void;
  onAddPickedWorkflow: (wf: PickedWorkflow) => void;
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
  availableAgents,
  sessionHasMessages,
  pickedTasks,
  pickedAssets,
  pickedWorkflows,
  pendingQuestion,
  queue,
  gitDiffStats,
  onSend,
  onStop,
  onChangeAgent,
  onRemovePickedTask,
  onAddPickedTask,
  onRemovePickedAsset,
  onAddPickedAsset,
  onAddPickedWorkflow,
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

  // @ mention state
  const [showAtMenu, setShowAtMenu] = useState(false);
  const [atQuery, setAtQuery] = useState("");
  const [atResults, setAtResults] = useState<Array<{
    id: string; type: "task" | "asset" | "workflow"; name: string;
    status?: string; priority?: string; createdBy?: string; assigner?: string; updatedAt?: string;
    projectName?: string; assetType?: string; projectId?: string;
  }>>([]);
  const [selectedAtIndex, setSelectedAtIndex] = useState(0);
  const [atCursorPos, setAtCursorPos] = useState(0);
  const atSearchTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const atMenuRef = useRef<HTMLDivElement>(null);
  const slashMenuRef = useRef<HTMLDivElement>(null);

  // Input area resize
  const [inputHeight, setInputHeight] = useState(100);
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
      setShowAtMenu(false);
    } else {
      setShowSlashMenu(false);
      setSlashFilter("");
    }

    // @ mention detection
    const textBeforeCursor = value.slice(0, cursorPos);
    const lastAtIdx = textBeforeCursor.lastIndexOf("@");
    if (lastAtIdx >= 0) {
      const afterAt = textBeforeCursor.slice(lastAtIdx + 1);
      if (!afterAt.includes("\n") && afterAt.length < 40) {
        setShowAtMenu(true);
        setAtQuery(afterAt);
        setAtCursorPos(cursorPos);
        return;
      }
    }
    setShowAtMenu(false);
    setAtQuery("");
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

  // @ mention search
  const currentProjectId = useAppStore((s) => s.currentSession?.project_id);
  useEffect(() => {
    if (!showAtMenu) return;
    if (atSearchTimer.current) clearTimeout(atSearchTimer.current);
    atSearchTimer.current = setTimeout(async () => {
      const q = atQuery.toLowerCase();
      const taskResults: typeof atResults = [];
      const assetResults: typeof atResults = [];
      const TOTAL_LIMIT = 20;
      const BASE_LIMIT = 10;
      // When searching, use a larger fetch limit so server-side q filter can scan more records
      const fetchLimit = q ? 200 : TOTAL_LIMIT;
      try {
        const [projs, projTasksRaw, globalTasksRaw] = await Promise.all([
          projectsApi.list(),
          currentProjectId ? tasksApi.list(fetchLimit, 0, currentProjectId, q || undefined) : Promise.resolve(null),
          tasksApi.list(fetchLimit, 0, undefined, q || undefined),
        ]);
        const projMap = new Map((projs || []).map((p) => [p.id, p.name]));

        const pushTask = (t: Task, projName: string | undefined) => {
          taskResults.push({
            id: t.id, type: "task", name: t.title,
            status: t.status, priority: t.priority,
            createdBy: t.created_by, assigner: t.assigner,
            updatedAt: t.updated_at,
            projectName: projName,
          });
        };

        // Current project tasks first (same order as list page)
        for (const t of projTasksRaw || []) {
          if (t.status === "done" || t.status === "closed") continue;
          if (q && !t.title.toLowerCase().includes(q)) continue;
          pushTask(t, projMap.get(currentProjectId!));
        }
        // Fill with global tasks if fewer than BASE_LIMIT from current project
        if (taskResults.length < BASE_LIMIT || !currentProjectId) {
          const seenIds = new Set(taskResults.map((t) => t.id));
          for (const t of globalTasksRaw || []) {
            if (seenIds.has(t.id)) continue;
            if (t.status === "done" || t.status === "closed") continue;
            if (q && !t.title.toLowerCase().includes(q)) continue;
            pushTask(t, t.project_id ? projMap.get(t.project_id) : undefined);
          }
        }

        const sortedProjs = [...(projs || [])];
        if (currentProjectId) {
          sortedProjs.sort((a, b) =>
            (a.id === currentProjectId ? 0 : 1) - (b.id === currentProjectId ? 0 : 1)
          );
        }

        const assetArrays = await Promise.all(
          sortedProjs.map((p) => assetsApi.list(p.id, fetchLimit, 0, undefined, q || undefined))
        );

        for (let i = 0; i < sortedProjs.length; i++) {
          const p = sortedProjs[i];
          for (const a of assetArrays[i] || []) {
            if (q && !a.title.toLowerCase().includes(q)) continue;
            assetResults.push({
              id: a.id, type: "asset", name: a.title,
              assetType: a.type, projectName: p.name, createdBy: a.created_by, updatedAt: a.updated_at,
            });
          }
        }
      } catch (e) {
        console.error("@ search failed:", e);
        toast.error("Search failed", { description: String(e) });
      }
      const taskLimit = Math.min(taskResults.length, Math.max(BASE_LIMIT, TOTAL_LIMIT - assetResults.length));
      const assetLimit = Math.min(assetResults.length, Math.max(5, TOTAL_LIMIT - taskLimit));
      setAtResults([...taskResults.slice(0, taskLimit), ...assetResults.slice(0, assetLimit)]);
      setSelectedAtIndex(0);
    }, 150);
    return () => { if (atSearchTimer.current) clearTimeout(atSearchTimer.current); };
  }, [showAtMenu, atQuery, currentProjectId]);

  useEffect(() => {
    if (!showSlashMenu || filteredCommands.length === 0) return;
    const container = slashMenuRef.current;
    if (!container) return;
    const item = container.querySelector(`[data-cmd-index="${selectedCommandIndex}"]`);
    if (item) item.scrollIntoView({ block: "nearest" });
  }, [selectedCommandIndex, showSlashMenu, filteredCommands.length]);

  useEffect(() => {
    if (!showAtMenu || atResults.length === 0) return;
    const container = atMenuRef.current;
    if (!container) return;
    const item = container.querySelector(`[data-at-index="${selectedAtIndex}"]`);
    if (item) item.scrollIntoView({ block: "nearest" });
  }, [selectedAtIndex, showAtMenu, atResults.length]);

  const handleAtSelect = async (item: typeof atResults[0]) => {
    const before = input.slice(0, atCursorPos);
    const atStart = before.lastIndexOf("@");
    const after = input.slice(textareaRef.current?.selectionStart || input.length);

    if (item.type === "task") {
      const newInput = before.slice(0, atStart) + after;
      setInput(newInput.trimEnd() ? newInput : newInput.trim());
      setShowAtMenu(false);
      setAtQuery("");

      try {
        const fullTask = await tasksApi.get(item.id);
        onAddPickedTask(fullTask);
      } catch {
        onAddPickedTask({
          id: item.id,
          parent_id: "",
          title: item.name,
          description: "",
          status: (item.status as Task["status"]) || "open",
          priority: (item.priority as Task["priority"]) || "medium",
          ref_count: 0,
          comment_count: 0,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        });
      }
    } else if (item.type === "workflow") {
      const newInput = before.slice(0, atStart) + after;
      setInput(newInput.trimEnd() ? newInput : newInput.trim());
      setShowAtMenu(false);
      setAtQuery("");
      onAddPickedWorkflow({ id: item.id, name: item.name, projectId: item.projectId || "" });
    } else {
      const newInput = before.slice(0, atStart) + after;
      setInput(newInput.trimEnd() ? newInput : newInput.trim());
      setShowAtMenu(false);
      setAtQuery("");
      onAddPickedAsset({ id: item.id, title: item.name, assetType: (item.assetType || "design_doc") as AssetType });
    }

    textareaRef.current?.focus();
  };

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

    if (showSlashMenu && filteredCommands.length > 0) {
      if (navigateMenu(e, filteredCommands.length, selectedCommandIndex, setSelectedCommandIndex,
        () => executeSlashCommand(filteredCommands[selectedCommandIndex]),
        () => { setShowSlashMenu(false); setInput(""); },
      )) return;
    }
    if (showAtMenu && atResults.length > 0) {
      if (navigateMenu(e, atResults.length, selectedAtIndex, setSelectedAtIndex,
        () => handleAtSelect(atResults[selectedAtIndex]),
        () => setShowAtMenu(false),
      )) return;
    }
    if (e.key === "Escape" && activeCommand && !showSlashMenu && !showAtMenu) {
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

  return (
    <div className="shrink-0">
      {/* Resize handle */}
      <div
        className="h-1 cursor-row-resize hover:bg-primary/20 active:bg-primary/40 transition-colors"
        onMouseDown={handleInputResizeMouseDown}
      />
      <div className="max-w-3xl mx-auto px-4 pb-3">
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
          {/* @ mention menu */}
          {showAtMenu && atResults.length > 0 && (
            <div ref={atMenuRef} className="absolute bottom-full left-0 right-0 mb-2 bg-background border rounded-lg shadow-lg overflow-hidden z-10 max-h-64 overflow-y-auto">
              <div className="p-2 text-xs text-muted-foreground border-b">
                <AtSign className="inline h-3 w-3 mr-1" />
                Reference — add context to this session
              </div>
              {atResults.map((item, index) => (
                <button
                  key={`${item.type}-${item.id}`}
                  data-at-index={index}
                  onClick={() => handleAtSelect(item)}
                  className={cn(
                    "w-full text-left px-3 py-1.5 flex items-center gap-1.5 hover:bg-accent transition-colors",
                    index === selectedAtIndex && "bg-accent"
                  )}
                >
                  {item.type === "task" ? (
                    <AtPickerTaskRow item={item} />
                  ) : item.type === "workflow" ? (
                    <AtPickerWorkflowRow item={item} />
                  ) : (
                    <AtPickerAssetRow item={item} />
                  )}
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
            placeholder={!isConnected ? "Connecting..." : activeCommand && "inputHint" in activeCommand ? activeCommand.inputHint : "Type a message, / for commands, @ for references..."}
            disabled={!isConnected}
            className="resize-none border-0 bg-transparent shadow-none focus-visible:ring-0 focus-visible:ring-offset-0"
            style={{ height: inputHeight }}
            rows={3}
          />

          {/* Bottom toolbar */}
          <div className="flex items-center justify-between px-3 pb-2 pt-1">
            <div className="flex items-center gap-2">
              <span className={`text-xs ${isConnected ? "text-green-500" : "text-red-500"}`}>
                {isConnected ? "Connected" : "Disconnected"}
              </span>
              {agentName && (
                <>
                  <span className="text-muted-foreground/40 text-xs">·</span>
                  <div className="relative" ref={agentMenuRef}>
                    <button
                      onClick={() => {
                        if (!sessionHasMessages && availableAgents.length > 1) setShowAgentMenu((v) => !v);
                      }}
                      className={cn(
                        "text-[10px] font-medium px-1.5 py-0.5 rounded-sm flex items-center gap-1 transition-colors",
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
            </div>
            <div className="flex items-center gap-1">
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
              {isStreaming && (
                <Button
                  onClick={onStop}
                  size="icon"
                  variant="outline"
                  className="h-7 w-7 rounded-md shrink-0"
                  title="Stop"
                >
                  <StopCircle className="h-3.5 w-3.5" />
                </Button>
              )}
              <Button
                onClick={handleSend}
                disabled={!isConnected || uploading || (!input.trim() && !activeCommand && pickedTasks.length === 0 && pickedAssets.length === 0 && pickedWorkflows.length === 0 && pickedComments.length === 0 && attachedImages.length === 0)}
                size="icon"
                className="h-7 w-7 rounded-md shrink-0"
                title={isStreaming ? "Queue message" : "Send message"}
              >
                <Send className="h-3.5 w-3.5" />
              </Button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
