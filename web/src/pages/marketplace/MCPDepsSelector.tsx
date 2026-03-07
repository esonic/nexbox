import { useState, useEffect, useRef } from "react";
import { ChevronDown, Loader2, X } from "lucide-react";
import { marketplaceApi, type MarketplacePackage } from "@/lib/api";

interface Props {
  value: string[];
  onChange: (deps: string[]) => void;
}

export function MCPDepsSelector({ value, onChange }: Props) {
  const [mcps, setMcps] = useState<MarketplacePackage[]>([]);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setLoading(true);
    marketplaceApi
      .listPackages({ type: "mcp", limit: 200 })
      .then((pkgs) => setMcps(pkgs ?? []))
      .catch(() => {})
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false);
        setSearch("");
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, []);

  const filtered = mcps.filter(
    (m) =>
      !search ||
      m.name.toLowerCase().includes(search.toLowerCase()) ||
      m.title.toLowerCase().includes(search.toLowerCase())
  );

  const toggle = (name: string) => {
    if (value.includes(name)) {
      onChange(value.filter((d) => d !== name));
    } else {
      onChange([...value, name]);
    }
  };

  return (
    <div className="space-y-2">
      {value.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {value.map((dep) => (
            <span
              key={dep}
              className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-mono bg-blue-500/10 text-blue-600 border border-blue-500/20"
            >
              {dep}
              <button
                type="button"
                onClick={() => toggle(dep)}
                className="hover:text-destructive transition-colors"
              >
                <X className="h-3 w-3" />
              </button>
            </span>
          ))}
        </div>
      )}

      <div ref={ref} className="relative">
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          className="w-full flex items-center justify-between px-3 py-2 text-sm bg-background border rounded-lg hover:bg-accent/30 transition-colors text-left"
        >
          <span className="text-muted-foreground text-sm">
            {value.length === 0 ? "Select MCP packages…" : `${value.length} selected`}
          </span>
          {loading ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />
          ) : (
            <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
          )}
        </button>

        {open && (
          <div className="absolute z-50 top-full mt-1 w-full bg-card border rounded-lg shadow-lg overflow-hidden">
            <div className="p-2 border-b">
              <input
                autoFocus
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search MCPs…"
                className="w-full px-2 py-1.5 text-xs bg-background border rounded outline-none focus:ring-1 focus:ring-primary"
              />
            </div>
            <div className="max-h-48 overflow-y-auto">
              {filtered.length === 0 ? (
                <div className="text-xs text-muted-foreground text-center py-3">
                  {loading ? "Loading…" : "No MCPs found"}
                </div>
              ) : (
                filtered.map((mcp) => (
                  <label
                    key={mcp.name}
                    className="flex items-center gap-2.5 px-3 py-2 hover:bg-accent/50 cursor-pointer transition-colors"
                  >
                    <input
                      type="checkbox"
                      checked={value.includes(mcp.name)}
                      onChange={() => toggle(mcp.name)}
                      className="shrink-0"
                    />
                    <div className="min-w-0">
                      <div className="text-xs font-medium truncate">{mcp.title}</div>
                      <div className="text-[10px] text-muted-foreground font-mono truncate">{mcp.name}</div>
                    </div>
                  </label>
                ))
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
