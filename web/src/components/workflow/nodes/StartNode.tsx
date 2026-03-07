import { memo } from 'react';
import { Handle, Position, NodeProps } from 'reactflow';
import { Play } from 'lucide-react';
import { NodeStatusBadge, getStatusRing } from './NodeStatusBadge';

export default memo(({ data, selected }: NodeProps) => {
  const statusRing = getStatusRing(data._status);

  return (
    <div className="relative">
      <NodeStatusBadge status={data._status} />
      <div
        className={`px-5 py-3 rounded-xl min-w-[120px] text-center transition-shadow
          bg-gradient-to-br from-emerald-500 to-emerald-600 dark:from-emerald-600 dark:to-emerald-700
          text-white shadow-md
          ${selected ? 'ring-2 ring-emerald-400 ring-offset-2 ring-offset-background shadow-lg' : statusRing}
        `}
      >
        <div className="flex items-center justify-center gap-2">
          <Play className="w-3.5 h-3.5" fill="currentColor" />
          <span className="font-semibold text-sm">{data.label || 'Start'}</span>
        </div>
        <Handle type="source" position={Position.Bottom} className="!bg-emerald-300 !border-emerald-600 !w-2.5 !h-2.5" />
      </div>
    </div>
  );
});
