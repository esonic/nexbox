import { useState, useEffect, useRef } from "react";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import {
  Plus,
  FolderOpen,
  CornerLeftUp,
  X,
  Loader2,
  LogOut,
  Trash2,
  HardDriveDownload,
  Users,
  UserMinus,
  UserPlus,
} from "lucide-react";
import { projectsApi, utilApi, serverApi } from "@/lib/api";
import { useAppStore } from "@/stores/app";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import type { Project } from "@/types";

// ── Dir picker sub-component ─────────────────────────────────────────────────

function DirPicker({
  value,
  onChange,
}: {
  value: string;
  onChange: (v: string) => void;
}) {
  const [browseDirs, setBrowseDirs] = useState<string[]>([]);
  const [browsePath, setBrowsePath] = useState("");
  const [loading, setLoading] = useState(false);

  const browseParent = browsePath ? browsePath.split("/").slice(0, -1).join("/") || "/" : null;

  const browse = async (path?: string) => {
    setLoading(true);
    try {
      const resp = await utilApi.browseDir(path);
      setBrowseDirs(resp.dirs?.map((d: any) => d.name) || []);
      setBrowsePath(resp.current || path || "");
    } catch (e) {
      toast.error("Failed to browse directory", { description: String(e) });
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="space-y-1.5">
      <div className="flex items-center gap-1.5">
        <Input
          value={value}
          onChange={(e) => { onChange(e.target.value); setBrowsePath(""); }}
          placeholder="/path/to/directory"
          className="h-8 text-sm font-mono flex-1 min-w-0"
        />
        <button
          type="button"
          onClick={() => browse(value.trim() || undefined)}
          className="h-8 px-2.5 rounded-md border bg-background text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors shrink-0 flex items-center gap-1"
        >
          <FolderOpen className="h-3.5 w-3.5" />
        </button>
      </div>
      {browsePath && (
        <div className="border rounded-md bg-background overflow-hidden">
          <div className="flex items-center gap-1.5 px-2.5 py-1.5 border-b bg-muted/30">
            {browseParent && (
              <button type="button" onClick={() => browse(browseParent)} className="p-0.5 rounded hover:bg-accent text-muted-foreground shrink-0">
                <CornerLeftUp className="h-3 w-3" />
              </button>
            )}
            <span className="text-[11px] font-mono text-muted-foreground truncate flex-1">{browsePath}</span>
            <button type="button" onClick={() => setBrowsePath("")} className="p-0.5 rounded hover:bg-accent text-muted-foreground/40 shrink-0">
              <X className="h-3 w-3" />
            </button>
          </div>
          <div className="max-h-36 overflow-y-auto">
            {loading ? (
              <div className="flex items-center justify-center py-3">
                <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />
              </div>
            ) : browseDirs.length === 0 ? (
              <div className="text-[11px] text-muted-foreground/60 text-center py-3">No subdirectories</div>
            ) : (
              browseDirs.map((dir) => {
                const full = browsePath === "/" ? `/${dir}` : `${browsePath}/${dir}`;
                return (
                  <div key={dir} className="flex items-center gap-1.5 px-2.5 py-1 hover:bg-accent/50 group">
                    <button type="button" onClick={() => browse(full)} className="flex items-center gap-1.5 flex-1 min-w-0 text-left">
                      <FolderOpen className="h-3 w-3 text-muted-foreground/60 shrink-0" />
                      <span className="text-[11px] truncate">{dir}</span>
                    </button>
                    <button
                      type="button"
                      onClick={() => { onChange(full); setBrowsePath(""); }}
                      className="text-[10px] text-primary/70 hover:text-primary opacity-0 group-hover:opacity-100 transition-opacity shrink-0"
                    >
                      Select
                    </button>
                  </div>
                );
              })
            )}
          </div>
          <div className="border-t px-2.5 py-1.5 bg-muted/20">
            <button type="button" onClick={() => { onChange(browsePath); setBrowsePath(""); }} className="text-[11px] text-primary/80 hover:text-primary transition-colors">
              Select this folder
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

// ── New Project dialog ────────────────────────────────────────────────────────

function NewProjectDialog({
  onClose,
  onCreate,
}: {
  onClose: () => void;
  onCreate: (p: Project) => void;
}) {
  const isCloud = useAppStore((s) => s.connectStatus) === "connected";
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [localDir, setLocalDir] = useState("");
  const [namespaceId, setNamespaceId] = useState("");
  const [namespaceError, setNamespaceError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  // Join prompt: set when the chosen dir's git remote matches an existing project
  const [joinCandidate, setJoinCandidate] = useState<Project | null>(null);
  const [joining, setJoining] = useState(false);

  const handleSubmit = async () => {
    if (!name.trim() || !localDir.trim()) return;
    if (isCloud) {
      if (!namespaceId.trim()) return;
      if (!/^[a-zA-Z0-9_]{3,10}$/.test(namespaceId.trim())) {
        setNamespaceError("3–10 characters: letters, numbers, underscore only");
        return;
      }
    }
    setSubmitting(true);
    setError("");
    try {
      // In cloud mode: check if the local dir's git remote matches an existing project
      if (isCloud) {
        try {
          const existing = await projectsApi.checkGitDir(localDir.trim());
          // Found a matching project — prompt user to join instead
          setJoinCandidate(existing);
          setSubmitting(false);
          return;
        } catch {
          // 404 = no match, proceed with normal creation
        }
      }

      const p = await projectsApi.create({
        ...(isCloud && namespaceId.trim() ? { namespace_id: namespaceId.trim() } : {}),
        name: name.trim(),
        description: description.trim(),
        git_repo_path: localDir.trim(),
      });
      onCreate(p);
      onClose();
    } catch (e: any) {
      setError(e?.message || "Failed to create project");
    } finally {
      setSubmitting(false);
    }
  };

  const handleJoinConfirm = async () => {
    if (!joinCandidate) return;
    setJoining(true);
    try {
      // Use create so Java's dedup logic runs: detects same gitlab_repo_url,
      // INSERT IGNORE-adds the user as member, returns the existing project.
      // addToSidebar (PUT) would 403 because the user isn't a member yet.
      const updated = await projectsApi.create({
        name: joinCandidate.name,
        git_repo_path: localDir.trim(),
      });
      onCreate(updated);
      onClose();
    } catch (e: any) {
      setError(e?.message || "Failed to join project");
      setJoinCandidate(null);
    } finally {
      setJoining(false);
    }
  };

  // Join confirmation overlay
  if (joinCandidate) {
    return (
      <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
        <div className="bg-background border rounded-lg shadow-xl w-[420px] flex flex-col">
          <div className="flex items-center justify-between px-4 py-3 border-b">
            <h3 className="text-sm font-semibold">Project Already Exists</h3>
            <button onClick={onClose} className="p-1 hover:bg-muted rounded"><X className="h-4 w-4" /></button>
          </div>
          <div className="p-4 space-y-3">
            <p className="text-sm">
              This git repository belongs to an existing project:{" "}
              <span className="font-semibold">{joinCandidate.name}</span>
            </p>
            <p className="text-xs text-muted-foreground">Join it and use this as your local directory?</p>
            <p className="text-[11px] font-mono text-muted-foreground/70 truncate bg-muted/30 px-2 py-1 rounded">{localDir}</p>
            {error && <p className="text-xs text-destructive">{error}</p>}
          </div>
          <div className="flex justify-between gap-2 px-4 pb-4">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => { setJoinCandidate(null); setError(""); }}
              className="h-7 text-xs"
            >
              Cancel
            </Button>
            <Button size="sm" onClick={handleJoinConfirm} disabled={joining} className="h-7 text-xs">
              {joining && <Loader2 className="h-3 w-3 animate-spin mr-1" />}
              Join Project
            </Button>
          </div>
        </div>
      </div>
    );
  }

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
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="My Project" className="h-8 text-sm" autoFocus />
          </div>
          <div className="space-y-1.5">
            <label className="text-xs text-muted-foreground">Description</label>
            <Textarea value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional…" className="text-sm min-h-[56px] resize-none" />
          </div>
          {isCloud && (
            <div className="space-y-1.5">
              <label className="text-xs text-muted-foreground">Namespace <span className="text-destructive">*</span></label>
              <Input
                value={namespaceId}
                onChange={(e) => { setNamespaceId(e.target.value); setNamespaceError(""); }}
                placeholder="my_team"
                className={`h-8 text-sm font-mono${namespaceError ? " border-destructive" : ""}`}
              />
              {namespaceError && <p className="text-xs text-destructive">{namespaceError}</p>}
            </div>
          )}
          <div className="space-y-1.5">
            <label className="text-xs text-muted-foreground">Local Directory <span className="text-destructive">*</span></label>
            <DirPicker value={localDir} onChange={setLocalDir} />
          </div>
          {error && <p className="text-xs text-destructive">{error}</p>}
        </div>
        <div className="flex justify-end gap-2 px-4 pb-4">
          <Button variant="ghost" size="sm" onClick={onClose} className="h-7 text-xs">Cancel</Button>
          <Button size="sm" onClick={handleSubmit} disabled={!name.trim() || !localDir.trim() || (isCloud && !namespaceId.trim()) || submitting} className="h-7 text-xs">
            {submitting && <Loader2 className="h-3 w-3 animate-spin mr-1" />}
            Create
          </Button>
        </div>
      </div>
    </div>
  );
}

// ── Link to Local dialog ──────────────────────────────────────────────────────

function AddToSidebarDialog({
  project,
  onClose,
  onDone,
}: {
  project: Project;
  onClose: () => void;
  onDone: (p: Project) => void;
}) {
  const [localDir, setLocalDir] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  const handleSubmit = async () => {
    if (!localDir.trim()) return;
    setSubmitting(true);
    setError("");
    try {
      const updated = await projectsApi.addToSidebar(project.id, localDir.trim());
      onDone(updated);
      onClose();
    } catch (e: any) {
      setError(e?.message || "Failed to link local directory");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-background border rounded-lg shadow-xl w-[420px] flex flex-col">
        <div className="flex items-center justify-between px-4 py-3 border-b">
          <h3 className="text-sm font-semibold">Link to Local — {project.name}</h3>
          <button onClick={onClose} className="p-1 hover:bg-muted rounded"><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-4">
          <p className="text-xs text-muted-foreground">Choose a local directory to associate with this project. It will then appear in your sidebar.</p>
          <div className="space-y-1.5">
            <label className="text-xs text-muted-foreground">Local Directory <span className="text-destructive">*</span></label>
            <DirPicker value={localDir} onChange={setLocalDir} />
          </div>
          {error && <p className="text-xs text-destructive">{error}</p>}
        </div>
        <div className="flex justify-end gap-2 px-4 pb-4">
          <Button variant="ghost" size="sm" onClick={onClose} className="h-7 text-xs">Cancel</Button>
          <Button size="sm" onClick={handleSubmit} disabled={!localDir.trim() || submitting} className="h-7 text-xs">
            {submitting && <Loader2 className="h-3 w-3 animate-spin mr-1" />}
            Link
          </Button>
        </div>
      </div>
    </div>
  );
}

// ── Manage Members dialog ─────────────────────────────────────────────────────

function ManageMembersDialog({
  project,
  currentUser,
  onClose,
}: {
  project: Project;
  currentUser: string;
  onClose: () => void;
}) {
  const [members, setMembers] = useState<{ user_name: string; created_at?: string }[]>([]);
  const [loading, setLoading] = useState(true);
  const [newUser, setNewUser] = useState("");
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    projectsApi.listMembers(project.id)
      .then((data) => setMembers(data))
      .catch((e) => setError(e?.message || "Failed to load members"))
      .finally(() => setLoading(false));
  }, [project.id]);

  const handleAdd = async () => {
    const u = newUser.trim();
    if (!u) return;
    if (members.some((m) => m.user_name === u)) {
      setError(`${u} is already a member`);
      return;
    }
    setAdding(true);
    setError("");
    try {
      await projectsApi.addMember(project.id, u);
      setMembers((prev) => [...prev, { user_name: u }]);
      setNewUser("");
    } catch (e: any) {
      setError(e?.message || "Failed to add member");
    } finally {
      setAdding(false);
    }
  };

  const handleRemove = async (userName: string) => {
    setRemoving(userName);
    setError("");
    try {
      await projectsApi.removeMember(project.id, userName);
      setMembers((prev) => prev.filter((m) => m.user_name !== userName));
    } catch (e: any) {
      setError(e?.message || "Failed to remove member");
    } finally {
      setRemoving(null);
    }
  };

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-background border rounded-lg shadow-xl w-[420px] flex flex-col">
        <div className="flex items-center justify-between px-4 py-3 border-b">
          <h3 className="text-sm font-semibold">Members — {project.name}</h3>
          <button onClick={onClose} className="p-1 hover:bg-muted rounded"><X className="h-4 w-4" /></button>
        </div>

        {/* Add member */}
        <div className="px-4 pt-4 pb-2 space-y-1.5">
          <label className="text-xs text-muted-foreground">Add member</label>
          <div className="flex gap-2">
            <Input
              value={newUser}
              onChange={(e) => { setNewUser(e.target.value); setError(""); }}
              onKeyDown={(e) => e.key === "Enter" && !e.nativeEvent.isComposing && handleAdd()}
              placeholder="username"
              className="h-8 text-sm flex-1"
              autoFocus
            />
            <Button size="sm" onClick={handleAdd} disabled={!newUser.trim() || adding} className="h-8 gap-1 px-3">
              {adding ? <Loader2 className="h-3 w-3 animate-spin" /> : <UserPlus className="h-3.5 w-3.5" />}
            </Button>
          </div>
          {error && <p className="text-xs text-destructive">{error}</p>}
        </div>

        {/* Member list */}
        <div className="px-4 pb-4">
          {loading ? (
            <div className="flex justify-center py-4">
              <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
            </div>
          ) : members.length === 0 ? (
            <p className="text-xs text-muted-foreground/60 text-center py-4">No members</p>
          ) : (
            <div className="border rounded-md divide-y mt-2 max-h-60 overflow-y-auto">
              {members.map((m) => {
                const isOwner = m.user_name === project.created_by;
                return (
                  <div key={m.user_name} className="flex items-center justify-between px-3 py-2">
                    <div className="flex items-center gap-2 min-w-0">
                      <span className="text-sm truncate">{m.user_name}</span>
                      {isOwner && (
                        <span className="text-[10px] text-primary/60 bg-primary/10 px-1.5 py-0.5 rounded-full shrink-0">
                          owner
                        </span>
                      )}
                    </div>
                    {!isOwner && m.user_name !== currentUser && (
                      <button
                        onClick={() => handleRemove(m.user_name)}
                        disabled={removing === m.user_name}
                        className="p-1 rounded hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-colors disabled:opacity-50"
                        title="Remove member"
                      >
                        {removing === m.user_name
                          ? <Loader2 className="h-3.5 w-3.5 animate-spin" />
                          : <UserMinus className="h-3.5 w-3.5" />}
                      </button>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </div>

        <div className="flex justify-end px-4 pb-4">
          <Button variant="ghost" size="sm" onClick={onClose} className="h-7 text-xs">Close</Button>
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
            This will permanently delete all tasks, assets and member data in{" "}
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
  const navigate = useNavigate();
  const { connectStatus, projects, setProjects } = useAppStore();
  const isCloud = connectStatus === "connected";

  const [loading, setLoading] = useState(true);
  const [showNew, setShowNew] = useState(false);
  const [addToSidebar, setAddToSidebar] = useState<Project | null>(null);
  const [manageMembers, setManageMembers] = useState<Project | null>(null);
  const [confirmLeave, setConfirmLeave] = useState<Project | null>(null);
  const [actioning, setActioning] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Project | null>(null);

  // cloudInfo for current user (to detect ownership)
  const [currentUser, setCurrentUser] = useState("");

  useEffect(() => {
    const load = async () => {
      try {
        const [data] = await Promise.all([projectsApi.list()]);
        setProjects(data || []);
      } catch (e) {
        toast.error("Failed to load projects", { description: String(e) });
      } finally {
        setLoading(false);
      }
    };
    load();
  }, []);

  useEffect(() => {
    serverApi.info().then((info: any) => {
      setCurrentUser(info.user_name || "");
    }).catch(() => {});
  }, []);

  const handleCreated = (p: Project) => {
    if (projects.some((x) => x.id === p.id)) {
      // Already in list (join case): update in-place
      setProjects(projects.map((x) => (x.id === p.id ? p : x)));
    } else {
      setProjects([p, ...projects]);
    }
    toast.success(`Project "${p.name}" created`);
  };

  const handleAddedToSidebar = (updated: Project) => {
    setProjects(projects.map((p) => p.id === updated.id ? updated : p));
    toast.success("Linked to local directory");
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

  const handleLeave = async (project: Project) => {
    setActioning(project.id);
    try {
      await projectsApi.leave(project.id);
      setProjects(projects.filter((p) => p.id !== project.id));
      toast.success(`Left project "${project.name}"`);
    } catch (e) {
      toast.error("Failed to leave project", { description: String(e) });
    } finally {
      setActioning(null);
      setConfirmLeave(null);
    }
  };

  // Non-unassigned projects only
  const allProjects = projects.filter((p) => p.name !== "__unassigned__");

  return (
    <div className="flex flex-col h-full overflow-y-auto">
      {/* Header */}
      <div className="flex items-center justify-between px-6 py-4 border-b shrink-0">
        <div className="w-full max-w-3xl mx-auto flex items-center justify-between">
          <div>
            <h1 className="text-lg font-semibold">Projects</h1>
            <p className="text-xs text-muted-foreground mt-0.5">
              {isCloud ? "All projects you're a member of" : "Local projects"}
            </p>
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
              const isOwner = !isCloud || project.created_by === currentUser;
              const inSidebar = !!project.git_repo_path;
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
                      {inSidebar && (
                        <span className="text-[10px] text-primary/60 bg-primary/10 px-1.5 py-0.5 rounded-full shrink-0">
                          Linked
                        </span>
                      )}
                      <span className="text-[10px] font-mono text-muted-foreground/40 shrink-0">{project.id}</span>
                    </div>
                    {project.description && (
                      <p className="text-xs text-muted-foreground mt-0.5 truncate">{project.description}</p>
                    )}
                    {(project.git_repo_path || project.gitlab_repo_url) && (
                      <div className="flex flex-col gap-0.5 mt-1">
                        {project.git_repo_path && (
                          <p className="text-[11px] font-mono text-muted-foreground truncate">
                            <span className="text-muted-foreground/60 mr-1">Local</span>{project.git_repo_path}
                          </p>
                        )}
                        {project.gitlab_repo_url && (
                          <p className="text-[11px] font-mono text-muted-foreground truncate">
                            <span className="text-muted-foreground/60 mr-1">GitLab</span>
                            <a href={project.gitlab_repo_url} target="_blank" rel="noopener noreferrer" className="hover:text-foreground hover:underline underline-offset-2 transition-colors">
                              {project.gitlab_repo_url}
                            </a>
                          </p>
                        )}
                      </div>
                    )}
                    {isCloud && (project.created_by || project.namespace_id) && (
                      <p className="text-[11px] text-muted-foreground mt-1">
                        {project.namespace_id && <span>Namespace: {project.namespace_id}</span>}
                        {project.namespace_id && project.created_by && <span className="mx-1.5">·</span>}
                        {project.created_by && <span>Owner: {project.created_by}</span>}
                      </p>
                    )}
                  </div>

                  {/* Actions */}
                  <div className="flex items-center gap-1.5 shrink-0">
                    {/* Link to Local button */}
                    {!inSidebar && (
                      <button
                        onClick={() => setAddToSidebar(project)}
                        className="flex items-center gap-1 px-2 py-1 text-xs border rounded-md hover:bg-accent transition-colors text-muted-foreground hover:text-foreground"
                        title="Link a local directory"
                      >
                        <HardDriveDownload className="h-3 w-3" />
                        Link to Local
                      </button>
                    )}

                    {/* Members button (cloud, owner only) */}
                    {isCloud && isOwner && (
                      <button
                        onClick={() => setManageMembers(project)}
                        className="flex items-center gap-1 px-2 py-1 text-xs border rounded-md hover:bg-accent transition-colors text-muted-foreground hover:text-foreground"
                        title="Manage members"
                      >
                        <Users className="h-3 w-3" />
                        Members
                      </button>
                    )}

                    {/* Leave button (cloud, non-owner) */}
                    {isCloud && !isOwner && (
                      confirmLeave?.id === project.id ? (
                        <div className="flex items-center gap-1">
                          <button
                            onClick={() => handleLeave(project)}
                            disabled={isActioning}
                            className="px-2 py-1 text-xs bg-destructive text-destructive-foreground rounded-md hover:bg-destructive/90 disabled:opacity-50"
                          >
                            {isActioning ? <Loader2 className="h-3 w-3 animate-spin" /> : "Confirm"}
                          </button>
                          <button onClick={() => setConfirmLeave(null)} className="px-2 py-1 text-xs bg-muted rounded-md hover:bg-accent">
                            Cancel
                          </button>
                        </div>
                      ) : (
                        <button
                          onClick={() => setConfirmLeave(project)}
                          className="flex items-center gap-1 px-2 py-1 text-xs border rounded-md hover:bg-accent transition-colors text-muted-foreground hover:text-destructive"
                          title="Leave project"
                        >
                          <LogOut className="h-3 w-3" />
                          Leave
                        </button>
                      )
                    )}

                    {/* Delete button (owner only) */}
                    {isOwner && (
                      <button
                        onClick={() => setDeleteTarget(project)}
                        className="flex items-center gap-1 px-2 py-1 text-xs border rounded-md hover:bg-accent transition-colors text-muted-foreground hover:text-destructive"
                        title="Delete project"
                      >
                        <Trash2 className="h-3 w-3" />
                        Delete
                      </button>
                    )}
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
      {addToSidebar && (
        <AddToSidebarDialog
          project={addToSidebar}
          onClose={() => setAddToSidebar(null)}
          onDone={handleAddedToSidebar}
        />
      )}
      {manageMembers && (
        <ManageMembersDialog
          project={manageMembers}
          currentUser={currentUser}
          onClose={() => setManageMembers(null)}
        />
      )}
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
