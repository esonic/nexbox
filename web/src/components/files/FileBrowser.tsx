import { useState, useEffect, useLayoutEffect, useCallback, useMemo, useRef, lazy, Suspense } from "react";
import {
  ChevronRight,
  ChevronDown,
  ChevronUp,
  File,
  Folder,
  FolderOpen,
  Loader2,
  RefreshCw,
  Search,
  ListFilter,
  Upload,
  Download,
  Trash2,
  Save,
  ExternalLink,
  X,
  FileText,
  FileCode,
  Eye,
  Code2,
  PanelLeftClose,
  PanelLeftOpen,
} from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { filesApi, serverApi, gitApi, type FileEntry, type FileContentResponse, type FileSearchResult, type GitStatusResponse } from "@/lib/api";
import { getLangFromPath, ensureLangRegistered, highlightCode } from "@/lib/highlight";
import { useFileBrowserStore } from "@/stores/fileBrowser";
import { useLayoutStore } from "@/stores/layout";
import { FILE_TREE_WIDTH_MIN, FILE_TREE_WIDTH_MAX } from "@/lib/constants";
import { MarkdownRenderer, isMarkdownPath } from "@/components/markdown/MarkdownRenderer";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from "@/components/ui/dropdown-menu";

const MonacoFileEditor = lazy(() => import("./MonacoFileEditor"));
// Type-only import: erased at build time, so it does not pull the Monaco chunk eagerly.
import type { MonacoFileEditorHandle } from "./MonacoFileEditor";

// MIME type detection by extension for the drag-source marker. The exact value
// is irrelevant; only the presence of this marker distinguishes internal moves
// from OS file drops.
const INTERNAL_DRAG_TYPE = "application/x-nexbox-files";

function dirnameOf(p: string): string {
  if (!p) return "";
  const idx = p.lastIndexOf("/");
  return idx === -1 ? "" : p.slice(0, idx);
}

function joinPath(dir: string, name: string): string {
  if (!dir) return name;
  return `${dir}/${name}`;
}

// The folder a drop on `entry` should target: a directory accepts into itself,
// a file accepts into its containing folder (so dragging onto any row inside an
// expanded folder lands in that folder rather than falling through to the root).
function dropTargetDir(entry: FileEntry): string {
  return entry.is_dir ? entry.path : dirnameOf(entry.path);
}

function isHtmlPath(p: string): boolean {
  const ext = p.split(".").pop()?.toLowerCase() || "";
  return ext === "html" || ext === "htm";
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// ── Git status decorations (VSCode-style) ──

// Letter badge + name color for a single git change. `dir` is the aggregate
// marker shown on folders that contain changes.
type GitChangeKind = "modified" | "added" | "deleted" | "renamed" | "untracked" | "dir-tracked" | "dir-untracked";
interface GitDeco {
  kind: GitChangeKind;
}

// VSCode's exact git-decoration foreground colors (Light+ / Dark+ themes), which
// are softer/less saturated than Tailwind's amber-500/green-600. Modified =
// tan-gold, added/untracked = sage green, deleted = muted red.
const GIT_DECO_COLOR: Record<GitChangeKind, string> = {
  modified: "text-[#895503] dark:text-[#e2c08d]",
  renamed: "text-[#895503] dark:text-[#e2c08d]",
  added: "text-[#587c0c] dark:text-[#81b88b]",
  untracked: "text-[#007100] dark:text-[#73c991]",
  deleted: "text-[#ad0707] dark:text-[#c74e39]",
  "dir-tracked": "text-[#895503] dark:text-[#e2c08d]",
  "dir-untracked": "text-[#007100] dark:text-[#73c991]",
};

// Single-letter badge shown at the right edge of a changed file row.
const GIT_DECO_LETTER: Partial<Record<GitChangeKind, string>> = {
  modified: "M",
  added: "A",
  deleted: "D",
  renamed: "R",
  untracked: "U",
};

// ── File tree item ──

function FileTreeItem({
  entry,
  depth,
  expanded,
  onToggle,
  highlightedPath,
  onSelect,
  onHighlight,
  getChildren,
  isLoading,
  getGitDecoration,
  dragOverPath,
  onDragStartItem,
  onDragOverItem,
  onDropOnItem,
  onDragEndItem,
}: {
  entry: FileEntry;
  depth: number;
  expanded: Set<string>;
  onToggle: (path: string) => void;
  highlightedPath: string | null;
  onSelect: (path: string) => void;
  onHighlight: (path: string) => void;
  getChildren: (path: string) => FileEntry[] | undefined;
  isLoading: (path: string) => boolean;
  getGitDecoration: (path: string, isDir: boolean) => GitDeco | undefined;
  dragOverPath: string | null;
  onDragStartItem: (entry: FileEntry, e: React.DragEvent) => void;
  onDragOverItem: (entry: FileEntry, e: React.DragEvent) => void;
  onDropOnItem: (entry: FileEntry, e: React.DragEvent) => void;
  onDragEndItem: () => void;
}) {
  const isExpanded = expanded.has(entry.path);
  const isHighlighted = highlightedPath === entry.path;
  const isDropTarget = entry.is_dir && dragOverPath === entry.path;
  const children = isExpanded && entry.is_dir ? getChildren(entry.path) : undefined;
  const loading = isExpanded && entry.is_dir && children === undefined && isLoading(entry.path);
  const gitDeco = getGitDecoration(entry.path, entry.is_dir);
  const gitColor = gitDeco ? GIT_DECO_COLOR[gitDeco.kind] : undefined;
  const gitLetter = gitDeco ? GIT_DECO_LETTER[gitDeco.kind] : undefined;

  return (
    <>
      <div
        draggable
        onDragStart={(e) => onDragStartItem(entry, e)}
        onDragOver={(e) => onDragOverItem(entry, e)}
        onDrop={(e) => onDropOnItem(entry, e)}
        onDragEnd={onDragEndItem}
        className={cn(
          "flex items-center gap-1 py-[3px] pr-2 cursor-pointer text-xs hover:bg-accent/50 transition-colors",
          isHighlighted && "bg-accent",
          isDropTarget && "ring-1 ring-inset ring-primary/60 bg-primary/10"
        )}
        style={{ paddingLeft: depth * 16 + 8 }}
        onClick={() => {
          onHighlight(entry.path);
          if (entry.is_dir) {
            onToggle(entry.path);
          } else {
            onSelect(entry.path);
          }
        }}
        title={entry.is_symlink ? `${entry.name} (symlink)` : entry.name}
      >
        {entry.is_dir ? (
          <>
            {isExpanded ? (
              <ChevronDown className="h-3 w-3 text-muted-foreground/50 shrink-0" />
            ) : (
              <ChevronRight className="h-3 w-3 text-muted-foreground/50 shrink-0" />
            )}
            {isExpanded ? (
              <FolderOpen className="h-3.5 w-3.5 text-blue-400/70 shrink-0" />
            ) : (
              <Folder className="h-3.5 w-3.5 text-blue-400/70 shrink-0" />
            )}
          </>
        ) : (
          <>
            <span className="w-3 shrink-0" />
            <File className="h-3.5 w-3.5 text-muted-foreground/50 shrink-0" />
          </>
        )}
        {/* Server-compacted folder chains arrive as "a/b/c" (path = innermost folder);
            render segments with dimmed separators like VSCode compact folders. */}
        <span className={cn("truncate min-w-0", entry.is_symlink && "italic", gitColor)}>
          {(entry.is_dir ? entry.name.split("/") : [entry.name]).map((segment, i) => (
            <span key={i}>
              {i > 0 && <span className="text-muted-foreground/40 px-px">/</span>}
              {segment}
            </span>
          ))}
        </span>
        {/* Git change marker at the right edge: a letter badge for files, a dot
            for folders that contain changes (VSCode-style). */}
        {gitLetter ? (
          <span className={cn("ml-auto pl-1 shrink-0 font-semibold tabular-nums", gitColor)}>{gitLetter}</span>
        ) : gitDeco && entry.is_dir ? (
          <span className={cn("ml-auto pl-1 shrink-0 text-base leading-none", gitColor)}>•</span>
        ) : null}
      </div>
      {isExpanded && entry.is_dir && (
        loading ? (
          <div
            className="flex items-center gap-1 py-[3px] text-xs text-muted-foreground/50"
            style={{ paddingLeft: (depth + 1) * 16 + 8 }}
          >
            <Loader2 className="h-3 w-3 animate-spin" />
          </div>
        ) : children?.map((child) => (
          <FileTreeItem
            key={child.path}
            entry={child}
            depth={depth + 1}
            expanded={expanded}
            onToggle={onToggle}
            highlightedPath={highlightedPath}
            onSelect={onSelect}
            onHighlight={onHighlight}
            getChildren={getChildren}
            isLoading={isLoading}
            getGitDecoration={getGitDecoration}
            dragOverPath={dragOverPath}
            onDragStartItem={onDragStartItem}
            onDragOverItem={onDragOverItem}
            onDropOnItem={onDropOnItem}
            onDragEndItem={onDragEndItem}
          />
        ))
      )}
    </>
  );
}

// ── Search results ──

function ContentSnippet({ content, query }: { content: string; query: string }) {
  if (!query || !content) return <>{content}</>;
  const lower = content.toLowerCase();
  const lowerQ = query.toLowerCase();
  const idx = lower.indexOf(lowerQ);
  if (idx === -1) return <>{content}</>;
  const prePad = 15;
  const postPad = 60;
  const start = Math.max(0, idx - prePad);
  const end = Math.min(content.length, idx + query.length + postPad);
  return (
    <>
      {start > 0 && "…"}
      {content.slice(start, idx)}
      <mark className="bg-yellow-300/50 dark:bg-yellow-600/50 text-foreground rounded-[2px] not-italic">{content.slice(idx, idx + query.length)}</mark>
      {content.slice(idx + query.length, end)}
      {end < content.length && "…"}
    </>
  );
}

function SearchResults({
  results,
  loading,
  searchType,
  query,
  onSelect,
}: {
  results: FileSearchResult[];
  loading: boolean;
  searchType: "name" | "content";
  query: string;
  onSelect: (path: string, line?: number) => void;
}) {
  if (loading) {
    return (
      <div className="flex items-center justify-center py-8">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      </div>
    );
  }
  if (results.length === 0) {
    return (
      <div className="text-xs text-muted-foreground text-center py-6">
        No results
      </div>
    );
  }

  if (searchType === "content") {
    const grouped = new Map<string, FileSearchResult[]>();
    for (const r of results) {
      const list = grouped.get(r.path) || [];
      list.push(r);
      grouped.set(r.path, list);
    }
    return (
      <div className="text-xs">
        {[...grouped.entries()].map(([path, matches]) => (
          <div key={path}>
            <div
              className="flex items-center gap-1.5 px-2 py-1.5 bg-muted/30 cursor-pointer hover:bg-accent/50 transition-colors"
              onClick={() => onSelect(path)}
            >
              <FileCode className="h-3 w-3 text-muted-foreground/60 shrink-0" />
              <span className="font-medium text-foreground/80 truncate">{path}</span>
              <span className="ml-auto text-muted-foreground/40 shrink-0">{matches.length}</span>
            </div>
            {matches.map((m, i) => (
              <div
                key={i}
                className="px-3 py-1 text-[11px] text-muted-foreground hover:bg-accent/30 cursor-pointer font-mono overflow-hidden whitespace-nowrap"
                onClick={() => onSelect(m.path, m.line)}
              >
                <ContentSnippet content={m.content ?? ""} query={query} />
              </div>
            ))}
          </div>
        ))}
      </div>
    );
  }

  return (
    <div className="text-xs">
      {results.map((r, i) => (
        <div
          key={i}
          className="flex items-center gap-1.5 px-2 py-1.5 hover:bg-accent/50 cursor-pointer transition-colors"
          onClick={() => onSelect(r.path)}
        >
          <FileText className="h-3 w-3 text-muted-foreground/50 shrink-0" />
          <span className="truncate">
            <span className="text-muted-foreground">{r.path.substring(0, r.path.lastIndexOf("/") + 1)}</span>
            <span className="font-medium text-foreground/80">{r.name || r.path.split("/").pop()}</span>
          </span>
        </div>
      ))}
    </div>
  );
}

// ── In-file search ──

interface LineMatch {
  start: number;
  end: number;
  globalIndex: number;
}

function computeLineMatches(lines: string[], query: string, caseSensitive: boolean): Map<number, LineMatch[]> {
  const result = new Map<number, LineMatch[]>();
  if (!query) return result;
  const needle = caseSensitive ? query : query.toLowerCase();
  let globalIndex = 0;

  for (let i = 0; i < lines.length; i++) {
    const haystack = caseSensitive ? lines[i] : lines[i].toLowerCase();
    let pos = 0;
    while (pos <= haystack.length - needle.length) {
      const idx = haystack.indexOf(needle, pos);
      if (idx === -1) break;
      const matches = result.get(i) ?? [];
      matches.push({ start: idx, end: idx + needle.length, globalIndex });
      result.set(i, matches);
      globalIndex++;
      pos = idx + 1;
    }
  }

  return result;
}

function HighlightOverlay({
  line,
  matches,
  currentGlobalIndex,
}: {
  line: string;
  matches: LineMatch[];
  currentGlobalIndex: number;
}) {
  const parts: React.ReactNode[] = [];
  let pos = 0;

  for (const match of matches) {
    if (pos < match.start) {
      parts.push(<span key={`text-${pos}`}>{line.slice(pos, match.start)}</span>);
    }

    const isCurrent = match.globalIndex === currentGlobalIndex;
    parts.push(
      <mark
        key={`match-${match.start}-${match.globalIndex}`}
        data-match-idx={match.globalIndex}
        style={{
          background: isCurrent ? "rgba(255, 160, 0, 0.65)" : "rgba(255, 220, 0, 0.35)",
          color: "transparent",
          borderRadius: "2px",
        }}
      >
        {line.slice(match.start, match.end)}
      </mark>
    );

    pos = match.end;
  }

  if (pos < line.length) {
    parts.push(<span key={`text-${pos}`}>{line.slice(pos)}</span>);
  }

  return (
    <div
      className="absolute inset-0 pl-2 pr-3 py-0 whitespace-pre-wrap break-all pointer-events-none select-none"
      style={{ color: "transparent", fontFamily: "inherit", fontSize: "inherit", lineHeight: "inherit" }}
      aria-hidden
    >
      {parts}
    </div>
  );
}

function FileSearchBar({
  query,
  caseSensitive,
  currentIndex,
  total,
  onQueryChange,
  onCaseSensitiveChange,
  onPrev,
  onNext,
  onClose,
}: {
  query: string;
  caseSensitive: boolean;
  currentIndex: number;
  total: number;
  onQueryChange: (query: string) => void;
  onCaseSensitiveChange: (value: boolean) => void;
  onPrev: () => void;
  onNext: () => void;
  onClose: () => void;
}) {
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
    inputRef.current?.select();
  }, []);

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Escape") {
      onClose();
      return;
    }
    if (e.key === "Enter") {
      if (e.shiftKey) onPrev();
      else onNext();
    }
  };

  return (
    <div className="absolute top-2 right-3 z-50 flex items-center gap-1 bg-popover border border-border rounded-md shadow-lg px-2 py-1.5">
      <div className="flex items-center gap-1 bg-muted/50 rounded px-1.5 border border-transparent focus-within:border-ring/50">
        <input
          ref={inputRef}
          value={query}
          onChange={(e) => onQueryChange(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder="Find in file..."
          className="bg-transparent text-xs py-0.5 outline-none w-36 placeholder:text-muted-foreground/40"
        />
        {query && (
          <button onClick={() => onQueryChange("")} className="text-muted-foreground/40 hover:text-muted-foreground">
            <X className="h-3 w-3" />
          </button>
        )}
      </div>
      <button
        onClick={() => onCaseSensitiveChange(!caseSensitive)}
        title="Match case"
        className={cn(
          "h-5 w-6 rounded text-[11px] font-semibold transition-colors border",
          caseSensitive
            ? "bg-primary/10 text-primary border-primary/30"
            : "text-muted-foreground/50 hover:text-muted-foreground border-transparent hover:border-border"
        )}
      >
        Aa
      </button>
      <span
        className={cn(
          "text-[11px] min-w-[4.5rem] text-center tabular-nums",
          query && total === 0 ? "text-destructive/70" : "text-muted-foreground/60"
        )}
      >
        {query ? (total === 0 ? "No results" : `${currentIndex + 1} of ${total}`) : ""}
      </span>
      <button
        onClick={onPrev}
        disabled={total === 0}
        className="p-0.5 rounded hover:bg-accent text-muted-foreground/60 hover:text-foreground transition-colors disabled:opacity-30"
        title="Previous match (Shift+Enter)"
      >
        <ChevronUp className="h-3.5 w-3.5" />
      </button>
      <button
        onClick={onNext}
        disabled={total === 0}
        className="p-0.5 rounded hover:bg-accent text-muted-foreground/60 hover:text-foreground transition-colors disabled:opacity-30"
        title="Next match (Enter)"
      >
        <ChevronDown className="h-3.5 w-3.5" />
      </button>
      <button
        onClick={onClose}
        className="p-0.5 rounded hover:bg-accent text-muted-foreground/50 hover:text-foreground transition-colors"
        title="Close (Escape)"
      >
        <X className="h-3.5 w-3.5" />
      </button>
    </div>
  );
}

// ── Code viewer with highlight.js ──

function CodeViewer({
  content,
  filepath,
  searchQuery = "",
  caseSensitive = false,
  currentMatchIndex = -1,
  onMatchesFound,
  scrollToLine,
  onScrollToLineReady,
}: {
  content: string;
  filepath: string;
  searchQuery?: string;
  caseSensitive?: boolean;
  currentMatchIndex?: number;
  onMatchesFound?: (total: number) => void;
  scrollToLine?: number;
  onScrollToLineReady?: (matchIndex: number) => void;
}) {
  const lang = useMemo(() => getLangFromPath(filepath), [filepath]);
  const [langReady, setLangReady] = useState(false);

  useEffect(() => {
    if (!lang) return;
    let cancelled = false;
    ensureLangRegistered(lang).then((ok) => {
      if (!cancelled) setLangReady(ok);
    });
    return () => { cancelled = true; };
  }, [lang]);

  const lines = useMemo(() => content.split("\n"), [content]);

  const highlightedLines = useMemo(() => {
    if (!langReady || !lang) return null;
    return lines.map((line) => highlightCode(line, lang));
  }, [lines, langReady, lang]);

  const gutterWidth = String(lines.length).length;
  const lineMatches = useMemo(
    () => computeLineMatches(lines, searchQuery, caseSensitive),
    [lines, searchQuery, caseSensitive]
  );
  const totalMatches = useMemo(() => {
    let total = 0;
    lineMatches.forEach((matches) => {
      total += matches.length;
    });
    return total;
  }, [lineMatches]);

  useEffect(() => {
    onMatchesFound?.(totalMatches);
  }, [totalMatches, onMatchesFound]);

  useEffect(() => {
    if (currentMatchIndex < 0) return;
    if (scrollToLine != null) return; // useLayoutEffect handles scroll when navigating from search results
    const currentMatch = document.querySelector(`[data-match-idx="${currentMatchIndex}"]`);
    currentMatch?.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [currentMatchIndex, scrollToLine]);

  useLayoutEffect(() => {
    if (scrollToLine == null || scrollToLine <= 0) return;
    const targetLineIdx = scrollToLine - 1;
    if (searchQuery && lineMatches.size > 0) {
      let globalIdx = 0;
      let found = false;
      for (const [lineIdx, matches] of lineMatches) {
        if (lineIdx === targetLineIdx) {
          document.querySelector(`[data-match-idx="${globalIdx}"]`)?.scrollIntoView({ block: "center", behavior: "instant" });
          onScrollToLineReady?.(globalIdx);
          found = true;
          break;
        } else if (lineIdx < targetLineIdx) {
          globalIdx += matches.length;
        }
      }
      if (!found) {
        document.querySelector(`[data-line-idx="${targetLineIdx}"]`)?.scrollIntoView({ block: "center", behavior: "instant" });
        onScrollToLineReady?.(-1);
      }
    } else if (!searchQuery) {
      document.querySelector(`[data-line-idx="${targetLineIdx}"]`)?.scrollIntoView({ block: "center", behavior: "instant" });
    }
  }, [scrollToLine, lineMatches, searchQuery, onScrollToLineReady]);

  return (
    <div className="overflow-auto h-full">
      <table className="w-full text-xs font-mono leading-[1.4]">
        <tbody>
          {lines.map((line, i) => {
            const matches = lineMatches.get(i);

            return (
              <tr key={i} data-line-idx={i} className="hover:bg-accent/30">
                <td
                  className="pl-2 pr-2 py-0 text-right select-none text-muted-foreground/40 border-r border-border/20 align-top whitespace-nowrap"
                  style={{ width: gutterWidth * 8 + 16 }}
                >
                  {i + 1}
                </td>
                <td className="pl-2 pr-3 py-0 whitespace-pre-wrap break-all relative w-full">
                  {highlightedLines?.[i] ? (
                    <span dangerouslySetInnerHTML={{ __html: highlightedLines[i] }} />
                  ) : (
                    <span>{line}</span>
                  )}
                  {matches && matches.length > 0 && (
                    <HighlightOverlay
                      line={line}
                      matches={matches}
                      currentGlobalIndex={currentMatchIndex}
                    />
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// ── Main component ──

interface FileBrowserProps {
  projectId: string;
  sessionId?: string;
}

export function FileBrowser({ projectId, sessionId }: FileBrowserProps) {
  // Lazy tree state: childrenMap[""] is the root level; childrenMap[path] is a folder's children.
  const [childrenMap, setChildrenMap] = useState<Record<string, FileEntry[]>>({});
  const [loadingPaths, setLoadingPaths] = useState<Set<string>>(() => new Set());
  const [rootLoading, setRootLoading] = useState(true);
  const [fileContent, setFileContent] = useState<FileContentResponse | null>(null);
  // Committed (HEAD) content of the open file — the dirty-diff baseline for the
  // editor's gutter/scrollbar change markers. undefined while loading or when
  // the file isn't tracked, which disables the markers.
  const [fileBaseline, setFileBaseline] = useState<string | undefined>(undefined);
  // Git status for the whole tree, polled so changed files/folders get VSCode-
  // style markers. Null until first load or when the project isn't a git repo.
  const [gitStatus, setGitStatus] = useState<GitStatusResponse | null>(null);
  const [loadingFile, setLoadingFile] = useState(false);
  const [markdownMode, setMarkdownMode] = useState<"source" | "rendered">("source");

  // Edit/save state (only meaningful when enableFileEdit is true)
  // File editing is always available (the old enable_file_edit gate was removed).
  const editEnabled = true;
  const [editorValue, setEditorValue] = useState("");
  const [saving, setSaving] = useState(false);

  // Drag-drop state for tree-internal moves and OS file uploads
  const [highlightedPath, setHighlightedPath] = useState<string | null>(null);
  const [draggingPath, setDraggingPath] = useState<string | null>(null);
  const [dragOverPath, setDragOverPath] = useState<string | null>(null);
  const [rootDragOver, setRootDragOver] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [pendingOverwrite, setPendingOverwrite] = useState<{ file: File; directory: string } | null>(null);
  const [deleteConfirmOpen, setDeleteConfirmOpen] = useState(false);
  const uploadInputRef = useRef<HTMLInputElement>(null);

  // Persisted state from store — bucketed per-project so switching projects
  // (and coming back) preserves both tree expansion and selected file.
  const EMPTY_EXPANDED = useMemo(() => new Set<string>(), []);
  const expanded = useFileBrowserStore(
    (s) => s.projects[projectId]?.expanded ?? EMPTY_EXPANDED
  );
  const selectedPath = useFileBrowserStore(
    (s) => s.projects[projectId]?.selectedPath ?? null
  );
  const toggleExpandRaw = useFileBrowserStore((s) => s.toggleExpand);
  const setSelectedPathRaw = useFileBrowserStore((s) => s.setSelectedPath);
  const toggleExpand = useCallback(
    (path: string) => toggleExpandRaw(projectId, path),
    [toggleExpandRaw, projectId]
  );
  const setSelectedPath = useCallback(
    (path: string | null) => setSelectedPathRaw(projectId, path),
    [setSelectedPathRaw, projectId]
  );
  const { fileTreeWidth, setFileTreeWidth } = useLayoutStore();

  // Resize handle for the tree/content divider
  const containerRef = useRef<HTMLDivElement>(null);
  const isResizingTree = useRef(false);
  const handleTreeResizeMouseDown = useCallback((e: React.MouseEvent) => {
    isResizingTree.current = true;
    e.preventDefault();
    const onMove = (ev: MouseEvent) => {
      if (!isResizingTree.current || !containerRef.current) return;
      const rect = containerRef.current.getBoundingClientRect();
      setFileTreeWidth(
        Math.min(FILE_TREE_WIDTH_MAX, Math.max(FILE_TREE_WIDTH_MIN, ev.clientX - rect.left))
      );
    };
    const onUp = () => {
      isResizingTree.current = false;
      document.removeEventListener("mousemove", onMove);
      document.removeEventListener("mouseup", onUp);
    };
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  }, [setFileTreeWidth]);

  // Clear the in-component highlight when projectId changes; per-project state
  // from the store is loaded automatically via selectors above.
  useEffect(() => {
    setHighlightedPath(null);
  }, [projectId]);

  // Search state
  const [showSearch, setShowSearch] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [searchType, setSearchType] = useState<"name" | "content">("name");
  const [searchResults, setSearchResults] = useState<FileSearchResult[]>([]);
  const [searchLoading, setSearchLoading] = useState(false);
  const [searchCaseSensitive, setSearchCaseSensitive] = useState(false);
  const searchSeqRef = useRef(0);
  const searchRef = useRef<HTMLInputElement>(null);
  const searchTimerRef = useRef<ReturnType<typeof setTimeout>>(undefined);
  const [fileSearchVisible, setFileSearchVisible] = useState(false);
  const [fileSearchQuery, setFileSearchQuery] = useState("");
  const [fileSearchCaseSensitive, setFileSearchCaseSensitive] = useState(false);
  const [fileSearchCurrentIndex, setFileSearchCurrentIndex] = useState(0);
  const [fileSearchTotal, setFileSearchTotal] = useState(0);
  const [pendingTargetLine, setPendingTargetLine] = useState<number | null>(null);
  // Bumped on each content-result click so Monaco re-reveals even when the
  // query/line are unchanged (e.g. re-clicking the same result).
  const [searchNavSeq, setSearchNavSeq] = useState(0);
  const skipIndexResetRef = useRef(false);
  const fileContentSelectableRef = useRef<HTMLDivElement>(null);
  // Imperative handle to the Monaco editor (when file editing is enabled) so
  // Cmd+F can open Monaco's own find widget instead of the read-only overlay.
  const monacoEditorRef = useRef<MonacoFileEditorHandle>(null);

  // Track if a load is for the current projectId/sessionId; ignore stale results after switch.
  const loadGenRef = useRef(0);

  // Tree pane collapse — gives the content/editor pane the full width.
  const [treeCollapsed, setTreeCollapsed] = useState(false);

  const loadChildren = useCallback(async (path: string) => {
    setLoadingPaths((prev) => {
      if (prev.has(path)) return prev;
      const next = new Set(prev);
      next.add(path);
      return next;
    });
    const gen = loadGenRef.current;
    try {
      const data = await filesApi.list(projectId, path, sessionId);
      if (gen !== loadGenRef.current) return;
      setChildrenMap((prev) => ({ ...prev, [path]: data || [] }));
    } catch (e) {
      console.error(`Failed to load directory "${path}":`, e);
      if (gen === loadGenRef.current) {
        // Cache an empty result so we don't keep retrying on every render.
        setChildrenMap((prev) => ({ ...prev, [path]: [] }));
      }
    } finally {
      if (gen === loadGenRef.current) {
        setLoadingPaths((prev) => {
          if (!prev.has(path)) return prev;
          const next = new Set(prev);
          next.delete(path);
          return next;
        });
      }
    }
  }, [projectId, sessionId]);

  const reloadAll = useCallback(async () => {
    loadGenRef.current++;
    setChildrenMap({});
    setLoadingPaths(new Set());
    setRootLoading(true);
    const gen = loadGenRef.current;
    try {
      const data = await filesApi.list(projectId, "", sessionId);
      if (gen !== loadGenRef.current) return;
      setChildrenMap((prev) => ({ ...prev, "": data || [] }));
    } catch (e) {
      console.error("Failed to load file tree:", e);
      if (gen === loadGenRef.current) {
        setChildrenMap((prev) => ({ ...prev, "": [] }));
      }
    } finally {
      if (gen === loadGenRef.current) {
        setRootLoading(false);
      }
    }
  }, [projectId, sessionId]);

  useEffect(() => {
    reloadAll();
  }, [reloadAll]);

  // After the root loads, fetch children for any persisted-expanded folders so
  // the user's previous tree state restores without manual re-clicks.
  const restoredRef = useRef(false);
  useEffect(() => {
    if (rootLoading) {
      restoredRef.current = false;
      return;
    }
    if (restoredRef.current) return;
    restoredRef.current = true;
    expanded.forEach((path) => {
      if (path && childrenMap[path] === undefined) {
        loadChildren(path);
      }
    });
  }, [rootLoading, expanded, childrenMap, loadChildren]);

  // Restore the previously-selected file after remount or project switch.
  // selectedPath lives in the per-project store bucket, but local fileContent
  // /highlightedPath do not, so reload them once per root-load cycle.
  const restoredFileRef = useRef(false);
  useEffect(() => {
    if (rootLoading) {
      restoredFileRef.current = false;
      return;
    }
    if (restoredFileRef.current) return;
    restoredFileRef.current = true;
    if (selectedPath) {
      handleSelectFile(selectedPath);
    }
    // handleSelectFile intentionally excluded from deps — we only want this
    // restore to fire once per (re)mount/project-switch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rootLoading, selectedPath, projectId]);

  const handleToggleExpand = useCallback((path: string) => {
    const willExpand = !expanded.has(path);
    toggleExpand(path);
    if (willExpand && childrenMap[path] === undefined && !loadingPaths.has(path)) {
      loadChildren(path);
    }
  }, [expanded, toggleExpand, childrenMap, loadingPaths, loadChildren]);

  const getChildren = useCallback((path: string) => childrenMap[path], [childrenMap]);
  const isLoadingPath = useCallback((path: string) => loadingPaths.has(path), [loadingPaths]);
  const rootEntries = childrenMap[""] ?? [];

  const markdownPreviewAvailable = !!selectedPath && !!fileContent && !fileContent.binary && isMarkdownPath(selectedPath);
  useEffect(() => {
    if (!markdownPreviewAvailable && markdownMode !== "source") {
      setMarkdownMode("source");
    }
  }, [markdownPreviewAvailable, markdownMode]);

  useEffect(() => {
    setFileSearchVisible(false);
    setFileSearchQuery("");
    setFileSearchCaseSensitive(false);
    setFileSearchCurrentIndex(0);
    setFileSearchTotal(0);
    setPendingTargetLine(null);
  }, [selectedPath]);

  useEffect(() => {
    if (skipIndexResetRef.current) {
      skipIndexResetRef.current = false;
      return;
    }
    setFileSearchCurrentIndex(0);
  }, [fileSearchQuery, fileSearchCaseSensitive]);

  const isDirty = !!fileContent && !fileContent.binary && editorValue !== fileContent.content;

  // Poll git status (like the diff panel) so tree markers stay current with
  // changes made elsewhere (agent edits, terminal, the git panel's stage/commit).
  const refreshGitStatus = useCallback(() => {
    if (!projectId) return;
    gitApi.status(projectId, sessionId).then(setGitStatus).catch(() => setGitStatus(null));
  }, [projectId, sessionId]);

  useEffect(() => {
    if (!projectId) return;
    refreshGitStatus();
    const interval = setInterval(refreshGitStatus, 5_000);
    return () => clearInterval(interval);
  }, [projectId, refreshGitStatus]);

  const handleSave = useCallback(async () => {
    if (!selectedPath || !fileContent || fileContent.binary) return;
    setSaving(true);
    try {
      await filesApi.save(projectId, selectedPath, editorValue, sessionId);
      setFileContent({ ...fileContent, content: editorValue, size: new Blob([editorValue]).size });
      toast.success("Saved");
      // Reflect the new change in the tree markers without waiting for the poll.
      refreshGitStatus();
    } catch (e) {
      console.error("Save failed:", e);
      toast.error("Save failed", { description: String(e) });
    } finally {
      setSaving(false);
    }
  }, [projectId, selectedPath, sessionId, fileContent, editorValue, refreshGitStatus]);

  useEffect(() => {
    if (!selectedPath) return;

    const handleKeyDown = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable)) {
        return;
      }

      const useModifier = e.ctrlKey || e.metaKey;
      if (!useModifier) return;
      const key = e.key.toLowerCase();

      if (key === "f") {
        // When the Monaco editor is mounted, let it own find (its native widget
        // already handles Cmd+F while focused; this covers focus being elsewhere).
        if (monacoEditorRef.current) {
          e.preventDefault();
          monacoEditorRef.current.openFind();
          return;
        }
        // Editing enabled but Monaco not mounted (markdown preview / binary):
        // there is no in-file search here, so defer to the browser.
        if (editEnabled) return;
        e.preventDefault();
        setFileSearchVisible(true);
        return;
      }

      if (key === "s" && editEnabled && fileContent && !fileContent.binary && isDirty && !saving) {
        e.preventDefault();
        handleSave();
        return;
      }

      if (key === "a" && fileContent && !fileContent.binary) {
        const root = fileContentSelectableRef.current;
        if (!root) return;
        const selection = window.getSelection();
        if (!selection) return;

        e.preventDefault();
        const range = document.createRange();
        range.selectNodeContents(root);
        selection.removeAllRanges();
        selection.addRange(range);
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [selectedPath, fileContent, editEnabled, isDirty, saving, handleSave]);

  // Fetch the committed (HEAD) baseline for dirty-diff markers. Runs in the
  // background alongside the content load; a monotonic seq guards against a slow
  // response for a previously-selected file clobbering the current one.
  const baselineReqRef = useRef(0);
  const loadFileBaseline = useCallback(async (path: string) => {
    const seq = ++baselineReqRef.current;
    try {
      const res = await filesApi.head(projectId, path, sessionId);
      if (baselineReqRef.current !== seq) return;
      // in_repo=false → not a git repo, no markers. Otherwise content is the
      // HEAD blob, or "" for a new/untracked file (renders as fully added).
      setFileBaseline(res.in_repo ? res.content : undefined);
    } catch (e) {
      if (baselineReqRef.current !== seq) return;
      console.error("Failed to load file baseline:", e);
      setFileBaseline(undefined);
    }
  }, [projectId, sessionId]);

  // Build the path → marker map and the set of ancestor folders that contain a
  // change. A file can appear in several status categories at once (staged +
  // unstaged); the working-tree (unstaged) status wins since it reflects the
  // current on-disk state, with untracked as its own category.
  const gitDecorations = useMemo(() => {
    const files = new Map<string, GitChangeKind>();
    const dirs = new Map<string, "dir-tracked" | "dir-untracked">();
    if (!gitStatus) return { files, dirs };

    const statusToKind = (s: string): GitChangeKind =>
      s === "added" ? "added"
      : s === "deleted" ? "deleted"
      : s === "renamed" || s === "copied" ? "renamed"
      : "modified";

    // staged first, then unstaged overrides (working-tree state is what's shown).
    for (const e of gitStatus.staged) files.set(e.path, statusToKind(e.status));
    for (const e of gitStatus.unstaged) files.set(e.path, statusToKind(e.status));
    for (const e of gitStatus.untracked) files.set(e.path, "untracked");

    // Mark every ancestor folder. Tracked changes outrank untracked-only for the
    // folder dot color.
    for (const [path, kind] of files) {
      const tracked = kind !== "untracked";
      const parts = path.split("/");
      let prefix = "";
      for (let i = 0; i < parts.length - 1; i++) {
        prefix = prefix ? `${prefix}/${parts[i]}` : parts[i];
        if (tracked) dirs.set(prefix, "dir-tracked");
        else if (!dirs.has(prefix)) dirs.set(prefix, "dir-untracked");
      }
    }
    return { files, dirs };
  }, [gitStatus]);

  const getGitDecoration = useCallback(
    (path: string, isDir: boolean): GitDeco | undefined => {
      if (isDir) {
        const kind = gitDecorations.dirs.get(path);
        return kind ? { kind } : undefined;
      }
      const kind = gitDecorations.files.get(path);
      return kind ? { kind } : undefined;
    },
    [gitDecorations],
  );

  const handleSelectFile = useCallback(async (path: string) => {
    setSelectedPath(path);
    setHighlightedPath(path);
    setLoadingFile(true);
    setFileBaseline(undefined);
    loadFileBaseline(path);
    try {
      const data = await filesApi.content(projectId, path, sessionId);
      setFileContent(data);
      setEditorValue(data?.binary ? "" : (data?.content ?? ""));
    } catch (e) {
      console.error("Failed to load file:", e);
      setFileContent(null);
      setEditorValue("");
    } finally {
      setLoadingFile(false);
    }
  }, [projectId, sessionId, setSelectedPath, loadFileBaseline]);

  // Open a file when another panel (e.g. the diff viewer) requests it while the
  // Files panel is already mounted. The first-mount case is covered by the
  // restore effect above, which reads the seeded selectedPath from the store.
  useEffect(() => {
    const handler = (e: Event) => {
      const path = (e as CustomEvent<{ path?: string }>).detail?.path;
      if (path) handleSelectFile(path);
    };
    window.addEventListener("open-file", handler);
    return () => window.removeEventListener("open-file", handler);
  }, [handleSelectFile]);

  const handleSelectSearchResult = useCallback(async (path: string, line?: number) => {
    setSelectedPath(path);
    setHighlightedPath(path);
    setLoadingFile(true);
    setFileBaseline(undefined);
    loadFileBaseline(path);
    try {
      const data = await filesApi.content(projectId, path, sessionId);
      setFileContent(data);
      setEditorValue(data?.binary ? "" : (data?.content ?? ""));
    } catch (e) {
      console.error("Failed to load file:", e);
      setFileContent(null);
      setEditorValue("");
    } finally {
      setLoadingFile(false);
    }
    if (line != null && searchQuery.trim()) {
      if (editEnabled) {
        // Monaco reveals the line and opens its own find widget; the overlay
        // search bar is only for the read-only CodeViewer.
        setFileSearchQuery(searchQuery.trim());
        setFileSearchCaseSensitive(searchCaseSensitive);
        setPendingTargetLine(line);
        setSearchNavSeq((seq) => seq + 1);
      } else {
        skipIndexResetRef.current = true;
        setFileSearchVisible(true);
        setFileSearchQuery(searchQuery.trim());
        setFileSearchCaseSensitive(searchCaseSensitive);
        setPendingTargetLine(line);
      }
    }
  }, [projectId, sessionId, searchQuery, searchCaseSensitive, editEnabled, setSelectedPath, loadFileBaseline]);

  // Determine the directory where new uploads should land based on highlight.
  // - Highlighted directory → upload into it
  // - Highlighted file → upload into its parent directory
  // - Nothing highlighted → project root ("")
  const uploadTargetDir = useMemo(() => {
    if (!highlightedPath) return "";
    // Look up the entry: we know dirs from childrenMap keys + any cached entry list.
    const findEntry = (path: string): FileEntry | undefined => {
      if (!path) return undefined;
      for (const entries of Object.values(childrenMap)) {
        for (const e of entries) {
          if (e.path === path) return e;
        }
      }
      return undefined;
    };
    const entry = findEntry(highlightedPath);
    if (entry?.is_dir) return highlightedPath;
    return dirnameOf(highlightedPath);
  }, [highlightedPath, childrenMap]);

  // Reload one directory plus the root (cheap; keeps the tree consistent
  // without blowing away expanded folders the user opened).
  const refreshDir = useCallback(async (dir: string) => {
    // Snapshot the gen but don't increment — incrementing would invalidate any
    // sibling refresh started in the same tick (e.g. the move handler refreshing
    // both old and new parent dirs concurrently).
    const gen = loadGenRef.current;
    try {
      const data = await filesApi.list(projectId, dir, sessionId);
      if (gen !== loadGenRef.current) return;
      setChildrenMap((prev) => ({ ...prev, [dir]: data || [] }));
    } catch (e) {
      console.error(`Failed to reload directory "${dir}":`, e);
    }
  }, [projectId, sessionId]);

  const uploadSingleFile = useCallback(async (file: File, directory: string, overwrite: boolean) => {
    setUploading(true);
    try {
      const res = await filesApi.upload(projectId, directory, file, { overwrite, sessionId });
      if ("conflict" in res) {
        setPendingOverwrite({ file, directory });
        return;
      }
      await refreshDir(directory);
      toast.success(`Uploaded ${res.name}`);
    } catch (e) {
      console.error("Upload failed:", e);
      toast.error("Upload failed", { description: String(e) });
    } finally {
      setUploading(false);
    }
  }, [projectId, sessionId, refreshDir]);

  const handlePickUpload = useCallback(() => {
    uploadInputRef.current?.click();
  }, []);

  const handleUploadInputChange = useCallback((e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    uploadSingleFile(file, uploadTargetDir, false);
    e.target.value = "";
  }, [uploadTargetDir, uploadSingleFile]);

  const handleConfirmOverwrite = useCallback(async () => {
    if (!pendingOverwrite) return;
    const { file, directory } = pendingOverwrite;
    setPendingOverwrite(null);
    await uploadSingleFile(file, directory, true);
  }, [pendingOverwrite, uploadSingleFile]);

  const handleDownload = useCallback(() => {
    if (!selectedPath) return;
    const url = filesApi.downloadUrl(projectId, selectedPath, sessionId);
    const a = document.createElement("a");
    a.href = url;
    a.download = selectedPath.split("/").pop() || "download";
    document.body.appendChild(a);
    a.click();
    a.remove();
  }, [projectId, selectedPath, sessionId]);

  const handleOpenInNewTab = useCallback(() => {
    if (!selectedPath) return;
    window.open(filesApi.serveUrl(projectId, selectedPath, sessionId), "_blank", "noopener");
  }, [projectId, selectedPath, sessionId]);

  const handleOpenShareLink = useCallback(async () => {
    if (!selectedPath) return;
    try {
      const { url } = await filesApi.createWebShare(projectId, selectedPath);
      window.open(url, "_blank", "noopener");
    } catch (e) {
      console.error("Create share failed:", e);
      toast.error("Failed to create share link", { description: String(e) });
    }
  }, [projectId, selectedPath]);

  const handleCopyShareLink = useCallback(async () => {
    if (!selectedPath) return;
    try {
      const { url } = await filesApi.createWebShare(projectId, selectedPath);
      const absoluteUrl = new URL(url, window.location.origin).href;
      await navigator.clipboard.writeText(absoluteUrl);
      toast.success("Share link copied to clipboard");
    } catch (e) {
      console.error("Copy share failed:", e);
      toast.error("Failed to copy share link", { description: String(e) });
    }
  }, [projectId, selectedPath]);

  const handleDelete = useCallback(async () => {
    if (!selectedPath) return;
    const path = selectedPath;
    setDeleteConfirmOpen(false);
    try {
      await filesApi.delete(projectId, path, sessionId);
      const parent = dirnameOf(path);
      await refreshDir(parent);
      setSelectedPath(null);
      setHighlightedPath(null);
      setFileContent(null);
      setEditorValue("");
      toast.success(`Deleted ${path.split("/").pop()}`);
    } catch (e) {
      console.error("Delete failed:", e);
      toast.error("Delete failed", { description: String(e) });
    }
  }, [projectId, selectedPath, sessionId, refreshDir, setSelectedPath]);

  // Drag-and-drop handlers
  const handleDragStartItem = useCallback((entry: FileEntry, e: React.DragEvent) => {
    e.dataTransfer.setData(INTERNAL_DRAG_TYPE, entry.path);
    e.dataTransfer.effectAllowed = "move";
    setDraggingPath(entry.path);
  }, []);

  const handleDragEndItem = useCallback(() => {
    setDraggingPath(null);
    setDragOverPath(null);
    setRootDragOver(false);
  }, []);

  const isInternalDrag = (e: React.DragEvent): boolean => e.dataTransfer.types.includes(INTERNAL_DRAG_TYPE);
  const isFileDrag = (e: React.DragEvent): boolean => e.dataTransfer.types.includes("Files");

  const handleDragOverItem = useCallback((entry: FileEntry, e: React.DragEvent) => {
    const internal = isInternalDrag(e);
    const files = isFileDrag(e);
    if (!internal && !files) return;
    const targetDir = dropTargetDir(entry);
    // Disallow dropping a node onto itself or into its own subtree.
    if (internal && draggingPath) {
      if (targetDir === draggingPath || targetDir.startsWith(draggingPath + "/")) return;
    }
    e.preventDefault();
    e.stopPropagation();
    e.dataTransfer.dropEffect = internal ? "move" : "copy";
    if (targetDir === "") {
      // Resolves to the project root. Skip the no-op of re-rooting a top-level item.
      if (internal && draggingPath && dirnameOf(draggingPath) === "") return;
      setDragOverPath(null);
      setRootDragOver(true);
    } else {
      setDragOverPath(targetDir);
      setRootDragOver(false);
    }
  }, [draggingPath]);

  const performMove = useCallback(async (fromPath: string, toDir: string) => {
    const name = fromPath.split("/").pop() || fromPath;
    const toPath = joinPath(toDir, name);
    if (fromPath === toPath) return;
    try {
      await filesApi.move(projectId, fromPath, toPath, sessionId);
      const oldParent = dirnameOf(fromPath);

      // Optimistic local-state patch: remove from old parent immediately so the
      // stale entry doesn't linger if the upcoming server refresh races. The
      // refresh below still corrects ordering / metadata.
      setChildrenMap((prev) => {
        const next = { ...prev };
        const oldList = next[oldParent];
        if (oldList) {
          next[oldParent] = oldList.filter((e) => e.path !== fromPath);
        }
        const newList = next[toDir];
        if (newList && oldList) {
          const moved = oldList.find((e) => e.path === fromPath);
          if (moved && !newList.some((e) => e.path === toPath)) {
            next[toDir] = [...newList, { ...moved, path: toPath, name }];
          }
        }
        return next;
      });

      // Server-of-truth refresh (sequential to avoid any setState batching
      // surprises between the two parents). refreshDir uses a non-incrementing
      // gen snapshot so neither cancels the other, but sequential is also fine.
      await refreshDir(oldParent);
      if (oldParent !== toDir) {
        await refreshDir(toDir);
      }
      if (selectedPath === fromPath) {
        setSelectedPath(toPath);
        setHighlightedPath(toPath);
      }
      toast.success(`Moved ${name}`);
    } catch (e) {
      console.error("Move failed:", e);
      toast.error("Move failed", { description: String(e) });
    }
  }, [projectId, sessionId, refreshDir, selectedPath, setSelectedPath]);

  const handleDropOnItem = useCallback(async (entry: FileEntry, e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setDragOverPath(null);
    setRootDragOver(false);

    const targetDir = dropTargetDir(entry);
    const internalPath = e.dataTransfer.getData(INTERNAL_DRAG_TYPE);
    if (internalPath) {
      // Ignore drops onto the item itself or into its own subtree.
      if (targetDir === internalPath || targetDir.startsWith(internalPath + "/")) return;
      await performMove(internalPath, targetDir);
      return;
    }
    const file = e.dataTransfer.files?.[0];
    if (file) {
      uploadSingleFile(file, targetDir, false);
    }
  }, [performMove, uploadSingleFile]);

  const handleRootDragOver = useCallback((e: React.DragEvent) => {
    const internal = isInternalDrag(e);
    const files = isFileDrag(e);
    if (!internal && !files) return;
    // Fires only over empty space below the tree (item rows stop propagation),
    // so any folder hover-highlight is no longer current.
    setDragOverPath(null);
    // Only allow root-level drop when the dragged node isn't already a top-level entry.
    if (internal && draggingPath && dirnameOf(draggingPath) === "") {
      setRootDragOver(false);
      return;
    }
    e.preventDefault();
    e.dataTransfer.dropEffect = internal ? "move" : "copy";
    setRootDragOver(true);
  }, [draggingPath]);

  const handleRootDragLeave = useCallback((e: React.DragEvent) => {
    if (e.currentTarget === e.target) {
      setRootDragOver(false);
      setDragOverPath(null);
    }
  }, []);

  const handleRootDrop = useCallback(async (e: React.DragEvent) => {
    e.preventDefault();
    setRootDragOver(false);
    setDragOverPath(null);
    const internalPath = e.dataTransfer.getData(INTERNAL_DRAG_TYPE);
    if (internalPath) {
      if (dirnameOf(internalPath) === "") return;
      await performMove(internalPath, "");
      return;
    }
    const file = e.dataTransfer.files?.[0];
    if (file) {
      uploadSingleFile(file, "", false);
    }
  }, [performMove, uploadSingleFile]);

  // Debounced search
  useEffect(() => {
    if (!searchQuery.trim()) {
      setSearchResults([]);
      setSearchLoading(false);
      return;
    }
    setSearchLoading(true);
    clearTimeout(searchTimerRef.current);
    searchTimerRef.current = setTimeout(async () => {
      // Ignore responses from superseded requests (slow earlier query landing late).
      const seq = ++searchSeqRef.current;
      try {
        const results = await filesApi.search(projectId, searchQuery.trim(), searchType, sessionId, searchCaseSensitive);
        if (seq === searchSeqRef.current) setSearchResults(results || []);
      } catch (e) {
        console.error("Search failed:", e);
        if (seq === searchSeqRef.current) setSearchResults([]);
      } finally {
        if (seq === searchSeqRef.current) setSearchLoading(false);
      }
    }, 300);
    return () => clearTimeout(searchTimerRef.current);
  }, [searchQuery, searchType, searchCaseSensitive, projectId, sessionId]);

  const handleToggleSearch = useCallback(() => {
    setShowSearch((prev) => {
      if (!prev) setTimeout(() => searchRef.current?.focus(), 50);
      return !prev;
    });
    setSearchQuery("");
    setSearchResults([]);
  }, []);

  const handleFileSearchMatchesFound = useCallback((total: number) => {
    setFileSearchTotal(total);
  }, []);

  const handleFileSearchNext = useCallback(() => {
    setFileSearchCurrentIndex((index) => (fileSearchTotal === 0 ? 0 : (index + 1) % fileSearchTotal));
  }, [fileSearchTotal]);

  const handleFileSearchPrev = useCallback(() => {
    setFileSearchCurrentIndex((index) => (fileSearchTotal === 0 ? 0 : (index - 1 + fileSearchTotal) % fileSearchTotal));
  }, [fileSearchTotal]);

  return (
    <div ref={containerRef} className="flex h-full">
      {/* File tree sidebar */}
      {!treeCollapsed && (
      <div className="shrink-0 border-r flex flex-col" style={{ width: fileTreeWidth }}>
        <div className="flex items-center justify-between px-3 py-1.5 border-b shrink-0">
          <span className="text-xs text-muted-foreground font-medium">Files</span>
          <div className="flex items-center gap-0.5">
            <button
              onClick={() => window.dispatchEvent(new CustomEvent("open-quick-open"))}
              className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
              title="Go to file (Cmd/Ctrl+P)"
              aria-label="Go to file"
            >
              <ListFilter className="h-3.5 w-3.5" />
            </button>
            <button
              onClick={handleToggleSearch}
              className={cn(
                "p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors",
                showSearch && "bg-accent text-foreground"
              )}
              title="Search files"
            >
              <Search className="h-3.5 w-3.5" />
            </button>
            <button
              onClick={handlePickUpload}
              disabled={uploading}
              className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors disabled:opacity-50"
              title={uploadTargetDir ? `Upload into ${uploadTargetDir}` : "Upload to project root"}
            >
              {uploading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Upload className="h-3.5 w-3.5" />}
            </button>
            <input
              ref={uploadInputRef}
              type="file"
              className="hidden"
              onChange={handleUploadInputChange}
            />
            <button
              onClick={reloadAll}
              className="p-1 rounded hover:bg-accent text-muted-foreground/50 hover:text-muted-foreground transition-colors"
              title="Refresh"
            >
              <RefreshCw className="h-3 w-3" />
            </button>
          </div>
        </div>

        {/* Search bar */}
        {showSearch && (
          <div className="border-b px-2 py-1.5 space-y-1.5">
            <div className="flex items-center gap-1 bg-muted/50 rounded px-1.5">
              <Search className="h-3 w-3 text-muted-foreground/50 shrink-0" />
              <input
                ref={searchRef}
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                onKeyDown={(e) => { if (e.key === "Escape") handleToggleSearch(); }}
                placeholder={searchType === "name" ? "File name..." : "Search in files..."}
                className="flex-1 bg-transparent text-xs py-1 outline-none placeholder:text-muted-foreground/40"
              />
              {searchQuery && (
                <button onClick={() => setSearchQuery("")} className="text-muted-foreground/40 hover:text-muted-foreground">
                  <X className="h-3 w-3" />
                </button>
              )}
            </div>
            <div className="flex gap-1">
              <button
                onClick={() => setSearchType("name")}
                className={cn(
                  "text-[10px] px-1.5 py-0.5 rounded transition-colors",
                  searchType === "name"
                    ? "bg-primary/10 text-primary font-medium"
                    : "text-muted-foreground/60 hover:text-muted-foreground"
                )}
              >
                Name
              </button>
              <button
                onClick={() => setSearchType("content")}
                className={cn(
                  "text-[10px] px-1.5 py-0.5 rounded transition-colors",
                  searchType === "content"
                    ? "bg-primary/10 text-primary font-medium"
                    : "text-muted-foreground/60 hover:text-muted-foreground"
                )}
              >
                Content
              </button>
              <button
                onClick={() => setSearchCaseSensitive((v) => !v)}
                title="Match case"
                aria-pressed={searchCaseSensitive}
                className={cn(
                  "ml-auto text-[10px] px-1.5 py-0.5 rounded font-mono transition-colors",
                  searchCaseSensitive
                    ? "bg-primary/10 text-primary font-medium"
                    : "text-muted-foreground/60 hover:text-muted-foreground"
                )}
              >
                Aa
              </button>
            </div>
          </div>
        )}

        {/* Tree or search results */}
        <div
          className={cn(
            "flex-1 overflow-y-auto",
            rootDragOver && "ring-1 ring-inset ring-primary/60 bg-primary/5"
          )}
          onDragOver={handleRootDragOver}
          onDragLeave={handleRootDragLeave}
          onDrop={handleRootDrop}
        >
          {showSearch && searchQuery ? (
            <SearchResults
              results={searchResults}
              loading={searchLoading}
              searchType={searchType}
              query={searchQuery}
              onSelect={handleSelectSearchResult}
            />
          ) : rootLoading ? (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
            </div>
          ) : rootEntries.length === 0 ? (
            <div className="text-xs text-muted-foreground text-center py-8">
              No files found
            </div>
          ) : (
            rootEntries.map((entry) => (
              <FileTreeItem
                key={entry.path}
                entry={entry}
                depth={0}
                expanded={expanded}
                onToggle={handleToggleExpand}
                highlightedPath={highlightedPath}
                onSelect={handleSelectFile}
                onHighlight={setHighlightedPath}
                getChildren={getChildren}
                isLoading={isLoadingPath}
                getGitDecoration={getGitDecoration}
                dragOverPath={dragOverPath}
                onDragStartItem={handleDragStartItem}
                onDragOverItem={handleDragOverItem}
                onDropOnItem={handleDropOnItem}
                onDragEndItem={handleDragEndItem}
              />
            ))
          )}
        </div>
      </div>
      )}

      {/* Tree/content resize handle (only when the tree is visible) */}
      {!treeCollapsed && (
        <div
          className="w-1 cursor-col-resize hover:bg-primary/20 active:bg-primary/40 transition-colors shrink-0"
          onMouseDown={handleTreeResizeMouseDown}
        />
      )}

      {/* File content viewer */}
      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
        <div className="flex items-center gap-2 px-3 py-1.5 border-b shrink-0 text-xs">
          <button
            onClick={() => setTreeCollapsed((v) => !v)}
            className="p-1 -ml-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors shrink-0"
            title={treeCollapsed ? "Show file tree" : "Hide file tree"}
          >
            {treeCollapsed ? (
              <PanelLeftOpen className="h-3.5 w-3.5" />
            ) : (
              <PanelLeftClose className="h-3.5 w-3.5" />
            )}
          </button>
          {selectedPath ? (
            <>
              <File className="h-3.5 w-3.5 text-muted-foreground/50 shrink-0" />
              <span className="font-mono truncate text-muted-foreground">{selectedPath}</span>
              {markdownPreviewAvailable && (
                <div className="ml-auto flex items-center gap-1">
                  <button
                    onClick={() => setMarkdownMode("source")}
                    className={cn(
                      "h-6 px-2 rounded border text-[11px] inline-flex items-center gap-1 transition-colors",
                      markdownMode === "source"
                        ? "bg-accent text-accent-foreground border-border"
                        : "text-muted-foreground hover:text-foreground hover:bg-accent/50 border-transparent"
                    )}
                    title="View source markdown"
                  >
                    <Code2 className="h-3 w-3" />
                  </button>
                  <button
                    onClick={() => setMarkdownMode("rendered")}
                    className={cn(
                      "h-6 px-2 rounded border text-[11px] inline-flex items-center gap-1 transition-colors",
                      markdownMode === "rendered"
                        ? "bg-accent text-accent-foreground border-border"
                        : "text-muted-foreground hover:text-foreground hover:bg-accent/50 border-transparent"
                    )}
                    title="View rendered markdown"
                  >
                    <Eye className="h-3 w-3" />
                  </button>
                </div>
              )}
              {editEnabled && fileContent && !fileContent.binary && (
                <button
                  onClick={handleSave}
                  disabled={!isDirty || saving}
                  className={cn(
                    "h-6 px-2 rounded border text-[11px] inline-flex items-center gap-1 transition-colors shrink-0",
                    !markdownPreviewAvailable && "ml-auto",
                    isDirty
                      ? "bg-primary/10 text-primary border-primary/30 hover:bg-primary/20"
                      : "text-muted-foreground/50 border-transparent",
                    saving && "opacity-50"
                  )}
                  title={isDirty ? "Save (Ctrl+S)" : "No changes"}
                >
                  {saving ? <Loader2 className="h-3 w-3 animate-spin" /> : <Save className="h-3 w-3" />}
                </button>
              )}
              {fileContent && !fileContent.binary && (
                <span className={cn(
                  "text-muted-foreground/40 shrink-0",
                  !markdownPreviewAvailable && !(editEnabled && fileContent && !fileContent.binary) && "ml-auto",
                )}>
                  {formatSize(fileContent.size)}
                </span>
              )}
              {selectedPath && isHtmlPath(selectedPath) && (
                <div className="flex items-center shrink-0">
                  <button
                    onClick={handleOpenInNewTab}
                    className="p-1 rounded-l hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
                    title="Open rendered HTML in new tab (login required)"
                  >
                    <ExternalLink className="h-3.5 w-3.5" />
                  </button>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <button
                        className="px-0.5 py-1 rounded-r hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
                        title="More sharing options"
                      >
                        <ChevronDown className="h-3 w-3" />
                      </button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem onClick={handleOpenShareLink}>
                        Open share link
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={handleCopyShareLink}>
                        Copy share link
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
              )}
              {selectedPath && (
                <button
                  onClick={handleDownload}
                  className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors shrink-0"
                  title="Download"
                >
                  <Download className="h-3.5 w-3.5" />
                </button>
              )}
              {selectedPath && (
                <Popover open={deleteConfirmOpen} onOpenChange={setDeleteConfirmOpen}>
                  <PopoverTrigger asChild>
                    <button
                      className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-destructive transition-colors shrink-0"
                      title="Delete"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </button>
                  </PopoverTrigger>
                  <PopoverContent align="end" className="w-56 p-3">
                    <div className="text-xs text-foreground mb-1">Delete this file?</div>
                    <div className="text-[11px] text-muted-foreground font-mono truncate mb-2">{selectedPath}</div>
                    <div className="flex justify-end gap-1">
                      <button
                        onClick={() => setDeleteConfirmOpen(false)}
                        className="h-6 px-2 rounded text-[11px] hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
                      >
                        Cancel
                      </button>
                      <button
                        onClick={handleDelete}
                        className="h-6 px-2 rounded text-[11px] bg-destructive text-destructive-foreground hover:opacity-90 transition-opacity"
                      >
                        Delete
                      </button>
                    </div>
                  </PopoverContent>
                </Popover>
              )}
            </>
          ) : null}
        </div>

        {selectedPath ? (
          loadingFile ? (
              <div className="flex-1 flex items-center justify-center">
                <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
              </div>
            ) : fileContent?.binary ? (
              <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
                Binary file ({formatSize(fileContent.size)})
              </div>
            ) : fileContent ? (
              <div className="flex-1 min-h-0 relative">
                {fileSearchVisible && !editEnabled && (
                  <FileSearchBar
                    query={fileSearchQuery}
                    caseSensitive={fileSearchCaseSensitive}
                    currentIndex={fileSearchCurrentIndex}
                    total={fileSearchTotal}
                    onQueryChange={(query) => {
                      setFileSearchQuery(query);
                      setFileSearchCurrentIndex(0);
                    }}
                    onCaseSensitiveChange={(value) => {
                      setFileSearchCaseSensitive(value);
                      setFileSearchCurrentIndex(0);
                    }}
                    onPrev={handleFileSearchPrev}
                    onNext={handleFileSearchNext}
                    onClose={() => {
                      setFileSearchVisible(false);
                      setFileSearchQuery("");
                    }}
                  />
                )}
                {markdownPreviewAvailable && markdownMode === "rendered" ? (
                  <div ref={fileContentSelectableRef} className="h-full">
                    <MarkdownRenderer
                      content={fileContent.content}
                      className="h-full overflow-auto px-4 py-3 markdown-content text-sm"
                    />
                  </div>
                ) : editEnabled ? (
                  <div className="h-full">
                    <Suspense
                      fallback={
                        <div className="flex h-full items-center justify-center">
                          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
                        </div>
                      }
                    >
                      <MonacoFileEditor
                        ref={monacoEditorRef}
                        filepath={selectedPath}
                        value={editorValue}
                        onChange={setEditorValue}
                        onSave={handleSave}
                        baseline={fileBaseline}
                        searchQuery={fileSearchQuery}
                        searchCaseSensitive={fileSearchCaseSensitive}
                        targetLine={pendingTargetLine ?? undefined}
                        searchNonce={searchNavSeq}
                      />
                    </Suspense>
                  </div>
                ) : (
                  <div ref={fileContentSelectableRef} className="h-full">
                    <CodeViewer
                      content={fileContent.content}
                      filepath={selectedPath}
                      searchQuery={fileSearchQuery}
                      caseSensitive={fileSearchCaseSensitive}
                      currentMatchIndex={fileSearchCurrentIndex}
                      onMatchesFound={handleFileSearchMatchesFound}
                      scrollToLine={pendingTargetLine ?? undefined}
                      onScrollToLineReady={(idx) => {
                        if (idx >= 0) setFileSearchCurrentIndex(idx);
                        setPendingTargetLine(null);
                      }}
                    />
                  </div>
                )}
              </div>
            ) : (
              <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
                Failed to load file
              </div>
            )
        ) : (
          <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
            Select a file to view
          </div>
        )}
      </div>

      <Dialog
        open={pendingOverwrite !== null}
        onOpenChange={(open) => { if (!open) setPendingOverwrite(null); }}
      >
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>File already exists</DialogTitle>
            <DialogDescription>
              <span className="font-mono text-foreground">{pendingOverwrite?.file.name}</span>
              {" "}already exists in{" "}
              <span className="font-mono text-foreground">{pendingOverwrite?.directory || "/"}</span>.
              Do you want to overwrite it?
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <button
              onClick={() => setPendingOverwrite(null)}
              className="h-8 px-3 rounded text-xs hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
            >
              Cancel
            </button>
            <button
              onClick={handleConfirmOverwrite}
              className="h-8 px-3 rounded text-xs bg-primary text-primary-foreground hover:opacity-90 transition-opacity"
            >
              Overwrite
            </button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
