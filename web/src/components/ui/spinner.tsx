import { cn } from "@/lib/utils";

/**
 * Mood-light spinner: a fading comet ring that rotates while its hue drifts
 * smoothly through soft pastel tones (defined by `.mood-spinner` in index.css).
 *
 * Pure CSS — no dependencies. Override size via className,
 * e.g. <Spinner className="size-3" />.
 */
export function Spinner({ className }: { className?: string }) {
  return (
    <span
      role="status"
      aria-label="Loading"
      className={cn("mood-spinner inline-block size-4 shrink-0 rounded-full", className)}
    />
  );
}
