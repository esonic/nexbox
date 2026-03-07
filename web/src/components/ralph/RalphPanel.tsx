import { useState, useEffect, useCallback } from "react";
import {
  Play,
  Square,
  Loader2,
  CheckCircle2,
  XCircle,
  ExternalLink,
  ChevronDown,
  ChevronRight,
} from "lucide-react";
import { toast } from "sonner";
import { ralphApi, workflowApi } from "@/lib/workflowApi";
import type { RalphRun, RalphWorkerLog, Workflow } from "@/lib/workflowApi";
import { tasksApi } from "@/lib/api";
import { globalWS } from "@/lib/ws";
import { cn } from "@/lib/utils";

interface LiveWorker {
  index: number;
  sessionId?: string;
  currentTask: { id: string; title: string } | null;
  completed: number;
  history: { taskId: string; title: string; status: "completed" | "failed" }[];
}

interface RalphPanelProps {
  projectId: string;
}

export function RalphPanel({ projectId }: RalphPanelProps) {
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [selectedWorkflowId, setSelectedWorkflowId] = useState("");
  const [workerCount, setWorkerCount] = useState(3);
  const [openTaskCount, setOpenTaskCount] = useState(0);
  const [starting, setStarting] = useState(false);

  const [activeRunId, setActiveRunId] = useState<string | null>(null);
  const [liveWorkers, setLiveWorkers] = useState<LiveWorker[]>([]);
  const [liveStats, setLiveStats] = useState({ completed: 0, failed: 0, total: 0 });
  const [running, setRunning] = useState(false);

  const [runs, setRuns] = useState<RalphRun[]>([]);
  const [expandedRunId, setExpandedRunId] = useState<string | null>(null);
  const [runLogs, setRunLogs] = useState<Record<string, RalphWorkerLog[]>>({});
  const [loading, setLoading] = useState(true);

  const loadData = useCallback(async () => {
    const [wfs, tasks, history] = await Promise.all([
      workflowApi.list(projectId).catch(() => []),
      tasksApi.list(200, 0, projectId).catch(() => []),
      ralphApi.listRuns(projectId).catch(() => []),
    ]);
    setWorkflows(wfs || []);
    setOpenTaskCount((tasks || []).filter((t: any) => t.status === "open" && (t.labels || []).includes("ralph")).length);
    setRuns(history || []);
    setLoading(false);

    const activeRun = (history || []).find((r: RalphRun) => r.status === "running");
    if (activeRun) {
      setActiveRunId(activeRun.id);
      setRunning(true);
      setLiveStats({ completed: activeRun.total_completed, failed: activeRun.total_failed, total: activeRun.total_tasks });
    }
  }, [projectId]);

  useEffect(() => { loadData(); }, [loadData]);

  useEffect(() => {
    if (!activeRunId) return;
    return globalWS.subscribeGlobal((event: any) => {
      if (event.type !== "ralph_progress" || event.ralph_id !== activeRunId) return;
      const ev = event.event as string;
      const workerIdx = event.worker as number | undefined;

      if (ev === "worker_picked" && workerIdx !== undefined) {
        setLiveWorkers((prev) => {
          const next = [...prev];
          while (next.length <= workerIdx) next.push({ index: next.length, completed: 0, currentTask: null, history: [] });
          next[workerIdx] = { ...next[workerIdx], currentTask: { id: event.task_id, title: event.task_title }, sessionId: event.session_id };
          return next;
        });
      } else if (ev === "worker_completed" && workerIdx !== undefined) {
        setLiveWorkers((prev) => {
          const next = [...prev];
          if (next[workerIdx]) {
            const cur = next[workerIdx].currentTask;
            next[workerIdx] = { ...next[workerIdx], currentTask: null, completed: next[workerIdx].completed + 1, history: cur ? [...next[workerIdx].history, { taskId: cur.id, title: cur.title, status: "completed" }] : next[workerIdx].history };
          }
          return next;
        });
        setLiveStats((prev) => ({ ...prev, completed: prev.completed + 1 }));
      } else if (ev === "worker_failed" && workerIdx !== undefined) {
        setLiveWorkers((prev) => {
          const next = [...prev];
          if (next[workerIdx]) {
            const cur = next[workerIdx].currentTask;
            next[workerIdx] = { ...next[workerIdx], currentTask: null, history: cur ? [...next[workerIdx].history, { taskId: cur.id, title: cur.title, status: "failed" }] : next[workerIdx].history };
          }
          return next;
        });
        setLiveStats((prev) => ({ ...prev, failed: prev.failed + 1 }));
      } else if (ev === "pool_done") {
        setRunning(false);
        setActiveRunId(null);
        toast.success("RALPH completed");
        loadData();
      }
    });
  }, [activeRunId, loadData]);

  useEffect(() => {
    return globalWS.subscribeGlobal((event: any) => {
      if (event.type === "change" && event.entity_type === "task") {
        tasksApi.list(200, 0, projectId).then((tasks) => {
          setOpenTaskCount((tasks || []).filter((t: any) => t.status === "open" && (t.labels || []).includes("ralph")).length);
        }).catch(() => {});
      }
    });
  }, [projectId]);

  const handleStart = async () => {
    setStarting(true);
    try {
      const res = await ralphApi.start(projectId, selectedWorkflowId, workerCount);
      setActiveRunId(res.id);
      setRunning(true);
      setLiveWorkers([]);
      setLiveStats({ completed: 0, failed: 0, total: res.total_tasks });
      toast.success(`RALPH started with ${res.workers} workers`);
    } catch (e: any) {
      toast.error("Failed to start: " + e.message);
    } finally {
      setStarting(false);
    }
  };

  const handleStop = async () => {
    if (!activeRunId) return;
    try { await ralphApi.stop(activeRunId); toast("RALPH stopped"); } catch { /* already finished */ }
    setRunning(false);
    setActiveRunId(null);
    loadData();
  };

  const toggleRunExpand = async (runId: string) => {
    if (expandedRunId === runId) { setExpandedRunId(null); return; }
    setExpandedRunId(runId);
    if (!runLogs[runId]) {
      try {
        const data = await ralphApi.getRun(runId);
        setRunLogs((prev) => ({ ...prev, [runId]: data.worker_logs || [] }));
      } catch { /* ignore */ }
    }
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center h-full">
        <Loader2 className="w-6 h-6 animate-spin text-muted-foreground" />
      </div>
    );
  }

  const progressPct = liveStats.total > 0 ? Math.round(((liveStats.completed + liveStats.failed) / liveStats.total) * 100) : 0;

  return (
    <div className="flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center px-5 h-[46px] border-b border-border/20 shrink-0">
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground tabular-nums">
          <span className="px-1.5 py-0.5 rounded bg-violet-500/10 text-violet-600 dark:text-violet-400 text-[10px] font-medium">ralph</span>
          {openTaskCount} task{openTaskCount !== 1 ? "s" : ""}
        </div>
        <div className="flex-1" />
        {running ? (
          <button
            onClick={handleStop}
            className="flex items-center gap-1 px-2 py-1 rounded-md text-xs font-medium bg-red-600 text-white hover:bg-red-700 transition-colors"
          >
            <Square className="h-3 w-3" />
            Stop
          </button>
        ) : (
          <button
            onClick={handleStart}
            disabled={starting || openTaskCount === 0}
            className="flex items-center gap-1 px-2 py-1 rounded-md text-xs text-muted-foreground hover:text-foreground hover:bg-accent/40 border border-border/30 hover:border-border/50 disabled:opacity-50 transition-colors"
          >
            {starting ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Play className="h-3.5 w-3.5" />}
            Start
          </button>
        )}
      </div>

      <div className="flex-1 overflow-auto">
        {/* ── Config ── */}
        {!running && (
          <div className="p-3 space-y-2.5 border-b border-border/20">
            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">Workflow (optional)</label>
              <select
                value={selectedWorkflowId}
                onChange={(e) => setSelectedWorkflowId(e.target.value)}
                className="w-full rounded-md border bg-background px-2.5 py-1.5 text-xs"
              >
                <option value="">Default prompt</option>
                {workflows.map((w) => (
                  <option key={w.id} value={w.id}>{w.name}</option>
                ))}
              </select>
            </div>
            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">Workers</label>
              <input
                type="number"
                min={1}
                max={16}
                value={workerCount}
                onChange={(e) => setWorkerCount(Math.max(1, Math.min(16, parseInt(e.target.value) || 1)))}
                className="w-20 rounded-md border bg-background px-2.5 py-1.5 text-xs"
              />
            </div>
          </div>
        )}

        {/* ── Live Monitor ── */}
        {running && (
          <div className="border-b border-border/20">
            <div className="flex items-center gap-2 px-3 py-2">
              <Loader2 className="w-3 h-3 text-violet-500 animate-spin shrink-0" />
              <span className="text-[11px] font-medium">Running</span>
              <span className="text-[10px] text-muted-foreground">{liveWorkers.length}w</span>
              <div className="flex-1" />
              <span className="text-[10px] tabular-nums text-muted-foreground">
                {liveStats.completed + liveStats.failed}/{liveStats.total} ({progressPct}%)
              </span>
            </div>
            <div className="h-1 bg-muted">
              <div className="h-full bg-violet-500 transition-all duration-500" style={{ width: `${progressPct}%` }} />
            </div>
            <div className="px-3 py-2 space-y-1">
              {liveWorkers.map((w) => (
                <div key={w.index} className="flex items-center gap-1.5 min-w-0">
                  <span className="text-[10px] text-muted-foreground font-mono w-5 shrink-0 text-right">W{w.index + 1}</span>
                  <div className="flex items-center gap-0.5 flex-1 overflow-x-auto min-w-0">
                    {w.history.slice(-3).map((h, i) => (
                      <TaskChip key={`${h.taskId}-${i}`} title={h.title} status={h.status} />
                    ))}
                    {w.currentTask && <TaskChip title={w.currentTask.title} status="running" />}
                    {!w.currentTask && w.history.length === 0 && (
                      <span className="text-[10px] text-muted-foreground/50 italic">idle</span>
                    )}
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}

        {/* ── History ── */}
        <div className="p-2 space-y-1">
          {runs.length === 0 && !running && (
            <div className="text-xs text-muted-foreground text-center py-6">No runs yet</div>
          )}
          {runs.map((run) => {
            const isExpanded = expandedRunId === run.id;
            const isActive = run.id === activeRunId;
            return (
              <div key={run.id} className={cn("rounded-md border border-border/30 overflow-hidden", isActive && "border-violet-500/40")}>
                <button
                  onClick={() => toggleRunExpand(run.id)}
                  className="w-full flex items-center gap-1.5 px-2.5 py-1.5 text-left hover:bg-accent/20 transition-colors"
                >
                  {isExpanded ? <ChevronDown className="w-3 h-3 text-muted-foreground/50 shrink-0" /> : <ChevronRight className="w-3 h-3 text-muted-foreground/50 shrink-0" />}
                  <StatusDot status={run.status} />
                  <span className="text-[10px] text-muted-foreground">{run.workers}w</span>
                  <span className="text-[10px]">
                    <span className="text-emerald-500">{run.total_completed}</span>
                    {run.total_failed > 0 && <span className="text-red-500">/{run.total_failed}</span>}
                    <span className="text-muted-foreground">/{run.total_tasks}</span>
                  </span>
                  <div className="flex-1" />
                  <span className="text-[9px] text-muted-foreground/60 tabular-nums">{formatTime(run.created_at)}</span>
                </button>
                {isExpanded && (
                  <div className="border-t border-border/20 px-2.5 py-1.5 space-y-0.5">
                    {runLogs[run.id] && runLogs[run.id].length === 0 && (
                      <p className="text-[10px] text-muted-foreground text-center py-1">No logs</p>
                    )}
                    {runLogs[run.id]?.map((log) => (
                      <div key={log.id} className="flex items-center gap-1.5 text-[10px]">
                        <span className="text-muted-foreground font-mono w-5 text-right shrink-0">W{log.worker_index + 1}</span>
                        {log.status === "completed" ? <CheckCircle2 className="w-2.5 h-2.5 text-emerald-500 shrink-0" /> : log.status === "failed" ? <XCircle className="w-2.5 h-2.5 text-red-500 shrink-0" /> : <Loader2 className="w-2.5 h-2.5 text-blue-500 animate-spin shrink-0" />}
                        <span className="truncate flex-1">{log.task_title || log.task_id}</span>
                      </div>
                    ))}
                    {!runLogs[run.id] && <Loader2 className="w-3 h-3 animate-spin text-muted-foreground mx-auto" />}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

function TaskChip({ title, status }: { title: string; status: "completed" | "failed" | "running" }) {
  const colors = {
    completed: "bg-emerald-500/10 border-emerald-500/30 text-emerald-600 dark:text-emerald-400",
    failed: "bg-red-500/10 border-red-500/30 text-red-600 dark:text-red-400",
    running: "bg-violet-500/10 border-violet-500/30 text-violet-600 dark:text-violet-400",
  };
  return (
    <div className={`flex items-center gap-0.5 px-1 py-px rounded text-[9px] border shrink-0 ${colors[status]}`}>
      {status === "completed" && <CheckCircle2 className="w-2 h-2" />}
      {status === "failed" && <XCircle className="w-2 h-2" />}
      {status === "running" && <Loader2 className="w-2 h-2 animate-spin" />}
      <span className="whitespace-nowrap max-w-[80px] truncate">{title}</span>
    </div>
  );
}

function StatusDot({ status }: { status: string }) {
  if (status === "running") return <span className="w-1.5 h-1.5 rounded-full bg-violet-500 animate-pulse shrink-0" />;
  if (status === "completed") return <span className="w-1.5 h-1.5 rounded-full bg-emerald-500 shrink-0" />;
  return <span className="w-1.5 h-1.5 rounded-full bg-orange-500 shrink-0" />;
}

function formatTime(dateStr: string) {
  try {
    const d = new Date(dateStr);
    return d.toLocaleString("en-US", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
  } catch { return dateStr; }
}
