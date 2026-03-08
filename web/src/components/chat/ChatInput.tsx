import { useState, useRef, useEffect, useCallback } from "react";
import { toast } from "sonner";
import { Send, StopCircle, FileText, ChevronDown, Check, X, GitBranch, ImagePlus, Play, Pencil, Workflow, MessageSquare } from "lucide-react";
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
import { imageApi, type DiffResponse } from "@/lib/api";
import { useAppStore } from "@/stores/app";
import { STATUS_ICONS } from "@/plugins/task/task-icons";
import { normalizeStatus, shortId, STATUS_MAP } from "@/plugins/task/task-config";
import { formatRelativeDate } from "@/lib/utils";
import type { Task } from "@/types";
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

const MODEL_OPTIONS: { value: string; label: string }[] = [
  { value: "claude-sonnet-4-6", label: "Sonnet" },
  { value: "claude-haiku-4-5", label: "Haiku" },
];

const MODEL_LABELS: Record<string, string> = {
  "claude-sonnet-4-6": "Sonnet",
  "claude-haiku-4-5": "Haiku",
};

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
  modelName: string;
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
  onChangeModel: (model: string) => void;
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
  modelName,
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
  onChangeModel,
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

  const textareaRef = useRef<HTMLTextAreaElement>(null);

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

  // Close agent/model menu on outside click
  useEffect(() => {
    if (!showAgentMenu && !showModelMenu) return;
    const handler = (e: MouseEvent) => {
      if (showAgentMenu && agentMenuRef.current && !agentMenuRef.current.contains(e.target as Node)) {
        setShowAgentMenu(false);
      }
      if (showModelMenu && modelMenuRef.current && !modelMenuRef.current.contains(e.target as Node)) {
        setShowModelMenu(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [showAgentMenu, showModelMenu]);

  const handleSend = () => {
    const hasContent = input.trim() || attachedImages.length > 0 || pickedTasks.length > 0 || pickedAssets.length > 0 || pickedWorkflows.length > 0 || pickedComments.length > 0;
    if (!hasContent) return;
    if (attachedImages.length > 0 && !attachedImages.every(i => i.filename)) return;

    let message = input;
    const features: string[] | undefined = undefined;

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
    setInput(value);
    saveDraft(value);
  };




  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.nativeEvent.isComposing) return;

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

  const currentModelLabel = MODEL_LABELS[modelName] || MODEL_LABELS["claude-sonnet-4-6"];

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
            placeholder={!isConnected ? "Connecting..." : "Type a message..."}
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
              {/* Model selector */}
              {agentName === "claude" && (
                <>
                  <span className="text-muted-foreground/40 text-xs">·</span>
                  <div className="relative" ref={modelMenuRef}>
                    <button
                      onClick={() => {
                        if (!sessionHasMessages) setShowModelMenu((v) => !v);
                      }}
                      className={cn(
                        "text-[10px] font-medium px-1.5 py-0.5 rounded-sm flex items-center gap-1 transition-colors",
                        sessionHasMessages
                          ? "bg-violet-500/15 text-violet-400 cursor-default"
                          : "bg-violet-500/15 text-violet-400 hover:bg-violet-500/25 cursor-pointer"
                      )}
                      title={sessionHasMessages ? "Model locked after first message" : "Click to change model"}
                    >
                      {currentModelLabel}
                      {!sessionHasMessages && (
                        <ChevronDown className="h-2.5 w-2.5" />
                      )}
                    </button>

                    {showModelMenu && (
                      <div className="absolute left-0 bottom-full z-50 mb-1 w-36 bg-popover border border-border rounded-lg shadow-lg overflow-hidden">
                        {MODEL_OPTIONS.map((opt) => (
                          <button
                            key={opt.value}
                            onClick={() => handleModelChange(opt.value)}
                            className={cn(
                              "w-full flex items-center justify-between px-3 py-1.5 text-xs hover:bg-accent transition-colors",
                              (modelName || "claude-sonnet-4-6") === opt.value && "bg-accent/50 font-medium"
                            )}
                          >
                            <span>{opt.label}</span>
                            {(modelName || "claude-sonnet-4-6") === opt.value && <Check className="h-3 w-3 text-primary" />}
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
                disabled={!isConnected || uploading || (!input.trim() && pickedTasks.length === 0 && pickedAssets.length === 0 && pickedWorkflows.length === 0 && pickedComments.length === 0 && attachedImages.length === 0)}
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
