import { useState, useEffect, useMemo, useRef } from "react";
import { FileText } from "lucide-react";
import { cn } from "@/lib/utils";
import { filesApi, type FileEntry } from "@/lib/api";

// Character ranges in `text` to highlight for query `q`. Prefers a contiguous
// substring; falls back to a subsequence only when `subseq` is set (used for
// filenames, not directory paths, to avoid scattered highlights).
function matchRanges(text: string, q: string, subseq: boolean): [number, number][] {
  if (!q) return [];
  const lt = text.toLowerCase();
  const idx = lt.indexOf(q);
  if (idx >= 0) return [[idx, idx + q.length]];
  if (!subseq) return [];
  const ranges: [number, number][] = [];
  let qi = 0;
  for (let i = 0; i < text.length && qi < q.length; i++) {
    if (lt[i] === q[qi]) {
      const prev = ranges[ranges.length - 1];
      if (prev && prev[1] === i) prev[1] = i + 1; // merge adjacent
      else ranges.push([i, i + 1]);
      qi++;
    }
  }
  return qi === q.length ? ranges : [];
}

function Highlighted({ text, ranges }: { text: string; ranges: [number, number][] }) {
  if (ranges.length === 0) return <>{text}</>;
  const parts: React.ReactNode[] = [];
  let cursor = 0;
  let k = 0;
  for (const [s, e] of ranges) {
    if (cursor < s) parts.push(text.slice(cursor, s));
    parts.push(<span key={k++} className="text-primary font-semibold">{text.slice(s, e)}</span>);
    cursor = e;
  }
  if (cursor < text.length) parts.push(text.slice(cursor));
  return <>{parts}</>;
}

// ── Quick Open (Cmd/Ctrl+P) ──

// A VSCode-style "Go to file" palette: fuzzy filename/path search over the flat
// file list, keyboard-navigable, opens the chosen file on Enter/click.
export function QuickOpen({
  projectId,
  sessionId,
  onSelect,
  onClose,
}: {
  projectId: string;
  sessionId?: string;
  onSelect: (path: string) => void;
  onClose: () => void;
}) {
  const [query, setQuery] = useState("");
  const [files, setFiles] = useState<FileEntry[]>([]);
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const activeRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    filesApi.flat(projectId, sessionId).then((e) => setFiles(e || [])).catch(() => {});
  }, [projectId, sessionId]);
  useEffect(() => { inputRef.current?.focus(); }, []);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return files.slice(0, 50);
    // Match primarily against the filename (VSCode-style). Scattered
    // subsequence matches across a long directory path produce noise, so path
    // hits are only accepted as a contiguous substring, ranked below name hits.
    const scored: { f: FileEntry; tier: number; s: number; len: number }[] = [];
    for (const f of files) {
      const path = f.path.toLowerCase();
      const slash = path.lastIndexOf("/");
      const base = slash >= 0 ? path.slice(slash + 1) : path;
      let tier = -1;
      let s = 0;
      const bi = base.indexOf(q);
      if (bi >= 0) {
        // Contiguous substring in the filename — best. Prefix beats mid-name.
        tier = 0;
        s = bi * 1000 + base.length;
      } else {
        // Subsequence within the filename only (tolerates skipped chars).
        let qi = 0, first = -1, last = -1;
        for (let i = 0; i < base.length && qi < q.length; i++) {
          if (base[i] === q[qi]) { if (first < 0) first = i; last = i; qi++; }
        }
        if (qi === q.length) {
          tier = 1;
          s = (last - first) * 1000 + base.length;
        } else {
          const pi = path.indexOf(q); // contiguous substring anywhere in the path
          if (pi >= 0) { tier = 2; s = pi; }
        }
      }
      if (tier < 0) continue;
      scored.push({ f, tier, s, len: f.path.length });
    }
    scored.sort((a, b) => a.tier - b.tier || a.s - b.s || a.len - b.len);
    return scored.slice(0, 50).map((x) => x.f);
  }, [files, query]);

  useEffect(() => { activeRef.current?.scrollIntoView({ block: "nearest" }); }, [active]);

  const choose = (f?: FileEntry) => { if (f) { onSelect(f.path); onClose(); } };

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center pt-[12vh] bg-black/30" onClick={onClose}>
      <div
        className="w-[520px] max-w-[90vw] rounded-lg border bg-background shadow-xl overflow-hidden"
        onClick={(e) => e.stopPropagation()}
      >
        <input
          ref={inputRef}
          value={query}
          onChange={(e) => { setQuery(e.target.value); setActive(0); }}
          onKeyDown={(e) => {
            // While an IME composition is active (e.g. selecting a Chinese/
            // Japanese candidate with Space or Enter), let the input method
            // consume the key — don't treat it as navigation/open.
            if (e.nativeEvent.isComposing || e.keyCode === 229) return;
            if (e.key === "ArrowDown") { e.preventDefault(); setActive((i) => Math.min(filtered.length - 1, i + 1)); }
            else if (e.key === "ArrowUp") { e.preventDefault(); setActive((i) => Math.max(0, i - 1)); }
            else if (e.key === "Enter") { e.preventDefault(); choose(filtered[active]); }
            else if (e.key === "Escape") { e.preventDefault(); onClose(); }
          }}
          placeholder="Go to file…"
          className="w-full px-3 py-2.5 text-sm bg-transparent outline-none border-b placeholder:text-muted-foreground/40"
        />
        <div className="max-h-[50vh] overflow-y-auto py-1">
          {filtered.length === 0 ? (
            <div className="px-3 py-4 text-xs text-muted-foreground text-center">No files</div>
          ) : filtered.map((f, i) => {
            const slash = f.path.lastIndexOf("/");
            const name = slash >= 0 ? f.path.slice(slash + 1) : f.path;
            const dir = slash >= 0 ? f.path.slice(0, slash) : "";
            const q = query.trim().toLowerCase();
            return (
              <div
                key={f.path}
                ref={i === active ? activeRef : undefined}
                onMouseEnter={() => setActive(i)}
                onClick={() => choose(f)}
                title={f.path}
                className={cn(
                  "flex items-center gap-2 px-3 py-1.5 text-xs cursor-pointer",
                  i === active ? "bg-primary/15 text-foreground" : "hover:bg-accent/40",
                )}
              >
                <FileText className="h-3.5 w-3.5 text-muted-foreground/50 shrink-0" />
                {/* Filename gets display priority; the directory truncates to fit. */}
                <span className="font-medium shrink-0 truncate max-w-[60%]">
                  <Highlighted text={name} ranges={matchRanges(name, q, true)} />
                </span>
                {dir && (
                  <span className="text-muted-foreground/50 truncate min-w-0 flex-1">
                    <Highlighted text={dir} ranges={matchRanges(dir, q, false)} />
                  </span>
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
