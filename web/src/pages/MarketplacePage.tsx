import { useState, useEffect, useCallback, useRef } from "react";
import { useTranslation } from "react-i18next";
import {
  Search,
  Package,
  Wrench,
  CheckCircle2,
  RefreshCw,
  Eye,
  EyeOff,
  ChevronDown,
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
} from "@/lib/api";
import type { Project } from "@/types";

type TabType = "connector" | "skill" | "plugin";

// Scope sentinel value meaning "install to all projects".
const SCOPE_ALL = "";

export function MarketplacePage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<TabType>("connector");
  const [search, setSearch] = useState("");
  const [plugins, setPlugins] = useState<LocalPlugin[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [installedMCPs, setInstalledMCPs] = useState<InstalledMCP[]>([]);
  const [installedSkills, setInstalledSkills] = useState<InstalledSkill[]>([]);
  const [installedPlugins, setInstalledPlugins] = useState<InstalledPluginEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [installing, setInstalling] = useState<string | null>(null);
  // Install dialog state
  const [dialogPlugin, setDialogPlugin] = useState<LocalPlugin | null>(null);

  const loadData = useCallback(async () => {
    setLoading(true);
    try {
      const [allPlugins, mcps, skills, pluginEntries, projs] = await Promise.all([
        marketplaceApi.listPlugins(),
        marketplaceApi.listInstalledMCPs(),
        marketplaceApi.listInstalledSkills(),
        marketplaceApi.listInstalledPlugins(),
        projectsApi.list(),
      ]);
      setPlugins(allPlugins || []);
      setInstalledMCPs(mcps || []);
      setInstalledSkills(skills || []);
      setInstalledPlugins(pluginEntries || []);
      setProjects(projs || []);
    } catch (e: any) {
      toast.error("Failed to load marketplace", { description: e.message });
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
      case "lsp":
        return installedPlugins.some((p) => p.name === plugin.name);
      case "skill":
        return installedSkills.some((s) => s.name === plugin.name);
      case "plugin":
        return installedPlugins.some((p) => p.name === plugin.name);
      default:
        return false;
    }
  };

  // Called by the Install button — opens the dialog.
  const handleInstallClick = (plugin: LocalPlugin) => {
    setDialogPlugin(plugin);
  };

  // Called by InstallDialog on confirm.
  const handleInstallConfirm = async (
    plugin: LocalPlugin,
    projectId: string,
    envValues: Record<string, string>,
  ) => {
    setInstalling(plugin.id);
    setDialogPlugin(null);
    try {
      if (plugin.type === "mcp") {
        const mcpConfig = plugin.mcp_config ? JSON.parse(plugin.mcp_config) : {};
        const manifest: Record<string, unknown> = mcpConfig.mcpServers
          ? { mcpServers: mcpConfig.mcpServers }
          : { mcpServers: { [plugin.name]: { command: plugin.command, ...(plugin.args?.length ? { args: plugin.args } : {}) } } };
        await marketplaceApi.installMCP({
          name: plugin.name,
          version: plugin.version || "0.0.0",
          mcp_key: plugin.name,
          manifest,
          env: envValues,
          project_id: projectId || undefined,
          auth_type: plugin.auth_type,
        });
        toast.success(`Installed connector: ${plugin.name}`);
      } else if (plugin.type === "lsp") {
        await marketplaceApi.installPlugin({
          name: plugin.name,
          install_path: plugin.install_path || undefined,
          lsp_config: plugin.lsp_config || undefined,
          project_id: projectId || undefined,
        });
        toast.success(`Installed LSP plugin: ${plugin.name}`);
      } else if (plugin.type === "skill") {
        await marketplaceApi.installSkill({
          name: plugin.name,
          package_id: plugin.id,
          version: plugin.version || "0.0.0",
          install_path: plugin.install_path,
          project_id: projectId || undefined,
        });
        toast.success(`Installed skill: ${plugin.name}`);
      } else if (plugin.type === "plugin") {
        await marketplaceApi.installPlugin({
          name: plugin.name,
          install_path: plugin.install_path || "",
          project_id: projectId || undefined,
        });
        toast.success(`Installed plugin: ${plugin.name}`);
      }
      await loadData();
    } catch (e: any) {
      toast.error(`Failed to install ${plugin.name}`, { description: e.message });
    } finally {
      setInstalling(null);
    }
  };

  const handleUninstall = async (plugin: LocalPlugin) => {
    setInstalling(plugin.id);
    try {
      if (plugin.type === "mcp") {
        await marketplaceApi.uninstallMCP(plugin.name);
        toast.success(`Uninstalled connector: ${plugin.name}`);
      } else if (plugin.type === "lsp") {
        await marketplaceApi.uninstallPlugin(plugin.name);
        toast.success(`Uninstalled LSP plugin: ${plugin.name}`);
      } else if (plugin.type === "skill") {
        await marketplaceApi.uninstallSkill(plugin.name);
        toast.success(`Uninstalled skill: ${plugin.name}`);
      } else if (plugin.type === "plugin") {
        await marketplaceApi.uninstallPlugin(plugin.name);
        toast.success(`Uninstalled plugin: ${plugin.name}`);
      }
      await loadData();
    } catch (e: any) {
      toast.error(`Failed to uninstall ${plugin.name}`, { description: e.message });
    } finally {
      setInstalling(null);
    }
  };

  const matchesTab = (p: LocalPlugin, currentTab: TabType) => {
    switch (currentTab) {
      case "connector":
        return p.type === "mcp";
      case "skill":
        return p.type === "skill";
      case "plugin":
        return p.type === "plugin" || p.type === "lsp";
      default:
        return false;
    }
  };

  const filteredPlugins = plugins.filter((p) => {
    if (!matchesTab(p, tab)) return false;
    if (search) {
      const q = search.toLowerCase();
      return (
        p.name.toLowerCase().includes(q) ||
        p.description?.toLowerCase().includes(q)
      );
    }
    return true;
  });

  const tabConfig = {
    connector: {
      label: t("customize.connectors"),
      subtitle: t("customize.connectors_subtitle"),
      placeholder: t("customize.connectors_search"),
    },
    skill: {
      label: t("customize.skills"),
      subtitle: t("customize.skills_subtitle"),
      placeholder: t("customize.skills_search"),
    },
    plugin: {
      label: t("customize.plugins"),
      subtitle: t("customize.plugins_subtitle"),
      placeholder: t("customize.plugins_search"),
    },
  };

  return (
    <div className="h-full overflow-y-auto">
      {/* Header */}
      <div className="bg-card">
        <div className="max-w-3xl mx-auto px-6 pt-6 pb-5">
          <h1 className="text-xl font-bold">{t("customize.title")}</h1>
          <p className="text-sm text-muted-foreground mt-0.5">
            {tabConfig[tab].subtitle}
          </p>
        </div>
      </div>

      {/* Sticky section: tabs + toolbar */}
      <div className="sticky top-0 z-10 bg-card">
        {/* Tab bar */}
        <div className="border-b border-t">
          <div className="max-w-3xl mx-auto px-6 flex">
            {(["connector", "skill", "plugin"] as TabType[]).map((tabKey) => (
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
        </div>

        {/* Toolbar */}
        <div className="border-b">
          <div className="max-w-3xl mx-auto px-6 pt-3 pb-3 flex items-center gap-3">
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
              {loading ? "..." : t("customize.available_count", { count: filteredPlugins.length })}
            </span>
            <button
              onClick={loadData}
              disabled={loading}
              className="p-1.5 rounded hover:bg-accent transition-colors text-muted-foreground hover:text-foreground"
              title={t("customize.refresh")}
            >
              <RefreshCw className={cn("h-3.5 w-3.5", loading && "animate-spin")} />
            </button>
          </div>
        </div>
      </div>

      {/* Plugin list */}
      <div className="py-6">
        {loading ? (
          <div className="flex items-center justify-center h-40 text-muted-foreground gap-2">
            <RefreshCw className="h-5 w-5 animate-spin" />
            {t("customize.loading")}
          </div>
        ) : filteredPlugins.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-40 text-muted-foreground gap-2">
            <Package className="h-10 w-10 opacity-30" />
            <p className="text-sm">{t("customize.no_plugins")}</p>
            <p className="text-xs text-muted-foreground/60">
              {t("customize.no_plugins_hint")}
            </p>
          </div>
        ) : (
          <div className="max-w-3xl mx-auto w-full px-6 flex flex-col gap-3">
            {filteredPlugins.map((plugin) => (
              <PluginCard
                key={plugin.id}
                plugin={plugin}
                installed={isInstalled(plugin)}
                installing={installing === plugin.id}
                onInstall={() => handleInstallClick(plugin)}
                onUninstall={() => handleUninstall(plugin)}
              />
            ))}
          </div>
        )}
      </div>

      {/* Install Dialog */}
      {dialogPlugin && (
        <InstallDialog
          plugin={dialogPlugin}
          projects={projects}
          onConfirm={(projectId, envValues) => handleInstallConfirm(dialogPlugin, projectId, envValues)}
          onClose={() => setDialogPlugin(null)}
        />
      )}

    </div>
  );
}

// ── PluginCard ──────────────────────────────────────────────────────────────

interface PluginCardProps {
  plugin: LocalPlugin;
  installed: boolean;
  installing: boolean;
  onInstall: () => void;
  onUninstall: () => void;
}

const typeStyles: Record<string, { bg: string; text: string; icon: "wrench" | "package" }> = {
  mcp:    { bg: "bg-blue-500/10",   text: "text-blue-600",    icon: "wrench" },
  lsp:    { bg: "bg-blue-500/10",   text: "text-blue-600",    icon: "wrench" },
  skill:  { bg: "bg-violet-500/10", text: "text-violet-600",  icon: "package" },
  plugin: { bg: "bg-amber-500/10",  text: "text-amber-600",   icon: "package" },
};

function PluginCard({ plugin, installed, installing, onInstall, onUninstall }: PluginCardProps) {
  const { t } = useTranslation();
  const style = typeStyles[plugin.type] || typeStyles.plugin;
  const displayLabel = plugin.source === "connector" ? "connector" : plugin.type;

  return (
    <div className="w-full border rounded-xl bg-card p-4 flex gap-4">
      {/* Icon */}
      <div className="shrink-0 mt-0.5">
        {style.icon === "wrench" ? (
          <Wrench className="h-5 w-5 text-blue-500" />
        ) : (
          <Package className="h-5 w-5 text-violet-500" />
        )}
      </div>

      {/* Content */}
      <div className="flex-1 min-w-0">
        <div className="flex items-start justify-between gap-2 mb-1">
          <div className="min-w-0">
            <span className="font-semibold text-sm">{plugin.name}</span>
            {plugin.version && (
              <span className="text-[11px] text-muted-foreground/60 ml-2">v{plugin.version}</span>
            )}
            {plugin.author && (
              <span className="text-[11px] text-muted-foreground/60 ml-2">
                {t("customize.by_author", { author: plugin.author })}
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
              {displayLabel}
            </span>
          </div>
        </div>

        <p className="text-[12px] text-muted-foreground line-clamp-2 mb-2">
          {plugin.description || t("customize.no_description")}
        </p>

        <div className="flex items-center justify-between gap-2">
          {plugin.command ? (
            <span className="text-[10px] font-mono text-muted-foreground/50 truncate">
              {plugin.command} {plugin.args?.join(" ")}
            </span>
          ) : (
            <span />
          )}
          <div className="flex items-center gap-1.5 shrink-0">
            {/* Install / Uninstall */}
            <button
              onClick={installed ? onUninstall : onInstall}
              disabled={installing}
              className={cn(
                "text-xs px-3 py-1 rounded-md font-medium transition-colors",
                installed
                  ? "bg-red-500/10 text-red-600 hover:bg-red-500/20"
                  : "bg-primary/10 text-primary hover:bg-primary/20",
                installing && "opacity-50 cursor-not-allowed"
              )}
            >
              {installing ? "..." : installed ? t("customize.uninstall") : t("customize.install")}
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
  onConfirm: (projectId: string, envValues: Record<string, string>) => void;
  onClose: () => void;
}

function InstallDialog({ plugin, projects, onConfirm, onClose }: InstallDialogProps) {
  const [scope, setScope] = useState<string>(SCOPE_ALL);
  const [envValues, setEnvValues] = useState<Record<string, string>>({});
  const [revealed, setRevealed] = useState<Record<string, boolean>>({});
  const [scopeOpen, setScopeOpen] = useState(false);
  const dropRef = useRef<HTMLDivElement>(null);

  const envVars = plugin.env_vars || [];
  const needsOAuth = plugin.auth_type === "oauth" || plugin.auth_type === "oauth_discovery";
  const canSubmit = envVars.every((v) => !!envValues[v]?.trim() || needsOAuth);

  // Close dropdown on outside click
  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (dropRef.current && !dropRef.current.contains(e.target as Node)) {
        setScopeOpen(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, []);

  const scopeLabel = scope === SCOPE_ALL
    ? "All Projects"
    : projects.find((p) => p.id === scope)?.name ?? scope;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <div className="bg-card border rounded-2xl shadow-2xl w-full max-w-md mx-4 p-6 flex flex-col gap-5">
        {/* Header */}
        <div className="flex items-start justify-between gap-2">
          <div>
            <h2 className="text-base font-semibold">Install {plugin.name}</h2>
            <p className="text-xs text-muted-foreground mt-0.5">Configure environment variables and install scope</p>
          </div>
          <button onClick={onClose} className="text-muted-foreground hover:text-foreground text-lg leading-none mt-0.5">×</button>
        </div>

        {/* Scope selector */}
        <div className="flex flex-col gap-1.5">
          <label className="text-sm font-medium">Install scope</label>
          <p className="text-[11px] text-muted-foreground">
            "All Projects" installs into every project's folder.
          </p>
          <div ref={dropRef} className="relative">
            <button
              type="button"
              onClick={() => setScopeOpen((v) => !v)}
              className="w-full flex items-center justify-between border rounded-lg px-3 py-2.5 text-sm bg-background hover:bg-accent transition-colors"
            >
              <span>{scopeLabel}</span>
              <ChevronDown className="h-4 w-4 text-muted-foreground" />
            </button>
            {scopeOpen && (
              <div className="absolute top-full left-0 right-0 mt-1 bg-card border rounded-lg shadow-lg z-10 max-h-48 overflow-y-auto">
                <button
                  className={cn("w-full text-left px-3 py-2 text-sm hover:bg-accent transition-colors", scope === SCOPE_ALL && "font-medium")}
                  onClick={() => { setScope(SCOPE_ALL); setScopeOpen(false); }}
                >
                  All Projects
                </button>
                {projects.map((p) => (
                  <button
                    key={p.id}
                    className={cn("w-full text-left px-3 py-2 text-sm hover:bg-accent transition-colors", scope === p.id && "font-medium")}
                    onClick={() => { setScope(p.id); setScopeOpen(false); }}
                  >
                    {p.name}
                  </button>
                ))}
              </div>
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
                    {needsOAuth ? "OAuth" : "Required"}
                  </span>
                </div>
                <div className="relative">
                  <input
                    type={revealed[varName] ? "text" : "password"}
                    placeholder={needsOAuth ? "Set via OAuth" : "Required"}
                    value={envValues[varName] || ""}
                    onChange={(e) => setEnvValues((prev) => ({ ...prev, [varName]: e.target.value }))}
                    className={cn(
                      "w-full pr-9 pl-3 py-2 text-sm bg-background border rounded-lg outline-none focus:ring-1 focus:ring-primary",
                      !envValues[varName] && !needsOAuth && "border-red-400"
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
            <p className="text-[11px] text-muted-foreground">These values are stored locally and injected at runtime.</p>
          </div>
        )}

        {/* OAuth notice */}
        {needsOAuth && envVars.length === 0 && (
          <p className="text-[12px] text-muted-foreground bg-orange-500/10 text-orange-700 rounded-lg px-3 py-2">
            This connector uses OAuth. After installing you'll be prompted to authorize.
          </p>
        )}

        {/* Actions */}
        <div className="flex justify-end gap-2 pt-1">
          <button
            onClick={onClose}
            className="px-4 py-2 text-sm rounded-lg border hover:bg-accent transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={() => onConfirm(scope, envValues)}
            disabled={!canSubmit}
            className="px-4 py-2 text-sm rounded-lg bg-primary text-primary-foreground hover:bg-primary/90 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            Install
          </button>
        </div>
      </div>
    </div>
  );
}

