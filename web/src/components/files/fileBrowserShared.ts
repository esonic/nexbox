import type { FileEntry } from "@/lib/api";

// MIME type detection by extension for the drag-source marker. The exact value
// is irrelevant; only the presence of this marker distinguishes internal moves
// from OS file drops.
export const INTERNAL_DRAG_TYPE = "application/x-beehub-files";

export function dirnameOf(p: string): string {
  if (!p) return "";
  const idx = p.lastIndexOf("/");
  return idx === -1 ? "" : p.slice(0, idx);
}

export function joinPath(dir: string, name: string): string {
  if (!dir) return name;
  return `${dir}/${name}`;
}

// The folder a drop on `entry` should target: a directory accepts into itself,
// a file accepts into its containing folder (so dragging onto any row inside an
// expanded folder lands in that folder rather than falling through to the root).
export function dropTargetDir(entry: FileEntry): string {
  return entry.is_dir ? entry.path : dirnameOf(entry.path);
}

export function isHtmlPath(p: string): boolean {
  const ext = p.split(".").pop()?.toLowerCase() || "";
  return ext === "html" || ext === "htm";
}

export function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// ── Git status decorations (VSCode-style) ──

// Letter badge + name color for a single git change. `dir` is the aggregate
// marker shown on folders that contain changes.
export type GitChangeKind = "modified" | "added" | "deleted" | "renamed" | "untracked" | "dir-tracked" | "dir-untracked";
export interface GitDeco {
  kind: GitChangeKind;
}

// VSCode's exact git-decoration foreground colors (Light+ / Dark+ themes), which
// are softer/less saturated than Tailwind's amber-500/green-600. Modified =
// tan-gold, added/untracked = sage green, deleted = muted red.
export const GIT_DECO_COLOR: Record<GitChangeKind, string> = {
  modified: "text-[#895503] dark:text-[#e2c08d]",
  renamed: "text-[#895503] dark:text-[#e2c08d]",
  added: "text-[#587c0c] dark:text-[#81b88b]",
  untracked: "text-[#007100] dark:text-[#73c991]",
  deleted: "text-[#ad0707] dark:text-[#c74e39]",
  "dir-tracked": "text-[#895503] dark:text-[#e2c08d]",
  "dir-untracked": "text-[#007100] dark:text-[#73c991]",
};

// Single-letter badge shown at the right edge of a changed file row.
export const GIT_DECO_LETTER: Partial<Record<GitChangeKind, string>> = {
  modified: "M",
  added: "A",
  deleted: "D",
  renamed: "R",
  untracked: "U",
};
