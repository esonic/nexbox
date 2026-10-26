import { useState, useEffect, useCallback } from "react";
import {
  Search,
  Plus,
  Package,
  Wrench,
  CheckCircle2,
  RefreshCw,
  Eye,
  EyeOff,
  Trash2,
  FolderPlus,
} from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import {
  marketplaceApi,
  projectsApi,
  type LocalPlugin,
  type InstalledMCP,
  type InstalledSkill,
  type InstalledPluginEntry,
  type MarketplaceSource,
} from "@/lib/api";
import type { Project } from "@/types";
import { PageHeader } from "@/components/layout/PageHeader";

type TabType = "mcp" | "skill" | "plugin";

const SCOPE_ALL = "";

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function isMCPServerConfig(value: unknown): boolean {
  if (!value || typeof value !== "object") return false;
  const config = value as Record<string, unknown>;
  return Boolean(config.command || config.url);
}

export function MarketplacePage() {
  const [tab, setTab] = useState<TabType>("mcp");
  const [search, setSearch] = useState("");
  const [plugins, setPlugins] = useState<LocalPlugin[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [installedMCPs, setInstalledMCPs] = useState<InstalledMCP[]>([]);
  const [installedSkills, setInstalledSkills] = useState<InstalledSkill[]>([]);
  const [installedPlugins, setInstalledPlugins] = useState<InstalledPluginEntry[]>([]);
  const [sources, setSources] = useState<MarketplaceSource[]>([]);
  const [loading, setLoading] = useState(true);
  const [loaded, setLoaded] = useState(false);
  const [installing, setInstalling] = useState<string | null>(null);
  const [dialogPlugin, setDialogPlugin] = useState<LocalPlugin | null>(null);
  const [showAddMarketplace, setShowAddMarketplace] = useState(false);
  const [showAddMCP, setShowAddMCP] = useState(false);

  const loadData = useCallback(async () => {
    setLoading(true);
    try {
      const [allPlugins, mcps, skills, pluginEntries, projs, srcs] = await Promise.all([
        marketplaceApi.listPlugins(),
        marketplaceApi.listInstalledMCPs(),
        marketplaceApi.listInstalledSkills(),
        marketplaceApi.listInstalledPlugins(),
        projectsApi.list(),
        marketplaceApi.listSources(),
      ]);
      setPlugins(allPlugins || []);
      setInstalledMCPs(mcps || []);
      setInstalledSkills(skills || []);
      setInstalledPlugins(pluginEntries || []);
      setProjects(projs || []);
      setSources(srcs || []);
      setLoaded(true);
    } catch (error: unknown) {
      toast.error("Failed to load marketplace", { description: errorMessage(error) });
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadData();
  }, [loadData]);

  const isInstalled = (plugin: LocalPlugin) => {
    switch (plugin.type) {
      case "mcp":
        return installedMCPs.some((m) => m.name === plugin.name);
      case "skill":
        return installedSkills.some((s) => s.name === plugin.name);
      case "plugin":
      case "lsp":
        return installedPlugins.some((p) => p.name === plugin.name);
      default:
        return false;
    }
  };

  // Returns the list of project_ids where a plugin is installed ("" = global/All).
  const getInstalledScopes = (plugin: LocalPlugin): string[] => {
    switch (plugin.type) {
      case "mcp":
        return installedMCPs.filter((m) => m.name === plugin.name).map((m) => m.project_id);
      case "skill":
        return installedSkills.filter((s) => s.name === plugin.name).map((s) => s.project_id);
      case "plugin":
      case "lsp":
        return installedPlugins.filter((p) => p.name === plugin.name).map((p) => p.project_id);
      default:
        return [];
    }
  };

  const handleInstallClick = (plugin: LocalPlugin) => {
    setDialogPlugin(plugin);
  };

  // Install a plugin into a single scope ("" = All Projects / global).
  const installToScope = async (
    plugin: LocalPlugin,
    projectId: string,
    envValues: Record<string, string>,
  ) => {
    if (plugin.type === "mcp") {
      const mcpConfig = (plugin.mcp_config ? JSON.parse(plugin.mcp_config) : {}) as Record<string, unknown>;
      let manifest: Record<string, unknown>;
      if (mcpConfig.mcpServers) {
        manifest = { mcpServers: mcpConfig.mcpServers };
      } else {
        // .mcp.json uses flat format: {"serverName": {"command":...}}
        // Wrap it as mcpServers
        const hasServerEntries = Object.values(mcpConfig).some(isMCPServerConfig);
        manifest = hasServerEntries
          ? { mcpServers: mcpConfig }
          : { mcpServers: { [plugin.name]: { command: plugin.command, ...(plugin.args?.length ? { args: plugin.args } : {}) } } };
      }
      await marketplaceApi.installMCP({
        name: plugin.name,
        package_id: plugin.id,
        version: plugin.version || "0.0.0",
        manifest,
        env: envValues,
        project_id: projectId || undefined,
        auth_type: plugin.auth_type,
      });
      // Probe the MCP server to detect OAuth requirement immediately after install.
      // If the upstream returns 401, the server broadcasts connector:needs_auth so
      // the standard "Authorize" toast appears — no custom handling needed here.
      marketplaceApi.probeMCP(plugin.name, projectId || undefined).then((status) => {
        if (status?.state === "unavailable" || status?.state === "auth_rejected") {
          toast.warning(`Installed ${plugin.name}, but its tools are unavailable`, { description: status.message });
        }
      }).catch(() => {
        toast.warning(`Installed ${plugin.name}, but its connection could not be verified`);
      });
    } else if (plugin.type === "skill") {
      await marketplaceApi.installSkill({
        name: plugin.name,
        package_id: plugin.id,
        version: plugin.version || "0.0.0",
        install_path: plugin.install_path,
        project_id: projectId || undefined,
      });
    } else if (plugin.type === "plugin" || plugin.type === "lsp") {
      await marketplaceApi.installPlugin({
        name: plugin.name,
        install_path: plugin.install_path || "",
        lsp_config: plugin.lsp_config || undefined,
        project_id: projectId || undefined,
      });
    }
  };

  const handleInstallConfirm = async (
    plugin: LocalPlugin,
    projectIds: string[],
    envValues: Record<string, string>,
  ) => {
    const previousScopes = getInstalledScopes(plugin);
    const added = projectIds.filter((id) => !previousScopes.includes(id));
    const removed = previousScopes.filter((id) => !projectIds.includes(id));
    setInstalling(plugin.id);
    const scopeName = (id: string) =>
      id === "" ? "All Projects" : projects.find((p) => p.id === id)?.name ?? id.slice(0, 8);
    const failures: string[] = [];
    try {
      // Install additions first. If one fails, retain all previous scopes.
      for (const projectId of added) {
        try {
          await installToScope(plugin, projectId, envValues);
        } catch (error: unknown) {
          failures.push(`${scopeName(projectId)}: ${errorMessage(error)}`);
        }
      }
      if (failures.length === 0) {
        for (const projectId of removed) {
          try {
            if (plugin.type === "mcp") {
              await marketplaceApi.uninstallMCP(plugin.name, projectId);
            } else if (plugin.type === "skill") {
              await marketplaceApi.uninstallSkill(plugin.name, projectId);
            } else {
              await marketplaceApi.uninstallPlugin(plugin.name, projectId);
            }
          } catch (error: unknown) {
            failures.push(`${scopeName(projectId)}: ${errorMessage(error)}`);
          }
        }
      }
      if (failures.length > 0) {
        toast.error("Some installation changes could not be saved", { description: failures.join("\n") });
      } else {
        toast.success(`Installation settings saved for ${plugin.name}`);
      }
      await loadData();
      return failures.length === 0;
    } finally {
      setInstalling(null);
    }
  };

  const handleAddMCP = async (name: string, url: string, headers: Record<string, string>) => {
    await marketplaceApi.addMCPSource(name, url, headers);
    toast.success("MCP added to Marketplace");
    await loadData();
    setShowAddMCP(false);
  };

  const matchesTab = (p: LocalPlugin, currentTab: TabType) => {
    switch (currentTab) {
      case "mcp":
        return p.type === "mcp";
      case "skill":
        return p.type === "skill";
      case "plugin":
        return p.type === "plugin" || p.type === "lsp";
      default:
        return false;
    }
  };

  const filteredPlugins = plugins
    .filter((p) => {
      if (!matchesTab(p, tab)) return false;
      if (search) {
        const q = search.toLowerCase();
        return (
          p.name.toLowerCase().includes(q) ||
          p.description?.toLowerCase().includes(q)
        );
      }
      return true;
    })
    .sort((a, b) => {
      const aInstalled = isInstalled(a) ? 0 : 1;
      const bInstalled = isInstalled(b) ? 0 : 1;
      if (aInstalled !== bInstalled) return aInstalled - bInstalled;
      return a.name.localeCompare(b.name);
    });

  // Installed items whose name no marketplace source offers anymore (source removed,
  // renamed, or deleted by hand). They have no card above, so list them here to stay removable.
  const orphanedInstalls = (() => {
    if (!loaded) return [];
    const offered = new Set(plugins.filter((p) => matchesTab(p, tab)).map((p) => p.name));
    const installed: { name: string; project_id: string }[] =
      tab === "mcp" ? installedMCPs : tab === "skill" ? installedSkills : installedPlugins;
    const byName = new Map<string, string[]>();
    for (const item of installed) {
      if (offered.has(item.name)) continue;
      byName.set(item.name, [...(byName.get(item.name) ?? []), item.project_id]);
    }
    const q = search.toLowerCase();
    return [...byName.entries()]
      .filter(([name]) => !q || name.toLowerCase().includes(q))
      .map(([name, scopes]) => ({ name, scopes }))
      .sort((a, b) => a.name.localeCompare(b.name));
  })();

  const handleUninstallOrphan = async (name: string) => {
    setInstalling(`orphan:${name}`);
    try {
      if (tab === "mcp") await marketplaceApi.uninstallMCP(name);
      else if (tab === "skill") await marketplaceApi.uninstallSkill(name);
      else await marketplaceApi.uninstallPlugin(name);
      toast.success(`Uninstalled ${name}`);
      await loadData();
    } catch (error: unknown) {
      toast.error(`Failed to uninstall ${name}`, { description: errorMessage(error) });
    } finally {
      setInstalling(null);
    }
  };

  const tabConfig = {
    mcp: {
      label: "MCP",
      subtitle: "Browse and install MCP servers and agent skills",
      placeholder: "Search MCP servers...",
    },
    skill: {
      label: "Skills",
      subtitle: "Browse and install MCP servers and agent skills",
      placeholder: "Search skills...",
    },
    plugin: {
      label: "Plugins",
      subtitle: "Browse and install plugins (Claude Code only)",
      placeholder: "Search plugins...",
    },
  } as const;

  return (
    <div className="h-full overflow-y-auto">
      {/* Header */}
      <PageHeader title="Customize" subtitle={tabConfig[tab].subtitle} />

      {/* Sticky section: tabs + toolbar */}
      <div className="sticky top-0 z-10 bg-card">
        {/* Tab bar */}
        <div className="border-b">
          <div className="max-w-[51rem] mx-auto px-6 flex items-center justify-between gap-3">
            <div className="flex min-w-0">
              {(["mcp", "skill", "plugin"] as TabType[]).map((tabKey) => (
                <button
                  key={tabKey}
                  onClick={() => setTab(tabKey)}
                  className={cn(
                    "px-4 py-2.5 text-sm font-medium transition-colors relative",
                    tab === tabKey
                      ? "text-foreground after:absolute after:bottom-0 after:left-0 after:right-0 after:h-0.5 after:bg-primary"
                      : "text-muted-foreground hover:text-foreground"
                  )}
                >
                  {tabConfig[tabKey].label}
                </button>
              ))}
            </div>
            <div className="flex items-center gap-2 shrink-0">
              {tab === "mcp" && (
                <button
                  onClick={() => setShowAddMCP(true)}
                  disabled={loading}
                  className="flex h-8 items-center gap-1.5 px-2.5 border rounded-md text-xs font-medium hover:bg-accent transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                >
                  <Plus className="h-3.5 w-3.5" />
                  Add MCP
                </button>
              )}
              <button
                onClick={() => setShowAddMarketplace(true)}
                className="flex h-8 items-center gap-1.5 px-2.5 border rounded-md text-xs font-medium hover:bg-accent transition-colors"
              >
                <FolderPlus className="h-3.5 w-3.5" />
                Add Marketplace
              </button>
            </div>
          </div>
        </div>

        {/* Toolbar */}
        <div className="border-b">
          <div className="max-w-[51rem] mx-auto px-6 pt-3 pb-3 flex items-center gap-3">
            <div className="relative flex-1">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
              <input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={tabConfig[tab].placeholder}
                className="w-full pl-10 pr-4 py-2 text-sm bg-background border rounded-lg outline-none focus:ring-1 focus:ring-primary"
              />
            </div>
            <span className="text-sm text-muted-foreground shrink-0">
              {loading ? "..." : `${filteredPlugins.length} available`}
            </span>
            <button
              onClick={loadData}
              disabled={loading}
              className="p-1.5 rounded hover:bg-accent transition-colors text-muted-foreground hover:text-foreground"
              title="Refresh"
            >
              <RefreshCw className={cn("h-3.5 w-3.5", loading && "animate-spin")} />
            </button>
          </div>
        </div>
      </div>

      {/* Claude Code only notice for Plugins tab */}
      {tab === "plugin" && (
        <div className="max-w-[51rem] mx-auto px-6 pt-3">
          <div className="text-xs text-sky-700 bg-sky-500/10 rounded-lg px-3 py-2">
            Plugins are for Claude Code only. They extend Claude Code with custom mcp, skills, commands, and hooks.
          </div>
        </div>
      )}

      {/* Plugin list */}
      <div className="py-6">
        {loading ? (
          <div className="flex items-center justify-center h-40 text-muted-foreground gap-2">
            <RefreshCw className="h-5 w-5 animate-spin" />
            Loading...
          </div>
        ) : filteredPlugins.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-40 text-muted-foreground gap-2">
            <Package className="h-10 w-10 opacity-30" />
            <p className="text-sm">No {tabConfig[tab].label.toLowerCase()} found</p>
            <p className="text-xs text-muted-foreground/60">
              Add a marketplace directory or git repo to discover {tabConfig[tab].label.toLowerCase()}.
            </p>
          </div>
        ) : (
          <div className="max-w-[51rem] mx-auto w-full px-6 flex flex-col gap-3">
            {filteredPlugins.map((plugin) => (
              <PluginCard
                key={plugin.id}
                plugin={plugin}
                installed={isInstalled(plugin)}
                installedScopes={getInstalledScopes(plugin)}
                projects={projects}
                installing={installing === plugin.id}
                onInstall={() => handleInstallClick(plugin)}
                onUninstall={(scope) => handleInstallConfirm(plugin, getInstalledScopes(plugin).filter((id) => id !== scope), {})}
              />
            ))}
          </div>
        )}
      </div>

      {/* Installed items no longer offered by any source */}
      {!loading && orphanedInstalls.length > 0 && (
        <div className="max-w-[51rem] mx-auto px-6 pb-6">
          <h3 className="text-sm font-medium text-muted-foreground mb-2">
            Installed, but no longer in any marketplace
          </h3>
          <div className="flex flex-col gap-1.5">
            {orphanedInstalls.map(({ name, scopes }) => (
              <div key={name} className="flex items-center gap-2 text-sm bg-muted/50 rounded-lg px-3 py-2">
                <span className="font-medium truncate flex-1">{name}</span>
                <span className="text-[10px] text-muted-foreground">
                  {scopes.includes(SCOPE_ALL)
                    ? "All Projects"
                    : scopes.map((id) => projects.find((p) => p.id === id)?.name ?? id.slice(0, 8)).join(", ")}
                </span>
                <button
                  onClick={() => handleUninstallOrphan(name)}
                  disabled={installing === `orphan:${name}`}
                  className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-red-500 transition-colors disabled:opacity-50"
                  title="Uninstall"
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </button>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Marketplace Sources section */}
      {sources.length > 0 && (
        <div className="max-w-[51rem] mx-auto px-6 pb-6">
          <h3 className="text-sm font-medium text-muted-foreground mb-2">Marketplace Sources</h3>
          <div className="flex flex-col gap-1.5">
            {sources.map((src) => (
              <div key={src.id} className="flex items-center gap-2 text-xs text-muted-foreground bg-muted/50 rounded-lg px-3 py-2">
                <span className="font-mono truncate flex-1">{src.url || src.path}</span>
                <span className="text-[10px] uppercase font-medium px-1.5 py-0.5 rounded bg-muted">{src.type}</span>
                <button
                  onClick={async () => {
                    try {
                      await marketplaceApi.removeSource(src.id);
                      toast.success("Marketplace source removed");
                      loadData();
                    } catch (error: unknown) {
                      toast.error("Failed to remove source", { description: errorMessage(error) });
                    }
                  }}
                  className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-red-500 transition-colors"
                  title="Remove"
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </button>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Install Dialog */}
      {dialogPlugin && (
        <InstallDialog
          plugin={dialogPlugin}
          projects={projects}
          installedScopes={getInstalledScopes(dialogPlugin)}
          saving={installing === dialogPlugin.id}
          onConfirm={(projectIds, envValues) => handleInstallConfirm(dialogPlugin, projectIds, envValues)}
          onClose={() => setDialogPlugin(null)}
        />
      )}

      {/* Add Marketplace Dialog */}
      {showAddMarketplace && (
        <AddMarketplaceDialog
          onClose={() => setShowAddMarketplace(false)}
          onAdded={() => { setShowAddMarketplace(false); loadData(); }}
        />
      )}

      {/* Add MCP Dialog */}
      {showAddMCP && (
        <AddMCPDialog
          onClose={() => setShowAddMCP(false)}
          onConfirm={handleAddMCP}
        />
      )}
    </div>
  );
}

// ── AddMCPDialog ─────────────────────────────────────────────────────────

interface AddMCPDialogProps {
  onClose: () => void;
  onConfirm: (name: string, url: string, headers: Record<string, string>) => Promise<void>;
}

const MCP_NAME_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_-]{0,39}$/;

function parseHeadersInput(input: string): Record<string, string> {
  const headers: Record<string, string> = {};
  const normalizedNames = new Set<string>();
  for (const [index, rawLine] of input.split("\n").entries()) {
    if (!rawLine.trim()) continue;
    const separator = rawLine.indexOf(":");
    if (separator <= 0) {
      throw new Error(`Header line ${index + 1} must use "Name: value"`);
    }
    const name = rawLine.slice(0, separator).trim();
    const value = rawLine.slice(separator + 1).trim();
    if (!/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(name)) {
      throw new Error(`Header line ${index + 1} has an invalid name`);
    }
    const normalizedName = name.toLowerCase();
    if (normalizedNames.has(normalizedName)) {
      throw new Error(`Header line ${index + 1} repeats ${name}`);
    }
    normalizedNames.add(normalizedName);
    headers[name] = value;
  }
  return headers;
}

function AddMCPDialog({ onClose, onConfirm }: AddMCPDialogProps) {
  const [name, setName] = useState("");
  const [url, setURL] = useState("");
  const [headersInput, setHeadersInput] = useState("");
  const [adding, setAdding] = useState(false);

  const handleAdd = async () => {
    try {
      const normalizedName = name.trim();
      if (!MCP_NAME_PATTERN.test(normalizedName)) {
        throw new Error("Name must be 1-40 characters, start with a letter or number, and use only letters, numbers, - or _");
      }
      const parsedURL = new URL(url.trim());
      if (parsedURL.protocol !== "http:" && parsedURL.protocol !== "https:") {
        throw new Error("URL must start with http:// or https://");
      }
      const headers = parseHeadersInput(headersInput);
      setAdding(true);
      await onConfirm(normalizedName, parsedURL.toString(), headers);
    } catch (error) {
      toast.error("Failed to add MCP", {
        description: error instanceof Error ? error.message : String(error),
      });
    } finally {
      setAdding(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50" onClick={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <div className="bg-card border rounded-2xl shadow-2xl w-full max-w-2xl mx-4 p-6 flex flex-col gap-5">
        <div className="flex items-start justify-between gap-2">
          <div>
            <h2 className="text-base font-semibold">Add MCP</h2>
            <p className="text-xs text-muted-foreground mt-0.5">Add a remote MCP to this Marketplace</p>
          </div>
          <button onClick={onClose} className="text-muted-foreground hover:text-foreground text-lg leading-none mt-0.5">&times;</button>
        </div>

        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-1.5">
            <label className="text-sm font-medium">Name</label>
            <input
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="my-mcp"
              maxLength={40}
              className="w-full px-3 py-2 text-sm bg-background border rounded-lg outline-none focus:ring-1 focus:ring-primary font-mono"
              autoFocus
            />
            <p className="text-[11px] text-muted-foreground">1–40 characters. Letters, numbers, hyphens and underscores only.</p>
          </div>
          <div className="flex flex-col gap-1.5">
            <label className="text-sm font-medium">URL</label>
            <input
              value={url}
              onChange={(event) => setURL(event.target.value)}
              placeholder="https://example.com/mcp"
              className="w-full px-3 py-2 text-sm bg-background border rounded-lg outline-none focus:ring-1 focus:ring-primary font-mono"
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <label className="text-sm font-medium">Headers</label>
            <textarea
              value={headersInput}
              onChange={(event) => setHeadersInput(event.target.value)}
              placeholder={"Authorization: Bearer token\nX-Org-ID: organization"}
              rows={5}
              spellCheck={false}
              className="w-full px-3 py-2 text-sm bg-background border rounded-lg outline-none focus:ring-1 focus:ring-primary font-mono resize-y"
            />
            <p className="text-[11px] text-muted-foreground">Optional. Enter one header per line.</p>
          </div>
        </div>

        <div className="flex justify-end gap-2 pt-1">
          <button onClick={onClose} className="px-4 py-2 text-sm rounded-lg border hover:bg-accent transition-colors">
            Cancel
          </button>
          <button
            onClick={handleAdd}
            disabled={!name.trim() || !url.trim() || adding}
            className="px-4 py-2 text-sm rounded-lg bg-primary text-primary-foreground hover:bg-primary/90 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {adding ? "Adding..." : "Add"}
          </button>
        </div>
      </div>
    </div>
  );
}

// ── PluginCard ──────────────────────────────────────────────────────────────

interface PluginCardProps {
  plugin: LocalPlugin;
  installed: boolean;
  installedScopes: string[];
  projects: Project[];
  installing: boolean;
  onInstall: () => void;
  onUninstall: (projectId: string) => void;
}

const typeStyles: Record<string, { bg: string; text: string; icon: "wrench" | "package" }> = {
  mcp:    { bg: "bg-blue-500/10",   text: "text-blue-600",    icon: "wrench" },
  lsp:    { bg: "bg-blue-500/10",   text: "text-blue-600",    icon: "wrench" },
  skill:  { bg: "bg-violet-500/10", text: "text-violet-600",  icon: "package" },
  plugin: { bg: "bg-blue-500/10",   text: "text-blue-600",    icon: "package" },
};

function PluginCard({ plugin, installed, installedScopes, projects, installing, onInstall, onUninstall }: PluginCardProps) {
  const style = typeStyles[plugin.type] || typeStyles.plugin;

  const scopeLabel = (projectId: string) => {
    if (projectId === "") return "All";
    return projects.find((p) => p.id === projectId)?.name ?? projectId.slice(0, 8);
  };

  return (
    <div className="w-full border rounded-xl bg-card p-4 flex gap-4">
      <div className="shrink-0 mt-0.5">
        {style.icon === "wrench" ? (
          <Wrench className="h-5 w-5 text-blue-500" />
        ) : (
          <Package className="h-5 w-5 text-violet-500" />
        )}
      </div>

      <div className="flex-1 min-w-0">
        <div className="flex items-start justify-between gap-2 mb-1">
          <div className="min-w-0 flex items-center flex-wrap gap-x-2 gap-y-0.5">
            <span className="font-semibold text-sm">{plugin.name}</span>
            {plugin.version && (
              <span className="text-[11px] text-muted-foreground/60">v{plugin.version}</span>
            )}
            {plugin.author && (
              <span className="text-[11px] text-muted-foreground/60">by {plugin.author}</span>
            )}
            {plugin.source_label && (
              <span className="relative group/src inline-flex">
                <span className="text-[10px] font-mono text-muted-foreground/60 bg-muted/80 border border-border/50 px-1.5 py-0.5 rounded cursor-default truncate max-w-[180px]">
                  {plugin.source_label}
                </span>
                <span className="pointer-events-none absolute bottom-full left-0 mb-1.5 z-50 hidden group-hover/src:block">
                  <span className="whitespace-nowrap text-[11px] font-mono bg-popover text-popover-foreground border border-border rounded px-2 py-1 shadow-md block">
                    {plugin.source_full || plugin.source_label}
                  </span>
                </span>
              </span>
            )}
          </div>
          <div className="flex items-center gap-1.5 shrink-0">
            {installed && (
              <CheckCircle2 className="h-4 w-4 text-green-500" aria-label="Installed" />
            )}
            <span
              className={cn(
                "text-[10px] font-medium px-1.5 py-0.5 rounded uppercase",
                style.bg,
                style.text,
              )}
            >
              {plugin.type}
            </span>
          </div>
        </div>

        <p className="text-[12px] text-muted-foreground line-clamp-2 mb-2">
          {plugin.description || "No description"}
        </p>

        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-1.5 min-w-0">
            {installed && installedScopes.length > 0 && (
              <div className="flex items-center gap-1 flex-wrap shrink-0">
                {installedScopes.map((sid) => (
                  <span
                    key={sid}
                    className="inline-flex items-center gap-1 text-[10px] font-medium px-1.5 py-0.5 rounded bg-green-500/10 text-green-600"
                  >
                    {scopeLabel(sid)}
                    <button
                      type="button"
                      onClick={() => onUninstall(sid)}
                      disabled={installing}
                      title={`Uninstall from ${sid === SCOPE_ALL ? "All Projects (global scope)" : scopeLabel(sid)}`}
                      aria-label={`Uninstall ${plugin.name} from ${sid === SCOPE_ALL ? "All Projects (global scope)" : scopeLabel(sid)}`}
                      className="leading-none text-green-600/70 hover:text-red-600 disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      ×
                    </button>
                  </span>
                ))}
              </div>
            )}
            {!installed && plugin.command && (
              <span className="text-[10px] font-mono text-muted-foreground/50 truncate">
                {plugin.command} {plugin.args?.join(" ")}
              </span>
            )}
          </div>
          <div className="flex items-center gap-1.5 shrink-0">
            <button
              onClick={onInstall}
              disabled={installing}
              className={cn(
                "text-xs px-3 py-1 rounded-md font-medium transition-colors",
                "bg-primary/10 text-primary hover:bg-primary/20",
                installing && "opacity-50 cursor-not-allowed"
              )}
            >
              {installing ? "Saving..." : "Install"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

// ── InstallDialog ─────────────────────────────────────────────────────────

interface InstallDialogProps {
  plugin: LocalPlugin;
  projects: Project[];
  installedScopes: string[];
  saving: boolean;
  onConfirm: (projectIds: string[], envValues: Record<string, string>) => Promise<boolean>;
  onClose: () => void;
}

function InstallDialog({ plugin, projects, installedScopes, saving, onConfirm, onClose }: InstallDialogProps) {
  // Selected scopes. SCOPE_ALL ("") means "All Projects" (global) and is mutually
  // exclusive with specific project ids; otherwise any number of projects can be picked.
  const [scopes, setScopes] = useState<string[]>(() => [...installedScopes]);
  const [envValues, setEnvValues] = useState<Record<string, string>>({});
  const [revealed, setRevealed] = useState<Record<string, boolean>>({});

  const addedScopes = scopes.filter((id) => !installedScopes.includes(id));
  const removedScopes = installedScopes.filter((id) => !scopes.includes(id));
  const envVars = addedScopes.length > 0 ? plugin.env_vars || [] : [];
  const needsOAuth = plugin.auth_type === "oauth" || plugin.auth_type === "oauth_discovery";
  const envOk = envVars.every((v) => !!envValues[v]?.trim());
  const canSubmit = !saving && envOk && (addedScopes.length > 0 || removedScopes.length > 0);

  const selectableProjects = projects.filter((p) => p.git_repo_path || installedScopes.includes(p.id));
  const missingScopes = installedScopes.filter((id) => id !== SCOPE_ALL && !projects.some((p) => p.id === id));

  const toggleScope = (id: string) => {
    setScopes((prev) => {
      if (id === SCOPE_ALL) {
        // "All Projects" is exclusive: selecting it clears specific projects.
        return prev.includes(SCOPE_ALL) ? [] : [SCOPE_ALL];
      }
      // Selecting a specific project clears "All Projects".
      const withoutAll = prev.filter((s) => s !== SCOPE_ALL);
      return withoutAll.includes(id)
        ? withoutAll.filter((s) => s !== id)
        : [...withoutAll, id];
    });
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50" onClick={(e) => { if (!saving && e.target === e.currentTarget) onClose(); }}>
      <div className="bg-card border rounded-2xl shadow-2xl w-full max-w-xl min-h-[min(32rem,calc(100dvh-2rem))] max-h-[calc(100dvh-2rem)] overflow-y-auto mx-4 p-6 flex flex-col gap-5">
        <div className="flex items-start justify-between gap-2">
          <div>
            <h2 className="text-base font-semibold">Installation settings</h2>
            <p className="text-xs text-muted-foreground mt-0.5">{plugin.name} — Select projects to install. Uncheck to uninstall.</p>
          </div>
          <button onClick={onClose} disabled={saving} aria-label="Close" className="text-muted-foreground hover:text-foreground text-lg leading-none mt-0.5 disabled:opacity-50">&times;</button>
        </div>

        <fieldset disabled={saving} className="contents">
          <div className="flex flex-col gap-1.5">
            <span id="installation-scopes-label" className="text-sm font-medium">Install scope</span>
            <div
              role="group"
              aria-labelledby="installation-scopes-label"
              className="max-h-80 overflow-y-auto overscroll-contain rounded-lg border bg-background divide-y"
            >
              <label className="flex items-center gap-2.5 px-3 py-2.5 text-sm cursor-pointer hover:bg-accent transition-colors">
                <input
                  type="checkbox"
                  checked={scopes.includes(SCOPE_ALL)}
                  onChange={() => toggleScope(SCOPE_ALL)}
                  className="h-4 w-4 shrink-0 accent-primary"
                />
                <span className="min-w-0">
                  <span className="block font-medium">All Projects</span>
                  <span className="block text-xs text-muted-foreground">Includes current and future projects</span>
                </span>
              </label>
              {selectableProjects.map((p) => (
                <label key={p.id} className="flex items-center gap-2.5 px-3 py-2.5 text-sm cursor-pointer hover:bg-accent transition-colors">
                  <input
                    type="checkbox"
                    checked={scopes.includes(p.id)}
                    onChange={() => toggleScope(p.id)}
                    className="h-4 w-4 shrink-0 accent-primary"
                  />
                  <span className="min-w-0 break-words">{p.name}</span>
                </label>
              ))}
              {missingScopes.map((id) => (
                <label key={id} className="flex items-center gap-2.5 px-3 py-2.5 text-sm cursor-pointer hover:bg-accent transition-colors">
                  <input
                    type="checkbox"
                    checked={scopes.includes(id)}
                    onChange={() => toggleScope(id)}
                    className="h-4 w-4 shrink-0 accent-primary"
                  />
                  <span>Unavailable project ({id.slice(0, 8)})</span>
                </label>
              ))}
              {selectableProjects.length === 0 && (
                <p className="px-3 py-2.5 text-xs text-muted-foreground">No projects with a local repo path.</p>
              )}
            </div>
          </div>

          {/* Environment variables */}
          {envVars.length > 0 && (
            <div className="flex flex-col gap-3">
              {envVars.map((varName) => (
                <div key={varName} className="flex flex-col gap-1">
                  <div className="flex items-center gap-2">
                    <label className="text-sm font-medium">{varName}</label>
                    <span className="text-[10px] font-medium px-1.5 py-0.5 rounded bg-blue-500/10 text-blue-600">
                      Required
                    </span>
                  </div>
                  <div className="relative">
                    <input
                      type={revealed[varName] ? "text" : "password"}
                      placeholder="Required"
                      value={envValues[varName] || ""}
                      onChange={(e) => setEnvValues((prev) => ({ ...prev, [varName]: e.target.value }))}
                      className={cn(
                        "w-full pr-9 pl-3 py-2 text-sm bg-background border rounded-lg outline-none focus:ring-1 focus:ring-primary",
                        !envValues[varName] && "border-red-400"
                      )}
                    />
                    <button
                      type="button"
                      onClick={() => setRevealed((prev) => ({ ...prev, [varName]: !prev[varName] }))}
                      className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                    >
                      {revealed[varName] ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                    </button>
                  </div>
                </div>
              ))}
              <p className="text-[11px] text-muted-foreground">These values are saved locally when the MCP is installed.</p>
            </div>
          )}

          {/* OAuth notice */}
          {needsOAuth && addedScopes.length > 0 && envVars.length === 0 && (
            <p className="text-[12px] bg-orange-500/10 text-orange-700 rounded-lg px-3 py-2">
              This MCP uses OAuth. After installing you&apos;ll be prompted to authorize.
            </p>
          )}

        </fieldset>
        {(addedScopes.length > 0 || removedScopes.length > 0) && (
          <p className="text-xs text-muted-foreground" role="status">
            {addedScopes.length} to install · {removedScopes.length} to uninstall
          </p>
        )}
        {/* Actions */}
        <div className="flex justify-end gap-2 pt-1 mt-auto">
          <button
            onClick={onClose}
            disabled={saving}
            className="px-4 py-2 text-sm rounded-lg border hover:bg-accent transition-colors disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            onClick={async () => {
              if (await onConfirm(scopes, envValues)) onClose();
            }}
            disabled={!canSubmit}
            className="px-4 py-2 text-sm rounded-lg bg-primary text-primary-foreground hover:bg-primary/90 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {saving ? "Saving..." : "Save"}
          </button>
        </div>
      </div>
    </div>
  );
}

// ── AddMarketplaceDialog ─────────────────────────────────────────────────

interface AddMarketplaceDialogProps {
  onClose: () => void;
  onAdded: () => void;
}

function AddMarketplaceDialog({ onClose, onAdded }: AddMarketplaceDialogProps) {
  const [input, setInput] = useState("");
  const [adding, setAdding] = useState(false);

  const handleAdd = async () => {
    if (!input.trim()) return;
    setAdding(true);
    try {
      await marketplaceApi.addSource(input.trim());
      toast.success("Marketplace source added");
      onAdded();
    } catch (error: unknown) {
      toast.error("Failed to add marketplace", { description: errorMessage(error) });
    } finally {
      setAdding(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <div className="bg-card border rounded-2xl shadow-2xl w-full max-w-lg mx-4 p-6 flex flex-col gap-5">
        <div className="flex items-start justify-between gap-2">
          <div>
            <h2 className="text-base font-semibold">Add Marketplace</h2>
            <p className="text-xs text-muted-foreground mt-0.5">
              Enter a local directory path or a git repo URL
            </p>
          </div>
          <button onClick={onClose} className="text-muted-foreground hover:text-foreground text-lg leading-none mt-0.5">&times;</button>
        </div>

        <div className="flex flex-col gap-2">
          <input
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder="/path/to/plugins or git@github.com:org/repo.git"
            className="w-full px-3 py-2 text-sm bg-background border rounded-lg outline-none focus:ring-1 focus:ring-primary font-mono"
            onKeyDown={(e) => { if (e.key === "Enter") handleAdd(); }}
            autoFocus
          />
          <p className="text-xs text-muted-foreground">
            Supports Claude{" "}
            <a
              href="https://code.claude.com/docs/en/plugin-marketplaces"
              target="_blank"
              rel="noopener noreferrer"
              className="text-primary underline underline-offset-2 hover:text-primary/80"
            >
              Marketplace format
            </a>
            , or any directory whose subdirectories contain SKILL.md files.
          </p>
        </div>

        <div className="flex justify-end gap-2 pt-1">
          <button
            onClick={onClose}
            className="px-4 py-2 text-sm rounded-lg border hover:bg-accent transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={handleAdd}
            disabled={!input.trim() || adding}
            className="px-4 py-2 text-sm rounded-lg bg-primary text-primary-foreground hover:bg-primary/90 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {adding ? "Adding..." : "Add"}
          </button>
        </div>
      </div>
    </div>
  );
}
