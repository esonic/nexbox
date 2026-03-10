import { ListChecks, Loader2, CheckCircle2, CircleDot, Circle, CircleDashed, Ban } from "lucide-react";
import { cn } from "@/lib/utils";

interface TodoItem {
  id: string;
  content: string;
  status: string;
}

interface TodoPanelProps {
  todos: TodoItem[];
  isPending: boolean;
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

export function TodoPanel({ todos, isPending }: TodoPanelProps) {
  if (todos.length === 0) return null;

  const completedCount = todos.filter(
    (t) => t.status === "TODO_STATUS_COMPLETED" || t.status === "completed"
  ).length;
  const totalCount = todos.length;

  return (
    <div className="border border-border rounded-lg overflow-hidden bg-background mb-3">
      {/* Header */}
      <div className="flex items-center gap-2 px-4 py-2 border-b bg-muted/30">
        <ListChecks className="h-4 w-4 text-muted-foreground" />
        <span className="text-sm font-medium">Tasks</span>
        <span className="ml-auto text-xs text-muted-foreground tabular-nums">
          {completedCount} / {totalCount}
        </span>
      </div>

      {/* Task list */}
      <div className="px-3 py-2 space-y-0.5 max-h-[240px] overflow-y-auto">
        {todos.map((t) => {
          const { icon: S, color } = TODO_ICONS[t.status] ?? { icon: CircleDashed, color: "text-muted-foreground/40" };
          const done = t.status === "TODO_STATUS_COMPLETED" || t.status === "completed";
          return (
            <div key={t.id} className="flex items-center gap-2.5 py-1 text-sm">
              <S className={cn("h-3.5 w-3.5 shrink-0", color)} />
              <span className={cn(done && "line-through text-muted-foreground/50")}>{t.content}</span>
            </div>
          );
        })}
      </div>

      {/* Footer */}
      {isPending && (
        <div className="flex items-center gap-2 px-4 py-2 border-t text-xs text-muted-foreground">
          <Loader2 className="h-3 w-3 animate-spin" />
          <span>Running…</span>
        </div>
      )}
    </div>
  );
}
