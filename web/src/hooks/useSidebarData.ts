import { useCallback, useEffect, useRef, useState } from "react";
import { useLocation } from "react-router-dom";
import { toast } from "sonner";
import { sessionsApi, projectsApi, serverApi, marketplaceApi } from "@/lib/api";
import type { CloudInfo } from "@/lib/api";
import { globalWS } from "@/lib/ws";
import { useAppStore } from "@/stores/app";

/**
 * Owns the sidebar's data spine: session/project loading, the WebSocket and
 * window-event wiring that keeps them fresh, server-mode detection, and the
 * cloud notification "unread" dot. Returns the loaders (for callers that need
 * to re-sync), whether projects have loaded at least once (the auto-select
 * effect gates on it), plus the derived cloud state.
 */
export function useSidebarData() {
  const location = useLocation();
  const setSessions = useAppStore((s) => s.setSessions);
  const initStreamingSessions = useAppStore((s) => s.initStreamingSessions);
  const setProjects = useAppStore((s) => s.setProjects);
  const setConnectStatus = useAppStore((s) => s.setConnectStatus);
  const setMarketplaceUpdateCount = useAppStore((s) => s.setMarketplaceUpdateCount);

  const [cloudInfo, setCloudInfo] = useState<CloudInfo | null>(null);
  const cloudUserRef = useRef<string | null>(null);

  // Notification unread dot (localStorage-backed; separate from per-session unread)
  const [notificationUnread, setNotificationUnread] = useState(
    () => localStorage.getItem("notification_unread") === "1"
  );
  const markNotificationUnread = () => {
    localStorage.setItem("notification_unread", "1");
    setNotificationUnread(true);
  };
  const clearNotificationUnread = () => {
    localStorage.removeItem("notification_unread");
    setNotificationUnread(false);
  };

  const loadSessions = useCallback(async () => {
    try {
      const data = await sessionsApi.list();
      const list = data.sessions || [];
      // The server filters hidden ("scratch") sessions out of the list, so
      // refetching would otherwise wipe an active scratch session and
      // orphan its activeSessionId. Preserve locally-known hidden sessions
      // that the server didn't return; the server's copy wins for any
      // session it does return (e.g. a just-promoted one).
      const remoteIds = new Set(list.map((s) => s.id));
      const prev = useAppStore.getState().sessions;
      const preservedScratch = prev.filter((s) => s.is_hidden && !remoteIds.has(s.id));
      setSessions([...list, ...preservedScratch]);
      initStreamingSessions(data.active_session_ids || []);
    } catch (e) {
      console.error("Failed to load sessions:", e);
    }
  }, [setSessions, initStreamingSessions]);

  // Tracks whether projectsApi.list has completed at least once. The auto-select
  // effect waits on this so it doesn't fall through to "first visible session"
  // before the Default-project check has had a chance to run.
  const [projectsLoaded, setProjectsLoaded] = useState(false);

  const loadProjects = useCallback(async () => {
    try {
      const data = await projectsApi.list();
      setProjects(data || []);
    } catch (e) {
      console.error("Failed to load projects:", e);
    } finally {
      setProjectsLoaded(true);
    }
  }, [setProjects]);

  const checkMarketplaceUpdates = useCallback(() => {
    // No remote registry – update count is always 0
    setMarketplaceUpdateCount(0);
  }, [setMarketplaceUpdateCount]);

  useEffect(() => {
    loadSessions();
    loadProjects();
  }, [loadSessions, loadProjects]);

  useEffect(() => {
    const timer = setTimeout(checkMarketplaceUpdates, 60 * 1000);
    const interval = setInterval(checkMarketplaceUpdates, 30 * 60 * 1000);
    return () => { clearTimeout(timer); clearInterval(interval); };
  }, [checkMarketplaceUpdates]);

  useEffect(() => {
    window.addEventListener("marketplace-package-updated", checkMarketplaceUpdates);
    return () => window.removeEventListener("marketplace-package-updated", checkMarketplaceUpdates);
  }, [checkMarketplaceUpdates]);

  useEffect(() => {
    const handler = () => { loadSessions(); loadProjects(); };
    window.addEventListener("session-updated", handler);
    return () => window.removeEventListener("session-updated", handler);
  }, [loadSessions, loadProjects]);

  // Session event streams are only observed for the currently open session.
  // If another session advances, invalidate its cached transcript so the next
  // visit reloads the messages that this client did not observe.
  useEffect(() => {
    return globalWS.subscribeUnrouted((sessionId) => {
      const store = useAppStore.getState();
      if (store.chatMessages[sessionId]?.length) {
        store.setChatMessages(sessionId, []);
      }
    });
  }, []);

  useEffect(() => {
    return globalWS.subscribeGlobal((event) => {
      if (event.type === "change" && event.entity_type === "session") {
        loadSessions();
      }
      if (event.type === "change" && event.entity_type === "notification") {
        const me = cloudUserRef.current;
        const recipient = event.recipient as string | undefined;
        const notifId = event.id as number | undefined;
        if (me && recipient && recipient.split(",").includes(me)) {
          const lastReadId = parseInt(localStorage.getItem("notification_last_read_id") || "0");
          const dismissedIds: number[] = JSON.parse(localStorage.getItem("notification_dismissed_ids") || "[]");
          if (!notifId || (notifId > lastReadId && !dismissedIds.includes(notifId))) {
            markNotificationUnread();
          }
        }
      }
      // OAuth authorization prompts from the MCP proxy.
      if (event.type === "connector:needs_auth") {
        const name = event.name as string;
        const projectId = event.project_id as string | undefined;
        toast.warning(`MCP "${name}" requires authorization`, {
          id: `connector-needs-auth-${projectId || "global"}-${name}`,
          duration: Infinity,
          closeButton: true,
          description: "Authorize to enable this tool, then talk to AI again.",
          action: {
            label: "Authorize",
            onClick: async () => {
              try {
                const { auth_url } = await marketplaceApi.oauthStart(name, projectId || undefined);
                window.open(auth_url, "_blank", "noopener,noreferrer");
              } catch (e) {
                toast.error(`Cannot start OAuth for "${name}"`, {
                  description: e instanceof Error ? e.message : String(e),
                });
              }
            },
          },
        });
      }
      if (event.type === "connector:connected") {
        const name = event.name as string;
        const projectId = event.project_id as string | undefined;
        toast.dismiss(`connector-needs-auth-${projectId || "global"}-${name}`);
        toast.success(`MCP "${name}" connected`);
      }
    });
  }, [loadSessions]);

  useEffect(() => {
    const handler = () => clearNotificationUnread();
    window.addEventListener("notification-read", handler);
    return () => window.removeEventListener("notification-read", handler);
  }, []);

  // Clear notification dot when user visits the Cloud page
  useEffect(() => {
    if (location.pathname === "/connect") {
      clearNotificationUnread();
    }
  }, [location.pathname]);

  // Re-sync streaming state when WebSocket reconnects (done events may have been lost)
  useEffect(() => {
    return globalWS.onConnection((connected) => {
      if (connected) {
        loadSessions();
      }
    });
  }, [loadSessions]);

  // ── Server mode detection ──
  useEffect(() => {
    serverApi.info()
      .then((info) => {
        const ci = info as CloudInfo;
        setCloudInfo(ci);
        cloudUserRef.current = ci.user_name ?? null;
        setConnectStatus(ci.mode === "cloud" ? "connected" : "idle");
      })
      .catch(() => setConnectStatus("idle"));
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return { loadSessions, loadProjects, projectsLoaded, cloudInfo, notificationUnread };
}
