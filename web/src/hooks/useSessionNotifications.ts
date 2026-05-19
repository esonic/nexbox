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

        const session = state.sessions.find((s) => s.id === id);
        const name = session?.name || "Session";
        new Notification("Session complete", {
          body: `「${name}」finished`,
          icon: "/favicon.svg",
        });
      }
    });

    return unsubscribe;
  }, []);
}
