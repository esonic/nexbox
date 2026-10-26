import { useState } from "react";
import { X, Loader2 } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import type { Project } from "@/types";

export function DeleteProjectDialog({
  project,
  deleting,
  onConfirm,
  onClose,
}: {
  project: Project;
  deleting: boolean;
  onConfirm: () => void;
  onClose: () => void;
}) {
  const [typed, setTyped] = useState("");
  const matches = typed === project.name;

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-background border rounded-lg shadow-xl w-[420px] flex flex-col">
        <div className="flex items-center justify-between px-4 py-3 border-b">
          <h3 className="text-sm font-semibold text-destructive">Delete Project</h3>
          <button onClick={onClose} className="p-1 hover:bg-muted rounded"><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3">
          <p className="text-sm">
            This will permanently delete all tasks, assets and data in{" "}
            <span className="font-semibold">{project.name}</span>.
          </p>
          <p className="text-xs text-muted-foreground">
            Type <span className="font-mono font-semibold text-foreground">{project.name}</span> to confirm:
          </p>
          <Input
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            placeholder={project.name}
            className="h-8 text-sm font-mono"
            autoFocus
          />
        </div>
        <div className="flex justify-end gap-2 px-4 pb-4">
          <Button variant="ghost" size="sm" onClick={onClose} disabled={deleting} className="h-7 text-xs">Cancel</Button>
          <Button
            variant="destructive"
            size="sm"
            onClick={onConfirm}
            disabled={!matches || deleting}
            className="h-7 text-xs"
          >
            {deleting && <Loader2 className="h-3 w-3 animate-spin mr-1" />}
            Delete Project
          </Button>
        </div>
      </div>
    </div>
  );
}
