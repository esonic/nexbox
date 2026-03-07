import { useState, useEffect, useCallback } from 'react';
import ReactFlow, {
  Background,
  Controls,
  Node,
  Edge,
  useNodesState,
  useEdgesState,
  ReactFlowProvider,
} from 'reactflow';
import 'reactflow/dist/style.css';
import {
  Play, Square, Loader2, CheckCircle2, XCircle, Clock, Timer, ArrowRight,
  ChevronDown, ChevronRight, User, Pencil,
} from 'lucide-react';
import { toast } from 'sonner';

import { nodeTypes } from './WorkflowEditor';
import { workflowApi, Workflow, WorkflowExecution, WorkflowNodeLog, WorkflowSchedule } from '@/lib/workflowApi';
import { globalWS } from '@/lib/ws';

interface WorkflowRunsViewProps {
  workflow: Workflow;
  projectId: string;
  onWorkflowChange?: (wf: Workflow) => void;
  onEdit?: () => void;
}

// ----- status helpers -----
function statusIcon(status: string, small = false) {
  const cls = small ? 'w-3 h-3' : 'w-3.5 h-3.5';
  if (status === 'running' || status === 'pending')
    return <Loader2 className={`${cls} text-blue-500 animate-spin`} />;
  if (status === 'completed')
    return <CheckCircle2 className={`${cls} text-emerald-500`} />;
  if (status === 'failed')
    return <XCircle className={`${cls} text-red-500`} />;
  if (status === 'waiting')
    return <User className={`${cls} text-amber-500`} />;
  if (status === 'cancelled')
    return <XCircle className={`${cls} text-muted-foreground`} />;
  return <Clock className={`${cls} text-muted-foreground/50`} />;
}

function statusColor(status: string) {
  if (status === 'running' || status === 'pending') return 'text-blue-500';
  if (status === 'completed') return 'text-emerald-500';
  if (status === 'failed') return 'text-red-500';
  if (status === 'waiting') return 'text-amber-500';
  if (status === 'cancelled') return 'text-muted-foreground';
  return 'text-muted-foreground/50';
}

function fmtDuration(ms?: number) {
  if (!ms) return '';
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`;
  return `${Math.round(ms / 60000)}m`;
}

// ----- Schedule display/editor -----
function scheduleLabel(schedule?: string): string {
  if (!schedule) return 'No schedule';
  try {
    const s: WorkflowSchedule = JSON.parse(schedule);
    if (s.type === 'interval') return `Every ${s.interval_hours}h`;
    if (s.type === 'weekly') {
      const days = (s.days || []).map((d: number) => ['Su', 'Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa'][d]).join(' ');
      return `Weekly: ${days} at ${String(s.hour ?? 9).padStart(2, '0')}:${String(s.minute ?? 0).padStart(2, '0')}`;
    }
  } catch {}
  return 'No schedule';
}

function ScheduleSection({ workflow, onSaved }: { workflow: Workflow; onSaved: (wf: Workflow) => void }) {
  const [open, setOpen] = useState(false);
  const [schedule, setSchedule] = useState<WorkflowSchedule | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    try { setSchedule(workflow.schedule ? JSON.parse(workflow.schedule) : null); } catch { setSchedule(null); }
  }, [workflow.schedule]);

  const save = async () => {
    setSaving(true);
    try {
      const updated = await workflowApi.update(workflow.id, { schedule: schedule ? JSON.stringify(schedule) : '' });
      onSaved(updated);
      setOpen(false);
      toast.success('Schedule saved');
    } catch (err: any) {
      toast.error('Failed to save schedule: ' + err.message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div>
      {/* Row: icon + label + edit button */}
      <div className="flex items-center gap-1.5">
        <Timer className="w-3 h-3 text-muted-foreground/50 shrink-0" />
        <span className="text-[11px] text-muted-foreground flex-1 truncate">{scheduleLabel(workflow.schedule)}</span>
        <button
          onClick={() => setOpen(!open)}
          className={`shrink-0 p-0.5 rounded transition-colors ${open ? 'text-primary bg-primary/10' : 'text-muted-foreground/50 hover:text-foreground hover:bg-accent/60'}`}
          title="Edit schedule"
        >
          <Pencil className="w-3 h-3" />
        </button>
      </div>

      {/* Inline editor — shown below the row when open */}
      {open && (
        <div className="mt-2 rounded-md border border-border/30 bg-muted/20 p-2 space-y-2">
          {/* Type selector */}
          <div className="flex gap-1">
            {(['none', 'interval', 'weekly'] as const).map((t) => (
              <button
                key={t}
                onClick={() => setSchedule(t === 'none' ? null : t === 'interval'
                  ? { type: 'interval', interval_hours: 4 }
                  : { type: 'weekly', days: [1], hour: 9, minute: 0 })}
                className={`px-2 py-0.5 rounded text-[10px] border transition-colors ${
                  (t === 'none' ? !schedule : schedule?.type === t)
                    ? 'bg-primary/10 border-primary/40 text-primary'
                    : 'border-border/30 text-muted-foreground hover:bg-accent/40'
                }`}
              >
                {t === 'none' ? 'None' : t === 'interval' ? 'Interval' : 'Weekly'}
              </button>
            ))}
          </div>

          {/* Interval config */}
          {schedule?.type === 'interval' && (
            <div className="flex items-center gap-1.5">
              <span className="text-[10px] text-muted-foreground">Every</span>
              <input
                type="number"
                value={schedule.interval_hours ?? 4}
                min={1} max={24} step={1}
                onChange={(e) => setSchedule({ ...schedule, interval_hours: Math.min(24, Math.max(1, Math.round(Number(e.target.value)))) })}
                className="field-input w-12 text-center text-[11px]"
              />
              <span className="text-[10px] text-muted-foreground">hours</span>
            </div>
          )}

          {/* Weekly config */}
          {schedule?.type === 'weekly' && (
            <div className="space-y-1.5">
              <div className="flex gap-0.5">
                {['Su', 'Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa'].map((d, i) => (
                  <button
                    key={i}
                    onClick={() => {
                      const days = schedule.days || [];
                      setSchedule({ ...schedule, days: days.includes(i) ? days.filter((x) => x !== i) : [...days, i].sort() });
                    }}
                    className={`w-6 h-5 rounded text-[9px] font-medium border transition-colors ${
                      (schedule.days || []).includes(i)
                        ? 'bg-primary text-primary-foreground border-primary'
                        : 'border-border/40 text-muted-foreground hover:bg-accent/40'
                    }`}
                  >
                    {d}
                  </button>
                ))}
              </div>
              <div className="flex items-center gap-1">
                <input
                  type="number"
                  value={schedule.hour ?? 9}
                  min={0} max={23}
                  onChange={(e) => setSchedule({ ...schedule, hour: Math.min(23, Math.max(0, Number(e.target.value))) })}
                  className="field-input w-10 text-center text-[11px]"
                />
                <span className="text-muted-foreground text-[10px]">:</span>
                <input
                  type="number"
                  value={schedule.minute ?? 0}
                  min={0} max={59}
                  onChange={(e) => setSchedule({ ...schedule, minute: Math.min(59, Math.max(0, Number(e.target.value))) })}
                  className="field-input w-10 text-center text-[11px]"
                />
              </div>
            </div>
          )}

          {/* Actions */}
          <div className="flex gap-1 pt-0.5 justify-end">
            <button
              onClick={save}
              disabled={saving}
              className="flex items-center gap-1 px-2 py-0.5 rounded text-[10px] bg-primary/90 text-primary-foreground hover:bg-primary disabled:opacity-50 transition-colors"
            >
              {saving && <Loader2 className="w-2.5 h-2.5 animate-spin" />}
              Save
            </button>
            <button
              onClick={() => setOpen(false)}
              className="px-2 py-0.5 rounded text-[10px] text-muted-foreground hover:text-foreground hover:bg-accent/40 transition-colors"
            >
              Cancel
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

// ----- Readonly canvas -----
function ReadonlyCanvas({ execution, nodeLogs, onNodeClick, selectedNodeId }: {
  execution: WorkflowExecution;
  nodeLogs: WorkflowNodeLog[];
  onNodeClick: (nodeId: string) => void;
  selectedNodeId: string | null;
}) {
  const [nodes, setNodes] = useNodesState([]);
  const [edges, setEdges] = useEdgesState([]);

  useEffect(() => {
    let def: any = null;
    try { def = JSON.parse(execution.definition_snapshot || '{}'); } catch {}
    if (!def?.nodes) return;

    // Build status map from logs.
    // If the execution is in a terminal state, any node still showing "running"
    // was interrupted mid-flight — override it to match the execution outcome.
    const terminalStates = ['completed', 'failed', 'cancelled'];
    const isTerminal = terminalStates.includes(execution.status);
    const statusMap = new Map<string, string>();
    nodeLogs.forEach((l) => {
      const s = isTerminal && l.status === 'running' ? execution.status : l.status;
      statusMap.set(l.node_id, s);
    });

    const flowNodes: Node[] = (def.nodes || []).map((n: any) => ({
      ...n,
      data: {
        ...n.data,
        _status: statusMap.get(n.id),
        _selected: n.id === selectedNodeId,
      },
      selected: n.id === selectedNodeId,
    }));

    const flowEdges: Edge[] = (def.edges || []).map((e: any) => {
      const srcStatus = statusMap.get(e.source);
      const tgtStatus = statusMap.get(e.target);
      let animated = false;
      let style: any = undefined;
      if (srcStatus === 'completed' && (tgtStatus === 'running' || tgtStatus === 'completed')) {
        animated = true;
        style = { stroke: 'hsl(142 71% 45%)', strokeWidth: 2.5 };
      } else if (srcStatus === 'running' || srcStatus === 'completed') {
        animated = true;
        style = { stroke: 'hsl(217 91% 60%)', strokeWidth: 2 };
      }
      return { ...e, animated, style, type: 'smoothstep' };
    });

    setNodes(flowNodes);
    setEdges(flowEdges);
  }, [execution, nodeLogs, selectedNodeId, setNodes, setEdges]);

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      onNodeClick={(_, node) => onNodeClick(node.id)}
      nodeTypes={nodeTypes}
      nodesDraggable={false}
      nodesConnectable={false}
      elementsSelectable={true}
      fitView
      className="workflow-canvas"
      proOptions={{ hideAttribution: true }}
    >
      <Background gap={16} size={1} className="!bg-background" />
      <Controls className="workflow-controls" showInteractive={false} />
    </ReactFlow>
  );
}

// ----- Node detail panel -----
function NodeDetailPanel({ nodeId, nodeLogs, executionId, executionStatus, onContinued }: {
  nodeId: string;
  nodeLogs: WorkflowNodeLog[];
  executionId: string;
  executionStatus: string;
  onContinued: () => void;
}) {
  const log = nodeLogs.find((l) => l.node_id === nodeId);
  const [continueInput, setContinueInput] = useState('');
  const [continuing, setContinuing] = useState(false);

  const handleContinue = async () => {
    setContinuing(true);
    try {
      await workflowApi.continueNode(executionId, nodeId, continueInput);
      setContinueInput('');
      onContinued();
    } catch (err: any) {
      toast.error('Continue failed: ' + err.message);
    } finally {
      setContinuing(false);
    }
  };

  if (!log) {
    return (
      <div className="flex items-center justify-center h-full text-[11px] text-muted-foreground/50">
        No execution data for this node
      </div>
    );
  }

  return (
    <div className="h-full overflow-y-auto p-3 space-y-2">
      <div className="flex items-center gap-2">
        {statusIcon(log.status)}
        <span className="text-[12px] font-medium">{log.node_label || nodeId}</span>
        <span className={`text-[10px] ${statusColor(log.status)}`}>{log.status}</span>
        {log.duration_ms && <span className="text-[10px] text-muted-foreground/60 ml-auto">{fmtDuration(log.duration_ms)}</span>}
      </div>

      {log.error && (
        <div className="text-[10px] text-red-500 bg-red-500/10 rounded px-2 py-1.5 font-mono break-all">
          {log.error}
        </div>
      )}

      {log.hook_result && (
        <div className={`flex items-center gap-1.5 text-[10px] rounded px-2 py-1 ${
          log.hook_result === 'ok' || log.hook_result === 'ok_after_retries'
            ? 'bg-emerald-500/10 text-emerald-400'
            : 'bg-amber-500/10 text-amber-400'
        }`}>
          <span className="font-semibold uppercase tracking-wider">Hook</span>
          <span>
            {log.hook_result === 'ok' && '✓ passed'}
            {log.hook_result === 'ok_after_retries' && `✓ passed after ${log.hook_retries} retr${log.hook_retries === 1 ? 'y' : 'ies'}`}
            {log.hook_result === 'agent_error' && '⚠ agent error (skipped)'}
            {log.hook_result === 'parse_error' && '⚠ response parse error (skipped)'}
          </span>
        </div>
      )}

      {log.output && (
        <div>
          <div className="text-[9px] font-semibold text-muted-foreground uppercase tracking-wider mb-0.5">Output</div>
          <div className="text-[10px] text-foreground/80 bg-muted/30 rounded px-2 py-1.5 font-mono break-all max-h-[80px] overflow-y-auto">
            {log.output}
          </div>
        </div>
      )}

      {/* Human node waiting — show continue UI */}
      {log.status === 'waiting' && executionStatus === 'running' && (
        <div className="space-y-1.5 border-t border-amber-500/20 pt-2">
          <div className="text-[10px] font-semibold text-amber-500 uppercase tracking-wider">Waiting at Checkpoint</div>
          <textarea
            value={continueInput}
            onChange={(e) => setContinueInput(e.target.value)}
            placeholder="Optional: add a message to pass to the next node"
            rows={2}
            className="field-input resize-none text-[11px] w-full"
          />
          <button
            onClick={handleContinue}
            disabled={continuing}
            className="flex items-center gap-1 px-2 py-1 rounded text-[11px] bg-emerald-600 text-white hover:bg-emerald-700 disabled:opacity-50 transition-colors"
          >
            {continuing ? <Loader2 className="w-3 h-3 animate-spin" /> : <ArrowRight className="w-3 h-3" />}
            Continue
          </button>
        </div>
      )}
    </div>
  );
}

// ----- Main component -----
function WorkflowRunsViewInner({ workflow, onWorkflowChange, onEdit }: WorkflowRunsViewProps) {
  const PAGE_SIZE = 20;

  const [executions, setExecutions] = useState<WorkflowExecution[]>([]);
  const [loadingExecs, setLoadingExecs] = useState(true);
  const [hasMore, setHasMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [selectedExec, setSelectedExec] = useState<WorkflowExecution | null>(null);
  const [nodeLogs, setNodeLogs] = useState<WorkflowNodeLog[]>([]);
  const [loadingLogs, setLoadingLogs] = useState(false);
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const [executing, setExecuting] = useState(false);

  const loadExecutions = useCallback(async () => {
    try {
      const data = await workflowApi.listExecutions(workflow.id, PAGE_SIZE);
      setExecutions(data || []);
      setHasMore((data || []).length >= PAGE_SIZE);
      // Auto-select running execution on initial load
      const running = (data || []).find((e) => e.status === 'running');
      if (running) {
        setSelectedExec(running);
        workflowApi.getExecutionLogs(running.id).then((logs) => {
          setNodeLogs(logs || []);
          const waiting = (logs || []).find((l) => l.status === 'waiting');
          if (waiting) setSelectedNodeId(waiting.node_id);
        }).catch(() => {});
      }
    } catch {
      // silent
    } finally {
      setLoadingExecs(false);
    }
  }, [workflow.id]);

  const loadMore = async () => {
    setLoadingMore(true);
    try {
      const more = await workflowApi.listExecutions(workflow.id, PAGE_SIZE, executions.length);
      setExecutions((prev) => [...prev, ...(more || [])]);
      setHasMore((more || []).length >= PAGE_SIZE);
    } catch {
      // silent
    } finally {
      setLoadingMore(false);
    }
  };

  useEffect(() => {
    loadExecutions();
  }, [loadExecutions]);

  // Subscribe to WS for live progress updates
  useEffect(() => {
    return globalWS.subscribeGlobal((event) => {
      if (event.type === 'workflow_progress') {
        const execId = event.execution_id as string;
        // Refresh execution status
        workflowApi.getExecution(execId).then((exec) => {
          if (!exec) return;
          setExecutions((prev) => {
            const idx = prev.findIndex((e) => e.id === execId);
            if (idx === -1) return [exec, ...prev];
            const next = [...prev];
            next[idx] = exec;
            return next;
          });
          if (selectedExec?.id === execId) {
            setSelectedExec(exec);
          }
        }).catch(() => {});

        // Refresh node logs for selected execution
        if (selectedExec?.id === execId) {
          workflowApi.getExecutionLogs(execId).then((logs) => {
            setNodeLogs(logs || []);
            // Auto-select a newly-waiting human node
            const waiting = (logs || []).find((l) => l.status === 'waiting');
            if (waiting) setSelectedNodeId(waiting.node_id);
          }).catch(() => {});
        }
      }

      // Workflow change — refresh execution list (a new run may have started)
      if (event.type === 'change' && event.entity_type === 'workflow' && event.entity_id === workflow.id) {
        loadExecutions();
      }
    });
  }, [selectedExec, workflow.id, loadExecutions]);

  const selectExecution = useCallback(async (exec: WorkflowExecution) => {
    setSelectedExec(exec);
    setSelectedNodeId(null);
    setNodeLogs([]);
    setLoadingLogs(true);
    try {
      const logs = await workflowApi.getExecutionLogs(exec.id);
      setNodeLogs(logs || []);
      // Auto-select any human node that is currently waiting
      const waiting = (logs || []).find((l) => l.status === 'waiting');
      if (waiting) setSelectedNodeId(waiting.node_id);
    } catch {
      // silent
    } finally {
      setLoadingLogs(false);
    }
  }, []);

  const handleRunNow = async () => {
    if (executions.some((e) => e.status === 'running')) {
      toast.error('Workflow is already running');
      return;
    }
    setExecuting(true);
    try {
      const exec = await workflowApi.execute(workflow.id);
      toast.success('Workflow started');
      setExecutions((prev) => [exec, ...prev]);
      selectExecution(exec);
    } catch (err: any) {
      toast.error('Failed to start: ' + err.message);
    } finally {
      setExecuting(false);
    }
  };

  const handleStop = async () => {
    const running = executions.find((e) => e.status === 'running');
    if (!running) return;
    try {
      await workflowApi.cancelExecution(running.id);
      toast.success('Execution stopped');
    } catch (err: any) {
      toast.error('Failed to stop: ' + err.message);
    }
  };

  const handleContinued = () => {
    if (selectedExec) {
      workflowApi.getExecutionLogs(selectedExec.id).then(setNodeLogs).catch(() => {});
    }
  };

  return (
    <div className="flex h-full min-h-0">
      {/* Left panel */}
      <div className="w-[240px] shrink-0 border-r border-border/20 flex flex-col">
        {/* Schedule */}
        <div className="px-3 py-2.5 border-b border-border/20">
          <ScheduleSection workflow={workflow} onSaved={(wf) => onWorkflowChange?.(wf)} />
        </div>

        {/* Run Now / Stop */}
        <div className="px-3 py-2 border-b border-border/20">
          {executions.some((e) => e.status === 'running') ? (
            <button
              onClick={handleStop}
              className="w-full flex items-center justify-center gap-1.5 px-3 py-1.5 rounded text-[12px] font-medium bg-red-600 text-white hover:bg-red-700 transition-colors"
            >
              <Square className="w-3.5 h-3.5 fill-current" />
              Stop
            </button>
          ) : (
            <button
              onClick={handleRunNow}
              disabled={executing}
              className="w-full flex items-center justify-center gap-1.5 px-3 py-1.5 rounded text-[12px] font-medium bg-emerald-600 text-white hover:bg-emerald-700 disabled:opacity-50 transition-colors"
            >
              {executing ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Play className="w-3.5 h-3.5" />}
              {executing ? 'Starting...' : 'Run Now'}
            </button>
          )}
        </div>

        {/* Execution history */}
        <div className="flex-1 overflow-y-auto">
          <div className="px-3 py-1.5 text-[9px] font-semibold text-muted-foreground uppercase tracking-wider">
            History
          </div>
          {loadingExecs ? (
            <div className="flex justify-center py-4">
              <Loader2 className="w-4 h-4 animate-spin text-muted-foreground" />
            </div>
          ) : executions.length === 0 ? (
            <div className="px-3 py-4 text-[11px] text-muted-foreground/50 text-center">No runs yet</div>
          ) : (
            <div className="space-y-px">
              {executions.map((exec) => (
                <button
                  key={exec.id}
                  onClick={() => selectExecution(exec)}
                  className={`w-full text-left flex items-center gap-2 px-3 py-1.5 transition-colors ${
                    selectedExec?.id === exec.id
                      ? 'bg-accent/60'
                      : 'hover:bg-accent/30'
                  }`}
                >
                  {statusIcon(exec.status, true)}
                  <div className="flex-1 min-w-0 flex items-center justify-between gap-1">
                    <span className="text-[11px] text-foreground/80 tabular-nums truncate">
                      {(() => { const d = new Date(exec.created_at); return `${String(d.getMonth()+1).padStart(2,'0')}/${String(d.getDate()).padStart(2,'0')} ${String(d.getHours()).padStart(2,'0')}:${String(d.getMinutes()).padStart(2,'0')}`; })()}
                    </span>
                    {exec.duration_ms && (
                      <span className="shrink-0 text-[10px] text-muted-foreground/60 tabular-nums">{fmtDuration(exec.duration_ms)}</span>
                    )}
                  </div>
                </button>
              ))}
              {hasMore && (
                <div className="flex justify-center py-2 border-t border-border/20">
                  <button
                    onClick={loadMore}
                    disabled={loadingMore}
                    className="px-3 py-1 text-xs text-muted-foreground hover:text-foreground hover:bg-accent rounded transition-colors disabled:opacity-50"
                  >
                    {loadingMore ? 'Loading...' : 'Load more'}
                  </button>
                </div>
              )}
            </div>
          )}
        </div>
      </div>

      {/* Right panel */}
      <div className="flex-1 flex flex-col min-w-0 min-h-0">
        {!selectedExec ? (
          <div className="relative flex items-center justify-center h-full text-[12px] text-muted-foreground/50">
            Select a run to view details
            {onEdit && (
              <button
                onClick={onEdit}
                className="absolute top-2 right-2 flex items-center gap-1 px-2 py-1 rounded text-[11px] text-muted-foreground hover:text-foreground hover:bg-accent/50 border border-border/30 transition-colors"
              >
                <Pencil className="w-3 h-3" />
                Edit Design
              </button>
            )}
          </div>
        ) : (
          <>
            {/* Execution meta bar */}
            <div className="flex items-center gap-2 px-3 h-[36px] border-b border-border/20 shrink-0">
              {statusIcon(selectedExec.status, true)}
              <span className={`text-[11px] font-medium ${statusColor(selectedExec.status)}`}>{selectedExec.status}</span>
              <span className="text-[10px] text-muted-foreground/60">v{selectedExec.workflow_version}</span>
              {selectedExec.duration_ms && (
                <span className="text-[10px] text-muted-foreground/60">{fmtDuration(selectedExec.duration_ms)}</span>
              )}
              {selectedExec.triggered_by && (
                <span className="text-[10px] text-muted-foreground/50">by {selectedExec.triggered_by}</span>
              )}
              {selectedExec.error && (
                <span className="text-[10px] text-red-500 truncate max-w-[200px]" title={selectedExec.error}>
                  {selectedExec.error}
                </span>
              )}
              <div className="flex-1" />
              {onEdit && (
                <button
                  onClick={onEdit}
                  className="flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors"
                  title="Edit design"
                >
                  <Pencil className="w-3 h-3" />
                  Edit
                </button>
              )}
            </div>

            {/* Canvas */}
            <div className="flex-1 min-h-0">
              {loadingLogs ? (
                <div className="flex items-center justify-center h-full">
                  <Loader2 className="w-4 h-4 animate-spin text-muted-foreground" />
                </div>
              ) : (
                <ReadonlyCanvas
                  execution={selectedExec}
                  nodeLogs={nodeLogs}
                  onNodeClick={(nodeId) => setSelectedNodeId(nodeId === selectedNodeId ? null : nodeId)}
                  selectedNodeId={selectedNodeId}
                />
              )}
            </div>

            {/* Node detail panel */}
            {selectedNodeId && (
              <div className="h-[180px] shrink-0 border-t border-border/20 bg-card">
                <NodeDetailPanel
                  nodeId={selectedNodeId}
                  nodeLogs={nodeLogs}
                  executionId={selectedExec.id}
                  executionStatus={selectedExec.status}
                  onContinued={handleContinued}
                />
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}

export default function WorkflowRunsView(props: WorkflowRunsViewProps) {
  return (
    <ReactFlowProvider>
      <WorkflowRunsViewInner {...props} />
    </ReactFlowProvider>
  );
}
