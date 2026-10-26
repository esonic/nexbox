import hljs from "highlight.js/lib/core";
import "highlight.js/styles/github.css";

const LANG_IMPORTS: Record<string, () => Promise<{ default: unknown }>> = {
  javascript: () => import("highlight.js/lib/languages/javascript"),
  typescript: () => import("highlight.js/lib/languages/typescript"),
  python: () => import("highlight.js/lib/languages/python"),
  go: () => import("highlight.js/lib/languages/go"),
  java: () => import("highlight.js/lib/languages/java"),
  json: () => import("highlight.js/lib/languages/json"),
  yaml: () => import("highlight.js/lib/languages/yaml"),
  xml: () => import("highlight.js/lib/languages/xml"),
  css: () => import("highlight.js/lib/languages/css"),
  scss: () => import("highlight.js/lib/languages/scss"),
  bash: () => import("highlight.js/lib/languages/bash"),
  shell: () => import("highlight.js/lib/languages/shell"),
  sql: () => import("highlight.js/lib/languages/sql"),
  rust: () => import("highlight.js/lib/languages/rust"),
  cpp: () => import("highlight.js/lib/languages/cpp"),
  c: () => import("highlight.js/lib/languages/c"),
  ruby: () => import("highlight.js/lib/languages/ruby"),
  php: () => import("highlight.js/lib/languages/php"),
  swift: () => import("highlight.js/lib/languages/swift"),
  kotlin: () => import("highlight.js/lib/languages/kotlin"),
  markdown: () => import("highlight.js/lib/languages/markdown"),
  dockerfile: () => import("highlight.js/lib/languages/dockerfile"),
  plaintext: () => import("highlight.js/lib/languages/plaintext"),
};

const EXT_TO_LANG: Record<string, string> = {
  js: "javascript", jsx: "javascript", mjs: "javascript", cjs: "javascript",
  ts: "typescript", tsx: "typescript", mts: "typescript",
  py: "python", go: "go", java: "java",
  json: "json", yaml: "yaml", yml: "yaml",
  html: "xml", xml: "xml", svg: "xml", vue: "xml",
  css: "css", scss: "scss", less: "css",
  sh: "bash", bash: "bash", zsh: "bash",
  sql: "sql", rs: "rust",
  cpp: "cpp", cc: "cpp", cxx: "cpp", hpp: "cpp", h: "c", c: "c",
  rb: "ruby", php: "php", swift: "swift", kt: "kotlin", kts: "kotlin",
  md: "markdown", dockerfile: "dockerfile",
  mod: "go", toml: "yaml",
};

const registeredLangs = new Set<string>();

export function getLangFromPath(filepath: string): string | undefined {
  const ext = filepath.split(".").pop()?.toLowerCase() || "";
  const base = filepath.split("/").pop()?.toLowerCase() || "";
  if (base === "dockerfile") return "dockerfile";
  return EXT_TO_LANG[ext];
}

export async function ensureLangRegistered(lang: string): Promise<boolean> {
  if (registeredLangs.has(lang)) return true;
  const loader = LANG_IMPORTS[lang];
  if (!loader) return false;
  try {
    const mod = await loader();
    hljs.registerLanguage(lang, mod.default as Parameters<typeof hljs.registerLanguage>[1]);
    registeredLangs.add(lang);
    return true;
  } catch {
    return false;
  }
}

export function highlightCode(content: string, lang: string): string {
  try {
    return hljs.highlight(content, { language: lang }).value;
  } catch {
    return "";
  }
}

// Resolve a fenced-code language token (e.g. "ts", "sh", "typescript") to a
// registry key understood by ensureLangRegistered/highlightCode, or undefined
// when we don't bundle a grammar for it (caller renders plain text).
export function resolveLang(token: string): string | undefined {
  const t = token.toLowerCase();
  if (t in LANG_IMPORTS) return t;
  return EXT_TO_LANG[t];
}
