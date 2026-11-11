import type { Terminal } from "@xterm/xterm";

// xterm's rendered screen covers its viewport on touch devices. Translate a
// single vertical drag into scrollback lines, preserving small partial moves.
export function bindTerminalTouchScroll(container: HTMLElement, term: Terminal): () => void {
  let start: { x: number; y: number } | null = null;
  let lastY = 0;
  let remainingPixels = 0;
  let vertical = false;
  const end = () => { start = null; remainingPixels = 0; vertical = false; };
  const onStart = (event: TouchEvent) => {
    end();
    if (event.touches.length !== 1 || term.buffer.active.type !== "normal" || term.hasSelection()) return;
    if (event.target instanceof Element && event.target.closest(".scrollbar")) return;
    const touch = event.touches[0];
    start = { x: touch.clientX, y: touch.clientY };
    lastY = touch.clientY;
  };
  const onMove = (event: TouchEvent) => {
    if (!start) return;
    if (event.touches.length !== 1 || term.buffer.active.type !== "normal" || term.hasSelection()) { end(); return; }
    const touch = event.touches[0];
    if (!vertical) {
      const dx = Math.abs(touch.clientX - start.x), dy = Math.abs(touch.clientY - start.y);
      if (Math.max(dx, dy) < 6) return;
      if (dx >= dy) { end(); return; }
      vertical = true;
    }
    const screen = term.element?.querySelector<HTMLElement>(".xterm-screen");
    const lineHeight = screen && term.rows > 0 ? screen.clientHeight / term.rows : 0;
    if (lineHeight <= 0) { end(); return; }
    remainingPixels += lastY - touch.clientY;
    lastY = touch.clientY;
    const lines = Math.trunc(remainingPixels / lineHeight);
    if (lines !== 0) {
      term.scrollLines(lines);
      remainingPixels -= lines * lineHeight;
    }
  };
  container.addEventListener("touchstart", onStart, { passive: true });
  container.addEventListener("touchmove", onMove, { passive: true });
  container.addEventListener("touchend", end);
  container.addEventListener("touchcancel", end);
  return () => {
    end();
    container.removeEventListener("touchstart", onStart);
    container.removeEventListener("touchmove", onMove);
    container.removeEventListener("touchend", end);
    container.removeEventListener("touchcancel", end);
  };
}
