import { sessionsApi } from '@/lib/api';
import { globalWS } from '@/lib/ws';
import { useAppStore } from '@/stores/app';
import type { WorkflowDefinition, WorkflowNode } from '@/lib/workflowApi';
import type { Session, Task, MessagePart } from '@/types';
import type { PickedAsset } from '@/stores/app';

export type NodeStatus = 'pending' | 'running' | 'completed' | 'failed';

export interface RunLogEntry {
  nodeId: string;
  label: string;
  status: NodeStatus;
  time: number;
}

export type NodeStatusCallback = (nodeId: string, status: NodeStatus) => void;
export type LogCallback = (entry: RunLogEntry) => void;

/**
 * Build topological levels from a DAG definition.
 * Nodes in the same level have no dependency on each other → can run in parallel.
 */
function buildLevels(def: WorkflowDefinition): WorkflowNode[][] {
  const inDegree = new Map<string, number>();
  const adj = new Map<string, string[]>();
  const nodeMap = new Map<string, WorkflowNode>();

  for (const n of def.nodes) {
    nodeMap.set(n.id, n);
    inDegree.set(n.id, 0);
    adj.set(n.id, []);
  }
  for (const e of def.edges) {
    adj.get(e.source)?.push(e.target);
    inDegree.set(e.target, (inDegree.get(e.target) || 0) + 1);
  }

  const levels: WorkflowNode[][] = [];
  let queue = [...inDegree.entries()].filter(([, d]) => d === 0).map(([id]) => id);

  while (queue.length > 0) {
    const level = queue.map((id) => nodeMap.get(id)!);
    levels.push(level);
    const next: string[] = [];
    for (const id of queue) {
      for (const neighbor of adj.get(id) || []) {
        const d = (inDegree.get(neighbor) || 1) - 1;
        inDegree.set(neighbor, d);
        if (d === 0) next.push(neighbor);
      }
    }
    queue = next;
  }

  return levels;
}

interface WaitResult {
  status: 'done' | 'error' | 'stopped';
  output: string;
}

function waitForDone(
  sessionId: string,
  signal?: AbortSignal,
  timeoutMs = 30 * 60 * 1000,
): Promise<WaitResult> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      resolve({ status: 'stopped', output: '' });
      return;
    }

    let output = '';

    const cleanup = () => {
      clearTimeout(timer);
      unsub();
      signal?.removeEventListener('abort', onAbort);
    };

    const onAbort = () => {
      cleanup();
      globalWS.stop(sessionId);
      resolve({ status: 'stopped', output });
    };

    const timer = setTimeout(() => {
      cleanup();
      reject(new Error('Session timed out'));
    }, timeoutMs);

    const unsub = globalWS.subscribe(sessionId, (event) => {
      if (event.type === 'text_delta' && event.content) {
        output += event.content;
      }
      if (event.type === 'done') {
        cleanup();
        resolve({ status: 'done', output });
      }
      if (event.type === 'error') {
        cleanup();
        resolve({ status: 'error', output });
      }
    });

    signal?.addEventListener('abort', onAbort);
  });
}

export interface RunOptions {
  workflowName: string;
  definition: WorkflowDefinition;
  projectId: string;
  defaultAgent?: string;
  userContext?: string;
  tasks?: Task[];
  assets?: PickedAsset[];
  onNodeStatus?: NodeStatusCallback;
  onLog?: LogCallback;
}

export async function runWorkflow(opts: RunOptions): Promise<'completed' | 'stopped'> {
  const { workflowName, definition, projectId, defaultAgent, userContext, tasks, assets, onNodeStatus, onLog } = opts;
  const levels = buildLevels(definition);
  const store = useAppStore.getState();
  const nodeLabels = new Map(definition.nodes.map((n) => [n.id, n.data.label || n.type || n.id]));

  const ac = new AbortController();
  const { signal } = ac;

  const onWorkflowStop = () => ac.abort();
  window.addEventListener('workflow-stop', onWorkflowStop);

  // Build parent map (node → list of parent node IDs)
  const parentMap = new Map<string, string[]>();
  for (const n of definition.nodes) parentMap.set(n.id, []);
  for (const e of definition.edges) {
    parentMap.get(e.target)?.push(e.source);
  }

  const nodeTypeMap = new Map(definition.nodes.map((n) => [n.id, n.type || '']));
  // Stores LLM text output (for AI nodes) or collected context (for map/reduce nodes)
  const nodeOutputs = new Map<string, string>();

  const emit = (nodeId: string, status: NodeStatus) => {
    onNodeStatus?.(nodeId, status);
    onLog?.({ nodeId, label: nodeLabels.get(nodeId) || nodeId, status, time: Date.now() });
  };

  const markRemaining = (fromLevel: number) => {
    for (let i = fromLevel; i < levels.length; i++) {
      for (const n of levels[i]) emit(n.id, 'failed');
    }
  };

  // Only return context from map/reduce parents — serial AI chains use session history instead
  const getUpstreamContext = (nodeId: string): string => {
    const parents = parentMap.get(nodeId) || [];
    const parts: string[] = [];
    for (const pid of parents) {
      const pType = nodeTypeMap.get(pid);
      if (pType === 'map' || pType === 'reduce') {
        const out = nodeOutputs.get(pid);
        if (out) parts.push(out);
      }
    }
    if (parts.length === 0) return '';
    return parts.join('\n\n---\n\n');
  };

  let userContextConsumed = false;

  const buildNodeMessage = (nodeId: string, originalPrompt: string, config?: Record<string, any>): { prompt: string; parts?: MessagePart[] } => {
    let prompt = originalPrompt;
    let parts: MessagePart[] | undefined;

    if (!userContextConsumed && (userContext || (tasks && tasks.length > 0) || (assets && assets.length > 0))) {
      userContextConsumed = true;

      if (userContext) {
        prompt = `[User Context]\n${userContext}\n\n---\n\n${prompt}`;
      }

      const refParts: MessagePart[] = [];
      if (tasks && tasks.length > 0) {
        for (const t of tasks) refParts.push({ type: 'task_ref', taskId: t.id, task: t });
      }
      if (assets && assets.length > 0) {
        for (const a of assets) refParts.push({ type: 'asset_ref', assetId: a.id, title: a.title, assetType: a.assetType });
      }
      if (refParts.length > 0) parts = refParts;
    }

    const ctx = getUpstreamContext(nodeId);
    if (ctx) {
      prompt = `[Context from previous steps]\n\n${ctx}\n\n---\n\n${prompt}`;
    }

    return { prompt, parts };
  };

  try {
    for (const n of definition.nodes) emit(n.id, 'pending');

    let primarySession: Session | null = null;

    for (let li = 0; li < levels.length; li++) {
      if (signal.aborted) {
        markRemaining(li);
        return 'stopped';
      }

      const level = levels[li];
      const aiNodes = level.filter((n) => n.type === 'ai' && n.data.config?.prompt);
      const passNodes = level.filter((n) => n.type !== 'ai' || !n.data.config?.prompt);

      // --- Process pass-through nodes (start, human, map, reduce) ---
      for (const n of passNodes) {
        emit(n.id, 'running');

        if (n.type === 'map') {
          // Map: collect upstream outputs and pass them through
          const parents = parentMap.get(n.id) || [];
          const upstreams: string[] = [];
          for (const pid of parents) {
            const out = nodeOutputs.get(pid);
            if (out) upstreams.push(out);
          }
          if (upstreams.length > 0) {
            nodeOutputs.set(n.id, upstreams.join('\n\n'));
          }
        } else if (n.type === 'reduce') {
          // Reduce: collect all upstream parallel outputs with labels
          const parents = parentMap.get(n.id) || [];
          const sections: string[] = [];
          for (const pid of parents) {
            const out = nodeOutputs.get(pid);
            if (out) {
              const label = nodeLabels.get(pid) || pid;
              sections.push(`## ${label}\n${out}`);
            }
          }
          if (sections.length > 0) {
            nodeOutputs.set(n.id, sections.join('\n\n'));
          }
        }

        emit(n.id, 'completed');
      }

      if (aiNodes.length === 0) continue;

      // --- Process AI nodes ---
      if (aiNodes.length === 1) {
        const node = aiNodes[0];
        const originalPrompt = node.data.config!.prompt as string;
        const agent = (node.data.config?.agent as string) || defaultAgent || undefined;
        const context = getUpstreamContext(node.id);
        const hasContext = context.length > 0;
        const { prompt: fullPrompt, parts: msgParts } = buildNodeMessage(node.id, originalPrompt, node.data.config);

        // Nodes with map/reduce context always get a fresh session
        if (hasContext || !primarySession) {
          const session = await sessionsApi.create({
            name: `WF: ${workflowName} — ${node.data.label || 'AI'}`,
            project_id: projectId,
            agent,
          });
          const latest = useAppStore.getState();
          latest.setSessions([session, ...latest.sessions]);
          latest.setActiveSessionId(session.id);
          window.dispatchEvent(new CustomEvent('session-updated'));

          if (!primarySession) primarySession = session;

          emit(node.id, 'running');
          globalWS.send(session.id, fullPrompt, msgParts, undefined, undefined);
          const result = await waitForDone(session.id, signal);
          if (result.status === 'stopped') {
            emit(node.id, 'failed');
            markRemaining(li + 1);
            return 'stopped';
          }
          emit(node.id, result.status === 'done' ? 'completed' : 'failed');
          if (result.output) nodeOutputs.set(node.id, result.output);
        } else {
          // Serial chain without map/reduce — reuse primary session
          emit(node.id, 'running');
          globalWS.send(primarySession.id, fullPrompt, msgParts, undefined, undefined);
          const result = await waitForDone(primarySession.id, signal);
          if (result.status === 'stopped') {
            emit(node.id, 'failed');
            markRemaining(li + 1);
            return 'stopped';
          }
          emit(node.id, result.status === 'done' ? 'completed' : 'failed');
          if (result.output) nodeOutputs.set(node.id, result.output);
        }
      } else {
        // Multiple AI nodes at same level → parallel, each gets own session
        const sessionsForNodes: { node: typeof aiNodes[0]; session: Session }[] = [];
        for (const node of aiNodes) {
          const agent = (node.data.config?.agent as string) || defaultAgent || undefined;
          const session = await sessionsApi.create({
            name: `WF: ${workflowName} — ${node.data.label || 'AI'}`,
            project_id: projectId,
            agent,
          });

          const latest = useAppStore.getState();
          latest.setSessions([session, ...latest.sessions]);
          window.dispatchEvent(new CustomEvent('session-updated'));

          if (!primarySession) {
            primarySession = session;
            latest.setActiveSessionId(session.id);
          }

          sessionsForNodes.push({ node, session });
        }

        let stopped = false;
        const promises = sessionsForNodes.map(async ({ node, session }) => {
          const originalPrompt = node.data.config!.prompt as string;
          const { prompt: fullPrompt, parts: msgParts } = buildNodeMessage(node.id, originalPrompt, node.data.config);

          emit(node.id, 'running');
          globalWS.send(session.id, fullPrompt, msgParts, undefined, undefined);
          const result = await waitForDone(session.id, signal);
          if (result.status === 'stopped') {
            stopped = true;
            emit(node.id, 'failed');
          } else {
            emit(node.id, result.status === 'done' ? 'completed' : 'failed');
            if (result.output) nodeOutputs.set(node.id, result.output);
          }
        });

        await Promise.all(promises);
        if (stopped) {
          markRemaining(li + 1);
          return 'stopped';
        }
      }
    }

    return 'completed';
  } finally {
    window.removeEventListener('workflow-stop', onWorkflowStop);
  }
}
