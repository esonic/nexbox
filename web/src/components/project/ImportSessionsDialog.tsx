import { useCallback, useEffect, useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";
import { cn, formatRelativeDate } from "@/lib/utils";
import { agentLabel } from "@/lib/agents";
import { sessionsApi } from "@/lib/api";
import type { ImportableSession, Project, Session } from "@/types";

// Agents whose own CLI session store NexBox can read.
const IMPORT_AGENTS = ["claude", "codex"] as const;

interface ImportSessionsDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  project: Project | null;
  onImported: (sessions: Session[]) => void;
}

export function ImportSessionsDialog({ open, onOpenChange, project, onImported }: ImportSessionsDialogProps) {
  const [agent, setAgent] = useState<string>(IMPORT_AGENTS[0]);
  const [items, setItems] = useState<ImportableSession[]>([]);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [hasMore, setHasMore] = useState(false);
  const [importing, setImporting] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async (projectId: string, ag: string) => {
    setLoading(true);
    setError("");
    setSelected(new Set());
    setHasMore(false);
    try {
      const res = await sessionsApi.listImportable(projectId, ag);
      setItems(res.sessions);
      setHasMore(res.has_more);
    } catch (e) {
      setItems([]);
      setError(String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (open && project) void load(project.id, agent);
  }, [open, project, agent, load]);

  const loadMore = async () => {
    if (!project || items.length === 0) return;
    setLoadingMore(true);
    try {
      const res = await sessionsApi.listImportable(project.id, agent, items[items.length - 1].updated_at);
      setItems((prev) => [...prev, ...res.sessions]);
      setHasMore(res.has_more);
    } catch (e) {
      toast.error("Failed to load more sessions", { description: String(e) });
    } finally {
      setLoadingMore(false);
    }
  };

  const toggle = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const allSelected = items.length > 0 && items.every((i) => selected.has(i.agent_session_id));
  const toggleAll = () => {
    setSelected(allSelected ? new Set() : new Set(items.map((i) => i.agent_session_id)));
  };

  const handleImport = async () => {
    if (!project || selected.size === 0) return;
    setImporting(true);
    const imported: Session[] = [];
    const failed: string[] = [];
    // Oldest first so the newest ends up on top of the sidebar.
    const ordered = items.filter((i) => selected.has(i.agent_session_id)).reverse();
    for (const item of ordered) {
      try {
        imported.push(await sessionsApi.import({
          project_id: project.id,
          agent,
          agent_session_id: item.agent_session_id,
        }));
      } catch (e) {
        failed.push(`${item.title || item.agent_session_id}: ${String(e)}`);
      }
    }
    setImporting(false);
    if (imported.length > 0) {
      onImported(imported);
      toast.success(`Imported ${imported.length} session${imported.length > 1 ? "s" : ""}`);
    }
    if (failed.length > 0) {
      toast.error(`Failed to import ${failed.length} session${failed.length > 1 ? "s" : ""}`, {
        description: failed[0],
      });
      void load(project.id, agent);
    } else {
      onOpenChange(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex flex-col w-[95vw] max-w-5xl h-[70vh] max-h-[70vh]">
        <DialogHeader>
          <DialogTitle>Import CLI sessions</DialogTitle>
          <DialogDescription>
            Sessions you ran directly in the agent CLI in {project?.git_repo_path || "this project"}.
            Imported sessions continue where the CLI left off.
          </DialogDescription>
        </DialogHeader>

        <div className="flex gap-1">
          {IMPORT_AGENTS.map((ag) => (
            <button
              key={ag}
              onClick={() => setAgent(ag)}
              className={cn(
                "px-3 py-1 rounded-md text-xs font-medium transition-colors",
                agent === ag ? "bg-accent text-foreground" : "text-muted-foreground hover:bg-accent/50",
              )}
            >
              {agentLabel(ag)}
            </button>
          ))}
        </div>

        <div className="flex-1 min-h-0 overflow-y-auto rounded-md border">
          {loading ? (
            <div className="flex items-center justify-center gap-2 py-10 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" /> Scanning…
            </div>
          ) : error ? (
            <div className="px-3 py-6 text-sm text-destructive">{error}</div>
          ) : items.length === 0 ? (
            <div className="px-3 py-10 text-center text-sm text-muted-foreground">
              No {agentLabel(agent)} sessions to import.
            </div>
          ) : (
            items.map((item) => (
              <label
                key={item.agent_session_id}
                className="flex items-center gap-3 px-3 py-1.5 text-sm hover:bg-accent/40 cursor-pointer border-b last:border-b-0"
              >
                <input
                  type="checkbox"
                  checked={selected.has(item.agent_session_id)}
                  onChange={() => toggle(item.agent_session_id)}
                />
                <span className="flex-1 min-w-0">
                  <span className="block truncate">{item.title || item.agent_session_id}</span>
                  <span className="block truncate font-mono text-[10px] text-muted-foreground/60">
                    {item.agent_session_id}
                  </span>
                </span>
                <span className="text-xs text-muted-foreground tabular-nums shrink-0">
                  {formatRelativeDate(item.updated_at)}
                </span>
              </label>
            ))
          )}
          {!loading && hasMore && (
            <button
              onClick={loadMore}
              disabled={loadingMore}
              className="w-full flex items-center justify-center gap-2 py-2 text-xs text-muted-foreground hover:bg-accent/40 disabled:opacity-60"
            >
              {loadingMore && <Loader2 className="h-3 w-3 animate-spin" />}
              Load more
            </button>
          )}
        </div>

        <DialogFooter className="sm:justify-between sm:space-x-0 gap-2">
          <Button
            variant="outline"
            onClick={toggleAll}
            disabled={importing || loading || items.length === 0}
          >
            {allSelected ? "Deselect all" : "Select all"}
          </Button>
          <div className="flex flex-col-reverse sm:flex-row gap-2">
            <Button variant="outline" onClick={() => onOpenChange(false)} disabled={importing}>
              Cancel
            </Button>
            <Button onClick={handleImport} disabled={importing || selected.size === 0}>
              {importing && <Loader2 className="h-4 w-4 mr-1.5 animate-spin" />}
              Import{selected.size > 0 ? ` (${selected.size})` : ""}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
