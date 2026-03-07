/**
 * IssueRow – A reusable Linear-style task row component.
 *
 * Usage:
 *   <IssueRow task={task} />                        // read-only, compact
 *   <IssueRow task={task} onStatusChange={...} />   // interactive
 */
import { useState, useRef, useEffect } from "react";
import { toast } from "sonner";
import { Plus, Trash2, ChevronRight, ChevronDown, CornerRightUp, AtSign, MessageSquare } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import { UserChip } from "./components/UserPicker";
import type { Task } from "@/types";
import {
  type TaskStatus,
  type TaskPriority,
  STATUS_LIST,
  PRIORITY_LIST,
  STATUS_MAP,
  PRIORITY_MAP,
  normalizeStatus,
  normalizePriority,
  shortId,
} from "./task-config";
import { formatRelativeDate } from "@/lib/utils";
import { STATUS_ICONS, PRIORITY_ICONS } from "./task-icons";

// ── Props ──

export interface IssueRowProps {
  task: Task;

  /** Indentation depth (default 0). Used for sub-issues. */
  depth?: number;

  /** Whether this row has children (shows expand toggle). */
  hasChildren?: boolean;
  /** Number of child issues. */
  childCount?: number;
  /** Whether children are currently expanded. */
  isExpanded?: boolean;

  /** Callbacks — omit any to make that part read-only. */
  onToggle?: (id: string) => void;
  onAddChild?: (id: string) => void;
  onDelete?: (id: string) => void;
  onStatusChange?: (id: string, status: TaskStatus) => void;
  onPriorityChange?: (id: string, priority: TaskPriority) => void;
  onTitleChange?: (id: string, title: string) => void;
  onClick?: (task: Task) => void;

  /** Callback to pick/send this task to chat. Shows a pick icon on hover. */
  onPick?: (task: Task) => void;

  /** If true, render a more compact version (no actions, no date). */
  compact?: boolean;

  /** Whether the row is inside a group section (adds left indent). */
  grouped?: boolean;

  /** Project name to display as a badge (if task belongs to a project). */
  projectName?: string;

  /** Additional className for the row container. */
  className?: string;
}

export function IssueRow({
  task,
  depth = 0,
  hasChildren = false,
  childCount = 0,
  isExpanded = false,
  onToggle,
  onAddChild,
  onDelete,
  onStatusChange,
  onPriorityChange,
  onTitleChange,
  onClick,
  onPick,
  compact = false,
  grouped = false,
  projectName,
  className,
}: IssueRowProps) {
  const [editing, setEditing] = useState(false);
  const [editValue, setEditValue] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const [confirmingDelete, setConfirmingDelete] = useState(false);

  useEffect(() => {
    if (!confirmingDelete) return;
    const handler = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (target.closest("[data-delete-confirm]")) return;
      setConfirmingDelete(false);
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [confirmingDelete]);

  const status = normalizeStatus(task.status);
  const priority = normalizePriority(task.priority);
  const isDone = status === "done" || status === "closed";
  const sc = STATUS_MAP.get(status)!;
  const pc = PRIORITY_MAP.get(priority)!;
  const StatusIcon = STATUS_ICONS[status];
  const PriorityIcon = PRIORITY_ICONS[priority];

  const startEdit = () => {
    if (!onTitleChange) return;
    setEditValue(task.title);
    setEditing(true);
    setTimeout(() => inputRef.current?.focus(), 0);
  };
  const commitEdit = () => {
    const v = editValue.trim();
    if (v && v !== task.title) onTitleChange?.(task.id, v);
    setEditing(false);
  };

  const interactiveStatus = !!onStatusChange;
  const interactivePriority = !!onPriorityChange;

  // ── Priority cell ──
  const priorityCell = interactivePriority ? (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          onClick={(e) => e.stopPropagation()}
          className={cn(
            "shrink-0 w-6 h-6 flex items-center justify-center rounded hover:bg-accent/60 outline-none transition-colors",
            pc.color
          )}
        >
          <PriorityIcon />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-[160px]">
        {PRIORITY_LIST.map((p) => {
          const PIc = PRIORITY_ICONS[p.value];
          return (
            <DropdownMenuItem
              key={p.value}
              onClick={(e) => { e.stopPropagation(); onPriorityChange!(task.id, p.value); }}
              className={cn("gap-2.5 text-xs cursor-pointer", p.color)}
            >
              <PIc className="shrink-0" /> {p.label}
            </DropdownMenuItem>
          );
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  ) : (
    <span className={cn("shrink-0 w-6 h-6 flex items-center justify-center", pc.color)}>
      <PriorityIcon />
    </span>
  );

  // ── Status cell ──
  const statusCell = interactiveStatus ? (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          onClick={(e) => e.stopPropagation()}
          className={cn(
            "shrink-0 w-6 h-6 flex items-center justify-center rounded hover:bg-accent/60 outline-none transition-colors",
            sc.color
          )}
        >
          <StatusIcon />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-[160px]">
        {STATUS_LIST.map((s) => {
          const SIc = STATUS_ICONS[s.value];
          return (
            <DropdownMenuItem
              key={s.value}
              onClick={(e) => { e.stopPropagation(); onStatusChange!(task.id, s.value); }}
              className={cn("gap-2.5 text-xs cursor-pointer", s.color)}
            >
              <SIc className="shrink-0" /> {s.label}
            </DropdownMenuItem>
          );
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  ) : (
    <span className={cn("shrink-0 w-6 h-6 flex items-center justify-center", sc.color)}>
      <StatusIcon />
    </span>
  );

  return (
    <div
      className={cn(
        "group/row flex items-center border-b border-border/10 hover:bg-[#ffffff06] dark:hover:bg-[#ffffff06] transition-colors text-sm select-none",
        compact ? "h-[32px]" : "h-[38px]",
        onClick && "cursor-pointer",
        className
      )}
      style={{ paddingLeft: compact ? 8 : (grouped ? 28 : 16) + depth * 20 }}
      onClick={onClick ? () => onClick(task) : undefined}
    >
      {/* Expand/collapse */}
      {hasChildren && onToggle ? (
        <button
          onClick={(e) => { e.stopPropagation(); onToggle(task.id); }}
          className="shrink-0 w-5 h-5 flex items-center justify-center rounded hover:bg-accent/60 transition-colors text-muted-foreground"
        >
          {isExpanded
            ? <ChevronDown className="h-3 w-3" />
            : <ChevronRight className="h-3 w-3" />}
        </button>
      ) : !compact && hasChildren !== undefined && (
        <span className="shrink-0 w-5" />
      )}

      {/* Priority */}
      {priorityCell}

      {/* Issue ID */}
      {!compact && (
        <span
          className="shrink-0 w-[64px] pl-2 text-[11px] text-muted-foreground font-mono tabular-nums tracking-wide hover:text-foreground cursor-copy"
          title={task.id}
          onClick={(e) => {
            e.stopPropagation();
            navigator.clipboard.writeText(task.id);
            toast.success("Copied", { duration: 1500 });
          }}
        >
          {shortId(task.id)}
        </span>
      )}

      {/* Status */}
      {statusCell}

      {/* Title area */}
      <div className="flex-1 min-w-0 flex items-center gap-2 pl-2 pr-2">
        {/* Title */}
        {editing ? (
          <input
            ref={inputRef}
            value={editValue}
            onChange={(e) => setEditValue(e.target.value)}
            onBlur={commitEdit}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.nativeEvent.isComposing) commitEdit();
              if (e.key === "Escape") setEditing(false);
            }}
            onClick={(e) => e.stopPropagation()}
            className="flex-1 bg-transparent outline-none text-[13px] font-medium min-w-0"
          />
        ) : (
          <span
            onDoubleClick={startEdit}
            className={cn(
              "truncate cursor-default text-[13px]",
              isDone ? "line-through text-muted-foreground" : "text-foreground"
            )}
          >
            {task.title}
          </span>
        )}

        {/* Child count badge */}
        {hasChildren && !isExpanded && childCount > 0 && (
          <span className="shrink-0 text-[10px] text-muted-foreground bg-muted/60 px-1.5 py-0.5 rounded tabular-nums">
            {childCount}
          </span>
        )}
        {/* Project badge */}
        {projectName && (
          <span className="shrink-0 text-[10px] text-violet-500/80 bg-violet-500/10 px-1.5 py-0.5 rounded font-medium truncate max-w-[120px]" title={projectName}>
            {projectName}
          </span>
        )}
        {/* Label badges */}
        {(task.labels || []).map((name) => (
          <span
            key={name}
            className="shrink-0 inline-flex items-center px-1.5 py-0.5 rounded-full text-[10px] font-medium bg-muted text-muted-foreground border"
          >
            {name}
          </span>
        ))}
      </div>

      {/* Right side */}
      {!compact && (
        <div className="flex items-center shrink-0">
          {/* Hover actions */}
          <div className="hidden group-hover/row:flex items-center gap-0 mr-1">
            {onPick && (
              <button
                onClick={(e) => { e.stopPropagation(); onPick(task); }}
                className="p-1 rounded hover:bg-primary/10 text-muted-foreground/60 hover:text-primary transition-colors"
                title="Send to chat"
              >
                <CornerRightUp className="h-3.5 w-3.5" />
              </button>
            )}
            {onAddChild && (
              <button
                onClick={(e) => { e.stopPropagation(); onAddChild(task.id); }}
                className="p-1 rounded hover:bg-accent/60 text-muted-foreground/60 hover:text-foreground transition-colors"
                title="Add sub-task"
              >
                <Plus className="h-3.5 w-3.5" />
              </button>
            )}
            <button
              onClick={(e) => {
                e.stopPropagation();
                window.dispatchEvent(new CustomEvent("reference-task", {
                  detail: { id: task.id, title: task.title, status: task.status },
                }));
              }}
              className="p-1 rounded hover:bg-accent/60 text-muted-foreground/60 hover:text-foreground transition-colors"
              title="Reference in chat"
            >
              <AtSign className="h-3.5 w-3.5" />
            </button>
            {onDelete && (
              <div className="relative">
                <button
                  onClick={(e) => { e.stopPropagation(); setConfirmingDelete(true); }}
                  className="p-1 rounded hover:bg-destructive/10 text-muted-foreground/60 hover:text-destructive transition-colors"
                  title="Delete"
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </button>
                {confirmingDelete && (
                  <div data-delete-confirm className="absolute right-0 top-full mt-1 z-50 bg-popover border rounded-md shadow-md p-1.5 flex gap-1 whitespace-nowrap">
                    <button
                      onClick={(e) => { e.stopPropagation(); onDelete(task.id); setConfirmingDelete(false); }}
                      className="px-2 py-0.5 text-xs bg-destructive text-destructive-foreground rounded hover:bg-destructive/90"
                    >
                      Delete
                    </button>
                    <button
                      onClick={(e) => { e.stopPropagation(); setConfirmingDelete(false); }}
                      className="px-2 py-0.5 text-xs bg-muted text-muted-foreground rounded hover:bg-accent"
                    >
                      Cancel
                    </button>
                  </div>
                )}
              </div>
            )}
          </div>

          {/* People + Ref + Date */}
          <div className="flex items-center ml-1 gap-2">
            {task.created_by && task.created_by !== "local" && (
              <UserChip userName={task.created_by} className="text-blue-400" />
            )}
            {task.assigner && (
              <UserChip userName={task.assigner} className="text-amber-500" />
            )}
            {task.comment_count > 0 && (
              <span
                className="shrink-0 flex items-center gap-0.5 text-[10px] text-muted-foreground"
                title={`${task.comment_count} comment${task.comment_count > 1 ? "s" : ""}`}
              >
                <MessageSquare className="h-3 w-3" />
                {task.comment_count}
              </span>
            )}
            {task.ref_count > 0 && (
              <span
                className="shrink-0 text-[10px] text-primary/60 bg-primary/10 px-1.5 py-0.5 rounded tabular-nums font-medium"
                title={`Referenced ${task.ref_count} time${task.ref_count > 1 ? "s" : ""} in chat`}
              >
                Ref {task.ref_count}
              </span>
            )}
            <span
              className="shrink-0 text-[11px] text-muted-foreground pr-2 ml-2 tabular-nums text-right"
              title={new Date(task.updated_at).toLocaleString()}
            >
              {formatRelativeDate(task.updated_at)}
            </span>
          </div>
        </div>
      )}
    </div>
  );
}
