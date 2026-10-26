import { useRef, useState } from "react";
import { toast } from "sonner";
import { sessionsApi } from "@/lib/api";
import { useAppStore } from "@/stores/app";
import type { Session } from "@/types";

export function useSessionPin() {
  const [pinPendingIds, setPinPendingIds] = useState<Set<string>>(new Set());
  const pinRequests = useRef(new Set<string>());

  const handleTogglePin = async (session: Session) => {
    if (pinRequests.current.has(session.id)) return;
    pinRequests.current.add(session.id);
    setPinPendingIds(new Set(pinRequests.current));
    try {
      const updated = await sessionsApi.update(session.id, { is_pinned: !session.is_pinned });
      const store = useAppStore.getState();
      // Activity, manual order and metadata may have changed while the pin
      // request was in flight. Merge only the field this action owns.
      store.setSessions(store.sessions.map((s) =>
        s.id === session.id ? { ...s, is_pinned: updated.is_pinned } : s));
    } catch (err) {
      toast.error("Failed to update pin", { description: String(err) });
    } finally {
      pinRequests.current.delete(session.id);
      setPinPendingIds(new Set(pinRequests.current));
    }
  };

  return { pinPendingIds, handleTogglePin };
}
