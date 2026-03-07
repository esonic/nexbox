import { memo } from 'react';
import { Handle, Position, NodeProps } from 'reactflow';
import { GitFork } from 'lucide-react';
import { NodeStatusBadge, getStatusRing } from './NodeStatusBadge';

export default memo(({ data, selected }: NodeProps) => {
  const statusRing = getStatusRing(data._status);

  return (
    <div className="relative">
      <NodeStatusBadge status={data._status} />
      <div
        className={`w-14 h-14 rotate-45 rounded-lg transition-shadow
          bg-gradient-to-br from-amber-400 to-amber-500 dark:from-amber-500 dark:to-amber-600
          shadow-md
          ${selected ? 'ring-2 ring-amber-400 ring-offset-2 ring-offset-background shadow-lg' : statusRing}
        `}
      />
      <div className="absolute inset-0 flex items-center justify-center">
        <GitFork className="w-5 h-5 text-white drop-shadow-sm" />
      </div>
      <div className="absolute -bottom-5 left-1/2 -translate-x-1/2 whitespace-nowrap text-[10px] font-medium text-muted-foreground">
        {data.label || 'Map'}
      </div>

      <Handle type="target" position={Position.Top} className="!bg-amber-300 !border-amber-600 !w-2.5 !h-2.5 !-top-[5px]" />
      <Handle type="source" position={Position.Bottom} className="!bg-amber-300 !border-amber-600 !w-2.5 !h-2.5 !-bottom-[5px]" />
    </div>
  );
});
