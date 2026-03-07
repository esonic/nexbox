import { memo } from 'react';
import { Handle, Position, NodeProps } from 'reactflow';
import { GitMerge } from 'lucide-react';
import { NodeStatusBadge, getStatusRing } from './NodeStatusBadge';

export default memo(({ data, selected }: NodeProps) => {
  const statusRing = getStatusRing(data._status);

  return (
    <div className="relative">
      <NodeStatusBadge status={data._status} />
      <div
        className={`w-14 h-14 rotate-45 rounded-lg transition-shadow
          bg-gradient-to-br from-indigo-400 to-indigo-500 dark:from-indigo-500 dark:to-indigo-600
          shadow-md
          ${selected ? 'ring-2 ring-indigo-400 ring-offset-2 ring-offset-background shadow-lg' : statusRing}
        `}
      />
      <div className="absolute inset-0 flex items-center justify-center">
        <GitMerge className="w-5 h-5 text-white drop-shadow-sm" />
      </div>
      <div className="absolute -bottom-5 left-1/2 -translate-x-1/2 whitespace-nowrap text-[10px] font-medium text-muted-foreground">
        {data.label || 'Reduce'}
      </div>

      <Handle type="target" position={Position.Top} className="!bg-indigo-300 !border-indigo-600 !w-2.5 !h-2.5 !-top-[5px]" />
      <Handle type="source" position={Position.Bottom} className="!bg-indigo-300 !border-indigo-600 !w-2.5 !h-2.5 !-bottom-[5px]" />
    </div>
  );
});
