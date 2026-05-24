import { useState, useEffect } from "react";
import { toast } from "sonner";
import {
  ArrowLeft,
  Plus,
  Loader2,
  Pencil,
  Eye,
  Save,
  Trash2,
  AtSign,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { tasksApi, projectsApi } from "@/lib/api";
import { cn } from "@/lib/utils";
import { MarkdownRenderer } from "@/components/markdown/MarkdownRenderer";
import type { Task, Project } from "@/types";
import { LabelPicker, LabelBadge } from "./components/LabelPicker";
import { UserDisplay } from "./components/UserPicker";

function formatTimestamp(dateStr: string): string {
  const d = new Date(dateStr);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

const STATUS_OPTIONS = [
  { value: "open", label: "Open" },
  { value: "closed", label: "Done" },
];

const PRIORITY_OPTIONS = [
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
];

const STATUS_COLORS: Record<string, string> = {
  open: "bg-blue-500/15 text-blue-500 border-blue-500/30",
  closed: "bg-emerald-500/15 text-emerald-500 border-emerald-500/30",
};

const PRIORITY_COLORS: Record<string, string> = {
  low: "bg-zinc-500/15 text-zinc-400 border-zinc-500/30",
  medium: "bg-blue-500/15 text-blue-500 border-blue-500/30",
  high: "bg-orange-500/15 text-orange-500 border-orange-500/30",
};

interface TaskDraft {
  editingTitle?: boolean;
  title?: string;
  descMode?: "edit" | "preview";
  description?: string;
}

const draftKey = (id: string) => `task-draft-${id}`;

const saveDraft = (id: string, draft: TaskDraft) =>
  localStorage.setItem(draftKey(id), JSON.stringify(draft));

const loadDraft = (id: string): TaskDraft | null => {
  try {
    const raw = localStorage.getItem(draftKey(id));
    return raw ? (JSON.parse(raw) as TaskDraft) : null;
  } catch {
    return null;
  }
};

const clearDraft = (id: string) => localStorage.removeItem(draftKey(id));

interface TaskDetailProps {
  taskId: string;
  onBack: () => void;
}

export function TaskDetail({ taskId, onBack }: TaskDetailProps) {
  const [task, setTask] = useState<Task | null>(null);
  const [loading, setLoading] = useState(true);

  const [editingTitle, setEditingTitle] = useState(false);
  const [editTitle, setEditTitle] = useState("");
  const [descMode, setDescMode] = useState<"preview" | "edit">("preview");
  const [editDesc, setEditDesc] = useState("");
  const [saving, setSaving] = useState(false);

  const [showLabelPicker, setShowLabelPicker] = useState(false);

  const [projects, setProjects] = useState<Project[]>([]);

  useEffect(() => {
    if (taskId) loadTask();
  }, [taskId]);

  const loadTask = async () => {
    setLoading(true);
    try {
      const [t, projList] = await Promise.all([
        tasksApi.get(taskId!),
        projectsApi.list(),
      ]);
      setTask(t);
      setProjects(projList || []);

      // Restore any in-progress draft
      const draft = loadDraft(taskId!);
      if (draft) {
        if (draft.title !== undefined) setEditTitle(draft.title);
        if (draft.editingTitle) setEditingTitle(true);
        if (draft.description !== undefined) setEditDesc(draft.description);
        if (draft.descMode) setDescMode(draft.descMode);
      } else {
        setEditTitle(t.title);
        setEditDesc(t.description);
      }
    } catch (e) {
      console.error("Failed to load task:", e);
    } finally {
      setLoading(false);
    }
  };

  const handleDelete = async () => {
    if (!taskId) return;
    if (!window.confirm("Delete this task? This cannot be undone.")) return;
    try {
      await tasksApi.delete(taskId);
      toast.success("Task deleted");
      onBack();
    } catch (e) {
      console.error("Failed to delete task:", e);
      toast.error("Failed to delete task", { description: String(e) });
    }
  };

  const handleLabelsUpdated = async () => {
    if (!taskId) return;
    const t = await tasksApi.get(taskId);
    setTask(t);
  };

  const handleSaveTitle = async () => {
    if (!taskId || !editTitle.trim()) return;
    setSaving(true);
    try {
      const updated = await tasksApi.update(taskId, { title: editTitle.trim() });
      setTask(updated);
      setEditingTitle(false);
      clearDraft(taskId);
    } catch (e) {
      console.error("Failed to update title:", e);
      toast.error("Failed to update title", { description: String(e) });
    } finally {
      setSaving(false);
    }
  };

  const handleSaveDesc = async () => {
    if (!taskId) return;
    setSaving(true);
    try {
      const updated = await tasksApi.update(taskId, { description: editDesc });
      setTask(updated);
      setDescMode("preview");
      clearDraft(taskId);
    } catch (e) {
      console.error("Failed to update description:", e);
      toast.error("Failed to update description", { description: String(e) });
    } finally {
      setSaving(false);
    }
  };

  const descHasChanges = editDesc !== (task?.description ?? "");

  // Persist draft whenever editing state changes
  useEffect(() => {
    if (!taskId) return;
    if (descMode === "edit" || editingTitle) {
      saveDraft(taskId, {
        ...(editingTitle && { editingTitle: true, title: editTitle }),
        ...(descMode === "edit" && { descMode: "edit", description: editDesc }),
      });
    }
  }, [editDesc, editTitle, descMode, editingTitle, taskId]);

  const handleStatusChange = async (status: string) => {
    if (!taskId) return;
    try {
      const updated = await tasksApi.update(taskId, { status });
      setTask(updated);
    } catch (e) {
      console.error("Failed to update status:", e);
      toast.error("Failed to update status", { description: String(e) });
    }
  };

  const handlePriorityChange = async (priority: string) => {
    if (!taskId) return;
    try {
      const updated = await tasksApi.update(taskId, { priority });
      setTask(updated);
    } catch (e) {
      console.error("Failed to update priority:", e);
      toast.error("Failed to update priority", { description: String(e) });
    }
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center h-full">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (!task) {
    return (
      <div className="flex items-center justify-center h-full text-muted-foreground">
        Task not found
      </div>
    );
  }

  const taskLabels = task.labels || [];

  return (
    <div className="h-full overflow-y-auto">
      <div className="max-w-3xl mx-auto px-6 py-4">
        {/* Back + Reference + Delete */}
        <div className="flex items-center justify-between mb-4">
          <button
            onClick={onBack}
            className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
          >
            <ArrowLeft className="h-4 w-4" />
            Back
          </button>
          <div className="flex items-center gap-1">
            <button
              onClick={() => {
                window.dispatchEvent(new CustomEvent("reference-task", {
                  detail: { id: task.id, title: task.title, status: task.status },
                }));
                toast.success("Referenced in chat");
              }}
              className="flex items-center gap-1 px-2 py-1 rounded text-xs text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
              title="Reference in chat"
            >
              <AtSign className="h-3.5 w-3.5" />
              Reference
            </button>
            <button
              onClick={handleDelete}
              className="flex items-center gap-1 px-2 py-1 rounded text-xs text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors"
              title="Delete task"
            >
              <Trash2 className="h-3.5 w-3.5" />
              Delete
            </button>
          </div>
        </div>

        {/* Type badge + Title */}
        <div className="mb-5">
          {editingTitle ? (
            <div className="flex items-center gap-2">
              <Input
                value={editTitle}
                onChange={(e) => setEditTitle(e.target.value)}
                className="text-xl font-bold"
                autoFocus
                onKeyDown={(e) => e.key === "Enter" && !e.nativeEvent.isComposing && handleSaveTitle()}
              />
              <Button size="sm" onClick={handleSaveTitle} disabled={saving}>Save</Button>
              <Button size="sm" variant="ghost" onClick={() => { setEditingTitle(false); setEditTitle(task.title); clearDraft(taskId); }}>Cancel</Button>
            </div>
          ) : (
            <h1
              className="text-2xl font-bold cursor-pointer hover:text-primary/80 group flex items-center gap-2"
              onClick={() => setEditingTitle(true)}
            >
              {task.title}
              <Pencil className="h-4 w-4 text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity" />
            </h1>
          )}
        </div>

        {/* Properties */}
        <div className="rounded-lg border mb-4">
          <table className="w-full text-sm">
            <tbody className="divide-y divide-border">
              <tr>
                <td className="px-4 py-2.5 text-muted-foreground w-28">Status</td>
                <td className="px-4 py-1.5">
                  <div className="flex gap-1 flex-wrap">
                    {STATUS_OPTIONS.map((s) => (
                      <button
                        key={s.value}
                        onClick={() => handleStatusChange(s.value)}
                        className={cn(
                          "px-2.5 py-1 text-xs rounded-md border transition-colors",
                          task.status === s.value
                            ? STATUS_COLORS[s.value]
                            : "border-transparent text-muted-foreground hover:bg-accent"
                        )}
                      >
                        {s.label}
                      </button>
                    ))}
                  </div>
                </td>
              </tr>
              <tr>
                <td className="px-4 py-2.5 text-muted-foreground">Priority</td>
                <td className="px-4 py-1.5">
                  <div className="flex gap-1 flex-wrap">
                    {PRIORITY_OPTIONS.map((p) => (
                      <button
                        key={p.value}
                        onClick={() => handlePriorityChange(p.value)}
                        className={cn(
                          "px-2.5 py-1 text-xs rounded-md border transition-colors",
                          task.priority === p.value
                            ? PRIORITY_COLORS[p.value]
                            : "border-transparent text-muted-foreground hover:bg-accent"
                        )}
                      >
                        {p.label}
                      </button>
                    ))}
                  </div>
                </td>
              </tr>
              {task.created_by && task.created_by !== "local" && (
                <tr>
                  <td className="px-4 py-2.5 text-muted-foreground">Creator</td>
                  <td className="px-4 py-1.5">
                    <UserDisplay userName={task.created_by} />
                  </td>
                </tr>
              )}
              <tr>
                <td className="px-4 py-2.5 text-muted-foreground">Project</td>
                <td className="px-4 py-1.5 text-sm">
                  {projects.find((p) => p.id === task.project_id)?.name || "None"}
                </td>
              </tr>
              <tr>
                <td className="px-4 py-2.5 text-muted-foreground">Labels</td>
                <td className="px-4 py-1.5">
                  <div className="relative flex items-center gap-1.5 flex-wrap">
                    {taskLabels.map((name) => (
                      <LabelBadge key={name} name={name} />
                    ))}
                    <button
                      onClick={() => setShowLabelPicker(!showLabelPicker)}
                      className="inline-flex items-center gap-1 px-2 py-0.5 text-xs text-muted-foreground hover:text-foreground hover:bg-accent rounded-md transition-colors"
                    >
                      <Plus className="h-3 w-3" />
                      {taskLabels.length === 0 && "Add label"}
                    </button>
                    {showLabelPicker && (
                      <LabelPicker
                        projectId={task?.project_id || ""}
                        taskId={taskId}
                        selectedLabels={taskLabels}
                        onUpdate={handleLabelsUpdated}
                        onClose={() => setShowLabelPicker(false)}
                      />
                    )}
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        {/* Metadata */}
        <div className="flex gap-6 flex-wrap text-[11px] text-muted-foreground/70 mb-5 px-1">
          <div>
            <span className="uppercase tracking-wider">Created</span>
            <div className="text-muted-foreground mt-0.5 tabular-nums">
              {formatTimestamp(task.created_at)}
              {task.created_by && task.created_by !== "local" && <span className="ml-1.5 opacity-70">by {task.created_by}</span>}
            </div>
          </div>
          <div>
            <span className="uppercase tracking-wider">Updated</span>
            <div className="text-muted-foreground mt-0.5 tabular-nums">{formatTimestamp(task.updated_at)}</div>
          </div>
          {task.closed_at && (
            <div>
              <span className="uppercase tracking-wider">Finished</span>
              <div className="text-muted-foreground mt-0.5 tabular-nums">{formatTimestamp(task.closed_at)}</div>
            </div>
          )}
        </div>

        {/* Description */}
        <div className="mb-5">
          <div className="flex items-center justify-between mb-2">
            <h2 className="text-sm font-semibold">Description</h2>
            <div className="flex items-center gap-1">
              <Button
                variant={descMode === "preview" ? "secondary" : "ghost"}
                size="sm"
                className="h-7 w-7 p-0"
                onClick={() => { setDescMode("preview"); clearDraft(taskId); }}
                title="Preview"
              >
                <Eye className="h-3.5 w-3.5" />
              </Button>
              <Button
                variant={descMode === "edit" ? "secondary" : "ghost"}
                size="sm"
                className="h-7 w-7 p-0"
                onClick={() => { setEditDesc(task.description); setDescMode("edit"); }}
                title="Edit"
              >
                <Pencil className="h-3.5 w-3.5" />
              </Button>
              {descMode === "edit" && (
                <Button
                  size="sm"
                  className="h-7 ml-1"
                  onClick={handleSaveDesc}
                  disabled={!descHasChanges || saving}
                >
                  <Save className="mr-1 h-3.5 w-3.5" />
                  {saving ? "Saving..." : "Save"}
                </Button>
              )}
            </div>
          </div>
          {descMode === "edit" ? (
            <textarea
              value={editDesc}
              onChange={(e) => setEditDesc(e.target.value)}
              className="w-full min-h-[160px] p-3 font-mono text-sm bg-transparent border rounded-md outline-none resize-y"
              placeholder="Write description in Markdown..."
              spellCheck={false}
              autoFocus
            />
          ) : (
            <div className="rounded-md border p-3 min-h-[40px]">
              {task.description ? (
                <MarkdownRenderer content={task.description} className="markdown-content text-sm" />
              ) : (
                <p className="text-sm text-muted-foreground/50 italic">No description</p>
              )}
            </div>
          )}
        </div>

      </div>
    </div>
  );
}
