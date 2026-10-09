import { useState, useMemo, useRef, useCallback, useEffect, memo, type ReactNode } from "react";
import {
  FileText,
  ChevronRight,
  ChevronDown,
  FilePlus,
  FileX,
  FileImage,
  Eye,
  EyeOff,
  Loader2,
  MessageSquare,
  Plus,
  X,
  ExternalLink,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { getFilePath, type ParsedDiffFile } from "@/lib/diff-parser";
import { diffApi, type GitRepository, type GitScope, type RepositoryFileRef } from "@/lib/api";
import { getLangFromPath, ensureLangRegistered, highlightCode } from "@/lib/highlight";
import { applyRangesToHtml } from "@/lib/inline-diff";
import { computeDiffOps } from "@/lib/vscode-diff";
import { useAppStore, type InlineComment } from "@/stores/app";

const LARGE_DIFF_THRESHOLD = 100;

// memo: the git panel lives inside SessionWorkspace, which re-renders for
// reasons that have nothing to do with the diff (session list refreshes, chat
// state). Re-rendering a card means re-running syntax highlighting over every
// line, so the memo boundary is load-bearing, not a micro-optimization.
export const DiffFileCard = memo(function DiffFileCard({
  file,
  isViewed,
  isActive,
  onToggleViewed,
  hideViewedToggle,
  lazyLoad,
  projectId,
  sessionId,
  repository,
  defaultExpanded,
  borderless,
  fullFile,
  onChangeCount,
  refreshKey,
  staged,
  workspacePath,
  allowLegacyComments,
}: {
  file: ParsedDiffFile;
  isViewed: boolean;
  isActive: boolean;
  /** Receives the file key so callers can pass one stable callback for every
   *  row instead of a fresh arrow per render (which would defeat the memo). */
  onToggleViewed?: (key: string) => void;
  hideViewedToggle?: boolean;
  lazyLoad?: boolean;
  projectId?: string;
  sessionId?: string;
  repository?: GitRepository;
  defaultExpanded?: boolean;
  borderless?: boolean;
  fullFile?: boolean;
  onChangeCount?: (count: number) => void;
  refreshKey?: number;
  staged?: boolean;
  workspacePath?: string;
  allowLegacyComments?: boolean;
}) {
  const totalChanges = file.additions + file.deletions;
  const isLarge = totalChanges > LARGE_DIFF_THRESHOLD;
  const displayPath = getFilePath(file);
  const [expanded, setExpanded] = useState(defaultExpanded ?? !isLarge);
  const [lazyDiff, setLazyDiff] = useState<{ key: string; text: string } | null>(null);
  const [loadingDiff, setLoadingDiff] = useState(false);
  const diffRequestGeneration = useRef(0);
  const repositoryId = repository?.id;
  const gitScope = useMemo<GitScope | null>(() => projectId && repositoryId ? ({
    projectId,
    sessionId,
    repositoryId,
  }) : null, [projectId, sessionId, repositoryId]);
  const lazyRequestKey = `${repositoryId ?? ""}:${displayPath}:${fullFile ? 1 : 0}:${staged ? 1 : 0}:${refreshKey ?? 0}`;
  const effectiveDiffText = lazyLoad
    ? (lazyDiff?.key === lazyRequestKey ? lazyDiff.text : null)
    : file.diffText;

  useEffect(() => {
    if (!lazyLoad || !gitScope || !expanded || file.isBinary) return;
    const generation = ++diffRequestGeneration.current;
    let cancelled = false;
    void Promise.resolve().then(async () => {
      if (cancelled || generation !== diffRequestGeneration.current) return;
      setLoadingDiff(true);
      try {
        const result = await diffApi.fileDiff(gitScope, displayPath, fullFile, staged);
        if (cancelled || generation !== diffRequestGeneration.current) return;
        setLazyDiff({ key: lazyRequestKey, text: result.diff });
      } catch (error) {
        if (cancelled || generation !== diffRequestGeneration.current) return;
        console.error("Failed to auto-load file diff:", error);
      } finally {
        if (!cancelled && generation === diffRequestGeneration.current) setLoadingDiff(false);
      }
    });
    return () => { cancelled = true; };
  }, [lazyLoad, gitScope, expanded, file.isBinary, displayPath, fullFile, staged, lazyRequestKey]);

  const handleExpand = () => {
    setExpanded((value) => !value);
  };

  const effectiveWorkspacePath = workspacePath ?? file.workspacePath;
  const fileRef: RepositoryFileRef | null = repository ? {
    repository_id: repository.id,
    repository_name: repository.name,
    repository_display_path: repository.display_path,
    path: displayPath,
    workspace_path: effectiveWorkspacePath,
  } : null;

  const FileIcon = file.isBinary
    ? FileImage
    : file.isNewFile
    ? FilePlus
    : file.isDeletedFile
    ? FileX
    : FileText;

  return (
    <div
      className={cn(
        // overflow-hidden establishes a clip/scroll context that breaks the
        // sticky header, so omit it in fullFile mode (the header must pin to
        // the outer scroll container).
        !fullFile && "overflow-hidden",
        !borderless && "border rounded-lg",
        !borderless && isActive && "ring-1 ring-primary",
        isViewed && "opacity-60"
      )}
    >
      <div className={cn("flex items-center gap-2 px-3 py-2 text-xs", fullFile ? "sticky top-0 z-[5] bg-muted" : "bg-muted/50")}>
        {fullFile ? (
          // Single-file view: the header is not a collapse toggle. The filename
          // is plain selectable text (so it can be copied), and a button jumps
          // to the Files panel and opens this file for viewing/editing.
          <div className="flex items-center gap-2 flex-1 min-w-0">
            <FileIcon
              className={cn(
                "h-3.5 w-3.5 shrink-0",
                file.isNewFile && "text-green-500",
                file.isDeletedFile && "text-red-500",
                !file.isNewFile && !file.isDeletedFile && "text-muted-foreground"
              )}
            />
            <span className="font-mono truncate min-w-0 select-text cursor-text" title={displayPath}>
              {displayPath}
            </span>
            <button
              onClick={() => window.dispatchEvent(new CustomEvent("open-file", {
                detail: fileRef ? { path: fileRef.workspace_path ?? displayPath, file: fileRef } : { path: displayPath },
              }))}
              className="shrink-0 p-1 rounded text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
              title="Open in Files panel"
            >
              <ExternalLink className="h-3.5 w-3.5" />
            </button>
          </div>
        ) : (
          <button
            onClick={handleExpand}
            className="flex items-center gap-2 flex-1 min-w-0 text-left hover:bg-muted rounded px-1 -mx-1 transition-colors"
          >
            {expanded ? (
              <ChevronDown className="h-3 w-3 text-muted-foreground shrink-0" />
            ) : (
              <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />
            )}
            <FileIcon
              className={cn(
                "h-3.5 w-3.5 shrink-0",
                file.isNewFile && "text-green-500",
                file.isDeletedFile && "text-red-500",
                !file.isNewFile && !file.isDeletedFile && "text-muted-foreground"
              )}
            />
            <span className="font-mono truncate min-w-0">{displayPath}</span>
          </button>
        )}
        <div className="flex items-center gap-2 shrink-0">
          {file.isNewFile && (
            <span className="text-[10px] uppercase font-medium text-green-600 dark:text-green-400">
              new
            </span>
          )}
          {file.isDeletedFile && (
            <span className="text-[10px] uppercase font-medium text-red-600 dark:text-red-400">
              deleted
            </span>
          )}
          {file.isBinary && (
            <span className="text-[10px] uppercase font-medium text-yellow-600 dark:text-yellow-400">
              binary
            </span>
          )}
          <span className="text-green-600 dark:text-green-400">+{file.additions}</span>
          <span className="text-red-600 dark:text-red-400">-{file.deletions}</span>
          {!hideViewedToggle && (
            <button
              onClick={() => onToggleViewed?.(file.key)}
              className={cn(
                "p-1 rounded hover:bg-muted transition-colors",
                isViewed ? "text-green-500" : "text-muted-foreground"
              )}
              title={isViewed ? "Mark as unviewed" : "Mark as viewed"}
            >
              {isViewed ? <Eye className="h-3 w-3" /> : <EyeOff className="h-3 w-3" />}
            </button>
          )}
        </div>
      </div>

      {!borderless && !expanded && (isLarge || lazyLoad) && (
        <button
          onClick={handleExpand}
          className="w-full px-4 py-2 text-xs text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors text-center"
        >
          {totalChanges} lines changed. Click to expand.
        </button>
      )}

      {expanded && loadingDiff && (
        <div className="flex items-center justify-center py-6">
          <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
          <span className="ml-2 text-xs text-muted-foreground">Loading diff...</span>
        </div>
      )}

      {expanded && !loadingDiff && !file.isBinary && effectiveDiffText && (
        <div className="overflow-x-auto">
          <DiffLines diffText={effectiveDiffText} filename={displayPath} hideHeaders={fullFile} repository={repository} workspacePath={effectiveWorkspacePath} allowLegacyComments={allowLegacyComments}
            onChangeCount={onChangeCount} />
        </div>
      )}
      {expanded && !loadingDiff && !file.isBinary && !effectiveDiffText && lazyLoad && (
        <div className="px-4 py-3 text-xs text-muted-foreground italic text-center">
          Failed to load diff content
        </div>
      )}
      {expanded && file.isBinary && (
        <div className="px-4 py-3 text-xs text-muted-foreground italic">
          Binary file changed
        </div>
      )}
    </div>
  );
});

// --- Diff line rendering ---

interface DiffLineData {
  type: "context" | "addition" | "deletion" | "header" | "meta";
  content: string;
  oldLineNum?: number;
  newLineNum?: number;
  changeIdx?: number;
  /** Word-level changed ranges, set on lines paired within a modify block. */
  intraRanges?: [number, number][];
}

// Re-align one hunk with VSCode's diff engine and append the resulting rows.
// git groups every touched line into one delete-block + add-block (so a
// re-indent looks like a full rewrite); re-diffing the reconstructed old/new
// content recovers unchanged lines as context and lights up only the genuinely
// changed words, VSCode-style. See web/src/lib/vscode-diff.ts.
function appendRealignedHunk(out: DiffLineData[], body: string[], oldStart: number, newStart: number) {
  const entries: { prefix: " " | "+" | "-"; content: string }[] = [];
  for (const l of body) {
    if (l.startsWith("\\")) continue; // "\ No newline at end of file"
    if (l.startsWith("+")) entries.push({ prefix: "+", content: l.slice(1) });
    else if (l.startsWith("-")) entries.push({ prefix: "-", content: l.slice(1) });
    else entries.push({ prefix: " ", content: l.startsWith(" ") ? l.slice(1) : l });
  }

  // Reconstruct the old/new line sequences this hunk represents.
  const oldLines: string[] = [];
  const newLines: string[] = [];
  for (const e of entries) {
    if (e.prefix === " ") {
      oldLines.push(e.content);
      newLines.push(e.content);
    } else if (e.prefix === "-") {
      oldLines.push(e.content);
    } else {
      newLines.push(e.content);
    }
  }

  const ops = computeDiffOps(oldLines, newLines, true /* ignoreTrimWhitespace */);
  for (const op of ops) {
    if (op.kind === "equal") {
      out.push({ type: "context", content: newLines[op.modIdx]!, oldLineNum: oldStart + op.origIdx, newLineNum: newStart + op.modIdx });
    } else if (op.kind === "delete") {
      out.push({ type: "deletion", content: oldLines[op.origIdx]!, oldLineNum: oldStart + op.origIdx, intraRanges: op.ranges.length ? op.ranges : undefined });
    } else {
      out.push({ type: "addition", content: newLines[op.modIdx]!, newLineNum: newStart + op.modIdx, intraRanges: op.ranges.length ? op.ranges : undefined });
    }
  }
}

function parseDiffLines(diffText: string): { lines: DiffLineData[]; changeCount: number } {
  const rawLines = diffText.split("\n");
  const result: DiffLineData[] = [];
  let i = 0;

  while (i < rawLines.length) {
    const line = rawLines[i]!;

    if (
      line.startsWith("diff --git") ||
      line.startsWith("index ") ||
      line.startsWith("--- ") ||
      line.startsWith("+++ ") ||
      line.startsWith("new file") ||
      line.startsWith("deleted file") ||
      line.startsWith("old mode") ||
      line.startsWith("new mode")
    ) {
      result.push({ type: "meta", content: line });
      i++;
      continue;
    }

    if (line.startsWith("@@")) {
      const match = line.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
      const oldStart = match ? parseInt(match[1]!, 10) : 0;
      const newStart = match ? parseInt(match[2]!, 10) : 0;
      result.push({ type: "header", content: line });
      i++;

      // Collect the hunk body up to the next hunk / file header.
      const body: string[] = [];
      while (i < rawLines.length) {
        const l = rawLines[i]!;
        if (l.startsWith("@@") || l.startsWith("diff --git")) break;
        if (
          l.startsWith("+") ||
          l.startsWith("-") ||
          l.startsWith(" ") ||
          l.startsWith("\\") ||
          l === ""
        ) {
          body.push(l);
          i++;
        } else {
          break;
        }
      }
      appendRealignedHunk(result, body, oldStart, newStart);
      continue;
    }

    i++;
  }

  // Assign a change index to the first row of each add/del run (used by the
  // minimap and change-to-change navigation). Intra-line ranges are computed
  // up front by computeDiffOps in appendRealignedHunk.
  let changeIdx = 0;
  let inChange = false;
  for (const ln of result) {
    if (ln.type === "addition" || ln.type === "deletion") {
      if (!inChange) {
        ln.changeIdx = changeIdx++;
        inChange = true;
      }
    } else {
      inChange = false;
    }
  }

  return { lines: result, changeCount: changeIdx };
}

// Renders plain (unhighlighted) line text with word-level changed ranges
// wrapped in <mark>. Used as the fallback before syntax highlighting loads.
function PlainContent({ content, ranges, markClass }: {
  content: string;
  ranges?: [number, number][];
  markClass: string;
}) {
  if (!ranges || ranges.length === 0) return <>{content}</>;
  const nodes: ReactNode[] = [];
  let pos = 0;
  ranges.forEach(([s, e], idx) => {
    if (s > pos) nodes.push(content.slice(pos, s));
    nodes.push(<mark key={idx} className={markClass}>{content.slice(s, e)}</mark>);
    pos = e;
  });
  if (pos < content.length) nodes.push(content.slice(pos));
  return <>{nodes}</>;
}

function CommentInput({ onAdd, onCancel, autoFocus }: {
  onAdd: (text: string) => void;
  onCancel: () => void;
  autoFocus?: boolean;
}) {
  const [text, setText] = useState("");
  const ref = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    if (autoFocus) {
      setTimeout(() => ref.current?.focus(), 0);
    }
  }, [autoFocus]);

  return (
    <div className="flex gap-2 items-start">
      <textarea
        ref={ref}
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder="Add a comment..."
        className="flex-1 text-[11px] bg-background border border-border rounded px-2 py-1.5 resize-none focus:outline-none focus:ring-1 focus:ring-blue-400 min-h-[52px]"
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault();
            const trimmed = text.trim();
            if (trimmed) onAdd(trimmed);
          }
          if (e.key === "Escape") {
            onCancel();
          }
        }}
      />
      <div className="flex flex-col gap-1 shrink-0">
        <button
          onClick={() => { const trimmed = text.trim(); if (trimmed) onAdd(trimmed); }}
          disabled={!text.trim()}
          className="px-2 py-1 text-[11px] bg-blue-500 text-white rounded hover:bg-blue-600 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
        >
          Add
        </button>
        <button
          onClick={onCancel}
          className="px-2 py-1 text-[11px] text-muted-foreground hover:text-foreground transition-colors"
        >
          Cancel
        </button>
      </div>
    </div>
  );
}

const DiffLines = memo(function DiffLines({ diffText, filename, hideHeaders, onChangeCount, repository, workspacePath, allowLegacyComments }: {
  diffText: string;
  filename?: string;
  hideHeaders?: boolean;
  onChangeCount?: (count: number) => void;
  repository?: GitRepository;
  workspacePath?: string;
  allowLegacyComments?: boolean;
}) {
  const parsed = useMemo(() => parseDiffLines(diffText), [diffText]);
  const [langReady, setLangReady] = useState(false);
  const lang = filename ? getLangFromPath(filename) : undefined;

  // Inline comments state
  const chatSessionId = useAppStore((s) => s.currentSession?.id);
  const allComments = useAppStore((s) => s.pickedComments);
  const addPickedComment = useAppStore((s) => s.addPickedComment);
  const removePickedComment = useAppStore((s) => s.removePickedComment);
  const fileComments: InlineComment[] = chatSessionId
    ? (allComments[chatSessionId] || []).filter((c) => c.filePath === filename && (c.repositoryId === repository?.id || (!c.repositoryId && allowLegacyComments)))
    : [];

  const [commentingLine, setCommentingLine] = useState<{ lineNum: number; side: "old" | "new" } | null>(null);

  const handleAddComment = useCallback((lineNum: number, side: "old" | "new", lineContent: string, text: string) => {
    if (!chatSessionId || !filename) return;
    addPickedComment(chatSessionId, {
      id: `${Date.now()}-${Math.random().toString(36).slice(2)}`,
      repositoryId: repository?.id,
      repositoryDisplayPath: repository?.display_path,
      filePath: filename,
      workspacePath,
      lineNumber: lineNum,
      side,
      lineContent: lineContent.trim(),
      text,
    });
    setCommentingLine(null);
  }, [chatSessionId, filename, repository, workspacePath, addPickedComment]);

  useEffect(() => {
    onChangeCount?.(parsed.changeCount);
  }, [parsed.changeCount]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!lang) return;
    let cancelled = false;
    ensureLangRegistered(lang).then((ok) => {
      if (!cancelled) setLangReady(ok);
    });
    return () => { cancelled = true; };
  }, [lang]);

  const visibleLines = useMemo(
    () => parsed.lines.filter((l) => l.type !== "meta" && !(hideHeaders && l.type === "header")),
    [parsed.lines, hideHeaders],
  );

  // hljs.highlight() is by far the most expensive thing this panel does, and it
  // runs once per diff line. Keep it out of the render path entirely: it only
  // depends on the diff text and the resolved language, so an unrelated parent
  // re-render must not pay for it. Word-level <mark> ranges are baked in here
  // too, since they are derived from the same inputs.
  const highlightedLines = useMemo(() => {
    if (!langReady || !lang) return null;
    return visibleLines.map((line) => {
      if (line.type === "header") return "";
      const html = highlightCode(line.content, lang);
      if (!html || !line.intraRanges?.length) return html;
      return applyRangesToHtml(
        html,
        line.intraRanges,
        line.type === "deletion" ? "diff-word-del" : "diff-word-add",
      );
    });
  }, [visibleLines, lang, langReady]);

  const canComment = !!chatSessionId && !!filename;

  return (
    <table className="w-full text-[15px] font-mono">
      <tbody>
        {visibleLines
          .map((line, i) => {
            if (line.type === "header") {
              return (
                <tr key={i} className="bg-blue-500/8 dark:bg-blue-500/10">
                  <td colSpan={3} className="px-3 py-1 text-blue-700 dark:text-blue-300 text-[11px] border-y border-border/20">
                    {line.content}
                  </td>
                </tr>
              );
            }

            const prefix =
              line.type === "addition" ? "+" : line.type === "deletion" ? "-" : " ";
            // Paired modify lines get word-level marks; pure add/del lines stay
            // fully colored. Both are precomputed in highlightedLines above.
            const hasIntra = !!line.intraRanges && line.intraRanges.length > 0;
            const markClass = line.type === "deletion" ? "diff-word-del" : "diff-word-add";
            const highlighted = highlightedLines?.[i] ?? "";

            const gutterCls =
              line.type === "addition"
                ? "bg-green-500/20 text-green-600 dark:text-green-400"
                : line.type === "deletion"
                ? "bg-red-500/20 text-red-600 dark:text-red-400"
                : "text-muted-foreground/40";

            const prefixCls =
              line.type === "addition"
                ? "text-green-600 dark:text-green-400"
                : line.type === "deletion"
                ? "text-red-600 dark:text-red-400"
                : "text-muted-foreground/30";

            // Determine anchor line number and side for comments
            const lineSide: "old" | "new" = line.type === "deletion" ? "old" : "new";
            const lineNum = lineSide === "old" ? line.oldLineNum : line.newLineNum;
            const lineComments = lineNum != null
              ? fileComments.filter((c) => c.lineNumber === lineNum && c.side === lineSide)
              : [];
            const hasComments = lineComments.length > 0;
            const isCommentingHere = commentingLine?.lineNum === lineNum && commentingLine?.side === lineSide;

            // For deletions, the comment anchor is the old-line column; for all others, the new-line column
            const commentOnOld = lineSide === "old";

            const rows = [
              <tr
                key={i}
                className={cn(
                  "group/row",
                  // Modified lines use a faint tint so the word-level marks
                  // stand out; pure insertions/removals get a solid, clearly
                  // visible fill (VSCode-style).
                  line.type === "addition" &&
                    (hasIntra
                      ? "bg-green-500/10 dark:bg-green-500/[0.08] hover:bg-green-500/[0.18] dark:hover:bg-green-500/15"
                      : "bg-green-500/25 dark:bg-green-500/20 hover:bg-green-500/30 dark:hover:bg-green-500/25"),
                  line.type === "deletion" &&
                    (hasIntra
                      ? "bg-red-500/10 dark:bg-red-500/[0.08] hover:bg-red-500/[0.18] dark:hover:bg-red-500/15"
                      : "bg-red-500/25 dark:bg-red-500/20 hover:bg-red-500/30 dark:hover:bg-red-500/25"),
                  line.type === "context" && "hover:bg-muted/40"
                )}
                data-line-type={line.type}
                {...(line.changeIdx != null ? { "data-change-idx": line.changeIdx } : {})}
              >
                {/* Old line number — comment anchor for deletion lines */}
                <td className={cn("relative px-0 py-0 text-right select-none w-[40px] align-top border-r border-border/20 text-[11px]", gutterCls)}>
                  {line.type !== "addition" && (
                    <span className={cn(
                      "px-2 block leading-5",
                      canComment && commentOnOld && lineNum != null && "group-hover/row:invisible"
                    )}>
                      {line.oldLineNum ?? ""}
                      {canComment && commentOnOld && hasComments && lineNum != null && (
                        <span className="inline-block w-1.5 h-1.5 rounded-full bg-blue-400 ml-0.5 align-middle group-hover/row:hidden" />
                      )}
                    </span>
                  )}
                  {canComment && commentOnOld && lineNum != null && (
                    <button
                      className="absolute inset-0 flex items-center justify-center opacity-0 group-hover/row:opacity-100 transition-opacity"
                      title="Add comment"
                      onClick={() => {
                        setCommentingLine(isCommentingHere ? null : { lineNum, side: lineSide });
                      }}
                    >
                      <span className="h-[18px] w-[18px] rounded-full bg-blue-500 inline-flex items-center justify-center text-white shadow-sm">
                        <Plus className="h-3 w-3" strokeWidth={2.5} />
                      </span>
                    </button>
                  )}
                </td>
                {/* New line number — comment anchor for addition and context lines */}
                <td className={cn("relative px-0 py-0 text-right select-none w-[40px] align-top border-r border-border/20 text-[11px]", gutterCls)}>
                  {line.type !== "deletion" && (
                    <span className={cn(
                      "px-2 block leading-5",
                      canComment && !commentOnOld && lineNum != null && "group-hover/row:invisible"
                    )}>
                      {line.newLineNum ?? ""}
                      {canComment && !commentOnOld && hasComments && lineNum != null && (
                        <span className="inline-block w-1.5 h-1.5 rounded-full bg-blue-400 ml-0.5 align-middle group-hover/row:hidden" />
                      )}
                    </span>
                  )}
                  {canComment && !commentOnOld && lineNum != null && (
                    <button
                      className="absolute inset-0 flex items-center justify-center opacity-0 group-hover/row:opacity-100 transition-opacity"
                      title="Add comment"
                      onClick={() => {
                        setCommentingLine(isCommentingHere ? null : { lineNum, side: lineSide });
                      }}
                    >
                      <span className="h-[18px] w-[18px] rounded-full bg-blue-500 inline-flex items-center justify-center text-white shadow-sm">
                        <Plus className="h-3 w-3" strokeWidth={2.5} />
                      </span>
                    </button>
                  )}
                </td>
                <td className="px-0 py-0 whitespace-pre-wrap break-all">
                  <span className={cn("inline-block w-5 text-center select-none", prefixCls)}>{prefix}</span>
                  {highlighted ? (
                    <span dangerouslySetInnerHTML={{ __html: highlighted }} />
                  ) : (
                    <span
                      className={cn(
                        line.type === "addition" && "text-green-800 dark:text-green-200",
                        line.type === "deletion" && "text-red-800 dark:text-red-200"
                      )}
                    >
                      <PlainContent content={line.content} ranges={line.intraRanges} markClass={markClass} />
                    </span>
                  )}
                </td>
              </tr>,
            ];

            // Existing comments row
            if (hasComments) {
              rows.push(
                <tr key={`comments-${i}`}>
                  <td colSpan={3} className="bg-blue-500/5 border-b border-blue-500/10 px-0 py-0">
                    {lineComments.map((c) => (
                      <div key={c.id} className="flex items-start gap-2 px-3 py-1.5 border-t border-blue-500/10 first:border-t-0">
                        <MessageSquare className="h-3 w-3 text-blue-400 shrink-0 mt-0.5" />
                        <span className="flex-1 text-[11px] text-foreground/80 whitespace-pre-wrap">{c.text}</span>
                        {chatSessionId && (
                          <button
                            onClick={() => removePickedComment(chatSessionId, c.id)}
                            className="shrink-0 text-muted-foreground/50 hover:text-destructive transition-colors"
                          >
                            <X className="h-3 w-3" />
                          </button>
                        )}
                      </div>
                    ))}
                  </td>
                </tr>
              );
            }

            // Inline comment input row
            if (isCommentingHere && lineNum != null) {
              rows.push(
                <tr key={`input-${i}`}>
                  <td colSpan={3} className="px-3 py-2 bg-blue-500/5 border-b border-blue-500/15">
                    <CommentInput
                      autoFocus
                      onAdd={(text) => handleAddComment(lineNum, lineSide, line.content, text)}
                      onCancel={() => setCommentingLine(null)}
                    />
                  </td>
                </tr>
              );
            }

            return rows;
          })}
      </tbody>
    </table>
  );
});
