import { useEffect, useRef, useState } from "react";
import { SquarePen, FolderOpen } from "lucide-react";
import { toast } from "sonner";
import { UNASSIGNED_PROJECT } from "@/lib/constants";
import { useAppStore } from "@/stores/app";

interface NewSessionButtonProps {
  onNewSession: (projectId?: string) => void;
}

/**
 * "New session" quick action. Requires at least one project; picks the target
 * automatically when there's exactly one local project, otherwise opens a
 * project picker dropdown.
 */
export function NewSessionButton({ onNewSession }: NewSessionButtonProps) {
  const projects = useAppStore((s) => s.projects);

  const [showMenu, setShowMenu] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!showMenu) return;
    const handler = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setShowMenu(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [showMenu]);

  const localProjects = projects.filter((p) => p.name !== UNASSIGNED_PROJECT && p.git_repo_path);

  return (
    <div className="px-2 pt-3 pb-2 relative" ref={menuRef}>
      <button
        onClick={() => {
          const realProjects = projects.filter((p) => p.name !== UNASSIGNED_PROJECT);
          if (realProjects.length === 0) {
            toast.info("Create a project first");
            return;
          }
          if (localProjects.length === 1) {
            onNewSession(localProjects[0].id);
          } else {
            setShowMenu((v) => !v);
          }
        }}
        className="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-foreground/80 hover:text-foreground hover:bg-accent/50 transition-colors"
      >
        <SquarePen className="h-4 w-4 shrink-0 text-muted-foreground" />
        <span>New session</span>
      </button>

      {/* Project picker dropdown */}
      {showMenu && (
        <div className="absolute left-2 right-2 top-full z-50 mt-0.5 bg-popover border border-border rounded-lg shadow-lg overflow-hidden">
          <div className="px-3 py-2 text-xs font-medium text-muted-foreground border-b border-border bg-muted/30">
            Select Project
          </div>
          {localProjects.map((p) => (
            <button
              key={p.id}
              onClick={() => { setShowMenu(false); onNewSession(p.id); }}
              className="w-full flex items-center gap-2 px-3 py-2 text-xs hover:bg-accent transition-colors"
            >
              <FolderOpen className="h-3.5 w-3.5 text-primary/60 shrink-0" />
              <span className="truncate">{p.name}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
