// Rendering helper for intra-line diff highlights.
//
// The actual diff computation (line alignment + which characters changed) lives
// in vscode-diff.ts. This module only overlays the resulting character ranges
// onto the syntax-highlighted HTML produced by the highlighter.

/**
 * Wrap the given visible-character ranges in `<mark class="...">` within an
 * HTML string produced by the syntax highlighter. Tags and entities are left
 * intact, and marks are split at tag boundaries so the markup stays valid.
 *
 * The highlighted HTML's visible text equals the original line character for
 * character (entities like `&lt;` count as one visible char), so ranges
 * computed on the plain line map directly onto offsets here.
 */
export function applyRangesToHtml(
  html: string,
  ranges: [number, number][],
  markClass: string,
): string {
  if (ranges.length === 0) return html;
  const open = `<mark class="${markClass}">`;
  const close = "</mark>";
  let out = "";
  let offset = 0; // index into the visible text
  let inMark = false;
  let ri = 0; // ranges are sorted; offset only increases, so advance forward
  const inRange = (o: number): boolean => {
    while (ri < ranges.length && o >= ranges[ri][1]) ri++;
    return ri < ranges.length && o >= ranges[ri][0];
  };

  let k = 0;
  while (k < html.length) {
    const ch = html[k];
    if (ch === "<") {
      // A tag — emit verbatim. Close any open mark first so nesting stays
      // valid; it reopens on the next visible char if still inside a range.
      if (inMark) {
        out += close;
        inMark = false;
      }
      const gt = html.indexOf(">", k);
      const end = gt === -1 ? html.length : gt + 1;
      out += html.slice(k, end);
      k = end;
      continue;
    }

    // One visible character — collapse an HTML entity into a single unit.
    let chunk = ch;
    if (ch === "&") {
      const semi = html.indexOf(";", k);
      if (semi !== -1 && semi - k <= 10) chunk = html.slice(k, semi + 1);
    }

    const want = inRange(offset);
    if (want && !inMark) {
      out += open;
      inMark = true;
    } else if (!want && inMark) {
      out += close;
      inMark = false;
    }
    out += chunk;
    offset++;
    k += chunk.length;
  }

  if (inMark) out += close;
  return out;
}
