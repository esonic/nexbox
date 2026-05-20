import React, { useEffect, useRef, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeRaw from "rehype-raw";
import { cn } from "@/lib/utils";

type MermaidRenderer = {
  initialize: (config: Record<string, unknown>) => void;
  render: (id: string, code: string) => Promise<{ svg: string }>;
  parse?: (code: string) => Promise<unknown> | unknown;
};

declare global {
  interface Window {
    mermaid?: MermaidRenderer;
    __nexboxMermaidScriptPromise?: Promise<void>;
    __nexboxMermaidRenderQueue?: Promise<void>;
  }
}

const MERMAID_CDN_URL = "https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.min.js";

const DARK_MERMAID_THEME_VARIABLES: Record<string, string> = {
  background: "#020b18",
  primaryColor: "#1f2937",
  primaryTextColor: "#e5e7eb",
  primaryBorderColor: "#cbd5e1",
  secondaryColor: "#111827",
  secondaryTextColor: "#e5e7eb",
  secondaryBorderColor: "#94a3b8",
  tertiaryColor: "#0b1220",
  tertiaryTextColor: "#e5e7eb",
  tertiaryBorderColor: "#334155",
  lineColor: "#cbd5e1",
  textColor: "#e5e7eb",
  clusterBkg: "#0f172a",
  clusterBorder: "#334155",
  actorBkg: "#1f2937",
  actorBorder: "#cbd5e1",
  actorTextColor: "#e5e7eb",
  noteBkgColor: "#111827",
  noteBorderColor: "#94a3b8",
  noteTextColor: "#e5e7eb",
  activationBkgColor: "#1f2937",
  activationBorderColor: "#94a3b8",
  signalColor: "#e5e7eb",
  signalTextColor: "#e5e7eb",
  labelBoxBkgColor: "#1f2937",
  labelBoxBorderColor: "#94a3b8",
  labelTextColor: "#e5e7eb",
  edgeLabelBackground: "#0b1220",
  sequenceNumberColor: "#e5e7eb",
};

function isDarkThemeActive(): boolean {
  if (typeof document === "undefined") return false;
  const body = document.body;
  const root = document.documentElement;
  return (
    body.classList.contains("dark") ||
    root.classList.contains("dark") ||
    body.getAttribute("data-theme") === "dark" ||
    root.getAttribute("data-theme") === "dark"
  );
}

async function ensureMermaidLoaded(): Promise<MermaidRenderer> {
  if (typeof window === "undefined") {
    throw new Error("Mermaid is only available in browser");
  }
  if (window.mermaid) return window.mermaid;
  if (!window.__nexboxMermaidScriptPromise) {
    window.__nexboxMermaidScriptPromise = new Promise<void>((resolve, reject) => {
      const script = document.createElement("script");
      script.src = MERMAID_CDN_URL;
      script.async = true;
      script.onload = () => resolve();
      script.onerror = () => reject(new Error("Failed to load mermaid runtime"));
      document.head.appendChild(script);
    });
  }
  await window.__nexboxMermaidScriptPromise;
  if (!window.mermaid) {
    throw new Error("Mermaid runtime unavailable");
  }
  return window.mermaid;
}

async function renderMermaidChart(chart: string, renderID: string, isDark: boolean): Promise<string> {
  if (typeof window === "undefined") {
    throw new Error("Mermaid is only available in browser");
  }

  let svg = "";
  const previous = window.__nexboxMermaidRenderQueue ?? Promise.resolve();
  const next = previous
    .catch(() => undefined)
    .then(async () => {
      const mermaid = await ensureMermaidLoaded();
      mermaid.initialize({
        startOnLoad: false,
        securityLevel: "loose",
        suppressErrorRendering: true,
        theme: isDark ? "base" : "default",
        themeVariables: isDark ? DARK_MERMAID_THEME_VARIABLES : undefined,
      });
      if (typeof mermaid.parse === "function") {
        await mermaid.parse(chart);
      }
      const rendered = await mermaid.render(renderID, chart);
      svg = rendered.svg;
    });

  window.__nexboxMermaidRenderQueue = next.then(
    () => undefined,
    () => undefined,
  );
  await next;
  return svg;
}

function MermaidBlock({ chart }: { chart: string }) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [error, setError] = useState<string | null>(null);
  const blockIdRef = useRef(`mermaid-${Math.random().toString(36).slice(2, 10)}`);
  const renderTokenRef = useRef(0);
  const [isDark, setIsDark] = useState<boolean>(() => isDarkThemeActive());

  useEffect(() => {
    if (typeof document === "undefined") return;
    const body = document.body;
    const root = document.documentElement;
    const sync = () => setIsDark(isDarkThemeActive());
    sync();
    const observer = new MutationObserver((mutations) => {
      for (const mutation of mutations) {
        if (
          mutation.type === "attributes" &&
          (mutation.attributeName === "class" || mutation.attributeName === "data-theme")
        ) {
          sync();
          break;
        }
      }
    });
    observer.observe(root, { attributes: true, attributeFilter: ["class", "data-theme"] });
    observer.observe(body, { attributes: true, attributeFilter: ["class", "data-theme"] });
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    let cancelled = false;
    const currentToken = ++renderTokenRef.current;

    const render = async () => {
      try {
        const source = chart.trim();
        if (!source) {
          if (containerRef.current) {
            containerRef.current.innerHTML = "";
          }
          setError(null);
          return;
        }
        const svg = await renderMermaidChart(source, `${blockIdRef.current}-${currentToken}`, isDark);
        if (cancelled || currentToken !== renderTokenRef.current) return;
        if (containerRef.current) {
          containerRef.current.innerHTML = svg;
        }
        setError(null);
      } catch (e) {
        if (cancelled) return;
        setError(e instanceof Error ? e.message : "Failed to render mermaid");
      }
    };

    render();
    return () => {
      cancelled = true;
    };
  }, [chart, isDark]);

  if (error) {
    return (
      <div className="text-xs text-destructive/90 bg-destructive/5 border border-destructive/30 rounded p-2">
        Mermaid render failed: {error}
      </div>
    );
  }

  return (
    <div className="my-2 rounded border bg-background/40 p-3 overflow-auto">
      <div ref={containerRef} />
    </div>
  );
}

export function isMarkdownPath(path: string): boolean {
  return /\.(md|markdown|mdown|mkd|mkdn|mdx)$/i.test(path);
}

interface MarkdownRendererProps {
  content: string;
  className?: string;
  enableMermaid?: boolean;
  components?: Record<string, React.ElementType>;
}

export function MarkdownRenderer({
  content,
  className,
  enableMermaid = true,
  components,
}: MarkdownRendererProps) {
  return (
    <div className={cn(className)}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[rehypeRaw]}
        components={{
          ...components,
          // Wide tables overflow narrow viewports — give every rendered table
          // its own horizontal scroll container so it stays in the message
          // column on phones instead of pushing the page wider.
          table({ children, ...props }) {
            return (
              <div className="overflow-x-auto -mx-1 my-2">
                <table {...props} className="min-w-full">
                  {children}
                </table>
              </div>
            );
          },
          code({ className: codeClassName, children, ...props }) {
            const lang = codeClassName?.replace("language-", "").toLowerCase() || "";
            const text = String(children ?? "").replace(/\n$/, "");
            if (enableMermaid && lang === "mermaid") {
              return <MermaidBlock chart={text} />;
            }
            return (
              <code className={codeClassName} {...props}>
                {children}
              </code>
            );
          },
        }}
      >
        {content}
      </ReactMarkdown>
    </div>
  );
}
