import { useState, useEffect, useRef, useMemo, useCallback } from "react";
import { toast } from "sonner";
import {
  Save, Clock, Eye, Pencil, GitCompare, ArrowLeft,
  MessageSquare, Plus, Send, Trash2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { MentionTextarea } from "@/plugins/task/components/MentionTextarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { assetsApi, commentsApi } from "@/lib/api";
import { assetTypeLabel, formatDate, formatRelativeDate, cn } from "@/lib/utils";
import { MarkdownRenderer } from "@/components/markdown/MarkdownRenderer";
import type { Asset, AssetVersion, DiffResult, DiffLine, ReviewComment } from "@/types";

interface AssetEditorProps {
  assetId: string;
  onClose: () => void;
  onSaved: () => void;
  initialMode?: "edit" | "preview" | "review" | "diff";
  initialLine?: number;
  initialCommentId?: string;
}

export function AssetEditor({ assetId, onClose, onSaved, initialMode, initialLine, initialCommentId }: AssetEditorProps) {
  const [asset, setAsset] = useState<Asset | null>(null);
  const [content, setContent] = useState("");
  const [originalContent, setOriginalContent] = useState("");
  const [versions, setVersions] = useState<AssetVersion[]>([]);
  const [saving, setSaving] = useState(false);
  const [mode, setMode] = useState<"edit" | "preview" | "review" | "diff">(initialMode ?? "preview");
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  // Title editing
  const [editingTitle, setEditingTitle] = useState(false);
  const [editTitle, setEditTitle] = useState("");

  // Diff state
  const [diffV1, setDiffV1] = useState<number>(0);
  const [diffV2, setDiffV2] = useState<number>(0);
  const [diff, setDiff] = useState<DiffResult | null>(null);
  const [diffLoading, setDiffLoading] = useState(false);

  // Review state
  const [comments, setComments] = useState<ReviewComment[]>([]);
  const [commentingLine, setCommentingLine] = useState<number | null>(null);
  const [newCommentText, setNewCommentText] = useState("");
  const [generalComment, setGeneralComment] = useState("");
  const [posting, setPosting] = useState(false);

  useEffect(() => {
    loadAsset();
    loadVersions();
    loadComments();
  }, [assetId]);

  useEffect(() => {
    if (mode === "diff" && diffV1 > 0 && diffV2 > 0 && diffV1 !== diffV2) {
      loadDiff();
    }
  }, [diffV1, diffV2, mode]);

  // Scroll to initial line when entering review mode
  useEffect(() => {
    if (mode !== "review" || !initialLine) return;
    const t = setTimeout(() => {
      document.getElementById(`review-line-${initialLine}`)?.scrollIntoView({ behavior: "smooth", block: "center" });
    }, 100);
    return () => clearTimeout(t);
  }, [mode, initialLine]);

  // Scroll to specific comment when entering review mode
  useEffect(() => {
    if (mode !== "review" || !initialCommentId || comments.length === 0) return;
    const t = setTimeout(() => {
      document.getElementById(`review-comment-${initialCommentId}`)?.scrollIntoView({ behavior: "smooth", block: "center" });
    }, 150);
    return () => clearTimeout(t);
  }, [mode, initialCommentId, comments.length]);

  const loadAsset = async () => {
    try {
      const data = await assetsApi.get(assetId);
      setAsset(data);
      setContent(data.content || "");
      setOriginalContent(data.content || "");
    } catch (e) {
      console.error("Failed to load asset:", e);
    }
  };

  const loadVersions = async () => {
    try {
      const data = await assetsApi.versions(assetId);
      setVersions(data);
      if (data.length >= 2) {
        setDiffV1(data[1].version);
        setDiffV2(data[0].version);
      }
    } catch (e) {
      console.error("Failed to load versions:", e);
    }
  };

  const loadComments = async () => {
    try {
      const data = await commentsApi.list(assetId);
      setComments(data || []);
    } catch (e) {
      console.error("Failed to load comments:", e);
    }
  };

  const loadDiff = async () => {
    setDiffLoading(true);
    try {
      const data = await assetsApi.diff(assetId, diffV1, diffV2);
      setDiff(data);
    } catch (e) {
      console.error("Failed to load diff:", e);
    } finally {
      setDiffLoading(false);
    }
  };

  // Only show comments for the current asset version
  const versionComments = useMemo(
    () => (asset ? comments.filter((c) => c.asset_version === asset.current_version) : []),
    [comments, asset],
  );

  const commentsByLine = useMemo(() => {
    const map: Record<number, ReviewComment[]> = {};
    for (const c of versionComments) {
      if (c.line_number != null) (map[c.line_number] ??= []).push(c);
    }
    return map;
  }, [versionComments]);

  const generalComments = useMemo(
    () => versionComments.filter((c) => c.line_number == null),
    [versionComments],
  );

  const doSave = async () => {
    setSaving(true);
    try {
      await assetsApi.update(assetId, { content });
      setOriginalContent(content);
      await loadAsset();
      await loadVersions();
      await loadComments();
      onSaved();
    } catch (e) {
      console.error("Failed to save:", e);
    } finally {
      setSaving(false);
    }
  };

  const handleSave = () => {
    if (content === originalContent) return;

    if (versionComments.length > 0) {
      toast.warning(
        `${versionComments.length} comment(s) on the current version will no longer be visible after saving.`,
        {
          duration: Infinity,
          action: { label: "Save anyway", onClick: doSave },
          cancel: { label: "Cancel", onClick: () => {} },
        },
      );
      return;
    }

    doSave();
  };

  const handleSaveTitle = async () => {
    if (!editTitle.trim()) return;
    setSaving(true);
    try {
      const updated = await assetsApi.update(assetId, { title: editTitle.trim() });
      setAsset(updated);
      setEditingTitle(false);
      onSaved();
    } catch (e) {
      console.error("Failed to save title:", e);
      toast.error("Failed to save title", { description: String(e) });
    } finally {
      setSaving(false);
    }
  };

  const handleAddComment = useCallback(async (lineNumber?: number) => {
    const text = lineNumber != null ? newCommentText : generalComment;
    if (!text.trim() || !asset) return;
    setPosting(true);
    try {
      await commentsApi.create(assetId, {
        asset_version: asset.current_version,
        line_number: lineNumber ?? undefined,
        content: text.trim(),
      });
      if (lineNumber != null) {
        setNewCommentText("");
        setCommentingLine(null);
      } else {
        setGeneralComment("");
      }
      await loadComments();
    } catch (e) {
      console.error("Failed to add comment:", e);
      toast.error("Failed to add comment", { description: String(e) });
    } finally {
      setPosting(false);
    }
  }, [assetId, asset, newCommentText, generalComment]);

  const handleDeleteComment = useCallback(async (commentId: string) => {
    try {
      await commentsApi.delete(commentId);
      await loadComments();
    } catch (e) {
      console.error("Failed to delete comment:", e);
      toast.error("Failed to delete comment", { description: String(e) });
    }
  }, [assetId]);

  const hasChanges = content !== originalContent;
  const canDiff = versions.length >= 2;
  const scrollRef = useRef<HTMLDivElement>(null);
  const [scrolled, setScrolled] = useState(false);

  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const handleScroll = () => setScrolled(el.scrollTop > 60);
    el.addEventListener("scroll", handleScroll, { passive: true });
    return () => el.removeEventListener("scroll", handleScroll);
  }, []);

  if (!asset) {
    return (
      <div className="flex items-center justify-center h-full text-muted-foreground">
        Loading...
      </div>
    );
  }

  const contentLines = content.split("\n");

  const modeButtons = (
    <div className="flex items-center gap-1.5 shrink-0">
      <Button variant={mode === "preview" ? "secondary" : "ghost"} size="sm" className="h-8 w-8 p-0" onClick={() => setMode("preview")} title="Preview">
        <Eye className="h-3.5 w-3.5" />
      </Button>
      <Button variant={mode === "review" ? "secondary" : "ghost"} size="sm" className="h-8 w-8 p-0" onClick={() => setMode("review")} title="Review">
        <MessageSquare className="h-3.5 w-3.5" />
      </Button>
      <Button variant={mode === "edit" ? "secondary" : "ghost"} size="sm" className="h-8 w-8 p-0" onClick={() => setMode("edit")} title="Edit">
        <Pencil className="h-3.5 w-3.5" />
      </Button>
      {canDiff && (
        <Button variant={mode === "diff" ? "secondary" : "ghost"} size="sm" className="h-8 w-8 p-0" onClick={() => setMode("diff")} title="Compare Versions">
          <GitCompare className="h-3.5 w-3.5" />
        </Button>
      )}
      {mode === "edit" && (
        <Button size="sm" className="h-8" onClick={handleSave} disabled={!hasChanges || saving}>
          <Save className="mr-1 h-3.5 w-3.5" />
          {saving ? "Saving..." : "Save"}
        </Button>
      )}
    </div>
  );

  return (
    <div className="h-full overflow-y-auto" ref={scrollRef}>
      {/* Sticky header */}
      <div className={cn(
        "sticky top-0 z-10 bg-background/95 backdrop-blur-sm transition-shadow",
        scrolled && "shadow-sm border-b",
      )}>
        <div className="max-w-3xl mx-auto px-6 flex items-center gap-3 h-11">
          <button onClick={onClose} className="text-muted-foreground hover:text-foreground shrink-0" title="Back">
            <ArrowLeft className="h-4 w-4" />
          </button>
          {scrolled && (
            <span className="text-sm font-medium truncate min-w-0">{asset.title}</span>
          )}
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <span>{assetTypeLabel(asset.type)}</span>
            <Badge variant="outline" className="text-xs">v{asset.current_version}</Badge>
            {versionComments.length > 0 && (
              <Badge variant="outline" className="text-xs flex items-center gap-1">
                <MessageSquare className="h-3 w-3" />
                {versionComments.length}
              </Badge>
            )}
          </div>
          <div className="ml-auto">{modeButtons}</div>
        </div>
      </div>

      <div className="max-w-3xl mx-auto px-6 pb-6">
      {/* Title */}
      <div className="mb-4 mt-2">
        {editingTitle ? (
          <div className="flex items-center gap-2">
            <Input
              value={editTitle}
              onChange={(e) => setEditTitle(e.target.value)}
              className="text-xl font-bold"
              autoFocus
              onKeyDown={(e) => e.key === "Enter" && !e.nativeEvent.isComposing && handleSaveTitle()}
            />
            <Button size="sm" onClick={handleSaveTitle} disabled={saving}>Save</Button>
            <Button size="sm" variant="ghost" onClick={() => { setEditingTitle(false); setEditTitle(asset.title); }}>Cancel</Button>
          </div>
        ) : (
          <h1
            className="text-2xl font-bold flex items-center gap-2 cursor-pointer hover:text-primary/80 group"
            onClick={() => { setEditingTitle(true); setEditTitle(asset.title); }}
          >
            {asset.title}
            <Pencil className="h-4 w-4 text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity" />
          </h1>
        )}
      </div>

      {/* Diff version selectors */}
      {mode === "diff" && (
        <div className="flex items-center gap-2 mb-4">
          <div className="flex-1">
            <label className="text-xs text-muted-foreground">From</label>
            <Select value={String(diffV1)} onValueChange={(val) => setDiffV1(Number(val))}>
              <SelectTrigger className="h-8 mt-1">
                <SelectValue placeholder="Select version" />
              </SelectTrigger>
              <SelectContent>
                {versions.map((v) => (
                  <SelectItem key={v.version} value={String(v.version)}>
                    v{v.version}
                    <span className="text-muted-foreground ml-2 text-xs">{formatDate(v.created_at)}</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="pt-4 text-muted-foreground">→</div>
          <div className="flex-1">
            <label className="text-xs text-muted-foreground">To</label>
            <Select value={String(diffV2)} onValueChange={(val) => setDiffV2(Number(val))}>
              <SelectTrigger className="h-8 mt-1">
                <SelectValue placeholder="Select version" />
              </SelectTrigger>
              <SelectContent>
                {versions.map((v) => (
                  <SelectItem key={v.version} value={String(v.version)}>
                    v{v.version}
                    <span className="text-muted-foreground ml-2 text-xs">{formatDate(v.created_at)}</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>
      )}

      {/* Content area */}
      <div className="mb-8">
        {mode === "edit" ? (
          <textarea
            ref={textareaRef}
            value={content}
            onChange={(e) => setContent(e.target.value)}
            className="w-full min-h-[200px] p-3 font-mono text-sm bg-transparent border rounded-md outline-none resize-y"
            placeholder="Write your content in Markdown..."
            spellCheck={false}
          />
        ) : mode === "preview" ? (
          <MarkdownRenderer
              content={content || "*No content yet*"}
              className="rounded-md border p-3 markdown-content"
            />
        ) : mode === "review" ? (
          <div className="border rounded-md overflow-hidden">
            <div className="font-mono text-sm">
              {contentLines.map((line, idx) => {
                const lineNum = idx + 1;
                const lineComments = commentsByLine[lineNum] || [];
                const isCommenting = commentingLine === lineNum;
                return (
                  <div key={idx} id={`review-line-${lineNum}`}>
                    {/* Line */}
                    <div
                      className={cn(
                        "flex group/line hover:bg-accent/50 cursor-pointer",
                        lineComments.length > 0 && "bg-yellow-500/5",
                        initialLine === lineNum && "bg-blue-500/10"
                      )}
                      onClick={() => {
                        setCommentingLine(isCommenting ? null : lineNum);
                        setNewCommentText("");
                      }}
                    >
                      <span className="relative w-10 text-right pr-2 py-0.5 text-muted-foreground/50 select-none border-r shrink-0 text-xs leading-5">
                        <button
                          className="absolute left-0 inset-y-0 w-5 flex items-center justify-center text-muted-foreground/30 hover:text-blue-500 opacity-0 group-hover/line:opacity-100 transition-opacity"
                          onClick={(e) => {
                            e.stopPropagation();
                            setCommentingLine(isCommenting ? null : lineNum);
                            setNewCommentText("");
                          }}
                          title="Add comment"
                        >
                          <Plus className="h-3 w-3" />
                        </button>
                        {lineNum}
                      </span>
                      <span className="flex-1 px-3 py-0.5 whitespace-pre-wrap text-xs leading-5">
                        {line || " "}
                      </span>
                      {lineComments.length > 0 && (
                        <span className="px-2 py-0.5 text-yellow-600 shrink-0">
                          <MessageSquare className="h-3 w-3" />
                        </span>
                      )}
                    </div>

                    {/* Existing comments for this line */}
                    {lineComments.map((c) => (
                      <div
                        key={c.id}
                        id={`review-comment-${c.id}`}
                        className={cn("ml-10 mr-3 my-1 border-l-2 rounded-r-md px-3 py-2",
                          initialCommentId === c.id
                            ? "border-blue-500/50 bg-blue-500/5"
                            : "border-yellow-500/50 bg-yellow-500/5"
                        )}
                      >
                        <div className="flex items-center gap-2 text-xs">
                          <span className="font-medium">{c.author || "user"}</span>
                          <span className="text-muted-foreground">
                            {formatRelativeDate(c.created_at)}
                          </span>
                          <button
                            onClick={() => handleDeleteComment(c.id)}
                            className="ml-auto text-muted-foreground hover:text-destructive transition-colors"
                            title="Delete comment"
                          >
                            <Trash2 className="h-3 w-3" />
                          </button>
                        </div>
                        <p className="text-xs mt-1 whitespace-pre-wrap">{c.content}</p>
                      </div>
                    ))}

                    {/* Inline comment input */}
                    {isCommenting && (
                      <div className="ml-10 mr-3 my-1 border-l-2 border-blue-500/50 bg-blue-500/5 rounded-r-md px-3 py-2">
                        <MentionTextarea
                          placeholder="Write a comment..."
                          value={newCommentText}
                          onChange={setNewCommentText}
                          className="text-xs min-h-[60px] bg-transparent border-muted w-full rounded-md border px-3 py-2 resize-none focus:outline-none focus:ring-1 focus:ring-ring"
                          minRows={2}
                          onKeyDown={(e) => {
                            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                              e.preventDefault();
                              handleAddComment(lineNum);
                            } else if (e.key === "Escape") {
                              setCommentingLine(null);
                              setNewCommentText("");
                            }
                          }}
                        />
                        <div className="flex items-center justify-between mt-1.5">
                          <span className="text-[10px] text-muted-foreground">
                            {navigator.platform.includes("Mac") ? "⌘" : "Ctrl"}+Enter to submit · Esc to cancel
                          </span>
                          <div className="flex gap-1.5">
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-6 text-xs"
                              onClick={() => { setCommentingLine(null); setNewCommentText(""); }}
                            >
                              Cancel
                            </Button>
                            <Button
                              size="sm"
                              className="h-6 text-xs"
                              onClick={() => handleAddComment(lineNum)}
                              disabled={!newCommentText.trim() || posting}
                            >
                              <Send className="h-3 w-3 mr-1" />
                              {posting ? "..." : "Send"}
                            </Button>
                          </div>
                        </div>
                      </div>
                    )}
                  </div>
                );
              })}
            </div>

            {/* General comments (no line number) */}
            {generalComments.length > 0 && (
              <div className="border-t px-4 py-3 space-y-2">
                <div className="text-xs font-medium text-muted-foreground">General comments</div>
                {generalComments.map((c) => (
                  <div
                    key={c.id}
                    id={`review-comment-${c.id}`}
                    className={cn("border-l-2 rounded-r-md px-3 py-2",
                      initialCommentId === c.id
                        ? "border-blue-500/50 bg-blue-500/5"
                        : "border-yellow-500/50 bg-yellow-500/5"
                    )}
                  >
                    <div className="flex items-center gap-2 text-xs">
                      <span className="font-medium">{c.author || "user"}</span>
                      <span className="text-muted-foreground">
                        {formatRelativeDate(c.created_at)}
                      </span>
                      <button
                        onClick={() => handleDeleteComment(c.id)}
                        className="ml-auto text-muted-foreground hover:text-destructive transition-colors"
                        title="Delete comment"
                      >
                        <Trash2 className="h-3 w-3" />
                      </button>
                    </div>
                    <p className="text-xs mt-1 whitespace-pre-wrap">{c.content}</p>
                  </div>
                ))}
              </div>
            )}

            {/* General comment input */}
            <div className="border-t px-4 py-3">
              <div className="flex gap-2">
                <MentionTextarea
                  placeholder="Write a general comment..."
                  value={generalComment}
                  onChange={setGeneralComment}
                  className="text-xs min-h-[50px] w-full rounded-md border px-3 py-2 resize-none focus:outline-none focus:ring-1 focus:ring-ring"
                  minRows={2}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                      e.preventDefault();
                      handleAddComment();
                    }
                  }}
                />
                <Button
                  size="icon"
                  className="h-[50px] w-10 shrink-0"
                  onClick={() => handleAddComment()}
                  disabled={!generalComment.trim() || posting}
                >
                  <Send className="h-4 w-4" />
                </Button>
              </div>
            </div>
          </div>
        ) : (
          <div>
            {diffLoading ? (
              <div className="flex items-center justify-center py-12 text-muted-foreground">
                Loading diff...
              </div>
            ) : diff ? (
              <div className="font-mono text-sm">
                <div className="flex items-center gap-2 mb-3 text-xs text-muted-foreground">
                  <Badge variant="outline" className="text-xs">v{diff.version1}</Badge>
                  <span>→</span>
                  <Badge variant="outline" className="text-xs">v{diff.version2}</Badge>
                </div>
                <div className="border rounded-md overflow-hidden">
                  <InlineDiffContent diffs={diff.diffs} />
                </div>
              </div>
            ) : (
              <div className="flex items-center justify-center py-12 text-muted-foreground text-sm">
                {diffV1 === diffV2 ? "Select two different versions to compare" : "Select versions to compare"}
              </div>
            )}
          </div>
        )}
      </div>

      {/* Version history (only in preview/edit mode, not in review/diff) */}
      {mode !== "diff" && mode !== "review" && versions.length > 1 && (
        <div className="border-t pt-4">
          <div className="text-xs font-medium text-muted-foreground mb-2 flex items-center gap-1">
            <Clock className="h-3 w-3" />
            Version History
          </div>
          <div className="max-h-24 overflow-y-auto space-y-1">
            {versions.map((v) => (
              <button
                key={v.id}
                onClick={() => setContent(v.content)}
                className="w-full text-left text-xs px-2 py-1 rounded hover:bg-accent transition-colors"
              >
                <span className="font-medium">v{v.version}</span>
                <span className="text-muted-foreground ml-2">
                  {formatDate(v.created_at)}
                </span>
              </button>
            ))}
          </div>
        </div>
      )}
      </div>

    </div>
  );
}

function InlineDiffContent({ diffs }: { diffs: DiffLine[] }) {
  return (
    <>
      {diffs.flatMap((d, i) =>
        d.content.split("\n").map((line, j, arr) => {
          if (j === arr.length - 1 && line === "") return null;
          return (
            <div
              key={`${i}-${j}`}
              className={cn(
                "px-3 py-0.5 text-xs leading-5 whitespace-pre-wrap border-b border-border/30",
                d.type === "insert" && "bg-green-50 dark:bg-green-950/50 text-green-800 dark:text-green-300",
                d.type === "delete" && "bg-red-50 dark:bg-red-950/50 text-red-800 dark:text-red-300",
                d.type === "equal" && "text-muted-foreground",
              )}
            >
              <span className="inline-block w-4 text-muted-foreground/50 select-none mr-2">
                {d.type === "insert" ? "+" : d.type === "delete" ? "-" : " "}
              </span>
              {line || " "}
            </div>
          );
        }),
      )}
    </>
  );
}
