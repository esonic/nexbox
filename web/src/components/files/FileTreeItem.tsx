import { ChevronRight, ChevronDown, File, Folder, FolderOpen, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import type { FileEntry } from "@/lib/api";
import { GIT_DECO_COLOR, GIT_DECO_LETTER, type GitDeco } from "./fileBrowserShared";

// ── File tree item ──

export function FileTreeItem({
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
  // A folder whose chain of single-child subfolders the server compacted arrives
  // as "com/okcoin/sre" with `path` pointing at the innermost folder; render the
  // segments with dimmed separators the way VSCode's compact folders do.
  const segments = entry.is_dir ? entry.name.split("/") : [entry.name];
  const gitColor = gitDeco ? GIT_DECO_COLOR[gitDeco.kind] : undefined;
  const gitLetter = gitDeco ? GIT_DECO_LETTER[gitDeco.kind] : undefined;

  return (
    <>
      <div
        draggable
        data-tree-path={entry.path}
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
        <span className={cn("truncate min-w-0", entry.is_symlink && "italic", gitColor)}>
          {segments.map((segment, i) => (
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
