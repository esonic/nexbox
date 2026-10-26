import type { Session } from "@/types";

export type SessionSort = "manual" | "created" | "activity";

// Metadata changes (updated_at) never count as conversation activity.
export function sessionActivity(session: Session): string {
  return session.last_active_at || session.created_at;
}

export function sortSessions(sessions: Session[], sort: SessionSort): Session[] {
  return [...sessions].sort((a, b) => {
    const pinned = Number(!!b.is_pinned) - Number(!!a.is_pinned);
    if (pinned) return pinned;
    // Pinned rows retain their manual order under every sort mode.
    if (sort === "manual" || a.is_pinned) {
      const order = a.sort_order - b.sort_order;
      if (order) return order;
    } else {
      const time = Date.parse(sort === "activity" ? sessionActivity(b) : b.created_at)
        - Date.parse(sort === "activity" ? sessionActivity(a) : a.created_at);
      if (time) return time;
    }
    return Date.parse(b.created_at) - Date.parse(a.created_at) || a.id.localeCompare(b.id);
  });
}

export function moveItem(ids: string[], source: string, target: string, below: boolean): string[] {
  if (source === target || !ids.includes(source) || !ids.includes(target)) return ids;
  const result = ids.filter((id) => id !== source);
  result.splice(result.indexOf(target) + Number(below), 0, source);
  return result;
}
