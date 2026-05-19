import { useState, useEffect, useCallback, useRef } from "react";
import { useSearchParams } from "react-router-dom";
import { toast } from "sonner";
import { FileText, BookOpen, Loader2, ArrowLeft, Plus, AtSign, Trash2, Search } from "lucide-react";
import { UserChip } from "@/plugins/task/components/UserPicker";
import { assetsApi, marketplaceApi } from "@/lib/api";
import { globalWS } from "@/lib/ws";
import { cn, formatRelativeDate } from "@/lib/utils";
import { shortId } from "@/plugins/task/task-config";
import { useLarkMCP } from "@/lib/useLarkMCP";
import type { Asset, AssetType } from "@/types";
import { AssetEditor } from "./AssetEditor";

const ASSET_TYPES: { value: AssetType; label: string }[] = [
  { value: "requirement_doc", label: "Requirement" },
  { value: "design_doc", label: "Design" },
  { value: "code_change", label: "Code Change" },
  { value: "unit_test", label: "Unit Test" },
  { value: "test_case", label: "Test Case" },
  { value: "lark_doc", label: "Lark Doc" },
];

const TYPE_LABELS: Record<string, string> = Object.fromEntries(
  ASSET_TYPES.map((t) => [t.value, t.label])
);

const TYPE_BADGE_COLORS: Record<string, string> = {
  requirement_doc: "bg-blue-500/15 text-blue-400",
  design_doc: "bg-violet-500/15 text-violet-400",
  code_change: "bg-emerald-500/15 text-emerald-400",
  unit_test: "bg-amber-500/15 text-amber-400",
  test_case: "bg-rose-500/15 text-rose-400",
  lark_doc: "bg-sky-500/10 text-sky-500",
};

const TYPE_COLORS: Record<string, string> = {
  requirement_doc: "text-blue-400",
  design_doc: "text-violet-400",
  code_change: "text-emerald-400",
  unit_test: "text-amber-400",
  test_case: "text-rose-400",
  lark_doc: "text-sky-500",
};

export function AssetPanel({ projectId }: { projectId: string }) {
  const [assets, setAssets] = useState<Asset[]>([]);
  const [loading, setLoading] = useState(true);
  const [searchParams, setSearchParams] = useSearchParams();
  const [editingId, setEditingId] = useState<string | null>(() => searchParams.get("open_asset"));
  const [initialLine, setInitialLine] = useState<number | undefined>(() => {
    const line = searchParams.get("line");
    return line ? parseInt(line) : undefined;
  });
  const [initialCommentId, setInitialCommentId] = useState<string | undefined>(() => searchParams.get("comment_id") ?? undefined);
  const isFirstMount = useRef(true);
  const { hasLarkMCP, larkMCP } = useLarkMCP(projectId);

  // Clear URL params consumed on mount
  useEffect(() => {
    if (searchParams.get("open_asset")) {
      setSearchParams((prev) => { prev.delete("open_asset"); prev.delete("line"); prev.delete("comment_id"); return prev; }, { replace: true });
    }
  }, []);
  const [hasMore, setHasMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const PAGE_SIZE = 30;

  // New asset form
  const [showForm, setShowForm] = useState(false);
  const [formTitle, setFormTitle] = useState("");
  const [formType, setFormType] = useState<AssetType>("design_doc");
  const [formContent, setFormContent] = useState("");
  const [saving, setSaving] = useState(false);

  // Lark doc form
  const [larkUrl, setLarkUrl] = useState("");
  const [fetching, setFetching] = useState(false);

  // Search / filter
  const [showSearch, setShowSearch] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const searchRef = useRef<HTMLInputElement>(null);

  // Delete confirmation
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);

  useEffect(() => {
    if (!confirmDeleteId) return;
    const handler = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (target.closest("[data-delete-confirm]")) return;
      setConfirmDeleteId(null);
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [confirmDeleteId]);

  const handleDelete = async (id: string) => {
    try {
      await assetsApi.delete(id);
      setAssets((prev) => prev.filter((a) => a.id !== id));
    } catch (e) {
      console.error("Failed to delete asset:", e);
      toast.error("Failed to delete asset", { description: String(e) });
    }
  };

  const load = useCallback(async () => {
    if (!projectId) { setLoading(false); return; }
    try {
      const data = await assetsApi.list(projectId, PAGE_SIZE, 0);
      setAssets(data || []);
      setHasMore((data || []).length >= PAGE_SIZE);
    } catch (e) {
      console.error("Failed to load assets:", e);
    } finally {
      setLoading(false);
    }
  }, [projectId]);

  const loadMore = async () => {
    if (!projectId) return;
    setLoadingMore(true);
    try {
      const more = await assetsApi.list(projectId, PAGE_SIZE, assets.length);
      setAssets((prev) => [...prev, ...(more || [])]);
      setHasMore((more || []).length >= PAGE_SIZE);
    } catch (e) {
      console.error("Failed to load more assets:", e);
    } finally {
      setLoadingMore(false);
    }
  };

  useEffect(() => {
    if (!projectId) return;
    setLoading(true);
    setAssets([]);
    if (!isFirstMount.current) {
      setEditingId(null);
      setInitialLine(undefined);
      setInitialCommentId(undefined);
    }
    isFirstMount.current = false;
    load();
  }, [load, projectId]);

  useEffect(() => {
    return globalWS.subscribeGlobal((event) => {
      if (event.type === "change" && event.entity_type === "asset") load();
    });
  }, [load]);

  const triggerLarkAuth = async () => {
    const mcpKey = larkMCP?.name ?? "OKEngine LARK MCP";
    try {
      const { auth_url } = await marketplaceApi.oauthStart(mcpKey, projectId);
      window.open(auth_url, "_blank", "noopener,noreferrer");
    } catch {
      toast.error("OKEngine LARK MCP 未连接", {
        description: "请先在 Marketplace 中安装 OKEngine LARK MCP，然后重新授权。",
      });
    }
  };

  const handleLarkFetch = async () => {
    if (!larkUrl.trim() || fetching) return;
    if (!hasLarkMCP) {
      await triggerLarkAuth();
      return;
    }
    setFetching(true);
    try {
      const res = await assetsApi.larkFetch(larkUrl.trim());
      setFormContent(res.content);
      if (res.title) setFormTitle(res.title);
      toast.success("Fetched Lark doc content");
    } catch (e) {
      const msg = String(e);
      if (msg.includes("authorization") || msg.toLowerCase().includes("unauthori")) {
        await triggerLarkAuth();
      } else {
        toast.error("Failed to fetch Lark doc", { description: msg });
      }
    } finally {
      setFetching(false);
    }
  };

  const handleCreate = async () => {
    if (!formTitle.trim() || saving) return;
    setSaving(true);
    try {
      const asset = await assetsApi.create(projectId, {
        type: formType,
        title: formTitle.trim(),
        content: formContent,
      });
      if (formType === "lark_doc" && larkUrl.trim()) {
        try {
          await assetsApi.setLarkDoc(asset.id, larkUrl.trim());
        } catch (e) {
          toast.error("Asset created but failed to save Lark URL", { description: String(e) });
        }
      }
      setShowForm(false);
      setFormTitle("");
      setFormContent("");
      setLarkUrl("");
      load();
    } catch (e) {
      console.error("Failed to create asset:", e);
      toast.error("Failed to create asset", { description: String(e) });
    } finally {
      setSaving(false);
    }
  };

  const filteredAssets = searchQuery
    ? assets.filter((a) => {
        const q = searchQuery.toLowerCase();
        return (
          a.title.toLowerCase().includes(q) ||
          (TYPE_LABELS[a.type] || a.type).toLowerCase().includes(q) ||
          a.id.toLowerCase().includes(q)
        );
      })
    : assets;

  // Show editor when an asset is selected
  if (editingId) {
    return (
      <AssetEditor
        assetId={editingId}
        onClose={() => { setEditingId(null); setInitialLine(undefined); setInitialCommentId(undefined); }}
        onSaved={load}
        initialMode={initialLine != null || initialCommentId != null ? "review" : undefined}
        initialLine={initialLine}
        initialCommentId={initialCommentId}
      />
    );
  }

  return (
    <div className="flex flex-col h-full">
      {/* Top bar */}
      <div className="flex items-center px-5 h-[46px] border-b border-border/20 shrink-0 gap-4">
        <div className="flex items-center gap-3 min-w-0">
          <span className="text-[13px] text-muted-foreground tabular-nums">▲ {assets.length}</span>

          {showSearch ? (
            <div className="flex items-center gap-1.5 bg-accent/30 rounded-md px-2.5 py-1 border border-border/20">
              <Search className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
              <input
                ref={searchRef}
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") { setShowSearch(false); setSearchQuery(""); }
                }}
                placeholder="Filter assets…"
                className="bg-transparent outline-none text-xs w-40 placeholder:text-muted-foreground/60"
              />
            </div>
          ) : (
            <button
              onClick={() => { setShowSearch(true); setTimeout(() => searchRef.current?.focus(), 50); }}
              className="flex items-center gap-1.5 px-2 py-1 rounded-md text-xs text-muted-foreground hover:text-foreground border border-border/30 hover:border-border/50 transition-colors"
            >
              <Search className="h-3 w-3" />
              <span>Filter</span>
            </button>
          )}
        </div>

        <div className="flex-1" />

        <button
          onClick={() => setShowForm(true)}
          className="flex items-center gap-1 px-2 py-1 rounded-md text-xs text-muted-foreground hover:text-foreground hover:bg-accent/40 border border-border/30 hover:border-border/50 transition-colors"
          title="New asset"
        >
          <Plus className="h-3.5 w-3.5" />
          <span>New</span>
        </button>
      </div>

      {/* New asset form */}
      {showForm && (
        <div className="px-3 py-2 border-b space-y-2">
          <input
            value={formTitle}
            onChange={(e) => setFormTitle(e.target.value)}
            placeholder={formType === "lark_doc" ? "Title (auto-filled from Lark doc)" : "Title"}
            readOnly={formType === "lark_doc"}
            className={cn(
              "w-full text-xs bg-transparent border rounded px-2 py-1 focus:outline-none focus:ring-1 focus:ring-ring",
              formType === "lark_doc" && "opacity-50 cursor-not-allowed",
            )}
            autoFocus={formType !== "lark_doc"}
          />
          <div className="flex gap-2">
            <select
              value={formType}
              onChange={(e) => {
                setFormType(e.target.value as AssetType);
                setFormContent("");
                setLarkUrl("");
              }}
              className="text-xs bg-background border rounded px-2 py-1 focus:outline-none focus:ring-1 focus:ring-ring"
            >
              {ASSET_TYPES.map((t) => (
                <option key={t.value} value={t.value}>{t.label}</option>
              ))}
            </select>
          </div>
          {formType === "lark_doc" && (
            <p className="text-[11px] text-muted-foreground bg-muted/40 border border-border/40 rounded px-2 py-1.5 leading-relaxed">
              Read-only after creation. Content can only be updated by syncing from Lark.
            </p>
          )}

          {formType === "lark_doc" ? (
            <div className="space-y-2">
              <div className="flex gap-1.5">
                <input
                  value={larkUrl}
                  onChange={(e) => setLarkUrl(e.target.value)}
                  placeholder="Lark doc URL (e.g. https://xxx.larksuite.com/wiki/...)"
                  className="flex-1 text-xs bg-transparent border rounded px-2 py-1 focus:outline-none focus:ring-1 focus:ring-ring"
                />
                <button
                  onClick={handleLarkFetch}
                  disabled={!larkUrl.trim() || fetching}
                  className="text-xs px-2 py-1 rounded bg-cyan-600 text-white hover:bg-cyan-500 transition-colors disabled:opacity-50 shrink-0"
                >
                  {fetching ? <Loader2 className="h-3 w-3 animate-spin" /> : "Fetch"}
                </button>
              </div>
              <textarea
                value={formContent}
                readOnly
                placeholder="Content will be fetched automatically…"
                rows={6}
                className="w-full text-xs bg-transparent border rounded px-2 py-1 resize-y min-h-[60px] focus:outline-none opacity-50 cursor-not-allowed"
              />
            </div>
          ) : (
            <textarea
              value={formContent}
              onChange={(e) => setFormContent(e.target.value)}
              placeholder="Content"
              rows={6}
              className="w-full text-xs bg-transparent border rounded px-2 py-1 resize-y min-h-[60px] focus:outline-none focus:ring-1 focus:ring-ring"
            />
          )}

          <div className="flex justify-end gap-1.5">
            <button
              onClick={() => { setShowForm(false); setLarkUrl(""); setFormContent(""); }}
              className="text-xs px-2 py-1 rounded hover:bg-accent transition-colors text-muted-foreground"
            >
              Cancel
            </button>
            <button
              onClick={handleCreate}
              disabled={!formTitle.trim() || saving || (formType === "lark_doc" && !larkUrl.trim())}
              className="text-xs px-2 py-1 rounded bg-primary text-primary-foreground hover:bg-primary/90 transition-colors disabled:opacity-50"
            >
              {saving ? "Saving..." : "Create"}
            </button>
          </div>
        </div>
      )}

      {/* Asset list */}
      <div className="flex-1 overflow-y-auto">
      {loading ? (
        <div className="flex items-center justify-center py-8 text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin mr-2" />
          <span className="text-xs">Loading assets...</span>
        </div>
      ) : filteredAssets.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-12 gap-3 text-muted-foreground">
          <div className="w-16 h-16 rounded-full bg-muted/30 flex items-center justify-center">
            <Plus className="h-7 w-7 text-muted-foreground" />
          </div>
          <div className="text-center">
            <p className="text-sm font-medium text-foreground/80">
              {searchQuery ? "No matching assets" : "No assets yet"}
            </p>
            <p className="text-xs text-muted-foreground mt-1">
              {searchQuery
                ? "Try a different search term"
                : "Click \"New asset\" to create your first asset"}
            </p>
          </div>
        </div>
      ) : (
        <div className="space-y-px">
          {filteredAssets.map((asset) => (
            <div
              key={asset.id}
              className="group/asset flex items-center h-[38px] border-b border-border/10 hover:bg-[#ffffff06] dark:hover:bg-[#ffffff06] transition-colors text-sm select-none cursor-pointer pl-4"
              onClick={() => setEditingId(asset.id)}
            >
              {/* Short ID */}
              <span
                className="shrink-0 text-[11px] text-muted-foreground font-mono tabular-nums hover:text-foreground cursor-copy"
                title={asset.id}
                onClick={(e) => {
                  e.stopPropagation();
                  navigator.clipboard.writeText(asset.id);
                  toast.success("Copied", { duration: 1500 });
                }}
              >
                {shortId(asset.id)}
              </span>

              {/* Icon + Title + Type badge */}
              <div className="flex-1 min-w-0 flex items-center gap-2 pl-2 pr-2">
                {asset.type === "lark_doc"
                  ? <BookOpen className={cn("h-3.5 w-3.5 shrink-0", TYPE_COLORS["lark_doc"])} />
                  : <FileText className={cn("h-3.5 w-3.5 shrink-0", TYPE_COLORS[asset.type] || "text-muted-foreground")} />
                }
                <span className="truncate text-[13px] text-foreground">{asset.title}</span>
                <span className={cn(
                  "shrink-0 text-[10px] font-medium px-1.5 py-0.5 rounded",
                  TYPE_BADGE_COLORS[asset.type] || "bg-muted text-muted-foreground"
                )}>
                  {TYPE_LABELS[asset.type] || asset.type}
                </span>
              </div>

              {/* Right side */}
              <div className="flex items-center shrink-0">
                {/* Hover actions */}
                <div className="hidden group-hover/asset:flex items-center gap-0 mr-1">
                  <button
                    onClick={(e) => {
                      e.stopPropagation();
                      window.dispatchEvent(new CustomEvent("reference-asset", {
                        detail: { id: asset.id, title: asset.title, assetType: asset.type },
                      }));
                    }}
                    className="p-1 rounded hover:bg-accent/60 text-muted-foreground/60 hover:text-foreground transition-colors"
                    title="Reference in chat"
                  >
                    <AtSign className="h-3.5 w-3.5" />
                  </button>
                  <div className="relative">
                    <button
                      onClick={(e) => { e.stopPropagation(); setConfirmDeleteId(asset.id); }}
                      className="p-1 rounded hover:bg-destructive/10 text-muted-foreground/60 hover:text-destructive transition-colors"
                      title="Delete"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </button>
                    {confirmDeleteId === asset.id && (
                      <div data-delete-confirm className="absolute right-0 top-full mt-1 z-50 bg-popover border rounded-md shadow-md p-1.5 flex gap-1 whitespace-nowrap">
                        <button
                          onClick={(e) => { e.stopPropagation(); handleDelete(asset.id); setConfirmDeleteId(null); }}
                          className="px-2 py-0.5 text-xs bg-destructive text-destructive-foreground rounded hover:bg-destructive/90"
                        >
                          Delete
                        </button>
                        <button
                          onClick={(e) => { e.stopPropagation(); setConfirmDeleteId(null); }}
                          className="px-2 py-0.5 text-xs bg-muted text-muted-foreground rounded hover:bg-accent"
                        >
                          Cancel
                        </button>
                      </div>
                    )}
                  </div>
                </div>

                {/* Creator + Date */}
                {asset.created_by && asset.created_by !== "local" && (
                  <UserChip userName={asset.created_by} className="text-blue-400" />
                )}
                <span
                  className="shrink-0 text-[11px] text-muted-foreground pr-2 ml-2 tabular-nums text-right"
                  title={new Date(asset.updated_at).toLocaleString()}
                >
                  {formatRelativeDate(asset.updated_at)}
                </span>
              </div>
            </div>
          ))}
        </div>
      )}
      {hasMore && (
        <div className="flex justify-center py-2 border-t">
          <button
            onClick={loadMore}
            disabled={loadingMore}
            className="px-3 py-1 text-xs text-muted-foreground hover:text-foreground hover:bg-accent rounded transition-colors disabled:opacity-50"
          >
            {loadingMore ? "Loading..." : "Load more"}
          </button>
        </div>
      )}
      </div>
    </div>
  );
}
