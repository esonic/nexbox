import { useEffect, useState } from "react";
import { Moon, Sun } from "lucide-react";

function getTheme(): "light" | "dark" {
  if (typeof window === "undefined") return "light";
  return localStorage.getItem("theme") === "dark" ? "dark" : "light";
}

function applyTheme(theme: "light" | "dark") {
  document.body.classList.toggle("dark", theme === "dark");
  localStorage.setItem("theme", theme);
  const meta = document.getElementById("theme-color-meta");
  if (meta) meta.setAttribute("content", theme === "dark" ? "#0d0d0d" : "#ffffff");
}

export function ThemeToggle() {
  const [theme, setTheme] = useState<"light" | "dark">(getTheme);

  useEffect(() => {
    applyTheme(theme);
  }, [theme]);

  return (
    <button
      onClick={() => setTheme(theme === "light" ? "dark" : "light")}
      className="p-2 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
      title={theme === "light" ? "Switch to dark mode" : "Switch to light mode"}
    >
      {theme === "light" ? (
        <Moon className="h-4 w-4" />
      ) : (
        <Sun className="h-4 w-4" />
      )}
    </button>
  );
}
