import { ChevronRight, ChevronDown, FileCode, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import type { FileSearchResult } from "@/lib/api";

// ── Panel content-search results ──

// Render a single matched line: window it around the first match and wrap every
// occurrence of the query in a <mark>. `caseSensitive` mirrors the search mode.
function ContentSnippet({ content, query, caseSensitive }: { content: string; query: string; caseSensitive?: boolean }) {
  if (!query || !content) return <>{content}</>;
  const hay = caseSensitive ? content : content.toLowerCase();
  const ndl = caseSensitive ? query : query.toLowerCase();
  const idxs: number[] = [];
  for (let from = 0; ; ) {
    const i = hay.indexOf(ndl, from);
    if (i === -1) break;
    idxs.push(i);
    from = i + ndl.length;
  }
  if (idxs.length === 0) return <>{content}</>;

  // Center the window on the first match so it's never clipped off-screen, and
  // keep the visible span bounded on very long lines.
  const prePad = 24;
  const maxWidth = 240;
  const first = idxs[0];
  const start = Math.max(0, first - prePad);
  const end = Math.min(content.length, start + maxWidth);

  const parts: React.ReactNode[] = [];
  let cursor = start;
  let key = 0;
  for (const i of idxs) {
    const mStart = i;
    const mEnd = i + ndl.length;
    if (mEnd <= start || mStart >= end) continue; // outside the visible window
    const s = Math.max(mStart, start);
    const e = Math.min(mEnd, end);
    if (cursor < s) parts.push(content.slice(cursor, s));
    parts.push(
      <mark key={key++} className="bg-yellow-300/50 dark:bg-yellow-600/50 text-foreground rounded-[2px] not-italic">
        {content.slice(s, e)}
      </mark>,
    );
    cursor = e;
  }
  if (cursor < end) parts.push(content.slice(cursor, end));
  return (
    <>
      {start > 0 && "…"}
      {parts}
      {end < content.length && "…"}
    </>
  );
}

// Content-only search results, grouped by file (VSCode-style). Filename lookup
// lives in Quick Open (Cmd/Ctrl+P) instead of a mode toggle here.
export function SearchResults({
  grouped,
  loading,
  query,
  caseSensitive,
  collapsed,
  onToggleGroup,
  activeIndex,
  activeRef,
  onSelect,
}: {
  grouped: [string, FileSearchResult[]][];
  loading: boolean;
  query: string;
  caseSensitive: boolean;
  collapsed: Set<string>;
  onToggleGroup: (path: string) => void;
  activeIndex: number;
  activeRef: React.RefObject<HTMLDivElement | null>;
  onSelect: (path: string, line?: number) => void;
}) {
  if (loading) {
    return (
      <div className="flex items-center justify-center py-8">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      </div>
    );
  }
  if (grouped.length === 0) {
    return (
      <div className="text-xs text-muted-foreground text-center py-6">
        No results
      </div>
    );
  }

  let navIdx = -1;
  return (
    <div className="text-xs">
      {grouped.map(([path, matches]) => {
        const isCollapsed = collapsed.has(path);
        return (
          <div key={path}>
            {/* Clicking anywhere on the file row toggles the group (VSCode-style);
                individual match lines below open the file at that line. */}
            <div
              className="flex items-center gap-1 px-1.5 py-1.5 bg-muted/30 hover:bg-accent/50 transition-colors cursor-pointer select-none"
              onClick={() => onToggleGroup(path)}
              title={path}
            >
              {isCollapsed ? (
                <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground/60" />
              ) : (
                <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground/60" />
              )}
              <FileCode className="h-3 w-3 text-muted-foreground/60 shrink-0" />
              <span className="font-medium text-foreground/80 truncate">{path}</span>
              <span className="ml-auto text-muted-foreground/40 shrink-0 tabular-nums">{matches.length}</span>
            </div>
            {!isCollapsed && matches.map((m, i) => {
              navIdx++;
              const isActive = navIdx === activeIndex;
              return (
                <div
                  key={i}
                  ref={isActive ? activeRef : undefined}
                  className={cn(
                    "px-3 py-1 text-[11px] cursor-pointer font-mono overflow-hidden whitespace-nowrap",
                    isActive ? "bg-primary/15 text-foreground" : "text-muted-foreground hover:bg-accent/30",
                  )}
                  onClick={() => onSelect(m.path, m.line)}
                >
                  <span className="text-muted-foreground/40 mr-2 tabular-nums">{m.line}</span>
                  <ContentSnippet content={m.content ?? ""} query={query} caseSensitive={caseSensitive} />
                </div>
              );
            })}
          </div>
        );
      })}
    </div>
  );
}
