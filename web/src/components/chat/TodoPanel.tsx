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
  completed: { icon: CheckCircle2, color: "text-green-500" },
  TODO_STATUS_IN_PROGRESS: { icon: CircleDot, color: "text-blue-500" },
  in_progress: { icon: CircleDot, color: "text-blue-500" },
  TODO_STATUS_PENDING: { icon: Circle, color: "text-muted-foreground/50" },
  pending: { icon: Circle, color: "text-muted-foreground/50" },
  TODO_STATUS_CANCELLED: { icon: Ban, color: "text-muted-foreground/30" },
  cancelled: { icon: Ban, color: "text-muted-foreground/30" },
};

export function TodoPanel({ todos, isPending }: TodoPanelProps) {
  if (todos.length === 0) return null;

  const completedCount = todos.filter(
    (t) => t.status === "TODO_STATUS_COMPLETED" || t.status === "completed"
  ).length;
  const totalCount = todos.length;
  const progressPercent = totalCount > 0 ? Math.round((completedCount / totalCount) * 100) : 0;

  return (
    <div className="bg-gradient-to-r from-blue-500/10 to-cyan-500/10 border border-blue-500/30 rounded-lg p-4 mb-3 space-y-3">
      {/* Header with progress */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2.5">
          <div className="flex items-center justify-center w-8 h-8 rounded-lg bg-blue-500/20">
            <ListChecks className="h-4 w-4 text-blue-600 dark:text-blue-400" />
          </div>
          <div>
            <div className="text-sm font-semibold text-foreground">跟踪任务进度</div>
            <div className="text-xs text-muted-foreground">{completedCount} / {totalCount} 完成</div>
          </div>
        </div>
        <div className="text-right">
          <div className="text-lg font-bold text-blue-600 dark:text-blue-400">{progressPercent}%</div>
        </div>
      </div>

      {/* Progress bar */}
      <div className="w-full bg-muted/40 rounded-full h-2 overflow-hidden">
        <div
          className="bg-gradient-to-r from-blue-500 to-cyan-500 h-2 rounded-full transition-all duration-300"
          style={{ width: `${progressPercent}%` }}
        />
      </div>

      {/* Task list */}
      <div className="space-y-1.5 max-h-[300px] overflow-y-auto">
        {todos.map((t) => {
          const { icon: S, color } = TODO_ICONS[t.status] ?? { icon: CircleDashed, color: "text-muted-foreground/40" };
          const done = t.status === "TODO_STATUS_COMPLETED" || t.status === "completed";
          const inProgress = t.status === "TODO_STATUS_IN_PROGRESS" || t.status === "in_progress";

          return (
            <div
              key={t.id}
              className={cn(
                "flex items-start gap-2.5 px-2.5 py-1.5 rounded-md text-sm transition-colors",
                inProgress && "bg-blue-500/10 border border-blue-500/20"
              )}
            >
              <S className={cn("h-4 w-4 shrink-0 mt-0.5", color)} />
              <span className={cn("min-w-0 break-words flex-1", done && "line-through text-muted-foreground/50")}>
                {t.content}
              </span>
            </div>
          );
        })}
      </div>

      {/* Loading indicator */}
      {isPending && (
        <div className="flex items-center gap-2 text-xs text-blue-600 dark:text-blue-400 pt-1 border-t border-blue-500/20">
          <Loader2 className="h-3 w-3 animate-spin" />
          <span>正在执行任务中...</span>
        </div>
      )}
    </div>
  );
}
