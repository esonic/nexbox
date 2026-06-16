import { forwardRef, useCallback, useEffect, useImperativeHandle, useRef, useState } from "react";
import Editor, { type OnMount } from "@monaco-editor/react";
import { Loader2 } from "lucide-react";

const EXT_TO_MONACO: Record<string, string> = {
  js: "javascript", jsx: "javascript", mjs: "javascript", cjs: "javascript",
  ts: "typescript", tsx: "typescript", mts: "typescript",
  py: "python", go: "go", java: "java", rs: "rust",
  json: "json", yaml: "yaml", yml: "yaml",
  html: "html", htm: "html", xml: "xml", svg: "xml", vue: "html",
  css: "css", scss: "scss", less: "less",
  sh: "shell", bash: "shell", zsh: "shell",
  sql: "sql", rb: "ruby", php: "php",
  swift: "swift", kt: "kotlin",
  cpp: "cpp", cc: "cpp", cxx: "cpp", hpp: "cpp", h: "cpp", c: "c",
  md: "markdown", markdown: "markdown",
  dockerfile: "dockerfile", toml: "ini", ini: "ini",
};

function langFromPath(filepath: string): string | undefined {
  const base = filepath.split("/").pop()?.toLowerCase() || "";
  if (base === "dockerfile") return "dockerfile";
  const ext = filepath.split(".").pop()?.toLowerCase() || "";
  return EXT_TO_MONACO[ext];
}

function getDocumentTheme(): "vs-dark" | "vs-light" {
  return document.body.classList.contains("dark") ? "vs-dark" : "vs-light";
}

type MonacoStandaloneEditor = Parameters<OnMount>[0];
type MonacoNamespace = Parameters<OnMount>[1];

export interface MonacoFileEditorHandle {
  /** Focus the editor and open Monaco's native find widget. */
  openFind: () => void;
}

interface MonacoFileEditorProps {
  filepath: string;
  value: string;
  onChange: (next: string) => void;
  onSave?: () => void;
  /**
   * When set, Monaco reveals the match and opens its native find widget seeded
   * with this query — used when a content search result is clicked.
   */
  searchQuery?: string;
  searchCaseSensitive?: boolean;
  /** 1-based line where the search match lives; used to anchor the reveal. */
  targetLine?: number;
  /**
   * Bumped by the parent each time a search result is selected, so re-clicking
   * the same result (identical query/line) re-triggers the reveal.
   */
  searchNonce?: number;
}

const MonacoFileEditor = forwardRef<MonacoFileEditorHandle, MonacoFileEditorProps>(
  function MonacoFileEditor(
    { filepath, value, onChange, onSave, searchQuery, searchCaseSensitive, targetLine, searchNonce },
    ref,
  ) {
    const [theme, setTheme] = useState<"vs-dark" | "vs-light">(getDocumentTheme);
    const editorRef = useRef<MonacoStandaloneEditor | null>(null);
    const monacoRef = useRef<MonacoNamespace | null>(null);
    const revealRafRef = useRef<number | null>(null);

    const onSaveRef = useRef(onSave);
    useEffect(() => {
      onSaveRef.current = onSave;
    }, [onSave]);

    // Keep the latest search request in a ref so onMount (first load, fires
    // after the async Monaco chunk resolves) navigates using current values,
    // free of closure-timing concerns. Updated in the effect below.
    const searchReqRef = useRef<{ query?: string; caseSensitive?: boolean; line?: number }>({});

    useEffect(() => {
      const obs = new MutationObserver(() => setTheme(getDocumentTheme()));
      obs.observe(document.body, { attributes: true, attributeFilter: ["class"] });
      return () => obs.disconnect();
    }, []);

    const openFind = useCallback(() => {
      const editor = editorRef.current;
      if (!editor) return;
      editor.focus();
      editor.getAction("actions.find")?.run();
    }, []);

    useImperativeHandle(ref, () => ({ openFind }), [openFind]);

    // Reveal the requested match line (centered) and open Monaco's native find
    // widget seeded from that match. Deferred to the next frame because:
    //   - switching files replaces the model content via executeEdits in
    //     @monaco-editor/react's own effect, so reading/searching the model
    //     synchronously here can hit the *previous* file's text;
    //   - right after mount the editor layout is not yet settled, so "center"
    //     misfires.
    // Running on the next frame guarantees the new content is in place and our
    // scroll is the final word. The whole search is recomputed inside the frame
    // (never reusing a stale range) so the line is always correct.
    const revealSearchTarget = useCallback(() => {
      if (revealRafRef.current != null) cancelAnimationFrame(revealRafRef.current);

      let frame = 0;
      let opened = false;

      const step = () => {
        revealRafRef.current = null;
        const editor = editorRef.current;
        const monaco = monacoRef.current;
        if (!editor || !monaco) return;
        const model = editor.getModel();
        if (!model) return;

        const { query: rawQuery, caseSensitive, line: rawLine } = searchReqRef.current;
        const query = rawQuery?.trim();
        const lineCount = model.getLineCount();
        const line = rawLine && rawLine > 0 ? Math.min(rawLine, lineCount) : undefined;
        if (!query && line == null) return;

        // Recompute against the *current* model every frame: switching files
        // replaces the content (via executeEdits) asynchronously, so the first
        // frame may still see the old text.
        let target: import("monaco-editor").IRange | null = null;
        if (query) {
          const from = line ?? 1;
          // Prefer the first match at/after the result line, falling back to the
          // first match anywhere so the find box never lands on "no results".
          const scope = new monaco.Range(from, 1, lineCount, model.getLineMaxColumn(lineCount));
          const match =
            model.findMatches(query, scope, false, !!caseSensitive, null, false, 1)[0] ??
            model.findMatches(query, true, false, !!caseSensitive, null, false, 1)[0];
          if (match) target = match.range;
        }
        if (!target && line != null) {
          target = new monaco.Range(line, 1, line, 1);
        }
        if (!target) return;

        // Selection + reveal run every frame against the recomputed target, so
        // even if an early frame raced ahead of the content flush and saw the
        // previous file, later frames land the cursor and scroll on the right
        // line. Opening the find widget happens once (it seeds from the
        // selection; with addExtraSpaceOnTop off it does not touch scroll).
        editor.setSelection(target);
        if (query && !opened) {
          editor.getAction("actions.find")?.run();
          opened = true;
        }
        editor.revealRangeInCenter(target, monaco.editor.ScrollType.Immediate);

        // Re-assert the centering over a few frames so any scroll Monaco does
        // *after* us (the content-flush moves the cursor to the document end)
        // cannot leave the match stuck at the top/bottom.
        frame += 1;
        if (frame < 6) {
          revealRafRef.current = requestAnimationFrame(step);
        }
      };

      revealRafRef.current = requestAnimationFrame(step);
    }, []);

    useEffect(
      () => () => {
        if (revealRafRef.current != null) cancelAnimationFrame(revealRafRef.current);
      },
      [],
    );

    const handleMount: OnMount = (editor, monaco) => {
      editorRef.current = editor;
      monacoRef.current = monaco;
      editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => {
        onSaveRef.current?.();
      });
      // First load: the effect below already ran (no editor yet); drive the
      // initial reveal here now that the editor exists.
      revealSearchTarget();
    };

    // Track the latest request and re-run on each search selection while the
    // editor is already mounted.
    useEffect(() => {
      searchReqRef.current = { query: searchQuery, caseSensitive: searchCaseSensitive, line: targetLine };
      revealSearchTarget();
    }, [searchQuery, searchCaseSensitive, targetLine, searchNonce, revealSearchTarget]);

    return (
      <Editor
        height="100%"
        language={langFromPath(filepath)}
        value={value}
        onChange={(v) => onChange(v ?? "")}
        onMount={handleMount}
        theme={theme}
        loading={<Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />}
        options={{
          minimap: { enabled: false },
          fontSize: 15,
          lineNumbers: "on",
          scrollBeyondLastLine: false,
          automaticLayout: true,
          wordWrap: "on",
          renderWhitespace: "selection",
          tabSize: 2,
          // addExtraSpaceOnTop:false keeps the find widget a pure floating
          // overlay. With it on (the default), opening find inserts a top view
          // zone and bumps scrollTop by its height — which fires after our
          // reveal and shoves the centered match up off the top of the screen.
          find: { seedSearchStringFromSelection: "always", addExtraSpaceOnTop: false },
        }}
      />
    );
  },
);

export default MonacoFileEditor;
