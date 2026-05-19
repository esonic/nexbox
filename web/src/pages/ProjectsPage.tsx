import { useState, useEffect } from "react";
import { toast } from "sonner";
import {
  Plus,
  FolderOpen,
  X,
  Loader2,
  Trash2,
} from "lucide-react";
import { projectsApi } from "@/lib/api";
import { useAppStore } from "@/stores/app";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import type { Project } from "@/types";

const PROJECT_NAME_RE = /^[A-Za-z0-9_-]{1,64}$/;
const PROJECT_NAME_ERROR = "Project name must be 1-64 chars from A-Z, a-z, 0-9, _, -";

// ── New Project dialog ────────────────────────────────────────────────────────

function NewProjectDialog({
  onClose,
  onCreate,
}: {
  onClose: () => void;
  onCreate: (p: Project) => void;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [nameError, setNameError] = useState<string | null>(null);

  const handleNameChange = (val: string) => {
    setName(val);
    if (val.trim() === "") {
      setNameError(null);
    } else if (!PROJECT_NAME_RE.test(val.trim())) {
      setNameError(PROJECT_NAME_ERROR);
    } else {
      setNameError(null);
    }
  };

  const handleSubmit = async () => {
    if (!name.trim()) return;
    if (!PROJECT_NAME_RE.test(name.trim())) {
      setNameError(PROJECT_NAME_ERROR);
      return;
    }
    setSubmitting(true);
    setError("");
    try {
      const p = await projectsApi.create({
        name: name.trim(),
        description: description.trim(),
      });
      onCreate(p);
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to create project");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-background border rounded-lg shadow-xl w-[420px] flex flex-col">
        <div className="flex items-center justify-between px-4 py-3 border-b">
          <h3 className="text-sm font-semibold">New Project</h3>
          <button onClick={onClose} className="p-1 hover:bg-muted rounded"><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-4">
          <div className="space-y-1.5">
            <label className="text-xs text-muted-foreground">Name <span className="text-destructive">*</span></label>
            <Input
              value={name}
              onChange={(e) => handleNameChange(e.target.value)}
              placeholder="my-project"
              className={`h-8 text-sm font-mono${nameError ? " border-destructive" : ""}`}
              autoFocus
            />
            {nameError && <p className="text-xs text-destructive">{nameError}</p>}
          </div>
          <div className="space-y-1.5">
            <label className="text-xs text-muted-foreground">Description</label>
            <Textarea value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional…" className="text-sm min-h-[56px] resize-none" />
          </div>
          {error && <p className="text-xs text-destructive">{error}</p>}
        </div>
        <div className="flex justify-end gap-2 px-4 pb-4">
          <Button variant="ghost" size="sm" onClick={onClose} className="h-7 text-xs">Cancel</Button>
          <Button size="sm" onClick={handleSubmit} disabled={!name.trim() || !!nameError || submitting} className="h-7 text-xs">
            {submitting && <Loader2 className="h-3 w-3 animate-spin mr-1" />}
            Create
          </Button>
        </div>
      </div>
    </div>
  );
}

// ── Delete Project confirmation dialog ─────────────────────────────────────────

function DeleteProjectDialog({
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

// ── ProjectsPage ──────────────────────────────────────────────────────────────

export function ProjectsPage() {
  const { projects, setProjects } = useAppStore();

  const [loading, setLoading] = useState(true);
  const [showNew, setShowNew] = useState(false);
  const [actioning, setActioning] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Project | null>(null);

  useEffect(() => {
    const load = async () => {
      try {
        const data = await projectsApi.list();
        setProjects(data || []);
      } catch (e) {
        toast.error("Failed to load projects", { description: String(e) });
      } finally {
        setLoading(false);
      }
    };
    load();
  }, []);

  const handleCreated = (p: Project) => {
    if (projects.some((x) => x.id === p.id)) {
      setProjects(projects.map((x) => (x.id === p.id ? p : x)));
    } else {
      setProjects([p, ...projects]);
    }
    toast.success(`Project "${p.name}" created`);
  };

  const handleDelete = async (project: Project) => {
    setActioning(project.id);
    try {
      await projectsApi.delete(project.id);
      setProjects(projects.filter((p) => p.id !== project.id));
      toast.success(`Project "${project.name}" deleted`);
    } catch (e) {
      toast.error("Failed to delete project", { description: String(e) });
    } finally {
      setActioning(null);
      setDeleteTarget(null);
    }
  };

  // Filter out the system "__unassigned__" project that may exist server-side.
  const allProjects = projects.filter((p) => p.name !== "__unassigned__");

  return (
    <div className="flex flex-col h-full overflow-y-auto">
      {/* Header */}
      <div className="flex items-center justify-between px-6 py-4 border-b shrink-0">
        <div className="w-full max-w-3xl mx-auto flex items-center justify-between">
          <div>
            <h1 className="text-lg font-semibold">Projects</h1>
            <p className="text-xs text-muted-foreground mt-0.5">Local projects</p>
          </div>
          <Button size="sm" onClick={() => setShowNew(true)} className="gap-1.5">
            <Plus className="h-3.5 w-3.5" />
            New Project
          </Button>
        </div>
      </div>

      {/* Project list */}
      <div className="flex-1 px-6 py-4">
        <div className="max-w-3xl mx-auto">
          {loading ? (
            <div className="flex items-center justify-center py-12">
              <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
            </div>
          ) : allProjects.length === 0 ? (
            <div className="text-center py-12 text-muted-foreground">
              <p className="text-sm">No projects yet.</p>
              <button onClick={() => setShowNew(true)} className="text-xs text-primary/70 hover:text-primary mt-2 underline-offset-2 hover:underline">
                Create your first project
              </button>
            </div>
          ) : (
            <div className="space-y-2">
              {allProjects.map((project) => {
                const isActioning = actioning === project.id;

                return (
                  <div
                    key={project.id}
                    className="flex items-start gap-4 p-4 border rounded-lg hover:bg-muted/20 transition-colors group"
                  >
                    <FolderOpen className="h-4 w-4 text-muted-foreground/60 mt-0.5 shrink-0" />
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-medium truncate">{project.name}</span>
                        <span className="text-[10px] font-mono text-muted-foreground/40 shrink-0">{project.id}</span>
                      </div>
                      {project.description && (
                        <p className="text-xs text-muted-foreground mt-0.5 truncate">{project.description}</p>
                      )}
                      {project.git_repo_path && (
                        <div className="flex flex-col gap-0.5 mt-1">
                          <p className="text-[11px] font-mono text-muted-foreground truncate">
                            <span className="text-muted-foreground/60 mr-1">Path</span>{project.git_repo_path}
                          </p>
                        </div>
                      )}
                    </div>

                    {/* Actions */}
                    <div className="flex items-center gap-1.5 shrink-0">
                      <button
                        onClick={() => setDeleteTarget(project)}
                        disabled={isActioning}
                        className="flex items-center gap-1 px-2 py-1 text-xs border rounded-md hover:bg-accent transition-colors text-muted-foreground hover:text-destructive disabled:opacity-50"
                        title="Delete project"
                      >
                        <Trash2 className="h-3 w-3" />
                        Delete
                      </button>
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </div>
      </div>

      {/* Dialogs */}
      {showNew && <NewProjectDialog onClose={() => setShowNew(false)} onCreate={handleCreated} />}
      {deleteTarget && (
        <DeleteProjectDialog
          project={deleteTarget}
          deleting={actioning === deleteTarget.id}
          onConfirm={() => handleDelete(deleteTarget)}
          onClose={() => setDeleteTarget(null)}
        />
      )}
    </div>
  );
}
