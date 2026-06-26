import { useEffect, useRef, useState, useCallback, type RefObject } from "react";
import { cn } from "@/lib/utils";

interface Marker {
  top: number;
  height: number;
  type: "addition" | "deletion";
}

/**
 * VSCode-style change overview ruler. Renders colored markers next to the
 * scroll container, showing the location of added/removed lines in the file.
 * Click a spot in the ruler to scroll the container to that position.
 */
export function DiffMinimap({ scrollRef }: { scrollRef: RefObject<HTMLDivElement | null> }) {
  const minimapRef = useRef<HTMLDivElement>(null);
  const [markers, setMarkers] = useState<Marker[]>([]);
  const [thumb, setThumb] = useState({ top: 0, height: 0 });

  const update = useCallback(() => {
    const scroll = scrollRef.current;
    if (!scroll) return;
    const scrollHeight = scroll.scrollHeight;
    if (scrollHeight <= 0) {
      setMarkers([]);
      setThumb({ top: 0, height: 0 });
      return;
    }

    const containerRect = scroll.getBoundingClientRect();
    const rows = scroll.querySelectorAll<HTMLElement>(
      '[data-line-type="addition"], [data-line-type="deletion"]',
    );
    // Coalesce contiguous same-type lines into a single run. Drawing one marker
    // per line forces each to a 2px floor; adjacent lines then overlap and the
    // translucent fills compound into uneven dark/light bands. Merging a
    // continuous change into one marker renders it as one smooth bar.
    const next: Marker[] = [];
    let run: { topPx: number; bottomPx: number; type: "addition" | "deletion" } | null = null;
    const flush = () => {
      if (!run) return;
      next.push({
        top: run.topPx / scrollHeight,
        height: (run.bottomPx - run.topPx) / scrollHeight,
        type: run.type,
      });
      run = null;
    };
    rows.forEach((row) => {
      const rowRect = row.getBoundingClientRect();
      const topPx = rowRect.top - containerRect.top + scroll.scrollTop;
      const bottomPx = topPx + rowRect.height;
      const type = (row.dataset.lineType as "addition" | "deletion") ?? "addition";
      // Extend the current run if it's the same type and visually contiguous
      // (next row starts at/before the previous row's bottom, +1px slack).
      if (run && run.type === type && topPx <= run.bottomPx + 1) {
        run.bottomPx = Math.max(run.bottomPx, bottomPx);
      } else {
        flush();
        run = { topPx, bottomPx, type };
      }
    });
    flush();
    setMarkers(next);
    setThumb({
      top: scroll.scrollTop / scrollHeight,
      height: Math.min(1, scroll.clientHeight / scrollHeight),
    });
  }, [scrollRef]);

  useEffect(() => {
    const scroll = scrollRef.current;
    if (!scroll) return;

    update();

    const onScroll = () => {
      const sc = scrollRef.current;
      if (!sc || sc.scrollHeight <= 0) return;
      setThumb({
        top: sc.scrollTop / sc.scrollHeight,
        height: Math.min(1, sc.clientHeight / sc.scrollHeight),
      });
    };
    scroll.addEventListener("scroll", onScroll, { passive: true });

    const ro = new ResizeObserver(() => update());
    ro.observe(scroll);

    const mo = new MutationObserver(() => update());
    mo.observe(scroll, { childList: true, subtree: true });

    return () => {
      scroll.removeEventListener("scroll", onScroll);
      ro.disconnect();
      mo.disconnect();
    };
  }, [scrollRef, update]);

  const scrollToFraction = (fraction: number) => {
    const scroll = scrollRef.current;
    if (!scroll) return;
    const target = fraction * scroll.scrollHeight - scroll.clientHeight / 2;
    scroll.scrollTo({ top: Math.max(0, target), behavior: "smooth" });
  };

  const handleClick = (e: React.MouseEvent) => {
    const minimap = minimapRef.current;
    if (!minimap) return;
    const rect = minimap.getBoundingClientRect();
    const fraction = (e.clientY - rect.top) / rect.height;
    scrollToFraction(fraction);
  };

  return (
    <div
      ref={minimapRef}
      onClick={handleClick}
      className="relative w-2.5 shrink-0 cursor-pointer bg-muted/20 hover:bg-muted/40 transition-colors"
      title="Change overview"
    >
      {markers.map((m, i) => (
        <div
          key={i}
          className={cn(
            "absolute inset-x-0.5 pointer-events-none",
            m.type === "addition"
              ? "bg-green-500/70 dark:bg-green-400/60"
              : "bg-red-500/70 dark:bg-red-400/60",
          )}
          style={{
            top: `${m.top * 100}%`,
            height: `max(2px, ${m.height * 100}%)`,
          }}
        />
      ))}
      {thumb.height > 0 && thumb.height < 1 && (
        <div
          className="absolute inset-x-0 bg-foreground/15 pointer-events-none"
          style={{ top: `${thumb.top * 100}%`, height: `${thumb.height * 100}%` }}
        />
      )}
    </div>
  );
}
