import { useMemo } from "react";
import { useLayoutStore } from "@/stores/layout";
import { sortSessions } from "@/lib/session-order";
import { UNASSIGNED_PROJECT } from "@/lib/constants";
import type { Project, Session } from "@/types";

export interface SessionGroupData {
  projectId: string | null;
  project: Project | null;
  sessions: Session[];
}

/**
 * Groups sessions by project into the ordered list the sidebar renders:
 * one group per "working" project (those with a local dir), in saved project
 * order (newest first initially), with unassigned sessions appended last.
 * Hidden ("scratch") sessions are excluded. Pinned sessions lead each group;
 * other sessions follow manual order or conversation activity.
 */
export function useGroupedSessions(sessions: Session[], projects: Project[]): SessionGroupData[] {
  const autoSort = useLayoutStore((s) => s.autoSortSessions);
  const projectOrder = useLayoutStore((s) => s.projectOrder);
  const projectMap = useMemo(() => {
    const map = new Map<string, Project>();
    for (const p of projects) map.set(p.id, p);
    return map;
  }, [projects]);

  return useMemo(() => {
    // Group sessions by project_id
    const sessionsByProject = new Map<string, Session[]>();
    const unbound: Session[] = [];

    for (const sess of sessions) {
      // Hidden ("scratch") sessions are per-project staging buffers — they
      // live in the store so the chat panel can render them, but the
      // sidebar list and project session count both ignore them until
      // they're promoted on first user message.
      if (sess.is_hidden) continue;
      if (sess.project_id) {
        const proj = projectMap.get(sess.project_id);
        if (!proj) continue; // project unlinked locally — hide session
        if (proj.name !== UNASSIGNED_PROJECT) {
          const list = sessionsByProject.get(sess.project_id) || [];
          list.push(sess);
          sessionsByProject.set(sess.project_id, list);
          continue;
        }
      }
      unbound.push(sess);
    }

    const result: SessionGroupData[] = [];

    // Only show "Working Projects" (those with a local dir mapping) in the sidebar
    for (const p of projects.filter((p) => p.name !== UNASSIGNED_PROJECT && p.git_repo_path)) {
      result.push({
        projectId: p.id,
        project: p,
        sessions: sortSessions(sessionsByProject.get(p.id) || [], autoSort ? "activity" : "manual"),
      });
    }

    // Keep saved project order; newly linked projects appear first.
    const ranks = new Map(projectOrder.map((id, i) => [id, i]));
    result.sort((a, b) => {
      const order = (ranks.get(a.projectId!) ?? -1) - (ranks.get(b.projectId!) ?? -1);
      if (order) return order;
      const aTime = new Date(a.project?.created_at || 0).getTime();
      const bTime = new Date(b.project?.created_at || 0).getTime();
      return bTime - aTime;
    });

    // Add unbound sessions at the end (Unassigned group)
    if (unbound.length > 0) {
      result.push({ projectId: null, project: null, sessions: sortSessions(unbound, autoSort ? "activity" : "manual") });
    }

    return result;
  }, [sessions, projects, projectMap, autoSort, projectOrder]);
}
