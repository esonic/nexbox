import { memo } from 'react';
import { Handle, Position, NodeProps } from 'reactflow';
import { Sparkles } from 'lucide-react';

export default memo(({ data, selected }: NodeProps) => {
  return (
    <div
      className={`px-4 py-3 shadow-md rounded-lg bg-white border-2 min-w-[180px] ${
        selected ? 'border-purple-500 shadow-lg' : 'border-gray-300'
      }`}
    >
      <Handle type="target" position={Position.Top} className="!bg-purple-500" />

      <div className="flex items-center gap-2 mb-1">
        <Sparkles className="w-4 h-4 text-purple-500" />
        <div className="font-medium text-sm">{data.label || 'LLM'}</div>
      </div>

      {data.config?.model && (
        <div className="text-xs text-gray-500 mt-1">
          {data.config.model}
        </div>
      )}

      <Handle type="source" position={Position.Bottom} className="!bg-purple-500" />
    </div>
  );
});
