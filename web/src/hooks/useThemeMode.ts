import { useEffect, useState } from "react";

// BorderBeam tunes its glow colors per background brightness. The app toggles a
// `dark` class on <body> (see ThemeToggle), so mirror that here and keep it in
// sync via a MutationObserver so beam colors track live theme switches.
function getMode(): "dark" | "light" {
  if (typeof document === "undefined") return "dark";
  return document.body.classList.contains("dark") ? "dark" : "light";
}

export function useThemeMode(): "dark" | "light" {
  const [mode, setMode] = useState<"dark" | "light">(getMode);

  useEffect(() => {
    const obs = new MutationObserver(() => setMode(getMode()));
    obs.observe(document.body, { attributes: true, attributeFilter: ["class"] });
    return () => obs.disconnect();
  }, []);

  return mode;
}
