import { useEffect, useRef, useState } from "react";
import {
  ChevronRight, ChevronDown, FolderOpen, MoreHorizontal, Plus, Pencil, Trash2,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { UNASSIGNED_PROJECT } from "@/lib/constants";
import type { Project, Session } from "@/types";
import type { SessionGroupData } from "@/hooks/useGroupedSessions";
import { SessionRow } from "./SessionRow";

interface SessionGroupProps {
  group: SessionGroupData;
  isCollapsed: boolean;
  onToggleCollapsed: (groupKey: string) => void;
  onSelectProject: (projectId: string) => void;
  onNewSession: (projectId?: string) => void;
  onEditProject: (project: Project) => void;
  onDeleteProject: (project: Project) => void;
  // Rename (forwarded to rows)
  renamingSessionId: string | null;
  renameValue: string;
  renameRef: React.RefObject<HTMLInputElement | null>;
  onRenameChange: (v: string) => void;
  onRenameCommit: () => void;
  onRenameCancel: () => void;
  onRenameStart: (id: string, name: string) => void;
  // Drag
  dragSessionId: string | null;
  dragOverSessionId: string | null;
  dragOverPosition: "above" | "below" | null;
  onDragStart: (e: React.DragEvent, id: string) => void;
  onDragOver: (e: React.DragEvent, id: string, group: Session[]) => void;
  onDragEnd: () => void;
  onDrop: (e: React.DragEvent, id: string, group: Session[]) => void;
  dragDisabled: boolean;
  onTogglePin: (session: Session) => void;
  pinPendingIds: Set<string>;
  projectDragProps: React.HTMLAttributes<HTMLDivElement>;
  isProjectDragging: boolean;
  projectDropPosition: "above" | "below" | null;
  // Delete
  onDelete: (id: string) => void;
  // Selection
  onSelectSession: (id: string) => void;
}

export function SessionGroup({
  group, isCollapsed, onToggleCollapsed,
  onSelectProject, onNewSession, onEditProject, onDeleteProject,
  renamingSessionId, renameValue, renameRef,
  onRenameChange, onRenameCommit, onRenameCancel, onRenameStart,
  dragSessionId, dragOverSessionId, dragOverPosition,
  onDragStart, onDragOver, onDragEnd, onDrop,
  onDelete, dragDisabled, onTogglePin, pinPendingIds,
  projectDragProps, isProjectDragging, projectDropPosition,
  onSelectSession,
}: SessionGroupProps) {
  const groupKey = group.projectId || "__unbound__";

  // Project "..." menu — one per group, closes on outside click.
  const [menuOpen, setMenuOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!menuOpen) return;
    const handler = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenuOpen(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [menuOpen]);

  return (
    <div className={cn("mb-1", isProjectDragging && "opacity-40")}>
      {/* Project group header. The chevron and the rest of the row
          are separate click targets: chevron toggles expand/collapse,
          the row body opens the project's scratch session so the
          user can start a new chat (and use Files/Terminal/Workflow
          in the meantime) without committing to a real session
          until they send the first message. */}
      <div
        {...projectDragProps}
        title={group.projectId ? "Drag to reorder project" : undefined}
        className={cn(
          "group flex items-center rounded-lg hover:bg-accent/30 transition-colors relative",
          group.projectId && "cursor-grab active:cursor-grabbing",
          projectDropPosition === "above" && "before:absolute before:left-0 before:right-0 before:top-0 before:h-0.5 before:bg-primary",
          projectDropPosition === "below" && "after:absolute after:left-0 after:right-0 after:bottom-0 after:h-0.5 after:bg-primary",
        )}
      >
        <button
          onClick={(e) => { e.stopPropagation(); onToggleCollapsed(groupKey); }}
          className="flex items-center justify-center pl-2 py-1.5 shrink-0"
          title={isCollapsed ? "Expand" : "Collapse"}
        >
          {isCollapsed
            ? <ChevronRight className="h-3 w-3 text-muted-foreground/50" />
            : <ChevronDown className="h-3 w-3 text-muted-foreground/50" />}
        </button>
        <button
          onClick={() => {
            if (group.projectId) {
              onSelectProject(group.projectId);
            } else {
              // Unassigned group: keep legacy expand/collapse behavior
              // since there's no project to anchor a scratch session.
              onToggleCollapsed(groupKey);
            }
          }}
          className="flex-1 flex items-center gap-1.5 pl-1 pr-2 py-1.5 min-w-0"
        >
          <FolderOpen className="h-3.5 w-3.5 text-muted-foreground/50 shrink-0" />
          <span className="text-[13px] text-muted-foreground truncate">
            {group.project?.name === UNASSIGNED_PROJECT ? "Unassigned" : (group.project?.name || "Unassigned")}
          </span>
          <span className="text-[11px] text-muted-foreground/30 tabular-nums ml-1.5 shrink-0">
            {group.sessions.length}
          </span>
        </button>
        {group.projectId && group.project ? (
          <div className="flex items-center shrink-0 relative">
            {/* ... menu button */}
            <button
              onClick={(e) => { e.stopPropagation(); setMenuOpen((v) => !v); }}
              className="p-1 rounded hover:bg-accent text-muted-foreground/40 hover:text-muted-foreground transition-all opacity-0 group-hover:opacity-100"
              title="Project options"
            >
              <MoreHorizontal className="h-3 w-3" />
            </button>
            {/* New session button */}
            <button
              onClick={(e) => { e.stopPropagation(); onNewSession(group.projectId!); }}
              className="p-1 mr-1 rounded hover:bg-accent text-muted-foreground/40 hover:text-muted-foreground transition-all"
              title="New session"
            >
              <Plus className="h-3 w-3" />
            </button>
            {/* Dropdown menu */}
            {menuOpen && (
              <div
                ref={menuRef}
                className="absolute right-0 top-full mt-0.5 z-50 w-44 bg-popover border rounded-lg shadow-lg overflow-hidden"
              >
                <button
                  onClick={(e) => { e.stopPropagation(); setMenuOpen(false); onEditProject(group.project!); }}
                  className="w-full flex items-center gap-2 px-3 py-1.5 text-xs hover:bg-accent transition-colors"
                >
                  <Pencil className="h-3 w-3 text-muted-foreground" />
                  Edit
                </button>
                <button
                  onClick={(e) => { e.stopPropagation(); setMenuOpen(false); onDeleteProject(group.project!); }}
                  className="w-full flex items-center gap-2 px-3 py-1.5 text-xs text-destructive hover:bg-destructive/10 transition-colors"
                >
                  <Trash2 className="h-3 w-3" />
                  Delete
                </button>
              </div>
            )}
          </div>
        ) : (
          <div className="w-5 mr-1.5 shrink-0" />
        )}
      </div>

      {/* Sessions in group */}
      {!isCollapsed && group.sessions.map((s) => (
        <SessionRow
          key={s.id}
          session={s}
          groupSessions={group.sessions}
          isRenaming={renamingSessionId === s.id}
          renameValue={renameValue}
          renameRef={renameRef}
          onRenameChange={onRenameChange}
          onRenameCommit={onRenameCommit}
          onRenameCancel={onRenameCancel}
          onRenameStart={onRenameStart}
          isDragging={dragSessionId === s.id}
          isDropTarget={dragOverSessionId === s.id}
          dropPosition={dragOverPosition}
          onDragStart={onDragStart}
          onDragOver={onDragOver}
          onDragEnd={onDragEnd}
          onDrop={onDrop}
          dragDisabled={dragDisabled}
          onTogglePin={onTogglePin}
          pinPending={pinPendingIds.has(s.id)}
          onDelete={onDelete}
          onSelect={onSelectSession}
        />
      ))}
    </div>
  );
}
