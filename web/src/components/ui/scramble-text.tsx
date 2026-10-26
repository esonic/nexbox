import { useEffect, useRef, useState } from "react";

const SCRAMBLE_CHARS = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz#%&*+-<>?/";

/**
 * Animates between text values with a per-character "decode" scramble: each
 * position flickers through random glyphs before settling on its target
 * character, so swapping words reads like a terminal decode rather than a hard
 * cut. Characters start and finish on slightly staggered frames for a rippling
 * feel.
 *
 * Render in a monospace font (font-mono) to avoid width jitter while glyphs
 * change. Color is inherited, so wrapping this in a `.mood-text` keeps it in
 * sync with the spinner's hue cycle.
 */
export function ScrambleText({ text, className }: { text: string; className?: string }) {
  const [display, setDisplay] = useState(text);
  const prevText = useRef(text);
  const rafRef = useRef<number | null>(null);

  useEffect(() => {
    const from = prevText.current;
    const to = text;
    prevText.current = to;
    if (from === to) return;

    const length = Math.max(from.length, to.length);
    const queue = Array.from({ length }, (_, i) => {
      const start = Math.floor(Math.random() * 30);
      return {
        fromChar: from[i] ?? "",
        toChar: to[i] ?? "",
        start,
        end: start + 30 + Math.floor(Math.random() * 30),
        scramble: "",
      };
    });

    let frame = 0;
    const tick = () => {
      let output = "";
      let done = 0;
      for (const q of queue) {
        if (frame >= q.end) {
          done++;
          output += q.toChar;
        } else if (frame >= q.start) {
          // Re-roll the placeholder glyph occasionally so it flickers.
          if (!q.scramble || Math.random() < 0.28) {
            q.scramble = SCRAMBLE_CHARS[Math.floor(Math.random() * SCRAMBLE_CHARS.length)];
          }
          output += q.scramble;
        } else {
          output += q.fromChar;
        }
      }
      setDisplay(output);
      if (done === queue.length) {
        rafRef.current = null;
        return;
      }
      frame++;
      rafRef.current = requestAnimationFrame(tick);
    };

    rafRef.current = requestAnimationFrame(tick);
    return () => {
      if (rafRef.current !== null) cancelAnimationFrame(rafRef.current);
    };
  }, [text]);

  return <span className={className}>{display}</span>;
}
