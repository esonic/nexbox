import { useState, useEffect, useCallback, useMemo, useRef } from "react";
import { useTranslation } from "react-i18next";
import {
  ChevronRight,
  ChevronDown,
  File,
  Folder,
  FolderOpen,
  Loader2,
  RefreshCw,
  Search,
  X,
  FileText,
  FileCode,
  Upload,
  Download,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { filesApi, type FileEntry, type FileContentResponse, type FileSearchResult } from "@/lib/api";
import { getLangFromPath, ensureLangRegistered, highlightCode } from "@/lib/highlight";
import { useFileBrowserStore } from "@/stores/fileBrowser";

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// VSCode-style: compact single-child directory chains into one display node.
interface CompactEntry {
  displayName: string;
  entry: FileEntry;
  depth: number;
}

function compactTree(entries: FileEntry[]): FileEntry[] {
  return entries.map((e) => {
    if (!e.is_dir || !e.children) return e;
    let current = e;
    let name = e.name;
    while (
      current.is_dir &&
      current.children &&
      current.children.length === 1 &&
      current.children[0].is_dir
    ) {
      current = current.children[0];
      name += "/" + current.name;
    }
    return {
      ...current,
      name,
      children: current.children ? compactTree(current.children) : undefined,
    };
  });
}

// ── File tree item ──

function FileTreeItem({
  entry,
  depth,
  expanded,
  onToggle,
  selectedPath,
  onSelect,
}: {
  entry: FileEntry;
  depth: number;
  expanded: Set<string>;
  onToggle: (path: string) => void;
  selectedPath: string | null;
  onSelect: (path: string) => void;
}) {
  const isExpanded = expanded.has(entry.path);
  const isSelected = selectedPath === entry.path;

  return (
    <>
      <div
        className={cn(
          "flex items-center gap-1 py-[3px] pr-2 cursor-pointer text-xs hover:bg-accent/50 transition-colors",
          isSelected && "bg-accent"
        )}
        style={{ paddingLeft: depth * 16 + 8 }}
        onClick={() => {
          if (entry.is_dir) {
            onToggle(entry.path);
          } else {
            onSelect(entry.path);
          }
        }}
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
        <span className="truncate min-w-0">{entry.name}</span>
      </div>
      {entry.is_dir && isExpanded && entry.children?.map((child) => (
        <FileTreeItem
          key={child.path}
          entry={child}
          depth={depth + 1}
          expanded={expanded}
          onToggle={onToggle}
          selectedPath={selectedPath}
          onSelect={onSelect}
        />
      ))}
    </>
  );
}

// ── Search results ──

function SearchResults({
  results,
  loading,
  searchType,
  onSelect,
}: {
  results: FileSearchResult[];
  loading: boolean;
  searchType: "name" | "content";
  onSelect: (path: string) => void;
}) {
  const { t } = useTranslation();
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
        {t("files.no_results")}
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
                className="px-3 py-1 text-[11px] text-muted-foreground hover:bg-accent/30 cursor-pointer truncate font-mono"
                onClick={() => onSelect(m.path)}
              >
                {m.content}
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

// ── Code viewer with highlight.js ──

function CodeViewer({ content, filepath }: { content: string; filepath: string }) {
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

  return (
    <div className="overflow-auto h-full">
      <table className="w-full text-xs font-mono leading-[1.4]">
        <tbody>
          {lines.map((line, i) => (
            <tr key={i} className="hover:bg-accent/30">
              <td
                className="px-3 py-0 text-right select-none text-muted-foreground/40 border-r border-border/20 align-top"
                style={{ minWidth: gutterWidth * 8 + 24 }}
              >
                {i + 1}
              </td>
              <td className="px-3 py-0 whitespace-pre-wrap break-all">
                {highlightedLines?.[i] ? (
                  <span dangerouslySetInnerHTML={{ __html: highlightedLines[i] }} />
                ) : (
                  <span>{line}</span>
                )}
              </td>
            </tr>
          ))}
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
  const { t } = useTranslation();
  const [tree, setTree] = useState<FileEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [fileContent, setFileContent] = useState<FileContentResponse | null>(null);
  const [loadingFile, setLoadingFile] = useState(false);

  // Persisted state from store
  const {
    ownerProjectId, expanded, selectedPath,
    setExpanded, setSelectedPath, toggleExpand, resetForProject,
  } = useFileBrowserStore();

  // Reset store state when projectId changes (stale expanded/selectedPath from a
  // different project must not bleed into the new project view).
  useEffect(() => {
    if (ownerProjectId !== projectId) {
      resetForProject(projectId);
    }
  }, [projectId, ownerProjectId, resetForProject]);

  // Search state
  const [showSearch, setShowSearch] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [searchType, setSearchType] = useState<"name" | "content">("name");
  const [searchResults, setSearchResults] = useState<FileSearchResult[]>([]);
  const [searchLoading, setSearchLoading] = useState(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const searchTimerRef = useRef<ReturnType<typeof setTimeout>>(undefined);

  const loadTree = useCallback(async () => {
    setLoading(true);
    try {
      const data = await filesApi.tree(projectId, sessionId);
      setTree(data || []);
    } catch (e) {
      console.error("Failed to load file tree:", e);
    } finally {
      setLoading(false);
    }
  }, [projectId, sessionId]);

  useEffect(() => {
    loadTree();
  }, [loadTree]);


  const compactedTree = useMemo(() => compactTree(tree), [tree]);

  const handleSelectFile = useCallback(async (path: string) => {
    setSelectedPath(path);
    setShowDeleteConfirm(false);
    setLoadingFile(true);
    try {
      const data = await filesApi.content(projectId, path, sessionId);
      setFileContent(data);
    } catch (e) {
      console.error("Failed to load file:", e);
      setFileContent(null);
    } finally {
      setLoadingFile(false);
    }
  }, [projectId, setSelectedPath]);

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
      try {
        const results = await filesApi.search(projectId, searchQuery.trim(), searchType, sessionId);
        setSearchResults(results || []);
      } catch (e) {
        console.error("Search failed:", e);
        setSearchResults([]);
      } finally {
        setSearchLoading(false);
      }
    }, 300);
    return () => clearTimeout(searchTimerRef.current);
  }, [searchQuery, searchType, projectId]);

  const handleToggleSearch = useCallback(() => {
    setShowSearch((prev) => {
      if (!prev) setTimeout(() => searchRef.current?.focus(), 50);
      return !prev;
    });
    setSearchQuery("");
    setSearchResults([]);
  }, []);

  // Upload
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = useState(false);
  const [isDragging, setIsDragging] = useState(false);

  const handleUpload = useCallback(async (files: FileList | null) => {
    if (!files || files.length === 0) return;
    setUploading(true);
    try {
      for (const file of Array.from(files)) {
        // Upload to the currently selected directory, or root
        const dir = selectedPath
          ? (tree.some((e) => e.path === selectedPath && e.is_dir) ? selectedPath : selectedPath.includes("/") ? selectedPath.substring(0, selectedPath.lastIndexOf("/")) : ".")
          : ".";
        await filesApi.upload(projectId, file, dir, sessionId);
      }
      toast.success(`Uploaded ${files.length} file${files.length > 1 ? "s" : ""}`);
      loadTree();
    } catch (e: any) {
      toast.error("Upload failed", { description: e.message });
    } finally {
      setUploading(false);
      if (fileInputRef.current) fileInputRef.current.value = "";
    }
  }, [projectId, sessionId, selectedPath, tree, loadTree]);

  const handleSidebarDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(true);
  }, []);

  const handleSidebarDragLeave = useCallback((e: React.DragEvent) => {
    if (!e.currentTarget.contains(e.relatedTarget as Node)) {
      setIsDragging(false);
    }
  }, []);

  const handleSidebarDrop = useCallback(async (e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(false);
    await handleUpload(e.dataTransfer.files);
  }, [handleUpload]);

  const handleDownload = useCallback(() => {
    if (!selectedPath) return;
    const url = filesApi.downloadUrl(projectId, selectedPath, sessionId);
    window.open(url, "_blank");
  }, [projectId, sessionId, selectedPath]);

  // Delete file
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false);
  const [deleting, setDeleting] = useState(false);

  // Dismiss delete confirmation on click outside
  useEffect(() => {
    if (!showDeleteConfirm) return;
    const handler = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (target.closest("[data-delete-confirm]")) return;
      setShowDeleteConfirm(false);
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [showDeleteConfirm]);

  const handleDelete = useCallback(async () => {
    if (!selectedPath) return;
    setDeleting(true);
    try {
      await filesApi.delete(projectId, selectedPath, sessionId);
      toast.success("File deleted");
      setSelectedPath(null);
      setFileContent(null);
      setShowDeleteConfirm(false);
      loadTree();
    } catch (e: any) {
      toast.error("Delete failed", { description: e.message });
    } finally {
      setDeleting(false);
    }
  }, [projectId, sessionId, selectedPath, setSelectedPath, loadTree]);

  return (
    <div className="flex h-full">
      {/* File tree sidebar */}
      <div
        className={cn("w-56 shrink-0 border-r flex flex-col", isDragging && "ring-2 ring-inset ring-primary/50 bg-primary/5")}
        onDragOver={handleSidebarDragOver}
        onDragLeave={handleSidebarDragLeave}
        onDrop={handleSidebarDrop}
      >
        <div className="flex items-center justify-between px-3 py-1.5 border-b shrink-0">
          <span className="text-xs text-muted-foreground font-medium">{t("files.title")}</span>
          <div className="flex items-center gap-0.5">
            <button
              onClick={handleToggleSearch}
              className={cn(
                "p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors",
                showSearch && "bg-accent text-foreground"
              )}
              title={t("files.search")}
            >
              <Search className="h-3.5 w-3.5" />
            </button>
            <button
              onClick={() => fileInputRef.current?.click()}
              disabled={uploading}
              className="p-1 rounded hover:bg-accent text-muted-foreground/50 hover:text-muted-foreground transition-colors"
              title={t("files.upload")}
            >
              {uploading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Upload className="h-3.5 w-3.5" />}
            </button>
            <input
              ref={fileInputRef}
              type="file"
              multiple
              className="hidden"
              onChange={(e) => handleUpload(e.target.files)}
            />
            <button
              onClick={loadTree}
              className="p-1 rounded hover:bg-accent text-muted-foreground/50 hover:text-muted-foreground transition-colors"
              title={t("files.refresh")}
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
                placeholder={searchType === "name" ? t("files.search_name_placeholder") : t("files.search_content_placeholder")}
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
                {t("files.search_name")}
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
                {t("files.search_content")}
              </button>
            </div>
          </div>
        )}

        {/* Tree or search results */}
        <div className="flex-1 overflow-y-auto">
          {showSearch && searchQuery ? (
            <SearchResults
              results={searchResults}
              loading={searchLoading}
              searchType={searchType}
              onSelect={handleSelectFile}
            />
          ) : loading ? (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
            </div>
          ) : tree.length === 0 ? (
            <div className="text-xs text-muted-foreground text-center py-8">
              {t("files.no_files")}
            </div>
          ) : (
            compactedTree.map((entry) => (
              <FileTreeItem
                key={entry.path}
                entry={entry}
                depth={0}
                expanded={expanded}
                onToggle={toggleExpand}
                selectedPath={selectedPath}
                onSelect={handleSelectFile}
              />
            ))
          )}
        </div>
      </div>

      {/* File content viewer */}
      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
        {selectedPath ? (
          <>
            <div className="flex items-center gap-2 px-3 py-1.5 border-b shrink-0 text-xs">
              <File className="h-3.5 w-3.5 text-muted-foreground/50 shrink-0" />
              <span className="font-mono truncate text-muted-foreground">{selectedPath}</span>
              <div className="ml-auto flex items-center gap-1.5 shrink-0">
                {fileContent && (
                  <span className="text-muted-foreground/40">
                    {formatSize(fileContent.size)}
                  </span>
                )}
                <button
                  onClick={handleDownload}
                  className="p-0.5 rounded hover:bg-accent text-muted-foreground/50 hover:text-muted-foreground transition-colors"
                  title={t("files.download")}
                >
                  <Download className="h-3.5 w-3.5" />
                </button>
                <div className="relative">
                  <button
                    onClick={() => setShowDeleteConfirm(true)}
                    className="p-0.5 rounded text-muted-foreground/50 hover:text-destructive hover:bg-destructive/10 transition-colors"
                    title={t("files.delete")}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </button>
                  {showDeleteConfirm && (
                    <div data-delete-confirm className="absolute right-0 top-full mt-1 z-50 bg-popover border rounded-md shadow-md p-2 flex items-center gap-1.5 whitespace-nowrap">
                      <span className="text-xs text-muted-foreground">{t("files.delete_confirm")}</span>
                      <button
                        onClick={handleDelete}
                        disabled={deleting}
                        className="px-2 py-0.5 text-xs bg-destructive text-destructive-foreground rounded hover:bg-destructive/90"
                      >
                        {deleting ? "..." : t("files.delete_btn")}
                      </button>
                      <button
                        onClick={() => setShowDeleteConfirm(false)}
                        className="px-2 py-0.5 text-xs bg-muted text-muted-foreground rounded hover:bg-accent"
                      >
                        {t("files.cancel")}
                      </button>
                    </div>
                  )}
                </div>
              </div>
            </div>

            {loadingFile ? (
              <div className="flex-1 flex items-center justify-center">
                <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
              </div>
            ) : fileContent?.binary ? (
              <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
                {t("files.binary_file", { size: formatSize(fileContent.size) })}
              </div>
            ) : fileContent ? (
              <div className="flex-1 min-h-0">
                <CodeViewer content={fileContent.content} filepath={selectedPath} />
              </div>
            ) : (
              <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
                {t("files.load_failed")}
              </div>
            )}
          </>
        ) : (
          <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
            {t("files.select_file")}
          </div>
        )}
      </div>
    </div>
  );
}
