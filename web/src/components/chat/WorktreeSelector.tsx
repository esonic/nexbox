import { useEffect, useRef, useState } from "react";
import { Check, ChevronDown, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";

export interface WorktreeSelectorProps {
  mode: "local" | "worktree";
  locked: boolean;
  worktreePending: boolean;
  hasGitRepo: boolean;
  onSetMode: (mode: "local" | "worktree") => void;
}

export function WorktreeSelector({
  mode,
  locked,
  worktreePending,
  hasGitRepo,
  onSetMode,
}: WorktreeSelectorProps) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [open]);

  const label = mode === "worktree" ? "Worktree" : "Local";

  if (locked) {
    return (
      <span className="text-[10px] font-medium px-1.5 py-0.5 rounded-sm bg-muted text-muted-foreground shrink-0 flex items-center gap-1 whitespace-nowrap">
        {worktreePending && <Loader2 className="h-2.5 w-2.5 animate-spin" />}
        {label}
      </span>
    );
  }

  return (
    <div className="relative shrink-0" ref={ref}>
      <button
        onClick={() => hasGitRepo && setOpen((o) => !o)}
        disabled={!hasGitRepo}
        className={cn(
          "text-[10px] font-medium px-1.5 py-0.5 rounded-sm flex items-center gap-1 whitespace-nowrap transition-colors",
          hasGitRepo
            ? "bg-muted text-muted-foreground hover:bg-accent hover:text-accent-foreground cursor-pointer"
            : "bg-muted text-muted-foreground opacity-40 cursor-not-allowed",
        )}
        title="Click to change mode"
      >
        {label}
        <ChevronDown className="h-2.5 w-2.5" />
      </button>
      {open && (
        <div className="absolute left-0 bottom-full z-50 mb-1 w-44 bg-popover border border-border rounded-lg shadow-lg overflow-hidden">
          {(["local", "worktree"] as const).map((m) => (
            <button
              key={m}
              onClick={() => { onSetMode(m); setOpen(false); }}
              className={cn(
                "w-full flex items-center justify-between px-3 py-1.5 text-xs hover:bg-accent transition-colors whitespace-nowrap",
                mode === m && "bg-accent/50 font-medium",
              )}
            >
              <span>{m === "local" ? "Local" : "New worktree"}</span>
              {mode === m && <Check className="h-3 w-3 text-foreground/50 shrink-0 ml-2" />}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
