import { useNavigate, useLocation } from "react-router-dom";
import { LayoutGrid, Store, Settings } from "lucide-react";
import { cn } from "@/lib/utils";
import { useAppStore } from "@/stores/app";

/**
 * Sidebar footer bottom nav: Projects / Customize / Settings.
 */
export function SidebarFooterNav() {
  const navigate = useNavigate();
  const location = useLocation();
  const marketplaceUpdateCount = useAppStore((s) => s.marketplaceUpdateCount);

  return (
    <div className="px-2 pb-3 pt-1 shrink-0 space-y-0.5 border-t border-border/40 mt-1">
      <button
        onClick={() => navigate("/projects")}
        className={cn(
          "w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm transition-colors mt-1",
          location.pathname === "/projects"
            ? "bg-accent text-foreground font-medium"
            : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
        )}
      >
        <LayoutGrid className="h-4 w-4 shrink-0" />
        <span>Projects</span>
      </button>
      <button
        onClick={() => navigate("/marketplace")}
        className={cn(
          "w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm transition-colors",
          location.pathname.startsWith("/marketplace")
            ? "bg-accent text-foreground font-medium"
            : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
        )}
      >
        <div className="relative shrink-0">
          <Store className="h-4 w-4" />
          {marketplaceUpdateCount > 0 && (
            <span className="absolute -top-0.5 -right-0.5 h-2 w-2 rounded-full bg-red-500" />
          )}
        </div>
        <span>Customize</span>
        {marketplaceUpdateCount > 0 && (
          <span className="ml-auto text-[10px] font-medium px-1.5 py-0.5 rounded-full bg-red-500/10 text-red-500">
            {marketplaceUpdateCount}
          </span>
        )}
      </button>
      <button
        onClick={() => navigate("/settings")}
        className={cn(
          "w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm transition-colors",
          location.pathname === "/settings"
            ? "bg-accent text-foreground font-medium"
            : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
        )}
      >
        <Settings className="h-4 w-4 shrink-0" />
        <span>Settings</span>
      </button>
    </div>
  );
}
