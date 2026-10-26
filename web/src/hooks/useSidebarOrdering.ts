import { useState } from "react";
import { toast } from "sonner";
import { sessionsApi } from "@/lib/api";
import { sortSessions, moveItem } from "@/lib/session-order";
import type { SessionSort } from "@/lib/session-order";
import { useAppStore } from "@/stores/app";
import { useLayoutStore } from "@/stores/layout";
import type { SessionGroupData } from "./useGroupedSessions";

export function useSidebarOrdering(groups: SessionGroupData[], reload: () => void) {
  const autoSort = useLayoutStore((s) => s.autoSortSessions);
  const setAutoSort = useLayoutStore((s) => s.setAutoSortSessions);
  const [saving, setSaving] = useState(false);
  const [dragProject, setDragProject] = useState<string | null>(null);
  const [projectDrop, setProjectDrop] = useState<{ id: string; below: boolean } | null>(null);

  const saveOrder = async (sort: SessionSort) => {
    if (saving) return;
    setSaving(true);
    try {
      // One request keeps all groups' new orders atomic on the server. The
      // numeric ranks can be global: only their relative order per group matters.
      const ids = groups.flatMap((g) => sortSessions(g.sessions, sort).map((s) => s.id));
      if (ids.length) await sessionsApi.reorder(ids);
      const ranks = new Map(ids.map((id, i) => [id, i + 1]));
      useAppStore.getState().setSessions(useAppStore.getState().sessions.map((s) =>
        ranks.has(s.id) ? { ...s, sort_order: ranks.get(s.id)! } : s));
      setAutoSort(false);
    } catch (err) {
      toast.error("Failed to save session order", { description: String(err) });
      reload();
    } finally {
      setSaving(false);
    }
  };

  const toggleAutoSort = () => {
    if (saving) return;
    // Capture the visible order when leaving automatic mode.
    if (autoSort) void saveOrder("activity");
    else setAutoSort(true);
  };

  const endProjectDrag = () => { setDragProject(null); setProjectDrop(null); };
  const projectDragProps = (id: string | null) => ({
    draggable: !!id,
    onDragStart: (e: React.DragEvent) => {
      if (!id) return;
      e.stopPropagation();
      e.dataTransfer.setData("application/x-nexbox-project", id);
      e.dataTransfer.effectAllowed = "move";
      setDragProject(id);
    },
    onDragOver: (e: React.DragEvent) => {
      if (!id || !dragProject || id === dragProject) return;
      e.preventDefault();
      e.stopPropagation();
      e.dataTransfer.dropEffect = "move";
      const rect = e.currentTarget.getBoundingClientRect();
      const below = e.clientY >= rect.top + rect.height / 2;
      setProjectDrop((prev) => prev?.id === id && prev.below === below ? prev : { id, below });
    },
    onDragEnd: endProjectDrag,
    onDrop: (e: React.DragEvent) => {
      if (!id || !dragProject) return;
      e.preventDefault();
      e.stopPropagation();
      const rect = e.currentTarget.getBoundingClientRect();
      const ids = groups.flatMap((g) => g.projectId ? [g.projectId] : []);
      const visibleOrder = moveItem(ids, dragProject, id, e.clientY >= rect.top + rect.height / 2);
      // Retain positions for projects temporarily unlinked from this browser.
      const hidden = useLayoutStore.getState().projectOrder.filter((key) => !ids.includes(key));
      useLayoutStore.getState().setProjectOrder([...visibleOrder, ...hidden]);
      endProjectDrag();
    },
  });

  return { autoSort, saving, saveOrder, toggleAutoSort, projectDragProps, dragProject, projectDrop };
}
