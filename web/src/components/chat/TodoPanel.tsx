import { ListChecks, CheckCircle2, CircleDot, Circle, CircleDashed, Ban } from "lucide-react";
import { BorderBeam } from "@/components/ui/border-beam";
import { cn } from "@/lib/utils";
import { useThemeMode } from "@/hooks/useThemeMode";

interface TodoItem {
  id: string;
  content: string;
  status: string;
  blockedBy?: string[];
}

interface TodoPanelProps {
  todos: TodoItem[];
  /** Header label — the in-progress task's activeForm; falls back to "Tasks". */
  title?: string;
}

const TODO_ICONS: Record<string, { icon: React.ComponentType<{ className?: string }>; color: string }> = {
  TODO_STATUS_COMPLETED: { icon: CheckCircle2, color: "text-green-500" },
  completed:             { icon: CheckCircle2, color: "text-green-500" },
  TODO_STATUS_IN_PROGRESS: { icon: CircleDot, color: "text-blue-400" },
  in_progress:             { icon: CircleDot, color: "text-blue-400" },
  TODO_STATUS_PENDING: { icon: Circle, color: "text-muted-foreground/40" },
  pending:             { icon: Circle, color: "text-muted-foreground/40" },
  TODO_STATUS_CANCELLED: { icon: Ban, color: "text-muted-foreground/30" },
  cancelled:             { icon: Ban, color: "text-muted-foreground/30" },
};

const isDone = (status: string) => status === "TODO_STATUS_COMPLETED" || status === "completed";
const isActive = (status: string) => status === "TODO_STATUS_IN_PROGRESS" || status === "in_progress";

export function TodoPanel({ todos, title }: TodoPanelProps) {
  const themeMode = useThemeMode();
  if (todos.length === 0) return null;

  const completedCount = todos.filter((t) => isDone(t.status)).length;
  const totalCount = todos.length;

  return (
    <BorderBeam size="pulse-outside" theme={themeMode} className="block mb-3">
      <div className="border border-border rounded-lg overflow-hidden bg-background">
      <div className="flex items-center gap-2 px-4 py-2 border-b bg-muted/30">
        <ListChecks className="h-4 w-4 text-muted-foreground shrink-0" />
        <span className="text-sm font-medium truncate">{title || "Tasks"}</span>
        <span className="ml-auto text-xs text-muted-foreground tabular-nums shrink-0">
          {completedCount} / {totalCount}
        </span>
      </div>

      <div className="px-3 py-2 space-y-0.5 max-h-[240px] overflow-y-auto">
        {todos.map((t) => {
          const { icon: S, color } = TODO_ICONS[t.status] ?? { icon: CircleDashed, color: "text-muted-foreground/40" };
          const done = isDone(t.status);
          const active = isActive(t.status);
          const deps = t.blockedBy ?? [];
          const blocked = deps.length > 0 && !done;
          return (
            <div key={t.id} className="flex items-center gap-2.5 py-1 text-sm">
              <S className={cn("h-3.5 w-3.5 shrink-0", color)} />
              <span className="shrink-0 text-xs tabular-nums text-muted-foreground/40">#{t.id}</span>
              <span className={cn(
                "min-w-0 truncate",
                done && "line-through text-muted-foreground/50",
                active && "font-medium",
                blocked && "text-muted-foreground/50",
              )}>
                {t.content}
              </span>
              {blocked && (
                <span className="shrink-0 text-xs text-muted-foreground/40">
                  · blocked by {deps.map((d) => `#${d}`).join(", ")}
                </span>
              )}
            </div>
          );
        })}
      </div>
      </div>
    </BorderBeam>
  );
}
