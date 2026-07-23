import { useEffect, useState } from "react";
import { useLocation } from "react-router-dom";
import { X, Circle } from "lucide-react";
import { cn, formatRelativeDate } from "@/lib/utils";
import { useAppStore } from "@/stores/app";
import type { Session } from "@/types";

interface SessionRowProps {
  session: Session;
  groupSessions: Session[];
  // Rename
  isRenaming: boolean;
  renameValue: string;
  renameRef: React.RefObject<HTMLInputElement | null>;
  onRenameChange: (v: string) => void;
  onRenameCommit: () => void;
  onRenameCancel: () => void;
  onRenameStart: (id: string, name: string) => void;
  // Drag
  isDragging: boolean;
  isDropTarget: boolean;
  dropPosition: "above" | "below" | null;
  onDragStart: (e: React.DragEvent, id: string) => void;
  onDragOver: (e: React.DragEvent, id: string) => void;
  onDragEnd: () => void;
  onDrop: (e: React.DragEvent, id: string, group: Session[]) => void;
  // Delete
  isConfirmingDelete: boolean;
  onConfirmDelete: (id: string) => void;
  onCancelDelete: () => void;
  onDelete: (id: string) => void;
  // Selection
  onSelect: (id: string) => void;
}

export function SessionRow({
  session: sess, groupSessions,
  isRenaming, renameValue, renameRef,
  onRenameChange, onRenameCommit, onRenameCancel, onRenameStart,
  isDragging, isDropTarget, dropPosition,
  onDragStart, onDragOver, onDragEnd, onDrop,
  isConfirmingDelete, onConfirmDelete, onCancelDelete, onDelete,
  onSelect,
}: SessionRowProps) {
  const { pathname } = useLocation();
  // Live per-session status comes straight from the store so a row re-renders
  // only when its own dot/active state changes.
  const isStreaming = useAppStore((s) => !!s.streamingSessions[sess.id]);
  const isUnread = useAppStore((s) => !!s.unreadSessions[sess.id]);
  const isActive = useAppStore((s) => s.activeSessionId === sess.id) && pathname === "/";
  const markSessionUnread = useAppStore((s) => s.markSessionUnread);

  // Right-click context menu, positioned at the cursor. Kept local to the row
  // so each session owns its own menu; closes on any outside interaction.
  const [menuPos, setMenuPos] = useState<{ x: number; y: number } | null>(null);
  useEffect(() => {
    if (!menuPos) return;
    const close = () => setMenuPos(null);
    window.addEventListener("mousedown", close);
    window.addEventListener("scroll", close, true);
    window.addEventListener("resize", close);
    window.addEventListener("keydown", close);
    return () => {
      window.removeEventListener("mousedown", close);
      window.removeEventListener("scroll", close, true);
      window.removeEventListener("resize", close);
      window.removeEventListener("keydown", close);
    };
  }, [menuPos]);

  return (
    <div
      draggable={!isRenaming}
      onDragStart={(e) => onDragStart(e, sess.id)}
      onDragOver={(e) => onDragOver(e, sess.id)}
      onDragEnd={onDragEnd}
      onDrop={(e) => onDrop(e, sess.id, groupSessions)}
      onContextMenu={(e) => {
        e.preventDefault();
        setMenuPos({ x: e.clientX, y: e.clientY });
      }}
      className={cn(
        "group flex items-center rounded-lg transition-colors mb-px relative",
        isActive ? "bg-accent" : "hover:bg-accent/50",
        isDragging && "opacity-40",
        isDropTarget && dropPosition === "above" && "before:absolute before:left-2 before:right-2 before:top-0 before:h-0.5 before:bg-primary before:rounded-full",
        isDropTarget && dropPosition === "below" && "after:absolute after:left-2 after:right-2 after:bottom-0 after:h-0.5 after:bg-primary after:rounded-full",
      )}
    >
      {isStreaming ? (
        <span className="absolute left-2.5 top-1/2 -translate-y-1/2 h-1.5 w-1.5 rounded-full bg-green-500 animate-pulse" />
      ) : isUnread ? (
        <span
          className="absolute left-2.5 top-1/2 -translate-y-1/2 h-1.5 w-1.5 rounded-full bg-sky-400"
          title="Unread"
        />
      ) : null}
      {menuPos && (
        <div
          data-session-context-menu
          style={{ position: "fixed", left: menuPos.x, top: menuPos.y }}
          onMouseDown={(e) => e.stopPropagation()}
          className="z-[60] min-w-[9rem] bg-popover border rounded-md shadow-md p-1 text-popover-foreground"
        >
          <button
            onClick={() => { markSessionUnread(sess.id); setMenuPos(null); }}
            className="w-full flex items-center gap-2 px-2 py-1.5 text-xs rounded hover:bg-accent transition-colors"
          >
            <Circle className="h-3 w-3 fill-sky-400 text-sky-400" />
            Mark as unread
          </button>
        </div>
      )}
      {isRenaming ? (
        <input
          ref={renameRef}
          value={renameValue}
          onChange={(e) => onRenameChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.nativeEvent.isComposing) onRenameCommit();
            if (e.key === "Escape") onRenameCancel();
          }}
          onBlur={onRenameCommit}
          className="flex-1 bg-transparent outline-none text-[13px] font-medium pl-7 pr-3 py-1.5 min-w-0"
        />
      ) : (
        <button
          onClick={() => onSelect(sess.id)}
          onDoubleClick={() => onRenameStart(sess.id, sess.name || "")}
          className={cn(
            "flex-1 flex items-center gap-1.5 pl-7 pr-2 py-1.5 text-[13px] min-w-0 transition-colors",
            isActive
              ? "text-foreground font-bold"
              : "text-muted-foreground hover:text-foreground"
          )}
        >
          <span className="truncate">{sess.name || "New Session"}</span>
        </button>
      )}
      <div className="relative shrink-0 w-5 h-5 mr-1.5 flex items-center justify-center">
        <span className="text-[11px] text-muted-foreground/40 tabular-nums group-hover:opacity-0 transition-opacity">
          {formatRelativeDate(sess.updated_at || sess.created_at)}
        </span>
        <button
          onClick={(e) => { e.stopPropagation(); onConfirmDelete(sess.id); }}
          className="absolute inset-0 flex items-center justify-center opacity-0 group-hover:opacity-100 p-1 rounded hover:bg-accent text-muted-foreground/40 hover:text-destructive transition-all"
          title="Move to trash"
        >
          <X className="h-3 w-3" />
        </button>
        {isConfirmingDelete && (
          <div data-delete-confirm className="absolute right-0 top-full mt-1 z-50 bg-popover border rounded-md shadow-md p-1.5 flex gap-1 whitespace-nowrap">
            <button
              onClick={(e) => { e.stopPropagation(); onDelete(sess.id); onCancelDelete(); }}
              className="px-2 py-0.5 text-xs bg-destructive text-destructive-foreground rounded hover:bg-destructive/90"
            >
              Move to Trash
            </button>
            <button
              onClick={(e) => { e.stopPropagation(); onCancelDelete(); }}
              className="px-2 py-0.5 text-xs bg-muted text-muted-foreground rounded hover:bg-accent"
            >
              Cancel
            </button>
          </div>
        )}
      </div>
    </div>
  );
}
