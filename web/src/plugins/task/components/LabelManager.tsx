import { useState, useEffect } from "react";
import { toast } from "sonner";
import { Plus, Trash2, X, Check } from "lucide-react";
import { labelsApi } from "@/lib/api";
import type { TaskLabel } from "@/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

const LABEL_NAME_RE = /^[a-zA-Z0-9_-]{2,10}$/;

interface LabelManagerProps {
  projectId: string;
  onClose: () => void;
}

export function LabelManager({ projectId, onClose }: LabelManagerProps) {
  const [labels, setLabels] = useState<TaskLabel[]>([]);
  const [loading, setLoading] = useState(true);
  const [showCreate, setShowCreate] = useState(false);
  const [name, setName] = useState("");
  const [nameError, setNameError] = useState("");

  const loadLabels = async () => {
    try {
      const data = await labelsApi.list(projectId);
      setLabels(data || []);
    } catch (e) {
      console.error("Failed to load labels:", e);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { loadLabels(); }, [projectId]);

  const handleCreate = async () => {
    const trimmed = name.trim();
    if (!LABEL_NAME_RE.test(trimmed)) {
      setNameError("2-10 chars, letters/digits/dash/underscore only");
      return;
    }
    setNameError("");
    try {
      await labelsApi.create(projectId, { name: trimmed });
      setName("");
      setShowCreate(false);
      loadLabels();
    } catch (e) {
      console.error("Failed to create label:", e);
      toast.error("Failed to create label", { description: String(e) });
    }
  };

  const handleDelete = async (id: string) => {
    try {
      await labelsApi.delete(projectId, id);
      loadLabels();
    } catch (e) {
      console.error("Failed to delete label:", e);
      toast.error("Failed to delete label", { description: String(e) });
    }
  };

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-background border rounded-lg shadow-xl w-[380px] max-h-[500px] flex flex-col">
        <div className="flex items-center justify-between px-4 py-3 border-b">
          <h3 className="text-sm font-semibold">Manage Labels</h3>
          <button onClick={onClose} className="p-1 hover:bg-muted rounded">
            <X className="h-4 w-4" />
          </button>
        </div>

        <div className="flex-1 overflow-y-auto p-4 space-y-2">
          {loading ? (
            <div className="text-sm text-muted-foreground text-center py-4">Loading...</div>
          ) : labels.length === 0 && !showCreate ? (
            <div className="text-sm text-muted-foreground text-center py-4">No labels yet</div>
          ) : (
            labels.map((label) => (
              <div
                key={label.id}
                className="flex items-center gap-2 px-2 py-1.5 rounded-md hover:bg-muted/50 group"
              >
                <span className="text-sm flex-1 truncate">{label.name}</span>
                <button
                  onClick={() => handleDelete(label.id)}
                  className="hidden group-hover:block p-1 rounded hover:bg-destructive/20 text-destructive"
                >
                  <Trash2 className="h-3 w-3" />
                </button>
              </div>
            ))
          )}

          {showCreate && (
            <div className="border rounded-md p-3 space-y-2">
              <Input
                value={name}
                onChange={(e) => { setName(e.target.value); setNameError(""); }}
                placeholder="Label name (e.g. bug, feature)"
                className="h-8 text-sm"
                autoFocus
                onKeyDown={(e) => {
                  if (e.key === "Enter") handleCreate();
                  if (e.key === "Escape") { setShowCreate(false); setName(""); }
                }}
              />
              {nameError && (
                <p className="text-xs text-destructive">{nameError}</p>
              )}
              <p className="text-[11px] text-muted-foreground">
                Letters, digits, dash, underscore only. 2-10 characters.
              </p>
              <div className="flex justify-end gap-1.5">
                <Button size="sm" variant="ghost" onClick={() => { setShowCreate(false); setName(""); }}>Cancel</Button>
                <Button size="sm" onClick={handleCreate} disabled={!name.trim()}>
                  <Check className="h-3.5 w-3.5 mr-1" /> Create
                </Button>
              </div>
            </div>
          )}
        </div>

        {!showCreate && (
          <div className="px-4 py-3 border-t">
            <Button
              size="sm"
              variant="outline"
              className="w-full gap-1.5"
              onClick={() => { setShowCreate(true); setName(""); setNameError(""); }}
            >
              <Plus className="h-3.5 w-3.5" /> Create label
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}
