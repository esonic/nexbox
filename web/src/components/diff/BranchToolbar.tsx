import { useState, useRef, useCallback, useEffect } from "react";
import { toast } from "sonner";
import {
  GitBranch,
  ArrowDownToLine,
  ArrowUpFromLine,
  ChevronUp,
  ChevronDown,
  Plus,
  Check,
  X,
  Loader2,
  RefreshCw,
  PanelLeftClose,
  PanelLeftOpen,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { gitApi, type GitBranchResponse, type GitScope } from "@/lib/api";

export function BranchToolbar({ scope, onBranchChanged, aheadCount, navLabel, onNavigate, sidebarCollapsed, onToggleSidebar }: {
  scope: GitScope;
  onBranchChanged?: () => void;
  aheadCount?: number;
  navLabel?: string;
  onNavigate?: (direction: "prev" | "next") => void;
  sidebarCollapsed?: boolean;
  onToggleSidebar?: () => void;
}) {
  const [branchData, setBranchData] = useState<GitBranchResponse | null>(null);
  const [showDropdown, setShowDropdown] = useState(false);
  const [creating, setCreating] = useState(false);
  const [newBranchName, setNewBranchName] = useState("");
  const [switching, setSwitching] = useState(false);
  const [pushing, setPushing] = useState(false);
  const [pulling, setPulling] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const branchRequestGeneration = useRef(0);

  const loadBranches = useCallback(async () => {
    const generation = ++branchRequestGeneration.current;
    try {
      const data = await gitApi.branches(scope);
      if (generation === branchRequestGeneration.current) setBranchData(data);
    } catch (e) {
      if (generation !== branchRequestGeneration.current) return;
      console.error("Failed to load branches:", e);
    }
  }, [scope]);

  useEffect(() => { loadBranches(); }, [loadBranches]);

  useEffect(() => {
    const handleClick = (e: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node)) {
        setShowDropdown(false);
        setCreating(false);
        setNewBranchName("");
      }
    };
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, []);

  useEffect(() => {
    if (creating && inputRef.current) inputRef.current.focus();
  }, [creating]);

  const handleCheckout = async (branch: string) => {
    setSwitching(true);
    try {
      await gitApi.checkout(scope, { branch });
      toast.success(`Switched to ${branch}`);
      setShowDropdown(false);
      await loadBranches();
      onBranchChanged?.();
    } catch (e) {
      toast.error("Checkout failed", { description: String(e) });
    } finally {
      setSwitching(false);
    }
  };

  // Create a local branch tracking a remote-tracking branch, instead of relying
  // on checkout's DWIM (which fails when several remotes share the branch name).
  const handleTrackRemote = async (localName: string, remoteBranch: string) => {
    setSwitching(true);
    try {
      await gitApi.checkout(scope, { branch: localName, create: true, start_point: remoteBranch });
      toast.success(`Switched to ${localName}`, { description: `Tracking ${remoteBranch}` });
      setShowDropdown(false);
      await loadBranches();
      onBranchChanged?.();
    } catch (e) {
      toast.error("Checkout failed", { description: String(e) });
    } finally {
      setSwitching(false);
    }
  };

  const handleCreateBranch = async () => {
    const name = newBranchName.trim();
    if (!name) return;
    setSwitching(true);
    try {
      await gitApi.checkout(scope, { branch: name, create: true });
      toast.success(`Created and switched to ${name}`);
      setCreating(false);
      setNewBranchName("");
      setShowDropdown(false);
      await loadBranches();
      onBranchChanged?.();
    } catch (e) {
      toast.error("Create branch failed", { description: String(e) });
    } finally {
      setSwitching(false);
    }
  };

  const handlePush = async () => {
    setPushing(true);
    try {
      await gitApi.push(scope);
      toast.success("Pushed to remote");
      await loadBranches();
      onBranchChanged?.();
    } catch (e) {
      toast.error("Push failed", { description: String(e) });
    } finally {
      setPushing(false);
    }
  };

  const handlePull = async () => {
    setPulling(true);
    try {
      await gitApi.pull(scope);
      toast.success("Pulled from remote");
      await loadBranches();
      onBranchChanged?.();
    } catch (e) {
      toast.error("Pull failed", { description: String(e) });
    } finally {
      setPulling(false);
    }
  };

  const localBranches = branchData?.branches.filter((b) => !b.is_remote) ?? [];
  const remoteBranches = branchData?.branches.filter((b) => b.is_remote) ?? [];

  return (
    <div className="flex items-center gap-1 px-2 py-1.5 border-b text-xs">
      {onToggleSidebar && (
        <button
          onClick={onToggleSidebar}
          className="p-1.5 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
          title={sidebarCollapsed ? "Show changes panel" : "Hide changes panel"}
        >
          {sidebarCollapsed ? <PanelLeftOpen className="h-3.5 w-3.5" /> : <PanelLeftClose className="h-3.5 w-3.5" />}
        </button>
      )}
      <div className="relative" ref={dropdownRef}>
        <button
          onClick={() => setShowDropdown(!showDropdown)}
          className="flex items-center gap-1.5 px-2 py-1 rounded hover:bg-muted transition-colors max-w-[180px]"
        >
          <GitBranch className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span className="truncate font-medium">{branchData?.current || "..."}</span>
          {(aheadCount ?? 0) > 0 && (
            <span className="text-[10px] text-blue-500">{aheadCount}↑</span>
          )}
        </button>

        {showDropdown && (
          <div className="absolute top-full left-0 mt-1 w-64 bg-popover border rounded-md shadow-lg z-50 max-h-80 overflow-y-auto">
            {creating ? (
              <div className="p-2 flex items-center gap-1">
                <input
                  ref={inputRef}
                  value={newBranchName}
                  onChange={(e) => setNewBranchName(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") handleCreateBranch();
                    if (e.key === "Escape") { setCreating(false); setNewBranchName(""); }
                  }}
                  placeholder="new-branch-name"
                  className="flex-1 px-2 py-1 text-xs bg-transparent border rounded focus:outline-none focus:ring-1 focus:ring-primary"
                  disabled={switching}
                />
                <button onClick={handleCreateBranch} disabled={switching || !newBranchName.trim()} className="p-1 hover:bg-muted rounded disabled:opacity-50">
                  <Check className="h-3.5 w-3.5" />
                </button>
                <button onClick={() => { setCreating(false); setNewBranchName(""); }} className="p-1 hover:bg-muted rounded">
                  <X className="h-3.5 w-3.5" />
                </button>
              </div>
            ) : (
              <button onClick={() => setCreating(true)} className="w-full px-3 py-1.5 text-left hover:bg-muted transition-colors flex items-center gap-2 text-muted-foreground border-b">
                <Plus className="h-3 w-3" />
                Create new branch
              </button>
            )}
            {localBranches.length > 0 && (
              <>
                <div className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground bg-muted/30">Local</div>
                {localBranches.map((b) => (
                  <button
                    key={b.name}
                    onClick={() => !b.is_current && handleCheckout(b.name)}
                    disabled={switching || b.is_current}
                    className={cn("w-full px-3 py-1.5 text-left hover:bg-muted transition-colors flex items-center gap-2", b.is_current && "bg-muted/50 font-medium")}
                  >
                    {b.is_current && <Check className="h-3 w-3 text-primary shrink-0" />}
                    <span className={cn("truncate", !b.is_current && "ml-5")}>{b.name}</span>
                    {(b.ahead || 0) > 0 && <span className="text-[10px] text-blue-500 ml-auto shrink-0">{b.ahead}↑</span>}
                    {(b.behind || 0) > 0 && <span className="text-[10px] text-orange-500 shrink-0">{b.behind}↓</span>}
                  </button>
                ))}
              </>
            )}
            {remoteBranches.length > 0 && (
              <>
                <div className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground bg-muted/30">Remote</div>
                {remoteBranches.map((b) => {
                  const localName = b.local_name || b.name;
                  const local = localBranches.find((lb) => lb.name === localName);
                  return (
                    <button
                      key={b.name}
                      onClick={() => {
                        if (!local) handleTrackRemote(localName, b.name);
                        else if (!local.is_current) handleCheckout(localName);
                      }}
                      disabled={switching || local?.is_current}
                      className={cn("w-full px-3 py-1.5 text-left hover:bg-muted transition-colors flex items-center gap-2 ml-5", local?.is_current && "opacity-50")}
                      title={local ? (local.is_current ? `Already on ${localName}` : `Switch to local ${localName}`) : `Create ${localName} tracking ${b.name}`}
                    >
                      <span className="truncate">{b.name}</span>
                      {local && <span className="text-[10px] text-muted-foreground ml-auto shrink-0">local</span>}
                    </button>
                  );
                })}
              </>
            )}
          </div>
        )}
      </div>

      <div className="flex items-center gap-0.5">
        <button onClick={handlePull} disabled={pulling} className="p-1.5 rounded hover:bg-muted transition-colors disabled:opacity-50" title="Pull">
          {pulling ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <ArrowDownToLine className="h-3.5 w-3.5" />}
        </button>
        <button onClick={handlePush} disabled={pushing} className="p-1.5 rounded hover:bg-muted transition-colors disabled:opacity-50" title="Push">
          {pushing ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <ArrowUpFromLine className="h-3.5 w-3.5" />}
        </button>
      </div>

      <div className="ml-auto flex items-center gap-0.5">
        {onNavigate && (
          <>
            {navLabel && <span className="text-[10px] text-muted-foreground tabular-nums mr-0.5">{navLabel}</span>}
            <button onClick={() => onNavigate("prev")}
              className="p-1.5 rounded hover:bg-muted transition-colors text-muted-foreground hover:text-foreground"
              title="Previous">
              <ChevronUp className="h-3.5 w-3.5" />
            </button>
            <button onClick={() => onNavigate("next")}
              className="p-1.5 rounded hover:bg-muted transition-colors text-muted-foreground hover:text-foreground"
              title="Next">
              <ChevronDown className="h-3.5 w-3.5" />
            </button>
          </>
        )}
        <button onClick={() => { loadBranches(); onBranchChanged?.(); }} className="p-1.5 rounded hover:bg-muted transition-colors" title="Refresh">
          <RefreshCw className="h-3.5 w-3.5" />
        </button>
      </div>
    </div>
  );
}
