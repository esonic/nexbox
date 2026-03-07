import { useState, useEffect } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { ArrowLeft, CornerLeftUp, FolderOpen, Loader2, Upload, X } from "lucide-react";
import { toast } from "sonner";
import { marketplaceApi, type MarketplacePackageDetail, utilApi } from "@/lib/api";
import { cn } from "@/lib/utils";
import { MCPDepsSelector } from "./MCPDepsSelector";

function autoVersion(): string {
  const now = new Date();
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}${pad(now.getHours())}${pad(now.getMinutes())}`;
}

export function PublishVersionPage() {
  const { packageId } = useParams<{ packageId: string }>();
  const navigate = useNavigate();

  const [detail, setDetail] = useState<MarketplacePackageDetail | null>(null);
  const [loadingDetail, setLoadingDetail] = useState(true);

  const [changelog, setChangelog] = useState("");
  const [manifestJson, setManifestJson] = useState("");

  // Skill dir picker
  const [skillDir, setSkillDir] = useState("");
  const [browseDirs, setBrowseDirs] = useState<string[]>([]);
  const [browseCurrentPath, setBrowseCurrentPath] = useState("");
  const [browseLoading, setBrowseLoading] = useState(false);

  const [mcpDeps, setMcpDeps] = useState<string[]>([]);

  const [submitting, setSubmitting] = useState(false);
  const [uploadingDir, setUploadingDir] = useState(false);

  useEffect(() => {
    if (!packageId) return;
    marketplaceApi
      .getPackage(packageId)
      .then((d) => {
        setDetail(d);
        if (d.package.type === "mcp") {
          const current =
            d.versions.find((v) => v.version === d.package.latest_version) ?? d.versions[0];
          if (current?.manifest) {
            try {
              setManifestJson(JSON.stringify(JSON.parse(current.manifest), null, 2));
            } catch {
              setManifestJson(current.manifest);
            }
          }
        }
      })
      .catch((e) => {
        toast.error("Failed to load package", { description: e.message });
        navigate("/marketplace");
      })
      .finally(() => setLoadingDetail(false));
  }, [packageId]);

  const handleBrowseDir = async (path?: string) => {
    setBrowseLoading(true);
    try {
      const resp = await utilApi.browseDir(path);
      setBrowseDirs(resp.dirs?.map((d) => d.name) || []);
      setBrowseCurrentPath(resp.current || path || "");
    } catch (e) {
      toast.error("Failed to browse directory", { description: String(e) });
    } finally {
      setBrowseLoading(false);
    }
  };

  const browseParent = browseCurrentPath
    ? browseCurrentPath.split("/").slice(0, -1).join("/") || "/"
    : null;

  if (loadingDetail) {
    return (
      <div className="h-full flex items-center justify-center text-muted-foreground gap-2">
        <Loader2 className="h-5 w-5 animate-spin" />
        Loading...
      </div>
    );
  }

  if (!detail) return null;

  const pkg = detail.package;
  const isSkill = pkg.type === "skill";

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (isSkill && !skillDir.trim()) {
      toast.error("Skill directory path is required");
      return;
    }

    const version = autoVersion();
    setSubmitting(true);
    try {
      let ossKey = "";

      let skillMd = "";
      if (isSkill) {
        setUploadingDir(true);
        const result = await marketplaceApi.uploadSkillDir(packageId!, skillDir.trim());
        ossKey = result.oss_key;
        skillMd = result.skill_md;
        setUploadingDir(false);
      }

      let manifest: object;
      if (isSkill) {
        manifest = {
          type: "skill",
          name: pkg.name,
          oss_key: ossKey,
          skill_md: skillMd,
          ...(mcpDeps.length > 0 ? { mcp_dependencies: mcpDeps } : {}),
        };
      } else {
        try {
          manifest = JSON.parse(manifestJson || "{}");
        } catch {
          toast.error("Invalid JSON in MCP configuration");
          return;
        }
      }

      await marketplaceApi.publishVersion(packageId!, {
        version,
        manifest,
        changelog: changelog.trim() || undefined,
        oss_key: ossKey || undefined,
      });

      toast.success("New version published!");
      navigate(`/marketplace/${packageId}`);
    } catch (err: any) {
      toast.error("Publish failed", { description: err.message });
    } finally {
      setSubmitting(false);
      setUploadingDir(false);
    }
  };

  const busy = submitting || uploadingDir;

  return (
    <div className="h-full overflow-y-auto">
      <div className="max-w-2xl mx-auto px-6 py-8">
        <div className="flex items-center gap-3 mb-6">
          <button
            onClick={() => navigate(`/marketplace/${packageId}`)}
            className="p-1.5 rounded-lg hover:bg-accent transition-colors text-muted-foreground hover:text-foreground"
          >
            <ArrowLeft className="h-4 w-4" />
          </button>
          <div>
            <h1 className="text-xl font-bold">New Version</h1>
            <p className="text-sm text-muted-foreground">
              {pkg.title} · <span className="font-mono">{pkg.name}</span>
              <span className="ml-2 text-muted-foreground/50">current: v{pkg.latest_version}</span>
            </p>
          </div>
        </div>

        <form onSubmit={handleSubmit} className="space-y-6">
          <Field label="Changelog">
            <textarea
              value={changelog}
              onChange={(e) => setChangelog(e.target.value)}
              placeholder="What's new in this version..."
              rows={3}
              className={cn(inputCls, "resize-none")}
            />
          </Field>

          {/* MCP manifest */}
          {!isSkill && (
            <div className="space-y-2 border rounded-xl p-4 bg-muted/20">
              <p className="text-[11px] font-medium text-muted-foreground uppercase tracking-wide">
                MCP Server Configuration
              </p>
              <Field label="mcpServers config" required>
                <textarea
                  value={manifestJson}
                  onChange={(e) => setManifestJson(e.target.value)}
                  rows={12}
                  spellCheck={false}
                  className={cn(inputCls, "resize-y font-mono text-xs")}
                />
              </Field>
            </div>
          )}

          {/* Skill directory */}
          {isSkill && (
            <div className="space-y-2 border rounded-xl p-4 bg-muted/20">
              <p className="text-[11px] font-medium text-muted-foreground uppercase tracking-wide">
                Skill Package
              </p>
              <Field label="Skill directory" required>
                <div className="flex items-center gap-1.5">
                  <input
                    value={skillDir}
                    onChange={(e) => {
                      setSkillDir(e.target.value);
                      setBrowseCurrentPath("");
                    }}
                    placeholder="/absolute/path/to/skill-dir"
                    className={cn(inputCls, "font-mono text-xs flex-1")}
                  />
                  <button
                    type="button"
                    onClick={() => handleBrowseDir(skillDir.trim() || undefined)}
                    className="h-[38px] px-2.5 rounded-lg border bg-background text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors shrink-0 flex items-center"
                    title="Browse directories"
                  >
                    <FolderOpen className="h-3.5 w-3.5" />
                  </button>
                </div>

                {browseCurrentPath && (
                  <div className="border rounded-md bg-background overflow-hidden mt-2">
                    <div className="flex items-center gap-1.5 px-2.5 py-1.5 border-b bg-muted/30 min-w-0">
                      {browseParent && (
                        <button
                          type="button"
                          onClick={() => handleBrowseDir(browseParent)}
                          className="p-0.5 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors shrink-0"
                        >
                          <CornerLeftUp className="h-3 w-3" />
                        </button>
                      )}
                      <span className="text-[11px] font-mono text-muted-foreground truncate flex-1">
                        {browseCurrentPath}
                      </span>
                      <button
                        type="button"
                        onClick={() => { setBrowseCurrentPath(""); setBrowseDirs([]); }}
                        className="p-0.5 rounded hover:bg-accent text-muted-foreground/40 hover:text-foreground transition-colors shrink-0"
                      >
                        <X className="h-3 w-3" />
                      </button>
                    </div>
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
                            browseCurrentPath === "/" ? `/${dir}` : `${browseCurrentPath}/${dir}`;
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
                                  setSkillDir(fullPath);
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
                    <div className="border-t px-2.5 py-1.5 bg-muted/20">
                      <button
                        type="button"
                        onClick={() => {
                          setSkillDir(browseCurrentPath);
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
              </Field>
            </div>
          )}

          {/* MCP dependencies (skills only) */}
          {isSkill && (
            <Field label="MCP Dependencies">
              <p className="text-xs text-muted-foreground mb-2">
                MCPs that must be installed for this skill to work. They will be auto-installed when users install your skill.
              </p>
              <MCPDepsSelector value={mcpDeps} onChange={setMcpDeps} />
            </Field>
          )}

          <div className="flex justify-end gap-3 pt-2">
            <button
              type="button"
              onClick={() => navigate(`/marketplace/${packageId}`)}
              className="px-4 py-2 rounded-lg text-sm border hover:bg-accent transition-colors"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={busy}
              className="flex items-center gap-2 px-4 py-2 rounded-lg text-sm bg-primary text-primary-foreground hover:bg-primary/90 transition-colors disabled:opacity-50"
            >
              {busy ? (
                <>
                  <Loader2 className="h-4 w-4 animate-spin" />
                  {uploadingDir ? "Uploading..." : "Publishing..."}
                </>
              ) : (
                <>
                  <Upload className="h-4 w-4" />
                  Publish Version
                </>
              )}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function Field({
  label,
  required,
  children,
}: {
  label: string;
  required?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      <label className="text-sm font-medium">
        {label}
        {required && <span className="text-destructive ml-0.5">*</span>}
      </label>
      {children}
    </div>
  );
}

const inputCls =
  "w-full px-3 py-2 text-sm bg-background border rounded-lg outline-none focus:ring-1 focus:ring-primary";
