import { forwardRef, useCallback, useEffect, useImperativeHandle, useRef, useState } from "react";
import Editor, { type OnMount } from "@monaco-editor/react";
import { Loader2 } from "lucide-react";
import { computeDiffOps } from "@/lib/vscode-diff";

type DirtyChangeKind = "add" | "modify" | "delete";
/** One rendered row of a change region's peek view. */
interface HunkLine {
  kind: "del" | "ins";
  text: string;
  /** Character ranges to highlight inline (word-level diff), from computeDiffOps. */
  ranges: [number, number][];
}
interface DirtyChange {
  /** 1-based line range in the *current* (modified) document. */
  startLine: number;
  endLine: number;
  kind: DirtyChangeKind;
  /** Removed-then-added lines of this region, for the click-to-peek view. */
  hunk: HunkLine[];
}

/**
 * Diff the committed baseline against the current content and collapse the
 * per-line op list into VSCode-style dirty-diff regions, expressed in current
 * document line numbers:
 *   - add    — lines present only in the current content (green)
 *   - modify — a region where lines were both removed and added (blue)
 *   - delete — lines removed with nothing added; marked on the line that now
 *              sits just below the deletion gap (red wedge)
 * Each region also carries its own removed/added lines so clicking the gutter
 * bar can show the hunk without re-diffing.
 */
function computeDirtyChanges(baseline: string, current: string): DirtyChange[] {
  const baseLines = baseline.split("\n");
  const curLines = current.split("\n");
  const ops = computeDiffOps(baseLines, curLines);
  const changes: DirtyChange[] = [];

  let i = 0;
  while (i < ops.length) {
    if (ops[i].kind === "equal") {
      i++;
      continue;
    }
    // Gather a contiguous run of non-equal ops (a single change region).
    let deletes = 0;
    const insertModIdx: number[] = [];
    // Removed lines first, then added — the reading order of a unified diff.
    const dels: HunkLine[] = [];
    const ins: HunkLine[] = [];
    while (i < ops.length && ops[i].kind !== "equal") {
      const op = ops[i];
      if (op.kind === "delete") {
        deletes++;
        dels.push({ kind: "del", text: baseLines[op.origIdx] ?? "", ranges: op.ranges });
      } else if (op.kind === "insert") {
        insertModIdx.push(op.modIdx);
        ins.push({ kind: "ins", text: curLines[op.modIdx] ?? "", ranges: op.ranges });
      }
      i++;
    }
    const hunk = [...dels, ...ins];

    if (insertModIdx.length > 0) {
      changes.push({
        startLine: Math.min(...insertModIdx) + 1,
        endLine: Math.max(...insertModIdx) + 1,
        kind: deletes > 0 ? "modify" : "add",
        hunk,
      });
    } else if (deletes > 0) {
      // Pure deletion: anchor the marker on the line that follows the gap in
      // the current document (the next equal op), or the last line at EOF.
      const next = ops[i];
      const line = next && next.kind === "equal" ? next.modIdx + 1 : Math.max(1, curLines.length);
      changes.push({ startLine: line, endLine: line, kind: "delete", hunk });
    }
  }
  return changes;
}

/**
 * Place the peek box over the content area. Monaco's `contentWidth` does NOT
 * exclude the vertical scrollbar / decorations overview ruler, so a full-width
 * box would run underneath them and hide its own right edge (the close button).
 */
function fitPeekBox(editor: MonacoStandaloneEditor, box: HTMLElement): void {
  const layout = editor.getLayoutInfo();
  box.style.left = `${layout.contentLeft}px`;
  box.style.width = `${Math.max(120, layout.contentWidth - layout.verticalScrollbarWidth - 2)}px`;
}

/**
 * Build the peek card for a change region: a thin header with the -/+ counts
 * and a close button, then one row per removed/added line with word-level
 * highlights. Plain DOM (not React) because Monaco owns the node's lifecycle.
 */
function renderPeekDom(change: DirtyChange, onClose: () => void): HTMLElement {
  const root = document.createElement("div");
  root.className = "dirty-diff-peek";

  const header = document.createElement("div");
  header.className = "dirty-diff-peek-header";
  const dels = change.hunk.filter((l) => l.kind === "del").length;
  const ins = change.hunk.length - dels;
  const counts = document.createElement("span");
  counts.className = "dirty-diff-peek-counts";
  if (dels) counts.appendChild(Object.assign(document.createElement("span"), {
    className: "dirty-diff-peek-count-del", textContent: `−${dels}`,
  }));
  if (ins) counts.appendChild(Object.assign(document.createElement("span"), {
    className: "dirty-diff-peek-count-ins", textContent: `+${ins}`,
  }));
  header.appendChild(counts);

  const close = document.createElement("button");
  close.className = "dirty-diff-peek-close";
  close.type = "button";
  close.title = "Close (Esc)";
  close.textContent = "✕";
  close.addEventListener("click", (e) => {
    e.preventDefault();
    e.stopPropagation();
    onClose();
  });
  header.appendChild(close);
  root.appendChild(header);

  const body = document.createElement("div");
  body.className = "dirty-diff-peek-body";
  for (const line of change.hunk) {
    const row = document.createElement("div");
    row.className = `dirty-diff-peek-line dirty-diff-peek-${line.kind}`;
    const sign = document.createElement("span");
    sign.className = "dirty-diff-peek-sign";
    sign.textContent = line.kind === "del" ? "-" : "+";
    row.appendChild(sign);

    const text = document.createElement("span");
    text.className = "dirty-diff-peek-text";
    // Word-level highlight: emit the untouched slices as text and the changed
    // ranges wrapped in <mark>. Ranges from computeDiffOps are sorted and
    // non-overlapping; anything outside them is context.
    let cursor = 0;
    for (const [from, to] of line.ranges) {
      if (from > cursor) text.appendChild(document.createTextNode(line.text.slice(cursor, from)));
      const mark = document.createElement("mark");
      mark.className = line.kind === "del" ? "diff-word-del" : "diff-word-add";
      mark.textContent = line.text.slice(from, to);
      text.appendChild(mark);
      cursor = to;
    }
    text.appendChild(document.createTextNode(line.text.slice(cursor)));
    row.appendChild(text);
    body.appendChild(row);
  }
  root.appendChild(body);
  return root;
}

/** Unique overlay-widget ids, so two editors never collide. */
let peekWidgetSeq = 0;

/**
 * Width of the line-decorations strip that belongs to dirty diff. Monaco's
 * folding controller hardcodes this same 4px as the region it will not treat as
 * a folding-arrow click, so it is a ceiling, not a preference.
 */
const DIRTY_DIFF_STRIP_PX = 4;
/** Line-number padding the hovered bar grows into; must match index.css. */
const LINE_NUMBERS_PAD_PX = 3;

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
   * Committed (HEAD) content of the file. When provided, the editor renders
   * VSCode-style dirty-diff markers (gutter bars + scrollbar overview) for the
   * difference between this baseline and the live `value`. Pass undefined to
   * disable (e.g. file not in git or baseline still loading).
   */
  baseline?: string;
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
    { filepath, value, onChange, onSave, baseline, searchQuery, searchCaseSensitive, targetLine, searchNonce },
    ref,
  ) {
    const [theme, setTheme] = useState<"vs-dark" | "vs-light">(getDocumentTheme);
    const editorRef = useRef<MonacoStandaloneEditor | null>(null);
    const monacoRef = useRef<MonacoNamespace | null>(null);
    const revealRafRef = useRef<number | null>(null);
    const dirtyDiffRef = useRef<ReturnType<MonacoStandaloneEditor["createDecorationsCollection"]> | null>(null);
    const dirtyDiffTimerRef = useRef<number | null>(null);
    // Latest change regions, keyed on by the gutter click handler.
    const dirtyChangesRef = useRef<DirtyChange[]>([]);
    // The open peek view zone, if any. `line` is the change's startLine, used to
    // toggle the peek closed when the same gutter bar is clicked again.
    const peekRef = useRef<{
      zoneId: string;
      line: number;
      widget: Parameters<MonacoStandaloneEditor["addOverlayWidget"]>[0];
      box: HTMLElement;
    } | null>(null);

    const onSaveRef = useRef(onSave);
    useEffect(() => {
      onSaveRef.current = onSave;
    }, [onSave]);

    // ── IME-safe content sync ────────────────────────────────────────────────
    // <Editor> is rendered UNCONTROLLED (defaultValue, not value). The controlled
    // `value` prop makes @monaco-editor/react run a full-range executeEdits
    // whenever `value !== editor.getValue()`. During an IME composition the React
    // `value` lags the live model by a keystroke or two, so that compare trips
    // mid-composition, replaces the whole document, and — via forceMoveMarkers —
    // yanks the caret to EOF. That is the "typing Chinese suddenly scrolls to the
    // bottom and the text lands at end-of-file" bug. We reproduce the sync here
    // but skip it entirely while a composition is active (composingRef). Echoes
    // of the user's own edits — including the characters just committed by the
    // IME — are no-ops because by the time this runs the model already holds them
    // (model.getValue() === value). Only genuinely external changes (file switch
    // / reload), where value diverges from the model outside composition, apply.
    const valueRef = useRef(value);
    const composingRef = useRef(false);

    const syncExternalValue = useCallback(() => {
      const editor = editorRef.current;
      const model = editor?.getModel();
      if (!editor || !model) return;
      if (composingRef.current) return; // never mutate the model mid-IME
      const next = valueRef.current;
      if (model.getValue() === next) return; // echo of the user's own edit / no-op
      editor.executeEdits("external-value-sync", [
        { range: model.getFullModelRange(), text: next, forceMoveMarkers: true },
      ]);
      editor.pushUndoStop();
    }, []);

    useEffect(() => {
      valueRef.current = value;
      syncExternalValue();
    }, [value, syncExternalValue]);

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
        if (dirtyDiffTimerRef.current != null) clearTimeout(dirtyDiffTimerRef.current);
      },
      [],
    );

    // Recompute dirty-diff markers (gutter bars + scrollbar overview) from the
    // committed baseline against the current model text, and apply them as a
    // single decorations collection. No-op until the editor is mounted.
    const applyDirtyDiff = useCallback(() => {
      const editor = editorRef.current;
      const monaco = monacoRef.current;
      if (!editor || !monaco) return;
      const model = editor.getModel();
      if (!model) return;

      // No baseline (file not in git / still loading) → clear any markers.
      if (baseline == null) {
        dirtyDiffRef.current?.set([]);
        dirtyChangesRef.current = [];
        return;
      }

      const current = model.getValue();
      const changes = current === baseline ? [] : computeDirtyChanges(baseline, current);
      dirtyChangesRef.current = changes;
      const Lane = monaco.editor.OverviewRulerLane;
      const lineCount = model.getLineCount();

      const decorations = changes.map((c) => {
        const start = Math.min(c.startLine, lineCount);
        const end = Math.min(c.endLine, lineCount);
        const rulerColor =
          c.kind === "add" ? "#2ea043" : c.kind === "modify" ? "#0c7d9d" : "#f85149";
        return {
          range: new monaco.Range(start, 1, end, 1),
          options: {
            // Gutter bar lives in the line-decorations margin (left of content).
            linesDecorationsClassName: `dirty-diff-glyph dirty-diff-${c.kind}`,
            overviewRuler: { color: rulerColor, position: Lane.Left },
          },
        };
      });

      if (!dirtyDiffRef.current) {
        dirtyDiffRef.current = editor.createDecorationsCollection(decorations);
      } else {
        dirtyDiffRef.current.set(decorations);
      }
    }, [baseline]);

    // Recompute on baseline/content change. Debounced so fast typing on large
    // files doesn't run the diff on every keystroke.
    useEffect(() => {
      if (dirtyDiffTimerRef.current != null) clearTimeout(dirtyDiffTimerRef.current);
      dirtyDiffTimerRef.current = window.setTimeout(applyDirtyDiff, 120);
      return () => {
        if (dirtyDiffTimerRef.current != null) clearTimeout(dirtyDiffTimerRef.current);
      };
    }, [applyDirtyDiff, value, baseline, filepath]);

    // ── Dirty-diff peek ──────────────────────────────────────────────────────
    // Clicking a gutter bar shows the change's removed/added lines above it
    // (VSCode's dirty-diff peek, minus the nested diff editor and the
    // navigate/revert actions).
    //
    // Structure follows VSCode's own ZoneWidget: the view zone is an EMPTY
    // spacer that only reserves vertical space, and the visible card lives in an
    // overlay widget kept aligned to the zone's top. Content placed directly
    // inside a view zone is unusable — Monaco's mouse handling owns the scrolled
    // content, so the cursor stays an I-beam, text can't be selected and clicks
    // never reach the DOM. Overlay widgets (the find widget is one) sit outside
    // that layer and get real events.
    const closePeek = useCallback(() => {
      const editor = editorRef.current;
      const peek = peekRef.current;
      if (!editor || !peek) return;
      peekRef.current = null;
      editor.changeViewZones((accessor) => accessor.removeZone(peek.zoneId));
      editor.removeOverlayWidget(peek.widget);
    }, []);

    const openPeek = useCallback(
      (change: DirtyChange) => {
        const editor = editorRef.current;
        const monaco = monacoRef.current;
        if (!editor || !monaco || change.hunk.length === 0) return;

        const lineHeight = editor.getOption(monaco.editor.EditorOption.lineHeight) || 19;
        const HEADER_PX = 24;
        const needed = HEADER_PX + change.hunk.length * lineHeight + 8;
        // Never let a huge hunk swallow the viewport; the body scrolls instead.
        const cap = Math.max(HEADER_PX + 3 * lineHeight, Math.round(editor.getLayoutInfo().height * 0.6));
        const height = Math.min(needed, cap);

        // The widget's own node stays at 0,0 (Monaco resets `top` on widgets
        // with a null position, so the box we move is a child of it).
        const widgetNode = document.createElement("div");
        widgetNode.className = "dirty-diff-peek-overlay";
        const box = document.createElement("div");
        box.className = "dirty-diff-peek-box";
        box.style.height = `${height}px`;
        box.appendChild(renderPeekDom(change, closePeek));
        widgetNode.appendChild(box);
        fitPeekBox(editor, box);
        // Interacting with the peek moves focus off Monaco's hidden textarea, so
        // the editor's own Escape handler stops firing — handle it here too.
        widgetNode.addEventListener("keydown", (e) => {
          if (e.key === "Escape") {
            e.stopPropagation();
            closePeek();
            editor.focus();
          }
        });

        const widgetId = `dirty-diff-peek.${++peekWidgetSeq}`;
        const widget = {
          getId: () => widgetId,
          getDomNode: () => widgetNode,
          // null position → we place the box ourselves from the zone's top.
          getPosition: () => null,
        };

        let zoneId = "";
        editor.changeViewZones((accessor) => {
          zoneId = accessor.addZone({
            // afterLineNumber 0 puts the zone above line 1.
            afterLineNumber: Math.max(0, change.startLine - 1),
            heightInPx: height,
            domNode: document.createElement("div"),
            // Fires on every zone render (including scroll), keeping the
            // overlay glued to the reserved gap.
            onDomNodeTop: (top) => {
              box.style.top = `${top}px`;
            },
            onComputedHeight: (h) => {
              box.style.height = `${h}px`;
            },
          });
        });
        editor.addOverlayWidget(widget);
        peekRef.current = { zoneId, line: change.startLine, widget, box };
      },
      [closePeek],
    );

    const handleGutterClick = useCallback(
      (line: number) => {
        const change = dirtyChangesRef.current.find((c) => line >= c.startLine && line <= c.endLine);
        const openAt = peekRef.current?.line;
        closePeek();
        // Clicking the bar of the already-open change toggles it shut.
        if (change && change.startLine !== openAt) openPeek(change);
      },
      [closePeek, openPeek],
    );

    const handleMount: OnMount = (editor, monaco) => {
      editorRef.current = editor;
      monacoRef.current = monaco;
      const MouseTarget = monaco.editor.MouseTargetType;
      editor.onMouseDown((e) => {
        const type = e.target.type;
        if (
          type !== MouseTarget.GUTTER_LINE_DECORATIONS &&
          type !== MouseTarget.GUTTER_LINE_NUMBERS
        ) {
          return;
        }
        // `type` discriminates a 3-member margin variant, so TS won't narrow the
        // union from the check above; reach for the margin detail directly.
        const { detail } = e.target as unknown as {
          detail: {
            offsetX: number;
            glyphMarginLeft: number;
            glyphMarginWidth: number;
            lineNumbersWidth: number;
          };
        };
        // x within the line-numbers band; the decorations band starts where it ends.
        const x = detail.offsetX - detail.glyphMarginLeft - detail.glyphMarginWidth;
        const inStrip =
          type === MouseTarget.GUTTER_LINE_DECORATIONS
            ? // The line-decorations gutter is shared with the folding arrows.
              // Monaco's folding controller ignores clicks in the leftmost 4px,
              // explicitly reserving them for dirty diff (contrib/folding/
              // folding.js: "this is such that we don't collide with dirty
              // diff"), so claim exactly that strip and leave the rest alone.
              x - detail.lineNumbersWidth < DIRTY_DIFF_STRIP_PX
            : // The line-number column gives up its last few px as padding (see
              // index.css) and the hovered bar grows into it — so that overhang
              // has to answer to clicks too.
              x >= detail.lineNumbersWidth - LINE_NUMBERS_PAD_PX;
        if (!inStrip) return;

        const line = e.target.position?.lineNumber;
        if (line != null) handleGutterClick(line);
      });
      editor.onKeyDown((e) => {
        if (e.keyCode === monaco.KeyCode.Escape && peekRef.current) {
          closePeek();
          e.preventDefault();
          e.stopPropagation();
        }
      });
      // Any edit (or an external content swap / file switch) invalidates the
      // hunk on screen, so drop the peek rather than show stale lines.
      editor.onDidChangeModelContent(() => closePeek());
      // Resizing changes the content area; re-fit the open box.
      editor.onDidLayoutChange(() => {
        const peek = peekRef.current;
        if (peek) fitPeekBox(editor, peek.box);
      });
      // Track IME composition so syncExternalValue never rewrites the model (and
      // forces the caret to EOF) while the user is mid-composition.
      //
      // Do NOT sync synchronously on composition end: at that instant React has
      // not yet committed the just-selected characters, so `valueRef` still holds
      // the pre-commit *pinyin* text — syncing now would overwrite the freshly
      // committed 汉字 with those letters and jump the caret to EOF. We only clear
      // the flag; the model already contains the committed text, and the ensuing
      // [value] effect (which runs after React commits, with composingRef=false
      // and model.getValue() === value) no-ops, leaving the committed text intact.
      editor.onDidCompositionStart(() => {
        composingRef.current = true;
      });
      editor.onDidCompositionEnd(() => {
        composingRef.current = false;
      });
      // This is a standalone single-file editor: the built-in TS worker has no
      // tsconfig/node_modules/JSX context, so it false-flags nearly every
      // identifier ("Cannot find name ...") and all JSX. Disable diagnostics so
      // only syntax highlighting remains.
      const noValidation = { noSemanticValidation: true, noSyntaxValidation: true };
      monaco.languages.typescript.typescriptDefaults.setDiagnosticsOptions(noValidation);
      monaco.languages.typescript.javascriptDefaults.setDiagnosticsOptions(noValidation);
      editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => {
        onSaveRef.current?.();
      });
      // First load: the effects below already ran (no editor yet); reconcile the
      // model against the current value (a no-op when defaultValue already
      // seeded it) and drive the initial reveal and dirty-diff render here now
      // that the editor exists.
      syncExternalValue();
      revealSearchTarget();
      applyDirtyDiff();
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
        // Uncontrolled: defaultValue seeds the model at mount; external changes
        // are applied imperatively by syncExternalValue (IME-safe). Binding the
        // controlled `value` prop here reintroduces the compose-jumps-to-EOF bug.
        defaultValue={value}
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
