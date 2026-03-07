import { useState, useEffect, useMemo } from "react";
import { useParams, useNavigate } from "react-router-dom";
import {
  ArrowLeft,
  Package,
  Wrench,
  Loader2,
  Plus,
  Trash2,
  CheckCircle2,
  Pencil,
  GitBranch,
  MessageSquare,
  User,
  Calendar,
  Tag,
  Star,
  Download,
  X,
  ArrowUpCircle,
  RefreshCw,
} from "lucide-react";
import { toast } from "sonner";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { cn } from "@/lib/utils";
import {
  marketplaceApi,
  serverApi,
  PACKAGE_CATEGORIES,
  type MarketplacePackageDetail,
  type PackageReview,
  type InstalledMCP,
  type InstalledSkill,
  type PackageCategoryValue,
} from "@/lib/api";
import { useAppStore } from "@/stores/app";
import { UNASSIGNED_PROJECT } from "@/lib/constants";
import { InstallDialog, parseEnvVarDefs, type ProjectOption } from "./InstallDialog";

function formatManifest(manifestStr: string): string {
  try {
    return JSON.stringify(JSON.parse(manifestStr), null, 2);
  } catch {
    return manifestStr;
  }
}

export function PackageDetailPage() {
  const { packageId } = useParams<{ packageId: string }>();
  const navigate = useNavigate();
  const currentProject = useAppStore((s) => s.currentProject);
  const allProjects = useAppStore((s) => s.projects);

  const [detail, setDetail] = useState<MarketplacePackageDetail | null>(null);
  const [comments, setComments] = useState<PackageReview[]>([]);
  const [installedMCPs, setInstalledMCPs] = useState<InstalledMCP[]>([]);
  const [installedSkills, setInstalledSkills] = useState<InstalledSkill[]>([]);
  const [loading, setLoading] = useState(true);
  const [installing, setInstalling] = useState(false);
  const [uninstalling, setUninstalling] = useState(false);
  const [installDialogOpen, setInstallDialogOpen] = useState(false);
  const [starred, setStarred] = useState(false);
  const [starCount, setStarCount] = useState(0);
  const [starring, setStarring] = useState(false);
  const [pendingMCPDeps, setPendingMCPDeps] = useState<string[]>([]);

  const [currentUser, setCurrentUser] = useState("");

  // Edit mode
  const [editMode, setEditMode] = useState(false);
  const [editTitle, setEditTitle] = useState("");
  const [editDesc, setEditDesc] = useState("");
  const [editCategory, setEditCategory] = useState<PackageCategoryValue | null>(null);
  const [savingEdit, setSavingEdit] = useState(false);

  const [deletingPackage, setDeletingPackage] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const [commentBody, setCommentBody] = useState("");
  const [submittingComment, setSubmittingComment] = useState(false);
  const [deletingCommentId, setDeletingCommentId] = useState<string | null>(null);

  useEffect(() => {
    serverApi.info().then((info: any) => setCurrentUser(info.user_name || "")).catch(() => {});
  }, []);

  useEffect(() => {
    if (!packageId) return;
    const load = async () => {
      setLoading(true);
      try {
        const [d, cs, mcps, skills, starStatus] = await Promise.all([
          marketplaceApi.getPackage(packageId),
          marketplaceApi.listComments(packageId).catch(() => [] as PackageReview[]),
          marketplaceApi.listInstalledMCPs().catch(() => [] as InstalledMCP[]),
          marketplaceApi.listInstalledSkills().catch(() => [] as InstalledSkill[]),
          marketplaceApi.getStarStatus(packageId).catch(() => ({ starred: false, star_count: 0 })),
        ]);
        setDetail(d);
        setComments(cs);
        setInstalledMCPs(mcps || []);
        setInstalledSkills(skills || []);
        setStarred(starStatus.starred);
        setStarCount(starStatus.star_count ?? d.package.star_count ?? 0);
      } catch (e: any) {
        toast.error("Failed to load package", { description: e.message });
        navigate("/marketplace");
      } finally {
        setLoading(false);
      }
    };
    load();
  }, [packageId]);

  const skillMd = useMemo(() => {
    if (!detail) return "";
    const p = detail.package;
    const l = detail.versions.find((v) => v.version === p.latest_version) ?? detail.versions[0];
    if (p.type !== "skill" || !l?.manifest) return "";
    try {
      const raw: string = JSON.parse(l.manifest)?.skill_md ?? "";
      if (!raw) return "";

      // Parse YAML frontmatter (--- ... ---)
      const fmMatch = raw.match(/^---\n([\s\S]*?)\n---\n?/);
      if (!fmMatch) return raw;

      const fmLines = fmMatch[1].split("\n");
      const rows: string[] = [];
      for (const line of fmLines) {
        const colonIdx = line.indexOf(":");
        if (colonIdx < 0) continue;
        const key = line.slice(0, colonIdx).trim();
        const value = line.slice(colonIdx + 1).trim();
        if (key) rows.push(`| ${key} | ${value} |`);
      }

      const table =
        rows.length > 0
          ? `| Field | Value |\n| --- | --- |\n${rows.join("\n")}\n\n`
          : "";

      const body = raw.slice(fmMatch[0].length);
      return table + body;
    } catch {
      return "";
    }
  }, [detail]);

  if (loading) {
    return (
      <div className="h-full flex items-center justify-center text-muted-foreground gap-2">
        <Loader2 className="h-5 w-5 animate-spin" />
        Loading...
      </div>
    );
  }

  if (!detail) return null;

  const pkg = detail.package;
  const latest = detail.versions.find((v) => v.version === pkg.latest_version) ?? detail.versions[0];
  const isOwner = currentUser !== "" && currentUser === pkg.author_id;

  const handleStartEdit = () => {
    setEditTitle(pkg.title);
    setEditDesc(pkg.description || "");
    const firstTag = pkg.tags?.[0] as PackageCategoryValue | undefined;
    setEditCategory(firstTag ?? null);
    setEditMode(true);
  };

  const handleSaveEdit = async () => {
    setSavingEdit(true);
    try {
      await marketplaceApi.updatePackage(pkg.id, {
        title: editTitle.trim(),
        description: editDesc.trim(),
        tags: editCategory ? [editCategory] : [],
      });
      // Refresh detail
      const updated = await marketplaceApi.getPackage(pkg.id);
      setDetail(updated);
      setEditMode(false);
      toast.success("Package updated");
    } catch (e: any) {
      toast.error("Update failed", { description: e.message });
    } finally {
      setSavingEdit(false);
    }
  };

  const handleDeletePackage = async () => {
    setDeletingPackage(true);
    try {
      await marketplaceApi.deletePackage(pkg.id);
      toast.success(`"${pkg.title}" deleted`);
      navigate("/marketplace");
    } catch (e: any) {
      toast.error("Delete failed", { description: e.message });
      setConfirmDelete(false);
    } finally {
      setDeletingPackage(false);
    }
  };

  const installedMCP = installedMCPs.find((m) => m.name === pkg.name);
  const installedSkill = installedSkills.find((s) => s.name === pkg.name);
  const isInstalled = pkg.type === "mcp" ? !!installedMCP : !!installedSkill;
  const isMissing = pkg.type === "skill" && !!installedSkill?.missing;
  const updateAvailable =
    isInstalled &&
    !!pkg.latest_version &&
    (pkg.type === "mcp"
      ? !!installedMCP?.version && installedMCP.version !== pkg.latest_version
      : !!installedSkill?.version && installedSkill.version !== pkg.latest_version);

  const handleInstall = async (env?: Record<string, string>, projectId?: string) => {
    if (!latest) return;
    setInstalling(true);
    try {
      if (pkg.type === "mcp") {
        let manifest: object;
        try {
          manifest = JSON.parse(latest.manifest);
        } catch {
          throw new Error("Invalid manifest JSON");
        }
        const mcpManifest = manifest as any;
        let mcpKey: string = pkg.name;
        if (mcpManifest.mcpServers && typeof mcpManifest.mcpServers === "object") {
          const firstKey = Object.keys(mcpManifest.mcpServers)[0];
          if (firstKey) mcpKey = firstKey;
        } else if (mcpManifest.key) {
          mcpKey = mcpManifest.key;
        }
        const targetProjectId = projectId ?? "";
        await marketplaceApi.installMCP({ name: pkg.name, package_id: pkg.id, version: latest.version, mcp_key: mcpKey, manifest, env, project_id: targetProjectId || undefined });
        toast.success(`MCP "${pkg.title}" installed`);
        setInstalledMCPs((prev) => {
          const next = prev.filter((m) => m.name !== pkg.name);
          return [...next, { name: pkg.name, project_id: targetProjectId, package_id: pkg.id, version: latest.version, mcp_key: mcpKey, manifest: latest.manifest, env_values: "{}", installed_at: Date.now() }];
        });
      } else {
        const targetProjectId = projectId ?? "";
        await marketplaceApi.installSkill({
          name: pkg.name,
          package_id: pkg.id,
          version: latest.version,
          project_id: targetProjectId || undefined,
        });
        toast.success(`Skill "${pkg.title}" installed`);
        setInstalledSkills((prev) => {
          const next = prev.filter((s) => s.name !== pkg.name);
          return [...next, { name: pkg.name, project_id: targetProjectId, version: latest.version, install_path: "", missing: false, installed_at: Date.now() }];
        });
      }
      window.dispatchEvent(new Event("marketplace-package-updated"));
    } catch (e: any) {
      toast.error("Install failed", { description: e.message });
    } finally {
      setInstalling(false);
    }
  };

  const handleInstallClick = () => {
    if (!latest) return;
    if (pkg.type === "mcp") {
      // Always show dialog so user can choose project scope (and fill required env vars if any)
      setInstallDialogOpen(true);
      return;
    } else {
      // skill: check for MCP dependencies that aren't yet installed
      const deps = parseMCPDeps(latest.manifest);
      const uninstalled = deps.filter((d) => !installedMCPs.some((m) => m.name === d));
      if (uninstalled.length > 0) {
        setPendingMCPDeps(uninstalled);
        return;
      }
      // Always show dialog for skills so user can choose project scope
      setInstallDialogOpen(true);
    }
  };

  const handleUninstall = async () => {
    setUninstalling(true);
    try {
      if (pkg.type === "mcp") {
        await marketplaceApi.uninstallMCP(pkg.name, installedMCP?.project_id || undefined);
        setInstalledMCPs((prev) => prev.filter((m) => m.name !== pkg.name));
        toast.success(`MCP "${pkg.title}" uninstalled`);
      } else {
        await marketplaceApi.uninstallSkill(pkg.name, installedSkill?.project_id || undefined);
        setInstalledSkills((prev) => prev.filter((s) => s.name !== pkg.name));
        toast.success(`Skill "${pkg.title}" uninstalled`);
      }
    } catch (e: any) {
      toast.error("Uninstall failed", { description: e.message });
    } finally {
      setUninstalling(false);
    }
  };

  const handleToggleStar = async () => {
    if (!detail || starring) return;
    setStarring(true);
    try {
      const status = starred
        ? await marketplaceApi.unstarPackage(detail.package.id)
        : await marketplaceApi.starPackage(detail.package.id);
      setStarred(status.starred);
      setStarCount(status.star_count);
    } catch (e: any) {
      toast.error("Failed to update star", { description: e.message });
    } finally {
      setStarring(false);
    }
  };

  const handleSubmitComment = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!commentBody.trim()) return;
    setSubmittingComment(true);
    try {
      const comment = await marketplaceApi.addComment(pkg.id, { body: commentBody });
      setComments((prev) => [comment, ...prev]);
      setCommentBody("");
      toast.success("Comment posted");
    } catch (e: any) {
      toast.error("Failed to post comment", { description: e.message });
    } finally {
      setSubmittingComment(false);
    }
  };

  const handleDeleteComment = async (commentId: string) => {
    setDeletingCommentId(commentId);
    try {
      await marketplaceApi.deleteComment(pkg.id, commentId);
      setComments((prev) => prev.filter((c) => c.id !== commentId));
    } catch (e: any) {
      toast.error("Failed to delete comment", { description: e.message });
    } finally {
      setDeletingCommentId(null);
    }
  };

  const envVars = latest ? parseEnvVarDefs(latest.manifest) : [];

  function parseMCPDeps(manifestStr: string): string[] {
    try {
      const parsed = JSON.parse(manifestStr);
      return Array.isArray(parsed?.mcp_dependencies) ? parsed.mcp_dependencies : [];
    } catch {
      return [];
    }
  }

  return (
    <>
      {installDialogOpen && latest && (
        <InstallDialog
          packageName={pkg.name}
          packageTitle={pkg.title}
          envVars={pkg.type === "mcp" ? envVars.filter((v) => v.required) : []}
          projects={(allProjects ?? [])
            .filter((p) => p.name !== UNASSIGNED_PROJECT && !!p.git_repo_path)
            .map((p): ProjectOption => ({ id: p.id, name: p.name }))}
          defaultProjectId={currentProject?.id ?? ""}
          onConfirm={async (env, projectId) => {
            setInstallDialogOpen(false);
            await handleInstall(env, projectId);
          }}
          onCancel={() => setInstallDialogOpen(false)}
        />
      )}

      {pendingMCPDeps.length > 0 && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
          <div className="bg-card border rounded-xl shadow-xl w-full max-w-sm mx-4 p-5 space-y-4">
            <div>
              <h2 className="font-semibold text-sm">Install MCP Dependencies</h2>
              <p className="text-xs text-muted-foreground mt-1">
                This skill requires the following MCP server{pendingMCPDeps.length > 1 ? "s" : ""} to be installed:
              </p>
            </div>
            <ul className="space-y-1">
              {pendingMCPDeps.map((dep) => (
                <li key={dep} className="flex items-center gap-2 text-xs font-mono bg-muted/50 rounded px-2.5 py-1.5">
                  <Wrench className="h-3 w-3 text-blue-500 shrink-0" />
                  {dep}
                </li>
              ))}
            </ul>
            <p className="text-xs text-muted-foreground">
              These MCPs will be installed automatically along with the skill.
            </p>
            <div className="flex justify-end gap-2">
              <button
                onClick={() => setPendingMCPDeps([])}
                className="px-3 py-1.5 text-sm border rounded-lg hover:bg-accent transition-colors"
              >
                Cancel
              </button>
              <button
                onClick={() => {
                  setPendingMCPDeps([]);
                  setInstallDialogOpen(true);
                }}
                className="px-3 py-1.5 text-sm bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 transition-colors"
              >
                Install All
              </button>
            </div>
          </div>
        </div>
      )}

      <div className="h-full overflow-y-auto">
        <div className="max-w-5xl mx-auto px-6 py-6 space-y-8">

          {/* ── Header ── */}
          <div>
            <button
              onClick={() => navigate(`/marketplace?tab=${detail?.package?.type ?? "mcp"}`)}
              className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground mb-4 transition-colors"
            >
              <ArrowLeft className="h-3.5 w-3.5" />
              Marketplace
            </button>

            <div className="flex items-start justify-between gap-4">
              <div className="flex items-center gap-3">
                {pkg.type === "mcp" ? (
                  <div className="p-2 rounded-lg bg-blue-500/10 shrink-0">
                    <Wrench className="h-5 w-5 text-blue-500" />
                  </div>
                ) : (
                  <div className="p-2 rounded-lg bg-violet-500/10 shrink-0">
                    <Package className="h-5 w-5 text-violet-500" />
                  </div>
                )}
                <div>
                  <h1 className="text-xl font-bold">{pkg.title}</h1>
                  <p className="text-xs text-muted-foreground font-mono mt-0.5">{pkg.name}</p>
                </div>
              </div>

              <div className="flex items-center gap-2 shrink-0">
                {/* Star button */}
                <button
                  onClick={handleToggleStar}
                  disabled={starring}
                  className={cn(
                    "flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg text-xs border transition-colors",
                    starred
                      ? "border-amber-400/60 bg-amber-400/10 text-amber-500 hover:bg-amber-400/20"
                      : "border-border text-muted-foreground hover:bg-accent"
                  )}
                >
                  {starring
                    ? <Loader2 className="h-3.5 w-3.5 animate-spin" />
                    : <Star className={cn("h-3.5 w-3.5", starred && "fill-amber-400")} />
                  }
                  <span className="font-mono tabular-nums">{starCount}</span>
                </button>

                {/* Install count (read-only badge) */}
                <span className="flex items-center gap-1 text-xs text-muted-foreground px-1.5">
                  <Download className="h-3.5 w-3.5" />
                  {detail.package.install_count ?? 0}
                </span>

                {isOwner && (
                  <>
                    <button
                      onClick={handleStartEdit}
                      className="flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg text-xs border hover:bg-accent transition-colors text-muted-foreground"
                    >
                      <Pencil className="h-3.5 w-3.5" />
                      Edit
                    </button>
                    <button
                      onClick={() => navigate(`/marketplace/publish/${pkg.id}`)}
                      className="flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg text-xs border hover:bg-accent transition-colors text-muted-foreground"
                    >
                      <GitBranch className="h-3.5 w-3.5" />
                      New Version
                    </button>
                    {confirmDelete ? (
                      <div className="flex items-center gap-1">
                        <span className="text-xs text-destructive font-medium">Sure?</span>
                        <button
                          onClick={handleDeletePackage}
                          disabled={deletingPackage}
                          className="flex items-center gap-1 px-2 py-1.5 rounded-lg text-xs bg-destructive text-destructive-foreground hover:bg-destructive/90 transition-colors"
                        >
                          {deletingPackage ? <Loader2 className="h-3 w-3 animate-spin" /> : null}
                          Delete
                        </button>
                        <button
                          onClick={() => setConfirmDelete(false)}
                          className="px-2 py-1.5 rounded-lg text-xs border hover:bg-accent transition-colors"
                        >
                          Cancel
                        </button>
                      </div>
                    ) : (
                      <button
                        onClick={() => setConfirmDelete(true)}
                        className="flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg text-xs border hover:bg-destructive/10 hover:text-destructive hover:border-destructive/30 text-muted-foreground transition-colors"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                        Delete
                      </button>
                    )}
                  </>
                )}
                {isInstalled ? (
                  <div className="flex items-center gap-2">
                    {isMissing ? (
                      <span className="text-xs text-amber-500 font-medium">Files missing</span>
                    ) : updateAvailable ? (
                      <ArrowUpCircle className="h-4 w-4 text-amber-500" aria-label={`Update to v${pkg.latest_version}`} />
                    ) : (
                      <CheckCircle2 className="h-4 w-4 text-green-500" />
                    )}
                    {(updateAvailable || isMissing) && (
                      <button
                        onClick={handleInstallClick}
                        disabled={installing || !latest}
                        className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs bg-amber-500 text-white hover:bg-amber-600 disabled:opacity-50 transition-colors"
                      >
                        {installing ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : isMissing ? <RefreshCw className="h-3.5 w-3.5" /> : <ArrowUpCircle className="h-3.5 w-3.5" />}
                        {isMissing ? "Reinstall" : `Update to v${pkg.latest_version}`}
                      </button>
                    )}
                    <button
                      onClick={handleUninstall}
                      disabled={uninstalling}
                      className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs border hover:bg-destructive/10 hover:text-destructive hover:border-destructive/30 text-muted-foreground transition-colors"
                    >
                      {uninstalling ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}
                      Uninstall
                    </button>
                  </div>
                ) : (
                  <button
                    onClick={handleInstallClick}
                    disabled={installing || !latest}
                    className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50 transition-colors"
                  >
                    {installing ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Plus className="h-3.5 w-3.5" />}
                    Install
                  </button>
                )}
              </div>
            </div>

            {!editMode && pkg.description && (
            <p className="text-sm text-muted-foreground mt-3 ml-12">{pkg.description}</p>
          )}

          {/* ── Inline Edit Form ── */}
          {editMode && (
            <div className="mt-4 border rounded-xl p-4 bg-muted/20 space-y-4">
              <div className="flex items-center justify-between">
                <p className="text-sm font-medium">Edit package info</p>
                <button
                  onClick={() => setEditMode(false)}
                  className="p-1 rounded hover:bg-accent text-muted-foreground"
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              </div>

              <div className="space-y-1.5">
                <label className="text-xs text-muted-foreground">Title <span className="text-destructive">*</span></label>
                <input
                  value={editTitle}
                  onChange={(e) => setEditTitle(e.target.value)}
                  className="w-full px-3 py-2 text-sm bg-background border rounded-lg outline-none focus:ring-1 focus:ring-primary"
                />
              </div>

              <div className="space-y-1.5">
                <label className="text-xs text-muted-foreground">Description</label>
                <textarea
                  value={editDesc}
                  onChange={(e) => setEditDesc(e.target.value)}
                  rows={3}
                  className="w-full px-3 py-2 text-sm bg-background border rounded-lg outline-none focus:ring-1 focus:ring-primary resize-none"
                />
              </div>

              <div className="space-y-1.5">
                <label className="text-xs text-muted-foreground">Category</label>
                <div className="flex flex-wrap gap-1.5">
                  {PACKAGE_CATEGORIES.map((cat) => (
                    <button
                      key={cat.value}
                      type="button"
                      onClick={() => setEditCategory(editCategory === cat.value ? null : cat.value as PackageCategoryValue)}
                      className={cn(
                        "px-3 py-1 rounded-full text-xs font-medium border transition-colors",
                        editCategory === cat.value
                          ? "border-primary bg-primary/10 text-primary"
                          : "border-border text-muted-foreground hover:border-muted-foreground hover:text-foreground"
                      )}
                    >
                      {cat.label}
                    </button>
                  ))}
                </div>
              </div>

              <div className="flex justify-end gap-2">
                <button
                  onClick={() => setEditMode(false)}
                  className="px-3 py-1.5 text-sm border rounded-lg hover:bg-accent transition-colors"
                >
                  Cancel
                </button>
                <button
                  onClick={handleSaveEdit}
                  disabled={savingEdit || !editTitle.trim()}
                  className="flex items-center gap-1.5 px-3 py-1.5 text-sm bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 disabled:opacity-50 transition-colors"
                >
                  {savingEdit && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                  Save
                </button>
              </div>
            </div>
          )}
          </div>

          {/* ── Main + Sidebar ── */}
          <div className="grid grid-cols-[1fr_220px] gap-8 items-start">

            {/* Main content + Comments */}
            <div className="space-y-6 min-w-0">
              {pkg.type === "mcp" && latest?.manifest && (
                <div>
                  <h2 className="text-sm font-semibold mb-3">Configuration</h2>
                  <pre className="bg-muted/60 border rounded-lg px-4 py-4 text-xs font-mono overflow-x-auto leading-relaxed">
                    {formatManifest(latest.manifest)}
                  </pre>
                </div>
              )}
              {pkg.type === "skill" && skillMd && (
                <div>
                  <h2 className="text-sm font-semibold mb-3">SKILL.md</h2>
                  <div className="markdown-content bg-muted/20 border rounded-lg px-4 py-4">
                    <ReactMarkdown remarkPlugins={[remarkGfm]}>{skillMd}</ReactMarkdown>
                  </div>
                </div>
              )}

              {/* ── Comments ── */}
              <div className="border-t pt-6 space-y-4">
                <h2 className="text-sm font-semibold flex items-center gap-2">
                  <MessageSquare className="h-4 w-4 text-muted-foreground" />
                  Comments
                  {comments.length > 0 && (
                    <span className="text-xs text-muted-foreground font-normal">({comments.length})</span>
                  )}
                </h2>

                <form onSubmit={handleSubmitComment} className="flex gap-3">
                  <textarea
                    value={commentBody}
                    onChange={(e) => setCommentBody(e.target.value)}
                    placeholder="Share your experience or questions..."
                    rows={2}
                    className="flex-1 px-3 py-2 text-sm bg-background border rounded-lg outline-none focus:ring-1 focus:ring-primary resize-none"
                  />
                  <button
                    type="submit"
                    disabled={submittingComment || !commentBody.trim()}
                    className="self-end flex items-center gap-1.5 px-3 py-2 text-sm bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 disabled:opacity-50 transition-colors whitespace-nowrap"
                  >
                    {submittingComment ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : null}
                    Post
                  </button>
                </form>

                {comments.length === 0 ? (
                  <p className="text-sm text-muted-foreground italic">No comments yet.</p>
                ) : (
                  <div className="space-y-3">
                    {comments.map((c) => (
                      <div key={c.id} className="border rounded-xl p-4 bg-card">
                        <div className="flex items-center justify-between mb-2">
                          <div className="flex items-center gap-2">
                            <span className="text-sm font-medium">{c.user_name}</span>
                            <span className="text-xs text-muted-foreground">{c.created_at?.slice(0, 10)}</span>
                          </div>
                          <button
                            onClick={() => handleDeleteComment(c.id)}
                            disabled={deletingCommentId === c.id}
                            className="p-1 rounded hover:bg-destructive/10 hover:text-destructive text-muted-foreground/40 transition-colors"
                          >
                            {deletingCommentId === c.id
                              ? <Loader2 className="h-3.5 w-3.5 animate-spin" />
                              : <Trash2 className="h-3.5 w-3.5" />
                            }
                          </button>
                        </div>
                        {c.body && <p className="text-sm text-muted-foreground whitespace-pre-wrap">{c.body}</p>}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </div>

            {/* Sidebar */}
            <div className="space-y-4">
              {/* Package info */}
              <SidebarCard title="Info">
                <div className="space-y-2">
                  <SidebarRow icon={<Tag className="h-3 w-3" />} label={pkg.type === "mcp" ? "MCP Server" : "Agent Skill"} />
                  <SidebarRow icon={<User className="h-3 w-3" />} label={pkg.author_id} />
                  <SidebarRow icon={<Calendar className="h-3 w-3" />} label={pkg.created_at?.slice(0, 10) ?? ""} />
                  {pkg.tags?.length > 0 && (() => {
                    const cat = PACKAGE_CATEGORIES.find((c) => c.value === pkg.tags[0]);
                    return cat ? (
                      <div className="pt-1">
                        <span className="inline-flex items-center px-2 py-0.5 rounded-full text-[11px] font-medium bg-muted text-muted-foreground">
                          {cat.label}
                        </span>
                      </div>
                    ) : null;
                  })()}
                </div>
              </SidebarCard>

              {/* Version + Changelog merged */}
              <SidebarCard title="Version">
                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <span className="text-xs text-muted-foreground">Latest</span>
                    <span className="font-mono text-xs font-semibold">v{pkg.latest_version}</span>
                  </div>
                  {isInstalled && (installedMCP?.version || installedSkill?.version) && (() => {
                    const pid = installedMCP?.project_id ?? installedSkill?.project_id ?? "";
                    const scopeName = pid
                      ? (allProjects?.find((p) => p.id === pid)?.name ?? pid.slice(0, 8))
                      : "All Projects";
                    return (
                      <>
                        <div className="flex items-center justify-between">
                          <span className="text-xs text-muted-foreground">Installed</span>
                          <span className={cn("font-mono text-xs", updateAvailable ? "text-amber-500" : "text-green-500")}>
                            v{installedMCP?.version || installedSkill?.version}
                          </span>
                        </div>
                        <div className="flex items-center justify-between">
                          <span className="text-xs text-muted-foreground">Scope</span>
                          <span className="text-xs text-muted-foreground">{scopeName}</span>
                        </div>
                      </>
                    );
                  })()}
                </div>
                {(() => {
                  const changelogVersions = detail.versions
                    .filter((v) => v.changelog?.trim())
                    .slice(0, 10);
                  if (changelogVersions.length === 0) return null;
                  return (
                    <div className="border-t mt-2 pt-2.5 space-y-3">
                      {changelogVersions.map((v) => (
                        <div key={v.id} className="space-y-0.5">
                          <div className="flex items-center justify-between">
                            <span className="font-mono text-[11px] font-semibold text-foreground">v{v.version}</span>
                            <span className="text-[10px] text-muted-foreground/60">{v.created_at?.slice(0, 10)}</span>
                          </div>
                          <p className="text-[11px] text-muted-foreground whitespace-pre-wrap leading-relaxed">{v.changelog}</p>
                        </div>
                      ))}
                    </div>
                  );
                })()}
              </SidebarCard>
            </div>
          </div>

        </div>
      </div>
    </>
  );
}

function SidebarCard({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="border rounded-xl p-3.5 bg-card space-y-2.5">
      <p className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">{title}</p>
      {children}
    </div>
  );
}

function SidebarRow({ icon, label }: { icon: React.ReactNode; label: string }) {
  return (
    <div className="flex items-center gap-2 text-xs text-foreground">
      <span className="text-muted-foreground shrink-0">{icon}</span>
      <span className="truncate">{label}</span>
    </div>
  );
}
