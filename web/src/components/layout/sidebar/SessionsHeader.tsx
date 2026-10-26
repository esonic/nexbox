import { useEffect, useRef, useState } from "react";
import { History, Trash2, X, RotateCcw, ArrowDownWideNarrow, FolderPlus } from "lucide-react";
import { toast } from "sonner";
import { sessionsApi } from "@/lib/api";
import { cn, formatRelativeDate } from "@/lib/utils";
import { useAppStore } from "@/stores/app";
import {
  DropdownMenu, DropdownMenuTrigger, DropdownMenuContent,
  DropdownMenuItem, DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import type { SessionSort } from "@/lib/session-order";
import type { Session } from "@/types";

/**
 * "Sessions" section header with the recently-deleted (trash) popover. Owns its
 * own open/confirm state and trash data; restoring a session pushes it back
 * into the store.
 */
const sortItemClassName = "justify-between rounded-md text-[13px] leading-5 text-muted-foreground focus:text-foreground";

interface SessionsHeaderProps {
  onNewProject: () => void;
  autoSort: boolean;
  sorting: boolean;
  onSort: (sort: SessionSort) => void;
  onToggleAutoSort: () => void;
}

export function SessionsHeader({ onNewProject, autoSort, sorting, onSort, onToggleAutoSort }: SessionsHeaderProps) {
  const setSessions = useAppStore((s) => s.setSessions);

  const [showTrash, setShowTrash] = useState(false);
  const [trashedSessions, setTrashedSessions] = useState<Session[]>([]);
  const [confirmEmptyTrash, setConfirmEmptyTrash] = useState(false);
  const trashRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!showTrash) return;
    const handler = (e: MouseEvent) => {
      if (trashRef.current && !trashRef.current.contains(e.target as Node)) {
        setShowTrash(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [showTrash]);

  const handleOpenTrash = async () => {
    try {
      const data = await sessionsApi.listTrashed();
      setTrashedSessions(data.sessions || []);
      setShowTrash(true);
    } catch (e) {
      console.error("Failed to load trash:", e);
      toast.error("Failed to load trash", { description: String(e) });
    }
  };

  const handleRestoreSession = async (sessionId: string) => {
    try {
      const restored = await sessionsApi.restore(sessionId);
      setTrashedSessions((prev) => prev.filter((s) => s.id !== sessionId));
      setSessions([restored, ...useAppStore.getState().sessions]);
    } catch (e) {
      console.error("Failed to restore session:", e);
      toast.error("Failed to restore session", { description: String(e) });
    }
  };

  const handleEmptyTrash = async () => {
    try {
      setConfirmEmptyTrash(false);
      await sessionsApi.emptyTrash();
      setTrashedSessions([]);
      toast.success("Trash emptied");
    } catch (e) {
      console.error("Failed to empty trash:", e);
      toast.error("Failed to empty trash", { description: String(e) });
    }
  };

  return (
    <div className="flex items-center justify-between pl-4 pr-3 pt-2 pb-1.5 relative" ref={trashRef}>
      <span className="text-[13px] font-medium text-muted-foreground/60">Sessions</span>
      <div className="flex items-center gap-0.5">
        <button onClick={onNewProject} className="p-1 rounded hover:bg-accent text-muted-foreground/40 hover:text-muted-foreground" title="New project">
          <FolderPlus className="h-3.5 w-3.5" />
        </button>
        <DropdownMenu onOpenChange={(open) => { if (open) setShowTrash(false); }}>
          <DropdownMenuTrigger asChild>
            <button
              disabled={sorting}
              className={cn(
                "p-1 rounded hover:bg-accent hover:text-muted-foreground transition-colors disabled:opacity-40",
                autoSort ? "text-muted-foreground/70" : "text-muted-foreground/40",
              )}
              title={autoSort ? "Sort sessions · automatic activity sorting on" : "Sort sessions"}
              aria-label="Sort sessions"
            >
              <ArrowDownWideNarrow className="h-3.5 w-3.5" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-52">
            <DropdownMenuItem className={sortItemClassName} disabled={sorting} onSelect={() => onSort("created")}>
              Sort by creation time
            </DropdownMenuItem>
            <DropdownMenuItem className={sortItemClassName} disabled={sorting} onSelect={() => onSort("activity")}>
              Sort by last activity
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem className={sortItemClassName} role="menuitemcheckbox" aria-checked={autoSort} disabled={sorting} onSelect={(event) => {
              event.preventDefault();
              onToggleAutoSort();
            }}>
              Auto-sort by activity
              <span
                aria-hidden="true"
                className={cn(
                  "relative h-4 w-7 shrink-0 rounded-full transition-colors motion-reduce:transition-none",
                  autoSort ? "bg-muted-foreground/60" : "bg-muted-foreground/20",
                )}
              >
                <span className={cn(
                  "absolute left-0.5 top-0.5 h-3 w-3 rounded-full bg-background shadow-sm transition-transform duration-150 motion-reduce:transition-none",
                  autoSort && "translate-x-3",
                )} />
              </span>
            </DropdownMenuItem>
            {autoSort && (
              <p className="px-2 pt-1 pb-1.5 text-[11px] leading-4 text-muted-foreground/60">
                Session dragging is disabled while auto-sort is on.
              </p>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
        <button
          onClick={handleOpenTrash}
          className="p-1 rounded hover:bg-accent text-muted-foreground/40 hover:text-muted-foreground transition-colors"
          title="Recently deleted"
        >
          <History className="h-3.5 w-3.5" />
        </button>
      </div>
      {/* Trash popover */}
      {showTrash && (
        <div className="absolute left-0 right-0 top-full z-50 bg-popover border border-border rounded-lg shadow-lg overflow-hidden">
          <div className="px-3 py-2 border-b border-border bg-muted/30 flex items-center justify-between gap-2">
            <div className="flex items-center gap-1.5">
              <History className="h-3.5 w-3.5 text-muted-foreground/60 shrink-0" />
              <span className="text-xs font-medium text-muted-foreground">Recently Deleted</span>
            </div>
            <div className="flex items-center gap-0.5 relative">
              {trashedSessions.length > 0 && (
                <button
                  onClick={() => setConfirmEmptyTrash(true)}
                  className="p-0.5 rounded hover:bg-destructive/20 text-muted-foreground/40 hover:text-destructive transition-colors"
                  title="Empty trash"
                >
                  <Trash2 className="h-3 w-3" />
                </button>
              )}
              {confirmEmptyTrash && (
                <div className="absolute right-0 top-full mt-1 z-50 bg-popover border rounded-md shadow-md p-1.5 flex gap-1 whitespace-nowrap">
                  <button
                    onClick={handleEmptyTrash}
                    className="px-2 py-0.5 text-xs bg-destructive text-destructive-foreground rounded hover:bg-destructive/90"
                  >
                    Empty Trash
                  </button>
                  <button
                    onClick={() => setConfirmEmptyTrash(false)}
                    className="px-2 py-0.5 text-xs bg-muted text-muted-foreground rounded hover:bg-accent"
                  >
                    Cancel
                  </button>
                </div>
              )}
              <button
                onClick={() => setShowTrash(false)}
                className="p-0.5 rounded hover:bg-accent text-muted-foreground/40 hover:text-muted-foreground transition-colors"
              >
                <X className="h-3 w-3" />
              </button>
            </div>
          </div>
          <div className="px-3 py-1.5 border-b border-border/50">
            <p className="text-[11px] text-muted-foreground/60">
              Sessions are permanently deleted after 7 days.
            </p>
          </div>
          <div className="max-h-64 overflow-y-auto">
            {trashedSessions.length === 0 ? (
              <div className="px-3 py-4 text-[12px] text-muted-foreground/40 text-center">
                No recently deleted sessions
              </div>
            ) : (
              trashedSessions.map((s) => (
                <div
                  key={s.id}
                  className="flex items-center gap-2 px-3 py-1.5 hover:bg-accent/50 transition-colors"
                >
                  <span className="shrink-0 text-[11px] text-muted-foreground/50 tabular-nums">
                    {s.deleted_at ? formatRelativeDate(s.deleted_at) : ""}
                  </span>
                  <span className="flex-1 text-[12px] text-foreground truncate min-w-0">
                    {s.name || "New Session"}
                  </span>
                  <button
                    onClick={() => handleRestoreSession(s.id)}
                    className="shrink-0 p-1 rounded hover:bg-accent text-muted-foreground/50 hover:text-foreground transition-colors"
                    title="Restore session"
                  >
                    <RotateCcw className="h-3 w-3" />
                  </button>
                </div>
              ))
            )}
          </div>
        </div>
      )}
    </div>
  );
}
