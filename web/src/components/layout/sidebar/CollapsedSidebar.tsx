import { useNavigate, useLocation } from "react-router-dom";
import { Menu, PanelLeftOpen, Store, Settings } from "lucide-react";
import { cn } from "@/lib/utils";
import { useAppStore } from "@/stores/app";
import { ThemeToggle } from "@/components/ui/theme-toggle";

/**
 * Mobile, drawer closed: a floating hamburger pinned to the viewport so the
 * drawer can slide in on demand.
 */
export function MobileMenuButton({ onOpen }: { onOpen: () => void }) {
  return (
    <button
      onClick={onOpen}
      className="fixed left-2 z-40 p-1.5 rounded-md bg-card/90 backdrop-blur border border-border/60 shadow-sm hover:bg-accent transition-colors"
      style={{ top: "calc(env(safe-area-inset-top) + 0.25rem)" }}
      title="Open menu"
      aria-label="Open menu"
    >
      <Menu className="h-4 w-4" />
    </button>
  );
}

/**
 * Desktop, sidebar collapsed: a narrow rail with expand + quick nav icons.
 */
export function CollapsedSidebarRail({ onExpand }: { onExpand: () => void }) {
  const navigate = useNavigate();
  const location = useLocation();
  const marketplaceUpdateCount = useAppStore((s) => s.marketplaceUpdateCount);

  return (
    <div className="w-10 border-r border-border/50 bg-card flex flex-col h-full items-center shrink-0">
      <div className="py-3 w-full flex flex-col items-center gap-1">
        <button
          onClick={onExpand}
          className="p-1.5 rounded hover:bg-accent transition-colors"
          title="Expand sidebar"
        >
          <PanelLeftOpen className="h-4 w-4" />
        </button>
        <ThemeToggle />
      </div>
      <div className="mt-auto pb-3 w-full flex flex-col items-center gap-1">
        <button
          onClick={() => navigate("/marketplace")}
          className={cn(
            "relative p-1.5 rounded transition-colors",
            location.pathname.startsWith("/marketplace")
              ? "bg-accent text-foreground"
              : "text-muted-foreground hover:text-foreground hover:bg-accent"
          )}
          title={marketplaceUpdateCount > 0 ? `Customize (${marketplaceUpdateCount} update${marketplaceUpdateCount > 1 ? "s" : ""} available)` : "Customize"}
        >
          <Store className="h-4 w-4" />
          {marketplaceUpdateCount > 0 && (
            <span className="absolute top-0 right-0 h-2 w-2 rounded-full bg-red-500" />
          )}
        </button>
        <button
          onClick={() => navigate("/settings")}
          className={cn(
            "p-1.5 rounded transition-colors",
            location.pathname === "/settings"
              ? "bg-accent text-foreground"
              : "text-muted-foreground hover:text-foreground hover:bg-accent"
          )}
          title="Settings"
        >
          <Settings className="h-4 w-4" />
        </button>
      </div>
    </div>
  );
}
