import { useCallback, useState, useRef, useEffect } from 'react';
import ReactFlow, {
  Background,
  Controls,
  MiniMap,
  Node,
  Edge,
  addEdge,
  Connection,
  useNodesState,
  useEdgesState,
  ReactFlowProvider,
  NodeChange,
  EdgeChange,
} from 'reactflow';
import 'reactflow/dist/style.css';
import { Save, Loader2, Sparkles, User, X, GitFork, GitMerge, Trash2 } from 'lucide-react';
import { toast } from 'sonner';

import AINode from './nodes/AINode';
import HumanNode from './nodes/HumanNode';
import StartNode from './nodes/StartNode';
import MapNode from './nodes/MapNode';
import ReduceNode from './nodes/ReduceNode';
import MultiSelectInput from './MultiSelectInput';

import { workflowApi, Workflow, WorkflowDefinition, HookConfig } from '@/lib/workflowApi';
import { tasksApi, assetsApi, serverApi } from '@/lib/api';
import type { Task, Asset } from '@/types';

export const nodeTypes = {
  start: StartNode,
  ai: AINode,
  human: HumanNode,
  map: MapNode,
  reduce: ReduceNode,
};

const PALETTE_ITEMS = [
  { type: 'ai', label: 'AI', icon: Sparkles },
  { type: 'human', label: 'Human', icon: User },
] as const;

interface WorkflowEditorProps {
  workflowId?: string;
  projectId: string;
  workflowName?: string;
  workflowDescription?: string;
  onSave?: (workflow: Workflow) => void;
  onDirtyChange?: (dirty: boolean) => void;
}

function WorkflowEditorInner({ workflowId, projectId, workflowName: externalName, workflowDescription: externalDesc, onSave, onDirtyChange }: WorkflowEditorProps) {
  const [nodes, setNodes, onNodesChangeBase] = useNodesState([]);
  const [edges, setEdges, onEdgesChangeBase] = useEdgesState([]);
  const [selectedNode, setSelectedNode] = useState<Node | null>(null);
  const [saving, setSaving] = useState(false);
  const [workflow, setWorkflow] = useState<Workflow | null>(null);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [assets, setAssets] = useState<Asset[]>([]);
  const [availableAgents, setAvailableAgents] = useState<string[]>([]);
  const [defaultAgent, setDefaultAgent] = useState<string>('');
  const [isDirty, setIsDirty] = useState(false);

  const reactFlowWrapper = useRef<HTMLDivElement>(null);
  const [reactFlowInstance, setReactFlowInstance] = useState<any>(null);
  const hasLoadedRef = useRef(false);

  useEffect(() => {
    serverApi.availableAgents().then((data) => {
      setAvailableAgents(data.agents || []);
      setDefaultAgent(data.default || '');
    }).catch(() => {});
  }, []);

  useEffect(() => {
    if (projectId) {
      tasksApi.list(100, 0, projectId).then(setTasks).catch(() => {});
      assetsApi.list(projectId, 100, 0).then(setAssets).catch(() => {});
    }
  }, [projectId]);

  useEffect(() => {
    hasLoadedRef.current = false;
    setIsDirty(false);
    onDirtyChange?.(false);

    let timeoutId: ReturnType<typeof setTimeout> | null = null;
    let cancelled = false;

    if (workflowId) {
      workflowApi.get(workflowId).then((wf) => {
        if (cancelled) return;
        setWorkflow(wf);
        if (wf.definition) {
          setNodes(wf.definition.nodes as Node[]);
          setEdges(wf.definition.edges as Edge[]);
        }
        // Mark as loaded after a tick so the state updates above don't trigger dirty
        timeoutId = setTimeout(() => { hasLoadedRef.current = true; }, 50);
      }).catch((err) => {
        if (!cancelled) toast.error('Failed to load workflow: ' + err.message);
      });
    } else {
      setNodes([
        {
          id: 'start-1',
          type: 'start',
          position: { x: 250, y: 50 },
          data: { label: 'Start' },
        },
      ]);
      setEdges([]);
      timeoutId = setTimeout(() => { hasLoadedRef.current = true; }, 50);
    }

    return () => {
      cancelled = true;
      if (timeoutId) clearTimeout(timeoutId);
    };
  }, [workflowId, setNodes, setEdges]);

  useEffect(() => {
    if (selectedNode) {
      const updatedNode = nodes.find((n) => n.id === selectedNode.id);
      if (updatedNode && updatedNode !== selectedNode) {
        setSelectedNode(updatedNode);
      }
    }
  }, [nodes, selectedNode]);

  const markDirty = useCallback(() => {
    if (!hasLoadedRef.current) return;
    if (!isDirty) {
      setIsDirty(true);
      onDirtyChange?.(true);
    }
  }, [isDirty, onDirtyChange]);

  // Wrap change handlers to detect meaningful user changes
  const onNodesChange = useCallback((changes: NodeChange[]) => {
    onNodesChangeBase(changes);
    const significant = changes.some((c) => {
      if (c.type === 'select' || c.type === 'dimensions') return false;
      // ReactFlow emits a position change with no position data on click (drag-stop with no movement) — ignore it
      if (c.type === 'position' && !(c as any).position) return false;
      return true;
    });
    if (significant) markDirty();
  }, [onNodesChangeBase, markDirty]);

  const onEdgesChange = useCallback((changes: EdgeChange[]) => {
    onEdgesChangeBase(changes);
    const significant = changes.some((c) => c.type !== 'select');
    if (significant) markDirty();
  }, [onEdgesChangeBase, markDirty]);

  const onConnect = useCallback(
    (connection: Connection) => {
      setEdges((eds) => addEdge({
        ...connection,
        animated: true,
        type: 'smoothstep',
      }, eds));
      markDirty();
    },
    [setEdges, markDirty]
  );

  const onNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    setSelectedNode(node);
  }, []);

  const onPaneClick = useCallback(() => {
    setSelectedNode(null);
  }, []);

  const onDragOver = useCallback((event: React.DragEvent) => {
    event.preventDefault();
    event.dataTransfer.dropEffect = 'move';
  }, []);

  const onDrop = useCallback(
    (event: React.DragEvent) => {
      event.preventDefault();

      const type = event.dataTransfer.getData('application/reactflow');
      if (!type || !reactFlowInstance) return;

      const position = reactFlowInstance.screenToFlowPosition({
        x: event.clientX,
        y: event.clientY,
      });

      const config: any = {};

      const defaultLabels: Record<string, string> = {
        ai: 'AI Node',
        human: 'Human Node',
        map: 'Map',
        reduce: 'Reduce',
      };

      const newNode: Node = {
        id: `${type}-${Date.now()}`,
        type,
        position,
        data: { label: defaultLabels[type] || `${type.charAt(0).toUpperCase() + type.slice(1)} Node`, config },
      };

      setNodes((nds) => [...nds, newNode]);
      markDirty();
    },
    [reactFlowInstance, setNodes, markDirty]
  );

  const buildDefinition = (): WorkflowDefinition => ({
    nodes: nodes.map((n) => ({
      id: n.id,
      type: n.type || 'default',
      position: n.position,
      data: n.data,
    })),
    edges: edges.map((e) => ({
      id: e.id,
      source: e.source,
      target: e.target,
      label: typeof e.label === 'string' ? e.label : undefined,
    })),
  });

  const handleSave = async () => {
    if (!projectId) {
      toast.error('Project ID is required');
      return;
    }

    setSaving(true);
    try {
      const definition = buildDefinition();
      let savedWorkflow: Workflow;
      if (workflowId) {
        const updateData: Parameters<typeof workflowApi.update>[1] = { definition };
        if (externalName) updateData.name = externalName;
        if (externalDesc !== undefined) updateData.description = externalDesc;
        savedWorkflow = await workflowApi.update(workflowId, updateData);
        toast.success('Workflow updated');
      } else {
        const name = (externalName || '').trim() || 'Untitled Workflow';
        savedWorkflow = await workflowApi.create(projectId, name, '', definition);
        toast.success('Workflow created');
      }

      setWorkflow(savedWorkflow);
      setIsDirty(false);
      onDirtyChange?.(false);
      onSave?.(savedWorkflow);
    } catch (error: any) {
      toast.error('Failed to save: ' + error.message);
    } finally {
      setSaving(false);
    }
  };

  const deleteSelectedNode = useCallback(() => {
    if (!selectedNode || selectedNode.type === 'start') return;
    setNodes((nds) => nds.filter((n) => n.id !== selectedNode.id));
    setEdges((eds) => eds.filter((e) => e.source !== selectedNode.id && e.target !== selectedNode.id));
    setSelectedNode(null);
    markDirty();
  }, [selectedNode, setNodes, setEdges, markDirty]);

  const updateNodeData = useCallback((nodeId: string, path: string, value: any) => {
    setNodes((nds) =>
      nds.map((n) => {
        if (n.id !== nodeId) return n;
        if (path === 'label') {
          return { ...n, data: { ...n.data, label: value } };
        }
        return {
          ...n,
          data: {
            ...n.data,
            config: { ...n.data.config, [path]: value },
          },
        };
      })
    );
    markDirty();
  }, [setNodes, markDirty]);

  return (
    <div className="h-full flex flex-col">
      {/* Toolbar */}
      <div className="flex items-center px-3 h-[36px] border-b border-border/20 shrink-0 gap-1.5">
        {PALETTE_ITEMS.map(({ type, label, icon: Icon }) => (
          <div
            key={type}
            draggable
            onDragStart={(e) => e.dataTransfer.setData('application/reactflow', type)}
            className="flex items-center gap-1 px-1.5 py-0.5 rounded text-[11px] text-muted-foreground hover:text-foreground hover:bg-accent/40 cursor-grab active:cursor-grabbing select-none transition-colors"
            title={`Drag to add ${label} node`}
          >
            <Icon className="w-3 h-3" />
            <span>{label}</span>
          </div>
        ))}

        <div className="flex-1" />

        <span className="text-[10px] text-muted-foreground tabular-nums">
          {nodes.length} nodes
        </span>

        {isDirty && (
          <span className="text-[10px] text-amber-500">unsaved</span>
        )}

        <button
          onClick={handleSave}
          disabled={saving}
          className="flex items-center gap-1 px-1.5 py-0.5 rounded text-[11px] text-muted-foreground hover:text-foreground hover:bg-accent/40 disabled:opacity-50 transition-colors"
        >
          {saving ? <Loader2 className="w-3 h-3 animate-spin" /> : <Save className="w-3 h-3" />}
          Save
        </button>
      </div>

      {/* Main editor area */}
      <div className="flex-1 flex min-h-0">
        {/* Canvas */}
        <div className="flex-1" ref={reactFlowWrapper}>
          <ReactFlow
            nodes={nodes}
            edges={edges}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onNodeClick={onNodeClick}
            onPaneClick={onPaneClick}
            onDrop={onDrop}
            onDragOver={onDragOver}
            onInit={setReactFlowInstance}
            nodeTypes={nodeTypes}
            fitView
            className="workflow-canvas"
            proOptions={{ hideAttribution: true }}
            deleteKeyCode="Backspace"
          >
            <Background gap={16} size={1} className="!bg-background" />
            <Controls className="workflow-controls" />
            <MiniMap
              nodeStrokeWidth={3}
              className="workflow-minimap"
              maskColor="hsl(var(--muted) / 0.7)"
              nodeColor="hsl(var(--muted-foreground) / 0.3)"
            />
          </ReactFlow>
        </div>

        {/* Property Panel - always visible */}
        <div className="w-72 border-l border-border/30 bg-card flex flex-col shrink-0">
          {/* Panel header */}
          <div className="flex items-center justify-between px-3 py-2 border-b border-border/30">
            <span className="text-xs font-semibold">{selectedNode ? 'Properties' : 'Workflow'}</span>
            {selectedNode && (
              <div className="flex items-center gap-1">
                {selectedNode.type !== 'start' && (
                  <button
                    onClick={deleteSelectedNode}
                    className="p-0.5 rounded hover:bg-destructive/20 text-muted-foreground hover:text-destructive transition-colors"
                    title="Delete node"
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                  </button>
                )}
                <button
                  onClick={() => setSelectedNode(null)}
                  className="p-0.5 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
                >
                  <X className="w-3.5 h-3.5" />
                </button>
              </div>
            )}
          </div>

          {/* Panel body */}
          <div className="flex-1 overflow-y-auto p-3 space-y-4">
            {selectedNode && (<>
              {/* General */}
              <section className="space-y-2.5">
                <div className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">General</div>
                <Field label="Node ID">
                  <input type="text" value={selectedNode.id} disabled className="field-input bg-muted/40 text-muted-foreground" />
                </Field>
                <Field label="Type">
                  <input type="text" value={selectedNode.type || ''} disabled className="field-input bg-muted/40 text-muted-foreground" />
                </Field>
                <Field label="Label">
                  <NodeInput
                    type="text"
                    nodeId={selectedNode.id}
                    value={selectedNode.data.label}
                    onChange={(v) => updateNodeData(selectedNode.id, 'label', v)}
                    className="field-input"
                  />
                </Field>
              </section>

              {/* AI Config */}
              {selectedNode.type === 'ai' && (
                <section className="space-y-2.5">
                  <div className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">AI Config</div>
                  <Field label="Agent">
                    <select
                      value={selectedNode.data.config?.agent || ''}
                      onChange={(e) => updateNodeData(selectedNode.id, 'agent', e.target.value)}
                      className="field-input"
                    >
                      <option value="">System Default{defaultAgent ? ` (${defaultAgent})` : ''}</option>
                      {availableAgents.map((ag) => (
                        <option key={ag} value={ag}>{ag}</option>
                      ))}
                    </select>
                  </Field>
                  <Field label="Prompt">
                    <PromptInput
                      nodeId={selectedNode.id}
                      value={selectedNode.data.config?.prompt || ''}
                      onChange={(value) => updateNodeData(selectedNode.id, 'prompt', value)}
                    />
                  </Field>
                  <Field label="Task Reference">
                    <MultiSelectInput
                      items={tasks}
                      selectedIds={selectedNode.data.config?.taskRef || []}
                      onSelect={(taskId) => {
                        const arr = Array.isArray(selectedNode.data.config?.taskRef) ? selectedNode.data.config.taskRef : [];
                        if (!arr.includes(taskId)) {
                          updateNodeData(selectedNode.id, 'taskRef', [...arr, taskId]);
                        }
                      }}
                      onRemove={(taskId) => {
                        updateNodeData(selectedNode.id, 'taskRef',
                          (selectedNode.data.config?.taskRef || []).filter((id: string) => id !== taskId));
                      }}
                      placeholder="Click to select tasks"
                      itemType="task"
                    />
                  </Field>
                  <Field label="Asset Reference">
                    <MultiSelectInput
                      items={assets}
                      selectedIds={selectedNode.data.config?.assetRef || []}
                      onSelect={(assetId) => {
                        const arr = Array.isArray(selectedNode.data.config?.assetRef) ? selectedNode.data.config.assetRef : [];
                        if (!arr.includes(assetId)) {
                          updateNodeData(selectedNode.id, 'assetRef', [...arr, assetId]);
                        }
                      }}
                      onRemove={(assetId) => {
                        updateNodeData(selectedNode.id, 'assetRef',
                          (selectedNode.data.config?.assetRef || []).filter((id: string) => id !== assetId));
                      }}
                      placeholder="Click to select assets"
                      itemType="asset"
                    />
                  </Field>
                </section>
              )}

              {/* Hook Config — only for AI nodes */}
              {selectedNode.type === 'ai' && (
                <section className="space-y-2.5">
                  <div className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">Hook</div>
                  <label className="flex items-center gap-2 cursor-pointer py-0.5">
                    <input
                      type="checkbox"
                      checked={!!selectedNode.data.config?.hook?.trigger}
                      onChange={(e) => {
                        if (e.target.checked) {
                          updateNodeData(selectedNode.id, 'hook', { trigger: 'on_reply_done', prompt: '', reset: false } as HookConfig);
                        } else {
                          updateNodeData(selectedNode.id, 'hook', undefined);
                        }
                      }}
                      className="rounded border-border accent-primary h-3.5 w-3.5"
                    />
                    <span className="text-[11px] text-muted-foreground">Enable Hook (on reply done)</span>
                  </label>
                  {!!selectedNode.data.config?.hook?.trigger && (
                    <>
                      <Field label="Hook Prompt">
                        <NodeTextarea
                          nodeId={selectedNode.id}
                          value={selectedNode.data.config?.hook?.prompt || ''}
                          onChange={(v) => updateNodeData(selectedNode.id, 'hook', { ...selectedNode.data.config.hook, prompt: v })}
                          placeholder="Describe what to verify. The hook agent will check the output and respond with {&quot;ok&quot;: true} or {&quot;ok&quot;: false, &quot;reason&quot;: &quot;...&quot;}"
                          rows={4}
                          className="field-input resize-none font-mono text-[10px]"
                        />
                      </Field>
                      <label className="flex items-center gap-2 cursor-pointer py-0.5">
                        <input
                          type="checkbox"
                          checked={!!selectedNode.data.config?.hook?.reset}
                          onChange={(e) => updateNodeData(selectedNode.id, 'hook', { ...selectedNode.data.config.hook, reset: e.target.checked })}
                          className="rounded border-border accent-primary h-3.5 w-3.5"
                        />
                        <span className="text-[11px] text-muted-foreground">Reset session on retry</span>
                      </label>
                    </>
                  )}
                </section>
              )}

              {/* Human Config */}
              {selectedNode.type === 'human' && (
                <section className="space-y-2.5">
                  <div className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">Task Config</div>
                  <Field label="Description">
                    <NodeTextarea
                      nodeId={selectedNode.id}
                      value={selectedNode.data.config?.description || ''}
                      onChange={(v) => updateNodeData(selectedNode.id, 'description', v)}
                      placeholder="Describe the manual task"
                      rows={4}
                      className="field-input resize-none"
                    />
                  </Field>
                </section>
              )}
            </>)}

            {!selectedNode && (
              <div className="text-[11px] text-muted-foreground/50 text-center pt-8">
                Click a node to edit properties
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="block text-[11px] font-medium mb-1 text-muted-foreground">{label}</label>
      {children}
    </div>
  );
}

// IME-safe textarea: local state prevents re-renders from breaking composition
function NodeTextarea({ nodeId, value, onChange, ...rest }: {
  nodeId: string; value: string; onChange: (v: string) => void;
} & React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  const [local, setLocal] = useState(value);
  const composingRef = useRef(false);

  useEffect(() => {
    if (!composingRef.current) setLocal(value);
  }, [nodeId, value]);

  return (
    <textarea
      {...rest}
      value={local}
      onChange={(e) => {
        setLocal(e.target.value);
        if (!composingRef.current) onChange(e.target.value);
      }}
      onCompositionStart={() => { composingRef.current = true; }}
      onCompositionEnd={(e) => {
        composingRef.current = false;
        const v = e.currentTarget.value;
        setLocal(v);
        onChange(v);
      }}
    />
  );
}

// IME-safe input: same pattern for single-line fields
function NodeInput({ nodeId, value, onChange, ...rest }: {
  nodeId: string; value: string; onChange: (v: string) => void;
} & React.InputHTMLAttributes<HTMLInputElement>) {
  const [local, setLocal] = useState(value);
  const composingRef = useRef(false);

  useEffect(() => {
    if (!composingRef.current) setLocal(value);
  }, [nodeId, value]);

  return (
    <input
      {...rest}
      value={local}
      onChange={(e) => {
        setLocal(e.target.value);
        if (!composingRef.current) onChange(e.target.value);
      }}
      onCompositionStart={() => { composingRef.current = true; }}
      onCompositionEnd={(e) => {
        composingRef.current = false;
        const v = e.currentTarget.value;
        setLocal(v);
        onChange(v);
      }}
    />
  );
}

function PromptInput({ nodeId, value, onChange }: { nodeId: string; value: string; onChange: (value: string) => void }) {
  const [localValue, setLocalValue] = useState(value);
  const timeoutRef = useRef<NodeJS.Timeout | null>(null);
  const composingRef = useRef(false);

  useEffect(() => {
    if (!composingRef.current) {
      setLocalValue(value);
      if (timeoutRef.current) { clearTimeout(timeoutRef.current); timeoutRef.current = null; }
    }
  }, [nodeId, value]);

  const handleChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const newValue = e.target.value;
    setLocalValue(newValue);
    if (composingRef.current) return;
    if (timeoutRef.current) clearTimeout(timeoutRef.current);
    timeoutRef.current = setTimeout(() => onChange(newValue), 300);
  };

  useEffect(() => {
    return () => { if (timeoutRef.current) clearTimeout(timeoutRef.current); };
  }, []);

  return (
    <textarea
      value={localValue}
      onChange={handleChange}
      onCompositionStart={() => { composingRef.current = true; }}
      onCompositionEnd={(e) => {
        composingRef.current = false;
        const v = e.currentTarget.value;
        setLocalValue(v);
        if (timeoutRef.current) clearTimeout(timeoutRef.current);
        onChange(v);
      }}
      placeholder="Enter AI prompt"
      rows={5}
      className="field-input resize-y font-mono min-h-[80px]"
    />
  );
}

export default function WorkflowEditor(props: WorkflowEditorProps) {
  return (
    <ReactFlowProvider>
      <WorkflowEditorInner {...props} />
    </ReactFlowProvider>
  );
}
