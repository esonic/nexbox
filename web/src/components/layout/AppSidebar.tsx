import { useEffect, useState, useCallback, useRef, useMemo } from "react";
import { useTranslation, Trans } from "react-i18next";
import { useNavigate, useLocation } from "react-router-dom";
import {
  PanelLeftClose,
  PanelLeftOpen,
  GripVertical,
  X,
  SquarePen,
  ChevronRight,
  ChevronDown,
  Plus,
  FolderOpen,
  FolderPlus,
  Settings,
  Cloud,
  CloudOff,
  MoreHorizontal,
  LayoutGrid,
  LogOut,
  History,
  RotateCcw,
  Store,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { sessionsApi, projectsApi, serverApi, marketplaceApi, authCloudApi, balanceApi, clearToken } from "@/lib/api";
import type { CloudInfo } from "@/lib/api";
import { globalWS } from "@/lib/ws";
import { cn, formatRelativeDate } from "@/lib/utils";
import { useAppStore } from "@/stores/app";
import { useLayoutStore } from "@/stores/layout";
import { ThemeToggle } from "@/components/ui/theme-toggle";
import { ProjectDialog } from "@/components/project/ProjectDialog";
import { UNASSIGNED_PROJECT, SIDEBAR_WIDTH_MIN, SIDEBAR_WIDTH_MAX } from "@/lib/constants";
import type { Project, Session } from "@/types";

// ── SessionRow ──────────────────────────────────────────────────────────

interface SessionRowProps {
  session: Session;
  isActive: boolean;
  isStreaming: boolean;
  // Rename
  isRenaming: boolean;
  renameValue: string;
  renameRef: React.RefObject<HTMLInputElement | null>;
  onRenameChange: (v: string) => void;
  onRenameCommit: () => void;
  onRenameCancel: () => void;
  onRenameStart: (id: string, name: string) => void;
  // Delete
  isConfirmingDelete: boolean;
  onConfirmDelete: (id: string) => void;
  onCancelDelete: () => void;
  onDelete: (id: string) => void;
  // Selection
  onSelect: (id: string) => void;
}

function SessionRow({
  session: sess,
  isActive, isStreaming,
  isRenaming, renameValue, renameRef,
  onRenameChange, onRenameCommit, onRenameCancel, onRenameStart,
  isConfirmingDelete, onConfirmDelete, onCancelDelete, onDelete,
  onSelect,
}: SessionRowProps) {
  return (
    <div
      className={cn(
        "group flex items-center rounded-lg transition-colors mb-px relative",
        isActive ? "bg-accent" : "hover:bg-accent/50",
      )}
    >
      {isStreaming && (
        <span className="absolute left-2.5 top-1/2 -translate-y-1/2 h-1.5 w-1.5 rounded-full bg-green-500 animate-pulse" />
      )}
      {isRenaming ? (
        <input
          ref={renameRef}
          value={renameValue}
          onChange={(e) => onRenameChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.nativeEvent.isComposing) onRenameCommit();
            if (e.key === "Escape") onRenameCancel();
          }}
          onBlur={onRenameCommit}
          className="flex-1 bg-transparent outline-none text-[13px] font-medium pl-7 pr-3 py-1.5 min-w-0"
        />
      ) : (
        <button
          onClick={() => onSelect(sess.id)}
          onDoubleClick={() => onRenameStart(sess.id, sess.name || "")}
          className={cn(
            "flex-1 flex items-center gap-1.5 pl-7 pr-2 py-1.5 text-[13px] min-w-0 transition-colors",
            isActive
              ? "text-foreground font-bold"
              : "text-muted-foreground hover:text-foreground"
          )}
        >
          <span className="truncate">{sess.name || "New Session"}</span>
        </button>
      )}
      <div className="relative shrink-0 w-5 h-5 mr-1.5 flex items-center justify-center">
        <span className="text-[11px] text-muted-foreground/40 tabular-nums group-hover:opacity-0 transition-opacity">
          {formatRelativeDate(sess.updated_at || sess.created_at)}
        </span>
        <button
          onClick={(e) => { e.stopPropagation(); onConfirmDelete(sess.id); }}
          className="absolute inset-0 flex items-center justify-center opacity-0 group-hover:opacity-100 p-1 rounded hover:bg-accent text-muted-foreground/40 hover:text-destructive transition-all"
          title="Move to trash"
        >
          <X className="h-3 w-3" />
        </button>
        {isConfirmingDelete && (
          <div data-delete-confirm className="absolute right-0 top-full mt-1 z-50 bg-popover border rounded-md shadow-md p-1.5 flex gap-1 whitespace-nowrap">
            <button
              onClick={(e) => { e.stopPropagation(); onDelete(sess.id); onCancelDelete(); }}
              className="px-2 py-0.5 text-xs bg-destructive text-destructive-foreground rounded hover:bg-destructive/90"
            >
              Move to Trash
            </button>
            <button
              onClick={(e) => { e.stopPropagation(); onCancelDelete(); }}
              className="px-2 py-0.5 text-xs bg-muted text-muted-foreground rounded hover:bg-accent"
            >
              Cancel
            </button>
          </div>
        )}
      </div>
    </div>
  );
}

// ── AppSidebar ──────────────────────────────────────────────────────────

export function AppSidebar() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const {
    sessions, setSessions, activeSessionId, setActiveSessionId,
    streamingSessions, initStreamingSessions,
    connectStatus, setConnectStatus,
    projects, setProjects,
    marketplaceUpdateCount, setMarketplaceUpdateCount,
    currentUser,
    balanceTokens, balanceCny, setBalance,
  } = useAppStore();
  const {
    sidebarCollapsed: collapsed, setSidebarCollapsed: setCollapsed,
    sidebarWidth, setSidebarWidth,
  } = useLayoutStore();

  // Resize
  const isResizing = useRef(false);

  // Session rename
  const [renamingSessionId, setRenamingSessionId] = useState<string | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const renameRef = useRef<HTMLInputElement>(null);

  // Collapsed project groups
  const [collapsedProjects, setCollapsedProjects] = useState<Set<string>>(new Set());

  // New session context menu
  const [showNewSessionMenu, setShowNewSessionMenu] = useState(false);
  const newSessionMenuRef = useRef<HTMLDivElement>(null);

  // Delete session confirmation
  const [confirmDeleteSessionId, setConfirmDeleteSessionId] = useState<string | null>(null);

  // Trash
  const [showTrash, setShowTrash] = useState(false);
  const [trashedSessions, setTrashedSessions] = useState<Session[]>([]);
  const trashRef = useRef<HTMLDivElement>(null);

  // Cloud info
  const [cloudInfo, setCloudInfo] = useState<CloudInfo | null>(null);

  // Notification unread dot
  const [notificationUnread, setNotificationUnread] = useState(
    () => localStorage.getItem("notification_unread") === "1"
  );
  const cloudUserRef = useRef<string | null>(null);

  const markNotificationUnread = () => {
    localStorage.setItem("notification_unread", "1");
    setNotificationUnread(true);
  };
  const clearNotificationUnread = () => {
    localStorage.removeItem("notification_unread");
    setNotificationUnread(false);
  };

  // Project dialog
  const [projectDialogOpen, setProjectDialogOpen] = useState(false);
  const [editingProject, setEditingProject] = useState<Project | null>(null);

  // Delete project confirmation
  const [deleteProjectDialogOpen, setDeleteProjectDialogOpen] = useState(false);
  const [deletingProject, setDeletingProject] = useState<Project | null>(null);
  const [deleteConfirmInput, setDeleteConfirmInput] = useState("");
  const [deleteProjectLoading, setDeleteProjectLoading] = useState(false);

  // Project ... menu
  const [projectMenuId, setProjectMenuId] = useState<string | null>(null);
  const projectMenuRef = useRef<HTMLDivElement>(null);



  // ── Data Loading ──

  const loadSessions = useCallback(async () => {
    try {
      const data = await sessionsApi.list();
      const list = data.sessions || [];
      setSessions(list);
      if (data.active_session_ids) {
        initStreamingSessions(data.active_session_ids);
      }
    } catch (e) {
      console.error("Failed to load sessions:", e);
    }
  }, [setSessions, initStreamingSessions]);

  const loadProjects = useCallback(async () => {
    try {
      const data = await projectsApi.list();
      const list = data || [];
      if (list.length === 0) {
        // Auto-create a default project when none exist
        try {
          const defaultProj = await projectsApi.create({ name: "Default" });
          setProjects([defaultProj]);
          // Also create a default session so the user can start immediately
          try {
            const sess = await sessionsApi.create({ name: "New Chat", project_id: defaultProj.id });
            setSessions([sess]);
          } catch (e) {
            console.error("Failed to create default session:", e);
          }
          return;
        } catch (e) {
          console.error("Failed to create default project:", e);
        }
      }
      setProjects(list);
    } catch (e) {
      console.error("Failed to load projects:", e);
    }
  }, [setSessions]);

  const checkMarketplaceUpdates = useCallback(async () => {
    // Local-only marketplace: no cloud update checking needed
    setMarketplaceUpdateCount(0);
  }, [setMarketplaceUpdateCount]);

  const loadBalance = useCallback(async () => {
    try {
      const data = await balanceApi.get();
      setBalance(data.balance_tokens, data.balance_cny);
    } catch {
      // balance endpoint may not be available
    }
  }, [setBalance]);

  useEffect(() => {
    loadSessions();
    loadProjects();
    loadBalance();
  }, [loadSessions, loadProjects, loadBalance]);

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

  useEffect(() => {
    return globalWS.subscribeGlobal((event) => {
      if (event.type === "change" && event.entity_type === "session") {
        loadSessions();
      }
      // Refresh balance when agent execution finishes
      if (event.type === "settle") {
        loadBalance();
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

  useEffect(() => {
    if (renamingSessionId && renameRef.current) {
      renameRef.current.focus();
      renameRef.current.select();
    }
  }, [renamingSessionId]);

  // Close new-session menu on outside click
  useEffect(() => {
    if (!showNewSessionMenu) return;
    const handler = (e: MouseEvent) => {
      if (newSessionMenuRef.current && !newSessionMenuRef.current.contains(e.target as Node)) {
        setShowNewSessionMenu(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [showNewSessionMenu]);

  // Close delete confirmation on outside click
  useEffect(() => {
    if (!confirmDeleteSessionId) return;
    const handler = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (target.closest("[data-delete-confirm]")) return;
      setConfirmDeleteSessionId(null);
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [confirmDeleteSessionId]);

  // Close trash popover on outside click
  useEffect(() => {
    if (!showTrash) return;
    const handler = (e: MouseEvent) => {
      if (trashRef.current && !trashRef.current.contains(e.target as Node)) {
        setShowTrash(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [showTrash]);

  // Close project ... menu on outside click
  useEffect(() => {
    if (!projectMenuId) return;
    const handler = (e: MouseEvent) => {
      if (projectMenuRef.current && !projectMenuRef.current.contains(e.target as Node)) {
        setProjectMenuId(null);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [projectMenuId]);

  // ── Grouped sessions ──

  const projectMap = useMemo(() => {
    const map = new Map<string, Project>();
    for (const p of projects) map.set(p.id, p);
    return map;
  }, [projects]);

  const groupedSessions = useMemo(() => {
    // Group sessions by project_id
    const sessionsByProject = new Map<string, typeof sessions>();
    const unbound: typeof sessions = [];

    for (const sess of sessions) {
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

    const result: { projectId: string | null; project: Project | null; sessions: typeof sessions }[] = [];

    const sortSessions = (list: typeof sessions) =>
      [...list].sort((a, b) => {
        // 按 updated_at 倒序（最近修改的在前）
        const ta = new Date(a.updated_at || a.created_at).getTime();
        const tb = new Date(b.updated_at || b.created_at).getTime();
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

  // Auto-select first visible session, or clear if active session is not visible
  useEffect(() => {
    const visibleSessions = groupedSessions.flatMap((g) => g.sessions);

    // If no sessions are loaded yet, don't do anything
    if (visibleSessions.length === 0) return;

    // If there's an activeSessionId (from persistence or previous selection)
    if (activeSessionId) {
      // Check if it still exists in the current session list
      const sessionExists = visibleSessions.some((s) => s.id === activeSessionId);
      if (!sessionExists) {
        // Session no longer exists, fall back to first session
        console.log('[AppSidebar] Active session not found, selecting first session');
        setActiveSessionId(visibleSessions[0].id);
      }
      // else: keep the current activeSessionId (it exists)
      return;
    }

    // No activeSessionId set, select the first available session
    console.log('[AppSidebar] No active session, selecting first session');
    setActiveSessionId(visibleSessions[0].id);
  }, [groupedSessions, activeSessionId, setActiveSessionId]);

  // ── Session actions ──

  const handleSelectSession = (sessionId: string) => {
    setActiveSessionId(sessionId);
    if (location.pathname !== "/") navigate("/");
  };

  const handleNewSession = async (projectId?: string) => {
    try {
      const sess = await sessionsApi.create({
        name: `Session ${sessions.length + 1}`,
        project_id: projectId,
      });
      setSessions([sess, ...sessions]);
      setActiveSessionId(sess.id);
      if (location.pathname !== "/") navigate("/");
      window.dispatchEvent(new CustomEvent("session-updated"));
    } catch (e) {
      console.error("Failed to create session:", e);
      toast.error("Failed to create session", { description: String(e) });
    }
  };

  const handleDeleteSession = async (sessionId: string) => {
    // Check if session has a dirty worktree and warn the user
    const session = sessions.find((s) => s.id === sessionId);
    if (session?.worktree_mode === "worktree" && session.worktree_path) {
      try {
        const status = await sessionsApi.getWorktreeStatus(sessionId);
        if (status.dirty) {
          toast.warning("Worktree has uncommitted or unpushed changes", {
            description: "The worktree directory will be removed when this session is permanently deleted from trash.",
          });
        }
      } catch {
        // ignore status check failure, proceed with delete
      }
    }
    try {
      await sessionsApi.delete(sessionId);
      const remaining = sessions.filter((s) => s.id !== sessionId);
      setSessions(remaining);
      if (activeSessionId === sessionId) {
        setActiveSessionId(remaining.length > 0 ? remaining[0].id : null);
      }
    } catch (e) {
      console.error("Failed to move session to trash:", e);
      toast.error("Failed to move session to trash", { description: String(e) });
    }
  };

  const handleOpenTrash = async () => {
    try {
      const data = await sessionsApi.listTrashed();
      setTrashedSessions(data.sessions || []);
      setShowTrash(true);
    } catch (e) {
      console.error("Failed to load trash:", e);
      toast.error("Failed to load trash", { description: String(e) });
    }
  };

  const handleRestoreSession = async (sessionId: string) => {
    try {
      const restored = await sessionsApi.restore(sessionId);
      setTrashedSessions(trashedSessions.filter((s) => s.id !== sessionId));
      setSessions([restored, ...sessions]);
    } catch (e) {
      console.error("Failed to restore session:", e);
      toast.error("Failed to restore session", { description: String(e) });
    }
  };

  const handleStartRename = (sessionId: string, currentName: string) => {
    setRenamingSessionId(sessionId);
    setRenameValue(currentName);
  };

  const handleCommitRename = async () => {
    if (!renamingSessionId) return;
    const name = renameValue.trim();
    setRenamingSessionId(null);
    if (!name) return;
    try {
      const updated = await sessionsApi.update(renamingSessionId, { name });
      setSessions(sessions.map((s) => (s.id === updated.id ? { ...s, name: updated.name } : s)));
    } catch (e) {
      console.error("Failed to rename session:", e);
      toast.error("Failed to rename session", { description: String(e) });
    }
  };

  const toggleProjectCollapsed = (projectId: string) => {
    setCollapsedProjects((prev) => {
      const next = new Set(prev);
      next.has(projectId) ? next.delete(projectId) : next.add(projectId);
      return next;
    });
  };

  // Project dialog handlers
  const handleOpenEditProject = (project: Project) => {
    setEditingProject(project);
    setProjectDialogOpen(true);
  };

  const handleOpenDeleteProject = (project: Project) => {
    setDeletingProject(project);
    setDeleteConfirmInput("");
    setDeleteProjectDialogOpen(true);
  };

  const handleConfirmDeleteProject = async () => {
    if (!deletingProject) return;
    setDeleteProjectLoading(true);
    try {
      await projectsApi.delete(deletingProject.id);
      handleProjectDeleted(deletingProject.id);
      setDeleteProjectDialogOpen(false);
    } catch (e) {
      console.error("Failed to delete project:", e);
      toast.error("Failed to delete project", { description: String(e) });
    } finally {
      setDeleteProjectLoading(false);
    }
  };

  const handleProjectSaved = (project: Project) => {
    if (editingProject) {
      setProjects(projects.map((p) => (p.id === project.id ? project : p)));
    } else {
      setProjects([project, ...projects]);
    }
  };

  const handleProjectDeleted = (projectId: string) => {
    setProjects(projects.filter((p) => p.id !== projectId));
    setSessions(sessions.filter((s) => s.project_id !== projectId));
  };

  const handleRemoveFromSidebar = async (project: Project) => {
    setProjectMenuId(null);
    try {
      await projectsApi.removeFromSidebar(project.id);
      // In cloud mode: project stays in cloud but loses local dir → filter will hide it
      // In local mode: project git_repo_path cleared → filter hides it
      setProjects(projects.map((p) => p.id === project.id ? { ...p, git_repo_path: "" } : p));
    } catch (e) {
      toast.error("Failed to unlink local directory", { description: String(e) });
    }
  };

  // ── Logout ──
  async function handleLogout() {
    try { await authCloudApi.logout(); } catch {}
    clearToken();
    window.location.reload();
  }

  // ── Server mode detection ──
  useEffect(() => {
    serverApi.info()
      .then((info: any) => {
        setCloudInfo(info as CloudInfo);
        cloudUserRef.current = (info as CloudInfo).user_name ?? null;
        setConnectStatus(info.mode === "cloud" ? "connected" : "idle");
      })
      .catch(() => setConnectStatus("idle"));
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // ── Resize ──

  const handleResizeMouseDown = useCallback((e: React.MouseEvent) => {
    isResizing.current = true;
    e.preventDefault();
    const onMouseMove = (ev: MouseEvent) => {
      if (!isResizing.current) return;
      setSidebarWidth(Math.min(SIDEBAR_WIDTH_MAX, Math.max(SIDEBAR_WIDTH_MIN, ev.clientX)));
    };
    const onMouseUp = () => {
      isResizing.current = false;
      document.removeEventListener("mousemove", onMouseMove);
      document.removeEventListener("mouseup", onMouseUp);
    };
    document.addEventListener("mousemove", onMouseMove);
    document.addEventListener("mouseup", onMouseUp);
  }, []);

  // ── Collapsed state ──

  if (collapsed) {
    return (
      <div className="w-10 border-r border-border/50 bg-card flex flex-col h-full items-center shrink-0">
        <div className="py-3 w-full flex flex-col items-center gap-1">
          <button
            onClick={() => setCollapsed(false)}
            className="p-1.5 rounded hover:bg-accent transition-colors"
            title={t("sidebar.expand_sidebar")}
          >
            <PanelLeftOpen className="h-4 w-4" />
          </button>
          <ThemeToggle />
        </div>
        <div className="mt-auto pb-3 w-full flex flex-col items-center gap-1">
          <button
            onClick={() => navigate("/marketplace")}
            className={cn(
              "relative p-1.5 rounded transition-colors",
              location.pathname.startsWith("/marketplace")
                ? "bg-accent text-foreground"
                : "text-muted-foreground hover:text-foreground hover:bg-accent"
            )}
            title={t("sidebar.marketplace")}
          >
            <Store className="h-4 w-4" />
            {marketplaceUpdateCount > 0 && (
              <span className="absolute top-0 right-0 h-2 w-2 rounded-full bg-red-500" />
            )}
          </button>
          <button
            onClick={() => navigate("/connect")}
            className={cn(
              "p-1.5 rounded transition-colors",
              location.pathname === "/connect"
                ? "bg-accent text-foreground"
                : "text-muted-foreground hover:text-foreground hover:bg-accent"
            )}
            title={connectStatus === "connected" ? t("sidebar.cloud") : t("sidebar.local")}
          >
            {connectStatus === "connected" ? (
              <Cloud className="h-4 w-4 text-green-500" />
            ) : (
              <CloudOff className="h-4 w-4" />
            )}
          </button>
          <button
            onClick={() => navigate("/settings")}
            className={cn(
              "p-1.5 rounded transition-colors",
              location.pathname === "/settings"
                ? "bg-accent text-foreground"
                : "text-muted-foreground hover:text-foreground hover:bg-accent"
            )}
            title={t("sidebar.settings")}
          >
            <Settings className="h-4 w-4" />
          </button>
        </div>
      </div>
    );
  }

  // ── Render ──

  return (
    <div className="flex h-full shrink-0">
      <div
        className="border-r border-border/50 bg-card flex flex-col h-full overflow-hidden"
        style={{ width: sidebarWidth }}
      >
        {/* Header */}
        <div className="px-3 py-3 border-b flex items-center justify-between shrink-0">
          <button
            onClick={() => navigate("/")}
            className="flex items-center gap-2 font-bold text-lg hover:opacity-80"
          >
            <img src="/favicon.svg" alt="BoxNex" className="w-7 h-7" aria-hidden="true" />
            BoxNex
          </button>
          <div className="flex items-center gap-0.5">
            <ThemeToggle />
            <button
              onClick={() => setCollapsed(true)}
              className="p-1.5 rounded hover:bg-accent transition-colors text-muted-foreground hover:text-foreground"
              title={t("sidebar.collapse_sidebar")}
            >
              <PanelLeftClose className="h-4 w-4" />
            </button>
          </div>
        </div>

        {/* ── Quick actions ── */}
        <div className="px-2 pt-3 pb-2 relative" ref={newSessionMenuRef}>
          <button
            onClick={() => {
              const realProjects = projects.filter((p) => p.name !== UNASSIGNED_PROJECT);
              if (realProjects.length === 0) {
                handleNewSession();
              } else if (realProjects.length === 1) {
                handleNewSession(realProjects[0].id);
              } else {
                setShowNewSessionMenu((v) => !v);
              }
            }}
            className="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-foreground/80 hover:text-foreground hover:bg-accent/50 transition-colors"
          >
            <SquarePen className="h-4 w-4 shrink-0 text-muted-foreground" />
            <span>{t("sidebar.new_session")}</span>
          </button>

          {/* Project picker dropdown */}
          {showNewSessionMenu && (
            <div className="absolute left-2 right-2 top-full z-50 mt-0.5 bg-popover border border-border rounded-lg shadow-lg overflow-hidden">
              <div className="px-3 py-2 text-xs font-medium text-muted-foreground border-b border-border bg-muted/30">
                {t("sidebar.select_project")}
              </div>
              {projects.filter((p) => p.name !== UNASSIGNED_PROJECT).map((p) => (
                <button
                  key={p.id}
                  onClick={() => { setShowNewSessionMenu(false); handleNewSession(p.id); }}
                  className="w-full flex items-center gap-2 px-3 py-2 text-xs hover:bg-accent transition-colors"
                >
                  <FolderOpen className="h-3.5 w-3.5 text-primary/60 shrink-0" />
                  <span className="truncate">{p.name}</span>
                </button>
              ))}
              {connectStatus !== "connected" && (
                <>
                  {projects.filter((p) => p.name !== UNASSIGNED_PROJECT).length > 0 && <div className="border-t border-border/50" />}
                  <button
                    onClick={() => { setShowNewSessionMenu(false); handleNewSession(); }}
                    className="w-full flex items-center gap-2 px-3 py-2 text-xs text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
                  >
                    <span className="text-muted-foreground/60">—</span>
                    <span>{t("sidebar.no_project")}</span>
                  </button>
                </>
              )}
            </div>
          )}
        </div>

        {/* ── Sessions section header ── */}
        <div className="flex items-center justify-between pl-4 pr-3 pt-2 pb-1.5 relative" ref={trashRef}>
          <span className="text-[13px] font-medium text-muted-foreground/60">{t("sidebar.sessions")}</span>
          <div className="flex items-center gap-0.5">
            <button
              onClick={() => setProjectDialogOpen(true)}
              className="p-1 rounded hover:bg-accent text-muted-foreground/40 hover:text-muted-foreground transition-colors"
              title={t("sidebar.new_project")}
            >
              <FolderPlus className="h-3.5 w-3.5" />
            </button>
            <button
              onClick={handleOpenTrash}
              className="p-1 rounded hover:bg-accent text-muted-foreground/40 hover:text-muted-foreground transition-colors"
              title={t("sidebar.recently_deleted")}
            >
              <History className="h-3.5 w-3.5" />
            </button>
          </div>
          {/* Trash popover */}
          {showTrash && (
            <div className="absolute left-0 right-0 top-full z-50 bg-popover border border-border rounded-lg shadow-lg overflow-hidden">
              <div className="px-3 py-2 border-b border-border bg-muted/30 flex items-center justify-between gap-2">
                <div className="flex items-center gap-1.5">
                  <History className="h-3.5 w-3.5 text-muted-foreground/60 shrink-0" />
                  <span className="text-xs font-medium text-muted-foreground">{t("sidebar.recently_deleted")}</span>
                </div>
                <button
                  onClick={() => setShowTrash(false)}
                  className="p-0.5 rounded hover:bg-accent text-muted-foreground/40 hover:text-muted-foreground transition-colors"
                >
                  <X className="h-3 w-3" />
                </button>
              </div>
              <div className="px-3 py-1.5 border-b border-border/50">
                <p className="text-[11px] text-muted-foreground/60">
                  {t("sidebar.trash_hint")}
                </p>
              </div>
              <div className="max-h-64 overflow-y-auto">
                {trashedSessions.length === 0 ? (
                  <div className="px-3 py-4 text-[12px] text-muted-foreground/40 text-center">
                    {t("sidebar.no_deleted_sessions")}
                  </div>
                ) : (
                  trashedSessions.map((s) => (
                    <div
                      key={s.id}
                      className="flex items-center gap-2 px-3 py-1.5 hover:bg-accent/50 transition-colors"
                    >
                      <span className="shrink-0 text-[11px] text-muted-foreground/50 tabular-nums">
                        {s.deleted_at ? formatRelativeDate(s.deleted_at) : ""}
                      </span>
                      <span className="flex-1 text-[12px] text-foreground truncate min-w-0">
                        {s.name || "New Session"}
                      </span>
                      <button
                        onClick={() => handleRestoreSession(s.id)}
                        className="shrink-0 p-1 rounded hover:bg-accent text-muted-foreground/50 hover:text-foreground transition-colors"
                        title={t("sidebar.restore")}
                      >
                        <RotateCcw className="h-3 w-3" />
                      </button>
                    </div>
                  ))
                )}
              </div>
            </div>
          )}
        </div>

        {/* ── Grouped session list ── */}
        <div className="flex-1 overflow-y-auto px-2 pb-2">
          {groupedSessions.map((group) => {
            const groupKey = group.projectId || "__unbound__";
            const isCollapsed = collapsedProjects.has(groupKey);

            return (
              <div key={groupKey} className="mb-1">
                {/* Project group header */}
                <div className="group flex items-center rounded-lg hover:bg-accent/30 transition-colors">
                  <button
                    onClick={() => toggleProjectCollapsed(groupKey)}
                    className="flex-1 flex items-center gap-1.5 px-2 py-1.5 min-w-0"
                  >
                    {isCollapsed
                      ? <ChevronRight className="h-3 w-3 text-muted-foreground/50 shrink-0" />
                      : <ChevronDown className="h-3 w-3 text-muted-foreground/50 shrink-0" />}
                    <FolderOpen className="h-3.5 w-3.5 text-muted-foreground/50 shrink-0" />
                    <span className="text-[13px] text-muted-foreground truncate">
                      {group.project?.name === UNASSIGNED_PROJECT
                        ? t("sidebar.unassigned")
                        : (group.project?.name || t("sidebar.unassigned"))}
                    </span>
                    <span className="text-[11px] text-muted-foreground/30 tabular-nums ml-1.5 shrink-0">
                      {group.sessions.length}
                    </span>
                  </button>
                  {group.projectId && group.project ? (
                    <div className="flex items-center shrink-0 relative">
                      {/* ... menu button */}
                      <button
                        onClick={(e) => { e.stopPropagation(); setProjectMenuId(projectMenuId === group.projectId ? null : group.projectId); }}
                        className="p-1 rounded hover:bg-accent text-muted-foreground/40 hover:text-muted-foreground transition-all opacity-0 group-hover:opacity-100"
                        title={t("sidebar.project_options")}
                      >
                        <MoreHorizontal className="h-3 w-3" />
                      </button>
                      {/* New session button */}
                      <button
                        onClick={(e) => { e.stopPropagation(); handleNewSession(group.projectId!); }}
                        className="p-1 mr-1 rounded hover:bg-accent text-muted-foreground/40 hover:text-muted-foreground transition-all"
                        title={t("sidebar.new_session")}
                      >
                        <Plus className="h-3 w-3" />
                      </button>
                      {/* Dropdown menu */}
                      {projectMenuId === group.projectId && (
                        <div
                          ref={projectMenuRef}
                          className="absolute right-0 top-full mt-0.5 z-50 w-44 bg-popover border rounded-lg shadow-lg overflow-hidden"
                        >
                          <button
                            onClick={(e) => { e.stopPropagation(); setProjectMenuId(null); handleOpenEditProject(group.project!); }}
                            className="w-full flex items-center gap-2 px-3 py-1.5 text-xs hover:bg-accent transition-colors"
                          >
                            <Settings className="h-3 w-3 text-muted-foreground" />
                            {t("sidebar.settings")}
                          </button>
                          <button
                            onClick={(e) => { e.stopPropagation(); setProjectMenuId(null); handleOpenDeleteProject(group.project!); }}
                            className="w-full flex items-center gap-2 px-3 py-1.5 text-xs hover:bg-accent transition-colors text-destructive"
                          >
                            <Trash2 className="h-3 w-3" />
                            {t("sidebar.delete_project")}
                          </button>
                        </div>
                      )}
                    </div>
                  ) : (
                    <div className="w-5 mr-1.5 shrink-0" />
                  )}
                </div>

                {/* Sessions in group */}
                {!isCollapsed && group.sessions.map((s) => (
                  <SessionRow
                    key={s.id}
                    session={s}
                    isActive={activeSessionId === s.id && location.pathname === "/"}
                    isStreaming={!!streamingSessions[s.id]}
                    isRenaming={renamingSessionId === s.id}
                    renameValue={renameValue}
                    renameRef={renameRef}
                    onRenameChange={setRenameValue}
                    onRenameCommit={handleCommitRename}
                    onRenameCancel={() => setRenamingSessionId(null)}
                    onRenameStart={handleStartRename}
                    isConfirmingDelete={confirmDeleteSessionId === s.id}
                    onConfirmDelete={setConfirmDeleteSessionId}
                    onCancelDelete={() => setConfirmDeleteSessionId(null)}
                    onDelete={handleDeleteSession}
                    onSelect={handleSelectSession}
                  />
                ))}
              </div>
            );
          })}

          {groupedSessions.length === 0 && sessions.length === 0 && (
            <div className="text-[12px] text-muted-foreground/40 text-center py-8">
              {t("sidebar.no_projects_hint")}<br />
              <button
                onClick={() => { setEditingProject(null); setProjectDialogOpen(true); }}
                className="text-primary/60 hover:text-primary underline-offset-2 hover:underline mt-1"
              >
                {t("sidebar.new_project")}
              </button>
            </div>
          )}
        </div>

        {/* Cloud / Local connect button — hidden (no longer needed) */}

        {/* ── Bottom nav ── */}
        <div className="px-2 pb-3 pt-1 shrink-0 space-y-0.5 border-t border-border/40 mt-1">
          {balanceTokens > 0 && (
            <div className="px-3 py-1.5 text-xs text-muted-foreground/70">
              <span className="tabular-nums">{balanceTokens.toLocaleString()}</span>
              <span className="ml-1">{t("sidebar.tokens")}</span>
              {balanceCny > 0 && (
                <span className="ml-1.5 text-muted-foreground/50">
                  ≈ ¥{balanceCny.toFixed(2)}
                </span>
              )}
            </div>
          )}
          <button
            onClick={() => navigate("/marketplace")}
            className={cn(
              "w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm transition-colors",
              location.pathname.startsWith("/marketplace")
                ? "bg-accent text-foreground font-medium"
                : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
            )}
          >
            <div className="relative shrink-0">
              <Store className="h-4 w-4" />
              {marketplaceUpdateCount > 0 && (
                <span className="absolute -top-0.5 -right-0.5 h-2 w-2 rounded-full bg-red-500" />
              )}
            </div>
            <span>{t("sidebar.marketplace")}</span>
            {marketplaceUpdateCount > 0 && (
              <span className="ml-auto text-[10px] font-medium px-1.5 py-0.5 rounded-full bg-red-500/10 text-red-500">
                {marketplaceUpdateCount}
              </span>
            )}
          </button>
          <button
            onClick={() => navigate("/settings")}
            className={cn(
              "w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm transition-colors",
              location.pathname === "/settings"
                ? "bg-accent text-foreground font-medium"
                : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
            )}
          >
            <Settings className="h-4 w-4 shrink-0" />
            <span>{t("sidebar.settings")}</span>
          </button>
          {currentUser && (
            <button
              onClick={handleLogout}
              className="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm transition-colors text-muted-foreground hover:text-foreground hover:bg-accent/50"
            >
              <LogOut className="h-4 w-4 shrink-0" />
              <span>{t("sidebar.logout")}</span>
            </button>
          )}
        </div>
      </div>

      {/* Project Dialog (edit only) */}
      <ProjectDialog
        open={projectDialogOpen}
        onOpenChange={setProjectDialogOpen}
        project={editingProject}
        onSuccess={handleProjectSaved}
        onDelete={handleProjectDeleted}
        hideDelete
      />

      {/* Delete Project Confirmation Dialog */}
      {deleteProjectDialogOpen && deletingProject && (
        <div className="fixed inset-0 z-50 flex items-center justify-center">
          <div className="fixed inset-0 bg-black/50" onClick={() => setDeleteProjectDialogOpen(false)} />
          <div className="relative bg-popover border rounded-lg shadow-lg p-5 w-[340px] space-y-4">
            <h3 className="text-sm font-semibold">{t("sidebar.delete_project_title")}</h3>
            <p className="text-xs text-muted-foreground">
              <Trans
                i18nKey="sidebar.delete_project_desc"
                values={{ name: deletingProject.name }}
                components={{ name: <span className="font-medium text-foreground" /> }}
              />
            </p>
            <div className="space-y-1.5">
              <label className="text-xs text-muted-foreground">
                <Trans
                  i18nKey="sidebar.delete_project_confirm_label"
                  values={{ name: deletingProject.name }}
                  components={{ name: <span className="font-medium text-foreground" /> }}
                />
              </label>
              <input
                value={deleteConfirmInput}
                onChange={(e) => setDeleteConfirmInput(e.target.value)}
                placeholder={deletingProject.name}
                className="w-full h-8 px-2 text-sm border rounded-md bg-background outline-none focus:ring-1 focus:ring-ring"
                autoFocus
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.nativeEvent.isComposing && deleteConfirmInput.trim() === deletingProject.name) {
                    handleConfirmDeleteProject();
                  }
                }}
              />
            </div>
            <div className="flex items-center justify-end gap-2">
              <button
                onClick={() => setDeleteProjectDialogOpen(false)}
                className="px-3 py-1.5 text-xs rounded-md hover:bg-accent transition-colors"
              >
                {t("sidebar.cancel")}
              </button>
              <button
                onClick={handleConfirmDeleteProject}
                disabled={deleteProjectLoading || deleteConfirmInput.trim() !== deletingProject.name}
                className="px-3 py-1.5 text-xs rounded-md bg-destructive text-destructive-foreground hover:bg-destructive/90 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                {deleteProjectLoading ? t("sidebar.deleting") : t("sidebar.delete_project")}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Resize handle */}
      <div
        className="w-1 cursor-col-resize hover:bg-primary/20 active:bg-primary/40 transition-colors flex items-center justify-center shrink-0"
        onMouseDown={handleResizeMouseDown}
      >
        <GripVertical className="h-4 w-4 text-muted-foreground/30" />
      </div>
    </div>
  );
}
