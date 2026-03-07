import { useState, useEffect } from "react";
import { toast } from "sonner";
import { Check, X } from "lucide-react";
import { labelsApi, tasksApi } from "@/lib/api";
import type { TaskLabel } from "@/types";

interface LabelPickerProps {
  projectId: string;
  taskId: string;
  selectedLabels: string[];
  onUpdate: () => void;
  onClose: () => void;
}

export function LabelPicker({
  projectId,
  taskId,
  selectedLabels,
  onUpdate,
  onClose,
}: LabelPickerProps) {
  const [allLabels, setAllLabels] = useState<TaskLabel[]>([]);
  const [search, setSearch] = useState("");
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    labelsApi.list(projectId).then((data) => {
      setAllLabels(data || []);
      setLoading(false);
    }).catch(() => setLoading(false));
  }, [projectId]);

  const toggleLabel = async (label: TaskLabel) => {
    const isSelected = selectedLabels.includes(label.name);
    try {
      if (isSelected) {
        await tasksApi.removeLabel(taskId, label.name);
      } else {
        if (selectedLabels.length >= 5) {
          toast.error("Max 5 labels per task");
          return;
        }
        await tasksApi.addLabel(taskId, label.name);
      }
      onUpdate();
    } catch (e) {
      console.error("Failed to toggle label:", e);
      toast.error("Failed to toggle label", { description: String(e) });
    }
  };

  const filteredLabels = allLabels.filter((label) =>
    label.name.toLowerCase().includes(search.toLowerCase())
  );

  return (
    <div className="absolute left-0 top-full mt-1 z-50 w-64 bg-popover border rounded-lg shadow-lg overflow-hidden">
      <div className="flex items-center justify-between px-3 py-2 border-b">
        <span className="text-xs font-medium">Labels</span>
        <button onClick={onClose} className="p-0.5 hover:bg-muted rounded">
          <X className="h-3.5 w-3.5" />
        </button>
      </div>
      <div className="p-2">
        <input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Filter labels..."
          className="w-full text-xs bg-muted/50 border rounded px-2 py-1 outline-none focus:ring-1 focus:ring-ring"
          autoFocus
        />
      </div>
      <div className="max-h-[200px] overflow-y-auto">
        {loading ? (
          <div className="text-xs text-muted-foreground text-center py-4">Loading...</div>
        ) : filteredLabels.length === 0 ? (
          <div className="text-xs text-muted-foreground text-center py-4">No labels</div>
        ) : (
          filteredLabels.map((label) => {
            const isSelected = selectedLabels.includes(label.name);
            return (
              <button
                key={label.id}
                onClick={() => toggleLabel(label)}
                className="w-full flex items-center gap-2 px-3 py-1.5 hover:bg-accent text-left transition-colors"
              >
                <span className="text-xs flex-1 truncate">{label.name}</span>
                {isSelected && <Check className="h-3.5 w-3.5 text-primary shrink-0" />}
              </button>
            );
          })
        )}
      </div>
    </div>
  );
}

export function LabelBadge({ name }: { name: string }) {
  return (
    <span className="inline-flex items-center px-1.5 py-0.5 rounded-full text-[10px] font-medium bg-muted text-muted-foreground border">
      {name}
    </span>
  );
}
