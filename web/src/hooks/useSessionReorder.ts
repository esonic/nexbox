import { useState } from "react";
import { sessionsApi } from "@/lib/api";
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
    setDragSessionId(sessionId);
    e.dataTransfer.effectAllowed = "move";
    // Make the drag ghost semi-transparent
    if (e.currentTarget instanceof HTMLElement) {
      e.dataTransfer.setDragImage(e.currentTarget, 0, 0);
    }
  };

  const handleDragOver = (e: React.DragEvent, sessionId: string) => {
    e.preventDefault();
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
    if (!dragSessionId || dragSessionId === targetSessionId) {
      handleDragEnd();
      return;
    }

    // Build new order for this group only (drag is scoped within a folder)
    const ids = groupSessions.map((s) => s.id);
    const fromIdx = ids.indexOf(dragSessionId);
    let toIdx = ids.indexOf(targetSessionId);
    if (fromIdx === -1 || toIdx === -1) { handleDragEnd(); return; }

    // Remove dragged item
    ids.splice(fromIdx, 1);
    // Re-find target index after removal
    toIdx = ids.indexOf(targetSessionId);
    // Insert at correct position
    if (dragOverPosition === "below") toIdx += 1;
    ids.splice(toIdx, 0, dragSessionId);

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
