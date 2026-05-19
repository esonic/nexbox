import { useRef, useEffect, useState, useCallback, useId } from "react";
import { Loader2 } from "lucide-react";

interface WidgetBlockProps {
  title: string;
  widgetCode: string;
  loadingMessages?: string[];
}

/**
 * Renders widget_code (SVG or HTML) in a sandboxed iframe.
 * - Injects resolved theme colors from the host page
 * - Provides sendPrompt() bridge via postMessage
 * - Auto-resizes iframe height via ResizeObserver inside the iframe
 */
export function WidgetBlock({ title, widgetCode, loadingMessages }: WidgetBlockProps) {
  const widgetId = useId();
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const [iframeHeight, setIframeHeight] = useState(200);
  const [loaded, setLoaded] = useState(false);
  const [loadingIdx, setLoadingIdx] = useState(0);

  // Cycle through loading messages
  useEffect(() => {
    if (loaded || !loadingMessages || loadingMessages.length <= 1) return;
    const interval = setInterval(() => {
      setLoadingIdx((i) => (i + 1) % loadingMessages.length);
    }, 2000);
    return () => clearInterval(interval);
  }, [loaded, loadingMessages]);

  // Listen for postMessage from THIS iframe only (matched by widgetId)
  useEffect(() => {
    const handler = (e: MessageEvent) => {
      if (!e.data || typeof e.data !== "object") return;
      if (e.data.widgetId !== widgetId) return;
      if (e.data.type === "widget-resize" && typeof e.data.height === "number") {
        setIframeHeight(Math.max(60, Math.ceil(e.data.height) + 2));
        setLoaded(true);
      }
      if (e.data.type === "widget-send-prompt" && typeof e.data.text === "string") {
        window.dispatchEvent(
          new CustomEvent("widget-send-prompt", { detail: e.data.text })
        );
      }
    };
    window.addEventListener("message", handler);
    return () => window.removeEventListener("message", handler);
  }, [widgetId]);

  // Build the srcdoc for the iframe
  const buildSrcdoc = useCallback(() => {
    const cssVars = resolveThemeColors();
    const isDark = document.documentElement.classList.contains("dark");
    const isSvg = widgetCode.trimStart().startsWith("<svg");

    return `<!DOCTYPE html>
<html${isDark ? ' class="dark"' : ""} style="color-scheme: ${isDark ? "dark" : "light"}">
<head>
<meta charset="utf-8">
<style>
:root { ${cssVars} }
*, *::before, *::after { box-sizing: border-box; }
html, body {
  margin: 0;
  padding: 0;
  background: transparent;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  font-size: 16px;
  line-height: 1.7;
  color: var(--color-text-primary);
  overflow: hidden;
}
html::-webkit-scrollbar, body::-webkit-scrollbar { display: none; }
html { scrollbar-width: none; }
h1 { font-size: 22px; font-weight: 500; }
h2 { font-size: 18px; font-weight: 500; }
h3 { font-size: 16px; font-weight: 500; }
a { color: var(--color-info); }
input, select, textarea {
  font-family: inherit;
  font-size: 14px;
  padding: 6px 10px;
  border: 0.5px solid var(--color-border-secondary);
  border-radius: 8px;
  background: var(--color-bg-primary);
  color: var(--color-text-primary);
  height: 36px;
}
input:focus, select:focus, textarea:focus {
  outline: none;
  box-shadow: 0 0 0 2px var(--color-info);
}
input[type="range"] {
  height: auto;
  padding: 0;
  border: none;
  background: transparent;
}
button {
  font-family: inherit;
  font-size: 14px;
  padding: 6px 14px;
  border: 0.5px solid var(--color-border-secondary);
  border-radius: 8px;
  background: transparent;
  color: var(--color-text-primary);
  cursor: pointer;
}
button:hover { background: var(--color-bg-secondary); }
button:active { transform: scale(0.98); }
${isSvg ? svgClassStyles() : ""}
</style>
</head>
<body>
${widgetCode}
<script>
var __WIDGET_DARK__ = ${isDark};
var __WIDGET_ID__ = ${JSON.stringify(widgetId)};
function sendPrompt(text) {
  window.parent.postMessage({ type: "widget-send-prompt", text: text, widgetId: __WIDGET_ID__ }, "*");
}
function openLink(url) {
  window.open(url, "_blank", "noopener");
}
(function() {
  var reported = 0;
  function report() {
    var h = document.documentElement.scrollHeight;
    if (h !== reported) {
      reported = h;
      window.parent.postMessage({ type: "widget-resize", height: h, widgetId: __WIDGET_ID__ }, "*");
    }
  }
  if (typeof ResizeObserver !== "undefined") {
    new ResizeObserver(report).observe(document.body);
  }
  report();
  setTimeout(report, 100);
  setTimeout(report, 500);
  setTimeout(report, 2000);
})();
</script>
</body>
</html>`;
  }, [widgetCode, widgetId]);

  return (
    <div className="my-2 w-full">
      {!loaded && loadingMessages && loadingMessages.length > 0 && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground mb-2">
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
          <span>{loadingMessages[loadingIdx]}</span>
        </div>
      )}
      <iframe
        ref={iframeRef}
        srcDoc={buildSrcdoc()}
        sandbox="allow-scripts"
        title={title || "widget"}
        style={{
          width: "100%",
          height: loaded ? `${iframeHeight}px` : "0px",
          border: "none",
          borderRadius: "12px",
          overflow: "hidden",
          display: "block",
          opacity: loaded ? 1 : 0,
          transition: "opacity 0.3s ease, height 0.3s ease",
        }}
      />
    </div>
  );
}

/**
 * Resolve actual computed color values from the host theme.
 * Uses a temporary DOM element to force the browser to compute the real RGB,
 * avoiding issues with @layer, HSL format differences, etc.
 */
function resolveThemeColors(): string {
  const probe = document.createElement("div");
  probe.style.display = "none";
  document.body.appendChild(probe);

  function resolve(cssProp: "color" | "backgroundColor" | "borderColor", cssValue: string): string {
    probe.style.cssText = `display:none; ${cssProp === "color" ? "color" : cssProp === "borderColor" ? "border-color" : "background-color"}: ${cssValue} !important`;
    const computed = getComputedStyle(probe);
    const val = cssProp === "color" ? computed.color
      : cssProp === "borderColor" ? computed.borderColor
        : computed.backgroundColor;
    return val || "";
  }

  const vars: string[] = [];

  // Colors: use hsl(var(--xxx)) which is how shadcn references them
  const colorMappings: [string, string, "color" | "backgroundColor" | "borderColor"][] = [
    ["--color-bg-primary", "hsl(var(--background))", "backgroundColor"],
    ["--color-bg-secondary", "hsl(var(--muted))", "backgroundColor"],
    ["--color-bg-tertiary", "hsl(var(--accent))", "backgroundColor"],
    ["--color-text-primary", "hsl(var(--foreground))", "color"],
    ["--color-text-secondary", "hsl(var(--muted-foreground))", "color"],
    ["--color-text-tertiary", "hsl(var(--muted-foreground))", "color"],
    ["--color-border-primary", "hsl(var(--border))", "borderColor"],
    ["--color-border-secondary", "hsl(var(--border))", "borderColor"],
    ["--color-border-tertiary", "hsl(var(--border))", "borderColor"],
    ["--color-info", "hsl(var(--primary))", "color"],
    ["--color-success", "hsl(142 76% 36%)", "color"],
    ["--color-warning", "hsl(38 92% 50%)", "color"],
    ["--color-danger", "hsl(var(--destructive))", "color"],
  ];

  for (const [widgetVar, cssExpr, prop] of colorMappings) {
    const resolved = resolve(prop, cssExpr);
    if (resolved && resolved !== "rgba(0, 0, 0, 0)") {
      vars.push(`${widgetVar}: ${resolved}`);
    }
  }

  // Non-color values read directly
  const computed = getComputedStyle(document.documentElement);
  const radius = computed.getPropertyValue("--radius").trim();
  if (radius) {
    vars.push(`--border-radius-md: ${radius}`);
    vars.push(`--border-radius-lg: ${radius}`);
  }

  document.body.removeChild(probe);
  return vars.join("; ");
}

function svgClassStyles(): string {
  return `
svg text.t { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; font-size: 14px; fill: var(--color-text-primary); }
svg text.ts { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; font-size: 12px; fill: var(--color-text-secondary); }
svg text.th { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; font-size: 14px; font-weight: 500; fill: var(--color-text-primary); }
svg .box { fill: var(--color-bg-secondary); stroke: var(--color-border-tertiary); stroke-width: 0.5px; }
svg .arr { stroke: var(--color-text-secondary); stroke-width: 1.5px; fill: none; }
svg .leader { stroke: var(--color-border-tertiary); stroke-width: 0.5px; stroke-dasharray: 4 2; fill: none; }
svg .node { cursor: pointer; }
svg .node:hover { opacity: 0.85; }

svg .c-purple > rect, svg .c-purple > circle, svg .c-purple > ellipse, svg rect.c-purple, svg circle.c-purple, svg ellipse.c-purple { fill: #EEEDFE; stroke: #534AB7; stroke-width: 0.5px; }
svg .c-purple text.t, svg .c-purple text.th { fill: #3C3489; }
svg .c-purple text.ts { fill: #534AB7; }

svg .c-teal > rect, svg .c-teal > circle, svg .c-teal > ellipse, svg rect.c-teal, svg circle.c-teal, svg ellipse.c-teal { fill: #E1F5EE; stroke: #0F6E56; stroke-width: 0.5px; }
svg .c-teal text.t, svg .c-teal text.th { fill: #085041; }
svg .c-teal text.ts { fill: #0F6E56; }

svg .c-coral > rect, svg .c-coral > circle, svg .c-coral > ellipse, svg rect.c-coral, svg circle.c-coral, svg ellipse.c-coral { fill: #FAECE7; stroke: #993C1D; stroke-width: 0.5px; }
svg .c-coral text.t, svg .c-coral text.th { fill: #712B13; }
svg .c-coral text.ts { fill: #993C1D; }

svg .c-pink > rect, svg .c-pink > circle, svg .c-pink > ellipse, svg rect.c-pink, svg circle.c-pink, svg ellipse.c-pink { fill: #FBEAF0; stroke: #993556; stroke-width: 0.5px; }
svg .c-pink text.t, svg .c-pink text.th { fill: #72243E; }
svg .c-pink text.ts { fill: #993556; }

svg .c-gray > rect, svg .c-gray > circle, svg .c-gray > ellipse, svg rect.c-gray, svg circle.c-gray, svg ellipse.c-gray { fill: #F1EFE8; stroke: #5F5E5A; stroke-width: 0.5px; }
svg .c-gray text.t, svg .c-gray text.th { fill: #444441; }
svg .c-gray text.ts { fill: #5F5E5A; }

svg .c-blue > rect, svg .c-blue > circle, svg .c-blue > ellipse, svg rect.c-blue, svg circle.c-blue, svg ellipse.c-blue { fill: #E6F1FB; stroke: #185FA5; stroke-width: 0.5px; }
svg .c-blue text.t, svg .c-blue text.th { fill: #0C447C; }
svg .c-blue text.ts { fill: #185FA5; }

svg .c-green > rect, svg .c-green > circle, svg .c-green > ellipse, svg rect.c-green, svg circle.c-green, svg ellipse.c-green { fill: #EAF3DE; stroke: #3B6D11; stroke-width: 0.5px; }
svg .c-green text.t, svg .c-green text.th { fill: #27500A; }
svg .c-green text.ts { fill: #3B6D11; }

svg .c-amber > rect, svg .c-amber > circle, svg .c-amber > ellipse, svg rect.c-amber, svg circle.c-amber, svg ellipse.c-amber { fill: #FAEEDA; stroke: #854F0B; stroke-width: 0.5px; }
svg .c-amber text.t, svg .c-amber text.th { fill: #633806; }
svg .c-amber text.ts { fill: #854F0B; }

svg .c-red > rect, svg .c-red > circle, svg .c-red > ellipse, svg rect.c-red, svg circle.c-red, svg ellipse.c-red { fill: #FCEBEB; stroke: #A32D2D; stroke-width: 0.5px; }
svg .c-red text.t, svg .c-red text.th { fill: #791F1F; }
svg .c-red text.ts { fill: #A32D2D; }

html.dark svg text.t { fill: var(--color-text-primary); }
html.dark svg text.ts { fill: var(--color-text-secondary); }
html.dark svg text.th { fill: var(--color-text-primary); }
html.dark svg .box { fill: var(--color-bg-secondary); stroke: var(--color-border-tertiary); }
html.dark svg .arr { stroke: var(--color-text-secondary); }

html.dark svg .c-purple > rect, html.dark svg .c-purple > circle, html.dark svg .c-purple > ellipse, html.dark svg rect.c-purple, html.dark svg circle.c-purple, html.dark svg ellipse.c-purple { fill: #3C3489; stroke: #AFA9EC; }
html.dark svg .c-purple text.t, html.dark svg .c-purple text.th { fill: #CECBF6; }
html.dark svg .c-purple text.ts { fill: #AFA9EC; }

html.dark svg .c-teal > rect, html.dark svg .c-teal > circle, html.dark svg .c-teal > ellipse, html.dark svg rect.c-teal, html.dark svg circle.c-teal, html.dark svg ellipse.c-teal { fill: #085041; stroke: #5DCAA5; }
html.dark svg .c-teal text.t, html.dark svg .c-teal text.th { fill: #9FE1CB; }
html.dark svg .c-teal text.ts { fill: #5DCAA5; }

html.dark svg .c-coral > rect, html.dark svg .c-coral > circle, html.dark svg .c-coral > ellipse, html.dark svg rect.c-coral, html.dark svg circle.c-coral, html.dark svg ellipse.c-coral { fill: #712B13; stroke: #F0997B; }
html.dark svg .c-coral text.t, html.dark svg .c-coral text.th { fill: #F5C4B3; }
html.dark svg .c-coral text.ts { fill: #F0997B; }

html.dark svg .c-pink > rect, html.dark svg .c-pink > circle, html.dark svg .c-pink > ellipse, html.dark svg rect.c-pink, html.dark svg circle.c-pink, html.dark svg ellipse.c-pink { fill: #72243E; stroke: #ED93B1; }
html.dark svg .c-pink text.t, html.dark svg .c-pink text.th { fill: #F4C0D1; }
html.dark svg .c-pink text.ts { fill: #ED93B1; }

html.dark svg .c-gray > rect, html.dark svg .c-gray > circle, html.dark svg .c-gray > ellipse, html.dark svg rect.c-gray, html.dark svg circle.c-gray, html.dark svg ellipse.c-gray { fill: #444441; stroke: #B4B2A9; }
html.dark svg .c-gray text.t, html.dark svg .c-gray text.th { fill: #D3D1C7; }
html.dark svg .c-gray text.ts { fill: #B4B2A9; }

html.dark svg .c-blue > rect, html.dark svg .c-blue > circle, html.dark svg .c-blue > ellipse, html.dark svg rect.c-blue, html.dark svg circle.c-blue, html.dark svg ellipse.c-blue { fill: #0C447C; stroke: #85B7EB; }
html.dark svg .c-blue text.t, html.dark svg .c-blue text.th { fill: #B5D4F4; }
html.dark svg .c-blue text.ts { fill: #85B7EB; }

html.dark svg .c-green > rect, html.dark svg .c-green > circle, html.dark svg .c-green > ellipse, html.dark svg rect.c-green, html.dark svg circle.c-green, html.dark svg ellipse.c-green { fill: #27500A; stroke: #97C459; }
html.dark svg .c-green text.t, html.dark svg .c-green text.th { fill: #C0DD97; }
html.dark svg .c-green text.ts { fill: #97C459; }

html.dark svg .c-amber > rect, html.dark svg .c-amber > circle, html.dark svg .c-amber > ellipse, html.dark svg rect.c-amber, html.dark svg circle.c-amber, html.dark svg ellipse.c-amber { fill: #633806; stroke: #EF9F27; }
html.dark svg .c-amber text.t, html.dark svg .c-amber text.th { fill: #FAC775; }
html.dark svg .c-amber text.ts { fill: #EF9F27; }

html.dark svg .c-red > rect, html.dark svg .c-red > circle, html.dark svg .c-red > ellipse, html.dark svg rect.c-red, html.dark svg circle.c-red, html.dark svg ellipse.c-red { fill: #791F1F; stroke: #F09595; }
html.dark svg .c-red text.t, html.dark svg .c-red text.th { fill: #F7C1C1; }
html.dark svg .c-red text.ts { fill: #F09595; }
`;
}
