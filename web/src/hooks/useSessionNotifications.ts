import { useEffect } from "react";
import { useAppStore } from "@/stores/app";

export function useSessionNotifications() {
  useEffect(() => {
    if (typeof Notification === "undefined") return;

    if (Notification.permission === "default") {
      Notification.requestPermission();
    }

    const unsubscribe = useAppStore.subscribe((state, prevState) => {
      if (Notification.permission !== "granted") return;

      const prev = prevState.streamingSessions;
      const curr = state.streamingSessions;

      for (const [id, wasStreaming] of Object.entries(prev)) {
        if (!wasStreaming || curr[id]) continue;

        // Skip if the user is actively watching this session in the foreground
        if (!document.hidden && state.activeSessionId === id) continue;

        // Defense-in-depth against the server-side broadcast bug: if a session
        // event for an id we don't own somehow reaches this client, our
        // sessions list won't contain it. Don't surface a notification in
        // that case — the server filter is the real fix, this is the belt.
        const session = state.sessions.find((s) => s.id === id);
        if (!session) continue;

        new Notification("Session complete", {
          body: `「${session.name || "Session"}」finished`,
          icon: "/favicon.svg",
        });
      }
    });

    return unsubscribe;
  }, []);
}
