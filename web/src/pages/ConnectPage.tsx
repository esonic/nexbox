import { useState, useEffect, useMemo, useRef } from "react";
import { useNavigate } from "react-router-dom";
import { Zap, FileText, FolderKanban, ListChecks, LayoutList, RefreshCw, TrendingUp, PlusCircle, Pencil, MessageSquare, Trash2, ArrowRight, Bell, X, UserCheck } from "lucide-react";
import { serverApi, changesApi, tasksApi, projectsApi } from "@/lib/api";
import type { CloudInfo, ChangeRecord } from "@/lib/api";
import type { Task } from "@/types";
import { cn } from "@/lib/utils";
import { useLayoutStore } from "@/stores/layout";

const ENTITY_ICONS: Record<string, typeof FileText> = {
  asset: FileText,
  task: ListChecks,
  project: FolderKanban,
  session: LayoutList,
  task_comment: MessageSquare,
  review_comment: MessageSquare,
};

const ACTION_COLORS: Record<string, string> = {
  created: "text-green-500",
  updated: "text-blue-400",
  deleted: "text-red-400",
};

function formatTime(dateStr: string) {
  const d = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - d.getTime();
  const diffMin = Math.floor(diffMs / 60000);
  if (diffMin < 1) return "just now";
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffHr = Math.floor(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h ago`;
  const diffDay = Math.floor(diffHr / 24);
  if (diffDay < 7) return `${diffDay}d ago`;
  return d.toLocaleDateString();
}

function formatDateLabel(dateStr: string) {
  const d = new Date(dateStr);
  const now = new Date();
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const target = new Date(d.getFullYear(), d.getMonth(), d.getDate());
  const diffDays = Math.round((today.getTime() - target.getTime()) / 86400000);
  if (diffDays === 0) return "Today";
  if (diffDays === 1) return "Yesterday";
  return d.toLocaleDateString("en-US", { month: "short", day: "2-digit", year: "numeric" });
}

function groupByDate(changes: ChangeRecord[]): { label: string; items: ChangeRecord[] }[] {
  const groups: { label: string; items: ChangeRecord[] }[] = [];
  let currentLabel = "";
  for (const ch of changes) {
    const label = formatDateLabel(ch.created_at);
    if (label !== currentLabel) {
      currentLabel = label;
      groups.push({ label, items: [] });
    }
    groups[groups.length - 1].items.push(ch);
  }
  return groups;
}

function describeChange(ch: ChangeRecord): { verb: string; entityLabel: string; title: string } {
  const title = ch.summary || "";
  switch (ch.entity_type) {
    case "task_comment":
      if (ch.action === "created") return { verb: "commented on", entityLabel: "task", title };
      if (ch.action === "deleted") return { verb: "deleted comment on", entityLabel: "task", title };
      return { verb: ch.action, entityLabel: "task comment", title };
    case "review_comment":
      if (ch.action === "created") return { verb: "commented on", entityLabel: "asset", title };
      if (ch.action === "deleted") return { verb: "deleted comment on", entityLabel: "asset", title };
      return { verb: ch.action, entityLabel: "review comment", title };
    default:
      return { verb: ch.action, entityLabel: ch.entity_type, title };
  }
}

function DetailBadges({ detail }: { detail: string }) {
  const parts = detail.split(", ");
  return (
    <div className="flex flex-wrap gap-1 mt-0.5">
      {parts.map((part, i) => {
        const arrowMatch = part.match(/^(.+?):\s*(.+?)\s*→\s*(.+)$/);
        if (arrowMatch) {
          const [, field, from, to] = arrowMatch;
          return (
            <span key={i} className="inline-flex items-center gap-1 text-[10px] bg-muted/60 px-1.5 py-0.5 rounded">
              <span className="text-muted-foreground">{field}</span>
              <span className="text-muted-foreground/60 line-through">{from}</span>
              <ArrowRight className="h-2.5 w-2.5 text-muted-foreground/40" />
              <span className="text-foreground/80">{to}</span>
            </span>
          );
        }
        return (
          <span key={i} className="text-[10px] bg-muted/60 text-muted-foreground px-1.5 py-0.5 rounded">
            {part}
          </span>
        );
      })}
    </div>
  );
}

const STATUS_COLORS: Record<string, string> = {
  open: "#60a5fa",
  in_progress: "#fbbf24",
  done: "#34d399",
  closed: "#9ca3af",
};

const STATUS_LABELS: Record<string, string> = {
  open: "Open",
  in_progress: "In Progress",
  done: "Done",
  closed: "Closed",
};

function TaskStatusPie({ tasks }: { tasks: Task[] }) {
  const counts = useMemo(() => {
    const m: Record<string, number> = { open: 0, in_progress: 0, done: 0, closed: 0 };
    for (const t of tasks) m[t.status] = (m[t.status] || 0) + 1;
    return m;
  }, [tasks]);

  const total = tasks.length;
  if (total === 0) {
    return (
      <div className="flex items-center justify-center h-full text-sm text-muted-foreground">
        No tasks
      </div>
    );
  }

  const entries = Object.entries(counts).filter(([, v]) => v > 0);
  const r = 64;
  const cx = 75;
  const cy = 75;
  let cumAngle = -Math.PI / 2;

  const slices = entries.map(([status, count]) => {
    const angle = (count / total) * 2 * Math.PI;
    const startAngle = cumAngle;
    cumAngle += angle;
    const endAngle = cumAngle;

    if (count === total) {
      return (
        <circle key={status} cx={cx} cy={cy} r={r} fill={STATUS_COLORS[status] || "#666"} />
      );
    }

    const x1 = cx + r * Math.cos(startAngle);
    const y1 = cy + r * Math.sin(startAngle);
    const x2 = cx + r * Math.cos(endAngle);
    const y2 = cy + r * Math.sin(endAngle);
    const largeArc = angle > Math.PI ? 1 : 0;

    return (
      <path
        key={status}
        d={`M ${cx} ${cy} L ${x1} ${y1} A ${r} ${r} 0 ${largeArc} 1 ${x2} ${y2} Z`}
        fill={STATUS_COLORS[status] || "#666"}
      />
    );
  });

  return (
    <div className="flex items-center gap-5">
      <svg width={150} height={150} viewBox="0 0 150 150">
        {slices}
        <circle cx={cx} cy={cy} r={36} className="fill-card" />
        <text x={cx} y={cy - 4} textAnchor="middle" className="fill-foreground text-[20px] font-bold">{total}</text>
        <text x={cx} y={cy + 12} textAnchor="middle" className="fill-muted-foreground text-[10px]">tasks</text>
      </svg>
      <div className="flex flex-col gap-2">
        {entries.map(([status, count]) => (
          <div key={status} className="flex items-center gap-2.5 text-sm">
            <span className="w-3 h-3 rounded-full shrink-0" style={{ backgroundColor: STATUS_COLORS[status] }} />
            <span className="text-muted-foreground">{STATUS_LABELS[status] || status}</span>
            <span className="font-semibold ml-auto tabular-nums">{count}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

interface DashboardStats {
  newTasks: number;
  updatedTasks: number;
  newAssets: number;
  updatedAssets: number;
  newComments: number;
}

function computeStats(changes: ChangeRecord[]): DashboardStats {
  const sevenDaysAgo = new Date();
  sevenDaysAgo.setDate(sevenDaysAgo.getDate() - 7);
  const recent = changes.filter((c) => new Date(c.created_at) >= sevenDaysAgo);

  return {
    newTasks: recent.filter((c) => c.entity_type === "task" && c.action === "created").length,
    updatedTasks: recent.filter((c) => c.entity_type === "task" && c.action === "updated").length,
    newAssets: recent.filter((c) => c.entity_type === "asset" && c.action === "created").length,
    updatedAssets: recent.filter((c) => c.entity_type === "asset" && c.action === "updated").length,
    newComments: recent.filter((c) => (c.entity_type === "task_comment" || c.entity_type === "review_comment") && c.action === "created").length,
  };
}

function ChangeRow({ ch, projectName }: { ch: ChangeRecord; projectName?: string }) {
  const Icon = ENTITY_ICONS[ch.entity_type] || FileText;
  const actionColor = ACTION_COLORS[ch.action] || "text-muted-foreground";
  const { verb, entityLabel, title } = describeChange(ch);
  const isComment = ch.entity_type === "task_comment" || ch.entity_type === "review_comment";
  const hasDetail = !!ch.detail;

  return (
    <div className="flex gap-3 px-4 py-2.5 text-sm">
      <div className="pt-0.5 shrink-0">
        {ch.action === "deleted" ? (
          <Trash2 className="h-4 w-4 text-red-400/60" />
        ) : (
          <Icon className="h-4 w-4 text-muted-foreground" />
        )}
      </div>
      <div className="flex-1 min-w-0">
        <div className="flex items-baseline gap-1.5 flex-wrap">
          <span className="font-medium text-foreground/90 text-[12px]">{ch.user_name}</span>
          <span className={cn("text-xs", actionColor)}>{verb}</span>
          <span className="text-xs text-muted-foreground">{entityLabel}</span>
          {title && (
            <span className="text-xs font-medium text-foreground truncate max-w-[250px]" title={title}>
              {title}
            </span>
          )}
          {projectName && ch.entity_type !== "project" && (
            <span className="text-[10px] text-violet-500/80 bg-violet-500/10 px-1.5 py-0.5 rounded font-medium">{projectName}</span>
          )}
        </div>
        {isComment && ch.detail && (
          <div className="mt-1 text-[11px] text-muted-foreground/80 bg-muted/40 rounded px-2 py-1 border-l-2 border-muted-foreground/20 line-clamp-2">
            {ch.detail}
          </div>
        )}
        {!isComment && hasDetail && <DetailBadges detail={ch.detail!} />}
      </div>
      <span className="text-[11px] text-muted-foreground/50 shrink-0 tabular-nums pt-0.5">
        {formatTime(ch.created_at)}
      </span>
    </div>
  );
}

export function ConnectPage() {
  const navigate = useNavigate();
  const { setRightPanel } = useLayoutStore();
  const [info, setInfo] = useState<CloudInfo | null>(null);
  const [loading, setLoading] = useState(true);
  const [changes, setChanges] = useState<ChangeRecord[]>([]);
  const [changesLoading, setChangesLoading] = useState(false);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [projectMap, setProjectMap] = useState<Map<string, string>>(new Map());
  const [lastReadId, setLastReadId] = useState<number>(
    () => parseInt(localStorage.getItem("notification_last_read_id") || "0")
  );
  const [dismissedIds, setDismissedIds] = useState<Set<number>>(
    () => new Set(JSON.parse(localStorage.getItem("notification_dismissed_ids") || "[]"))
  );
  const initialLastReadIdRef = useRef(parseInt(localStorage.getItem("notification_last_read_id") || "0"));
  const autoMarkedRef = useRef(false);

  useEffect(() => {
    serverApi.info()
      .then((data) => setInfo(data as CloudInfo))
      .catch(() => {})
      .finally(() => setLoading(false));
    tasksApi.list(200, 0).then((t) => setTasks(t || [])).catch(() => {});
    projectsApi.list().then((ps) => {
      setProjectMap(new Map((ps || []).map((p) => [p.id, p.name])));
    }).catch(() => {});
  }, []);

  const isCloud = info?.mode === "cloud";

  useEffect(() => {
    if (!isCloud) return;
    setChangesLoading(true);
    changesApi.list(100, 0, "desc")
      .then(setChanges)
      .catch(() => {})
      .finally(() => setChangesLoading(false));
  }, [isCloud]);

  const refreshChanges = () => {
    setChangesLoading(true);
    changesApi.list(100, 0, "desc")
      .then(setChanges)
      .catch(() => {})
      .finally(() => setChangesLoading(false));
  };

  const activityChanges = useMemo(() => changes.filter((c) => c.entity_type !== "notification"), [changes]);
  const grouped = useMemo(() => groupByDate(activityChanges), [activityChanges]);
  const stats = useMemo(() => computeStats(activityChanges), [activityChanges]);

  const myNotifications = useMemo(() => {
    const me = info?.user_name;
    if (!me) return [];
    return changes.filter(
      (c) => c.entity_type === "notification" &&
             c.recipient?.split(",").includes(me) &&
             !dismissedIds.has(c.id)
    );
  }, [changes, info, dismissedIds]);

  const hasUnread = myNotifications.some((n) => n.id > lastReadId);

  const openNotification = (n: ChangeRecord) => {
    if (n.task_id) {
      setRightPanel("task");
      const params = new URLSearchParams({ open_task: n.task_id });
      if (n.comment_id) params.set("comment_id", n.comment_id);
      navigate(`/?${params.toString()}`);
    } else if (n.asset_id) {
      setRightPanel("asset");
      const params = new URLSearchParams({ open_asset: n.asset_id });
      if (n.line_number != null) params.set("line", String(n.line_number));
      if (n.comment_id) params.set("comment_id", n.comment_id);
      navigate(`/?${params.toString()}`);
    }
  };

  const dismissNotification = (id: number, e: React.MouseEvent) => {
    e.stopPropagation();
    const next = new Set(dismissedIds);
    next.add(id);
    setDismissedIds(next);
    localStorage.setItem("notification_dismissed_ids", JSON.stringify([...next]));
  };

  // Auto-mark all notifications as read when the page is viewed
  useEffect(() => {
    if (!isCloud || myNotifications.length === 0 || autoMarkedRef.current) return;
    autoMarkedRef.current = true;
    const maxId = Math.max(...myNotifications.map((n) => n.id));
    if (maxId <= initialLastReadIdRef.current) return;
    localStorage.setItem("notification_last_read_id", String(maxId));
    setLastReadId(maxId);
    window.dispatchEvent(new CustomEvent("notification-read"));
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isCloud, myNotifications]);

  return (
    <div className="h-full overflow-y-auto">
      <div className="max-w-3xl mx-auto px-6 py-8">
        {/* Header */}
        <div className="flex items-center gap-3 mb-6">
          <div className="p-2 rounded-lg bg-muted">
            <Zap className="h-5 w-5 text-muted-foreground" />
          </div>
          <div className="flex-1">
            <h1 className="text-lg font-semibold">Dashboard</h1>
            <p className="text-sm text-muted-foreground">Project overview & activity</p>
          </div>
          <span className={cn(
            "px-3 py-1.5 rounded-md text-xs font-medium",
            isCloud
              ? "bg-green-500/10 text-green-500"
              : "bg-muted text-muted-foreground"
          )}>
            {isCloud ? "Cloud" : "Local"}
          </span>
        </div>

        {loading ? (
          <div className="rounded-lg border bg-card px-4 py-8 text-center text-sm text-muted-foreground">
            Loading...
          </div>
        ) : (
          <div className="space-y-5">
            {/* Connection info */}
            <div className="rounded-lg border bg-card p-4">
              <div className="grid grid-cols-[120px_1fr] gap-y-3 text-sm">
                {isCloud ? (
                  <>
                    <span className="text-muted-foreground">User</span>
                    <span className="font-medium">{info?.user_name || "-"}</span>
                  </>
                ) : (
                  <>
                    <span className="text-muted-foreground">Storage</span>
                    <span className="font-medium">SQLite (local)</span>
                  </>
                )}
              </div>
            </div>

            {!isCloud && (
              <p className="text-xs text-muted-foreground/60 px-1">
                Running in local mode. Configure <code className="bg-muted px-1 py-0.5 rounded text-[10px]">config.json</code> with <code className="bg-muted px-1 py-0.5 rounded text-[10px]">token</code> to enable cloud mode.
              </p>
            )}

            {/* Dashboard cards */}
            <div className="grid grid-cols-2 gap-4">
              {/* Task status pie */}
              <div className="rounded-lg border bg-card p-4 col-span-2 sm:col-span-1">
                <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3">Task Status</h3>
                <TaskStatusPie tasks={tasks} />
              </div>

              {/* 7-day stats */}
              <div className="rounded-lg border bg-card p-4 col-span-2 sm:col-span-1">
                <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3">
                  <TrendingUp className="h-3.5 w-3.5 inline mr-1.5 -mt-0.5" />
                  Last 7 Days
                </h3>
                <div className="grid grid-cols-2 gap-3">
                  <div className="rounded-md bg-green-500/5 border border-green-500/10 px-3 py-2.5">
                    <div className="flex items-center gap-1.5 text-green-500 mb-1">
                      <PlusCircle className="h-3.5 w-3.5" />
                      <span className="text-[10px] font-medium uppercase">New Tasks</span>
                    </div>
                    <span className="text-xl font-bold tabular-nums">{stats.newTasks}</span>
                  </div>
                  <div className="rounded-md bg-blue-500/5 border border-blue-500/10 px-3 py-2.5">
                    <div className="flex items-center gap-1.5 text-blue-400 mb-1">
                      <Pencil className="h-3.5 w-3.5" />
                      <span className="text-[10px] font-medium uppercase">Updated Tasks</span>
                    </div>
                    <span className="text-xl font-bold tabular-nums">{stats.updatedTasks}</span>
                  </div>
                  <div className="rounded-md bg-green-500/5 border border-green-500/10 px-3 py-2.5">
                    <div className="flex items-center gap-1.5 text-green-500 mb-1">
                      <PlusCircle className="h-3.5 w-3.5" />
                      <span className="text-[10px] font-medium uppercase">New Assets</span>
                    </div>
                    <span className="text-xl font-bold tabular-nums">{stats.newAssets}</span>
                  </div>
                  <div className="rounded-md bg-purple-500/5 border border-purple-500/10 px-3 py-2.5">
                    <div className="flex items-center gap-1.5 text-purple-400 mb-1">
                      <MessageSquare className="h-3.5 w-3.5" />
                      <span className="text-[10px] font-medium uppercase">Comments</span>
                    </div>
                    <span className="text-xl font-bold tabular-nums">{stats.newComments}</span>
                  </div>
                </div>
              </div>
            </div>

            {/* Notifications */}
            {isCloud && (
              <div className="rounded-lg border bg-card">
                <div className="flex items-center justify-between px-4 py-3 border-b">
                  <div className="flex items-center gap-2">
                    <Bell className="h-4 w-4 text-muted-foreground" />
                    <h2 className="text-sm font-medium">Notifications</h2>
                    {hasUnread && <span className="h-2 w-2 rounded-full bg-red-500 shrink-0" />}
                  </div>
                </div>
                {myNotifications.length === 0 ? (
                  <div className="px-4 py-8 text-center text-sm text-muted-foreground">No notifications yet.</div>
                ) : (
                  <div className="divide-y divide-border/50">
                    {myNotifications.map((n) => {
                      const isNew = n.id > initialLastReadIdRef.current;
                      const clickable = !!(n.task_id || n.asset_id);
                      const verb = n.comment_id
                        ? (n.asset_id ? "commented on asset" : "commented on task")
                        : (n.asset_id ? "mentioned you in asset" : "assigned you to task");
                      return (
                        <div
                          key={n.id}
                          className={cn(
                            "group relative flex items-center gap-3 px-4 py-2.5",
                            isNew && "border-l-2 border-red-500 notification-new",
                            clickable && "cursor-pointer hover:bg-accent/40 transition-colors"
                          )}
                          onClick={clickable ? () => openNotification(n) : undefined}
                        >
                          <div className="pt-0.5 shrink-0">
                            {n.comment_id
                              ? <MessageSquare className="h-4 w-4 text-muted-foreground" />
                              : n.task_id
                                ? <UserCheck className="h-4 w-4 text-muted-foreground" />
                                : <Bell className="h-4 w-4 text-muted-foreground" />
                            }
                          </div>
                          <div className="flex-1 min-w-0">
                            <div className="flex items-baseline gap-1.5 text-xs flex-wrap">
                              <span className="font-medium text-foreground/90">{n.user_name}</span>
                              <span className={cn(
                                n.comment_id ? "text-green-500" : n.task_id ? "text-blue-400" : "text-muted-foreground"
                              )}>{verb}</span>
                              {n.summary && <span className="font-medium text-foreground truncate max-w-[200px]" title={n.summary}>{n.summary}</span>}
                              {n.project_id && projectMap.get(n.project_id) && (
                                <span className="text-[10px] text-violet-500/80 bg-violet-500/10 px-1.5 py-0.5 rounded font-medium">{projectMap.get(n.project_id)}</span>
                              )}
                            </div>
                            {n.comment_preview && (
                              <div className="text-[11px] text-muted-foreground/50 mt-1 line-clamp-2 bg-muted/40 rounded px-1.5 py-0.5 border-l border-muted-foreground/20">{n.comment_preview}</div>
                            )}
                          </div>
                          <span className="text-[11px] text-muted-foreground/40 shrink-0 tabular-nums">{formatTime(n.created_at)}</span>
                          <button
                            onClick={(e) => dismissNotification(n.id, e)}
                            className="opacity-0 group-hover:opacity-100 p-0.5 rounded text-muted-foreground/40 hover:text-destructive hover:bg-destructive/10 transition-all shrink-0"
                            title="Dismiss"
                          >
                            <X className="h-3.5 w-3.5" />
                          </button>
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>
            )}

            {/* Recent activity */}
            {isCloud && (
              <div className="rounded-lg border bg-card">
                <div className="flex items-center justify-between px-4 py-3 border-b">
                  <h2 className="text-sm font-medium">Recent Activity</h2>
                  <button
                    onClick={refreshChanges}
                    disabled={changesLoading}
                    className="p-1 rounded hover:bg-muted transition-colors text-muted-foreground hover:text-foreground disabled:opacity-50"
                  >
                    <RefreshCw className={cn("h-3.5 w-3.5", changesLoading && "animate-spin")} />
                  </button>
                </div>

                {changesLoading && activityChanges.length === 0 ? (
                  <div className="px-4 py-8 text-center text-sm text-muted-foreground">
                    Loading changes...
                  </div>
                ) : activityChanges.length === 0 ? (
                  <div className="px-4 py-8 text-center text-sm text-muted-foreground">
                    No recent changes.
                  </div>
                ) : (
                  <div>
                    {grouped.map((group) => (
                      <div key={group.label}>
                        <div className="px-4 py-2 text-xs font-semibold text-muted-foreground bg-muted/30 border-b">
                          {group.label}
                        </div>
                        <div className="divide-y divide-border/50">
                          {group.items.map((ch) => (
                            <ChangeRow key={ch.id} ch={ch} projectName={ch.project_id ? projectMap.get(ch.project_id) : undefined} />
                          ))}
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
