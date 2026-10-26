import { useState, useEffect } from "react";
import { toast } from "sonner";
import {
  Plus,
  FolderOpen,
  Loader2,
  Trash2,
} from "lucide-react";
import { projectsApi } from "@/lib/api";
import { useAppStore } from "@/stores/app";
import { Button } from "@/components/ui/button";
import { ProjectDialog } from "@/components/project/ProjectDialog";
import { DeleteProjectDialog } from "@/components/project/DeleteProjectDialog";
import type { Project } from "@/types";

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
      {showNew && <ProjectDialog open={showNew} onOpenChange={setShowNew} onSuccess={handleCreated} />}
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
