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
  FolderOpen,
  Loader2,
  CornerLeftUp,
  X,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { projectsApi, utilApi } from "@/lib/api";
import { useAppStore } from "@/stores/app";
import type { Project } from "@/types";

interface ProjectDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  project?: Project | null; // null/undefined = create mode, project = edit mode
  onSuccess?: (project: Project) => void;
  onDelete?: (projectId: string) => void;
  hideDelete?: boolean; // suppress delete/remove button (e.g. when opened from sidebar)
}

export function ProjectDialog({
  open,
  onOpenChange,
  project,
  onSuccess,
  onDelete,
  hideDelete = false,
}: ProjectDialogProps) {
  const isEdit = !!project;
  const isCloud = useAppStore((s) => s.connectStatus) === "connected";

  // Form state
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [gitRepoPath, setGitRepoPath] = useState("");
  const [namespaceId, setNamespaceId] = useState("");
  const [namespaceError, setNamespaceError] = useState("");

  // Directory browser
  const [browseDirs, setBrowseDirs] = useState<string[]>([]);
  const [browseCurrentPath, setBrowseCurrentPath] = useState("");
  const [browseLoading, setBrowseLoading] = useState(false);

  // Submit / delete state
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleting, setDeleting] = useState(false);

  // Join prompt: set when the chosen dir's git remote matches an existing project
  const [joinCandidate, setJoinCandidate] = useState<Project | null>(null);
  const [joining, setJoining] = useState(false);

  // Reset form when dialog opens or project changes
  useEffect(() => {
    if (open) {
      if (project) {
        setName(project.name);
        setDescription(project.description || "");
        setGitRepoPath(project.git_repo_path || "");
        setNamespaceId("");
      } else {
        setName("");
        setDescription("");
        setGitRepoPath("");
        setNamespaceId("");
      }
      setBrowseDirs([]);
      setBrowseCurrentPath("");
      setSubmitError("");
      setNamespaceError("");
      setConfirmDelete(false);
      setJoinCandidate(null);
    }
  }, [open, project]);

  // Browse directory
  const handleBrowseDir = async (path?: string) => {
    setBrowseLoading(true);
    try {
      const resp = await utilApi.browseDir(path);
      setBrowseDirs(resp.dirs?.map((d) => d.name) || []);
      setBrowseCurrentPath(resp.current || path || "");
    } catch (e) {
      console.error("Failed to list directories:", e);
      toast.error("Failed to browse directory", { description: String(e) });
    } finally {
      setBrowseLoading(false);
    }
  };

  const browseParent = browseCurrentPath
    ? browseCurrentPath.split("/").slice(0, -1).join("/") || "/"
    : null;

  // Submit
  const handleSubmit = async () => {
    if (!name.trim() || !gitRepoPath.trim()) return;
    if (isCloud && !isEdit) {
      if (!namespaceId.trim()) return;
      if (!/^[a-zA-Z0-9_]{3,10}$/.test(namespaceId.trim())) {
        setNamespaceError("3–10 characters: letters, numbers, underscore only");
        return;
      }
    }
    setSubmitting(true);
    setSubmitError("");
    try {
      let result: Project;
      if (isEdit && project) {
        result = await projectsApi.update(project.id, {
          name: name.trim(),
          description: description.trim(),
          git_repo_path: gitRepoPath.trim(),
        });
      } else {
        // Check if the chosen dir's git remote already belongs to an existing project
        if (isCloud) {
          try {
            const existing = await projectsApi.checkGitDir(gitRepoPath.trim());
            setJoinCandidate(existing);
            setSubmitting(false);
            return;
          } catch {
            // 404 = no match, proceed with creation
          }
        }
        result = await projectsApi.create({
          ...(isCloud && namespaceId.trim() ? { namespace_id: namespaceId.trim() } : {}),
          name: name.trim(),
          description: description.trim(),
          git_repo_path: gitRepoPath.trim(),
        });
      }
      onSuccess?.(result);
      onOpenChange(false);
    } catch (e: any) {
      const msg = e?.message || "Failed to save project";
      setSubmitError(msg);
    } finally {
      setSubmitting(false);
    }
  };

  // Handle joining an existing project instead of creating a new one.
  // We call create (not addToSidebar/PUT) so the Java backend's dedup logic runs:
  // it detects the same gitlab_repo_url, INSERT IGNORE-adds the user as member,
  // and returns the existing project — avoiding the 403 that would occur on PUT
  // (which requires prior membership).
  const handleJoin = async () => {
    if (!joinCandidate) return;
    setJoining(true);
    setSubmitError("");
    try {
      const updated = await projectsApi.create({
        name: joinCandidate.name,
        git_repo_path: gitRepoPath.trim(),
      });
      onSuccess?.(updated);
      onOpenChange(false);
    } catch (e: any) {
      setSubmitError(e?.message || "Failed to join project");
      setJoinCandidate(null);
    } finally {
      setJoining(false);
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
              onChange={(e) => setName(e.target.value)}
              placeholder="My Project"
              className="h-8 text-sm"
              autoFocus
            />
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

          {/* Namespace (cloud mode, create only) */}
          {isCloud && !isEdit && (
            <div className="space-y-1.5">
              <label htmlFor="project-namespace" className="text-xs text-muted-foreground">
                Namespace <span className="text-destructive">*</span>
              </label>
              <Input
                id="project-namespace"
                value={namespaceId}
                onChange={(e) => { setNamespaceId(e.target.value); setNamespaceError(""); }}
                placeholder="my_team"
                className={`h-8 text-sm font-mono${namespaceError ? " border-destructive" : ""}`}
              />
              {namespaceError && <p className="text-xs text-destructive">{namespaceError}</p>}
            </div>
          )}

          {/* Working Directory */}
          <div className="space-y-1.5">
            <label htmlFor="project-path" className="text-xs text-muted-foreground">
              Working Directory <span className="text-destructive">*</span>
            </label>
            <div className="flex items-center gap-1.5">
              <Input
                id="project-path"
                value={gitRepoPath}
                onChange={(e) => {
                  setGitRepoPath(e.target.value);
                  setBrowseCurrentPath("");
                }}
                placeholder="/path/to/project"
                className="h-8 text-sm font-mono flex-1 min-w-0"
              />
              <button
                type="button"
                onClick={() => handleBrowseDir(gitRepoPath.trim() || undefined)}
                className="h-8 px-2.5 rounded-md border bg-background text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors shrink-0 flex items-center gap-1"
                title="Browse directories"
              >
                <FolderOpen className="h-3.5 w-3.5" />
              </button>
            </div>

            {/* Directory browser */}
            {browseCurrentPath && (
              <div className="border rounded-md bg-background overflow-hidden mt-2">
                {/* Current path bar */}
                <div className="flex items-center gap-1.5 px-2.5 py-1.5 border-b bg-muted/30 min-w-0">
                  {browseParent && (
                    <button
                      type="button"
                      onClick={() => handleBrowseDir(browseParent)}
                      className="p-0.5 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors shrink-0"
                      title="Go to parent"
                    >
                      <CornerLeftUp className="h-3 w-3" />
                    </button>
                  )}
                  <span className="text-[11px] font-mono text-muted-foreground truncate flex-1">
                    {browseCurrentPath}
                  </span>
                  <button
                    type="button"
                    onClick={() => {
                      setBrowseCurrentPath("");
                      setBrowseDirs([]);
                    }}
                    className="p-0.5 rounded hover:bg-accent text-muted-foreground/40 hover:text-foreground transition-colors shrink-0"
                    title="Close browser"
                  >
                    <X className="h-3 w-3" />
                  </button>
                </div>

                {/* Directory list */}
                <div className="max-h-36 overflow-y-auto">
                  {browseLoading ? (
                    <div className="flex items-center justify-center py-3">
                      <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />
                    </div>
                  ) : browseDirs.length === 0 ? (
                    <div className="text-[11px] text-muted-foreground/60 text-center py-3">
                      No subdirectories
                    </div>
                  ) : (
                    browseDirs.map((dir) => {
                      const fullPath =
                        browseCurrentPath === "/"
                          ? `/${dir}`
                          : `${browseCurrentPath}/${dir}`;
                      return (
                        <div
                          key={dir}
                          className="flex items-center gap-1.5 px-2.5 py-1 hover:bg-accent/50 transition-colors group"
                        >
                          <button
                            type="button"
                            onClick={() => handleBrowseDir(fullPath)}
                            className="flex items-center gap-1.5 flex-1 min-w-0 text-left"
                          >
                            <FolderOpen className="h-3 w-3 text-muted-foreground/60 shrink-0" />
                            <span className="text-[11px] truncate">{dir}</span>
                          </button>
                          <button
                            type="button"
                            onClick={() => {
                              setGitRepoPath(fullPath);
                              setBrowseCurrentPath("");
                              setBrowseDirs([]);
                            }}
                            className="text-[10px] text-primary/70 hover:text-primary opacity-0 group-hover:opacity-100 transition-opacity shrink-0"
                          >
                            Select
                          </button>
                        </div>
                      );
                    })
                  )}
                </div>

                {/* Select current folder button */}
                <div className="border-t px-2.5 py-1.5 bg-muted/20">
                  <button
                    type="button"
                    onClick={() => {
                      setGitRepoPath(browseCurrentPath);
                      setBrowseCurrentPath("");
                      setBrowseDirs([]);
                    }}
                    className="text-[11px] text-primary/80 hover:text-primary transition-colors"
                  >
                    Select this folder
                  </button>
                </div>
              </div>
            )}
          </div>

        </div>

        {/* Join prompt — shown when existing project detected via git remote */}
        {joinCandidate && (
          <div className="rounded-lg border bg-muted/20 p-3 space-y-1.5">
            <p className="text-xs font-medium">Repository already belongs to a project</p>
            <p className="text-xs text-muted-foreground">
              <span className="font-medium text-foreground">{joinCandidate.name}</span>{" "}
              uses the same git remote. Add it to your sidebar instead of creating a new project?
            </p>
          </div>
        )}

        {/* Error message */}
        {submitError && (
          <p className="text-xs text-destructive px-1">{submitError}</p>
        )}

        {/* Actions */}
        <div className="flex items-center justify-between pt-2">
          {joinCandidate ? (
            <>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => { setJoinCandidate(null); setSubmitError(""); }}
                className="h-7 text-xs"
              >
                Back
              </Button>
              <Button
                size="sm"
                onClick={handleJoin}
                disabled={joining}
                className="h-7 text-xs"
              >
                {joining && <Loader2 className="h-3 w-3 animate-spin mr-1" />}
                Join Project
              </Button>
            </>
          ) : isEdit && confirmDelete ? (
            <div className="flex items-center gap-2 w-full justify-between">
              <span className="text-xs text-destructive">
                {isCloud ? "Remove this project from your workspace?" : "Delete project and related data?"}
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
                  {isCloud ? "Remove" : "Delete"}
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
                  disabled={!name.trim() || !gitRepoPath.trim() || (isCloud && !isEdit && !namespaceId.trim()) || submitting}
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
