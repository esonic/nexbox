import { cn } from "@/lib/utils";

// Lightweight unified-diff renderer for tool-call cards and the end-of-turn
// "N files changed" summary. Deliberately self-contained (no highlighting,
// no git-repo plumbing) — this renders whatever unified diff the agent
// backend derived for a single edit/write-family tool call, which is
// typically small (a few hunks), not a full-repo diff review surface.

interface DiffLine {
  kind: "add" | "del" | "ctx" | "hunk";
  text: string;
}

const HUNK_HEADER_RE = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;
// Unified-diff convention for "the preceding line has no trailing newline in
// the file" (git, diffy, GNU diff all emit this verbatim). It annotates the
// *previous* line, isn't itself a diff line, and must not consume any of the
// hunk's declared old/new line budget — otherwise a hunk ending in a
// no-trailing-newline line looks "one line short" and everything after it
// (in a concatenated multi-op diff) gets treated as being outside any hunk.
const NO_NEWLINE_MARKER = "\\ No newline at end of file";

// parseUnifiedDiff is a small state machine, not a per-line pattern match,
// because a removed/added *content* line can itself look exactly like a
// file header pair (e.g. changing "-- old" to "++ new" renders as the two
// consecutive lines "--- old" / "+++ new" — indistinguishable from a real
// header by content alone, including with one-line lookahead). The only
// reliable signal is structural: we track how many old/new lines a hunk's
// own "@@ -a,b +c,d @@" header says it contains, and only look for the next
// file's "--- "/"+++ " header pair once that hunk has consumed exactly that
// many lines — never while still inside one.
function parseUnifiedDiff(diff: string): DiffLine[] {
  const lines = diff.split("\n");
  const out: DiffLine[] = [];
  let oldRemaining = 0;
  let newRemaining = 0;
  const inHunk = () => oldRemaining > 0 || newRemaining > 0;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (!inHunk()) {
      if (line.startsWith("diff --git") || line.startsWith("index ")) continue;
      if (line.startsWith("--- ") && lines[i + 1]?.startsWith("+++ ")) {
        i += 1; // also skip the paired "+++ " line
        continue;
      }
    }
    const hunkMatch = !inHunk() ? line.match(HUNK_HEADER_RE) : null;
    if (hunkMatch) {
      oldRemaining = hunkMatch[2] !== undefined ? Number(hunkMatch[2]) : 1;
      newRemaining = hunkMatch[4] !== undefined ? Number(hunkMatch[4]) : 1;
      out.push({ kind: "hunk", text: line });
      continue;
    }
    if (line === NO_NEWLINE_MARKER) {
      continue;
    }
    if (!inHunk()) {
      // Outside any hunk and not a header/hunk line — stray protocol text
      // between diffs (e.g. a concatenated diff's blank separator line).
      continue;
    }
    if (line.startsWith("+")) {
      newRemaining--;
      out.push({ kind: "add", text: line.slice(1) });
    } else if (line.startsWith("-")) {
      oldRemaining--;
      out.push({ kind: "del", text: line.slice(1) });
    } else {
      // A context line is exactly " " + content; anything else this deep
      // inside a hunk (including a blank line, which loses its leading
      // space when a trailing-whitespace-trimming layer touches the diff)
      // still counts as context so the old/new counters stay in sync.
      oldRemaining--;
      newRemaining--;
      out.push({ kind: "ctx", text: line.startsWith(" ") ? line.slice(1) : line });
    }
  }
  return out;
}

export function DiffStats({ diff }: { diff: string }) {
  // Reuses the same header/hunk-aware parser as DiffPreview so the +N/-N
  // count always matches what's actually rendered below it, instead of
  // recomputing with a separate (and previously buggier) line scan.
  const lines = parseUnifiedDiff(diff);
  let additions = 0;
  let deletions = 0;
  for (const l of lines) {
    if (l.kind === "add") additions++;
    else if (l.kind === "del") deletions++;
  }
  if (additions === 0 && deletions === 0) return null;
  return (
    <span className="inline-flex items-center gap-1 font-mono text-[10px]">
      {additions > 0 && <span className="text-green-600 dark:text-green-500">+{additions}</span>}
      {deletions > 0 && <span className="text-red-600 dark:text-red-500">-{deletions}</span>}
    </span>
  );
}

/** Renders a single tool call's unified diff (already scoped to one file). */
export function DiffPreview({ diff, maxLines = 400 }: { diff: string; maxLines?: number }) {
  const lines = parseUnifiedDiff(diff);
  if (lines.length === 0) return null;
  const truncated = lines.length > maxLines;
  const shown = truncated ? lines.slice(0, maxLines) : lines;

  return (
    <div className="rounded-md border border-border/50 bg-muted/20 overflow-hidden">
      <pre className="text-[11px] font-mono leading-[1.35] overflow-auto max-h-80">
        {shown.map((l, i) => {
          if (l.kind === "hunk") {
            return (
              <div key={i} className="px-2 py-0.5 text-muted-foreground/70 bg-muted/40 select-none">
                {l.text}
              </div>
            );
          }
          const cls =
            l.kind === "add"
              ? "bg-green-500/10 text-green-700 dark:text-green-400"
              : l.kind === "del"
                ? "bg-red-500/10 text-red-700 dark:text-red-400"
                : "text-foreground/80";
          const sign = l.kind === "add" ? "+" : l.kind === "del" ? "-" : " ";
          return (
            <div key={i} className={cn("px-2 whitespace-pre", cls)}>
              <span className="select-none opacity-60">{sign}</span>
              {l.text}
            </div>
          );
        })}
        {truncated && (
          <div className="px-2 py-0.5 text-muted-foreground/60 select-none">… diff truncated</div>
        )}
      </pre>
    </div>
  );
}
