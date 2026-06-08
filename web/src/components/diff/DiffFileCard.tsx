import { useState, useMemo, useRef, useCallback, useEffect } from "react";
import { toast } from "sonner";
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
} from "lucide-react";
import { cn } from "@/lib/utils";
import { type ParsedDiffFile } from "@/lib/diff-parser";
import { diffApi } from "@/lib/api";
import { getLangFromPath, ensureLangRegistered, highlightCode } from "@/lib/highlight";
import { useAppStore, type InlineComment } from "@/stores/app";

export function getFilePath(f: ParsedDiffFile) {
  return f.isNewFile ? f.newPath : f.isDeletedFile ? f.oldPath : f.newPath || f.oldPath;
}

const LARGE_DIFF_THRESHOLD = 100;

export function DiffFileCard({
  file,
  isViewed,
  isActive,
  onToggleViewed,
  hideViewedToggle,
  lazyLoad,
  projectId,
  sessionId,
  defaultExpanded,
  borderless,
  fullFile,
  onChangeCount,
  refreshKey,
  staged,
}: {
  file: ParsedDiffFile;
  isViewed: boolean;
  isActive: boolean;
  onToggleViewed: () => void;
  hideViewedToggle?: boolean;
  lazyLoad?: boolean;
  projectId?: string;
  sessionId?: string;
  defaultExpanded?: boolean;
  borderless?: boolean;
  fullFile?: boolean;
  onChangeCount?: (count: number) => void;
  refreshKey?: number;
  staged?: boolean;
}) {
  const totalChanges = file.additions + file.deletions;
  const isLarge = totalChanges > LARGE_DIFF_THRESHOLD;
  const [expanded, setExpanded] = useState(defaultExpanded ?? !isLarge);
  const [lazyDiffText, setLazyDiffText] = useState<string | null>(null);
  const [loadingDiff, setLoadingDiff] = useState(false);

  const effectiveDiffText = lazyLoad ? lazyDiffText : file.diffText;

  // When refreshKey or staged changes, clear stale cached diff so it gets re-fetched
  useEffect(() => {
    setLazyDiffText(null);
  }, [refreshKey, staged]);

  useEffect(() => {
    if (!lazyLoad || !projectId || (isLarge && !defaultExpanded)) return;
    const filePath = getFilePath(file);
    setLoadingDiff(true);
    diffApi.fileDiff(projectId, filePath, fullFile, sessionId, staged).then((result) => {
      setLazyDiffText(result.diff);
    }).catch((e) => {
      console.error("Failed to auto-load file diff:", e);
    }).finally(() => {
      setLoadingDiff(false);
    });
  }, [lazyLoad, isLarge, projectId, refreshKey, staged]); // eslint-disable-line react-hooks/exhaustive-deps

  const handleExpand = async () => {
    if (expanded) {
      setExpanded(false);
      return;
    }
    setExpanded(true);
    if (lazyLoad && !lazyDiffText && projectId) {
      const filePath = getFilePath(file);
      setLoadingDiff(true);
      try {
        const result = await diffApi.fileDiff(projectId, filePath, fullFile, sessionId, staged);
        setLazyDiffText(result.diff);
      } catch (e) {
        console.error("Failed to load file diff:", e);
        toast.error("Failed to load diff", { description: String(e) });
        setLazyDiffText(null);
      } finally {
        setLoadingDiff(false);
      }
    }
  };

  const displayPath = getFilePath(file);

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
        "overflow-hidden",
        !borderless && "border rounded-lg",
        !borderless && isActive && "ring-1 ring-primary",
        isViewed && "opacity-60"
      )}
    >
      <div className={cn("flex items-center gap-2 px-3 py-2 bg-muted/50 text-xs", fullFile && "sticky top-0 z-[5]")}>
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
              onClick={onToggleViewed}
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
          <DiffLines diffText={effectiveDiffText} filename={displayPath} hideHeaders={fullFile}
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
}

// --- Diff line rendering ---

interface DiffLineData {
  type: "context" | "addition" | "deletion" | "header" | "meta";
  content: string;
  oldLineNum?: number;
  newLineNum?: number;
  changeIdx?: number;
}

function parseDiffLines(diffText: string): { lines: DiffLineData[]; changeCount: number } {
  const rawLines = diffText.split("\n");
  const result: DiffLineData[] = [];
  let oldLine = 0;
  let newLine = 0;
  let inHunk = false;
  let inChange = false;
  let changeIdx = 0;

  for (const line of rawLines) {
    if (line.startsWith("@@")) {
      const match = line.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
      if (match) {
        oldLine = parseInt(match[1]!, 10);
        newLine = parseInt(match[2]!, 10);
      }
      inHunk = true;
      inChange = false;
      result.push({ type: "header", content: line });
      continue;
    }

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
      continue;
    }

    if (!inHunk) continue;

    if (line.startsWith("+")) {
      const isStart = !inChange;
      inChange = true;
      result.push({ type: "addition", content: line.slice(1), newLineNum: newLine, changeIdx: isStart ? changeIdx++ : undefined });
      newLine++;
    } else if (line.startsWith("-")) {
      const isStart = !inChange;
      inChange = true;
      result.push({ type: "deletion", content: line.slice(1), oldLineNum: oldLine, changeIdx: isStart ? changeIdx++ : undefined });
      oldLine++;
    } else if (line.startsWith(" ") || line === "") {
      inChange = false;
      result.push({
        type: "context",
        content: line.startsWith(" ") ? line.slice(1) : line,
        oldLineNum: oldLine,
        newLineNum: newLine,
      });
      oldLine++;
      newLine++;
    }
  }

  return { lines: result, changeCount: changeIdx };
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

function DiffLines({ diffText, filename, hideHeaders, onChangeCount }: {
  diffText: string;
  filename?: string;
  hideHeaders?: boolean;
  onChangeCount?: (count: number) => void;
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
    ? (allComments[chatSessionId] || []).filter((c) => c.filePath === filename)
    : [];

  const [commentingLine, setCommentingLine] = useState<{ lineNum: number; side: "old" | "new" } | null>(null);

  // Reset comment input when file changes
  useEffect(() => {
    setCommentingLine(null);
  }, [filename]);

  const handleAddComment = useCallback((lineNum: number, side: "old" | "new", lineContent: string, text: string) => {
    if (!chatSessionId || !filename) return;
    addPickedComment(chatSessionId, {
      id: `${Date.now()}-${Math.random().toString(36).slice(2)}`,
      filePath: filename,
      lineNumber: lineNum,
      side,
      lineContent: lineContent.trim(),
      text,
    });
    setCommentingLine(null);
  }, [chatSessionId, filename, addPickedComment]);

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

  const highlightLine = useCallback(
    (content: string): string => {
      if (!langReady || !lang) return "";
      return highlightCode(content, lang);
    },
    [langReady, lang],
  );

  const canComment = !!chatSessionId && !!filename;

  return (
    <table className="w-full text-[15px] font-mono">
      <tbody>
        {parsed.lines
          .filter((l) => l.type !== "meta" && !(hideHeaders && l.type === "header"))
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
            const highlighted = langReady ? highlightLine(line.content) : "";

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
                  line.type === "addition" && "bg-green-500/15 dark:bg-green-500/10 hover:bg-green-500/25 dark:hover:bg-green-500/20",
                  line.type === "deletion" && "bg-red-500/15 dark:bg-red-500/10 hover:bg-red-500/25 dark:hover:bg-red-500/20",
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
                      {line.content}
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
}
