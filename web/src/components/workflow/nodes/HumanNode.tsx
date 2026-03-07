import { memo } from 'react';
import { Handle, Position, NodeProps } from 'reactflow';
import { User } from 'lucide-react';
import { NodeStatusBadge, getStatusRing } from './NodeStatusBadge';

export default memo(({ data, selected }: NodeProps) => {
  const assignee = data.config?.assignee;
  const description = data.config?.description;
  const statusRing = getStatusRing(data._status);

  return (
    <div className="relative">
      <NodeStatusBadge status={data._status} />
      <div
        className={`rounded-xl min-w-[200px] max-w-[260px] transition-shadow
          bg-card border shadow-md
          ${selected ? 'ring-2 ring-sky-500 ring-offset-2 ring-offset-background shadow-lg border-sky-500/50' : statusRing || 'border-border/60'}
        `}
      >
        <Handle type="target" position={Position.Top} className="!bg-sky-500 !border-sky-300 !w-2.5 !h-2.5" />

        {/* Header */}
        <div className="flex items-center gap-2 px-3 py-2.5 bg-sky-500/10 dark:bg-sky-500/15 border-b border-border/30 rounded-t-xl">
          <div className="w-6 h-6 rounded-md bg-sky-500/20 flex items-center justify-center shrink-0">
            <User className="w-3.5 h-3.5 text-sky-500" />
          </div>
          <span className="font-semibold text-sm truncate">{data.label || 'Human'}</span>
        </div>

        {/* Body */}
        <div className="px-3 py-2 space-y-1.5 text-[11px] text-muted-foreground">
          {assignee && (
            <div className="flex items-center gap-1.5">
              <span className="text-muted-foreground/60">Assignee:</span>
              <span className="font-medium text-foreground/80">{assignee}</span>
            </div>
          )}
          {description && (
            <div className="line-clamp-2 text-muted-foreground/70 leading-relaxed">
              {description}
            </div>
          )}
          {!assignee && !description && (
            <span className="text-muted-foreground/40">Not configured</span>
          )}
        </div>

        <Handle type="source" position={Position.Bottom} className="!bg-sky-500 !border-sky-300 !w-2.5 !h-2.5" />
      </div>
    </div>
  );
});
