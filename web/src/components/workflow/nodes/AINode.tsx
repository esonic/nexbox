import { memo } from 'react';
import { Handle, Position, NodeProps } from 'reactflow';
import { Sparkles } from 'lucide-react';
import { NodeStatusBadge, getStatusRing } from './NodeStatusBadge';

export default memo(({ data, selected }: NodeProps) => {
  const agent = data.config?.agent;
  const prompt = data.config?.prompt;
  const taskCount = data.config?.taskRef?.length || 0;
  const assetCount = data.config?.assetRef?.length || 0;
  const statusRing = getStatusRing(data._status);

  return (
    <div className="relative">
      <NodeStatusBadge status={data._status} />
      <div
        className={`rounded-xl min-w-[200px] max-w-[260px] transition-shadow
          bg-card border shadow-md
          ${selected ? 'ring-2 ring-violet-500 ring-offset-2 ring-offset-background shadow-lg border-violet-500/50' : statusRing || 'border-border/60'}
        `}
      >
        <Handle type="target" position={Position.Top} className="!bg-violet-500 !border-violet-300 !w-2.5 !h-2.5" />

        {/* Header */}
        <div className="flex items-center gap-2 px-3 py-2.5 bg-violet-500/10 dark:bg-violet-500/15 border-b border-border/30 rounded-t-xl">
          <div className="w-6 h-6 rounded-md bg-violet-500/20 flex items-center justify-center shrink-0">
            <Sparkles className="w-3.5 h-3.5 text-violet-500" />
          </div>
          <span className="font-semibold text-sm truncate">{data.label || 'AI'}</span>
        </div>

        {/* Body */}
        <div className="px-3 py-2 space-y-1.5 text-[11px] text-muted-foreground">
          {agent && (
            <div className="flex items-center gap-1.5">
              <span className="text-muted-foreground/60">Agent:</span>
              <span className="font-medium text-foreground/80 truncate">{agent}</span>
            </div>
          )}
          {prompt && (
            <div className="text-muted-foreground/70">
              ✓ Prompt configured
            </div>
          )}
          {(taskCount > 0 || assetCount > 0) && (
            <div className="flex items-center gap-2 pt-0.5">
              {taskCount > 0 && (
                <span className="px-1.5 py-0.5 rounded bg-blue-500/10 text-blue-500 text-[10px] font-medium">
                  {taskCount} task{taskCount > 1 ? 's' : ''}
                </span>
              )}
              {assetCount > 0 && (
                <span className="px-1.5 py-0.5 rounded bg-emerald-500/10 text-emerald-500 text-[10px] font-medium">
                  {assetCount} asset{assetCount > 1 ? 's' : ''}
                </span>
              )}
            </div>
          )}
          {!agent && !prompt && taskCount === 0 && assetCount === 0 && (
            <span className="text-muted-foreground/40">Not configured</span>
          )}
        </div>

        <Handle type="source" position={Position.Bottom} className="!bg-violet-500 !border-violet-300 !w-2.5 !h-2.5" />
      </div>
    </div>
  );
});
