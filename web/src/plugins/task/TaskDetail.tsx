import React, { useState, useEffect } from "react";
import { toast } from "sonner";
import {
  ArrowLeft,
  Plus,
  X,
  Loader2,
  Pencil,
  Eye,
  Save,
  MessageSquare,
  Link2,
  Unlink,
  ChevronRight,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { tasksApi, projectsApi } from "@/lib/api";
import { cn, formatRelativeDate } from "@/lib/utils";
import { MarkdownRenderer } from "@/components/markdown/MarkdownRenderer";
import type { Task, Project, TaskComment } from "@/types";
import { LabelPicker, LabelBadge } from "./components/LabelPicker";
import { UserPicker, UserDisplay } from "./components/UserPicker";
import { MentionTextarea } from "./components/MentionTextarea";
import { TypeIssue, TypeTask } from "./task-icons";
import { shortId } from "./task-config";

function formatTimestamp(dateStr: string): string {
  const d = new Date(dateStr);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

const STATUS_OPTIONS = [
  { value: "open", label: "Open" },
  { value: "in_progress", label: "In Progress" },
  { value: "done", label: "Done" },
  { value: "closed", label: "Closed" },
];

const PRIORITY_OPTIONS = [
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "critical", label: "Critical" },
];

const STATUS_COLORS: Record<string, string> = {
  open: "bg-blue-500/15 text-blue-500 border-blue-500/30",
  in_progress: "bg-yellow-500/15 text-yellow-500 border-yellow-500/30",
  done: "bg-green-500/15 text-green-500 border-green-500/30",
  closed: "bg-zinc-500/15 text-zinc-400 border-zinc-500/30",
};

const PRIORITY_COLORS: Record<string, string> = {
  low: "bg-zinc-500/15 text-zinc-400 border-zinc-500/30",
  medium: "bg-blue-500/15 text-blue-500 border-blue-500/30",
  high: "bg-orange-500/15 text-orange-500 border-orange-500/30",
  critical: "bg-red-500/15 text-red-500 border-red-500/30",
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
  initialCommentId?: string;
}

export function TaskDetail({ taskId, onBack, initialCommentId }: TaskDetailProps) {
  const [task, setTask] = useState<Task | null>(null);
  const [loading, setLoading] = useState(true);

  const [editingTitle, setEditingTitle] = useState(false);
  const [editTitle, setEditTitle] = useState("");
  const [descMode, setDescMode] = useState<"preview" | "edit">("preview");
  const [editDesc, setEditDesc] = useState("");
  const [saving, setSaving] = useState(false);

  const [comments, setComments] = useState<TaskComment[]>([]);
  const [newComment, setNewComment] = useState("");
  const [postingComment, setPostingComment] = useState(false);

  const [showLabelPicker, setShowLabelPicker] = useState(false);

  const [parentTask, setParentTask] = useState<Task | null>(null);
  const [showParentPicker, setShowParentPicker] = useState(false);
  const [parentSearch, setParentSearch] = useState("");
  const [parentCandidates, setParentCandidates] = useState<Task[]>([]);
  const [loadingParents, setLoadingParents] = useState(false);

  const [projects, setProjects] = useState<Project[]>([]);

  useEffect(() => {
    if (taskId) loadTask();
  }, [taskId]);

  useEffect(() => {
    if (!initialCommentId || loading || comments.length === 0) return;
    const t = setTimeout(() => {
      document.getElementById(`task-comment-${initialCommentId}`)?.scrollIntoView({ behavior: "smooth", block: "center" });
    }, 100);
    return () => clearTimeout(t);
  }, [initialCommentId, loading, comments.length]);

  const loadTask = async () => {
    setLoading(true);
    try {
      const [t, cmts, projList] = await Promise.all([
        tasksApi.get(taskId!),
        tasksApi.listComments(taskId!),
        projectsApi.list(),
      ]);
      setTask(t);
      setComments(cmts || []);
      setProjects(projList || []);

      // Load parent task info if this task has a parent
      if (t.parent_id) {
        try {
          const parent = await tasksApi.get(t.parent_id);
          setParentTask(parent);
        } catch {
          setParentTask(null);
        }
      } else {
        setParentTask(null);
      }

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

  const handlePostComment = async () => {
    if (!newComment.trim() || !taskId) return;
    setPostingComment(true);
    try {
      const comment = await tasksApi.createComment(taskId, { body: newComment.trim() });
      setComments((prev) => [...prev, comment]);
      setNewComment("");
      const t = await tasksApi.get(taskId);
      setTask(t);
    } catch (e) {
      console.error("Failed to post comment:", e);
      toast.error("Failed to post comment", { description: String(e) });
    } finally {
      setPostingComment(false);
    }
  };

  const handleDeleteComment = async (commentId: string) => {
    try {
      await tasksApi.deleteComment(commentId);
      setComments((prev) => prev.filter((c) => c.id !== commentId));
      if (taskId) {
        const t = await tasksApi.get(taskId);
        setTask(t);
      }
    } catch (e) {
      console.error("Failed to delete comment:", e);
      toast.error("Failed to delete comment", { description: String(e) });
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

  // Render @username as inline UserDisplay inside markdown
  const MENTION_RE = /(@[\w.\-]+)/g;
  function injectMentions(children: React.ReactNode): React.ReactNode {
    if (typeof children === "string") {
      const parts = children.split(MENTION_RE);
      if (parts.length === 1) return children;
      return parts.map((part, i) =>
        part.startsWith("@")
          ? <UserDisplay key={i} userName={part.slice(1)} className="inline-flex align-middle mx-0.5" />
          : part
      );
    }
    if (Array.isArray(children)) {
      return (children as React.ReactNode[]).map((child, i) =>
        typeof child === "string"
          ? <React.Fragment key={i}>{injectMentions(child)}</React.Fragment>
          : child
      );
    }
    return children;
  }
  const mentionComponents = {
    p: ({ children }: { children?: React.ReactNode }) => <p>{injectMentions(children)}</p>,
    li: ({ children }: { children?: React.ReactNode }) => <li>{injectMentions(children)}</li>,
  };

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

  const handleAssignerChange = async (assigner: string) => {
    if (!taskId) return;
    try {
      const updated = await tasksApi.update(taskId, { assigner });
      setTask(updated);
    } catch (e) {
      console.error("Failed to update assigner:", e);
      toast.error("Failed to update assigner", { description: String(e) });
    }
  };


  const handleSearchParents = async (query: string) => {
    setParentSearch(query);
    if (!query.trim()) {
      setParentCandidates([]);
      return;
    }
    setLoadingParents(true);
    try {
      const all = await tasksApi.list(50, 0, task?.project_id || undefined);
      const filtered = (all || []).filter(
        (t) => t.id !== taskId && !t.parent_id && t.title.toLowerCase().includes(query.toLowerCase())
      );
      setParentCandidates(filtered);
    } catch {
      setParentCandidates([]);
    } finally {
      setLoadingParents(false);
    }
  };

  const handleChangeParent = async (parentId: string) => {
    if (!taskId) return;
    try {
      await tasksApi.setParent(taskId, parentId);
      const [t, parent] = await Promise.all([
        tasksApi.get(taskId),
        tasksApi.get(parentId),
      ]);
      setTask(t);
      setParentTask(parent);
      setShowParentPicker(false);
      setParentSearch("");
      setParentCandidates([]);
      toast.success("Parent updated");
    } catch (e) {
      console.error("Failed to set parent:", e);
      toast.error("Failed to set parent", { description: String(e) });
    }
  };

  const handleRemoveParent = async () => {
    if (!taskId) return;
    try {
      await tasksApi.removeParent(taskId);
      setParentTask(null);
      const t = await tasksApi.get(taskId);
      setTask(t);
      toast.success("Parent removed");
    } catch (e) {
      console.error("Failed to remove parent:", e);
      toast.error("Failed to remove parent", { description: String(e) });
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
        {/* Back */}
        <button
          onClick={onBack}
          className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground mb-4"
        >
          <ArrowLeft className="h-4 w-4" />
          Back
        </button>

        {/* Type badge + Title */}
        <div className="mb-5">
          {task.source === "gitlab" && (
            <div className="flex items-center gap-2 mb-2">
              <span className={cn(
                "inline-flex items-center gap-1.5 px-2 py-0.5 rounded text-xs font-medium",
                task.external_type === "Task"
                  ? "bg-purple-500/10 text-purple-500"
                  : "bg-green-500/10 text-green-500"
              )}>
                {task.external_type === "Task" ? <TypeTask className="h-3 w-3" /> : <TypeIssue className="h-3 w-3" />}
                {task.external_type || "Issue"}
              </span>
              {task.external_url && (
                <a
                  href={task.external_url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-xs text-muted-foreground hover:text-foreground transition-colors"
                >
                  View in GitLab →
                </a>
              )}
            </div>
          )}
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
              <tr>
                <td className="px-4 py-2.5 text-muted-foreground">Assignee</td>
                <td className="px-4 py-1.5">
                  <UserPicker
                    value={task.assigner}
                    onChange={handleAssignerChange}
                  />
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
              {/* Parent relationship */}
              <tr>
                <td className="px-4 py-2.5 text-muted-foreground">Parent</td>
                <td className="px-4 py-1.5">
                  {parentTask ? (
                    <div className="flex items-center gap-2">
                      <TypeIssue className="shrink-0 text-blue-500" />
                      <span
                        className="text-sm text-blue-500 truncate"
                        title={parentTask.title}
                      >
                        {shortId(parentTask.id)} {parentTask.title}
                      </span>
                      <button
                        onClick={() => setShowParentPicker(true)}
                        className="shrink-0 p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
                        title="Change parent"
                      >
                        <Pencil className="h-3 w-3" />
                      </button>
                      <button
                        onClick={handleRemoveParent}
                        className="shrink-0 p-1 rounded hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-colors"
                        title="Remove parent"
                      >
                        <Unlink className="h-3.5 w-3.5" />
                      </button>
                    </div>
                  ) : task.parent_id ? (
                    <div className="flex items-center gap-2">
                      <span className="text-sm text-muted-foreground">{shortId(task.parent_id)}</span>
                      <button
                        onClick={() => setShowParentPicker(true)}
                        className="shrink-0 p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
                        title="Change parent"
                      >
                        <Pencil className="h-3 w-3" />
                      </button>
                      <button
                        onClick={handleRemoveParent}
                        className="shrink-0 p-1 rounded hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-colors"
                        title="Remove parent"
                      >
                        <Unlink className="h-3.5 w-3.5" />
                      </button>
                    </div>
                  ) : (
                    <button
                      onClick={() => setShowParentPicker(true)}
                      className="inline-flex items-center gap-1 px-2 py-0.5 text-xs text-muted-foreground hover:text-foreground hover:bg-accent rounded-md transition-colors"
                    >
                      <Plus className="h-3 w-3" />
                      Set parent
                    </button>
                  )}
                  {showParentPicker && (
                    <div className="mt-2 border rounded-md bg-popover p-2 space-y-2">
                      <div className="flex items-center gap-2">
                        <input
                          value={parentSearch}
                          onChange={(e) => handleSearchParents(e.target.value)}
                          placeholder="Search for parent task..."
                          className="flex-1 text-xs bg-transparent border rounded px-2 py-1 focus:outline-none focus:ring-1 focus:ring-ring"
                          autoFocus
                        />
                        <button
                          onClick={() => { setShowParentPicker(false); setParentSearch(""); setParentCandidates([]); }}
                          className="p-1 rounded hover:bg-accent text-muted-foreground"
                        >
                          <X className="h-3.5 w-3.5" />
                        </button>
                      </div>
                      {loadingParents && (
                        <div className="flex justify-center py-2">
                          <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                        </div>
                      )}
                      {parentCandidates.length > 0 && (
                        <div className="max-h-[200px] overflow-y-auto divide-y divide-border/50">
                          {parentCandidates.map((candidate) => (
                            <button
                              key={candidate.id}
                              onClick={() => handleChangeParent(candidate.id)}
                              className="w-full flex items-center gap-2 px-2 py-1.5 text-left hover:bg-accent/50 rounded transition-colors"
                            >
                              <TypeIssue className="shrink-0 text-green-500" />
                              <span className="text-[11px] text-muted-foreground font-mono">{shortId(candidate.id)}</span>
                              <span className="text-xs truncate">{candidate.title}</span>
                            </button>
                          ))}
                        </div>
                      )}
                      {parentSearch && !loadingParents && parentCandidates.length === 0 && (
                        <p className="text-xs text-muted-foreground text-center py-2">No matching tasks found</p>
                      )}
                    </div>
                  )}
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        {/* Children */}
        {task.children && task.children.length > 0 && (
          <div className="rounded-lg border mb-4">
            <div className="px-4 py-2.5 border-b bg-muted/30 flex items-center gap-2">
              <Link2 className="h-3.5 w-3.5 text-muted-foreground" />
              <span className="text-sm font-medium">
                Child tasks
                <span className="ml-1.5 text-muted-foreground font-normal">({task.children.length})</span>
              </span>
            </div>
            <div className="divide-y divide-border/50">
              {task.children.map((child) => (
                <div
                  key={child.id}
                  className="flex items-center gap-2.5 px-4 py-2 hover:bg-accent/30 transition-colors cursor-pointer"
                  onClick={() => onBack()}
                >
                  <TypeTask className="shrink-0 text-muted-foreground" />
                  <span className="text-[11px] text-muted-foreground font-mono">{shortId(child.id)}</span>
                  <span className={cn(
                    "text-sm truncate flex-1",
                    child.status === "closed" ? "line-through text-muted-foreground" : "text-foreground"
                  )}>
                    {child.title}
                  </span>
                  <span className={cn(
                    "text-[10px] px-1.5 py-0.5 rounded",
                    child.status === "closed"
                      ? "bg-zinc-500/15 text-zinc-400"
                      : "bg-blue-500/15 text-blue-500"
                  )}>
                    {child.status === "closed" ? "Closed" : "Open"}
                  </span>
                  <ChevronRight className="h-3.5 w-3.5 text-muted-foreground/50" />
                </div>
              ))}
            </div>
          </div>
        )}

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

        {/* Divider */}
        <div className="border-t mb-5" />

        {/* Comments */}
        <div className="mb-6">
          <h2 className="text-sm font-semibold mb-3 flex items-center gap-1.5">
            <MessageSquare className="h-4 w-4" />
            Comments
            {comments.length > 0 && (
              <span className="text-muted-foreground font-normal">({comments.length})</span>
            )}
          </h2>

          <div className="space-y-3">
            {comments.map((comment) => (
              <div
                key={comment.id}
                id={`task-comment-${comment.id}`}
                className={cn("border rounded-lg transition-colors", initialCommentId === comment.id && "border-blue-500/50 bg-blue-500/5")}
              >
                <div className="border-b bg-muted/30 px-4 py-2 flex items-center justify-between">
                  <UserDisplay userName={comment.author} />
                  <div className="flex items-center gap-2">
                    <span className="text-[11px] text-muted-foreground">
                      {formatRelativeDate(comment.created_at)}
                    </span>
                    <button
                      onClick={() => handleDeleteComment(comment.id)}
                      className="p-0.5 rounded hover:bg-destructive/20 text-muted-foreground hover:text-destructive transition-colors"
                      title="Delete comment"
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </div>
                </div>
                <div className="px-4 py-3">
                  <MarkdownRenderer content={comment.body} className="markdown-content text-sm" components={mentionComponents} />
                </div>
              </div>
            ))}

            {/* New comment */}
            <div className="border rounded-lg">
              <div className="border-b bg-muted/30 px-4 py-2">
                <span className="text-xs font-medium">Add a comment</span>
              </div>
              <div className="p-4 space-y-3">
                <MentionTextarea
                  value={newComment}
                  onChange={setNewComment}
                  placeholder="Leave a comment (supports Markdown, @mention)"
                  className="w-full min-h-[80px] px-3 py-2 text-sm bg-transparent border rounded-md outline-none resize-y focus:ring-1 focus:ring-ring"
                />
                <Button
                  onClick={handlePostComment}
                  disabled={!newComment.trim() || postingComment}
                  size="sm"
                >
                  {postingComment ? (
                    <>
                      <Loader2 className="h-3 w-3 animate-spin mr-2" />
                      Posting...
                    </>
                  ) : (
                    "Comment"
                  )}
                </Button>
              </div>
            </div>
          </div>
        </div>

      </div>
    </div>
  );
}
