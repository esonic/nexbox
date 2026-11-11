import { useState, useEffect, useCallback, useMemo, useRef, lazy, Suspense } from "react";
import { createPortal } from "react-dom";
import {
  File,
  ChevronDown,
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
  Eye,
  Code2,
  CaseSensitive,
  PanelLeftClose,
  PanelLeftOpen,
} from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { ApiError, filesApi, gitApi, type FileEntry, type FileContentResponse, type FileSearchResult, type GitStatusResponse, type RepositoryFileRef } from "@/lib/api";
import { useFileBrowserStore } from "@/stores/fileBrowser";
import { gitContextKey } from "@/stores/git";
import { useLayoutStore } from "@/stores/layout";
import { FILE_TREE_WIDTH_MIN, FILE_TREE_WIDTH_MAX } from "@/lib/constants";
import { MarkdownRenderer, isMarkdownPath } from "@/components/markdown/MarkdownRenderer";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem } from "@/components/ui/dropdown-menu";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

const MonacoFileEditor = lazy(() => import("./MonacoFileEditor"));
// Type-only import: erased at build time, so it does not pull the Monaco chunk eagerly.
import type { MonacoFileEditorHandle } from "./MonacoFileEditor";

import { useFileTree } from "./useFileTree";
import { FileTreeItem } from "./FileTreeItem";
import { SearchResults } from "./PanelSearch";
import {
  INTERNAL_DRAG_TYPE,
  dirnameOf,
  joinPath,
  dropTargetDir,
  isHtmlPath,
  formatSize,
  type GitDeco,
  type GitChangeKind,
} from "./fileBrowserShared";


// ── Main component ──

interface FileBrowserProps {
  projectId: string;
  projectName?: string;
  sessionId?: string;
  onOpenQuickOpen: () => void;
  refreshTarget?: HTMLDivElement | null;
}

export function FileBrowser({ projectId, projectName = "Files", sessionId, onOpenQuickOpen, refreshTarget }: FileBrowserProps) {
  const browserContextKey = gitContextKey(projectId, sessionId);
  const fileNavigation = useFileBrowserStore((s) => s.navigation[browserContextKey]);
  const [fileContent, setFileContent] = useState<FileContentResponse | null>(null);
  // Committed (HEAD) content of the open file — the dirty-diff baseline for the
  // editor's gutter/scrollbar change markers. undefined while loading or when
  // the file isn't tracked, which disables the markers.
  const [fileBaseline, setFileBaseline] = useState<string | undefined>(undefined);
  // Git status for the whole tree, polled so changed files/folders get VSCode-
  // style markers. Null until first load or when the project isn't a git repo.
  const [gitStatus, setGitStatus] = useState<GitStatusResponse | null>(null);
  const [loadingFile, setLoadingFile] = useState(false);
  const [fileLoadError, setFileLoadError] = useState<string | null>(null);
  const [markdownMode, setMarkdownMode] = useState<"source" | "rendered">("source");
  const [imageSource, setImageSource] = useState(false);
  const [imageError, setImageError] = useState(false);

  // Edit/save state — file editing (Monaco) is always available now.
  const [editorValue, setEditorValueState] = useState("");
  const editorValueRef = useRef("");
  const setEditorValue = useCallback((value: string) => {
    editorValueRef.current = value;
    setEditorValueState(value);
  }, []);
  const [refreshing, setRefreshing] = useState(false);
  const [imageRevision, setImageRevision] = useState(0);
  const [reloadTarget, setReloadTarget] = useState<{ context: string; request: number } | null>(null);
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

  // Persisted state from store — bucketed per project + local/worktree context
  // so repository IDs and paths from one checkout cannot leak into another.
  const EMPTY_EXPANDED = useMemo(() => new Set<string>(), []);
  const expanded = useFileBrowserStore(
    (s) => s.projects[browserContextKey]?.expanded ?? EMPTY_EXPANDED
  );
  const selectedPath = useFileBrowserStore(
    (s) => s.projects[browserContextKey]?.selectedPath ?? null
  );
  const selectedRepositoryFile = useFileBrowserStore(
    (s) => s.projects[browserContextKey]?.selectedRepositoryFile ?? null
  );
  const toggleExpandRaw = useFileBrowserStore((s) => s.toggleExpand);
  const expandAncestorsRaw = useFileBrowserStore((s) => s.expandAncestors);
  const setSelectedPathRaw = useFileBrowserStore((s) => s.setSelectedPath);
  const setSelectedRepositoryFileRaw = useFileBrowserStore((s) => s.setSelectedRepositoryFile);
  const toggleExpand = useCallback(
    (path: string) => toggleExpandRaw(browserContextKey, path),
    [toggleExpandRaw, browserContextKey]
  );
  const setSelectedPath = useCallback(
    (path: string | null) => setSelectedPathRaw(browserContextKey, path),
    [setSelectedPathRaw, browserContextKey]
  );
  const setSelectedRepositoryFile = useCallback(
    (file: RepositoryFileRef | null) => setSelectedRepositoryFileRaw(browserContextKey, file),
    [setSelectedRepositoryFileRaw, browserContextKey]
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

  // Search state (panel search = content search, VSCode Search-view style)
  const [searchQuery, setSearchQuery] = useState("");
  const [searchCaseSensitive, setSearchCaseSensitive] = useState(false);
  const [searchResults, setSearchResults] = useState<FileSearchResult[]>([]);
  const [searchLoading, setSearchLoading] = useState(false);
  const [collapsedSearchGroups, setCollapsedSearchGroups] = useState<Set<string>>(new Set());
  const [activeResultIndex, setActiveResultIndex] = useState(-1);
  const [searchHistory, setSearchHistory] = useState<string[]>(() => {
    try {
      const raw = localStorage.getItem("beehub:file-search-history");
      return raw ? (JSON.parse(raw) as string[]) : [];
    } catch { return []; }
  });
  const [historyOpen, setHistoryOpen] = useState(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const searchTimerRef = useRef<ReturnType<typeof setTimeout>>(undefined);
  const searchReqRef = useRef(0);
  const activeResultRef = useRef<HTMLDivElement>(null);
  // In-file search state shared with Monaco (seeded when a content-search result
  // is clicked so the editor reveals the match and opens its native find).
  const [fileSearchQuery, setFileSearchQuery] = useState("");
  const [fileSearchCaseSensitive, setFileSearchCaseSensitive] = useState(false);
  const [pendingTargetLine, setPendingTargetLine] = useState<number | null>(null);
  useEffect(() => {
    if (!fileNavigation) return;
    setPendingTargetLine(fileNavigation.line ?? null);
    if (fileNavigation.line) {
      setMarkdownMode("source");
      setFileSearchQuery("");
      setSearchNavSeq((seq) => seq + 1);
    }
  }, [fileNavigation]);
  // Bumped on each content-result click so Monaco re-reveals even when the
  // query/line are unchanged (e.g. re-clicking the same result).
  const [searchNavSeq, setSearchNavSeq] = useState(0);
  const fileContentSelectableRef = useRef<HTMLDivElement>(null);
  // Imperative handle to the Monaco editor so Cmd+F opens its native find widget.
  const monacoEditorRef = useRef<MonacoFileEditorHandle>(null);

  // Track if a load is for the current projectId/sessionId; ignore stale results after switch.
  const fileRequestRef = useRef(0);
  const workspaceStatusRequestRef = useRef(0);

  // Tree pane collapse — gives the content/editor pane the full width.
  const [treeCollapsed, setTreeCollapsed] = useState(false);

  const { childrenMap, setChildrenMap, loadingPaths, rootLoading, loadChildren, refreshDir, refreshTree } =
    useFileTree(projectId, sessionId, expanded, containerRef);

  // Mirror childrenMap in a ref so revealPath can check what's already loaded
  // without depending on childrenMap (which would re-create the callback often).
  const childrenMapRef = useRef(childrenMap);
  useEffect(() => { childrenMapRef.current = childrenMap; }, [childrenMap]);

  // Expand every ancestor folder of `path` and load their children so the file
  // becomes visible/selected in the tree — used when opening a file from panel
  // search, Quick Open, or the diff viewer.
  const revealPath = useCallback((path: string) => {
    if (!path) return;
    expandAncestorsRaw(browserContextKey, path);
    const parts = path.split("/");
    let acc = "";
    for (let i = 0; i < parts.length - 1; i++) {
      acc = acc ? `${acc}/${parts[i]}` : parts[i];
      if (acc && childrenMapRef.current[acc] === undefined) loadChildren(acc);
    }
  }, [expandAncestorsRaw, browserContextKey, loadChildren]);

  // Scroll the highlighted tree row into view once the tree renders it (after
  // reveal expands its ancestors and their children load in).
  const treeScrollRef = useRef<HTMLDivElement>(null);
  const lastRevealedPathRef = useRef<string | null>(null);
  useEffect(() => {
    if (!highlightedPath) {
      lastRevealedPathRef.current = null;
      return;
    }
    if (lastRevealedPathRef.current === highlightedPath) return;
    const id = setTimeout(() => {
      const el = treeScrollRef.current?.querySelector(`[data-tree-path="${CSS.escape(highlightedPath)}"]`);
      if (el) {
        el.scrollIntoView({ block: "nearest" });
        lastRevealedPathRef.current = highlightedPath;
      }
    }, 60);
    return () => clearTimeout(id);
  }, [highlightedPath, childrenMap]);

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
      handleSelectFile(selectedPath, selectedRepositoryFile ?? undefined);
    }
    // handleSelectFile intentionally excluded from deps — we only want this
    // restore to fire once per (re)mount/project-switch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rootLoading, selectedPath, selectedRepositoryFile, browserContextKey]);

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

  // Markdown image srcs are relative to the previewed file, not the SPA URL.
  const resolveMarkdownImageSrc = useCallback(
    (src: string) => {
      if (!selectedPath || /^([a-z][a-z0-9+.-]*:|\/\/|#)/i.test(src)) return src;
      const [pathPart] = src.split(/[?#]/);
      let decoded = pathPart;
      try {
        decoded = decodeURIComponent(pathPart);
      } catch {
        // keep the raw path
      }
      const parts: string[] = decoded.startsWith("/") ? [] : selectedPath.split("/").slice(0, -1);
      for (const seg of decoded.split("/")) {
        if (seg === "" || seg === ".") continue;
        if (seg === "..") {
          if (parts.length === 0) return src;
          parts.pop();
        } else {
          parts.push(seg);
        }
      }
      return filesApi.serveUrl(projectId, parts.join("/"), sessionId, selectedRepositoryFile?.repository_id);
    },
    [projectId, selectedPath, sessionId, selectedRepositoryFile],
  );
  const markdownPreviewAvailable = !!selectedPath && !!fileContent && !fileContent.binary && isMarkdownPath(selectedPath);
  const imagePreviewAvailable = !!selectedPath && !!fileContent && /\.(png|jpe?g|gif|webp|avif|bmp|ico|svg)$/i.test(selectedPath);
  const showingImage = imagePreviewAvailable && (!imageSource || fileContent.binary);
  const sourcePreviewAvailable = markdownPreviewAvailable || (imagePreviewAvailable && !fileContent.binary);
  const showingPreview = showingImage || (markdownPreviewAvailable && markdownMode === "rendered");
  useEffect(() => {
    setImageSource(false);
    setImageError(false);
  }, [browserContextKey, selectedPath, selectedRepositoryFile]);
  useEffect(() => {
    if (!markdownPreviewAvailable && markdownMode !== "source") {
      setMarkdownMode("source");
    }
  }, [markdownPreviewAvailable, markdownMode]);

  useEffect(() => {
    setFileSearchQuery("");
    setFileSearchCaseSensitive(false);
    const navigation = useFileBrowserStore.getState().navigation[browserContextKey];
    setPendingTargetLine(navigation?.path === selectedPath ? (navigation.line ?? null) : null);
  }, [browserContextKey, selectedPath]);

  const isDirty = !!fileContent && !fileContent.binary && editorValue !== fileContent.content;

  // Poll git status (like the diff panel) so tree markers stay current with
  // changes made elsewhere (agent edits, terminal, the git panel's stage/commit).
  const refreshGitStatus = useCallback(() => {
    if (!projectId) return;
    const request = ++workspaceStatusRequestRef.current;
    gitApi.workspaceStatus(projectId, sessionId, { includeFiles: true }).then((data) => {
      if (request !== workspaceStatusRequestRef.current) return;
      const status: GitStatusResponse = { staged: [], unstaged: [], untracked: [] };
      for (const file of data.files) {
        if (file.index_status) status.staged.push({ path: file.workspace_path, status: file.index_status });
        if (file.worktree_status === "untracked") status.untracked.push({ path: file.workspace_path, status: "untracked" });
        else if (file.worktree_status) status.unstaged.push({ path: file.workspace_path, status: file.worktree_status });
      }
      setGitStatus(status);
    }).catch(() => {
      if (request === workspaceStatusRequestRef.current) setGitStatus(null);
    });
  }, [projectId, sessionId]);

  useEffect(() => {
    if (!projectId) return;
    refreshGitStatus();
    const interval = setInterval(refreshGitStatus, 5_000);
    return () => clearInterval(interval);
  }, [projectId, refreshGitStatus]);

  const handleSave = useCallback(async () => {
    if (!selectedPath || !fileContent || fileContent.binary) return;
    const request = fileRequestRef.current;
    const content = editorValue;
    setSaving(true);
    try {
      await filesApi.save(projectId, selectedPath, content, sessionId, selectedRepositoryFile?.repository_id, !selectedRepositoryFile && fileNavigation?.path === selectedPath && fileNavigation.safeLink);
      if (fileRequestRef.current === request) {
        setFileContent({ ...fileContent, content, size: new Blob([content]).size });
      }
      toast.success("Saved");
      // Reflect the new change in the tree markers without waiting for the poll.
      refreshGitStatus();
    } catch (e) {
      console.error("Save failed:", e);
      toast.error("Save failed", { description: String(e) });
    } finally {
      setSaving(false);
    }
  }, [projectId, selectedPath, sessionId, selectedRepositoryFile, fileNavigation, fileContent, editorValue, refreshGitStatus]);

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
        // Monaco owns find when mounted (its native widget handles Cmd+F while
        // focused; this covers focus being elsewhere). For markdown preview /
        // binary (no Monaco), defer to the browser's find.
        if (monacoEditorRef.current) {
          e.preventDefault();
          monacoEditorRef.current.openFind();
        }
        return;
      }

      if (key === "s" && fileContent && !fileContent.binary && isDirty && !saving) {
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
  }, [selectedPath, fileContent, isDirty, saving, handleSave]);

  // Fetch the committed (HEAD) baseline for dirty-diff markers. Runs in the
  // background alongside the content load; a monotonic seq guards against a slow
  // response for a previously-selected file clobbering the current one.
  const baselineReqRef = useRef(0);
  const loadFileBaseline = useCallback(async (path: string, repositoryId?: string) => {
    const seq = ++baselineReqRef.current;
    try {
      const res = await filesApi.head(projectId, path, sessionId, repositoryId);
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

  const handleSelectFile = useCallback(async (path: string, repositoryFile?: RepositoryFileRef, safeLink?: boolean) => {
    if (useFileBrowserStore.getState().navigation[browserContextKey]?.path !== path) {
      useFileBrowserStore.getState().clearNavigation(browserContextKey);
      setPendingTargetLine(null);
    }
    const request = ++fileRequestRef.current;
    setImageError(false);
    if (repositoryFile) setSelectedRepositoryFile(repositoryFile);
    else setSelectedPath(path);
    setHighlightedPath(repositoryFile?.workspace_path ?? (repositoryFile ? null : path));
    if (!repositoryFile) revealPath(path);
    setLoadingFile(true);
    setFileLoadError(null);
    setFileBaseline(undefined);
    loadFileBaseline(path, repositoryFile?.repository_id);
    try {
      const navigation = useFileBrowserStore.getState().navigation[browserContextKey];
      const data = await filesApi.content(projectId, path, sessionId, repositoryFile?.repository_id, safeLink || (!repositoryFile && navigation?.path === path && navigation.safeLink));
      if (fileRequestRef.current !== request) return;
      setFileContent(data);
      setEditorValue(data?.binary ? "" : (data?.content ?? ""));
    } catch (e) {
      if (fileRequestRef.current !== request) return;
      console.error("Failed to load file:", e);
      setFileLoadError(e instanceof Error ? e.message : "An unexpected error occurred. Please try again.");
      if (repositoryFile && e instanceof ApiError && (e.code === "repository_not_found" || e.code === "repository_stale")) {
        setSelectedRepositoryFile(null);
        toast.warning("The repository for this file is no longer available");
      }
      setFileContent(null);
      setEditorValue("");
    } finally {
      if (fileRequestRef.current === request) setLoadingFile(false);
    }
  }, [browserContextKey, projectId, sessionId, setSelectedPath, setSelectedRepositoryFile, loadFileBaseline, revealPath, setEditorValue]);

  // Refresh in place: do not show the initial-load spinner or unmount the
  // preview/editor, and ignore responses for a file that is no longer selected.
  const refreshCurrentFile = useCallback(async () => {
    if (!selectedPath) return;
    const request = fileRequestRef.current;
    const localValue = editorValueRef.current;
    try {
      const navigation = useFileBrowserStore.getState().navigation[browserContextKey];
      const data = await filesApi.content(projectId, selectedPath, sessionId, selectedRepositoryFile?.repository_id,
        !selectedRepositoryFile && navigation?.path === selectedPath && navigation.safeLink);
      if (request !== fileRequestRef.current) return;
      // Typing can continue while a reload is in flight. Confirmation only
      // covers the edits that existed when the reload started.
      if (editorValueRef.current !== localValue) {
        toast.warning("Local edits changed during refresh. Refresh again to reload the file.");
        return;
      }
      setFileContent(previous => previous && JSON.stringify(previous) === JSON.stringify(data) ? previous : data);
      setEditorValue(data.binary ? "" : data.content);
      setFileLoadError(null);
      if (/\.(png|jpe?g|gif|webp|avif|bmp|ico|svg)$/i.test(selectedPath)) {
        setImageError(false);
        setImageRevision(value => value + 1);
      }
      void loadFileBaseline(selectedPath, selectedRepositoryFile?.repository_id);
    } catch (error) {
      if (request !== fileRequestRef.current) return;
      toast.error(error instanceof ApiError && error.status === 404
        ? "The file no longer exists. The last loaded content is still shown."
        : "Unable to refresh the current file", {
        description: error instanceof Error ? error.message : String(error),
      });
    }
  }, [browserContextKey, projectId, selectedPath, sessionId, selectedRepositoryFile, setEditorValue, loadFileBaseline]);

  const refreshOperationRef = useRef(0);
  useEffect(() => {
    setRefreshing(false);
    setReloadTarget(null);
    const lifecycle = { fileRequestRef, baselineReqRef, refreshOperationRef };
    return () => {
      // Reject content and baseline responses after unmount/context changes.
      lifecycle.fileRequestRef.current++;
      lifecycle.baselineReqRef.current++;
      lifecycle.refreshOperationRef.current++;
    };
  }, [browserContextKey]);

  const runRefresh = useCallback(async (reloadFile: boolean) => {
    const operation = ++refreshOperationRef.current;
    setRefreshing(true);
    refreshGitStatus();
    try {
      const [treeRefreshed] = await Promise.all([refreshTree(), reloadFile ? refreshCurrentFile() : Promise.resolve()]);
      if (!treeRefreshed && refreshOperationRef.current === operation) {
        toast.error("Some folders could not be refreshed. Please try again.");
      }
    } finally {
      if (refreshOperationRef.current === operation) setRefreshing(false);
    }
  }, [refreshTree, refreshCurrentFile, refreshGitStatus]);

  const handleRefresh = useCallback(() => {
    if (refreshing || saving || loadingFile || rootLoading) return;
    if (selectedPath && isDirty) {
      setReloadTarget({ context: browserContextKey, request: fileRequestRef.current });
    }
    void runRefresh(!isDirty);
  }, [refreshing, saving, loadingFile, rootLoading, selectedPath, isDirty, browserContextKey, runRefresh]);

  const handleConfirmReload = () => {
    const target = reloadTarget;
    setReloadTarget(null);
    if (!target || target.context !== browserContextKey || target.request !== fileRequestRef.current || saving) return;
    void runRefresh(true);
  };

  // Open a file when another panel (e.g. the diff viewer) requests it while the
  // Files panel is already mounted. The first-mount case is covered by the
  // restore effect above, which reads the seeded selectedPath from the store.
  useEffect(() => {
    const handler = (e: Event) => {
      const detail = (e as CustomEvent<{ path?: string; safeLink?: boolean; file?: RepositoryFileRef }>).detail;
      if (detail?.file) handleSelectFile(detail.file.path, detail.file);
      else if (detail?.path) handleSelectFile(detail.path, undefined, detail.safeLink);
    };
    window.addEventListener("open-file", handler);
    return () => window.removeEventListener("open-file", handler);
  }, [handleSelectFile]);

  const handleSelectSearchResult = useCallback(async (path: string, line?: number) => {
    const request = ++fileRequestRef.current;
    setSelectedPath(path);
    setHighlightedPath(path);
    revealPath(path);
    setLoadingFile(true);
    setFileLoadError(null);
    setFileBaseline(undefined);
    loadFileBaseline(path);
    try {
      const navigation = useFileBrowserStore.getState().navigation[browserContextKey];
      const data = await filesApi.content(projectId, path, sessionId, undefined, navigation?.path === path && navigation.safeLink);
      if (fileRequestRef.current !== request) return;
      setFileContent(data);
      setEditorValue(data?.binary ? "" : (data?.content ?? ""));
    } catch (e) {
      if (fileRequestRef.current !== request) return;
      console.error("Failed to load file:", e);
      setFileLoadError(e instanceof Error ? e.message : "An unexpected error occurred. Please try again.");
      setFileContent(null);
      setEditorValue("");
    } finally {
      if (fileRequestRef.current === request) setLoadingFile(false);
    }
    if (line != null && searchQuery.trim()) {
      // Mirror the panel's Match Case into the in-file search so Monaco reveals
      // and highlights exactly what the panel matched.
      setFileSearchCaseSensitive(searchCaseSensitive);
      setFileSearchQuery(searchQuery.trim());
      setPendingTargetLine(line);
      setSearchNavSeq((seq) => seq + 1);
    }
  }, [browserContextKey, projectId, sessionId, searchQuery, searchCaseSensitive, setSelectedPath, loadFileBaseline, revealPath, setEditorValue]);

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
    const url = filesApi.downloadUrl(projectId, selectedPath, sessionId, selectedRepositoryFile?.repository_id, !selectedRepositoryFile && fileNavigation?.path === selectedPath && fileNavigation.safeLink);
    const a = document.createElement("a");
    a.href = url;
    a.download = selectedPath.split("/").pop() || "download";
    document.body.appendChild(a);
    a.click();
    a.remove();
  }, [projectId, selectedPath, sessionId, selectedRepositoryFile, fileNavigation]);

  const handleOpenInNewTab = useCallback(() => {
    if (!selectedPath) return;
    window.open(filesApi.serveUrl(projectId, selectedPath, sessionId, selectedRepositoryFile?.repository_id, !selectedRepositoryFile && fileNavigation?.path === selectedPath && fileNavigation.safeLink), "_blank", "noopener");
  }, [projectId, selectedPath, sessionId, selectedRepositoryFile, fileNavigation]);

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
    const request = fileRequestRef.current;
    setDeleteConfirmOpen(false);
    try {
      await filesApi.delete(projectId, path, sessionId, selectedRepositoryFile?.repository_id, !selectedRepositoryFile && fileNavigation?.path === path && fileNavigation.safeLink);
      if (!selectedRepositoryFile) {
        const parent = dirnameOf(path);
        await refreshDir(parent);
      }
      if (fileRequestRef.current === request) {
        fileRequestRef.current++;
        setSelectedPath(null);
        setHighlightedPath(null);
        setFileContent(null);
        setEditorValue("");
      }
      toast.success(`Deleted ${path.split("/").pop()}`);
    } catch (e) {
      console.error("Delete failed:", e);
      toast.error("Delete failed", { description: String(e) });
    }
  }, [projectId, selectedPath, sessionId, selectedRepositoryFile, fileNavigation, refreshDir, setSelectedPath, setEditorValue]);

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
  }, [projectId, sessionId, refreshDir, selectedPath, setSelectedPath, setChildrenMap]);

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

  // Debounced content search (race-guarded so a slow response can't clobber a
  // newer query's results).
  useEffect(() => {
    if (!searchQuery.trim()) {
      setSearchResults([]);
      setSearchLoading(false);
      return;
    }
    setSearchLoading(true);
    clearTimeout(searchTimerRef.current);
    const req = ++searchReqRef.current;
    searchTimerRef.current = setTimeout(async () => {
      try {
        const results = await filesApi.search(projectId, searchQuery.trim(), "content", sessionId, searchCaseSensitive);
        if (searchReqRef.current !== req) return;
        setSearchResults(results || []);
      } catch (e) {
        if (searchReqRef.current !== req) return;
        console.error("Search failed:", e);
        setSearchResults([]);
      } finally {
        if (searchReqRef.current === req) setSearchLoading(false);
      }
    }, 300);
    return () => clearTimeout(searchTimerRef.current);
  }, [searchQuery, searchCaseSensitive, projectId, sessionId]);

  // Group content matches by file, preserving first-seen order.
  const groupedSearch = useMemo(() => {
    const map = new Map<string, FileSearchResult[]>();
    for (const r of searchResults) {
      const list = map.get(r.path);
      if (list) list.push(r); else map.set(r.path, [r]);
    }
    return [...map.entries()];
  }, [searchResults]);

  // Flat list of keyboard-navigable match rows, in render order and skipping
  // collapsed groups — the index into this array is `activeResultIndex`.
  const navRows = useMemo(() => {
    const rows: { path: string; line?: number }[] = [];
    for (const [path, matches] of groupedSearch) {
      if (collapsedSearchGroups.has(path)) continue;
      for (const m of matches) rows.push({ path, line: m.line });
    }
    return rows;
  }, [groupedSearch, collapsedSearchGroups]);

  // Keep the active index in range as results/collapse change; scroll into view.
  useEffect(() => {
    setActiveResultIndex((i) => (i >= navRows.length ? navRows.length - 1 : i));
  }, [navRows.length]);
  useEffect(() => {
    if (activeResultIndex >= 0) activeResultRef.current?.scrollIntoView({ block: "nearest" });
  }, [activeResultIndex]);

  const pushSearchHistory = useCallback((q: string) => {
    const term = q.trim();
    if (!term) return;
    setSearchHistory((prev) => {
      const next = [term, ...prev.filter((h) => h !== term)].slice(0, 8);
      try { localStorage.setItem("beehub:file-search-history", JSON.stringify(next)); } catch { /* ignore */ }
      return next;
    });
  }, []);

  const toggleSearchGroup = useCallback((path: string) => {
    setCollapsedSearchGroups((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path); else next.add(path);
      return next;
    });
  }, []);

  const handleSearchKeyDown = useCallback((e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Escape") {
      if (searchQuery) { setSearchQuery(""); setActiveResultIndex(-1); }
      else searchRef.current?.blur();
      setHistoryOpen(false);
      return;
    }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setHistoryOpen(false);
      setActiveResultIndex((i) => Math.min(navRows.length - 1, i + 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActiveResultIndex((i) => Math.max(-1, i - 1));
    } else if (e.key === "Enter") {
      e.preventDefault();
      const row = navRows[activeResultIndex >= 0 ? activeResultIndex : 0];
      if (row) { pushSearchHistory(searchQuery); handleSelectSearchResult(row.path, row.line); }
      setHistoryOpen(false);
    }
  }, [navRows, activeResultIndex, searchQuery, pushSearchHistory, handleSelectSearchResult]);

  // Global shortcuts (Cmd/Ctrl+P Quick Open) live in SessionWorkspace so they
  // work even when the Files panel is closed.

  const refreshButton = (
    <Button
      variant="ghost"
      size="sm"
      onClick={handleRefresh}
      disabled={refreshing || loadingFile || rootLoading || saving}
      className="h-7 w-7 p-0"
      title="Refresh files and current file"
      aria-label="Refresh files and current file"
    >
      <RefreshCw className={cn("h-3.5 w-3.5", refreshing && "animate-spin")} />
    </Button>
  );

  const showTreeButton = (
    <button
      onClick={() => setTreeCollapsed(false)}
      className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors shrink-0"
      title="Show file tree"
      aria-label="Show file tree"
    >
      <PanelLeftOpen className="h-3.5 w-3.5" />
    </button>
  );

  return (
    <div ref={containerRef} className="flex h-full">
      {refreshTarget && createPortal(refreshButton, refreshTarget)}
      {/* File tree sidebar */}
      {!treeCollapsed && (
      <div className="shrink-0 border-r flex flex-col" style={{ width: fileTreeWidth }}>
        <div className="flex items-center justify-between px-3 py-1.5 border-b shrink-0">
          <span className="text-xs text-muted-foreground font-medium truncate min-w-0 mr-2" title={projectName}>{projectName}</span>
          <div className="flex items-center gap-0.5 shrink-0">
            <button
              onClick={onOpenQuickOpen}
              className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
              title="Go to file (Cmd/Ctrl+P)"
              aria-label="Go to file"
            >
              <ListFilter className="h-3.5 w-3.5" />
            </button>
            {!selectedRepositoryFile && (
              <button
                onClick={handlePickUpload}
                disabled={uploading}
                className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors disabled:opacity-50"
                title={uploadTargetDir ? `Upload into ${uploadTargetDir}` : "Upload to project root"}
              >
                {uploading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Upload className="h-3.5 w-3.5" />}
              </button>
            )}
            <input
              ref={uploadInputRef}
              type="file"
              className="hidden"
              onChange={handleUploadInputChange}
            />
            {refreshTarget === undefined && refreshButton}
            <button
              onClick={() => setTreeCollapsed(true)}
              className="p-1 rounded hover:bg-accent text-muted-foreground/50 hover:text-muted-foreground transition-colors"
              title="Hide file tree"
            >
              <PanelLeftClose className="h-3.5 w-3.5" />
            </button>
          </div>
        </div>

        {/* Persistent content-search bar (filename lookup is Cmd/Ctrl+P) */}
        <div className="border-b px-2 py-1.5 relative">
          <div className="flex items-center gap-1 bg-muted/50 rounded px-1.5">
            <Search className="h-3 w-3 text-muted-foreground/50 shrink-0" />
            <input
              ref={searchRef}
              value={searchQuery}
              onChange={(e) => { setSearchQuery(e.target.value); setActiveResultIndex(-1); }}
              onKeyDown={handleSearchKeyDown}
              onFocus={() => { if (!searchQuery) setHistoryOpen(true); }}
              onBlur={() => setTimeout(() => setHistoryOpen(false), 150)}
              placeholder="Search text  (⌘/Ctrl+P for files)"
              className="flex-1 bg-transparent text-xs py-1 outline-none placeholder:text-muted-foreground/40"
            />
            <button
              onClick={() => setSearchCaseSensitive((v) => !v)}
              className={cn(
                "shrink-0 p-0.5 rounded transition-colors",
                searchCaseSensitive
                  ? "bg-primary/15 text-primary"
                  : "text-muted-foreground/40 hover:text-muted-foreground"
              )}
              title="Match case"
            >
              <CaseSensitive className="h-3.5 w-3.5" />
            </button>
            {searchQuery && (
              <button onClick={() => { setSearchQuery(""); setActiveResultIndex(-1); searchRef.current?.focus(); }} className="shrink-0 text-muted-foreground/40 hover:text-muted-foreground">
                <X className="h-3 w-3" />
              </button>
            )}
          </div>
          {historyOpen && !searchQuery && searchHistory.length > 0 && (
            <div className="absolute left-2 right-2 top-full z-10 mt-0.5 rounded border bg-background shadow-lg py-1">
              <div className="px-2 pb-0.5 text-[10px] uppercase tracking-wide text-muted-foreground/40">Recent</div>
              {searchHistory.map((h) => (
                <button
                  key={h}
                  onMouseDown={(e) => { e.preventDefault(); setSearchQuery(h); setHistoryOpen(false); }}
                  className="flex w-full items-center gap-1.5 px-2 py-1 text-xs text-left hover:bg-accent/50"
                >
                  <Search className="h-3 w-3 text-muted-foreground/40 shrink-0" />
                  <span className="truncate">{h}</span>
                </button>
              ))}
            </div>
          )}
        </div>

        {/* Tree or search results */}
        <div
          ref={treeScrollRef}
          className={cn(
            "flex-1 overflow-y-auto",
            rootDragOver && "ring-1 ring-inset ring-primary/60 bg-primary/5"
          )}
          onDragOver={handleRootDragOver}
          onDragLeave={handleRootDragLeave}
          onDrop={handleRootDrop}
        >
          {searchQuery.trim() ? (
            <SearchResults
              grouped={groupedSearch}
              loading={searchLoading}
              query={searchQuery.trim()}
              caseSensitive={searchCaseSensitive}
              collapsed={collapsedSearchGroups}
              onToggleGroup={toggleSearchGroup}
              activeIndex={activeResultIndex}
              activeRef={activeResultRef}
              onSelect={(path, line) => { pushSearchHistory(searchQuery); handleSelectSearchResult(path, line); }}
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

      {/* Tree/content resize handle */}
      {!treeCollapsed && (
      <div
        className="w-1 cursor-col-resize hover:bg-primary/20 active:bg-primary/40 transition-colors shrink-0"
        onMouseDown={handleTreeResizeMouseDown}
      />
      )}

      {/* File content viewer */}
      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
        {selectedPath ? (
          <>
            <div className="flex items-center gap-2 px-3 py-1.5 border-b shrink-0 text-xs">
              {treeCollapsed && showTreeButton}
              <File className="h-3.5 w-3.5 text-muted-foreground/50 shrink-0" />
              {selectedRepositoryFile && (
                <span className="shrink-0 rounded border bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground" title={`Repository: ${selectedRepositoryFile.repository_display_path}`}>
                  {selectedRepositoryFile.repository_name}
                </span>
              )}
              <span className="flex-1 min-w-0 font-mono truncate text-muted-foreground" title={selectedPath}>{selectedPath}</span>
              {sourcePreviewAvailable && (
                <div className="flex items-center gap-1 shrink-0" role="group" aria-label="File view mode">
                  <button
                    onClick={() => markdownPreviewAvailable ? setMarkdownMode("source") : setImageSource(true)}
                    className={cn(
                      "h-6 px-2 rounded border text-[11px] inline-flex items-center gap-1 transition-colors",
                      !showingPreview
                        ? "bg-accent text-accent-foreground border-border"
                        : "text-muted-foreground hover:text-foreground hover:bg-accent/50 border-transparent"
                    )}
                    title={markdownPreviewAvailable ? "View source markdown" : "View source"}
                    aria-label="View source"
                    aria-pressed={!showingPreview}
                  >
                    <Code2 className="h-3 w-3" />
                  </button>
                  <button
                    onClick={() => markdownPreviewAvailable ? setMarkdownMode("rendered") : setImageSource(false)}
                    className={cn(
                      "h-6 px-2 rounded border text-[11px] inline-flex items-center gap-1 transition-colors",
                      showingPreview
                        ? "bg-accent text-accent-foreground border-border"
                        : "text-muted-foreground hover:text-foreground hover:bg-accent/50 border-transparent"
                    )}
                    title={markdownPreviewAvailable ? "View rendered markdown" : "View image"}
                    aria-label="View preview"
                    aria-pressed={showingPreview}
                  >
                    <Eye className="h-3 w-3" />
                  </button>
                </div>
              )}
              {fileContent && !fileContent.binary && (
                <button
                  onClick={handleSave}
                  disabled={!isDirty || saving}
                  className={cn(
                    "h-6 px-2 rounded border text-[11px] inline-flex items-center gap-1 transition-colors shrink-0",
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
            </div>

            {loadingFile ? (
              <div className="flex-1 flex items-center justify-center">
                <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
              </div>
            ) : showingImage ? (
              <div className="flex-1 min-h-0 overflow-auto flex items-center justify-center p-4">
                {imageError ? (
                  <p role="alert" className="text-sm text-muted-foreground">Unable to display image</p>
                ) : (
                  <img
                    key={`${browserContextKey}:${selectedPath}:${fileRequestRef.current}:${imageRevision}`}
                    src={`${filesApi.serveUrl(projectId, selectedPath, sessionId, selectedRepositoryFile?.repository_id, !selectedRepositoryFile && fileNavigation?.path === selectedPath && fileNavigation.safeLink)}&preview_request=${fileRequestRef.current}&refresh=${imageRevision}`}
                    alt={selectedPath}
                    onError={() => setImageError(true)}
                    className="max-w-full max-h-full object-contain"
                  />
                )}
              </div>
            ) : fileContent?.binary ? (
              <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
                Binary file ({formatSize(fileContent.size)})
              </div>
            ) : fileContent ? (
              <div className="flex-1 min-h-0 relative">
                {markdownPreviewAvailable && markdownMode === "rendered" ? (
                  <div ref={fileContentSelectableRef} className="h-full">
                    <MarkdownRenderer
                      content={fileContent.content}
                      resolveImageSrc={resolveMarkdownImageSrc}
                      className="h-full overflow-auto px-4 py-3 markdown-content text-sm"
                    />
                  </div>
                ) : (
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
                )}
              </div>
            ) : (
              <div role="alert" className="flex-1 flex flex-col items-center justify-center gap-2 px-6 text-center text-sm text-muted-foreground">
                <p>⚠️ Failed to load file</p>
                {fileLoadError && <p className="text-xs break-words max-w-full">{fileLoadError}</p>}
              </div>
            )}
          </>
        ) : (
          <>
            {treeCollapsed && <div className="px-3 py-1.5 border-b shrink-0">{showTreeButton}</div>}
            <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
              Select a file to view
            </div>
          </>
        )}
      </div>

      <Dialog
        open={reloadTarget !== null && reloadTarget.context === browserContextKey && reloadTarget.request === fileRequestRef.current}
        onOpenChange={(open) => { if (!open) setReloadTarget(null); }}
      >
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>Discard unsaved changes?</DialogTitle>
            <DialogDescription>
              Reloading this file will discard your unsaved changes and load the latest content from disk.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <button onClick={() => setReloadTarget(null)}
              className="h-8 px-3 rounded text-xs hover:bg-accent text-muted-foreground hover:text-foreground transition-colors">
              Keep editing
            </button>
            <button onClick={handleConfirmReload} disabled={saving}
              className="h-8 px-3 rounded text-xs bg-destructive text-destructive-foreground hover:opacity-90 disabled:opacity-50">
              Discard and reload
            </button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

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
