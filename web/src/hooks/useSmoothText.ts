import { useEffect, useRef, useState } from "react";

const REVEAL_WINDOW_MS = 250;
const BASE_CHARS_PER_MS = 0.06;

// Each state commit re-renders and re-parses the (syntax-highlighted) markdown,
// and that cost grows with the reply length. So throttle adaptively rather than
// at a flat rate: short/medium replies commit on every animation frame for a
// fully smooth reveal, and only long, parse-heavy replies fall back to a capped
// rate to protect the frame budget. Returns the minimum ms between commits.
function renderIntervalFor(len: number): number {
  if (len < 2000) return 0; // every frame (~60fps) — smooth
  if (len < 6000) return 33; // ~30fps
  return 50; // ~20fps for very long replies
}

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
        // re-parse) no more often than the adaptive interval. The final chunk is
        // always shown at once so the message never lags behind the finished stream.
        if (next === tgt.length || now - lastRender >= renderIntervalFor(tgt.length)) {
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
