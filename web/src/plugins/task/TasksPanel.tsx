import { useState, useEffect, useCallback, useMemo, useRef } from "react";
import {
  Plus,
  Loader2,
  Search,
  SlidersHorizontal,
  ListFilter,
  Tag,
} from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import { toast } from "sonner";
import { tasksApi, projectsApi, serverApi } from "@/lib/api";
import { globalWS } from "@/lib/ws";
import { cn } from "@/lib/utils";
import type { Task, Project } from "@/types";
import { LabelManager } from "./components/LabelManager";
import { UserPicker } from "./components/UserPicker";
import { IssueRow } from "./IssueRow";
import {
  type TaskStatus,
  type TaskPriority,
  STATUS_LIST,
  PRIORITY_LIST,
  STATUS_MAP,
  PRIORITY_MAP,
  STATUS_ORDER,
  PRIORITY_ORDER,
  normalizeStatus,
  normalizePriority,
} from "./task-config";
import { STATUS_ICONS, PRIORITY_ICONS } from "./task-icons";

// ═══════════════════════════════════════════════════════════════════════════
// New-task draft persistence
// ═══════════════════════════════════════════════════════════════════════════

interface NewTaskDraft {
  title: string;
  description: string;
  priority: string;
  status: string;
  projectId: string;
  assigner: string;
}

const newTaskDraftKey = (projectId: string) => `new-task-draft-${projectId}`;

const saveNewTaskDraft = (projectId: string, draft: NewTaskDraft) =>
  localStorage.setItem(newTaskDraftKey(projectId), JSON.stringify(draft));

const loadNewTaskDraft = (projectId: string): NewTaskDraft | null => {
  try {
    const raw = localStorage.getItem(newTaskDraftKey(projectId));
    return raw ? (JSON.parse(raw) as NewTaskDraft) : null;
  } catch {
    return null;
  }
};

const clearNewTaskDraft = (projectId: string) =>
  localStorage.removeItem(newTaskDraftKey(projectId));

// ═══════════════════════════════════════════════════════════════════════════
// Tree & Grouping
// ═══════════════════════════════════════════════════════════════════════════

type GroupBy = "status" | "priority" | "none";

interface DisplayTask {
  task: Task;
  status: TaskStatus;
  priority: TaskPriority;
  depth: number;
  hasChildren: boolean;
  childCount: number;
  isExpanded: boolean;
}

function buildFlatList(tasks: Task[]): DisplayTask[] {
  return tasks.map((task) => ({
    task,
    status: normalizeStatus(task.status),
    priority: normalizePriority(task.priority),
    depth: 0,
    hasChildren: false,
    childCount: 0,
    isExpanded: false,
  }));
}

interface GroupData {
  key: string;
  label: string;
  color: string;
  Icon: React.FC<{ className?: string }>;
  items: DisplayTask[];
}

function buildGroupedList(
  tasks: Task[],
  groupBy: GroupBy,
  searchQuery: string
): GroupData[] {
  const all = buildFlatList(tasks);

  const filtered = searchQuery.trim()
    ? all.filter((d) => {
        const q = searchQuery.toLowerCase();
        return d.task.title.toLowerCase().includes(q) || d.task.id.toLowerCase().includes(q);
      })
    : all;

  if (groupBy === "none") {
    return [{
      key: "__all__",
      label: "All Tasks",
      color: "text-foreground",
      Icon: () => null,
      items: filtered,
    }];
  }

  if (groupBy === "status") {
    return STATUS_ORDER.map((status) => {
      const cfg = STATUS_MAP.get(status)!;
      const SIcon = STATUS_ICONS[status];
      return {
        key: status,
        label: cfg.label,
        color: cfg.color,
        Icon: SIcon,
        items: filtered.filter((d) => d.status === status),
      };
    }).filter((g) => g.items.length > 0);
  }

  // groupBy === "priority"
  return PRIORITY_ORDER.map((priority) => {
    const cfg = PRIORITY_MAP.get(priority)!;
    const PIcon = PRIORITY_ICONS[priority];
    return {
      key: priority,
      label: cfg.label,
      color: cfg.color,
      Icon: PIcon,
      items: filtered.filter((d) => d.priority === priority),
    };
  }).filter((g) => g.items.length > 0);
}

// ═══════════════════════════════════════════════════════════════════════════
// GroupSection – Linear style
// ═══════════════════════════════════════════════════════════════════════════

function GroupSection({
  group,
  collapsed,
  onToggle,
  onAddInGroup,
  children,
  hideHeader,
}: {
  group: GroupData;
  collapsed: boolean;
  onToggle: () => void;
  onAddInGroup: () => void;
  children: React.ReactNode;
  hideHeader?: boolean;
}) {
  if (hideHeader) {
    return <div>{children}</div>;
  }

  return (
    <div>
      <div className="sticky top-0 z-10 flex items-center h-[36px] bg-background/95 backdrop-blur-sm border-b border-border/20 px-4">
        <button
          onClick={onToggle}
          className="flex items-center gap-2.5 flex-1 h-full min-w-0"
        >
          <span className={cn("shrink-0", group.color)}>
            <group.Icon />
          </span>
          <span className="text-[13px] font-medium text-foreground">{group.label}</span>
          <span className="text-[12px] text-muted-foreground tabular-nums">
            ▲ {group.items.length}
          </span>
        </button>

        <button
          onClick={onAddInGroup}
          className="shrink-0 p-1 rounded hover:bg-accent/40 text-muted-foreground/60 hover:text-foreground transition-colors"
          title="Add task"
        >
          <Plus className="h-4 w-4" />
        </button>
      </div>

      {!collapsed && children}
    </div>
  );
}

// ═══════════════════════════════════════════════════════════════════════════
// Inline New Issue Row
// ═══════════════════════════════════════════════════════════════════════════

function NewIssueForm({
  parentId,
  defaultStatus,
  defaultPriority,
  defaultProjectId,
  onSubmit,
  onCancel,
}: {
  parentId: string | null;
  defaultStatus?: TaskStatus;
  defaultPriority?: TaskPriority;
  defaultProjectId?: string;
  onSubmit: (title: string, parentId: string | null, status?: TaskStatus, priority?: TaskPriority, description?: string, projectId?: string, assigner?: string) => void;
  onCancel: () => void;
}) {
  const draftKey = defaultProjectId ? newTaskDraftKey(defaultProjectId) : null;
  const savedDraft = draftKey ? loadNewTaskDraft(defaultProjectId!) : null;

  const [title, setTitle] = useState(savedDraft?.title ?? "");
  const [description, setDescription] = useState(savedDraft?.description ?? "");
  const [priority, setPriority] = useState<TaskPriority>((savedDraft?.priority as TaskPriority) ?? defaultPriority ?? "medium");
  const [status, setStatus] = useState<TaskStatus>((savedDraft?.status as TaskStatus) ?? defaultStatus ?? "open");
  const [projectId, setProjectId] = useState(savedDraft?.projectId ?? defaultProjectId ?? "");
  const [assigner, setAssigner] = useState(savedDraft?.assigner ?? "");
  const titleRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    setTimeout(() => titleRef.current?.focus(), 50);
  }, []);

  // Persist draft on every change
  useEffect(() => {
    if (!defaultProjectId) return;
    saveNewTaskDraft(defaultProjectId, { title, description, priority, status, projectId, assigner });
  }, [title, description, priority, status, projectId, assigner, defaultProjectId]);

  const handleSubmit = () => {
    const t = title.trim();
    if (!t) return;
    if (defaultProjectId) clearNewTaskDraft(defaultProjectId);
    onSubmit(t, parentId, status, priority, description.trim() || undefined, projectId || undefined, assigner.trim() || undefined);
  };

  const handleCancel = () => {
    if (defaultProjectId) clearNewTaskDraft(defaultProjectId);
    onCancel();
  };

  return (
    <div className="px-3 py-2 border-b space-y-2">
      <input
        ref={titleRef}
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") handleCancel();
        }}
        placeholder={parentId ? "Sub-task title…" : "Task title…"}
        className="w-full text-xs bg-transparent border rounded px-2 py-1 focus:outline-none focus:ring-1 focus:ring-ring"
      />
      <div className="flex gap-2 flex-wrap">
        <select
          value={status}
          onChange={(e) => setStatus(e.target.value as TaskStatus)}
          className="text-xs bg-background border rounded px-2 py-1 focus:outline-none focus:ring-1 focus:ring-ring"
        >
          {STATUS_LIST.map((s) => (
            <option key={s.value} value={s.value}>{s.label}</option>
          ))}
        </select>
        <select
          value={priority}
          onChange={(e) => setPriority(e.target.value as TaskPriority)}
          className="text-xs bg-background border rounded px-2 py-1 focus:outline-none focus:ring-1 focus:ring-ring"
        >
          {PRIORITY_LIST.map((p) => (
            <option key={p.value} value={p.value}>{p.label}</option>
          ))}
        </select>
        <UserPicker
          value={assigner}
          onChange={(val) => setAssigner(val)}
          placeholder="Assignee…"
        />
      </div>
      <textarea
        value={description}
        onChange={(e) => setDescription(e.target.value)}
        placeholder="Description (Markdown supported)"
        rows={4}
        className="w-full text-xs bg-transparent border rounded px-2 py-1 resize-y min-h-[60px] focus:outline-none focus:ring-1 focus:ring-ring"
      />
      <div className="flex justify-end gap-1.5">
        <button
          onClick={handleCancel}
          className="text-xs px-2 py-1 rounded hover:bg-accent transition-colors text-muted-foreground"
        >
          Cancel
        </button>
        <button
          onClick={handleSubmit}
          disabled={!title.trim()}
          className="text-xs px-2 py-1 rounded bg-primary text-primary-foreground hover:bg-primary/90 transition-colors disabled:opacity-50"
        >
          Create
        </button>
      </div>
    </div>
  );
}

// ═══════════════════════════════════════════════════════════════════════════
// Main TasksPage
// ═══════════════════════════════════════════════════════════════════════════

export function TasksPage({ embedded = false, onPickTask, onSelectTask, currentProjectId, refreshKey }: { embedded?: boolean; onPickTask?: (task: Task) => void; onSelectTask?: (taskId: string) => void; currentProjectId?: string; refreshKey?: number } = {}) {
  const [tasks, setTasks]                     = useState<Task[]>([]);
  const [loading, setLoading]                 = useState(true);
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(new Set());
  const [groupBy, setGroupBy]                 = useState<GroupBy>("none");
  const [searchQuery, setSearchQuery]         = useState("");
  const [showSearch, setShowSearch]           = useState(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const [projects, setProjects]               = useState<Project[]>([]);
  type AssignerFilter = "all" | "assigned" | "unassigned" | "mine";
  const [filterAssigner, setFilterAssigner]   = useState<AssignerFilter>("all");
  const [filterLabels, setFilterLabels]       = useState<Set<string>>(new Set());
  const [currentUser, setCurrentUser]         = useState("");
  const [showLabelManager, setShowLabelManager] = useState(false);
  const [hasMore, setHasMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const PAGE_SIZE = 100;
  const [activeTab, setActiveTab]             = useState<"active" | "completed">("active");
  const migratingRef = useRef(false);
  const projectNameMap = useMemo(() => {
    const m = new Map<string, string>();
    for (const p of projects) m.set(p.id, p.name);
    return m;
  }, [projects]);

  const [newIssue, setNewIssue] = useState<{
    parentId: string | null;
    defaultStatus?: TaskStatus;
    defaultPriority?: TaskPriority;
  } | undefined>(() =>
    // Auto-restore form if a draft was saved for this project
    currentProjectId && loadNewTaskDraft(currentProjectId) ? { parentId: null } : undefined
  );

  // ── Load ──

  const load = useCallback(async () => {
    try {
      const [taskList, projList] = await Promise.all([
        tasksApi.list(PAGE_SIZE, 0, currentProjectId || undefined),
        projectsApi.list(),
      ]);
      setProjects(projList || []);
      const fetched = taskList || [];
      setTasks(fetched);
      setHasMore(fetched.length >= PAGE_SIZE);
    } catch (e) {
      console.error("Failed to load tasks:", e);
    } finally {
      setLoading(false);
    }
  }, [currentProjectId]);

  const loadMore = async () => {
    setLoadingMore(true);
    try {
      const more = await tasksApi.list(PAGE_SIZE, tasks.length, currentProjectId || undefined);
      const fetched = more || [];
      setTasks((prev) => [...prev, ...fetched]);
      setHasMore(fetched.length >= PAGE_SIZE);
    } catch (e) {
      console.error("Failed to load more tasks:", e);
    } finally {
      setLoadingMore(false);
    }
  };

  useEffect(() => {
    load();
    serverApi.info().then((info: any) => {
      if (info.user_name) setCurrentUser(info.user_name);
    }).catch(() => {});
  }, [load]);

  // Reload when returning from detail view
  useEffect(() => {
    if (refreshKey) load();
  }, [refreshKey]);

  // Poll for updates every 60s so changes from other users (e.g. on GitLab) are visible
  // Pauses when the tab is hidden to avoid unnecessary API calls
  useEffect(() => {
    let id: ReturnType<typeof setInterval> | null = setInterval(load, 60_000);
    const onVisibility = () => {
      if (document.hidden) {
        if (id) { clearInterval(id); id = null; }
      } else {
        load();
        id = setInterval(load, 60_000);
      }
    };
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      if (id) clearInterval(id);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [load]);

  useEffect(() => {
    return globalWS.subscribeGlobal((event) => {
      if (event.type === "change" && event.entity_type === "task") load();
    });
  }, [load]);

  // ── Keyboard shortcuts ──

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement;
      if (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable) return;
      if (e.key === "c" && !e.metaKey && !e.ctrlKey && !e.altKey) {
        e.preventDefault();
        setNewIssue({ parentId: null });
      }
      if (e.key === "/" && !e.metaKey && !e.ctrlKey && !e.altKey) {
        e.preventDefault();
        setShowSearch(true);
        setTimeout(() => searchRef.current?.focus(), 50);
      }
      if (e.key === "Escape") {
        setShowSearch(false);
        setSearchQuery("");
      }
    };
    document.addEventListener("keydown", h);
    return () => document.removeEventListener("keydown", h);
  }, []);

  // ── Data ──

  const allLabels = useMemo(() => {
    const names = new Set<string>();
    for (const t of tasks) {
      for (const name of (t.labels || [])) {
        names.add(name);
      }
    }
    return Array.from(names).sort();
  }, [tasks]);

  const filteredTasks = useMemo(() => {
    let result = tasks;

    // Filter by tab (active vs completed)
    if (activeTab === "active") {
      result = result.filter((t) => t.status !== "closed");
    } else {
      result = result.filter((t) => t.status === "closed");
    }

    // Filter by assigner
    if (filterAssigner === "assigned") result = result.filter((t) => t.assigner && t.assigner.length > 0);
    else if (filterAssigner === "unassigned") result = result.filter((t) => !t.assigner);
    else if (filterAssigner === "mine") result = result.filter((t) => t.assigner && currentUser && t.assigner === currentUser);

    // Filter by labels
    if (filterLabels.size > 0) {
      result = result.filter((t) => (t.labels || []).some((name) => filterLabels.has(name)));
    }

    // Sort by updated_at descending (newest first)
    result.sort((a, b) => {
      const aTime = new Date(a.updated_at).getTime();
      const bTime = new Date(b.updated_at).getTime();
      return bTime - aTime;
    });

    return result;
  }, [tasks, activeTab, filterAssigner, filterLabels, currentUser]);

  const groups = useMemo(
    () => buildGroupedList(filteredTasks, groupBy, searchQuery),
    [filteredTasks, groupBy, searchQuery]
  );

  const totalCount = tasks.length;

  // ── Callbacks ──

  const toggleGroup = useCallback(
    (key: string) => setCollapsedGroups((p) => { const n = new Set(p); n.has(key) ? n.delete(key) : n.add(key); return n; }),
    []
  );

  const handleAddChild = useCallback(
    (parentId: string) => {
      setNewIssue({ parentId });
    },
    []
  );

  const handleAddInGroup = useCallback(
    (groupKey: string) => {
      if (groupBy === "status") {
        setNewIssue({ parentId: null, defaultStatus: groupKey as TaskStatus });
      } else if (groupBy === "priority") {
        setNewIssue({ parentId: null, defaultPriority: groupKey as TaskPriority });
      } else {
        setNewIssue({ parentId: null });
      }
    },
    [groupBy]
  );

  const handleCreate = useCallback(
    async (title: string, parentId: string | null, status?: TaskStatus, priority?: TaskPriority, description?: string, projectId?: string, assigner?: string) => {
      try {
        const task = await tasksApi.create({
          title,
          parent_id: parentId || undefined,
          priority,
          description,
          project_id: projectId,
          assigner,
        });
        if (status && status !== "open") {
          const updated = await tasksApi.update(task.id, { status });
          setTasks((p) => [...p, updated]);
        } else {
          setTasks((p) => [...p, task]);
        }
        setNewIssue(undefined);
      } catch (e) {
        console.error(e);
        toast.error("Failed to create task", { description: String(e) });
      }
    },
    []
  );

  const handleStatus = useCallback(
    async (id: string, status: TaskStatus) => {
      try {
        const u = await tasksApi.update(id, { status });
        setTasks((p) => p.map((t) => t.id === u.id ? u : t));
      } catch (e) { console.error(e); toast.error("Failed to update status", { description: String(e) }); }
    },
    []
  );
  const handlePriority = useCallback(
    async (id: string, priority: TaskPriority) => {
      try {
        const u = await tasksApi.update(id, { priority });
        setTasks((p) => p.map((t) => t.id === u.id ? u : t));
      } catch (e) { console.error(e); toast.error("Failed to update priority", { description: String(e) }); }
    },
    []
  );
  const handleTitle = useCallback(
    async (id: string, title: string) => {
      try {
        const u = await tasksApi.update(id, { title });
        setTasks((p) => p.map((t) => t.id === u.id ? u : t));
      } catch (e) { console.error(e); toast.error("Failed to update title", { description: String(e) }); }
    },
    []
  );

  // ── Render ──

  if (loading) {
    return (
      <div className="flex items-center justify-center h-full">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
    );
  }

  const activeCount = tasks.filter((t) => t.status !== "closed").length;
  const completedCount = tasks.filter((t) => t.status === "closed").length;

  return (
    <div className="flex flex-col h-full">
      {/* ═══════ Top bar – Linear style ═══════ */}
      <div className="flex items-center px-5 h-[46px] border-b border-border/20 shrink-0 gap-4">
        <div className="flex items-center gap-3 min-w-0">
          {/* Tab Selector */}
          <div className="flex items-center gap-1 bg-muted/30 rounded-lg p-0.5">
            <button
              onClick={() => setActiveTab("active")}
              className={cn(
                "px-2.5 py-1 rounded-md text-xs font-medium transition-colors",
                activeTab === "active"
                  ? "bg-background text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              Active
              <span className="ml-1.5 text-[10px] tabular-nums opacity-70">{activeCount}</span>
            </button>
            <button
              onClick={() => setActiveTab("completed")}
              className={cn(
                "px-2.5 py-1 rounded-md text-xs font-medium transition-colors",
                activeTab === "completed"
                  ? "bg-background text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              Closed
              <span className="ml-1.5 text-[10px] tabular-nums opacity-70">{completedCount}</span>
            </button>
          </div>

          {showSearch ? (
            <div className="flex items-center gap-1.5 bg-accent/30 rounded-md px-2.5 py-1 border border-border/20">
              <Search className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
              <input
                ref={searchRef}
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") { setShowSearch(false); setSearchQuery(""); }
                }}
                placeholder="Filter tasks…"
                className="bg-transparent outline-none text-xs w-40 placeholder:text-muted-foreground/60"
              />
            </div>
          ) : (
            <button
              onClick={() => { setShowSearch(true); setTimeout(() => searchRef.current?.focus(), 50); }}
              className="flex items-center gap-1.5 px-2 py-1 rounded-md text-xs text-muted-foreground hover:text-foreground border border-border/30 hover:border-border/50 transition-colors"
            >
              <Search className="h-3 w-3" />
              <span>Filter</span>
            </button>
          )}
        </div>

        <div className="flex-1" />

        <button
          onClick={() => setShowLabelManager(true)}
          className="p-1.5 rounded-md hover:text-foreground hover:bg-accent/30 transition-colors text-muted-foreground"
          title="Manage labels"
        >
          <Tag className="h-4 w-4" />
        </button>

        <button
          onClick={() => setNewIssue({ parentId: null })}
          className="flex items-center gap-1 px-2 py-1 rounded-md text-xs text-muted-foreground hover:text-foreground hover:bg-accent/40 border border-border/30 hover:border-border/50 transition-colors"
          title="New task"
        >
          <Plus className="h-3.5 w-3.5" />
          <span>New</span>
        </button>

        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button className={cn(
              "p-1.5 rounded-md hover:text-foreground hover:bg-accent/30 transition-colors",
              (filterAssigner !== "all" || filterLabels.size > 0) ? "text-primary" : "text-muted-foreground"
            )}>
              <SlidersHorizontal className="h-4 w-4" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-[160px]">
            <div className="px-2 py-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
              Group by
            </div>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onClick={() => setGroupBy("status")}
              className={cn("text-xs cursor-pointer", groupBy === "status" && "font-semibold text-primary")}
            >
              <ListFilter className="h-3.5 w-3.5 mr-2" /> Status
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={() => setGroupBy("priority")}
              className={cn("text-xs cursor-pointer", groupBy === "priority" && "font-semibold text-primary")}
            >
              <ListFilter className="h-3.5 w-3.5 mr-2" /> Priority
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onClick={() => setGroupBy("none")}
              className={cn("text-xs cursor-pointer", groupBy === "none" && "font-semibold text-primary")}
            >
              No grouping
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <div className="px-2 py-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
              Filter
            </div>
            <DropdownMenuItem
              onClick={() => setFilterAssigner((v) => v === "assigned" ? "all" : "assigned")}
              className={cn("text-xs cursor-pointer", filterAssigner === "assigned" && "font-semibold text-primary")}
            >
              <ListFilter className="h-3.5 w-3.5 mr-2" /> Assigned
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={() => setFilterAssigner((v) => v === "unassigned" ? "all" : "unassigned")}
              className={cn("text-xs cursor-pointer", filterAssigner === "unassigned" && "font-semibold text-primary")}
            >
              <ListFilter className="h-3.5 w-3.5 mr-2" /> Unassigned
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={() => setFilterAssigner((v) => v === "mine" ? "all" : "mine")}
              className={cn("text-xs cursor-pointer", filterAssigner === "mine" && "font-semibold text-primary")}
            >
              <ListFilter className="h-3.5 w-3.5 mr-2" /> Assigned to Me
            </DropdownMenuItem>
            {allLabels.length > 0 && (
              <>
                <DropdownMenuSeparator />
                <div className="px-2 py-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
                  Labels
                </div>
                {allLabels.map((name) => {
                  const isActive = filterLabels.has(name);
                  return (
                    <DropdownMenuItem
                      key={name}
                      onClick={() => setFilterLabels((prev) => {
                        const next = new Set(prev);
                        if (isActive) next.delete(name); else next.add(name);
                        return next;
                      })}
                      className={cn("text-xs cursor-pointer gap-2", isActive && "font-semibold text-primary")}
                    >
                      {name}
                    </DropdownMenuItem>
                  );
                })}
              </>
            )}
            {(filterAssigner !== "all" || filterLabels.size > 0) && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  onClick={() => { setFilterAssigner("all"); setFilterLabels(new Set()); }}
                  className="text-xs cursor-pointer text-destructive"
                >
                  Clear all filters
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {/* ═══════ New issue form ═══════ */}
      {newIssue !== undefined && (
        <NewIssueForm
          parentId={newIssue.parentId}
          defaultStatus={newIssue.defaultStatus}
          defaultPriority={newIssue.defaultPriority}
          defaultProjectId={currentProjectId}
          onSubmit={handleCreate}
          onCancel={() => setNewIssue(undefined)}
        />
      )}

      {/* ═══════ Issue list ═══════ */}
      <div className="flex-1 overflow-y-auto">
        {groups.length > 0 ? (
          groups.map((g) => {
            const isCollapsed = collapsedGroups.has(g.key);
            return (
              <GroupSection
                key={g.key}
                group={g}
                collapsed={isCollapsed}
                onToggle={() => toggleGroup(g.key)}
                onAddInGroup={() => handleAddInGroup(g.key)}
                hideHeader={groupBy === "none"}
              >
                {g.items.map((item) => (
                  <IssueRow
                    key={item.task.id}
                    task={item.task}
                    grouped={groupBy !== "none"}
                    onAddChild={!item.task.parent_id ? handleAddChild : undefined}
                    onStatusChange={handleStatus}
                    onPriorityChange={handlePriority}
                    onTitleChange={handleTitle}
                    onPick={onPickTask}
                    onClick={onSelectTask ? (task) => onSelectTask(task.id) : undefined}
                    projectName={item.task.project_id ? projectNameMap.get(item.task.project_id) : undefined}
                  />
                ))}
              </GroupSection>
            );
          })
        ) : (
          <div className="flex flex-col items-center justify-center h-full text-muted-foreground gap-3">
            <div className="w-16 h-16 rounded-full bg-muted/30 flex items-center justify-center">
              <Plus className="h-7 w-7 text-muted-foreground" />
            </div>
            <div className="text-center">
              <p className="text-sm font-medium text-foreground/80">
                {searchQuery ? "No matching tasks" : "No tasks yet"}
              </p>
              <p className="text-xs text-muted-foreground mt-1">
                {searchQuery
                  ? "Try a different search term"
                  : <>Press <kbd className="px-1.5 py-0.5 bg-muted/40 rounded text-[10px] font-mono">C</kbd> to create your first task</>}
              </p>
            </div>
            {!searchQuery && (
              <button
                onClick={() => setNewIssue({ parentId: null })}
                className="text-xs text-primary hover:underline mt-1"
              >
                Create task
              </button>
            )}
          </div>
        )}
        {hasMore && (
          <div className="flex justify-center py-2 border-t">
            <button
              onClick={loadMore}
              disabled={loadingMore}
              className="px-3 py-1 text-xs text-muted-foreground hover:text-foreground hover:bg-accent rounded transition-colors disabled:opacity-50"
            >
              {loadingMore ? "Loading..." : "Load more"}
            </button>
          </div>
        )}
      </div>

      {showLabelManager && currentProjectId && (
        <LabelManager
          projectId={currentProjectId}
          onClose={() => { setShowLabelManager(false); load(); }}
        />
      )}
    </div>
  );
}
