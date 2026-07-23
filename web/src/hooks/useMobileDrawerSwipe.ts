import { useEffect, useState, type RefObject } from "react";

interface SwipeState {
  dragX: number;      // live drag offset, ≤ 0
  swiping: boolean;   // finger down & dragging → suppress transition
  panelW: number;     // measured panel width, for backdrop opacity
}

/**
 * Swipe-to-close for the mobile sidebar drawer. The drawer hugs the left edge,
 * so dragging it leftward dismisses it. Native touch listeners are used
 * (React's onTouch* are passive and can't preventDefault) so an engaged
 * horizontal drag won't also scroll the session list; vertical and rightward
 * gestures are left untouched.
 */
export function useMobileDrawerSwipe(
  panelRef: RefObject<HTMLDivElement | null>,
  isMobile: boolean,
  mobileOpen: boolean,
  setMobileOpen: (open: boolean) => void,
): SwipeState {
  const [dragX, setDragX] = useState(0);
  const [swiping, setSwiping] = useState(false);
  const [panelW, setPanelW] = useState(0);

  // Every close path (tap X, backdrop, route change, breakpoint, swipe) resets
  // the offset so the next open starts flush against the edge.
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- resetting the drag offset when the drawer closes is intentional
    if (!mobileOpen) { setDragX(0); setSwiping(false); }
  }, [mobileOpen]);

  useEffect(() => {
    const el = panelRef.current;
    if (!isMobile || !mobileOpen || !el) return;
    setPanelW(el.offsetWidth);

    let startX = 0, startY = 0, startT = 0, dx = 0;
    let mode: "pending" | "drag" | "ignore" = "pending";

    const onStart = (e: TouchEvent) => {
      const t = e.touches[0];
      startX = t.clientX; startY = t.clientY; startT = performance.now();
      dx = 0; mode = "pending";
    };
    const onMove = (e: TouchEvent) => {
      const t = e.touches[0];
      dx = t.clientX - startX;
      const dy = t.clientY - startY;
      if (mode === "pending") {
        if (Math.abs(dx) < 8 && Math.abs(dy) < 8) return; // wait for clear intent
        mode = Math.abs(dx) > Math.abs(dy) && dx < 0 ? "drag" : "ignore";
        if (mode === "drag") setSwiping(true);
      }
      if (mode !== "drag") return;
      e.preventDefault(); // hold the session list still mid-swipe
      setDragX(Math.min(0, dx));
    };
    const onEnd = () => {
      if (mode !== "drag") { mode = "pending"; return; }
      setSwiping(false);
      const width = el.offsetWidth || 1;
      const velocity = dx / Math.max(1, performance.now() - startT); // px/ms, <0 = left
      if (-dx > width / 3 || (velocity < -0.4 && -dx > 24)) {
        setDragX(-width);                                   // finish sliding out…
        window.setTimeout(() => setMobileOpen(false), 200); // …then unmount
      } else {
        setDragX(0);                                        // snap back
      }
      mode = "pending";
    };

    el.addEventListener("touchstart", onStart, { passive: true });
    el.addEventListener("touchmove", onMove, { passive: false });
    el.addEventListener("touchend", onEnd);
    el.addEventListener("touchcancel", onEnd);
    return () => {
      el.removeEventListener("touchstart", onStart);
      el.removeEventListener("touchmove", onMove);
      el.removeEventListener("touchend", onEnd);
      el.removeEventListener("touchcancel", onEnd);
    };
  }, [isMobile, mobileOpen, panelRef, setMobileOpen]);

  return { dragX, swiping, panelW };
}
