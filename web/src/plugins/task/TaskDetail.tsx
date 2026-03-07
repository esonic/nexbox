import React, { useState, useEffect, useMemo, useRef } from "react";
import { toast } from "sonner";
import {
  ArrowLeft,
  Plus,
  X,
  Loader2,
  Pencil,
  FolderGit2,
  FileText,
  FileCode,
  TestTube,
  ClipboardList,
  Eye,
  Save,
  MessageSquare,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { tasksApi, projectsApi, assetsApi } from "@/lib/api";
import { cn, formatRelativeDate } from "@/lib/utils";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import type { Task, Asset, Project, TaskComment } from "@/types";
import { ReactionPicker } from "./components/ReactionPicker";
import { LabelPicker, LabelBadge } from "./components/LabelPicker";
import { UserPicker, UserDisplay } from "./components/UserPicker";
import { MentionTextarea } from "./components/MentionTextarea";

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

const ASSET_TYPE_ICONS: Record<string, typeof FileText> = {
  requirement_doc: ClipboardList,
  design_doc: FileText,
  code_change: FileCode,
  unit_test: TestTube,
  test_case: TestTube,
};

const ASSET_TYPE_LABELS: Record<string, string> = {
  requirement_doc: "Requirements",
  design_doc: "Design Docs",
  code_change: "Code Changes",
  unit_test: "Unit Tests",
  test_case: "Test Cases",
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
  const [linkedAssets, setLinkedAssets] = useState<Asset[]>([]);
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

  const [showLinkDialog, setShowLinkDialog] = useState(false);
  const [projects, setProjects] = useState<Project[]>([]);
  const [selectedProjectId, setSelectedProjectId] = useState("");
  const [projectAssets, setProjectAssets] = useState<Asset[]>([]);
  const [loadingAssets, setLoadingAssets] = useState(false);
  const [assetsHasMore, setAssetsHasMore] = useState(false);
  const [assetsOffset, setAssetsOffset] = useState(0);
  const [linkError, setLinkError] = useState("");
  const [linking, setLinking] = useState(false);

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
      const [t, assets, cmts, projList] = await Promise.all([
        tasksApi.get(taskId!),
        tasksApi.listAssets(taskId!),
        tasksApi.listComments(taskId!),
        projectsApi.list(),
      ]);
      setTask(t);
      setLinkedAssets(assets || []);
      setComments(cmts || []);
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

  const [projectNames, setProjectNames] = useState<Record<string, string>>({});
  const fetchedNamesRef = useRef<Set<string>>(new Set());

  const groupedByProject = useMemo(() => {
    const groups: Record<string, { projectName: string; assets: Asset[] }> = {};
    for (const asset of linkedAssets) {
      const pid = asset.project_id;
      if (!groups[pid]) {
        groups[pid] = { projectName: projectNames[pid] || pid, assets: [] };
      }
      groups[pid].assets.push(asset);
    }
    return groups;
  }, [linkedAssets, projectNames]);

  useEffect(() => {
    const pids = [...new Set(linkedAssets.map((a) => a.project_id))];
    for (const pid of pids) {
      if (!fetchedNamesRef.current.has(pid)) {
        fetchedNamesRef.current.add(pid);
        projectsApi.get(pid).then((p) => {
          setProjectNames((prev) => ({ ...prev, [pid]: p.name }));
        }).catch(() => {});
      }
    }
  }, [linkedAssets]);

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

  const handleProjectChange = async (projectId: string) => {
    if (!taskId) return;
    try {
      const updated = await tasksApi.update(taskId, { project_id: projectId });
      setTask(updated);
    } catch (e) {
      console.error("Failed to update project:", e);
      toast.error("Failed to update project", { description: String(e) });
    }
  };

  const handleUnlinkAsset = async (assetId: string) => {
    if (!taskId) return;
    try {
      await tasksApi.unlinkAsset(taskId, assetId);
      setLinkedAssets((prev) => prev.filter((a) => a.id !== assetId));
    } catch (e) {
      console.error("Failed to unlink asset:", e);
      toast.error("Failed to unlink asset", { description: String(e) });
    }
  };

  const handleOpenLinkDialog = async () => {
    setShowLinkDialog(true);
    setSelectedProjectId("");
    setProjectAssets([]);
    setLinkError("");
    try {
      const p = await projectsApi.list();
      setProjects(p || []);
    } catch (e) {
      console.error("Failed to load projects:", e);
      toast.error("Failed to load projects", { description: String(e) });
    }
  };

  const handleSelectProject = async (projectId: string) => {
    setSelectedProjectId(projectId);
    setLoadingAssets(true);
    setAssetsOffset(0);
    try {
      const data = await assetsApi.list(projectId, 20, 0);
      setProjectAssets(data || []);
      setAssetsHasMore((data || []).length === 20);
      setAssetsOffset((data || []).length);
    } catch (e) {
      console.error("Failed to load assets:", e);
      toast.error("Failed to load assets", { description: String(e) });
    } finally {
      setLoadingAssets(false);
    }
  };

  const handleLoadMoreDialogAssets = async () => {
    if (!selectedProjectId || loadingAssets) return;
    setLoadingAssets(true);
    try {
      const data = await assetsApi.list(selectedProjectId, 20, assetsOffset);
      setProjectAssets((prev) => [...prev, ...(data || [])]);
      setAssetsHasMore((data || []).length === 20);
      setAssetsOffset((prev) => prev + (data || []).length);
    } catch (e) {
      console.error("Failed to load more assets:", e);
      toast.error("Failed to load assets", { description: String(e) });
    } finally {
      setLoadingAssets(false);
    }
  };

  const handleLinkAsset = async (asset: Asset) => {
    if (!taskId || linking) return;
    setLinking(true);
    setLinkError("");
    try {
      await tasksApi.linkAsset(taskId, asset.id);
      setLinkedAssets((prev) => [...prev, asset]);
      setShowLinkDialog(false);
    } catch (e: any) {
      const msg = e?.message || String(e);
      setLinkError(`Failed to link "${asset.title}": ${msg}`);
      console.error("Failed to link asset:", e);
    } finally {
      setLinking(false);
    }
  };

  const dialogAssetsGrouped = useMemo(() => {
    const alreadyLinked = new Set(linkedAssets.map((a) => a.id));
    const available = projectAssets.filter((a) => !alreadyLinked.has(a.id));
    const grouped: Record<string, Asset[]> = {};
    for (const a of available) {
      if (!grouped[a.type]) grouped[a.type] = [];
      grouped[a.type].push(a);
    }
    return grouped;
  }, [projectAssets, linkedAssets]);

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

        {/* Title */}
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
                <td className="px-4 py-1.5">
                  <select
                    value={task.project_id || ""}
                    onChange={(e) => handleProjectChange(e.target.value)}
                    className="text-sm bg-transparent outline-none cursor-pointer"
                  >
                    <option value="">None</option>
                    {projects.map((p) => (
                      <option key={p.id} value={p.id}>{p.name}</option>
                    ))}
                  </select>
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
                <div className="markdown-content text-sm">
                  <ReactMarkdown remarkPlugins={[remarkGfm]}>
                    {task.description}
                  </ReactMarkdown>
                </div>
              ) : (
                <p className="text-sm text-muted-foreground/50 italic">No description</p>
              )}
            </div>
          )}
          <div className="mt-2">
            <ReactionPicker targetType="task" targetId={taskId} />
          </div>
        </div>

        {/* Linked Assets */}
        <div className="mb-5">
          <div className="flex items-center justify-between mb-2">
            <h2 className="text-sm font-semibold">Linked Assets</h2>
            <Button variant="outline" size="sm" className="h-7 gap-1 text-xs" onClick={handleOpenLinkDialog}>
              <Plus className="h-3 w-3" />
              Link Asset
            </Button>
          </div>

          {linkedAssets.length === 0 ? (
            <div className="text-xs text-muted-foreground border border-dashed rounded-lg py-4 text-center">
              No assets linked.{" "}
              <button onClick={handleOpenLinkDialog} className="text-primary hover:underline">
                Link one
              </button>
            </div>
          ) : (
            <div className="space-y-3">
              {Object.entries(groupedByProject).map(([projectId, group]) => (
                <div key={projectId}>
                  <div className="flex items-center gap-1.5 text-xs text-muted-foreground mb-1">
                    <FolderGit2 className="h-3 w-3" />
                    <span>{group.projectName !== projectId ? group.projectName : projectId.slice(0, 8)}</span>
                  </div>
                  <div className="space-y-1">
                    {group.assets.map((asset) => {
                      const Icon = ASSET_TYPE_ICONS[asset.type] || FileText;
                      return (
                        <div
                          key={asset.id}
                          className="flex items-center gap-2 px-3 py-2 border rounded-md hover:bg-accent/50 transition-colors group"
                        >
                          <Icon className="h-4 w-4 text-muted-foreground shrink-0" />
                          <div className="flex-1 min-w-0">
                            <span className="text-sm font-medium truncate block">{asset.title}</span>
                            <span className="text-[11px] text-muted-foreground">
                              {ASSET_TYPE_LABELS[asset.type] || asset.type} · v{asset.current_version} · {asset.status}
                            </span>
                          </div>
                          <button
                            onClick={() => handleUnlinkAsset(asset.id)}
                            className="p-1 rounded opacity-0 group-hover:opacity-100 hover:bg-destructive/20 text-muted-foreground hover:text-destructive transition-all"
                            title="Unlink"
                          >
                            <X className="h-3.5 w-3.5" />
                          </button>
                        </div>
                      );
                    })}
                  </div>
                </div>
              ))}
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
                  <div className="markdown-content text-sm">
                    <ReactMarkdown remarkPlugins={[remarkGfm]} components={mentionComponents}>
                      {comment.body}
                    </ReactMarkdown>
                  </div>
                </div>
                <div className="px-4 pb-2.5">
                  <ReactionPicker targetType="task_comment" targetId={comment.id} />
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

        {/* Link Asset Dialog */}
        <Dialog open={showLinkDialog} onOpenChange={setShowLinkDialog}>
          <DialogContent className="sm:max-w-lg">
            <DialogHeader>
              <DialogTitle>Link Asset to Task</DialogTitle>
            </DialogHeader>
            <div className="space-y-3">
              <div>
                <label className="text-sm font-medium">Project</label>
                <select
                  className="w-full border rounded-md px-3 py-2 text-sm mt-1 bg-background"
                  value={selectedProjectId}
                  onChange={(e) => handleSelectProject(e.target.value)}
                >
                  <option value="">Select a project...</option>
                  {projects.map((p) => (
                    <option key={p.id} value={p.id}>{p.name}</option>
                  ))}
                </select>
              </div>

              {linkError && (
                <div className="text-sm text-destructive bg-destructive/10 border border-destructive/20 rounded-md px-3 py-2">
                  {linkError}
                </div>
              )}

              {linking && (
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  Linking...
                </div>
              )}

              {selectedProjectId && (
                <div className="border rounded-md max-h-[300px] overflow-y-auto">
                  {loadingAssets && projectAssets.length === 0 ? (
                    <div className="flex items-center justify-center py-6">
                      <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                    </div>
                  ) : Object.keys(dialogAssetsGrouped).length === 0 ? (
                    <div className="text-xs text-muted-foreground text-center py-6">
                      No available assets in this project
                    </div>
                  ) : (
                    Object.entries(dialogAssetsGrouped).map(([type, assets]) => {
                      const Icon = ASSET_TYPE_ICONS[type] || FileText;
                      return (
                        <div key={type}>
                          <div className="sticky top-0 bg-muted/80 backdrop-blur-sm px-3 py-1 text-xs font-medium text-muted-foreground flex items-center gap-1.5 border-b">
                            <Icon className="h-3 w-3" />
                            {ASSET_TYPE_LABELS[type] || type} ({assets.length})
                          </div>
                          {assets.map((asset) => (
                            <button
                              key={asset.id}
                              onClick={() => handleLinkAsset(asset)}
                              disabled={linking}
                              className={cn(
                                "w-full text-left px-3 py-2 hover:bg-accent transition-colors border-b last:border-b-0",
                                linking && "opacity-50 cursor-not-allowed"
                              )}
                            >
                              <div className="text-sm font-medium">{asset.title}</div>
                              <div className="text-xs text-muted-foreground">
                                v{asset.current_version} · {asset.status}
                              </div>
                            </button>
                          ))}
                        </div>
                      );
                    })
                  )}
                  {assetsHasMore && (
                    <button
                      onClick={handleLoadMoreDialogAssets}
                      disabled={loadingAssets}
                      className="w-full text-center text-xs text-muted-foreground py-2 hover:bg-accent transition-colors border-t"
                    >
                      {loadingAssets ? <Loader2 className="h-3 w-3 animate-spin inline" /> : "Load more..."}
                    </button>
                  )}
                </div>
              )}
            </div>
          </DialogContent>
        </Dialog>
      </div>
    </div>
  );
}
