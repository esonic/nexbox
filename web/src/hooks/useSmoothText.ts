import { useEffect, useRef, useState } from "react";

const REVEAL_WINDOW_MS = 250;
const BASE_CHARS_PER_MS = 0.06;
// Cap how often the revealed text is committed to state. Each commit re-renders
// and re-parses the markdown, so during a long streaming reply this throttles
// the re-parse to ~15fps instead of running it on every animation frame (60fps).
const MIN_RENDER_INTERVAL_MS = 66;

/**
 * Gradually reveals characters of `target` via an RAF loop so streamed
 * chunks appear as a steady flow rather than discrete jumps. When `enabled`
 * is false, the returned string mirrors `target` immediately.
 */
export function useSmoothText(target: string, enabled: boolean): string {
  const [displayed, setDisplayed] = useState(target);
  const targetRef = useRef(target);
  const lenRef = useRef(target.length);

  useEffect(() => {
    targetRef.current = target;
    if (!enabled) {
      lenRef.current = target.length;
      setDisplayed(target);
    }
  }, [enabled, target]);

  useEffect(() => {
    if (!enabled) return;
    let last = performance.now();
    let lastRender = 0;
    let raf = 0;
    const tick = (now: number) => {
      const dt = now - last;
      last = now;
      const tgt = targetRef.current;
      const cur = lenRef.current;
      if (cur < tgt.length) {
        const lag = tgt.length - cur;
        const speed = Math.max(BASE_CHARS_PER_MS, lag / REVEAL_WINDOW_MS);
        const advance = Math.max(1, Math.floor(dt * speed));
        const next = Math.min(cur + advance, tgt.length);
        lenRef.current = next;
        // Advance the cursor every frame, but commit (→ re-render + markdown
        // re-parse) at most ~15fps. The final chunk is always shown at once so
        // the message never lags behind the finished stream.
        if (next === tgt.length || now - lastRender >= MIN_RENDER_INTERVAL_MS) {
          lastRender = now;
          setDisplayed(tgt.slice(0, next));
        }
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [enabled]);

  return displayed;
}
