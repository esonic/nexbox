import { useState, useMemo, useRef, useCallback, useEffect } from "react";
import { Loader2, ArrowLeft } from "lucide-react";
import { cn, formatAbsoluteDateTime } from "@/lib/utils";
import { getFilePath, splitUnifiedDiffByFile } from "@/lib/diff-parser";
import { gitApi, type GitCommitInfo, type GitCommitFileEntry, type GitRepository, type GitScope } from "@/lib/api";
import { DiffFileCard } from "./DiffFileCard";

export function HistoryTab({ scope, repository, allowLegacyComments, autoLoad }: { scope: GitScope | null; repository?: GitRepository; allowLegacyComments?: boolean; autoLoad: boolean }) {
  const PAGE_SIZE = 20;
  const LOAD_MORE_SIZE = 10;
  const [commits, setCommits] = useState<GitCommitInfo[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [hasMore, setHasMore] = useState(true);
  const [selectedCommit, setSelectedCommit] = useState<string | null>(null);
  const [commitDiffText, setCommitDiffText] = useState<string | null>(null);
  const [loadingDiff, setLoadingDiff] = useState(false);
  const [commitFileList, setCommitFileList] = useState<GitCommitFileEntry[]>([]);
  const [activeFile, setActiveFile] = useState<string | null>(null);
  const fileRefs = useRef<Record<string, HTMLDivElement | null>>({});
  const commitRequestGeneration = useRef(0);

  const commitDiffFiles = useMemo(
    () => (commitDiffText ? splitUnifiedDiffByFile(commitDiffText) : []),
    [commitDiffText],
  );

  const loadHistory = useCallback(async () => {
    if (!scope) return;
    setLoading(true);
    try {
      const data = await gitApi.log(scope, PAGE_SIZE);
      setCommits(data);
      setHasMore(data.length >= PAGE_SIZE);
    } catch (e) { console.error("Failed to load history:", e); }
    finally { setLoading(false); }
  }, [scope]);

  const loadMore = useCallback(async () => {
    if (!scope || loadingMore) return;
    setLoadingMore(true);
    try {
      const data = await gitApi.log(scope, LOAD_MORE_SIZE, commits.length);
      setCommits((prev) => [...prev, ...data]);
      setHasMore(data.length >= LOAD_MORE_SIZE);
    } catch (e) { console.error("Failed to load more:", e); }
    finally { setLoadingMore(false); }
  }, [scope, commits.length, loadingMore]);

  const loadedRef = useRef(false);
  useEffect(() => {
    if (autoLoad && !loadedRef.current && commits.length === 0) {
      loadedRef.current = true;
      loadHistory();
    }
  }, [autoLoad, loadHistory, commits.length]);

  const handleSelectCommit = async (hash: string) => {
    if (!scope) return;
    const generation = ++commitRequestGeneration.current;
    setSelectedCommit(hash);
    setLoadingDiff(true);
    setCommitFileList([]);
    setActiveFile(null);
    try {
      const [diffData, filesData] = await Promise.all([
        gitApi.commitDiff(scope, hash),
        gitApi.commitFiles(scope, hash),
      ]);
      if (generation !== commitRequestGeneration.current) return;
      setCommitDiffText(diffData.diff);
      setCommitFileList(filesData);
    } catch (e) {
      if (generation !== commitRequestGeneration.current) return;
      console.error("Failed to load commit diff:", e);
      setCommitDiffText(null);
      setCommitFileList([]);
    } finally {
      if (generation === commitRequestGeneration.current) setLoadingDiff(false);
    }
  };

  const handleBack = () => {
    commitRequestGeneration.current++;
    setSelectedCommit(null);
    setCommitDiffText(null);
    setCommitFileList([]);
    setActiveFile(null);
  };

  const scrollToFile = useCallback((key: string) => {
    setActiveFile(key);
    fileRefs.current[key]?.scrollIntoView({ behavior: "instant", block: "start" });
  }, []);

  const selectedCommitInfo = selectedCommit ? commits.find((c) => c.hash === selectedCommit) : null;

  const sidebar = (
    <div className="flex-1 overflow-y-auto flex flex-col">
      {loading ? (
        <div className="flex items-center justify-center h-32">
          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
        </div>
      ) : commits.length === 0 ? (
        <div className="text-center py-8 text-muted-foreground text-xs">No commits yet</div>
      ) : !selectedCommit ? (
        <>
          {commits.map((commit) => (
            <button
              key={commit.hash}
              onClick={() => handleSelectCommit(commit.hash)}
              className="w-full text-left px-3 py-2.5 border-b hover:bg-muted/80 transition-colors"
            >
              <div className="text-xs font-medium truncate">{commit.message}</div>
              <div className="flex items-center gap-2 mt-1 text-[10px] text-muted-foreground">
                <code className="font-mono">{commit.short_hash}</code>
                <span>{commit.author}</span>
                <span className="ml-auto whitespace-nowrap font-mono">
                  {formatAbsoluteDateTime(commit.date)}
                </span>
              </div>
            </button>
          ))}
          {hasMore && (
            <button
              onClick={loadMore}
              disabled={loadingMore}
              className="w-full py-2 text-xs text-muted-foreground hover:text-foreground hover:bg-muted/80 transition-colors flex items-center justify-center gap-1.5"
            >
              {loadingMore ? <Loader2 className="h-3 w-3 animate-spin" /> : null}
              {loadingMore ? "Loading..." : "Load more"}
            </button>
          )}
        </>
      ) : (
        <>
          <div className="border-b">
            <button onClick={handleBack} className="flex items-center gap-1.5 w-full px-3 py-2 text-xs hover:bg-muted/80 transition-colors">
              <ArrowLeft className="h-3 w-3" />
              <span className="text-muted-foreground">Back to commits</span>
            </button>
            {selectedCommitInfo && (
              <div className="px-3 pb-2">
                <div className="text-xs font-medium truncate">{selectedCommitInfo.message}</div>
                <div className="flex items-center gap-2 mt-0.5 text-[10px] text-muted-foreground">
                  <code className="font-mono">{selectedCommitInfo.short_hash}</code>
                  <span>{commitFileList.length} files</span>
                </div>
              </div>
            )}
          </div>
          {loadingDiff ? (
            <div className="flex items-center justify-center h-32">
              <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
            </div>
          ) : (
            <div className="flex-1 overflow-y-auto">
              {commitFileList.map((cf) => {
                const fileName = cf.path.split("/").pop() || cf.path;
                const dirPath = cf.path.includes("/") ? cf.path.substring(0, cf.path.lastIndexOf("/")) : "";
                const matchedDiffFile = commitDiffFiles.find((df) => getFilePath(df) === cf.path);
                const diffKey = matchedDiffFile?.key || cf.path;
                return (
                  <div
                    key={cf.path}
                    className={cn("flex items-center gap-1.5 px-2 py-1.5 cursor-pointer text-xs hover:bg-muted/80 transition-colors", activeFile === diffKey && "bg-muted")}
                    onClick={() => scrollToFile(diffKey)}
                  >
                    <span className={cn("w-1.5 h-1.5 rounded-full shrink-0",
                      cf.status === "added" && "bg-green-500", cf.status === "deleted" && "bg-red-500",
                      cf.status === "modified" && "bg-yellow-500", cf.status === "renamed" && "bg-blue-500"
                    )} />
                    <div className="flex-1 min-w-0 flex items-center overflow-hidden">
                      {dirPath && <span className="text-muted-foreground truncate flex-shrink min-w-0">{dirPath}/</span>}
                      <span className="font-medium flex-shrink-0 whitespace-nowrap">{fileName}</span>
                    </div>
                    <span className={cn("text-[10px] uppercase shrink-0",
                      cf.status === "added" && "text-green-600 dark:text-green-400",
                      cf.status === "deleted" && "text-red-600 dark:text-red-400",
                      cf.status === "modified" && "text-yellow-600 dark:text-yellow-400",
                      cf.status === "renamed" && "text-blue-600 dark:text-blue-400"
                    )}>
                      {cf.status[0]?.toUpperCase()}
                    </span>
                  </div>
                );
              })}
            </div>
          )}
        </>
      )}
    </div>
  );

  const content = (
    <div className="p-4">
      {!selectedCommit ? (
        <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
          Select a commit to view its changes
        </div>
      ) : loadingDiff ? (
        <div className="flex items-center justify-center h-32">
          <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
        </div>
      ) : commitDiffFiles.length > 0 ? (
        <div className="space-y-2">
          {commitDiffFiles.map((file) => (
            <div key={file.key} ref={(el) => { fileRefs.current[file.key] = el; }}>
              <DiffFileCard file={file} isViewed={false} isActive={activeFile === file.key} hideViewedToggle
                projectId={scope?.projectId} sessionId={scope?.sessionId} repository={repository}
                allowLegacyComments={allowLegacyComments}
                workspacePath={commitFileList.find((entry) => entry.path === getFilePath(file))?.workspace_path} />
            </div>
          ))}
        </div>
      ) : (
        <div className="text-center py-8 text-muted-foreground text-sm">No diff available for this commit</div>
      )}
    </div>
  );

  return { sidebar, content };
}
