import { useState, useEffect, useCallback } from "react";
import {
  Search,
  Package,
  Wrench,
  CheckCircle2,
  RefreshCw,
} from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import {
  marketplaceApi,
  type LocalPlugin,
  type InstalledMCP,
  type InstalledSkill,
} from "@/lib/api";

type TabType = "mcp" | "skill";

export function MarketplacePage() {
  const [tab, setTab] = useState<TabType>("mcp");
  const [search, setSearch] = useState("");
  const [plugins, setPlugins] = useState<LocalPlugin[]>([]);
  const [installedMCPs, setInstalledMCPs] = useState<InstalledMCP[]>([]);
  const [installedSkills, setInstalledSkills] = useState<InstalledSkill[]>([]);
  const [loading, setLoading] = useState(true);

  const loadPlugins = useCallback(async () => {
    setLoading(true);
    try {
      const [allPlugins, mcps, skills] = await Promise.all([
        marketplaceApi.listPlugins(),
        marketplaceApi.listInstalledMCPs(),
        marketplaceApi.listInstalledSkills(),
      ]);
      setPlugins(allPlugins || []);
      setInstalledMCPs(mcps || []);
      setInstalledSkills(skills || []);
    } catch (e: any) {
      toast.error("Failed to load marketplace", { description: e.message });
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadPlugins();
  }, [loadPlugins]);

  const isInstalled = (plugin: LocalPlugin) =>
    plugin.type === "mcp"
      ? installedMCPs.some((m) => m.name === plugin.name)
      : installedSkills.some((s) => s.name === plugin.name);

  const filteredPlugins = plugins.filter((p) => {
    if (p.type !== tab) return false;
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
    mcp: {
      label: "MCP Servers",
      subtitle: "Browse and install MCP servers",
      placeholder: "Search MCP servers...",
    },
    skill: {
      label: "Agent Skills",
      subtitle: "Browse and install agent skills",
      placeholder: "Search skills...",
    },
  } as const;

  return (
    <div className="h-full overflow-y-auto">
      {/* Header */}
      <div className="bg-card">
        <div className="max-w-3xl mx-auto px-6 pt-6 pb-5">
          <h1 className="text-xl font-bold">Marketplace</h1>
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
            {(["mcp", "skill"] as TabType[]).map((t) => (
              <button
                key={t}
                onClick={() => setTab(t)}
                className={cn(
                  "px-4 py-2.5 text-sm font-medium transition-colors relative",
                  tab === t
                    ? "text-foreground after:absolute after:bottom-0 after:left-0 after:right-0 after:h-0.5 after:bg-primary"
                    : "text-muted-foreground hover:text-foreground"
                )}
              >
                {tabConfig[t].label}
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
              {loading ? "..." : `${filteredPlugins.length} available`}
            </span>
            <button
              onClick={loadPlugins}
              disabled={loading}
              className="p-1.5 rounded hover:bg-accent transition-colors text-muted-foreground hover:text-foreground"
              title="Refresh"
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
            Loading...
          </div>
        ) : filteredPlugins.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-40 text-muted-foreground gap-2">
            <Package className="h-10 w-10 opacity-30" />
            <p className="text-sm">No plugins found</p>
            <p className="text-xs text-muted-foreground/60">
              Admin can add plugins by placing JSON files in the marketplace directory
            </p>
          </div>
        ) : (
          <div className="max-w-3xl mx-auto w-full px-6 flex flex-col gap-3">
            {filteredPlugins.map((plugin) => (
              <PluginCard
                key={plugin.id}
                plugin={plugin}
                installed={isInstalled(plugin)}
              />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

// ── PluginCard ──────────────────────────────────────────────────────────────

interface PluginCardProps {
  plugin: LocalPlugin;
  installed: boolean;
}

function PluginCard({ plugin, installed }: PluginCardProps) {
  return (
    <div className="w-full border rounded-xl bg-card p-4 flex gap-4">
      {/* Icon */}
      <div className="shrink-0 mt-0.5">
        {plugin.type === "mcp" ? (
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
          </div>
          <div className="flex items-center gap-1.5 shrink-0">
            {installed && (
              <CheckCircle2 className="h-4 w-4 text-green-500" aria-label="Installed" />
            )}
            <span
              className={cn(
                "text-[10px] font-medium px-1.5 py-0.5 rounded uppercase",
                plugin.type === "mcp"
                  ? "bg-blue-500/10 text-blue-600"
                  : "bg-violet-500/10 text-violet-600"
              )}
            >
              {plugin.type}
            </span>
          </div>
        </div>

        <p className="text-[12px] text-muted-foreground line-clamp-2 mb-1">
          {plugin.description || "No description"}
        </p>

        {plugin.command && (
          <span className="text-[10px] font-mono text-muted-foreground/50">
            {plugin.command} {plugin.args?.join(" ")}
          </span>
        )}
      </div>
    </div>
  );
}
