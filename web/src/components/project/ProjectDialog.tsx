import { useState, useEffect } from "react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Loader2,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { projectsApi } from "@/lib/api";
import type { Project } from "@/types";

interface ProjectDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  project?: Project | null; // null/undefined = create mode, project = edit mode
  onSuccess?: (project: Project) => void;
  onDelete?: (projectId: string) => void;
  hideDelete?: boolean;
  initialShowDelete?: boolean; // Open with delete confirmation visible
}

export function ProjectDialog({
  open,
  onOpenChange,
  project,
  onSuccess,
  onDelete,
  hideDelete = false,
  initialShowDelete = false,
}: ProjectDialogProps) {
  const isEdit = !!project;

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState("");

  // Delete confirmation
  const [confirmDeleteInput, setConfirmDeleteInput] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false);

  useEffect(() => {
    if (open) {
      if (project) {
        setName(project.name);
        setDescription(project.description || "");
      } else {
        setName("");
        setDescription("");
      }
      setSubmitError("");
      setShowDeleteConfirm(initialShowDelete);
      setConfirmDeleteInput("");
    }
  }, [open, project]);

  const handleSubmit = async () => {
    if (!name.trim()) return;
    setSubmitting(true);
    setSubmitError("");
    try {
      let result: Project;
      if (isEdit && project) {
        result = await projectsApi.update(project.id, {
          name: name.trim(),
          description: description.trim(),
        });
      } else {
        result = await projectsApi.create({
          name: name.trim(),
          description: description.trim(),
        });
      }
      onSuccess?.(result);
      onOpenChange(false);
    } catch (e: any) {
      setSubmitError(e?.message || "Failed to save project");
    } finally {
      setSubmitting(false);
    }
  };

  const handleDelete = async () => {
    if (!project) return;
    setDeleting(true);
    try {
      await projectsApi.delete(project.id);
      onDelete?.(project.id);
      onOpenChange(false);
    } catch (e) {
      console.error("Failed to delete project:", e);
      toast.error("Failed to delete project", { description: String(e) });
    } finally {
      setDeleting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="sm:max-w-[380px] overflow-hidden"
        onPointerDownOutside={(e) => e.preventDefault()}
        onInteractOutside={(e) => e.preventDefault()}
      >
        <DialogHeader>
          <DialogTitle>{isEdit ? "Edit Project" : "New Project"}</DialogTitle>
        </DialogHeader>

        <div className="space-y-4 py-2 min-w-0">
          {/* Name */}
          <div className="space-y-1.5">
            <label htmlFor="project-name" className="text-xs text-muted-foreground">
              Name <span className="text-destructive">*</span>
            </label>
            <Input
              id="project-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="My Project"
              className="h-8 text-sm"
              maxLength={20}
              autoFocus
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.nativeEvent.isComposing && name.trim()) {
                  e.preventDefault();
                  handleSubmit();
                }
              }}
            />
            <div className="text-[10px] text-muted-foreground/50 text-right">{name.length}/20</div>
          </div>

          {/* Description */}
          <div className="space-y-1.5">
            <label htmlFor="project-desc" className="text-xs text-muted-foreground">
              Description
            </label>
            <Textarea
              id="project-desc"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Optional description..."
              className="text-sm min-h-[60px] resize-none"
              maxLength={100}
            />
            <div className="text-[10px] text-muted-foreground/50 text-right">{description.length}/100</div>
          </div>
        </div>

        {submitError && (
          <p className="text-xs text-destructive px-1">{submitError}</p>
        )}

        {/* Delete confirmation */}
        {isEdit && showDeleteConfirm && (
          <div className="rounded-lg border border-destructive/30 bg-destructive/5 p-3 space-y-2">
            <p className="text-xs text-destructive font-medium">
              Type the project name to confirm deletion:
            </p>
            <Input
              value={confirmDeleteInput}
              onChange={(e) => setConfirmDeleteInput(e.target.value)}
              placeholder={project?.name}
              className="h-7 text-xs"
            />
            <div className="flex items-center gap-2 justify-end">
              <Button
                variant="ghost"
                size="sm"
                onClick={() => { setShowDeleteConfirm(false); setConfirmDeleteInput(""); }}
                className="h-6 text-xs"
              >
                Cancel
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={handleDelete}
                disabled={deleting || confirmDeleteInput.trim() !== project?.name}
                className="h-6 text-xs"
              >
                {deleting && <Loader2 className="h-3 w-3 animate-spin mr-1" />}
                Delete Project
              </Button>
            </div>
          </div>
        )}

        {/* Actions */}
        <div className="flex items-center justify-between pt-2">
          {isEdit && !hideDelete && !showDeleteConfirm ? (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setShowDeleteConfirm(true)}
              className="h-7 text-xs text-destructive hover:text-destructive"
            >
              <Trash2 className="h-3 w-3 mr-1" />
              Delete Project
            </Button>
          ) : (
            <div />
          )}
          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => onOpenChange(false)}
              className="h-7 text-xs"
            >
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={handleSubmit}
              disabled={!name.trim() || submitting}
              className="h-7 text-xs"
            >
              {submitting && <Loader2 className="h-3 w-3 animate-spin mr-1" />}
              {isEdit ? "Save" : "Create"}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
