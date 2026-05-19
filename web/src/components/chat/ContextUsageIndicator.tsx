import { useState } from "react";
import { cn } from "@/lib/utils";
import { useAppStore } from "@/stores/app";

interface ContextUsageIndicatorProps {
  sessionId: string;
  fallbackContextWindow: number;
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + "M";
  if (n >= 1_000) return (n / 1_000).toFixed(1) + "k";
  return String(n);
}

/**
 * ContextUsageIndicator renders a small circular progress donut next to the
 * model picker showing how much of the context window the current session
 * has consumed. Hovering reveals the precise numbers.
 *
 * Data source: the `context_usage` WebSocket event emitted after each turn
 * by the backend. When no usage has been reported yet (fresh session), the
 * indicator stays hidden so it doesn't take up space.
 */
export function ContextUsageIndicator({ sessionId, fallbackContextWindow }: ContextUsageIndicatorProps) {
  const usage = useAppStore((s) => s.sessionContextUsage[sessionId]);
  const [hover, setHover] = useState(false);

  if (!usage || !usage.used_tokens) return null;

  const total = usage.context_window || fallbackContextWindow || 0;
  const used = usage.used_tokens;
  const pct = total > 0 ? Math.min(100, Math.max(0, (used / total) * 100)) : 0;

  // Donut geometry — r controls visual size; stroke controls ring thickness.
  const r = 6;
  const stroke = 2;
  const c = 2 * Math.PI * r;
  const offset = c * (1 - pct / 100);

  // Color ramp: teal under 60%, amber 60-85%, red over 85%.
  const color = pct > 85
    ? "text-red-500"
    : pct > 60
    ? "text-amber-400"
    : "text-teal-400";

  return (
    <div
      className="relative inline-flex items-center"
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
    >
      <svg width={16} height={16} viewBox="0 0 16 16" className={cn("ml-1", color)}>
        <circle
          cx={8}
          cy={8}
          r={r}
          fill="none"
          strokeWidth={stroke}
          className="stroke-muted-foreground/20"
        />
        <circle
          cx={8}
          cy={8}
          r={r}
          fill="none"
          strokeWidth={stroke}
          strokeDasharray={c}
          strokeDashoffset={offset}
          strokeLinecap="round"
          transform="rotate(-90 8 8)"
          className="stroke-current transition-[stroke-dashoffset] duration-500"
        />
      </svg>
      {hover && (
        <div className="absolute left-1/2 -translate-x-1/2 bottom-full mb-1 z-50 whitespace-nowrap rounded-md bg-popover border border-border px-2 py-1 text-[11px] text-foreground shadow-md">
          Context {formatTokens(used)}
          {total > 0 && <> / {formatTokens(total)}</>}
          {total > 0 && <> ({pct.toFixed(0)}%)</>}
        </div>
      )}
    </div>
  );
}
