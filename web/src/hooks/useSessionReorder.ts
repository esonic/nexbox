import { useState } from "react";
import { sessionsApi } from "@/lib/api";
import { useLayoutStore } from "@/stores/layout";
import { moveItem } from "@/lib/session-order";
import { toast } from "sonner";
import { useAppStore } from "@/stores/app";
import type { Session } from "@/types";

/**
 * Native HTML5 drag-and-drop reordering for session rows, scoped within a
 * folder/project group. Persists the new order via `sessionsApi.reorder` and
 * reloads from the server if that fails. `loadSessions` is injected so the
 * error fallback re-syncs the same source the sidebar loads from.
 */
export function useSessionReorder(loadSessions: () => void) {
  const setSessions = useAppStore((s) => s.setSessions);
  const [dragSessionId, setDragSessionId] = useState<string | null>(null);
  const [dragOverSessionId, setDragOverSessionId] = useState<string | null>(null);
  const [dragOverPosition, setDragOverPosition] = useState<"above" | "below" | null>(null);

  const handleDragStart = (e: React.DragEvent, sessionId: string) => {
    e.stopPropagation();
    if (useLayoutStore.getState().autoSortSessions) { e.preventDefault(); return; }
    setDragSessionId(sessionId);
    e.dataTransfer.setData("application/x-nexbox-session", sessionId);
    e.dataTransfer.effectAllowed = "move";
    // Make the drag ghost semi-transparent
    if (e.currentTarget instanceof HTMLElement) {
      e.dataTransfer.setDragImage(e.currentTarget, 0, 0);
    }
  };

  const handleDragOver = (e: React.DragEvent, sessionId: string, groupSessions: Session[]) => {
    if (!dragSessionId) return;
    // Match the rendered group, including the merged Unassigned group.
    const source = groupSessions.find((s) => s.id === dragSessionId);
    const target = groupSessions.find((s) => s.id === sessionId);
    if (!source || !target || !!source.is_pinned !== !!target.is_pinned) {
      setDragOverSessionId(null);
      setDragOverPosition(null);
      return;
    }
    e.preventDefault();
    e.stopPropagation();
    e.dataTransfer.dropEffect = "move";
    if (!dragSessionId || dragSessionId === sessionId) {
      setDragOverSessionId(null);
      setDragOverPosition(null);
      return;
    }
    // Determine if cursor is in the top or bottom half of the target
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
    const midY = rect.top + rect.height / 2;
    setDragOverSessionId(sessionId);
    setDragOverPosition(e.clientY < midY ? "above" : "below");
  };

  const handleDragEnd = () => {
    setDragSessionId(null);
    setDragOverSessionId(null);
    setDragOverPosition(null);
  };

  const handleDrop = (e: React.DragEvent, targetSessionId: string, groupSessions: Session[]) => {
    e.preventDefault();
    e.stopPropagation();
    if (useLayoutStore.getState().autoSortSessions) { handleDragEnd(); return; }
    if (!dragSessionId || dragSessionId === targetSessionId) {
      handleDragEnd();
      return;
    }

    // A drag cannot change project or pin status.
    const source = groupSessions.find((s) => s.id === dragSessionId);
    const target = groupSessions.find((s) => s.id === targetSessionId);
    if (!source || !target || !!source.is_pinned !== !!target.is_pinned) {
      handleDragEnd();
      return;
    }
    const ids = moveItem(groupSessions.map((s) => s.id), dragSessionId, targetSessionId, dragOverPosition === "below");
    handleDragEnd();

    // Optimistic update
    const orderMap = new Map(ids.map((id, i) => [id, i + 1]));
    const updated = useAppStore.getState().sessions.map((s) =>
      orderMap.has(s.id) ? { ...s, sort_order: orderMap.get(s.id)! } : s
    );
    setSessions(updated);

    // Persist to server
    sessionsApi.reorder(ids).catch((err) => {
      console.error("Failed to reorder sessions:", err);
      toast.error("Failed to save session order");
      loadSessions();
    });
  };

  return {
    dragSessionId,
    dragOverSessionId,
    dragOverPosition,
    handleDragStart,
    handleDragOver,
    handleDragEnd,
    handleDrop,
  };
}
