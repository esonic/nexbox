import { useState, useEffect, useCallback, useMemo, useRef, lazy, Suspense } from "react";
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
  Eye,
  Code2,
  Upload,
  Download,
  Trash2,
  Save,
} from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { filesApi, type FileEntry, type FileContentResponse, type FileSearchResult } from "@/lib/api";
import { useFileBrowserStore } from "@/stores/fileBrowser";
import { MarkdownRenderer, isMarkdownPath } from "@/components/markdown/MarkdownRenderer";
import type { editor as MonacoEditorNS } from "monaco-editor";

const MonacoEditor = lazy(() => import("@monaco-editor/react"));

// ── Helpers ──

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function getMonacoLanguage(filepath: string): string {
  const name = filepath.split("/").pop()?.toLowerCase() ?? "";
  if (name === "dockerfile") return "dockerfile";
  if (name === "makefile" || name === "gnumakefile") return "makefile";
  const ext = name.split(".").pop() ?? "";
  const map: Record<string, string> = {
    ts: "typescript", tsx: "typescript",
    js: "javascript", jsx: "javascript", mjs: "javascript", cjs: "javascript",
    py: "python", pyw: "python",
    go: "go",
    java: "java",
    rs: "rust",
    c: "c", h: "c",
    cpp: "cpp", cc: "cpp", cxx: "cpp", hpp: "cpp", hxx: "cpp",
    cs: "csharp",
    html: "html", htm: "html",
    css: "css", scss: "scss", sass: "sass", less: "less",
    json: "json", jsonc: "json",
    yaml: "yaml", yml: "yaml",
    md: "markdown", mdx: "markdown",
    sh: "shell", bash: "shell", zsh: "shell", fish: "shell",
    sql: "sql",
    php: "php",
    rb: "ruby",
    kt: "kotlin", kts: "kotlin",
    swift: "swift",
    r: "r",
    xml: "xml", svg: "xml",
    toml: "ini",
    proto: "proto",
    lua: "lua",
    vim: "ini",
    tf: "hcl", hcl: "hcl",
  };
  return map[ext] ?? "plaintext";
}

function getMonacoTheme(): string {
  return document.body.classList.contains("dark") ? "vs-dark" : "vs";
}

// VSCode-style: compact single-child directory chains into one display node.
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
          if (entry.is_dir) onToggle(entry.path);
          else onSelect(entry.path);
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
  const [markdownMode, setMarkdownMode] = useState<"source" | "rendered">("source");

  // Persisted state from store
  const {
    ownerProjectId, expanded, selectedPath,
    setSelectedPath, toggleExpand, resetForProject,
  } = useFileBrowserStore();

  useEffect(() => {
    if (ownerProjectId !== projectId) resetForProject(projectId);
  }, [projectId, ownerProjectId, resetForProject]);

  // Sidebar search state
  const [showSearch, setShowSearch] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [searchType, setSearchType] = useState<"name" | "content">("name");
  const [searchResults, setSearchResults] = useState<FileSearchResult[]>([]);
  const [searchLoading, setSearchLoading] = useState(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const searchTimerRef = useRef<ReturnType<typeof setTimeout>>(undefined);

  // Editor state
  const [editorContent, setEditorContent] = useState<string>("");
  const [isDirty, setIsDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const editorRef = useRef<MonacoEditorNS.IStandaloneCodeEditor | null>(null);
  const [monacoTheme, setMonacoTheme] = useState(getMonacoTheme);

  // Sync Monaco theme when the app's dark/light class changes on document.body
  useEffect(() => {
    const observer = new MutationObserver(() => setMonacoTheme(getMonacoTheme()));
    observer.observe(document.body, { attributes: true, attributeFilter: ["class"] });
    return () => observer.disconnect();
  }, []);

  const markdownPreviewAvailable = !!selectedPath && !!fileContent && !fileContent.binary && isMarkdownPath(selectedPath);
  useEffect(() => {
    if (!markdownPreviewAvailable && markdownMode !== "source") setMarkdownMode("source");
  }, [markdownPreviewAvailable, markdownMode]);

  const compactedTree = useMemo(() => compactTree(tree), [tree]);

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

  useEffect(() => { loadTree(); }, [loadTree]);

  // Load file content whenever selectedPath changes — also handles remount after
  // panel switch (selectedPath is persisted in the store but fileContent is local).
  useEffect(() => {
    if (!selectedPath) {
      setFileContent(null);
      setEditorContent("");
      setIsDirty(false);
      return;
    }
    let cancelled = false;
    setLoadingFile(true);
    setIsDirty(false);
    filesApi.content(projectId, selectedPath, sessionId)
      .then((data) => {
        if (!cancelled) {
          setFileContent(data);
          setEditorContent(data.binary ? "" : data.content);
        }
      })
      .catch(() => {
        if (!cancelled) { setFileContent(null); setEditorContent(""); }
      })
      .finally(() => { if (!cancelled) setLoadingFile(false); });
    return () => { cancelled = true; };
  }, [selectedPath, projectId, sessionId]);

  const handleSelectFile = useCallback((path: string) => {
    setSelectedPath(path);
    setShowDeleteConfirm(false);
  }, [setSelectedPath]);

  const handleSave = useCallback(async () => {
    if (!selectedPath || !isDirty || saving) return;
    setSaving(true);
    try {
      await filesApi.writeContent(projectId, selectedPath, editorContent, sessionId);
      // Update the saved baseline so dirty tracking resets
      setFileContent((prev) => prev ? { ...prev, content: editorContent, size: new Blob([editorContent]).size } : prev);
      setIsDirty(false);
      toast.success("Saved");
    } catch (e: any) {
      toast.error("Save failed", { description: e.message });
    } finally {
      setSaving(false);
    }
  }, [projectId, sessionId, selectedPath, editorContent, isDirty, saving]);

  // Debounced sidebar search
  useEffect(() => {
    if (!searchQuery.trim()) { setSearchResults([]); setSearchLoading(false); return; }
    setSearchLoading(true);
    clearTimeout(searchTimerRef.current);
    searchTimerRef.current = setTimeout(async () => {
      try {
        const results = await filesApi.search(projectId, searchQuery.trim(), searchType, sessionId);
        setSearchResults(results || []);
      } catch {
        setSearchResults([]);
      } finally {
        setSearchLoading(false);
      }
    }, 300);
    return () => clearTimeout(searchTimerRef.current);
  }, [searchQuery, searchType, projectId, sessionId]);

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
        const dir = selectedPath
          ? (tree.some((e) => e.path === selectedPath && e.is_dir)
            ? selectedPath
            : selectedPath.includes("/") ? selectedPath.substring(0, selectedPath.lastIndexOf("/")) : ".")
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

  const handleDownload = useCallback(() => {
    if (!selectedPath) return;
    window.open(filesApi.downloadUrl(projectId, selectedPath, sessionId), "_blank");
  }, [projectId, sessionId, selectedPath]);

  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false);
  const [deleting, setDeleting] = useState(false);

  useEffect(() => {
    if (!showDeleteConfirm) return;
    const handler = (e: MouseEvent) => {
      if (!(e.target as HTMLElement).closest("[data-delete-confirm]")) setShowDeleteConfirm(false);
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
      setEditorContent("");
      setIsDirty(false);
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
        onDragOver={(e) => { e.preventDefault(); setIsDragging(true); }}
        onDragLeave={(e) => { if (!e.currentTarget.contains(e.relatedTarget as Node)) setIsDragging(false); }}
        onDrop={async (e) => { e.preventDefault(); setIsDragging(false); await handleUpload(e.dataTransfer.files); }}
      >
        <div className="flex items-center justify-between px-3 py-1.5 border-b shrink-0">
          <span className="text-xs text-muted-foreground font-medium">{t("files.title")}</span>
          <div className="flex items-center gap-0.5">
            <button
              onClick={handleToggleSearch}
              className={cn("p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors", showSearch && "bg-accent text-foreground")}
              title={t("files.search")}
            >
              <Search className="h-3.5 w-3.5" />
            </button>
            <button
              onClick={() => fileInputRef.current?.click()}
              disabled={uploading}
              className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
              title={t("files.upload")}
            >
              {uploading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Upload className="h-3.5 w-3.5" />}
            </button>
            <input ref={fileInputRef} type="file" multiple className="hidden" onChange={(e) => handleUpload(e.target.files)} />
            <button onClick={loadTree} className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors" title={t("files.refresh")}>
              <RefreshCw className="h-3 w-3" />
            </button>
          </div>
        </div>

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
              {(["name", "content"] as const).map((type) => (
                <button
                  key={type}
                  onClick={() => setSearchType(type)}
                  className={cn(
                    "text-[10px] px-1.5 py-0.5 rounded transition-colors",
                    searchType === type ? "bg-primary/10 text-primary font-medium" : "text-muted-foreground/60 hover:text-muted-foreground"
                  )}
                >
                  {t(`files.search_${type}`)}
                </button>
              ))}
            </div>
          </div>
        )}

        <div className="flex-1 overflow-y-auto">
          {showSearch && searchQuery ? (
            <SearchResults results={searchResults} loading={searchLoading} searchType={searchType} onSelect={handleSelectFile} />
          ) : loading ? (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
            </div>
          ) : tree.length === 0 ? (
            <div className="text-xs text-muted-foreground text-center py-8">{t("files.no_files")}</div>
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

      {/* File content viewer / editor */}
      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
        {selectedPath ? (
          <>
            {/* Header bar */}
            <div className="flex items-center gap-2 px-3 py-1.5 border-b shrink-0 text-xs">
              <File className="h-3.5 w-3.5 text-muted-foreground/50 shrink-0" />
              <span className="font-mono truncate text-muted-foreground">{selectedPath}</span>
              {isDirty && <span className="w-1.5 h-1.5 rounded-full bg-primary shrink-0" title="Unsaved changes" />}
              <div className="ml-auto flex items-center gap-1.5 shrink-0">
                {markdownPreviewAvailable && (
                  <div className="flex items-center gap-1">
                    {(["source", "rendered"] as const).map((mode) => (
                      <button
                        key={mode}
                        onClick={() => setMarkdownMode(mode)}
                        className={cn(
                          "h-6 px-2 rounded border text-[11px] inline-flex items-center gap-1 transition-colors",
                          markdownMode === mode
                            ? "bg-accent text-accent-foreground border-border"
                            : "text-muted-foreground hover:text-foreground hover:bg-accent/50 border-transparent"
                        )}
                      >
                        {mode === "source" ? <><Code2 className="h-3 w-3" />Source</> : <><Eye className="h-3 w-3" />Rendered</>}
                      </button>
                    ))}
                  </div>
                )}
                {fileContent && !fileContent.binary && (
                  <button
                    onClick={handleSave}
                    disabled={!isDirty || saving}
                    className={cn(
                      "h-6 px-2 rounded border text-[11px] inline-flex items-center gap-1 transition-colors",
                      isDirty
                        ? "bg-primary text-primary-foreground border-primary hover:bg-primary/90"
                        : "text-muted-foreground/40 border-transparent cursor-default"
                    )}
                    title="Save (Ctrl+S)"
                  >
                    {saving ? <Loader2 className="h-3 w-3 animate-spin" /> : <Save className="h-3 w-3" />}
                    Save
                  </button>
                )}
                {fileContent && (
                  <span className="text-muted-foreground/70">{formatSize(fileContent.size)}</span>
                )}
                <button onClick={handleDownload} className="p-0.5 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors" title={t("files.download")}>
                  <Download className="h-3.5 w-3.5" />
                </button>
                <div className="relative">
                  <button onClick={() => setShowDeleteConfirm(true)} className="p-0.5 rounded text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors" title={t("files.delete")}>
                    <Trash2 className="h-3.5 w-3.5" />
                  </button>
                  {showDeleteConfirm && (
                    <div data-delete-confirm className="absolute right-0 top-full mt-1 z-50 bg-popover border rounded-md shadow-md p-2 flex items-center gap-1.5 whitespace-nowrap">
                      <span className="text-xs text-muted-foreground">{t("files.delete_confirm")}</span>
                      <button onClick={handleDelete} disabled={deleting} className="px-2 py-0.5 text-xs bg-destructive text-destructive-foreground rounded hover:bg-destructive/90">
                        {deleting ? "..." : t("files.delete_btn")}
                      </button>
                      <button onClick={() => setShowDeleteConfirm(false)} className="px-2 py-0.5 text-xs bg-muted text-muted-foreground rounded hover:bg-accent">
                        {t("files.cancel")}
                      </button>
                    </div>
                  )}
                </div>
              </div>
            </div>

            {/* Content area */}
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
                {markdownPreviewAvailable && markdownMode === "rendered" ? (
                  <MarkdownRenderer
                    content={editorContent}
                    className="h-full overflow-auto px-4 py-3 markdown-content text-sm"
                  />
                ) : (
                  <Suspense fallback={<div className="flex-1 flex items-center justify-center"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div>}>
                    <MonacoEditor
                      height="100%"
                      language={getMonacoLanguage(selectedPath)}
                      theme={monacoTheme}
                      value={editorContent}
                      onChange={(value) => {
                        const v = value ?? "";
                        setEditorContent(v);
                        setIsDirty(v !== (fileContent?.content ?? ""));
                      }}
                      onMount={(editor, monaco) => {
                        editorRef.current = editor;
                        // Ctrl+S / Cmd+S to save
                        editor.addCommand(
                          monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS,
                          () => handleSave(),
                        );
                      }}
                      options={{
                        fontSize: 13,
                        lineHeight: 20,
                        fontFamily: "var(--font-mono, 'JetBrains Mono', 'Fira Code', monospace)",
                        minimap: { enabled: false },
                        scrollBeyondLastLine: false,
                        automaticLayout: true,
                        wordWrap: isMarkdownPath(selectedPath) ? "on" : "off",
                        tabSize: 2,
                        renderWhitespace: "selection",
                        smoothScrolling: true,
                        cursorSmoothCaretAnimation: "on",
                        padding: { top: 8, bottom: 8 },
                      }}
                    />
                  </Suspense>
                )}
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
