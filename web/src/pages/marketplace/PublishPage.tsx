import { useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { ArrowLeft, CornerLeftUp, FolderOpen, Loader2, Plus, X } from "lucide-react";
import { toast } from "sonner";
import { marketplaceApi, PACKAGE_CATEGORIES, type PackageCategoryValue, utilApi } from "@/lib/api";
import { cn } from "@/lib/utils";
import { MCPDepsSelector } from "./MCPDepsSelector";

type PackageType = "mcp" | "skill";

const MCP_TEMPLATE = JSON.stringify(
  {
    mcpServers: {
      "my-server": {
        command: "npx",
        args: ["-y", "my-mcp-package"],
      },
    },
  },
  null,
  2
);

function autoVersion(): string {
  const now = new Date();
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}${pad(now.getHours())}${pad(now.getMinutes())}`;
}

export function PublishPage() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const type = (searchParams.get("type") === "skill" ? "skill" : "mcp") as PackageType;

  const [name, setName] = useState("");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [category, setCategory] = useState<PackageCategoryValue | null>(null);

  // MCP
  const [manifestJson, setManifestJson] = useState("");

  // Skill
  const [skillDir, setSkillDir] = useState("");
  const [mcpDeps, setMcpDeps] = useState<string[]>([]);
  const [browseDirs, setBrowseDirs] = useState<string[]>([]);
  const [browseCurrentPath, setBrowseCurrentPath] = useState("");
  const [browseLoading, setBrowseLoading] = useState(false);

  const [submitting, setSubmitting] = useState(false);
  const [uploadingDir, setUploadingDir] = useState(false);
  const [createdPackageId, setCreatedPackageId] = useState<string | null>(null);

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

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim() || !title.trim()) {
      toast.error("Name and title are required");
      return;
    }

    const version = autoVersion();
    setSubmitting(true);
    try {
      if (type === "skill") {
        if (!skillDir.trim()) {
          toast.error("Skill directory path is required");
          return;
        }
        setUploadingDir(true);
        let packageId = createdPackageId;
        if (!packageId) {
          const createdDetail = await marketplaceApi.publishPackage({
            name: name.trim(),
            type,
            title: title.trim(),
            description: description.trim(),
            version: "0.0.0-init",
            manifest: { type: "skill", name: name.trim() },
            tags: category ? [category] : [],
          });
          packageId = createdDetail.package.id;
          setCreatedPackageId(packageId);
        }

        const { oss_key, skill_md } = await marketplaceApi.uploadSkillDir(packageId, skillDir.trim());
        setUploadingDir(false);

        await marketplaceApi.publishVersion(packageId, {
          version,
          manifest: {
            type: "skill",
            name: name.trim(),
            oss_key,
            skill_md,
            ...(mcpDeps.length > 0 ? { mcp_dependencies: mcpDeps } : {}),
          },
          oss_key,
        });

        setCreatedPackageId(null);
        toast.success("Skill created successfully!");
        navigate("/marketplace");
        return;
      }

      // MCP
      let manifest: object;
      try {
        manifest = JSON.parse(manifestJson);
      } catch {
        toast.error("Invalid JSON in MCP configuration");
        return;
      }
      if (!(manifest as any).mcpServers) {
        toast.error('Configuration must contain a "mcpServers" key');
        return;
      }

      await marketplaceApi.publishPackage({
        name: name.trim(),
        type,
        title: title.trim(),
        description: description.trim(),
        version,
        manifest,
        tags: category ? [category] : [],
      });

      toast.success("MCP created successfully!");
      navigate("/marketplace");
    } catch (err: any) {
      toast.error("Create failed", { description: err.message });
    } finally {
      setSubmitting(false);
      setUploadingDir(false);
    }
  };

  const busy = submitting || uploadingDir;
  const isMcp = type === "mcp";
  const nameLabel = isMcp ? "MCP Name" : "Skill Name";
  const namePlaceholder = isMcp ? "e.g. my-mcp-server" : "e.g. my-agent-skill";

  return (
    <div className="h-full overflow-y-auto">
      <div className="max-w-2xl mx-auto px-6 py-8">
        <div className="flex items-center gap-3 mb-6">
          <button
            onClick={() => navigate("/marketplace")}
            className="p-1.5 rounded-lg hover:bg-accent transition-colors text-muted-foreground hover:text-foreground"
          >
            <ArrowLeft className="h-4 w-4" />
          </button>
          <div>
            <h1 className="text-xl font-bold">
              {isMcp ? "Create MCP Server" : "Create Agent Skill"}
            </h1>
            <p className="text-sm text-muted-foreground">
              {isMcp ? "Share an MCP server with your team" : "Share an agent skill with your team"}
            </p>
          </div>
        </div>

        <form onSubmit={handleSubmit} className="space-y-6">
          {/* Name + Title */}
          <Field label={nameLabel} required>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={namePlaceholder}
              pattern="[a-z0-9\-_]+"
              title="Lowercase letters, numbers, hyphens, underscores"
              className={inputCls}
              required
            />
          </Field>

          <Field label="Title" required>
            <input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="Human-readable title"
              className={inputCls}
              required
            />
          </Field>

          <Field label="Description">
            <textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder={isMcp ? "Describe what this MCP server does..." : "Describe what this skill does..."}
              rows={3}
              className={cn(inputCls, "resize-none")}
            />
          </Field>

          {/* Category */}
          <div className="space-y-2">
            <label className="text-sm font-medium">
              Category
              <span className="text-muted-foreground font-normal ml-1 text-xs">(optional)</span>
            </label>
            <div className="flex flex-wrap gap-1.5">
              {PACKAGE_CATEGORIES.map((cat) => (
                <button
                  key={cat.value}
                  type="button"
                  title={cat.description}
                  onClick={() => setCategory(category === cat.value ? null : cat.value)}
                  className={cn(
                    "px-3 py-1 rounded-full text-xs font-medium border transition-colors",
                    category === cat.value
                      ? "border-primary bg-primary/10 text-primary"
                      : "border-border text-muted-foreground hover:border-muted-foreground hover:text-foreground"
                  )}
                >
                  {cat.label}
                </button>
              ))}
            </div>
          </div>

          {/* Type-specific */}
          {isMcp ? (
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
                  placeholder={MCP_TEMPLATE}
                  className={cn(inputCls, "resize-y font-mono text-xs")}
                />
              </Field>
              <p className="text-[11px] text-muted-foreground">
                Standard <code className="bg-muted px-1 rounded">mcpServers</code> format.
                For env vars that users must supply, add them to{" "}
                <code className="bg-muted px-1 rounded">env</code> with an empty string value.
              </p>
            </div>
          ) : (
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

                {/* Directory browser */}
                {browseCurrentPath && (
                  <div className="border rounded-md bg-background overflow-hidden mt-2">
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
                        onClick={() => { setBrowseCurrentPath(""); setBrowseDirs([]); }}
                        className="p-0.5 rounded hover:bg-accent text-muted-foreground/40 hover:text-foreground transition-colors shrink-0"
                        title="Close browser"
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
              <Field label="MCP Dependencies">
                <p className="text-xs text-muted-foreground mb-2">
                  MCPs that must be installed for this skill to work. They will be auto-installed when users install your skill.
                </p>
                <MCPDepsSelector value={mcpDeps} onChange={setMcpDeps} />
              </Field>
            </div>
          )}

          <div className="flex justify-end gap-3 pt-2">
            <button
              type="button"
              onClick={() => navigate("/marketplace")}
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
                  {uploadingDir ? "Uploading..." : "Creating..."}
                </>
              ) : (
                <>
                  <Plus className="h-4 w-4" />
                  Create
                </>
              )}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function Field({ label, required, children }: { label: string; required?: boolean; children: React.ReactNode }) {
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
