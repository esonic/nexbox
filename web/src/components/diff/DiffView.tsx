import { useState, useMemo, useRef, useCallback, useEffect } from "react";
import { toast } from "sonner";
import {
  Filter,
  Trash2,
  Sparkles,
  Loader2,
  GitCommitHorizontal,
  Plus,
  Minus,
  Check,
  LayoutList,
  FileCode2,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { getFilePath, splitUnifiedDiffByFile, type ParsedDiffFile } from "@/lib/diff-parser";
import {
  gitApi,
  type DiffFileInfo,
  type GitScope,
  type GitRepository,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { DiffFileCard } from "./DiffFileCard";
import { DiffMinimap } from "./DiffMinimap";
import { HistoryTab } from "./HistoryTab";
import { BranchToolbar } from "./BranchToolbar";
import { useAppStore } from "@/stores/app";
import type { GitReviewSnapshot } from "@/hooks/useGitReview";

interface DiffViewProps {
  diffText?: string;
  fileStats?: DiffFileInfo[];
  projectId?: string;
  sessionId?: string;
  repository?: GitRepository;
  isDefaultRepository?: boolean;
  onCommitted?: () => void;
  review?: GitReviewSnapshot;
}

type FileCategory = "staged" | "unstaged" | "untracked";
const CATEGORY_LABELS: Record<FileCategory, string> = { staged: "Staged Changes", unstaged: "Changes", untracked: "Untracked Files" };
const CATEGORY_ORDER: FileCategory[] = ["staged", "unstaged", "untracked"];
const LEFT_WIDTH_MIN = 200;
const LEFT_WIDTH_MAX = 600;

export function DiffView({ diffText, fileStats, projectId, sessionId, repository, isDefaultRepository, onCommitted, review }: DiffViewProps) {
  const isLazyMode = !diffText && !!fileStats;
  const repositoryId = repository?.id;
  const gitScope = useMemo<GitScope | null>(() => projectId && repositoryId ? ({
    projectId,
    sessionId,
    repositoryId,
  }) : null, [projectId, sessionId, repositoryId]);

  const files = useMemo(() => {
    if (diffText) return splitUnifiedDiffByFile(diffText);
    if (fileStats) {
      return fileStats.map((f): ParsedDiffFile => ({
        key: `${f.original_path ?? f.path}->${f.path}`,
        oldPath: f.status === "added" ? "/dev/null" : (f.original_path ?? f.path),
        newPath: f.status === "deleted" ? "/dev/null" : f.path,
        diffText: "",
        isBinary: !!f.binary,
        additions: f.insertions,
        deletions: f.deletions,
        isNewFile: f.status === "added",
        isDeletedFile: f.status === "deleted",
        fileLang: null,
        workspacePath: f.workspace_path,
      }));
    }
    return [];
  }, [diffText, fileStats]);

  const [fileFilter, setFileFilter] = useState("");
  const [activeFile, setActiveFile] = useState<string | null>(null);
  const [activeFileCategory, setActiveFileCategory] = useState<FileCategory>("unstaged");
  const [viewedFiles, setViewedFiles] = useState<Set<string>>(new Set());
  const [viewMode, _setViewMode] = useState<"inline" | "single">(() => {
    const saved = localStorage.getItem("diffViewMode");
    return saved === "single" ? "single" : "inline";
  });
  const setViewMode = useCallback((mode: "inline" | "single") => {
    _setViewMode(mode);
    localStorage.setItem("diffViewMode", mode);
  }, []);
  const [showViewMenu, setShowViewMenu] = useState(false);
  const viewMenuRef = useRef<HTMLDivElement>(null);
  const [commitMsg, setCommitMsg] = useState("");
  const [committing, setCommitting] = useState(false);
  const [generatingMsg, setGeneratingMsg] = useState(false);
  const [discardingFile, setDiscardingFile] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState<"changes" | "history">("changes");
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const gitStatus = review?.status;
  const fileRefs = useRef<Record<string, HTMLDivElement | null>>({});

  // Resizable left list. Width persisted to localStorage, clamped to a sane range.
  const splitContainerRef = useRef<HTMLDivElement>(null);
  const [leftWidth, setLeftWidth] = useState(() => {
    const saved = parseInt(localStorage.getItem("diffLeftWidth") || "", 10);
    return Number.isFinite(saved) ? Math.min(LEFT_WIDTH_MAX, Math.max(LEFT_WIDTH_MIN, saved)) : 288;
  });
  const handleResizeMouseDown = useCallback((e: React.MouseEvent) => {
    e.preventDefault();
    let latest = leftWidth;
    const onMove = (ev: MouseEvent) => {
      const rect = splitContainerRef.current?.getBoundingClientRect();
      if (!rect) return;
      latest = Math.min(LEFT_WIDTH_MAX, Math.max(LEFT_WIDTH_MIN, ev.clientX - rect.left));
      setLeftWidth(latest);
    };
    const onUp = () => {
      document.removeEventListener("mousemove", onMove);
      document.removeEventListener("mouseup", onUp);
      localStorage.setItem("diffLeftWidth", String(Math.round(latest)));
    };
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  }, [leftWidth]);

  const fileRefreshKey = review?.fileRevision ?? 0;

  const filteredFiles = useMemo(() => {
    if (!fileFilter.trim()) return files;
    const lower = fileFilter.toLowerCase();
    return files.filter((f) => getFilePath(f).toLowerCase().includes(lower));
  }, [files, fileFilter]);

  // A file can appear in multiple categories simultaneously (e.g. staged with additional
  // unstaged changes). Build sets per category and push file into every matching group.
  const groupedFiles = useMemo(() => {
    const groups: Record<FileCategory, ParsedDiffFile[]> = { staged: [], unstaged: [], untracked: [] };
    const stagedPaths = new Set(gitStatus?.staged.map((f) => f.path) ?? []);
    const unstagedPaths = new Set(gitStatus?.unstaged.map((f) => f.path) ?? []);
    const untrackedPaths = new Set(gitStatus?.untracked.map((f) => f.path) ?? []);
    for (const file of filteredFiles) {
      const path = getFilePath(file);
      let placed = false;
      if (stagedPaths.has(path)) { groups.staged.push(file); placed = true; }
      if (unstagedPaths.has(path)) { groups.unstaged.push(file); placed = true; }
      if (untrackedPaths.has(path)) { groups.untracked.push(file); placed = true; }
      if (!placed) groups[file.isNewFile ? "untracked" : "unstaged"].push(file);
    }
    return groups;
  }, [filteredFiles, gitStatus]);

  const totalAdditions = files.reduce((s, f) => s + f.additions, 0);
  const totalDeletions = files.reduce((s, f) => s + f.deletions, 0);

  useEffect(() => {
    if (!showViewMenu) return;
    const handleClick = (e: MouseEvent) => {
      if (viewMenuRef.current && !viewMenuRef.current.contains(e.target as Node)) setShowViewMenu(false);
    };
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, [showViewMenu]);

  const scrollToFile = useCallback((key: string, category: FileCategory = "unstaged") => {
    setActiveFile(key);
    setActiveFileCategory(category);
    if (viewMode === "inline") {
      fileRefs.current[key]?.scrollIntoView({ behavior: "instant", block: "start" });
    }
  }, [viewMode]);

  const toggleViewed = useCallback((key: string) => {
    setViewedFiles((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  const handleDiscard = (filePath: string) => {
    if (!gitScope) return;
    const doDiscard = async () => {
      setDiscardingFile(filePath);
      try { await gitApi.discard(gitScope, { file_path: filePath }); onCommitted?.(); }
      catch (e) { console.error("Failed to discard:", e); toast.error("Discard failed", { description: String(e) }); }
      finally { setDiscardingFile(null); }
    };
    toast.warning(`Discard all changes to "${filePath}"?`, {
      duration: Infinity,
      action: { label: "Discard", onClick: doDiscard },
      cancel: { label: "Cancel", onClick: () => {} },
    });
  };

  const commitFilePaths = useMemo(() => {
    if (gitStatus && gitStatus.staged.length > 0) {
      return gitStatus.staged.map((f) => f.path);
    }
    return files.map((f) => getFilePath(f));
  }, [gitStatus, files]);

  const handleCommit = async () => {
    if (!gitScope || !commitMsg.trim() || commitFilePaths.length === 0) return;
    setCommitting(true);
    try {
      await gitApi.commit(gitScope, { file_paths: commitFilePaths, message: commitMsg.trim() });
      setCommitMsg("");
      onCommitted?.();
    } catch (e) { console.error("Failed to commit:", e); toast.error("Commit failed", { description: String(e) }); }
    finally { setCommitting(false); }
  };

  const handleGenerateMessage = async () => {
    if (!gitScope || commitFilePaths.length === 0) return;
    setGeneratingMsg(true);
    try { setCommitMsg((await gitApi.generateMessage(gitScope, { file_paths: commitFilePaths })).message); }
    catch (e) { console.error("Failed to generate message:", e); toast.error("Failed to generate message", { description: String(e) }); }
    finally { setGeneratingMsg(false); }
  };

  const handleRefreshAll = useCallback(() => {
    onCommitted?.();
  }, [onCommitted]);

  const handleDiscardAll = (paths: string[]) => {
    if (!gitScope || paths.length === 0) return;
    toast.warning(`Discard all changes to ${paths.length} file(s)?`, {
      duration: Infinity,
      action: {
        label: "Discard All",
        onClick: async () => {
          for (const p of paths) {
            try { await gitApi.discard(gitScope, { file_path: p }); }
            catch (e) { console.error(`Failed to discard ${p}:`, e); }
          }
          onCommitted?.();
        },
      },
      cancel: { label: "Cancel", onClick: () => {} },
    });
  };

  const handleStageFiles = async (paths: string[]) => {
    if (!gitScope || paths.length === 0) return;
    try {
      await gitApi.stage(gitScope, { file_paths: paths });
      onCommitted?.();
    } catch (e) {
      toast.error("Stage failed", { description: String(e) });
    }
  };

  const handleUnstageFiles = async (paths: string[]) => {
    if (!gitScope || paths.length === 0) return;
    try {
      await gitApi.unstage(gitScope, { file_paths: paths });
      onCommitted?.();
    } catch (e) {
      toast.error("Unstage failed", { description: String(e) });
    }
  };

  // --- Navigation state for BranchToolbar ---
  const [changeCount, setChangeCount] = useState(0);
  const [currentChangeIdx, setCurrentChangeIdx] = useState(0);
  const rightPanelRef = useRef<HTMLDivElement>(null);
  const autoScrolledRef = useRef(false);

  // Inline comments for file list badges
  const chatSessionId = useAppStore((s) => s.currentSession?.id);
  const allPickedComments = useAppStore((s) => s.pickedComments);
  const sessionComments = chatSessionId ? (allPickedComments[chatSessionId] || []) : [];

  useEffect(() => {
    setCurrentChangeIdx(0);
    setChangeCount(0);
    autoScrolledRef.current = false;
  }, [activeFile, activeFileCategory]);

  // Auto-scroll to first change when diff content loads for a newly selected file
  useEffect(() => {
    if (viewMode !== "single" || changeCount === 0 || autoScrolledRef.current) return;
    autoScrolledRef.current = true;
    const container = rightPanelRef.current;
    if (!container) return;
    const target = container.querySelector<HTMLElement>('[data-change-idx="0"]');
    if (target) {
      target.scrollIntoView({ behavior: "instant", block: "center" });
    }
  }, [changeCount, viewMode]);

  const activeFileIdx = useMemo(() => {
    if (!activeFile) return -1;
    return filteredFiles.findIndex((f) => f.key === activeFile);
  }, [activeFile, filteredFiles]);

  const handleNavigate = useCallback((direction: "prev" | "next") => {
    if (viewMode === "inline") {
      if (filteredFiles.length === 0) return;
      let idx = activeFileIdx;
      if (direction === "next") {
        idx = idx < filteredFiles.length - 1 ? idx + 1 : 0;
      } else {
        idx = idx > 0 ? idx - 1 : filteredFiles.length - 1;
      }
      const file = filteredFiles[idx];
      if (file) scrollToFile(file.key);
    } else {
      if (changeCount === 0) return;
      const container = rightPanelRef.current;
      if (!container) return;

      let targetIdx: number;
      if (direction === "next") {
        targetIdx = currentChangeIdx < changeCount - 1 ? currentChangeIdx + 1 : 0;
      } else {
        targetIdx = currentChangeIdx > 0 ? currentChangeIdx - 1 : changeCount - 1;
      }

      const target = container.querySelector<HTMLElement>(`[data-change-idx="${targetIdx}"]`);
      if (target) {
        target.scrollIntoView({ behavior: "instant", block: "center" });
        setCurrentChangeIdx(targetIdx);
      }
    }
  }, [viewMode, filteredFiles, activeFileIdx, scrollToFile, changeCount, currentChangeIdx]);

  const navLabel = useMemo(() => {
    if (viewMode === "inline") {
      if (filteredFiles.length === 0) return undefined;
      return `${activeFileIdx >= 0 ? activeFileIdx + 1 : 1}/${filteredFiles.length}`;
    }
    if (changeCount === 0) return undefined;
    return `${currentChangeIdx + 1}/${changeCount}`;
  }, [viewMode, filteredFiles, activeFileIdx, changeCount, currentChangeIdx]);

  // Always called to satisfy hooks rules; state is self-contained
  const history = HistoryTab({ scope: gitScope, repository, allowLegacyComments: isDefaultRepository, commitsSnapshot: review?.commits });

  const renderFileRow = (file: ParsedDiffFile, category: FileCategory) => {
    const path = getFilePath(file);
    const fileName = path.split("/").pop() || path;
    const dirPath = path.includes("/") ? path.substring(0, path.lastIndexOf("/")) : "";
    const isStaged = category === "staged";
    const commentCount = sessionComments.filter((c) =>
      c.filePath === path && (c.repositoryId === repository?.id || (!c.repositoryId && isDefaultRepository))
    ).length;
    return (
      <div
        key={file.key}
        className={cn("relative flex items-center gap-1.5 px-2 py-1.5 cursor-pointer text-xs hover:bg-muted/80 transition-colors group",
          activeFile === file.key && activeFileCategory === category && "bg-muted", viewedFiles.has(file.key) && "opacity-50")}
        onClick={() => scrollToFile(file.key, category)}
      >
        <span className={cn("w-1.5 h-1.5 rounded-full shrink-0",
          file.isNewFile && "bg-green-500", file.isDeletedFile && "bg-red-500",
          !file.isNewFile && !file.isDeletedFile && "bg-yellow-500")} />
        <div className="flex-1 min-w-0 flex items-center overflow-hidden">
          {dirPath && <span className="text-muted-foreground truncate flex-shrink min-w-0">{dirPath}/</span>}
          <span className="font-medium flex-shrink-0 whitespace-nowrap">{fileName}</span>
        </div>
        {commentCount > 0 && (
          <span className="shrink-0 text-[10px] font-medium px-1.5 py-0.5 rounded-full bg-blue-500/15 text-blue-400">
            {commentCount}
          </span>
        )}
        {projectId && (
          <div className="absolute inset-y-0 right-0 pl-6 pr-2 flex items-center gap-0.5 opacity-0 group-hover:opacity-100 transition-opacity bg-gradient-to-l from-muted from-60% to-transparent">
            {isStaged ? (
              <button onClick={(e) => { e.stopPropagation(); handleUnstageFiles([path]); }}
                className="p-0.5 text-muted-foreground hover:text-orange-500 transition-colors" title="Unstage">
                <Minus className="h-3 w-3" />
              </button>
            ) : (
              <button onClick={(e) => { e.stopPropagation(); handleStageFiles([path]); }}
                className="p-0.5 text-muted-foreground hover:text-green-500 transition-colors" title="Stage">
                <Plus className="h-3 w-3" />
              </button>
            )}
            <button onClick={(e) => { e.stopPropagation(); handleDiscard(path); }}
              disabled={discardingFile === path}
              className="p-0.5 text-muted-foreground hover:text-destructive transition-colors"
              title="Discard changes">
              {discardingFile === path ? <Loader2 className="h-3 w-3 animate-spin" /> : <Trash2 className="h-3 w-3" />}
            </button>
          </div>
        )}
      </div>
    );
  };

  return (
    <div className="flex flex-col h-full">
      {gitScope && (
        <BranchToolbar scope={gitScope} onBranchChanged={handleRefreshAll}
          branchData={review?.branches}
          aheadCount={gitStatus?.ahead}
          navLabel={activeTab === "changes" && activeFile ? navLabel : undefined}
          onNavigate={activeTab === "changes" && activeFile && filteredFiles.length > 0 ? handleNavigate : undefined}
          sidebarCollapsed={sidebarCollapsed}
          onToggleSidebar={() => setSidebarCollapsed((v) => !v)} />
      )}
      <div className="flex flex-1 min-h-0" ref={splitContainerRef}>
        {/* Left sidebar (resizable) */}
        {!sidebarCollapsed && (
        <>
        <div className="shrink-0 border-r flex flex-col" style={{ width: leftWidth }}>
          <div className="flex border-b text-xs">
            {(["changes", "history"] as const).map((tab) => (
              <button key={tab} onClick={() => setActiveTab(tab)}
                className={cn("flex-1 px-3 py-2 text-center capitalize transition-colors",
                  activeTab === tab ? "border-b-2 border-primary font-medium" : "text-muted-foreground hover:text-foreground")}>
                {tab}
              </button>
            ))}
          </div>

          {activeTab === "changes" ? (
            <>
              <div className="p-2 border-b">
                <div className="relative">
                  <Filter className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
                  <input type="text" value={fileFilter} onChange={(e) => setFileFilter(e.target.value)}
                    placeholder="Filter files..." className="w-full pl-7 pr-2 py-1.5 text-xs bg-transparent border rounded focus:outline-none focus:ring-1 focus:ring-primary" />
                </div>
              </div>
              <div className="flex items-center gap-2 px-2 py-1.5 border-b text-xs text-muted-foreground">
                <span>{filteredFiles.length} files</span>
                <span className="ml-auto text-green-600 dark:text-green-400">+{totalAdditions}</span>
                <span className="text-red-600 dark:text-red-400">-{totalDeletions}</span>
                <div className="relative" ref={viewMenuRef}>
                  <button onClick={() => setShowViewMenu(!showViewMenu)}
                    className="p-1 rounded hover:bg-muted transition-colors" title="View mode">
                    {viewMode === "inline" ? <LayoutList className="h-3.5 w-3.5" /> : <FileCode2 className="h-3.5 w-3.5" />}
                  </button>
                  {showViewMenu && (
                    <div className="absolute right-0 top-full mt-1 w-36 bg-popover border rounded-md shadow-lg z-50">
                      <button onClick={() => { setViewMode("inline"); setShowViewMenu(false); }}
                        className={cn("w-full px-3 py-1.5 text-left text-xs hover:bg-muted transition-colors flex items-center gap-2", viewMode === "inline" && "bg-muted/50 font-medium")}>
                        {viewMode === "inline" ? <Check className="h-3 w-3" /> : <span className="w-3" />}
                        <LayoutList className="h-3 w-3" /> Inline diff
                      </button>
                      <button onClick={() => { setViewMode("single"); setShowViewMenu(false); }}
                        className={cn("w-full px-3 py-1.5 text-left text-xs hover:bg-muted transition-colors flex items-center gap-2", viewMode === "single" && "bg-muted/50 font-medium")}>
                        {viewMode === "single" ? <Check className="h-3 w-3" /> : <span className="w-3" />}
                        <FileCode2 className="h-3 w-3" /> Single file
                      </button>
                    </div>
                  )}
                </div>
              </div>
              <div className="flex-1 overflow-y-auto">
                {filteredFiles.length === 0 && (
                  <div className="text-center py-8 text-muted-foreground text-xs">No changes</div>
                )}
                {CATEGORY_ORDER.map((cat) => {
                  const group = groupedFiles[cat];
                  if (group.length === 0) return null;
                  const groupPaths = group.map((f) => getFilePath(f));
                  return (
                    <div key={cat}>
                      <div className="px-2 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground bg-muted/30 border-b flex items-center group/cat">
                        <span>{CATEGORY_LABELS[cat]} ({group.length})</span>
                        {projectId && (
                          <div className="ml-auto opacity-0 group-hover/cat:opacity-100 flex items-center gap-0.5 transition-opacity">
                            {cat === "staged" ? (
                              <button onClick={() => handleUnstageFiles(groupPaths)}
                                className="p-0.5 hover:text-orange-500 transition-colors" title="Unstage all">
                                <Minus className="h-3 w-3" />
                              </button>
                            ) : (
                              <button onClick={() => handleStageFiles(groupPaths)}
                                className="p-0.5 hover:text-green-500 transition-colors" title="Stage all">
                                <Plus className="h-3 w-3" />
                              </button>
                            )}
                            {cat !== "staged" && (
                              <button onClick={() => handleDiscardAll(groupPaths)}
                                className="p-0.5 hover:text-destructive transition-colors" title="Discard all">
                                <Trash2 className="h-3 w-3" />
                              </button>
                            )}
                          </div>
                        )}
                      </div>
                      {group.map((f) => renderFileRow(f, cat))}
                    </div>
                  );
                })}
              </div>
              {projectId && (
                <div className="border-t p-2 space-y-2">
                  <div className="relative">
                    <Textarea value={commitMsg} onChange={(e) => setCommitMsg(e.target.value)}
                      placeholder="Commit message..." className="text-xs min-h-[60px] pr-8 resize-none" rows={2} />
                    <button onClick={handleGenerateMessage} disabled={generatingMsg || commitFilePaths.length === 0}
                      className="absolute top-1.5 right-1.5 p-1 rounded hover:bg-muted text-muted-foreground hover:text-primary transition-colors disabled:opacity-50"
                      title="Generate commit message">
                      {generatingMsg ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Sparkles className="h-3.5 w-3.5" />}
                    </button>
                  </div>
                <Button size="sm" className="w-full h-7 text-xs" onClick={handleCommit}
                  disabled={committing || !commitMsg.trim() || commitFilePaths.length === 0}>
                  {committing ? <Loader2 className="h-3 w-3 animate-spin mr-1" /> : <GitCommitHorizontal className="h-3 w-3 mr-1" />}
                  Commit ({commitFilePaths.length} files)
                </Button>
                </div>
              )}
            </>
          ) : (
            history.sidebar
          )}
        </div>

        {/* Drag handle between the file list and the diff content */}
        <div
          className="w-1 cursor-col-resize hover:bg-primary/20 active:bg-primary/40 transition-colors shrink-0"
          onMouseDown={handleResizeMouseDown}
          title="Drag to resize"
        />
        </>
        )}

        {/* Right: Diff content */}
        <div className="flex-1 min-w-0 flex">
          <div className="flex-1 overflow-y-auto min-w-0" ref={rightPanelRef}>
            {activeTab === "changes" ? (
              viewMode === "inline" ? (
                <div className="space-y-2 p-4">
                  {filteredFiles.map((file) => (
                    <div key={file.key} ref={(el) => { fileRefs.current[file.key] = el; }}>
                      <DiffFileCard file={file} isViewed={viewedFiles.has(file.key)} isActive={activeFile === file.key}
                        onToggleViewed={toggleViewed} lazyLoad={isLazyMode} projectId={projectId} sessionId={sessionId}
                        refreshKey={fileRefreshKey} repository={repository}
                        allowLegacyComments={isDefaultRepository}
                        staged={activeFile === file.key ? activeFileCategory === "staged" : undefined} />
                    </div>
                  ))}
                </div>
              ) : (() => {
                const file = activeFile ? filteredFiles.find((f) => f.key === activeFile) : null;
                return file ? (
                  <div className="h-full">
                    <DiffFileCard key={`${file.key}:${activeFileCategory}`} file={file} isViewed={viewedFiles.has(file.key)} isActive
                      onToggleViewed={toggleViewed} lazyLoad={isLazyMode} projectId={projectId} sessionId={sessionId}
                      defaultExpanded borderless fullFile onChangeCount={setChangeCount} refreshKey={fileRefreshKey} repository={repository}
                      allowLegacyComments={isDefaultRepository}
                      staged={activeFileCategory === "staged"} />
                  </div>
                ) : (
                  <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
                    Select a file to view its changes
                  </div>
                );
              })()
            ) : (
              history.content
            )}
          </div>
          {activeTab === "changes" && viewMode === "single" && activeFile && (
            <DiffMinimap scrollRef={rightPanelRef} />
          )}
        </div>
      </div>
    </div>
  );
}
