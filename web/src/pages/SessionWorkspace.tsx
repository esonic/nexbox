import { useState, useEffect, useRef, useCallback, lazy, Suspense } from "react";
import { createPortal } from "react-dom";

import {
  X,
  GitBranch,
  GitMerge,
  ExternalLink,
  Check,
  ChevronDown,
  PanelRightClose,
  PanelBottom,
  PanelRight,
  GripVertical,
  Loader2,
  FolderGit2,
  Clock,
  FolderCode,
  TerminalSquare,
  Trash2,
  ListTodo,
  FileText,
  Workflow,
  ArrowLeft,
  Repeat,
  Pencil,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { sessionsApi, projectsApi, diffApi, gitlabApi, type DiffResponse, type CreateMRResponse } from "@/lib/api";
import { TaskPlugin } from "@/plugins/task/TaskPlugin";
import { AssetPanel } from "@/plugins/asset/AssetPanel";
import { WorkflowPanel } from "@/components/workflow/WorkflowPanel";
import { RalphPanel } from "@/components/ralph/RalphPanel";
import { ChangeOrderMenuItem } from "@/plugins/okone/ChangeOrderMenuItem";
import { ChangeOrderResult } from "@/plugins/okone/ChangeOrderResult";
import { ChatPanel } from "@/components/chat/ChatPanel";
import { DiffView } from "@/components/diff/DiffView";
const FileBrowser = lazy(() => import("@/components/files/FileBrowser").then(m => ({ default: m.FileBrowser })));
import { TerminalPanel } from "@/plugins/terminal/TerminalPanel";
import { ProjectDialog } from "@/components/project/ProjectDialog";
import type { Project } from "@/types";
import { useAppStore } from "@/stores/app";
import { useLayoutStore } from "@/stores/layout";
import { globalWS } from "@/lib/ws";
import { cn } from "@/lib/utils";
import { formatTotalDuration } from "@/lib/format";
import {
  UNASSIGNED_PROJECT,
  PANEL_WIDTH_MIN, PANEL_WIDTH_MAX,
  TERMINAL_HEIGHT_MIN, TERMINAL_HEIGHT_MAX,
} from "@/lib/constants";
import { toast } from "sonner";

// ── WorktreeSelector ────────────────────────────────────────────────────

function WorktreeSelector({
  sessionId,
  mode,
  locked,
  worktreePending,
  hasGitRepo,
  onSetMode,
}: {
  sessionId: string;
  mode: "local" | "worktree";
  locked: boolean;
  worktreePending: boolean;
  hasGitRepo: boolean;
  onSetMode: (mode: "local" | "worktree") => void;
}) {
  const shortId = sessionId.slice(0, 8);
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [open]);

  const label = mode === "worktree" ? `Worktree · ${shortId}` : `Local · ${shortId}`;

  if (locked) {
    return (
      <span className="text-[10px] font-medium px-1.5 py-0.5 rounded-sm bg-muted text-muted-foreground shrink-0 flex items-center gap-1">
        {worktreePending && <Loader2 className="h-2.5 w-2.5 animate-spin" />}
        {label}
      </span>
    );
  }

  return (
    <div className="relative shrink-0" ref={ref}>
      <button
        onClick={() => hasGitRepo && setOpen((o) => !o)}
        disabled={!hasGitRepo}
        className={cn(
          "text-[10px] font-medium px-1.5 py-0.5 rounded-sm flex items-center gap-1 transition-colors",
          hasGitRepo
            ? "bg-muted text-muted-foreground hover:bg-accent hover:text-accent-foreground cursor-pointer"
            : "bg-muted text-muted-foreground opacity-40 cursor-not-allowed"
        )}
        title="Click to change mode"
      >
        {label}
        <ChevronDown className="h-2.5 w-2.5" />
      </button>
      {open && (
        <div className="absolute left-0 bottom-full z-50 mb-1 w-44 bg-popover border border-border rounded-lg shadow-lg overflow-hidden">
          {(["local", "worktree"] as const).map((m) => (
            <button
              key={m}
              onClick={() => { onSetMode(m); setOpen(false); }}
              className={cn(
                "w-full flex items-center justify-between px-3 py-1.5 text-xs hover:bg-accent transition-colors whitespace-nowrap",
                mode === m && "bg-accent/50 font-medium"
              )}
            >
              <span>{m === "local" ? "Local" : "New worktree"}</span>
              {mode === m && <Check className="h-3 w-3 text-foreground/50 shrink-0 ml-2" />}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// ── ProjectSelector ─────────────────────────────────────────────────────

function ProjectSelector({
  projects,
  onBind,
  onNewProject,
}: {
  projects: Project[];
  onBind: (projectId: string) => void;
  onNewProject: () => void;
}) {
  const visible = projects.filter((p) => p.name !== UNASSIGNED_PROJECT);
  return (
    <div className="border-b bg-muted/30 shrink-0">
      <div className="flex items-center gap-2 px-4 py-2">
        <FolderGit2 className="h-4 w-4 text-muted-foreground/50 shrink-0" />
        <span className="text-xs text-muted-foreground">Select a working directory</span>
        <button
          onClick={onNewProject}
          className="text-[11px] text-primary/70 hover:text-primary transition-colors"
        >
          + New project
        </button>
      </div>
      {visible.length > 0 && (
        <div className="px-4 pb-2 flex flex-wrap gap-1.5">
          {visible.map((p) => (
            <button
              key={p.id}
              onClick={() => onBind(p.id)}
              className="flex items-center gap-1.5 px-2.5 py-1 rounded-md border bg-background text-xs hover:bg-accent/50 transition-colors"
            >
              <FolderGit2 className="h-3 w-3 text-muted-foreground/60" />
              <span className="truncate max-w-[120px]">{p.name}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// ── GitPanelContent ─────────────────────────────────────────────────────

function GitPanelContent({
  projectId,
  sessionId,
  gitRepoPath,
  diffData,
  loadingDiff,
  onRefresh,
  headerPortalTarget,
}: {
  projectId: string;
  sessionId?: string;
  gitRepoPath: string;
  diffData: DiffResponse | null;
  loadingDiff: boolean;
  onRefresh: () => void;
  headerPortalTarget: HTMLDivElement | null;
}) {
  const projects = useAppStore((s) => s.projects);
  const currentProject = projects.find((p) => p.id === projectId);
  const okoneEnabled = !!(currentProject?.okone?.application_id);
  const [mrLoading, setMrLoading] = useState(false);
  const [mrResult, setMrResult] = useState<{ success: boolean; data?: CreateMRResponse; error?: string } | null>(null);
  const [showMrMenu, setShowMrMenu] = useState(false);

  // Close MR menu on outside click
  useEffect(() => {
    if (!showMrMenu) return;
    const handler = (e: MouseEvent) => {
      if (!(e.target as HTMLElement).closest("[data-mr-menu]")) setShowMrMenu(false);
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [showMrMenu]);

  const handleCreateMR = useCallback(async () => {
    if (mrLoading) return;
    setMrLoading(true);
    setMrResult(null);
    try {
      const data = await gitlabApi.createMR(projectId);
      setMrResult({ success: true, data });
    } catch (e: unknown) {
      setMrResult({ success: false, error: e instanceof Error ? e.message : "Unknown error" });
    } finally {
      setMrLoading(false);
    }
  }, [projectId, mrLoading]);

  const mrButton = gitRepoPath ? (
    <div className="relative" data-mr-menu>
      <div className="flex items-center">
        <Button
          size="sm"
          onClick={handleCreateMR}
          disabled={mrLoading}
          className={cn(
            "h-6 text-xs bg-blue-600 text-white hover:bg-blue-700 dark:bg-blue-600 dark:hover:bg-blue-700",
            okoneEnabled && "rounded-r-none border-r border-r-blue-500/40",
          )}
        >
          {mrLoading ? <Loader2 className="mr-1 h-3 w-3 animate-spin" /> : <GitMerge className="mr-1 h-3 w-3" />}
          Create MR
        </Button>
        {okoneEnabled && (
          <Button
            size="sm"
            disabled={mrLoading}
            onClick={() => setShowMrMenu(!showMrMenu)}
            className="h-6 px-1 bg-blue-600 text-white hover:bg-blue-700 dark:bg-blue-600 dark:hover:bg-blue-700 rounded-l-none"
          >
            <ChevronDown className="h-3 w-3" />
          </Button>
        )}
      </div>
      {showMrMenu && okoneEnabled && (
        <div className="absolute top-full right-0 mt-1 w-44 bg-popover border rounded-md shadow-lg overflow-hidden z-10">
          <button
            onClick={() => { setShowMrMenu(false); handleCreateMR(); }}
            className="w-full flex items-center gap-2 px-3 py-2 text-xs hover:bg-accent transition-colors"
          >
            <GitMerge className="h-3.5 w-3.5" />
            Create MR
          </button>
          <ChangeOrderMenuItem projectId={projectId} onClose={() => setShowMrMenu(false)} />
        </div>
      )}
    </div>
  ) : null;

  return (
    <>
      {/* Portal MR button into panel header */}
      {headerPortalTarget && mrButton && createPortal(mrButton, headerPortalTarget)}

      {/* MR result + plugin slots */}
      <div className="px-4 py-2 border-b space-y-2 empty:hidden">
        {mrResult && (
          <div className={cn("rounded-lg border p-3 text-sm", mrResult.success ? "border-green-500/30" : "border-destructive/30")}>
            <div className="flex items-start gap-2">
              {mrResult.success ? <Check className="h-4 w-4 text-green-500 shrink-0 mt-0.5" /> : <X className="h-4 w-4 text-destructive shrink-0 mt-0.5" />}
              <div className="flex-1 min-w-0">
                {mrResult.success && mrResult.data ? (
                  <>
                    <div className="font-medium text-green-600 dark:text-green-400 text-xs">MR Created</div>
                    <a href={mrResult.data.web_url} target="_blank" rel="noopener noreferrer" className="flex items-center gap-1 text-xs text-blue-500 hover:underline mt-0.5 break-all">
                      !{mrResult.data.iid} — {mrResult.data.title}
                      <ExternalLink className="h-3 w-3 shrink-0" />
                    </a>
                  </>
                ) : (
                  <>
                    <div className="font-medium text-destructive text-xs">MR Failed</div>
                    <div className="text-xs text-muted-foreground mt-0.5 break-all">{mrResult.error}</div>
                  </>
                )}
              </div>
              <button onClick={() => setMrResult(null)} className="text-muted-foreground hover:text-foreground shrink-0">
                <X className="h-3.5 w-3.5" />
              </button>
            </div>
          </div>
        )}
        <ChangeOrderResult projectId={projectId} />
      </div>

      {/* Diff view */}
      <div className="flex flex-col flex-1 overflow-hidden">
        {loadingDiff ? (
          <div className="flex items-center justify-center h-full text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin mr-2" />
            Loading changes...
          </div>
        ) : diffData?.files ? (
          <DiffView fileStats={diffData.files} projectId={projectId} sessionId={sessionId} onCommitted={onRefresh} />
        ) : (
          <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
            No changes found
          </div>
        )}
      </div>
    </>
  );
}

export function SessionWorkspace() {
  const {
    sessions, setSessions, activeSessionId,
    setCurrentSession, setCurrentProject, sessionTotalDuration,
    projects, setProjects, setGitDiffStats, chatMessages,
  } = useAppStore();
  const {
    rightPanel, setRightPanel, panelWidth, setPanelWidth,
    terminalHeight, setTerminalHeight,
    terminalPosition, setTerminalPosition,
    terminalStates, setTerminalShow, killTerminal,
  } = useLayoutStore();
  const isResizingTerminal = useRef(false);
  const sessionTerminal = terminalStates[activeSessionId ?? ""] ?? { show: false, key: 0 };
  const showTerminal = sessionTerminal.show;
  const terminalKey = sessionTerminal.key;
  // When terminal is moved to "right" and the active session has it visible,
  // it occupies the same right panel slot as Files/Git (mutually exclusive).
  const showingTerminalInRightSlot = terminalPosition === "right" && showTerminal;
  // Slot is visible when something should be drawn (a panel is selected or terminal is showing).
  const rightSlotVisible = !!rightPanel || showingTerminalInRightSlot;
  // Keep slot mounted (invisible) so terminal panels inside survive hide/show and mode swaps.
  const hasMountedTerminalsInRightMode = terminalPosition === "right" && Object.keys(terminalStates).length > 0;
  const rightSlotMounted = rightSlotVisible || hasMountedTerminalsInRightMode;

  // Workflow editing state — allows WorkflowPanel to replace the panel header title
  const [wfEditing, setWfEditing] = useState(false);
  const [wfName, setWfName] = useState('');
  const [wfNameEditable, setWfNameEditable] = useState(false);
  const [wfNameEditing, setWfNameEditing] = useState(false);
  const [wfNameDraft, setWfNameDraft] = useState('');
  const wfNameChangeRef = useRef<((name: string) => void) | null>(null);
  const wfBackRef = useRef<(() => void) | null>(null);

  const handleWfEditState = useCallback((editing: boolean, name: string, onNameChange: ((name: string) => void) | null, onBack: (() => void) | null) => {
    setWfEditing(editing);
    setWfName(name);
    setWfNameEditable(onNameChange !== null);
    setWfNameEditing(false);
    wfNameChangeRef.current = onNameChange;
    wfBackRef.current = onBack;
  }, []);
  const activeSessionIdRef = useRef(activeSessionId);
  const showTerminalRef = useRef(showTerminal);
  const isRealProjectRef = useRef(false);
  activeSessionIdRef.current = activeSessionId;
  showTerminalRef.current = showTerminal;

  // Track sessions where user sent first message but worktree not yet created.
  // Used to immediately disable the worktree selector and show a preparing indicator.
  const [preparingWorktrees, setPreparingWorktrees] = useState<Set<string>>(new Set());

  const gitDiffData = useAppStore((s) => s.gitDiffStats);
  const [loadingGitDiff, setLoadingGitDiff] = useState(false);
  const [gitHeaderActionsEl, setGitHeaderActionsEl] = useState<HTMLDivElement | null>(null);
  const isResizing = useRef(false);
  const containerRef = useRef<HTMLDivElement>(null);
  // New project dialog
  const [showNewProjectDialog, setShowNewProjectDialog] = useState(false);

  const activeSession = sessions.find((s) => s.id === activeSessionId) || null;
  const hasProjectId = !!(activeSession?.project_id);
  const boundProject = hasProjectId ? (projects.find((p) => p.id === activeSession!.project_id) || null) : null;
  const isRealProject = !!boundProject && boundProject.name !== UNASSIGNED_PROJECT;
  isRealProjectRef.current = isRealProject;

  // ── Worktree mode ──
  const handleSetWorktreeMode = async (mode: "local" | "worktree") => {
    if (!activeSession) return;
    try {
      const updated = await sessionsApi.setWorktreeMode(activeSession.id, mode);
      setSessions(sessions.map((s) => (s.id === updated.id ? updated : s)));
    } catch (e) {
      console.error("Failed to set worktree mode:", e);
      toast.error("Failed to set worktree mode", { description: String(e) });
    }
  };

  // Clear preparingWorktrees when session state resolves (worktree created or
  // mode reverted to local after failure).
  useEffect(() => {
    if (!activeSession) return;
    const id = activeSession.id;
    if (!preparingWorktrees.has(id)) return;
    if (activeSession.worktree_path || activeSession.worktree_mode !== "worktree") {
      setPreparingWorktrees((prev) => { const next = new Set(prev); next.delete(id); return next; });
      if (activeSession.worktree_mode !== "worktree") {
        toast.error("Worktree creation failed", { description: "Session fell back to local mode." });
      }
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeSession?.worktree_path, activeSession?.worktree_mode, activeSession?.id]);

  // ── Bind project to session ──
  const handleBindProject = async (projectId: string) => {
    if (!activeSession) return;
    try {
      const updated = await sessionsApi.update(activeSession.id, { project_id: projectId });
      setSessions(sessions.map((s) => (s.id === updated.id ? updated : s)));
      window.dispatchEvent(new CustomEvent("session-updated"));
    } catch (e) {
      console.error("Failed to bind project:", e);
      toast.error("Failed to bind project", { description: String(e) });
    }
  };

  const handleProjectCreated = async (project: Project) => {
    setProjects(
      projects.some((p) => p.id === project.id)
        ? projects.map((p) => (p.id === project.id ? project : p))
        : [project, ...projects],
    );
    await handleBindProject(project.id);
  };

  const loadProjects = async () => {
    try {
      const data = await projectsApi.list();
      setProjects(data || []);
    } catch (e) {
      console.error("Failed to load projects:", e);
    }
  };

  useEffect(() => {
    loadProjects();
    const handler = () => loadProjects();
    window.addEventListener("session-updated", handler);
    return () => window.removeEventListener("session-updated", handler);
  }, []);

  // Sync active session to store
  useEffect(() => {
    setCurrentSession(activeSession);
    setCurrentProject(boundProject);
  }, [activeSession, boundProject, setCurrentSession, setCurrentProject]);

  const worktreeSessionId = activeSession?.worktree_mode === "worktree" ? activeSession.id : undefined;

  // Reset built-in panel data when the bound project changes
  const prevProjectIdRef = useRef(boundProject?.id);
  useEffect(() => {
    if (prevProjectIdRef.current !== boundProject?.id) {
      prevProjectIdRef.current = boundProject?.id;
      setGitDiffStats(null);
      if (rightPanel === "git" && boundProject?.git_repo_path) {
        setLoadingGitDiff(true);
        diffApi.stats(boundProject.id)
          .then(setGitDiffStats)
          .catch((e) => console.error("Failed to load git diff:", e))
          .finally(() => setLoadingGitDiff(false));
      }
    }
  }, [boundProject, rightPanel, setGitDiffStats]);

  // When terminal occupies the right slot and the user opens a panel
  // (Files/Git/Tasks/etc.), the panel takes over the slot and the terminal hides.
  // Conversely, opening the terminal in right mode hides any open panel.
  const setRightPanelExclusive = useCallback((panel: string | null) => {
    setRightPanel(panel);
    if (panel !== null && terminalPosition === "right" && activeSessionId && showTerminal) {
      setTerminalShow(activeSessionId, false);
    }
  }, [setRightPanel, setTerminalShow, terminalPosition, activeSessionId, showTerminal]);

  const setTerminalShowExclusive = useCallback((sessionId: string, show: boolean) => {
    setTerminalShow(sessionId, show);
    if (show && terminalPosition === "right" && rightPanel && sessionId === activeSessionId) {
      setRightPanel(null);
    }
  }, [setRightPanel, setTerminalShow, terminalPosition, activeSessionId, rightPanel]);

  const handleOpenGit = useCallback(async () => {
    if (!boundProject) return;
    setRightPanelExclusive("git");
    setLoadingGitDiff(true);
    try {
      const data = await diffApi.stats(boundProject.id, worktreeSessionId);
      setGitDiffStats(data);
    } catch (e) {
      console.error("Failed to load git diff:", e);
      toast.error("Failed to load git diff", { description: String(e) });
    } finally {
      setLoadingGitDiff(false);
    }
  }, [boundProject, worktreeSessionId, setGitDiffStats, setRightPanelExclusive]);

  // Allow other components to open the Git panel via custom event
  useEffect(() => {
    const handler = () => handleOpenGit();
    window.addEventListener("open-git-panel", handler);
    return () => window.removeEventListener("open-git-panel", handler);
  }, [handleOpenGit]);

  // Auto-show terminal when the backend requests it (e.g. run_in_terminal MCP tool).
  // entity_id carries the session ID so we open the right session's terminal.
  // Also listens for session/updated events (e.g. worktree_path set) to reload session data.
  useEffect(() => {
    return globalWS.subscribeGlobal((event) => {
      if (event.type === "change" && event.entity_type === "terminal" && event.action === "open") {
        const sid = (event.entity_id as string) || activeSessionIdRef.current;
        if (sid) setTerminalShowExclusive(sid, true);
      }
      if (event.type === "change" && event.entity_type === "session" && event.action === "updated") {
        window.dispatchEvent(new CustomEvent("session-updated"));
      }
    });
  }, [setTerminalShowExclusive]);

  // Ctrl+` keyboard shortcut to toggle terminal (mirrors right sidebar terminal button)
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === "`" && e.ctrlKey && !e.altKey && !e.metaKey) {
        if (!activeSessionIdRef.current || !isRealProjectRef.current) return;
        e.preventDefault();
        setTerminalShowExclusive(activeSessionIdRef.current, !showTerminalRef.current);
      }
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, [setTerminalShowExclusive]);

  const refreshGitReview = useCallback(async () => {
    if (!boundProject) return;
    try {
      const data = await diffApi.stats(boundProject.id, worktreeSessionId);
      setGitDiffStats(data);
    } catch (e) {
      console.error("Failed to refresh git:", e);
      toast.error("Failed to refresh git", { description: String(e) });
    }
  }, [boundProject, worktreeSessionId, setGitDiffStats]);

  // Right panel resize handler
  const handleMouseDown = useCallback((e: React.MouseEvent) => {
    isResizing.current = true;
    e.preventDefault();
    const onMove = (ev: MouseEvent) => {
      if (!isResizing.current || !containerRef.current) return;
      const rect = containerRef.current.getBoundingClientRect();
      setPanelWidth(Math.min(PANEL_WIDTH_MAX, Math.max(PANEL_WIDTH_MIN, rect.right - ev.clientX)));
    };
    const onUp = () => {
      isResizing.current = false;
      document.removeEventListener("mousemove", onMove);
      document.removeEventListener("mouseup", onUp);
    };
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  }, []);

  // Terminal resize handler (vertical)
  const handleTerminalMouseDown = useCallback((e: React.MouseEvent) => {
    isResizingTerminal.current = true;
    e.preventDefault();
    const onMove = (ev: MouseEvent) => {
      if (!isResizingTerminal.current || !containerRef.current) return;
      const rect = containerRef.current.getBoundingClientRect();
      setTerminalHeight(Math.min(TERMINAL_HEIGHT_MAX, Math.max(TERMINAL_HEIGHT_MIN, rect.bottom - ev.clientY)));
    };
    const onUp = () => {
      isResizingTerminal.current = false;
      document.removeEventListener("mousemove", onMove);
      document.removeEventListener("mouseup", onUp);
    };
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  }, []);

  return (
    <div ref={containerRef} className="flex flex-col h-full">
      {/* Toolbar — always visible */}
      <div className="flex items-center gap-1.5 px-3 py-1.5 border-b bg-muted/20 text-sm shrink-0 min-h-[36px]">
        <div className="flex-1 min-w-0">
          {activeSession && (
            <span className="text-sm font-medium text-foreground/80 truncate block max-w-[50%]">
              {activeSession.name || "New Session"}
            </span>
          )}
        </div>

        {/* Tasks & Assets panel buttons */}
        <Button
          variant={rightPanel === "task" ? "secondary" : "ghost"}
          size="sm"
          className="h-7 text-xs gap-1"
          onClick={() => setRightPanelExclusive(rightPanel === "task" ? null : "task")}
        >
          <ListTodo className="h-3.5 w-3.5" />
          Task
        </Button>
        <Button
          variant={rightPanel === "asset" ? "secondary" : "ghost"}
          size="sm"
          className="h-7 text-xs gap-1"
          onClick={() => setRightPanelExclusive(rightPanel === "asset" ? null : "asset")}
        >
          <FileText className="h-3.5 w-3.5" />
          Asset
        </Button>

        {/* Workflows button */}
        <Button
          variant={rightPanel === "workflow" ? "secondary" : "ghost"}
          size="sm"
          className="h-7 text-xs gap-1"
          onClick={() => setRightPanelExclusive(rightPanel === "workflow" ? null : "workflow")}
        >
          <Workflow className="h-3.5 w-3.5" />
          Workflow
        </Button>

      </div>

      {/* Main content */}
      {activeSession ? (
        <div className="flex flex-col flex-1 overflow-hidden">
        <div className="flex flex-1 overflow-hidden">
          {/* Chat area */}
          <div className="flex-1 flex flex-col overflow-hidden">
            {!hasProjectId && (
              <ProjectSelector
                projects={projects}
                onBind={handleBindProject}
                onNewProject={() => setShowNewProjectDialog(true)}
              />
            )}

            {/* Chat */}
            <ChatPanel
              session={activeSession}
              project={boundProject}
              onMessageSent={
                activeSession.worktree_mode === "worktree" && !activeSession.worktree_path
                  ? () => setPreparingWorktrees((prev) => new Set([...prev, activeSession.id]))
                  : undefined
              }
            />

          </div>

          {/* Right panel — shared slot for Files/Git/Tasks/etc. AND terminal when terminalPosition === "right".
              Stays mounted (invisible via display:none) while any terminal exists in right mode, so its
              PTY survives panel switches and hide/show — matching the bottom-mode behavior. */}
          {rightSlotMounted && (
            <div style={{ display: rightSlotVisible ? "contents" : "none" }}>
              <div
                className="w-1 cursor-col-resize hover:bg-primary/20 active:bg-primary/40 transition-colors flex items-center justify-center shrink-0"
                onMouseDown={handleMouseDown}
              >
                <GripVertical className="h-4 w-4 text-muted-foreground/50" />
              </div>
              <div
                className="border-l bg-background overflow-hidden shrink-0 flex flex-col"
                style={{ width: panelWidth }}
              >
                {/* Header — terminal vs rightPanel based on current mode */}
                {showingTerminalInRightSlot ? (
                  <div className="flex items-center justify-between px-4 py-2 border-b gap-2 shrink-0">
                    <div className="flex items-center gap-1 flex-1 min-w-0">
                      <Button variant="ghost" size="sm" className="h-7 w-7 p-0" onClick={() => activeSessionId && setTerminalShow(activeSessionId, false)}>
                        <PanelRightClose className="h-4 w-4" />
                      </Button>
                      <h3 className="font-medium text-sm">Terminal</h3>
                    </div>
                    <div className="flex items-center gap-0.5">
                      <Button variant="ghost" size="sm" className="h-7 w-7 p-0" title="Move to bottom" onClick={() => setTerminalPosition("bottom")}>
                        <PanelBottom className="h-4 w-4" />
                      </Button>
                      <Button variant="ghost" size="sm" className="h-7 w-7 p-0" title="Kill terminal" onClick={() => activeSessionId && killTerminal(activeSessionId)}>
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  </div>
                ) : rightPanel ? (
                  <>
                {/* Panel header */}
                <div className="flex items-center justify-between px-4 py-2 border-b gap-2 shrink-0">
                  <div className="flex items-center gap-1 flex-1 min-w-0">
                    {rightPanel === "workflow" && wfEditing ? (
                      <>
                        <Button variant="ghost" size="sm" className="h-7 w-7 p-0 shrink-0" onClick={() => { setWfNameEditing(false); wfBackRef.current?.(); }}>
                          <ArrowLeft className="h-4 w-4" />
                        </Button>
                        {wfNameEditing ? (
                          <div className="flex items-center gap-0.5 flex-1 min-w-0">
                            <input
                              type="text"
                              value={wfNameDraft}
                              onChange={(e) => setWfNameDraft(e.target.value)}
                              onKeyDown={(e) => {
                                if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
                                  setWfName(wfNameDraft);
                                  wfNameChangeRef.current?.(wfNameDraft);
                                  setWfNameEditing(false);
                                } else if (e.key === 'Escape') {
                                  setWfNameDraft(wfName);
                                  setWfNameEditing(false);
                                }
                              }}
                              // eslint-disable-next-line jsx-a11y/no-autofocus
                              autoFocus
                              placeholder="Workflow Name"
                              className="text-sm font-medium bg-transparent border-b border-border outline-none flex-1 min-w-0"
                            />
                            <button
                              onClick={() => { setWfName(wfNameDraft); wfNameChangeRef.current?.(wfNameDraft); setWfNameEditing(false); }}
                              className="p-1 rounded text-green-500 hover:bg-accent/60 transition-colors shrink-0"
                              title="Save"
                            >
                              <Check className="h-3.5 w-3.5" />
                            </button>
                            <button
                              onClick={() => { setWfNameDraft(wfName); setWfNameEditing(false); }}
                              className="p-1 rounded text-muted-foreground hover:bg-accent/60 transition-colors shrink-0"
                              title="Cancel"
                            >
                              <X className="h-3.5 w-3.5" />
                            </button>
                          </div>
                        ) : (
                          <div className="group/wftitle flex items-center gap-1 flex-1 min-w-0">
                            <span className="text-sm font-medium truncate">{wfName || 'Untitled Workflow'}</span>
                            {wfNameEditable && (
                              <button
                                onClick={() => { setWfNameDraft(wfName); setWfNameEditing(true); }}
                                className="opacity-0 group-hover/wftitle:opacity-100 p-1 rounded text-muted-foreground/60 hover:text-foreground hover:bg-accent/60 transition-all shrink-0"
                                title="Edit name"
                              >
                                <Pencil className="h-3 w-3" />
                              </button>
                            )}
                          </div>
                        )}
                      </>
                    ) : (
                      <>
                        <Button variant="ghost" size="sm" className="h-7 w-7 p-0" onClick={() => setRightPanel(null)}>
                          <PanelRightClose className="h-4 w-4" />
                        </Button>
                        <h3 className="font-medium text-sm">
                          {rightPanel === "git" ? "Git" : rightPanel === "files" ? "Files" : rightPanel === "task" ? "Tasks" : rightPanel === "asset" ? "Assets" : rightPanel === "workflow" ? "Workflows" : rightPanel === "ralph" ? "RALPH" : rightPanel}
                        </h3>
                      </>
                    )}
                  </div>
                  {rightPanel === "git" && <div ref={setGitHeaderActionsEl} />}
                </div>

                {/* Panel content */}
                {rightPanel === "git" && boundProject && (
                  <GitPanelContent
                    projectId={boundProject.id}
                    sessionId={worktreeSessionId}
                    gitRepoPath={boundProject.git_repo_path}
                    diffData={gitDiffData}
                    loadingDiff={loadingGitDiff}
                    onRefresh={refreshGitReview}
                    headerPortalTarget={gitHeaderActionsEl}
                  />
                )}
                {rightPanel === "files" && boundProject && (
                  <div className="flex-1 overflow-hidden">
                    <Suspense fallback={<div className="flex items-center justify-center h-full"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div>}>
                      <FileBrowser key={`${boundProject.id}-${worktreeSessionId ?? "local"}`} projectId={boundProject.id} sessionId={worktreeSessionId} />
                    </Suspense>
                  </div>
                )}
                {(rightPanel === "task" || rightPanel === "asset" || rightPanel === "workflow" || rightPanel === "ralph" || rightPanel === "files" || rightPanel === "git") && !boundProject && (
                  <div className="flex-1 flex items-center justify-center text-muted-foreground text-sm">
                    Bind a project to this session first
                  </div>
                )}
                {rightPanel === "task" && boundProject && (
                  <div className="flex flex-col flex-1 overflow-hidden">
                    <TaskPlugin key={boundProject.id} projectId={boundProject.id} />
                  </div>
                )}
                {rightPanel === "asset" && boundProject && (
                  <div className="flex flex-col flex-1 overflow-hidden">
                    <AssetPanel key={boundProject.id} projectId={boundProject.id} />
                  </div>
                )}
                {boundProject && (
                  <div className="flex flex-col flex-1 overflow-hidden" style={{ display: rightPanel === "workflow" ? undefined : "none" }}>
                    <WorkflowPanel key={boundProject.id} projectId={boundProject.id} onEditStateChange={handleWfEditState} />
                  </div>
                )}
                {rightPanel === "ralph" && boundProject && (
                  <div className="flex flex-col flex-1 overflow-hidden">
                    <RalphPanel key={boundProject.id} projectId={boundProject.id} />
                  </div>
                )}
                  </>
                ) : null}

                {/* Terminal panels — always rendered when terminalPosition === "right" so PTY connections
                    survive panel switches and hide/show, matching the bottom-mode behavior. */}
                {terminalPosition === "right" && (
                  <div style={{ display: showingTerminalInRightSlot ? "contents" : "none" }}>
                    {sessions.map((session) => {
                      const ts = terminalStates[session.id];
                      if (!ts) return null;
                      const sessionProject = projects.find((p) => p.id === session.project_id);
                      if (!sessionProject || sessionProject.name === UNASSIGNED_PROJECT) return null;
                      const isActive = session.id === activeSessionId;
                      const isVisible = isActive && ts.show;
                      return (
                        <div key={session.id} className="flex-1 overflow-hidden" style={{ display: isVisible ? "block" : "none" }}>
                          <TerminalPanel
                            key={ts.key}
                            sessionId={session.id}
                            visible={isActive && ts.show}
                            onExited={() => killTerminal(session.id)}
                          />
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>
            </div>
          )}

          {/* Right activity bar — always visible */}
          {isRealProject && (
            <div className="flex flex-col items-center gap-3 px-1 py-3 border-l bg-muted/10 shrink-0">
              {boundProject?.git_repo_path && (
                <button
                  onClick={() => setRightPanelExclusive(rightPanel === "files" ? null : "files")}
                  className={cn(
                    "p-1.5 rounded transition-colors",
                    rightPanel === "files" ? "bg-accent text-accent-foreground" : "text-muted-foreground/50 hover:text-muted-foreground hover:bg-accent/50"
                  )}
                  title="Files"
                >
                  <FolderCode className="h-4 w-4" />
                </button>
              )}
              <button
                onClick={() => activeSessionId && setTerminalShowExclusive(activeSessionId, !showTerminal)}
                className={cn(
                  "p-1.5 rounded transition-colors",
                  showTerminal ? "bg-accent text-accent-foreground" : "text-muted-foreground/50 hover:text-muted-foreground hover:bg-accent/50"
                )}
                title="Terminal"
              >
                <TerminalSquare className="h-4 w-4" />
              </button>
              {boundProject?.git_repo_path && (
                <button
                  onClick={() => rightPanel === "git" ? setRightPanelExclusive(null) : handleOpenGit()}
                  className={cn(
                    "relative p-1.5 rounded transition-colors",
                    rightPanel === "git" ? "bg-accent text-accent-foreground" : "text-muted-foreground/50 hover:text-muted-foreground hover:bg-accent/50"
                  )}
                  title="Git"
                >
                  <GitBranch className="h-4 w-4" />
                  {gitDiffData && (gitDiffData.insertions > 0 || gitDiffData.deletions > 0) && (
                    <span className="absolute -top-0.5 -right-0.5 h-2 w-2 rounded-full bg-blue-500" />
                  )}
                </button>
              )}
            </div>
          )}
        </div>

        {/* Session info */}
        <div className="flex items-center gap-3 px-4 py-1 shrink-0">
          {isRealProject && (
            <WorktreeSelector
              sessionId={activeSession.id}
              mode={activeSession.worktree_mode || "local"}
              locked={!!activeSession.agent_session_id || preparingWorktrees.has(activeSession.id) || !!activeSession.worktree_path || (chatMessages[activeSession.id] || []).some(m => m.role === "user")}
              worktreePending={preparingWorktrees.has(activeSession.id) && !activeSession.worktree_path}
              hasGitRepo={!!boundProject?.git_repo_path}
              onSetMode={handleSetWorktreeMode}
            />
          )}
          {hasProjectId && (
            <div className="flex items-center gap-1.5 min-w-0">
              <FolderGit2 className="h-3 w-3 text-muted-foreground/60 shrink-0" />
              <span className="text-[11px] text-muted-foreground/70 truncate">
                {boundProject?.name || "Loading..."}
                {(activeSession.worktree_mode === "worktree" && activeSession.worktree_path
                  ? activeSession.worktree_path
                  : boundProject?.git_repo_path) && (
                  <span className="text-muted-foreground/50 ml-1">
                    ({activeSession.worktree_mode === "worktree" && activeSession.worktree_path
                      ? activeSession.worktree_path
                      : boundProject?.git_repo_path})
                  </span>
                )}
              </span>
            </div>
          )}
          {(sessionTotalDuration[activeSession.id] || 0) > 0 && (
            <div className="flex items-center gap-1 ml-auto shrink-0">
              <Clock className="h-3 w-3 text-muted-foreground/60" />
              <span className="text-[11px] text-muted-foreground/70 tabular-nums">
                {formatTotalDuration(sessionTotalDuration[activeSession.id])}
              </span>
            </div>
          )}
        </div>

        {/* Bottom terminal panels — one per session that has ever opened a terminal.
            All are kept mounted (preserving the PTY session); only the active
            session's terminal is visible via display:contents vs display:none. */}
        {terminalPosition === "bottom" && sessions.map((session) => {
          const ts = terminalStates[session.id];
          if (!ts) return null; // never opened for this session
          const sessionProject = projects.find((p) => p.id === session.project_id);
          if (!sessionProject || sessionProject.name === UNASSIGNED_PROJECT) return null;

          const isActive = session.id === activeSessionId;
          const isVisible = isActive && ts.show;

          return (
            <div key={session.id} style={{ display: isVisible ? "contents" : "none" }}>
              <div
                className="h-1 cursor-row-resize hover:bg-primary/20 active:bg-primary/40 transition-colors flex items-center justify-center shrink-0 border-t"
                onMouseDown={handleTerminalMouseDown}
              >
                <GripVertical className="h-3 w-3 text-muted-foreground/50 rotate-90" />
              </div>
              <div className="shrink-0 overflow-hidden bg-background" style={{ height: terminalHeight }}>
                <div className="flex items-center justify-between px-3 py-1 border-b shrink-0">
                  <div className="flex items-center gap-2">
                    <span className="text-xs text-muted-foreground font-medium">Terminal</span>
                  </div>
                  <div className="flex items-center gap-0.5">
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-5 w-5 p-0"
                      title="Move to right"
                      onClick={() => {
                        setTerminalPosition("right");
                        // In right mode the terminal shares the slot with Files/Git;
                        // prevent overlap by closing any open right panel.
                        if (rightPanel) setRightPanel(null);
                      }}
                    >
                      <PanelRight className="h-3 w-3" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-5 w-5 p-0"
                      title="Kill terminal"
                      onClick={() => killTerminal(session.id)}
                    >
                      <Trash2 className="h-3 w-3" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-5 w-5 p-0"
                      title="Hide terminal"
                      onClick={() => setTerminalShow(session.id, false)}
                    >
                      <X className="h-3 w-3" />
                    </Button>
                  </div>
                </div>
                <div style={{ height: "calc(100% - 28px)" }}>
                  <TerminalPanel
                    key={ts.key}
                    sessionId={session.id}
                    visible={isActive && ts.show}
                    onExited={() => killTerminal(session.id)}
                  />
                </div>
              </div>
            </div>
          );
        })}
        </div>
      ) : (
        <div className="flex-1 flex items-center justify-center text-muted-foreground">
          <p className="text-sm">Select or create a session from the sidebar</p>
        </div>
      )}

      {/* Project Dialog */}
      <ProjectDialog
        open={showNewProjectDialog}
        onOpenChange={setShowNewProjectDialog}
        onSuccess={handleProjectCreated}
      />
    </div>
  );
}

