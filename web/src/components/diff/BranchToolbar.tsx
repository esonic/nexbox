import { useState, useRef, useEffect, type ReactNode } from "react";
import { createPortal } from "react-dom";
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
} from "lucide-react";
import { cn } from "@/lib/utils";
import { gitApi, type GitBranchResponse, type GitScope } from "@/lib/api";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

export function BranchToolbar({ scope, onBranchChanged, aheadCount, navLabel, onNavigate, branchData, trailingActions, showRefresh = true, controlsTarget, actionsTarget }: {
  scope: GitScope;
  branchData?: GitBranchResponse;
  onBranchChanged?: () => void;
  aheadCount?: number;
  navLabel?: string;
  onNavigate?: (direction: "prev" | "next") => void;
  trailingActions?: ReactNode;
  showRefresh?: boolean;
  controlsTarget?: HTMLDivElement | null;
  actionsTarget?: HTMLDivElement | null;
}) {
  const [showDropdown, setShowDropdown] = useState(false);
  const [creating, setCreating] = useState(false);
  const [newBranchName, setNewBranchName] = useState("");
  const [switching, setSwitching] = useState(false);
  const [pushing, setPushing] = useState(false);
  const [pulling, setPulling] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (creating && inputRef.current) inputRef.current.focus();
  }, [creating]);

  const handleCheckout = async (branch: string) => {
    setSwitching(true);
    try {
      await gitApi.checkout(scope, { branch });
      toast.success(`Switched to ${branch}`);
      setShowDropdown(false);
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
      onBranchChanged?.();
    } catch (e) {
      toast.error("Pull failed", { description: String(e) });
    } finally {
      setPulling(false);
    }
  };

  const localBranches = branchData?.branches.filter((b) => !b.is_remote) ?? [];
  const remoteBranches = branchData?.branches.filter((b) => b.is_remote) ?? [];

  // Undefined preserves standalone use; null waits for the workspace header.
  const embedded = controlsTarget !== undefined;
  const controls = (
    <>
      <div className="relative min-w-0">
        <Popover open={showDropdown} onOpenChange={(open) => {
          setShowDropdown(open);
          if (!open) { setCreating(false); setNewBranchName(""); }
        }}>
        <PopoverTrigger asChild>
        <button
          className={cn("flex items-center gap-1.5 py-1 rounded hover:bg-muted transition-colors", embedded ? "px-1 max-w-[72px] md:max-w-[80px] [@container(min-width:640px)]:max-w-[180px]" : "px-2 max-w-[180px]")}
          title={branchData?.current ? `Switch branch (${branchData.current})` : "Switch branch"}
          aria-label="Switch branch"
        >
          <GitBranch className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span className="truncate font-medium">{branchData?.current || "..."}</span>
          {(aheadCount ?? 0) > 0 && (
            <span className="text-[10px] text-blue-500">{aheadCount}↑</span>
          )}
        </button>
        </PopoverTrigger>
          <PopoverContent align="start" sideOffset={4} className="w-[22rem] max-w-[calc(100vw-2rem)] p-0 max-h-80 overflow-y-auto overflow-x-hidden text-xs">
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
                    title={b.name}
                  >
                    {b.is_current && <Check className="h-3 w-3 text-primary shrink-0" />}
                    <span className={cn("truncate min-w-0", !b.is_current && "ml-5")}>{b.name}</span>
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
                      className={cn("w-full pl-8 pr-3 py-1.5 text-left hover:bg-muted transition-colors flex items-center gap-2", local?.is_current && "opacity-50")}
                      title={local ? (local.is_current ? `Already on ${localName}` : `Switch to local ${localName}`) : `Create ${localName} tracking ${b.name}`}
                    >
                      <span className="truncate min-w-0">{b.name}</span>
                      {local && <span className="text-[10px] text-muted-foreground ml-auto shrink-0">local</span>}
                    </button>
                  );
                })}
              </>
            )}
          </PopoverContent>
        </Popover>
      </div>

      <div className={cn("flex items-center shrink-0", embedded ? "gap-1" : "gap-0.5")}>
        <button onClick={handlePull} disabled={pulling} className={cn("rounded hover:bg-muted transition-colors disabled:opacity-50", embedded ? "p-1" : "p-1.5")} title="Pull">
          {pulling ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <ArrowDownToLine className="h-3.5 w-3.5" />}
        </button>
        <button onClick={handlePush} disabled={pushing} className={cn("rounded hover:bg-muted transition-colors disabled:opacity-50", embedded ? "p-1" : "p-1.5")} title="Push">
          {pushing ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <ArrowUpFromLine className="h-3.5 w-3.5" />}
        </button>
      </div>
    </>
  );
  const actions = (
    <>
        {onNavigate && (
          <>
            {navLabel && <span className={cn("text-[10px] text-muted-foreground tabular-nums mr-0.5", embedded && "hidden [@container(min-width:640px)]:inline")}>{navLabel}</span>}
            <button onClick={() => onNavigate("prev")}
              className={cn("rounded hover:bg-muted transition-colors text-muted-foreground hover:text-foreground", embedded ? "p-0.5" : "p-1.5")}
              title="Previous">
              <ChevronUp className="h-3.5 w-3.5" />
            </button>
            <button onClick={() => onNavigate("next")}
              className={cn("rounded hover:bg-muted transition-colors text-muted-foreground hover:text-foreground", embedded ? "p-0.5" : "p-1.5")}
              title="Next">
              <ChevronDown className="h-3.5 w-3.5" />
            </button>
          </>
        )}
        {showRefresh && (
          <button onClick={onBranchChanged} className="p-1.5 rounded hover:bg-muted transition-colors" title="Refresh">
            <RefreshCw className="h-3.5 w-3.5" />
          </button>
        )}
        {trailingActions}
    </>
  );

  if (embedded) {
    return <>
      {controlsTarget && createPortal(controls, controlsTarget)}
      {actionsTarget && createPortal(actions, actionsTarget)}
    </>;
  }
  return (
    <div className="flex items-center gap-1 px-2 py-1.5 border-b text-xs">
      {controls}
      <div className="ml-auto flex items-center gap-0.5">
        {actions}
      </div>
    </div>
  );
}
