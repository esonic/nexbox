import { useState, useEffect, useCallback } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import {
  Search,
  Plus,
  Package,
  Wrench,
  CheckCircle2,
  RefreshCw,
  Star,
  Download,
  ArrowUpCircle,
} from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import {
  marketplaceApi,
  PACKAGE_CATEGORIES,
  type MarketplacePackage,
  type InstalledMCP,
  type InstalledSkill,
} from "@/lib/api";
import { useAppStore } from "@/stores/app";

type TabType = "mcp" | "skill";

export function MarketplacePage() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const currentProject = useAppStore((s) => s.currentProject);

  const setMarketplaceUpdateCount = useAppStore((s) => s.setMarketplaceUpdateCount);

  const [tab, setTab] = useState<TabType>(
    searchParams.get("tab") === "skill" ? "skill" : "mcp"
  );
  const [search, setSearch] = useState("");
  const [category, setCategory] = useState<string | null>(null);
  const [showInstalledOnly, setShowInstalledOnly] = useState(false);
  const [sort, setSort] = useState("updated");
  const [packages, setPackages] = useState<MarketplacePackage[]>([]);
  const [installedMCPs, setInstalledMCPs] = useState<InstalledMCP[]>([]);
  const [installedSkills, setInstalledSkills] = useState<InstalledSkill[]>([]);
  const [loading, setLoading] = useState(true);

  const loadPackages = useCallback(async () => {
    setLoading(true);
    try {
      const [pkgs, mcps, skills] = await Promise.all([
        marketplaceApi.listPackages({
          type: tab,
          q: search || undefined,
          tag: category || undefined,
          sort,
          limit: 50,
        }),
        marketplaceApi.listInstalledMCPs(),
        marketplaceApi.listInstalledSkills(),
      ]);
      const allPkgs = pkgs || [];
      const allMCPs = mcps || [];
      const allSkills = skills || [];
      setPackages(allPkgs);
      setInstalledMCPs(allMCPs);
      setInstalledSkills(allSkills);

      // Compute how many installed packages have a newer version available
      const pkgById = new Map(allPkgs.map((p) => [p.id, p]));
      let updateCount = 0;
      for (const m of allMCPs) {
        if (!m.package_id || !m.version) continue;
        const pkg = pkgById.get(m.package_id);
        if (pkg && pkg.latest_version && pkg.latest_version !== m.version) updateCount++;
      }
      for (const sk of allSkills) {
        // skills matched by name since package_id isn't on InstalledSkill
        const pkg = allPkgs.find((p) => p.type === "skill" && p.name === sk.name);
        if (pkg && pkg.latest_version && sk.version && pkg.latest_version !== sk.version) updateCount++;
      }
      setMarketplaceUpdateCount(updateCount);
    } catch (e: any) {
      toast.error("Failed to load marketplace", { description: e.message });
    } finally {
      setLoading(false);
    }
  }, [tab, search, category, sort, setMarketplaceUpdateCount]);

  useEffect(() => {
    loadPackages();
  }, [loadPackages]);

  const isInstalled = (pkg: MarketplacePackage) =>
    pkg.type === "mcp"
      ? installedMCPs.some((m) => m.name === pkg.name)
      : installedSkills.some((s) => s.name === pkg.name);

  const installedScope = (pkg: MarketplacePackage): string | null => {
    if (!isInstalled(pkg)) return null;
    const pid = pkg.type === "mcp"
      ? installedMCPs.find((m) => m.name === pkg.name)?.project_id
      : installedSkills.find((s) => s.name === pkg.name)?.project_id;
    return pid ? null : "All";
  };

  const hasUpdate = (pkg: MarketplacePackage) => {
    if (!pkg.latest_version) return false;
    if (pkg.type === "mcp") {
      const m = installedMCPs.find((m) => m.package_id === pkg.id || m.name === pkg.name);
      return !!m?.version && m.version !== pkg.latest_version;
    } else {
      const sk = installedSkills.find((s) => s.name === pkg.name);
      return !!sk?.version && sk.version !== pkg.latest_version;
    }
  };

  const displayedPackages = showInstalledOnly
    ? packages.filter(isInstalled)
    : packages;

  const tabConfig = {
    mcp: {
      label: "MCP Servers",
      subtitle: "Browse and install MCP servers",
      placeholder: "Search MCP servers...",
      publishLabel: "Publish MCP",
    },
    skill: {
      label: "Agent Skills",
      subtitle: "Browse and install agent skills",
      placeholder: "Search skills...",
      publishLabel: "Publish Skill",
    },
  } as const;

  return (
    <div className="h-full overflow-y-auto">
      {/* Header — scrolls away */}
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
        {/* Tab bar — border spans full width */}
        <div className="border-b border-t">
          <div className="max-w-3xl mx-auto px-6 flex">
            {(["mcp", "skill"] as TabType[]).map((t) => (
              <button
                key={t}
                onClick={() => { setTab(t); setCategory(null); setShowInstalledOnly(false); }}
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

        {/* Toolbar: Search + Publish + Count + Filters — single area with one border */}
        <div className="border-b">
          <div className="max-w-3xl mx-auto px-6 pt-3 pb-2 flex items-center gap-3">
            <div className="relative flex-1">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
              <input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={tabConfig[tab].placeholder}
                className="w-full pl-10 pr-4 py-2 text-sm bg-background border rounded-lg outline-none focus:ring-1 focus:ring-primary"
              />
            </div>
            <button
              onClick={() => navigate(`/marketplace/publish?type=${tab}`)}
              className="flex items-center gap-1.5 px-3 py-2 bg-primary text-primary-foreground rounded-lg text-sm font-medium hover:bg-primary/90 transition-colors shrink-0"
            >
              <Plus className="h-4 w-4" />
              {tabConfig[tab].publishLabel}
            </button>
          </div>
          <div className="max-w-3xl mx-auto px-6 pb-2 flex items-center">
            <span className="text-sm text-muted-foreground">
              {loading ? (
                <span className="opacity-50">Loading...</span>
              ) : (
                <>
                  <span className="font-semibold text-foreground">{displayedPackages.length}</span>
                  {" "}{tab === "mcp" ? "MCP server" : "skill"}{displayedPackages.length !== 1 ? "s" : ""}
                </>
              )}
            </span>

            <div className="ml-auto flex items-center gap-4">
              <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
                Filter
                <select
                  value={showInstalledOnly ? "__installed__" : (category ?? "")}
                  onChange={(e) => {
                    const v = e.target.value;
                    if (v === "__installed__") {
                      setShowInstalledOnly(true);
                      setCategory(null);
                    } else {
                      setShowInstalledOnly(false);
                      setCategory(v || null);
                    }
                  }}
                  className="text-xs bg-background border rounded-lg px-2 py-1 outline-none focus:ring-1 focus:ring-primary text-foreground"
                >
                  <option value="">All</option>
                  <option value="__installed__">Installed</option>
                  {PACKAGE_CATEGORIES.map((cat) => (
                    <option key={cat.value} value={cat.value} title={cat.description}>
                      {cat.label}
                    </option>
                  ))}
                </select>
              </label>

              <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
                Sort by
                <select
                  value={sort}
                  onChange={(e) => setSort(e.target.value)}
                  className="text-xs bg-background border rounded-lg px-2 py-1 outline-none focus:ring-1 focus:ring-primary text-foreground"
                >
                  <option value="updated">Recently updated</option>
                  <option value="installs">Most installs</option>
                  <option value="stars">Most stars</option>
                  <option value="created">Oldest first</option>
                  <option value="name">Name</option>
                </select>
              </label>

              <button
                onClick={loadPackages}
                disabled={loading}
                className="p-1 rounded hover:bg-accent transition-colors text-muted-foreground hover:text-foreground"
                title="Refresh"
              >
                <RefreshCw className={cn("h-3.5 w-3.5", loading && "animate-spin")} />
              </button>
            </div>
          </div>
        </div>
      </div>

      {/* Package list */}

      <div className="py-6">
        {loading ? (
          <div className="flex items-center justify-center h-40 text-muted-foreground gap-2">
            <RefreshCw className="h-5 w-5 animate-spin" />
            Loading...
          </div>
        ) : displayedPackages.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-40 text-muted-foreground gap-2">
            <Package className="h-10 w-10 opacity-30" />
            <p className="text-sm">{showInstalledOnly ? "No installed packages" : "No packages found"}</p>
            {!showInstalledOnly && (
              <button
                onClick={() => navigate("/marketplace/publish")}
                className="text-xs text-primary hover:underline"
              >
                Publish the first one
              </button>
            )}
          </div>
        ) : (
          <div className="max-w-3xl mx-auto w-full px-6 flex flex-col gap-3">
            {displayedPackages.map((pkg) => (
              <PackageCard
                key={pkg.id}
                pkg={pkg}
                installed={isInstalled(pkg)}
                installedScope={installedScope(pkg)}
                updateAvailable={hasUpdate(pkg)}
                onViewDetail={() => navigate(`/marketplace/${pkg.id}`)}
              />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

// ── PackageCard ──────────────────────────────────────────────────────────────

interface PackageCardProps {
  pkg: MarketplacePackage;
  installed: boolean;
  installedScope: string | null;
  updateAvailable: boolean;
  onViewDetail: () => void;
}

function categoryLabel(tags: string[] | undefined): string | null {
  if (!tags?.length) return null;
  const cat = PACKAGE_CATEGORIES.find((c) => c.value === tags[0]);
  return cat?.label ?? null;
}

function PackageCard({ pkg, installed, installedScope, updateAvailable, onViewDetail }: PackageCardProps) {
  const catLabel = categoryLabel(pkg.tags);

  return (
    <button
      onClick={onViewDetail}
      className="w-full border rounded-xl bg-card p-4 flex gap-4 hover:shadow-sm hover:border-border/80 transition-all text-left"
    >
      {/* Icon */}
      <div className="shrink-0 mt-0.5">
        {pkg.type === "mcp" ? (
          <Wrench className="h-5 w-5 text-blue-500" />
        ) : (
          <Package className="h-5 w-5 text-violet-500" />
        )}
      </div>

      {/* Content */}
      <div className="flex-1 min-w-0">
        <div className="flex items-start justify-between gap-2 mb-1">
          <div className="min-w-0">
            <span className="font-semibold text-sm">{pkg.title}</span>
            <span className="text-[11px] text-muted-foreground/60 ml-2">{pkg.name}</span>
          </div>
          <div className="flex items-center gap-1.5 shrink-0">
            {updateAvailable && (
              <ArrowUpCircle className="h-4 w-4 text-amber-500" aria-label="Update available" />
            )}
            {installed && !updateAvailable && (
              <CheckCircle2 className="h-4 w-4 text-green-500" aria-label="Installed" />
            )}
            {installedScope && (
              <span className="text-[10px] font-mono text-muted-foreground bg-muted px-1 py-0.5 rounded">
                {installedScope}
              </span>
            )}
            <span
              className={cn(
                "text-[10px] font-medium px-1.5 py-0.5 rounded uppercase",
                pkg.type === "mcp"
                  ? "bg-blue-500/10 text-blue-600"
                  : "bg-violet-500/10 text-violet-600"
              )}
            >
              {pkg.type}
            </span>
          </div>
        </div>

        <p className="text-[12px] text-muted-foreground line-clamp-2 mb-2">
          {pkg.description || "No description"}
        </p>

        <div className="flex items-center gap-3">
          <span className="text-[11px] text-muted-foreground/50">
            v{pkg.latest_version || "0.1.0"} · {pkg.author_id}
          </span>
          {catLabel && (
            <span className="text-[10px] font-medium px-1.5 py-0.5 rounded-full bg-muted text-muted-foreground">
              {catLabel}
            </span>
          )}
          <span className="flex items-center gap-0.5 text-[11px] text-muted-foreground/60">
            <Star className="h-3 w-3" />
            {pkg.star_count ?? 0}
          </span>
          <span className="flex items-center gap-0.5 text-[11px] text-muted-foreground/60">
            <Download className="h-3 w-3" />
            {pkg.install_count ?? 0}
          </span>
        </div>
      </div>
    </button>
  );
}
