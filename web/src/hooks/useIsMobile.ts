import { useEffect, useState } from "react";

// Treat anything <=767px wide OR <=500px tall as mobile. The height clause
// catches landscape phones (e.g. iPhone 15 Pro at 852×393) so they get the
// mobile layout instead of a cramped, overflowing desktop one.
const MOBILE_QUERY = "(max-width: 767px), (max-height: 500px)";

export function useIsMobile(): boolean {
  const [isMobile, setIsMobile] = useState(() => {
    if (typeof window === "undefined") return false;
    return window.matchMedia(MOBILE_QUERY).matches;
  });

  useEffect(() => {
    if (typeof window === "undefined") return;
    const mql = window.matchMedia(MOBILE_QUERY);
    const handler = (e: MediaQueryListEvent) => setIsMobile(e.matches);
    mql.addEventListener("change", handler);
    return () => mql.removeEventListener("change", handler);
  }, []);

  return isMobile;
}
