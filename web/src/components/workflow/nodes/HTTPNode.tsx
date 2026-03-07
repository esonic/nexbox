import { memo } from 'react';
import { Handle, Position, NodeProps } from 'reactflow';
import { Globe } from 'lucide-react';

export default memo(({ data, selected }: NodeProps) => {
  return (
    <div
      className={`px-4 py-3 shadow-md rounded-lg bg-white border-2 min-w-[180px] ${
        selected ? 'border-blue-500 shadow-lg' : 'border-gray-300'
      }`}
    >
      <Handle type="target" position={Position.Top} className="!bg-blue-500" />

      <div className="flex items-center gap-2 mb-1">
        <Globe className="w-4 h-4 text-blue-500" />
        <div className="font-medium text-sm">{data.label || 'HTTP'}</div>
      </div>

      {data.config?.method && (
        <div className="text-xs text-gray-500 mt-1">
          {data.config.method} {data.config.url ? data.config.url : ''}
        </div>
      )}

      <Handle type="source" position={Position.Bottom} className="!bg-blue-500" />
    </div>
  );
});
