import { useEffect, useState, useCallback, useRef } from "react";
import { useNavigate, useLocation } from "react-router-dom";
import { PanelLeftClose, GripVertical, X, FolderOpen, Plus } from "lucide-react";
import { toast } from "sonner";
import { sessionsApi, projectsApi } from "@/lib/api";
import { cn } from "@/lib/utils";
import { useAppStore } from "@/stores/app";
import { useLayoutStore } from "@/stores/layout";
import { ThemeToggle } from "@/components/ui/theme-toggle";
import { ProjectDialog } from "@/components/project/ProjectDialog";
import { DeleteProjectDialog } from "@/components/project/DeleteProjectDialog";
import { UNASSIGNED_PROJECT, SIDEBAR_WIDTH_MIN, SIDEBAR_WIDTH_MAX } from "@/lib/constants";
import { useSessionNotifications } from "@/hooks/useSessionNotifications";
import { useIsMobile } from "@/hooks/useIsMobile";
import { useSidebarData } from "@/hooks/useSidebarData";
import { useGroupedSessions } from "@/hooks/useGroupedSessions";
import { useSessionReorder } from "@/hooks/useSessionReorder";
import { useMobileDrawerSwipe } from "@/hooks/useMobileDrawerSwipe";
import { SessionGroup } from "@/components/layout/sidebar/SessionGroup";
import { NewSessionButton } from "@/components/layout/sidebar/NewSessionButton";
import { SessionsHeader } from "@/components/layout/sidebar/SessionsHeader";
import { SidebarFooterNav } from "@/components/layout/sidebar/SidebarFooterNav";
import { MobileMenuButton, CollapsedSidebarRail } from "@/components/layout/sidebar/CollapsedSidebar";
import type { Project } from "@/types";

// Tracks whether the first auto-select pass has run in this JS runtime.
// Module-level so it survives React strict-mode double-mounts but resets
// on every cold app launch (new browser tab, TWA/PWA cold start). This is
// what makes "land on Default project scratch on every cold open" work
// even when localStorage still has a valid persisted activeSessionId.
let initialAutoSelectDone = false;

export function AppSidebar() {
  const navigate = useNavigate();
  const location = useLocation();
  const {
    sessions, setSessions, activeSessionId, setActiveSessionId,
    markSessionRead, projects, setProjects,
  } = useAppStore();
  const {
    sidebarCollapsed: collapsed, setSidebarCollapsed: setCollapsed,
    sidebarWidth, setSidebarWidth,
  } = useLayoutStore();
  const isMobile = useIsMobile();

  // On mobile, the sidebar is a drawer overlay. When closed it shows nothing
  // inline (just a floating hamburger), regardless of the persisted desktop
  // `collapsed` state. We start closed on first mount so users see the chat,
  // not the drawer, on a fresh phone load.
  const [mobileOpen, setMobileOpen] = useState(false);
  useEffect(() => {
    // Whenever we cross the breakpoint, reset the drawer to closed so the
    // mobile experience is predictable.
    if (isMobile) setMobileOpen(false);
  }, [isMobile]);
  // Close the mobile drawer whenever the route changes — typical drawer UX.
  useEffect(() => {
    if (isMobile) setMobileOpen(false);
  }, [location.pathname, isMobile]);

  // ── Data + derived state (see hooks for the heavy lifting) ──
  const panelRef = useRef<HTMLDivElement>(null);
  const { dragX, swiping, panelW } = useMobileDrawerSwipe(panelRef, isMobile, mobileOpen, setMobileOpen);
  const { loadSessions, projectsLoaded } = useSidebarData();
  const groupedSessions = useGroupedSessions(sessions, projects);
  const {
    dragSessionId, dragOverSessionId, dragOverPosition,
    handleDragStart, handleDragOver, handleDragEnd, handleDrop,
  } = useSessionReorder(loadSessions);
  useSessionNotifications();

  // Resize
  const isResizing = useRef(false);

  // Session rename
  const [renamingSessionId, setRenamingSessionId] = useState<string | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const renameRef = useRef<HTMLInputElement>(null);

  // Collapsed project groups
  const [collapsedProjects, setCollapsedProjects] = useState<Set<string>>(new Set());

  // Delete session confirmation
  const [confirmDeleteSessionId, setConfirmDeleteSessionId] = useState<string | null>(null);

  // Project dialog + delete
  const [projectDialogOpen, setProjectDialogOpen] = useState(false);
  const [editingProject, setEditingProject] = useState<Project | null>(null);
  const [deleteTargetProject, setDeleteTargetProject] = useState<Project | null>(null);
  const [deletingProject, setDeletingProject] = useState(false);

  useEffect(() => {
    if (renamingSessionId && renameRef.current) {
      renameRef.current.focus();
      renameRef.current.select();
    }
  }, [renamingSessionId]);

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

  // Opens the given project's scratch session and makes it active, without
  // navigating — the shared core of "click a project row" that the delete and
  // auto-select fallbacks reuse so they land on the same surface a manual
  // project click would.
  const selectProjectDefaultSession = useCallback(async (projectId: string) => {
    const scratch = await projectsApi.scratchSession(projectId);
    const current = useAppStore.getState().sessions;
    if (!current.some((s) => s.id === scratch.id)) {
      setSessions([scratch, ...current]);
    }
    setActiveSessionId(scratch.id);
    return scratch;
  }, [setSessions, setActiveSessionId]);

  // Auto-select a session.
  //
  // On the FIRST run after projectsLoaded (cold app launch — new browser tab,
  // TWA/PWA reopen), always land on the Default project's scratch session,
  // even if localStorage still has a valid persisted activeSessionId. Without
  // this, persisted state would silently win on every TWA reopen.
  //
  // On SUBSEQUENT runs (session list refetch, project mutation, etc.) we only
  // step in when the active session is missing or stale, so mid-app refetches
  // don't yank the user off a real conversation.
  //
  // Fallback order in both modes:
  //   1. Default project scratch session
  //   2. First sidebar project's scratch session (same as clicking its row)
  //   3. First visible session in the sidebar
  //   4. Clear the selection
  useEffect(() => {
    if (!projectsLoaded) return;

    const visibleSessions = groupedSessions.flatMap((g) => g.sessions);

    const activeIsScratch = activeSessionId
      ? sessions.some((s) => s.id === activeSessionId && s.is_hidden)
      : false;
    const activeExists = activeSessionId
      ? visibleSessions.some((s) => s.id === activeSessionId) || activeIsScratch
      : false;

    const isFirstPass = !initialAutoSelectDone;
    if (!isFirstPass && activeExists) return;
    initialAutoSelectDone = true;

    const defaultProject = projects.find(
      (p) => p.name === "Default" && !!p.git_repo_path,
    );
    if (defaultProject) {
      (async () => {
        try {
          await selectProjectDefaultSession(defaultProject.id);
        } catch (e) {
          console.error("Failed to open Default project scratch session:", e);
          if (!activeExists && visibleSessions.length > 0) {
            setActiveSessionId(visibleSessions[0].id);
          }
        }
      })();
      return;
    }

    if (activeExists) return;
    const selectFirstVisible = () => {
      if (visibleSessions.length > 0) {
        setActiveSessionId(visibleSessions[0].id);
      } else if (activeSessionId && !activeIsScratch) {
        setActiveSessionId(null);
      }
    };
    const firstProjectId = groupedSessions[0]?.projectId;
    if (firstProjectId) {
      selectProjectDefaultSession(firstProjectId).catch((e) => {
        console.error("Failed to select default project session:", e);
        selectFirstVisible();
      });
    } else {
      selectFirstVisible();
    }
  }, [groupedSessions, sessions, activeSessionId, setActiveSessionId, projects, projectsLoaded, selectProjectDefaultSession]);

  // ── Session actions ──

  const handleSelectSession = (sessionId: string) => {
    setActiveSessionId(sessionId);
    markSessionRead(sessionId);
    if (location.pathname !== "/") navigate("/");
  };

  // Opening a session on the chat page counts as reading it — clears its blue
  // dot. Covers clicks, route changes back to "/", and the rehydrated active
  // session on first load.
  useEffect(() => {
    if (activeSessionId && location.pathname === "/") markSessionRead(activeSessionId);
  }, [activeSessionId, location.pathname, markSessionRead]);

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

  // Clicking a project row body activates that project's hidden ("scratch")
  // session: a per-project staging surface for a new chat. The session is
  // created lazily on first request (server-side) and gets promoted to a
  // visible session automatically when the user sends the first message,
  // so right-side panels (Files / Terminal / Workflow) and chat input all
  // work just like a regular session in the meantime.
  const handleSelectProject = useCallback(async (projectId: string) => {
    try {
      await selectProjectDefaultSession(projectId);
      if (location.pathname !== "/") navigate("/");
    } catch (e) {
      console.error("Failed to open project scratch session:", e);
      toast.error("Failed to open project", { description: String(e) });
    }
  }, [selectProjectDefaultSession, location.pathname, navigate]);

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
      // If this session is active, switch to its own project's default (scratch)
      // session *before* removing it, so the auto-select effect doesn't race in
      // with its own fallback. Only applies when the project is a sidebar row.
      if (activeSessionId === sessionId && session?.project_id &&
          groupedSessions.some((g) => g.projectId === session.project_id)) {
        try {
          await selectProjectDefaultSession(session.project_id);
        } catch (e) {
          console.error("Failed to select default project session:", e);
        }
      }
      await sessionsApi.delete(sessionId);
      const remaining = useAppStore.getState().sessions.filter((s) => s.id !== sessionId);
      setSessions(remaining);
      if (useAppStore.getState().activeSessionId === sessionId) {
        // Either not switched above, or the project fallback didn't apply/failed.
        setActiveSessionId(remaining.length > 0 ? remaining[0].id : null);
      }
    } catch (e) {
      console.error("Failed to move session to trash:", e);
      toast.error("Failed to move session to trash", { description: String(e) });
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
      if (next.has(projectId)) next.delete(projectId);
      else next.add(projectId);
      return next;
    });
  };

  // ── Project dialog handlers ──
  const handleOpenEditProject = (project: Project) => {
    setEditingProject(project);
    setProjectDialogOpen(true);
  };

  const handleOpenNewProject = () => {
    setEditingProject(null);
    setProjectDialogOpen(true);
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

  const handleConfirmDeleteProject = async () => {
    if (!deleteTargetProject) return;
    const target = deleteTargetProject;
    setDeletingProject(true);
    try {
      await projectsApi.delete(target.id);
      handleProjectDeleted(target.id);
      toast.success(`Project "${target.name}" deleted`);
      setDeleteTargetProject(null);
    } catch (e) {
      toast.error("Failed to delete project", { description: String(e) });
    } finally {
      setDeletingProject(false);
    }
  };

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
  }, [setSidebarWidth]);

  // ── Collapsed state ──
  // Desktop: a 40px icon rail. Mobile: nothing inline; just a floating
  // hamburger pinned to the viewport so the drawer can slide in on demand.

  if (isMobile && !mobileOpen) {
    return <MobileMenuButton onOpen={() => setMobileOpen(true)} />;
  }

  if (!isMobile && collapsed) {
    return <CollapsedSidebarRail onExpand={() => setCollapsed(false)} />;
  }

  // ── Render ──

  return (
    <div
      className={cn(
        isMobile
          ? "fixed inset-0 z-50 flex h-full"
          : "flex h-full shrink-0",
      )}
    >
      {/* Backdrop — mobile only; taps to close, fades with the swipe. */}
      {isMobile && (
        <div
          className={cn(
            "absolute inset-0 bg-black/40",
            !swiping && "transition-opacity duration-200 ease-out",
          )}
          style={{ opacity: panelW ? Math.max(0, Math.min(1, 1 + dragX / panelW)) : 1 }}
          onClick={() => setMobileOpen(false)}
          aria-hidden="true"
        />
      )}
      <div
        ref={panelRef}
        className={cn(
          "border-r border-border/50 bg-card flex flex-col h-full overflow-hidden",
          isMobile && "relative z-10 shadow-xl",
          // Animate the snap-back / slide-out, but follow the finger 1:1 mid-drag.
          isMobile && !swiping && "transition-transform duration-200 ease-out",
        )}
        style={{
          width: isMobile ? "min(85vw, 320px)" : sidebarWidth,
          // Drawer container is fixed-positioned and ignores body's safe-area
          // padding; push the panel's content down past the iOS status bar.
          ...(isMobile && {
            paddingTop: "env(safe-area-inset-top)",
            transform: `translateX(${dragX}px)`,
          }),
        }}
      >
        {/* Header */}
        <div className="px-3 py-3 border-b flex items-center justify-between shrink-0">
          <button
            onClick={() => navigate("/")}
            className="flex items-center gap-2 font-bold text-lg hover:opacity-80"
          >
            <img src="/favicon.svg" alt="NexBox" className="w-7 h-7" aria-hidden="true" />
            NexBox
          </button>
          <div className="flex items-center gap-0.5">
            <ThemeToggle />
            <button
              onClick={() => {
                if (isMobile) setMobileOpen(false);
                else setCollapsed(true);
              }}
              className="p-1.5 rounded hover:bg-accent transition-colors text-muted-foreground hover:text-foreground"
              title={isMobile ? "Close menu" : "Collapse sidebar"}
            >
              {isMobile ? <X className="h-4 w-4" /> : <PanelLeftClose className="h-4 w-4" />}
            </button>
          </div>
        </div>

        {/* ── Quick actions ── */}
        <NewSessionButton onNewSession={handleNewSession} />

        {/* ── Sessions section header ── */}
        <SessionsHeader onNewProject={handleOpenNewProject} />

        {/* ── Grouped session list ── */}
        <div className="flex-1 overflow-y-auto px-2 pb-2">
          {groupedSessions.map((group) => (
            <SessionGroup
              key={group.projectId || "__unbound__"}
              group={group}
              isCollapsed={collapsedProjects.has(group.projectId || "__unbound__")}
              onToggleCollapsed={toggleProjectCollapsed}
              onSelectProject={handleSelectProject}
              onNewSession={handleNewSession}
              onEditProject={handleOpenEditProject}
              onDeleteProject={setDeleteTargetProject}
              renamingSessionId={renamingSessionId}
              renameValue={renameValue}
              renameRef={renameRef}
              onRenameChange={setRenameValue}
              onRenameCommit={handleCommitRename}
              onRenameCancel={() => setRenamingSessionId(null)}
              onRenameStart={handleStartRename}
              dragSessionId={dragSessionId}
              dragOverSessionId={dragOverSessionId}
              dragOverPosition={dragOverPosition}
              onDragStart={handleDragStart}
              onDragOver={handleDragOver}
              onDragEnd={handleDragEnd}
              onDrop={handleDrop}
              confirmDeleteSessionId={confirmDeleteSessionId}
              onConfirmDelete={setConfirmDeleteSessionId}
              onCancelDelete={() => setConfirmDeleteSessionId(null)}
              onDelete={handleDeleteSession}
              onSelectSession={handleSelectSession}
            />
          ))}

          {projects.filter((p) => p.name !== UNASSIGNED_PROJECT).length === 0 && (
            <div className="flex flex-col items-center text-center px-3 py-8 gap-2">
              <FolderOpen className="h-6 w-6 text-muted-foreground/40" />
              <p className="text-[12px] text-muted-foreground">
                No projects yet. Create one to start chatting.
              </p>
              <button
                onClick={handleOpenNewProject}
                className="mt-1 inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md text-[12px] font-medium text-primary-foreground bg-primary hover:bg-primary/90 transition-colors"
              >
                <Plus className="h-3 w-3" />
                New Project
              </button>
            </div>
          )}
        </div>

        {/* ── Footer: bottom nav ── */}
        <SidebarFooterNav />
      </div>

      {/* Project Dialog (create + edit). Delete uses the dedicated typed-name
          confirmation dialog rendered below. */}
      <ProjectDialog
        open={projectDialogOpen}
        onOpenChange={setProjectDialogOpen}
        project={editingProject}
        onSuccess={handleProjectSaved}
        onDelete={handleProjectDeleted}
        hideDelete
      />

      {deleteTargetProject && (
        <DeleteProjectDialog
          project={deleteTargetProject}
          deleting={deletingProject}
          onConfirm={handleConfirmDeleteProject}
          onClose={() => { if (!deletingProject) setDeleteTargetProject(null); }}
        />
      )}

      {/* Resize handle — desktop only; dragging doesn't apply on touch. */}
      {!isMobile && (
        <div
          className="w-1 cursor-col-resize hover:bg-primary/20 active:bg-primary/40 transition-colors flex items-center justify-center shrink-0"
          onMouseDown={handleResizeMouseDown}
        >
          <GripVertical className="h-4 w-4 text-muted-foreground/30" />
        </div>
      )}
    </div>
  );
}
