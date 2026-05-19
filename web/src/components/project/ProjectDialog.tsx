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
import { Loader2, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { projectsApi } from "@/lib/api";
import type { Project } from "@/types";

interface ProjectDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  project?: Project | null; // null/undefined = create mode, project = edit mode
  onSuccess?: (project: Project) => void;
  onDelete?: (projectId: string) => void;
  hideDelete?: boolean; // suppress delete button (e.g. when opened from sidebar)
}

const PROJECT_NAME_RE = /^[A-Za-z0-9_-]{1,64}$/;
const PROJECT_NAME_ERROR = "Project name must be 1-64 chars from A-Z, a-z, 0-9, _, -";

export function ProjectDialog({
  open,
  onOpenChange,
  project,
  onSuccess,
  onDelete,
  hideDelete = false,
}: ProjectDialogProps) {
  const isEdit = !!project;

  // Form state
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [nameError, setNameError] = useState<string | null>(null);

  // Submit / delete state
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleting, setDeleting] = useState(false);

  // Reset form when dialog opens or project changes
  useEffect(() => {
    if (!open) return;
    if (project) {
      setName(project.name);
      setDescription(project.description || "");
    } else {
      setName("");
      setDescription("");
    }
    setNameError(null);
    setSubmitError("");
    setConfirmDelete(false);
  }, [open, project]);

  const validateName = (val: string): boolean => {
    if (!PROJECT_NAME_RE.test(val.trim())) {
      setNameError(PROJECT_NAME_ERROR);
      return false;
    }
    setNameError(null);
    return true;
  };

  const handleNameChange = (val: string) => {
    setName(val);
    // Live validation — clear the error as soon as the input becomes valid,
    // but don't surface it until the user types something invalid.
    if (val.trim() === "") {
      setNameError(null);
    } else if (!PROJECT_NAME_RE.test(val.trim())) {
      setNameError(PROJECT_NAME_ERROR);
    } else {
      setNameError(null);
    }
  };

  // Submit
  const handleSubmit = async () => {
    if (!name.trim()) return;
    if (!validateName(name)) return;
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
    } catch (e) {
      const msg = e instanceof Error ? e.message : "Failed to save project";
      setSubmitError(msg);
    } finally {
      setSubmitting(false);
    }
  };

  // Delete
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
        className="sm:max-w-[420px] overflow-hidden"
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
              onChange={(e) => handleNameChange(e.target.value)}
              placeholder="my-project"
              className={`h-8 text-sm font-mono${nameError ? " border-destructive" : ""}`}
              autoFocus
            />
            {nameError && <p className="text-xs text-destructive">{nameError}</p>}
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
            />
          </div>

          {/* Read-only: server-managed working directory */}
          {isEdit && project?.git_repo_path && (
            <div className="space-y-1.5">
              <label className="text-xs text-muted-foreground">Working Directory</label>
              <code className="block text-[11px] font-mono text-muted-foreground bg-muted px-2 py-1.5 rounded break-all">
                {project.git_repo_path}
              </code>
              <p className="text-[11px] text-muted-foreground/70">
                Platform-managed. Derived from your username and the project name.
              </p>
            </div>
          )}
        </div>

        {/* Error message */}
        {submitError && (
          <p className="text-xs text-destructive px-1">{submitError}</p>
        )}

        {/* Actions */}
        <div className="flex items-center justify-between pt-2">
          {isEdit && confirmDelete ? (
            <div className="flex items-center gap-2 w-full justify-between">
              <span className="text-xs text-destructive">
                Delete project and related data?
              </span>
              <div className="flex items-center gap-2 shrink-0">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setConfirmDelete(false)}
                  className="h-7 text-xs"
                >
                  Cancel
                </Button>
                <Button
                  variant="destructive"
                  size="sm"
                  onClick={handleDelete}
                  disabled={deleting}
                  className="h-7 text-xs"
                >
                  {deleting && <Loader2 className="h-3 w-3 animate-spin mr-1" />}
                  Confirm
                </Button>
              </div>
            </div>
          ) : (
            <>
              {isEdit && !hideDelete ? (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setConfirmDelete(true)}
                  className="h-7 text-xs text-destructive hover:text-destructive"
                >
                  <Trash2 className="h-3 w-3 mr-1" />
                  Delete
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
                  disabled={!name.trim() || !!nameError || submitting}
                  className="h-7 text-xs"
                >
                  {submitting && <Loader2 className="h-3 w-3 animate-spin mr-1" />}
                  {isEdit ? "Save" : "Create"}
                </Button>
              </div>
            </>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
