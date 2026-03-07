import { useCallback, useEffect, useRef, useState } from 'react';
import ReactFlow, {
  Background,
  Controls,
  MiniMap,
  Node,
  Edge,
  useNodesState,
  useEdgesState,
  ReactFlowProvider,
} from 'reactflow';
import 'reactflow/dist/style.css';
import {
  ArrowLeft,
  CheckCircle2,
  XCircle,
  Loader2,
  Clock,
  RotateCw,
} from 'lucide-react';

import AINode from './nodes/AINode';
import HumanNode from './nodes/HumanNode';
import StartNode from './nodes/StartNode';
import MapNode from './nodes/MapNode';
import ReduceNode from './nodes/ReduceNode';
import {
  workflowApi,
  WorkflowExecution,
  WorkflowNodeLog,
  WorkflowDefinition,
} from '@/lib/workflowApi';

const nodeTypes = { start: StartNode, ai: AINode, human: HumanNode, map: MapNode, reduce: ReduceNode };

const STATUS_RING: Record<string, string> = {
  running:   'ring-2 ring-blue-500 ring-offset-2 ring-offset-background',
  completed: 'ring-2 ring-emerald-500 ring-offset-2 ring-offset-background',
  failed:    'ring-2 ring-red-500 ring-offset-2 ring-offset-background',
  skipped:   'ring-2 ring-muted-foreground/40 ring-offset-2 ring-offset-background',
  pending:   '',
};

const STATUS_ICON: Record<string, React.ReactNode> = {
  running:   <Loader2 className="w-3.5 h-3.5 animate-spin text-blue-500" />,
  completed: <CheckCircle2 className="w-3.5 h-3.5 text-emerald-500" />,
  failed:    <XCircle className="w-3.5 h-3.5 text-red-500" />,
  pending:   <Clock className="w-3.5 h-3.5 text-muted-foreground" />,
  skipped:   <Clock className="w-3.5 h-3.5 text-muted-foreground/50" />,
};

interface ExecutionViewProps {
  executionId: string;
  definition: WorkflowDefinition;
  onBack: () => void;
}

function ExecutionViewInner({ executionId, definition, onBack }: ExecutionViewProps) {
  const [nodes, setNodes, onNodesChange] = useNodesState([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState([]);
  const [execution, setExecution] = useState<WorkflowExecution | null>(null);
  const [logs, setLogs] = useState<WorkflowNodeLog[]>([]);
  const [selectedLog, setSelectedLog] = useState<WorkflowNodeLog | null>(null);
  const pollRef = useRef<ReturnType<typeof setInterval>>(undefined);

  useEffect(() => {
    setNodes(
      definition.nodes.map((n) => ({
        ...n,
        data: { ...n.data, _execStatus: 'pending' },
        draggable: false,
        connectable: false,
      })) as Node[],
    );
    setEdges(
      definition.edges.map((e) => ({
        ...e,
        animated: false,
        type: 'smoothstep',
      })) as Edge[],
    );
  }, [definition, setNodes, setEdges]);

  const refresh = useCallback(async () => {
    try {
      const [exec, nodeLogs] = await Promise.all([
        workflowApi.getExecution(executionId),
        workflowApi.getExecutionLogs(executionId),
      ]);
      setExecution(exec);
      setLogs(nodeLogs || []);

      const statusMap = new Map<string, string>();
      (nodeLogs || []).forEach((l) => statusMap.set(l.node_id, l.status));

      setNodes((nds) =>
        nds.map((n) => {
          const st = statusMap.get(n.id) || 'pending';
          return {
            ...n,
            data: { ...n.data, _execStatus: st },
            className: STATUS_RING[st] || '',
          };
        }),
      );

      if (exec.status === 'completed' || exec.status === 'failed' || exec.status === 'cancelled') {
        if (pollRef.current) clearInterval(pollRef.current);
      }
    } catch {
      /* ignore */
    }
  }, [executionId, setNodes]);

  useEffect(() => {
    refresh();
    pollRef.current = setInterval(refresh, 1500);
    return () => { if (pollRef.current) clearInterval(pollRef.current); };
  }, [refresh]);

  const isTerminal = execution && ['completed', 'failed', 'cancelled'].includes(execution.status);

  return (
    <div className="h-full flex flex-col">
      {/* Header */}
      <div className="flex items-center px-3 py-1.5 border-b border-border/30 bg-muted/30 shrink-0 gap-2">
        <button
          onClick={onBack}
          className="p-1 rounded-md hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
        >
          <ArrowLeft className="w-4 h-4" />
        </button>

        <div className="h-4 w-px bg-border/40" />

        <span className="text-[11px] font-semibold">Execution</span>
        <code className="text-[10px] text-muted-foreground font-mono">{executionId.slice(0, 8)}</code>

        {execution && (
          <span
            className={`ml-1 px-1.5 py-0.5 rounded-full text-[10px] font-medium ${
              execution.status === 'completed'
                ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400'
                : execution.status === 'failed'
                ? 'bg-red-500/15 text-red-600 dark:text-red-400'
                : execution.status === 'running'
                ? 'bg-blue-500/15 text-blue-600 dark:text-blue-400'
                : 'bg-muted text-muted-foreground'
            }`}
          >
            {execution.status}
          </span>
        )}

        {execution?.duration_ms != null && (
          <span className="text-[10px] text-muted-foreground tabular-nums">
            {execution.duration_ms}ms
          </span>
        )}

        <div className="flex-1" />

        {!isTerminal && (
          <span className="flex items-center gap-1 text-[10px] text-blue-500">
            <Loader2 className="w-3 h-3 animate-spin" /> polling
          </span>
        )}
        {isTerminal && (
          <button
            onClick={refresh}
            className="flex items-center gap-1 px-2 py-1 rounded-md text-[10px] text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
          >
            <RotateCw className="w-3 h-3" /> Refresh
          </button>
        )}
      </div>

      {/* Body */}
      <div className="flex-1 flex min-h-0">
        {/* Canvas */}
        <div className="flex-1">
          <ReactFlow
            nodes={nodes}
            edges={edges}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            nodeTypes={nodeTypes}
            nodesDraggable={false}
            nodesConnectable={false}
            elementsSelectable={false}
            fitView
            className="workflow-canvas"
            proOptions={{ hideAttribution: true }}
          >
            <Background gap={16} size={1} className="!bg-background" />
            <Controls className="workflow-controls" showInteractive={false} />
            <MiniMap
              nodeStrokeWidth={3}
              className="workflow-minimap"
              maskColor="hsl(var(--muted) / 0.7)"
              nodeColor="hsl(var(--muted-foreground) / 0.3)"
            />
          </ReactFlow>
        </div>

        {/* Logs panel */}
        <div className="w-72 border-l border-border/30 bg-card flex flex-col shrink-0">
          <div className="px-3 py-2 border-b border-border/30">
            <span className="text-xs font-semibold">Node Logs</span>
            <span className="ml-1.5 text-[10px] text-muted-foreground tabular-nums">{logs.length}</span>
          </div>
          <div className="flex-1 overflow-y-auto">
            {logs.length === 0 ? (
              <div className="flex items-center justify-center h-full text-xs text-muted-foreground">
                Waiting for logs...
              </div>
            ) : (
              <div className="p-1.5 space-y-0.5">
                {logs.map((log) => (
                  <button
                    key={log.id}
                    onClick={() => setSelectedLog(selectedLog?.id === log.id ? null : log)}
                    className={`w-full text-left px-2.5 py-2 rounded-md text-xs transition-colors ${
                      selectedLog?.id === log.id
                        ? 'bg-accent'
                        : 'hover:bg-accent/50'
                    }`}
                  >
                    <div className="flex items-center gap-1.5">
                      {STATUS_ICON[log.status] || STATUS_ICON.pending}
                      <span className="font-medium truncate flex-1">{log.node_id}</span>
                      <span className="text-[10px] text-muted-foreground tabular-nums shrink-0">
                        {log.duration_ms != null ? `${log.duration_ms}ms` : '–'}
                      </span>
                    </div>

                    {selectedLog?.id === log.id && (
                      <div className="mt-2 space-y-1.5 text-[11px]">
                        <div className="flex gap-2">
                          <span className="text-muted-foreground shrink-0">Type</span>
                          <span className="font-mono">{log.node_type}</span>
                        </div>
                        <div className="flex gap-2">
                          <span className="text-muted-foreground shrink-0">Status</span>
                          <span className="font-mono">{log.status}</span>
                        </div>
                        {log.output && (
                          <div>
                            <span className="text-muted-foreground">Output</span>
                            <pre className="mt-0.5 p-1.5 rounded bg-muted/50 text-[10px] font-mono whitespace-pre-wrap break-all max-h-32 overflow-auto">
                              {log.output}
                            </pre>
                          </div>
                        )}
                        {log.error && (
                          <div>
                            <span className="text-red-500">Error</span>
                            <pre className="mt-0.5 p-1.5 rounded bg-red-500/10 text-[10px] font-mono text-red-600 dark:text-red-400 whitespace-pre-wrap break-all max-h-32 overflow-auto">
                              {log.error}
                            </pre>
                          </div>
                        )}
                      </div>
                    )}
                  </button>
                ))}
              </div>
            )}
          </div>

          {/* Execution output/error summary */}
          {execution && isTerminal && (
            <div className="border-t border-border/30 p-3">
              {execution.error ? (
                <div>
                  <div className="text-[10px] font-semibold text-red-500 uppercase tracking-wider mb-1">Error</div>
                  <pre className="text-[10px] font-mono text-red-600 dark:text-red-400 whitespace-pre-wrap break-all max-h-24 overflow-auto">
                    {execution.error}
                  </pre>
                </div>
              ) : (
                <div>
                  <div className="text-[10px] font-semibold text-emerald-500 uppercase tracking-wider mb-1">Completed</div>
                  {execution.output && (
                    <pre className="text-[10px] font-mono text-muted-foreground whitespace-pre-wrap break-all max-h-24 overflow-auto">
                      {execution.output.slice(0, 500)}
                    </pre>
                  )}
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

export default function ExecutionView(props: ExecutionViewProps) {
  return (
    <ReactFlowProvider>
      <ExecutionViewInner {...props} />
    </ReactFlowProvider>
  );
}
