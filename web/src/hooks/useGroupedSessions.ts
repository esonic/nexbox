import { useMemo } from "react";
import { UNASSIGNED_PROJECT } from "@/lib/constants";
import type { Project, Session } from "@/types";

export interface SessionGroupData {
  projectId: string | null;
  project: Project | null;
  sessions: Session[];
}

/**
 * Groups sessions by project into the ordered list the sidebar renders:
 * one group per "working" project (those with a local dir), newest project
 * first, with unassigned sessions appended last. Hidden ("scratch") sessions
 * are excluded. Within a group, sessions sort by `sort_order` (manual
 * drag-reorder) then newest-created first.
 */
export function useGroupedSessions(sessions: Session[], projects: Project[]): SessionGroupData[] {
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

    const sortSessions = (list: Session[]) =>
      [...list].sort((a, b) => {
        // 1. 优先按 sort_order 排序（升序）
        if (a.sort_order !== b.sort_order) {
          return a.sort_order - b.sort_order;
        }
        // 2. sort_order 相同时，按 created_at 倒序（新创建的在前）
        const ta = new Date(a.created_at).getTime();
        const tb = new Date(b.created_at).getTime();
        return tb - ta;
      });

    // Only show "Working Projects" (those with a local dir mapping) in the sidebar
    for (const p of projects.filter((p) => p.name !== UNASSIGNED_PROJECT && p.git_repo_path)) {
      result.push({
        projectId: p.id,
        project: p,
        sessions: sortSessions(sessionsByProject.get(p.id) || []),
      });
    }

    // Sort project groups by created_at (newest first)
    result.sort((a, b) => {
      const aTime = new Date(a.project?.created_at || 0).getTime();
      const bTime = new Date(b.project?.created_at || 0).getTime();
      return bTime - aTime;
    });

    // Add unbound sessions at the end (Unassigned group)
    if (unbound.length > 0) {
      result.push({ projectId: null, project: null, sessions: sortSessions(unbound) });
    }

    return result;
  }, [sessions, projects, projectMap]);
}
