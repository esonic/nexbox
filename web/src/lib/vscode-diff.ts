// A faithful port of VSCode's diff engine (DefaultLinesDiffComputer), adapted
// to emit the per-line / per-character data our renderer needs.
//
// Ported from microsoft/vscode (MIT License):
//   src/vs/editor/common/diff/defaultLinesDiffComputer/*
// Pipeline: weighted dynamic-programming line alignment (Myers fallback for big
// inputs) → per-region character refinement → cleanup heuristics. We compare
// lines by trimmed content (VSCode's ignoreTrimWhitespace default) so a
// re-indented block reads as unchanged context, and the character refinement
// trims per-line leading/trailing whitespace so only in-line edits light up.

// ---------------------------------------------------------------------------
// Core geometry
// ---------------------------------------------------------------------------

class OffsetRange {
  constructor(public readonly start: number, public readonly endExclusive: number) {}
  static ofLength(length: number): OffsetRange {
    return new OffsetRange(0, length);
  }
  get length(): number {
    return this.endExclusive - this.start;
  }
  get isEmpty(): boolean {
    return this.start === this.endExclusive;
  }
  delta(offset: number): OffsetRange {
    return offset === 0 ? this : new OffsetRange(this.start + offset, this.endExclusive + offset);
  }
  deltaStart(offset: number): OffsetRange {
    return offset === 0 ? this : new OffsetRange(this.start + offset, this.endExclusive);
  }
  deltaEnd(offset: number): OffsetRange {
    return offset === 0 ? this : new OffsetRange(this.start, this.endExclusive + offset);
  }
  join(other: OffsetRange): OffsetRange {
    return new OffsetRange(Math.min(this.start, other.start), Math.max(this.endExclusive, other.endExclusive));
  }
  intersect(other: OffsetRange): OffsetRange | undefined {
    const start = Math.max(this.start, other.start);
    const end = Math.min(this.endExclusive, other.endExclusive);
    return start <= end ? new OffsetRange(start, end) : undefined;
  }
  intersects(other: OffsetRange): boolean {
    return Math.max(this.start, other.start) < Math.min(this.endExclusive, other.endExclusive);
  }
}

class OffsetPair {
  static readonly zero = new OffsetPair(0, 0);
  static readonly max = new OffsetPair(Number.MAX_SAFE_INTEGER, Number.MAX_SAFE_INTEGER);
  constructor(public readonly offset1: number, public readonly offset2: number) {}
  delta(offset: number): OffsetPair {
    return offset === 0 ? this : new OffsetPair(this.offset1 + offset, this.offset2 + offset);
  }
  equals(other: OffsetPair): boolean {
    return this.offset1 === other.offset1 && this.offset2 === other.offset2;
  }
}

class SequenceDiff {
  constructor(public readonly seq1Range: OffsetRange, public readonly seq2Range: OffsetRange) {}

  swap(): SequenceDiff {
    return new SequenceDiff(this.seq2Range, this.seq1Range);
  }
  join(other: SequenceDiff): SequenceDiff {
    return new SequenceDiff(this.seq1Range.join(other.seq1Range), this.seq2Range.join(other.seq2Range));
  }
  delta(offset: number): SequenceDiff {
    return offset === 0 ? this : new SequenceDiff(this.seq1Range.delta(offset), this.seq2Range.delta(offset));
  }
  deltaStart(offset: number): SequenceDiff {
    return offset === 0 ? this : new SequenceDiff(this.seq1Range.deltaStart(offset), this.seq2Range.deltaStart(offset));
  }
  deltaEnd(offset: number): SequenceDiff {
    return offset === 0 ? this : new SequenceDiff(this.seq1Range.deltaEnd(offset), this.seq2Range.deltaEnd(offset));
  }
  intersect(other: SequenceDiff): SequenceDiff | undefined {
    const i1 = this.seq1Range.intersect(other.seq1Range);
    const i2 = this.seq2Range.intersect(other.seq2Range);
    if (!i1 || !i2) return undefined;
    return new SequenceDiff(i1, i2);
  }
  getStarts(): OffsetPair {
    return new OffsetPair(this.seq1Range.start, this.seq2Range.start);
  }
  getEndExclusives(): OffsetPair {
    return new OffsetPair(this.seq1Range.endExclusive, this.seq2Range.endExclusive);
  }

  static fromOffsetPairs(start: OffsetPair, endExclusive: OffsetPair): SequenceDiff {
    return new SequenceDiff(
      new OffsetRange(start.offset1, endExclusive.offset1),
      new OffsetRange(start.offset2, endExclusive.offset2),
    );
  }

  // The equal mappings (gaps) between the given diffs, including before the
  // first and after the last.
  static invert(sequenceDiffs: SequenceDiff[], doc1Length: number): SequenceDiff[] {
    const result: SequenceDiff[] = [];
    forEachAdjacent(sequenceDiffs, (a, b) => {
      result.push(SequenceDiff.fromOffsetPairs(
        a ? a.getEndExclusives() : OffsetPair.zero,
        b ? b.getStarts() : new OffsetPair(doc1Length, (a ? a.seq2Range.endExclusive - a.seq1Range.endExclusive : 0) + doc1Length),
      ));
    });
    return result;
  }
}

interface ISequence {
  getElement(offset: number): number;
  get length(): number;
  getBoundaryScore?(length: number): number;
  isStronglyEqual(offset1: number, offset2: number): boolean;
}

function forEachAdjacent<T>(arr: T[], f: (a: T | undefined, b: T | undefined) => void): void {
  for (let i = 0; i <= arr.length; i++) {
    f(i === 0 ? undefined : arr[i - 1], i === arr.length ? undefined : arr[i]);
  }
}

function forEachWithNeighbors<T>(arr: T[], f: (prev: T | undefined, cur: T, next: T | undefined) => void): void {
  for (let i = 0; i < arr.length; i++) {
    f(i === 0 ? undefined : arr[i - 1], arr[i]!, i === arr.length - 1 ? undefined : arr[i + 1]);
  }
}

class Array2D<T> {
  private readonly array: T[];
  constructor(public readonly width: number, public readonly height: number) {
    this.array = new Array<T>(width * height);
  }
  get(x: number, y: number): T {
    return this.array[x + y * this.width]!;
  }
  set(x: number, y: number, value: T): void {
    this.array[x + y * this.width] = value;
  }
}

// ---------------------------------------------------------------------------
// Diff algorithms
// ---------------------------------------------------------------------------

const MAX_DP_CELLS = 4_000_000; // safety valve for the O(n·m) line DP

// O(MN) DP diff that supports a per-match score function.
function dynamicProgrammingDiffing(
  seq1: ISequence,
  seq2: ISequence,
  equalityScore?: (offset1: number, offset2: number) => number,
): SequenceDiff[] {
  if (seq1.length === 0 || seq2.length === 0) {
    return [new SequenceDiff(OffsetRange.ofLength(seq1.length), OffsetRange.ofLength(seq2.length))];
  }

  const lcsLengths = new Array2D<number>(seq1.length, seq2.length);
  const directions = new Array2D<number>(seq1.length, seq2.length);
  const lengths = new Array2D<number>(seq1.length, seq2.length);

  for (let s1 = 0; s1 < seq1.length; s1++) {
    for (let s2 = 0; s2 < seq2.length; s2++) {
      const horizontalLen = s1 === 0 ? 0 : lcsLengths.get(s1 - 1, s2);
      const verticalLen = s2 === 0 ? 0 : lcsLengths.get(s1, s2 - 1);

      let extendedSeqScore: number;
      if (seq1.getElement(s1) === seq2.getElement(s2)) {
        extendedSeqScore = s1 === 0 || s2 === 0 ? 0 : lcsLengths.get(s1 - 1, s2 - 1);
        if (s1 > 0 && s2 > 0 && directions.get(s1 - 1, s2 - 1) === 3) {
          extendedSeqScore += lengths.get(s1 - 1, s2 - 1); // prefer consecutive diagonals
        }
        extendedSeqScore += equalityScore ? equalityScore(s1, s2) : 1;
      } else {
        extendedSeqScore = -1;
      }

      const newValue = Math.max(horizontalLen, verticalLen, extendedSeqScore);
      if (newValue === extendedSeqScore) {
        const prevLen = s1 > 0 && s2 > 0 ? lengths.get(s1 - 1, s2 - 1) : 0;
        lengths.set(s1, s2, prevLen + 1);
        directions.set(s1, s2, 3);
      } else if (newValue === horizontalLen) {
        lengths.set(s1, s2, 0);
        directions.set(s1, s2, 1);
      } else {
        lengths.set(s1, s2, 0);
        directions.set(s1, s2, 2);
      }
      lcsLengths.set(s1, s2, newValue);
    }
  }

  const result: SequenceDiff[] = [];
  let lastAligningPosS1 = seq1.length;
  let lastAligningPosS2 = seq2.length;
  const report = (s1: number, s2: number) => {
    if (s1 + 1 !== lastAligningPosS1 || s2 + 1 !== lastAligningPosS2) {
      result.push(new SequenceDiff(new OffsetRange(s1 + 1, lastAligningPosS1), new OffsetRange(s2 + 1, lastAligningPosS2)));
    }
    lastAligningPosS1 = s1;
    lastAligningPosS2 = s2;
  };

  let s1 = seq1.length - 1;
  let s2 = seq2.length - 1;
  while (s1 >= 0 && s2 >= 0) {
    if (directions.get(s1, s2) === 3) {
      report(s1, s2);
      s1--;
      s2--;
    } else if (directions.get(s1, s2) === 1) {
      s1--;
    } else {
      s2--;
    }
  }
  report(-1, -1);
  result.reverse();
  return result;
}

class SnakePath {
  constructor(public readonly prev: SnakePath | null, public readonly x: number, public readonly y: number, public readonly length: number) {}
}

class FastInt32Array {
  private positiveArr = new Int32Array(10);
  private negativeArr = new Int32Array(10);
  get(idx: number): number {
    if (idx < 0) return this.negativeArr[-idx - 1]!;
    return this.positiveArr[idx]!;
  }
  set(idx: number, value: number): void {
    if (idx < 0) {
      idx = -idx - 1;
      if (idx >= this.negativeArr.length) {
        const arr = this.negativeArr;
        this.negativeArr = new Int32Array(arr.length * 2);
        this.negativeArr.set(arr);
      }
      this.negativeArr[idx] = value;
    } else {
      if (idx >= this.positiveArr.length) {
        const arr = this.positiveArr;
        this.positiveArr = new Int32Array(arr.length * 2);
        this.positiveArr.set(arr);
      }
      this.positiveArr[idx] = value;
    }
  }
}

class FastArrayNegativeIndices<T> {
  private readonly positiveArr: T[] = [];
  private readonly negativeArr: T[] = [];
  get(idx: number): T {
    if (idx < 0) return this.negativeArr[-idx - 1]!;
    return this.positiveArr[idx]!;
  }
  set(idx: number, value: T): void {
    if (idx < 0) this.negativeArr[-idx - 1] = value;
    else this.positiveArr[idx] = value;
  }
}

// O(ND) Myers diff. Used when inputs are too large for the DP.
function myersDiffAlgorithm(seq1: ISequence, seq2: ISequence): SequenceDiff[] {
  if (seq1.length === 0 || seq2.length === 0) {
    return [new SequenceDiff(OffsetRange.ofLength(seq1.length), OffsetRange.ofLength(seq2.length))];
  }
  const seqX = seq1;
  const seqY = seq2;

  function getXAfterSnake(x: number, y: number): number {
    while (x < seqX.length && y < seqY.length && seqX.getElement(x) === seqY.getElement(y)) {
      x++;
      y++;
    }
    return x;
  }

  let d = 0;
  const V = new FastInt32Array();
  V.set(0, getXAfterSnake(0, 0));
  const paths = new FastArrayNegativeIndices<SnakePath | null>();
  paths.set(0, V.get(0) === 0 ? null : new SnakePath(null, 0, 0, V.get(0)));
  let k = 0;

  loop: while (true) {
    d++;
    const lowerBound = -Math.min(d, seqY.length + (d % 2));
    const upperBound = Math.min(d, seqX.length + (d % 2));
    for (k = lowerBound; k <= upperBound; k += 2) {
      const maxXofDLineTop = k === upperBound ? -1 : V.get(k + 1);
      const maxXofDLineLeft = k === lowerBound ? -1 : V.get(k - 1) + 1;
      const x = Math.min(Math.max(maxXofDLineTop, maxXofDLineLeft), seqX.length);
      const y = x - k;
      if (x > seqX.length || y > seqY.length) continue;
      const newMaxX = getXAfterSnake(x, y);
      V.set(k, newMaxX);
      const lastPath = x === maxXofDLineTop ? paths.get(k + 1) : paths.get(k - 1);
      paths.set(k, newMaxX !== x ? new SnakePath(lastPath, x, y, newMaxX - x) : lastPath);
      if (V.get(k) === seqX.length && V.get(k) - k === seqY.length) break loop;
    }
  }

  let path = paths.get(k);
  const result: SequenceDiff[] = [];
  let lastAligningPosS1 = seqX.length;
  let lastAligningPosS2 = seqY.length;
  while (true) {
    const endX = path ? path.x + path.length : 0;
    const endY = path ? path.y + path.length : 0;
    if (endX !== lastAligningPosS1 || endY !== lastAligningPosS2) {
      result.push(new SequenceDiff(new OffsetRange(endX, lastAligningPosS1), new OffsetRange(endY, lastAligningPosS2)));
    }
    if (!path) break;
    lastAligningPosS1 = path.x;
    lastAligningPosS2 = path.y;
    path = path.prev;
  }
  result.reverse();
  return result;
}

// ---------------------------------------------------------------------------
// Sequences
// ---------------------------------------------------------------------------

function getIndentation(str: string): number {
  let i = 0;
  while (i < str.length && (str.charCodeAt(i) === 32 /* space */ || str.charCodeAt(i) === 9 /* tab */)) i++;
  return i;
}

class LineSequence implements ISequence {
  constructor(private readonly trimmedHash: number[], private readonly lines: string[]) {}
  getElement(offset: number): number {
    return this.trimmedHash[offset]!;
  }
  get length(): number {
    return this.trimmedHash.length;
  }
  getBoundaryScore(length: number): number {
    const before = length === 0 ? 0 : getIndentation(this.lines[length - 1]!);
    const after = length === this.lines.length ? 0 : getIndentation(this.lines[length]!);
    return 1000 - (before + after);
  }
  getText(range: OffsetRange): string {
    return this.lines.slice(range.start, range.endExclusive).join("\n");
  }
  isStronglyEqual(offset1: number, offset2: number): boolean {
    return this.lines[offset1] === this.lines[offset2];
  }
}

const enum CharCat {
  WordLower,
  WordUpper,
  WordNumber,
  End,
  Other,
  Separator,
  Space,
  LineBreakCR,
  LineBreakLF,
}

const CAT_SCORE: Record<CharCat, number> = {
  [CharCat.WordLower]: 0,
  [CharCat.WordUpper]: 0,
  [CharCat.WordNumber]: 0,
  [CharCat.End]: 10,
  [CharCat.Other]: 2,
  [CharCat.Separator]: 30,
  [CharCat.Space]: 3,
  [CharCat.LineBreakCR]: 10,
  [CharCat.LineBreakLF]: 10,
};

function isWordChar(c: number): boolean {
  return (c >= 97 && c <= 122) || (c >= 65 && c <= 90) || (c >= 48 && c <= 57);
}
function getCategory(c: number): CharCat {
  if (c === 10) return CharCat.LineBreakLF;
  if (c === 13) return CharCat.LineBreakCR;
  if (c === 32 || c === 9) return CharCat.Space;
  if (c >= 97 && c <= 122) return CharCat.WordLower;
  if (c >= 65 && c <= 90) return CharCat.WordUpper;
  if (c >= 48 && c <= 57) return CharCat.WordNumber;
  if (c === -1) return CharCat.End;
  if (c === 44 /* , */ || c === 59 /* ; */) return CharCat.Separator;
  return CharCat.Other;
}

/** A character sequence over a slice of lines, with VSCode's boundary scoring. */
class CharSlice implements ISequence {
  private readonly elements: number[] = [];
  // elements offset where each line's content begins
  private readonly lineStartOffset: number[] = [];
  // leading whitespace trimmed off each line (restored when mapping columns)
  private readonly trimmedWs: number[] = [];

  constructor(
    allLines: string[],
    public readonly startLineIdx: number,
    endLineIdxExclusive: number,
    considerWhitespaceChanges: boolean,
  ) {
    for (let li = startLineIdx; li < endLineIdxExclusive; li++) {
      this.lineStartOffset.push(this.elements.length);
      let line = allLines[li]!;
      let trimmed = 0;
      if (!considerWhitespaceChanges) {
        const ts = line.replace(/^\s+/, "");
        trimmed = line.length - ts.length;
        line = ts.replace(/\s+$/, "");
      }
      this.trimmedWs.push(trimmed);
      for (let i = 0; i < line.length; i++) this.elements.push(line.charCodeAt(i));
      if (li < endLineIdxExclusive - 1) this.elements.push(10 /* \n */);
    }
  }

  get length(): number {
    return this.elements.length;
  }
  getElement(offset: number): number {
    return this.elements[offset]!;
  }
  isStronglyEqual(offset1: number, offset2: number): boolean {
    return this.elements[offset1] === this.elements[offset2];
  }
  getText(range: OffsetRange): string {
    let s = "";
    for (let i = range.start; i < range.endExclusive; i++) s += String.fromCharCode(this.elements[i]!);
    return s;
  }

  getBoundaryScore(length: number): number {
    const prev = getCategory(length > 0 ? this.elements[length - 1]! : -1);
    const next = getCategory(length < this.elements.length ? this.elements[length]! : -1);
    if (prev === CharCat.LineBreakCR && next === CharCat.LineBreakLF) return 0;
    if (prev === CharCat.LineBreakLF) return 150;
    let score = 0;
    if (prev !== next) {
      score += 10;
      if (prev === CharCat.WordLower && next === CharCat.WordUpper) score += 1;
    }
    score += CAT_SCORE[prev];
    score += CAT_SCORE[next];
    return score;
  }

  findWordContaining(offset: number): OffsetRange | undefined {
    if (offset < 0 || offset >= this.elements.length || !isWordChar(this.elements[offset]!)) return undefined;
    let start = offset;
    while (start > 0 && isWordChar(this.elements[start - 1]!)) start--;
    let end = offset;
    while (end < this.elements.length && isWordChar(this.elements[end]!)) end++;
    return new OffsetRange(start, end);
  }

  private lineIdxAtOffset(offset: number): number {
    // largest i with lineStartOffset[i] <= offset
    let lo = 0;
    let hi = this.lineStartOffset.length - 1;
    let res = 0;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (this.lineStartOffset[mid]! <= offset) {
        res = mid;
        lo = mid + 1;
      } else {
        hi = mid - 1;
      }
    }
    return res;
  }

  countLinesIn(range: OffsetRange): number {
    return this.lineIdxAtOffset(range.endExclusive) - this.lineIdxAtOffset(range.start);
  }

  extendToFullLines(range: OffsetRange): OffsetRange {
    const startLine = this.lineIdxAtOffset(range.start);
    const start = this.lineStartOffset[startLine]!;
    const endLine = this.lineIdxAtOffset(range.endExclusive);
    // end = start of the line after endLine, minus the trailing '\n', or EOF
    const end = endLine + 1 < this.lineStartOffset.length ? this.lineStartOffset[endLine + 1]! - 1 : this.elements.length;
    return new OffsetRange(start, end);
  }

  // The exclusive end offset of a line's content (excludes the trailing '\n').
  private lineContentEnd(lineIdx: number): number {
    return lineIdx + 1 < this.lineStartOffset.length ? this.lineStartOffset[lineIdx + 1]! - 1 : this.elements.length;
  }

  // Break a char range into per-line column ranges over the ORIGINAL (untrimmed)
  // line content, so the renderer can highlight just the changed spans.
  perLineRanges(range: OffsetRange): { lineIdx: number; start: number; end: number }[] {
    const out: { lineIdx: number; start: number; end: number }[] = [];
    const firstLine = this.lineIdxAtOffset(range.start);
    const lastLine = this.lineIdxAtOffset(Math.max(range.start, range.endExclusive - 1));
    for (let li = firstLine; li <= lastLine && li < this.lineStartOffset.length; li++) {
      const lineStart = this.lineStartOffset[li]!;
      const lineEnd = this.lineContentEnd(li);
      const s = Math.max(range.start, lineStart);
      const e = Math.min(range.endExclusive, lineEnd);
      if (s < e) {
        out.push({
          lineIdx: this.startLineIdx + li,
          start: this.trimmedWs[li]! + (s - lineStart),
          end: this.trimmedWs[li]! + (e - lineStart),
        });
      }
    }
    return out;
  }
}

// ---------------------------------------------------------------------------
// Heuristics (cleanup of raw diff output)
// ---------------------------------------------------------------------------

function optimizeSequenceDiffs(seq1: ISequence, seq2: ISequence, diffs: SequenceDiff[]): SequenceDiff[] {
  let result = joinSequenceDiffsByShifting(seq1, seq2, diffs);
  result = joinSequenceDiffsByShifting(seq1, seq2, result);
  result = shiftSequenceDiffs(seq1, seq2, result);
  return result;
}

function joinSequenceDiffsByShifting(seq1: ISequence, seq2: ISequence, diffs: SequenceDiff[]): SequenceDiff[] {
  if (diffs.length === 0) return diffs;
  const result: SequenceDiff[] = [diffs[0]!];

  for (let i = 1; i < diffs.length; i++) {
    const prev = result[result.length - 1]!;
    let cur = diffs[i]!;
    if (cur.seq1Range.isEmpty || cur.seq2Range.isEmpty) {
      const length = cur.seq1Range.start - prev.seq1Range.endExclusive;
      let d: number;
      for (d = 1; d <= length; d++) {
        if (
          seq1.getElement(cur.seq1Range.start - d) !== seq1.getElement(cur.seq1Range.endExclusive - d) ||
          seq2.getElement(cur.seq2Range.start - d) !== seq2.getElement(cur.seq2Range.endExclusive - d)
        ) {
          break;
        }
      }
      d--;
      if (d === length) {
        result[result.length - 1] = new SequenceDiff(
          new OffsetRange(prev.seq1Range.start, cur.seq1Range.endExclusive - length),
          new OffsetRange(prev.seq2Range.start, cur.seq2Range.endExclusive - length),
        );
        continue;
      }
      cur = cur.delta(-d);
    }
    result.push(cur);
  }

  const result2: SequenceDiff[] = [];
  for (let i = 0; i < result.length - 1; i++) {
    const next = result[i + 1]!;
    let cur = result[i]!;
    if (cur.seq1Range.isEmpty || cur.seq2Range.isEmpty) {
      const length = next.seq1Range.start - cur.seq1Range.endExclusive;
      let d: number;
      for (d = 0; d < length; d++) {
        if (
          !seq1.isStronglyEqual(cur.seq1Range.start + d, cur.seq1Range.endExclusive + d) ||
          !seq2.isStronglyEqual(cur.seq2Range.start + d, cur.seq2Range.endExclusive + d)
        ) {
          break;
        }
      }
      if (d === length) {
        result[i + 1] = new SequenceDiff(
          new OffsetRange(cur.seq1Range.start + length, next.seq1Range.endExclusive),
          new OffsetRange(cur.seq2Range.start + length, next.seq2Range.endExclusive),
        );
        continue;
      }
      if (d > 0) cur = cur.delta(d);
    }
    result2.push(cur);
  }
  if (result.length > 0) result2.push(result[result.length - 1]!);
  return result2;
}

function shiftSequenceDiffs(seq1: ISequence, seq2: ISequence, diffs: SequenceDiff[]): SequenceDiff[] {
  if (!seq1.getBoundaryScore || !seq2.getBoundaryScore) return diffs;
  for (let i = 0; i < diffs.length; i++) {
    const prev = i > 0 ? diffs[i - 1] : undefined;
    const diff = diffs[i]!;
    const next = i + 1 < diffs.length ? diffs[i + 1] : undefined;
    const seq1Valid = new OffsetRange(prev ? prev.seq1Range.endExclusive + 1 : 0, next ? next.seq1Range.start - 1 : seq1.length);
    const seq2Valid = new OffsetRange(prev ? prev.seq2Range.endExclusive + 1 : 0, next ? next.seq2Range.start - 1 : seq2.length);
    if (diff.seq1Range.isEmpty) {
      diffs[i] = shiftDiffToBetterPosition(diff, seq1, seq2, seq1Valid, seq2Valid);
    } else if (diff.seq2Range.isEmpty) {
      diffs[i] = shiftDiffToBetterPosition(diff.swap(), seq2, seq1, seq2Valid, seq1Valid).swap();
    }
  }
  return diffs;
}

function shiftDiffToBetterPosition(diff: SequenceDiff, seq1: ISequence, seq2: ISequence, seq1Valid: OffsetRange, seq2Valid: OffsetRange): SequenceDiff {
  const maxShiftLimit = 100;
  let deltaBefore = 1;
  while (
    diff.seq1Range.start - deltaBefore >= seq1Valid.start &&
    diff.seq2Range.start - deltaBefore >= seq2Valid.start &&
    seq2.isStronglyEqual(diff.seq2Range.start - deltaBefore, diff.seq2Range.endExclusive - deltaBefore) &&
    deltaBefore < maxShiftLimit
  ) {
    deltaBefore++;
  }
  deltaBefore--;

  let deltaAfter = 0;
  while (
    diff.seq1Range.start + deltaAfter < seq1Valid.endExclusive &&
    diff.seq2Range.endExclusive + deltaAfter < seq2Valid.endExclusive &&
    seq2.isStronglyEqual(diff.seq2Range.start + deltaAfter, diff.seq2Range.endExclusive + deltaAfter) &&
    deltaAfter < maxShiftLimit
  ) {
    deltaAfter++;
  }
  if (deltaBefore === 0 && deltaAfter === 0) return diff;

  let bestDelta = 0;
  let bestScore = -1;
  for (let delta = -deltaBefore; delta <= deltaAfter; delta++) {
    const seq2Start = diff.seq2Range.start + delta;
    const seq2End = diff.seq2Range.endExclusive + delta;
    const seq1Offset = diff.seq1Range.start + delta;
    const score = seq1.getBoundaryScore!(seq1Offset) + seq2.getBoundaryScore!(seq2Start) + seq2.getBoundaryScore!(seq2End);
    if (score > bestScore) {
      bestScore = score;
      bestDelta = delta;
    }
  }
  return diff.delta(bestDelta);
}

function removeShortMatches(diffs: SequenceDiff[]): SequenceDiff[] {
  const result: SequenceDiff[] = [];
  for (const s of diffs) {
    const last = result[result.length - 1];
    if (!last) {
      result.push(s);
      continue;
    }
    if (s.seq1Range.start - last.seq1Range.endExclusive <= 2 || s.seq2Range.start - last.seq2Range.endExclusive <= 2) {
      result[result.length - 1] = new SequenceDiff(last.seq1Range.join(s.seq1Range), last.seq2Range.join(s.seq2Range));
    } else {
      result.push(s);
    }
  }
  return result;
}

function mergeSequenceDiffs(a: SequenceDiff[], b: SequenceDiff[]): SequenceDiff[] {
  const result: SequenceDiff[] = [];
  const sd1 = [...a];
  const sd2 = [...b];
  while (sd1.length > 0 || sd2.length > 0) {
    const x = sd1[0];
    const y = sd2[0];
    let next: SequenceDiff;
    if (x && (!y || x.seq1Range.start < y.seq1Range.start)) next = sd1.shift()!;
    else next = sd2.shift()!;
    if (result.length > 0 && result[result.length - 1]!.seq1Range.endExclusive >= next.seq1Range.start) {
      result[result.length - 1] = result[result.length - 1]!.join(next);
    } else {
      result.push(next);
    }
  }
  return result;
}

function extendDiffsToEntireWordIfAppropriate(seq1: CharSlice, seq2: CharSlice, diffs: SequenceDiff[]): SequenceDiff[] {
  // The equal regions between the diffs. Both the outer loop and scanWord
  // consume from this single shared array (a word may span several regions).
  const equalMappings = SequenceDiff.invert(diffs, seq1.length);
  const additional: SequenceDiff[] = [];
  let lastPoint = new OffsetPair(0, 0);

  const scanWord = (pair: OffsetPair, equalMapping: SequenceDiff) => {
    if (pair.offset1 < lastPoint.offset1 || pair.offset2 < lastPoint.offset2) return;
    const w1 = seq1.findWordContaining(pair.offset1);
    const w2 = seq2.findWordContaining(pair.offset2);
    if (!w1 || !w2) return;
    let w = new SequenceDiff(w1, w2);
    const equalPart = w.intersect(equalMapping)!;
    let equalChars1 = equalPart.seq1Range.length;
    let equalChars2 = equalPart.seq2Range.length;

    while (equalMappings.length > 0) {
      const next = equalMappings[0]!;
      const intersects = next.seq1Range.intersects(w.seq1Range) || next.seq2Range.intersects(w.seq2Range);
      if (!intersects) break;
      const v1 = seq1.findWordContaining(next.seq1Range.start);
      const v2 = seq2.findWordContaining(next.seq2Range.start);
      const v = new SequenceDiff(v1!, v2!);
      const ep = v.intersect(next)!;
      equalChars1 += ep.seq1Range.length;
      equalChars2 += ep.seq2Range.length;
      w = w.join(v);
      if (w.seq1Range.endExclusive >= next.seq1Range.endExclusive) equalMappings.shift();
      else break;
    }

    // Extend to the whole word only when it's mostly a change (< 2/3 equal).
    if (equalChars1 + equalChars2 < ((w.seq1Range.length + w.seq2Range.length) * 2) / 3) {
      additional.push(w);
    }
    lastPoint = w.getEndExclusives();
  };

  while (equalMappings.length > 0) {
    const next = equalMappings.shift()!;
    if (next.seq1Range.isEmpty) continue;
    scanWord(next.getStarts(), next);
    // -1 gives a character known to be equal in both sides.
    scanWord(next.getEndExclusives().delta(-1), next);
  }

  return mergeSequenceDiffs(diffs, additional);
}

function removeVeryShortMatchingLinesBetweenDiffs(seq1: LineSequence, diffs: SequenceDiff[]): SequenceDiff[] {
  if (diffs.length === 0) return diffs;
  let counter = 0;
  let shouldRepeat: boolean;
  do {
    shouldRepeat = false;
    const result: SequenceDiff[] = [diffs[0]!];
    for (let i = 1; i < diffs.length; i++) {
      const cur = diffs[i]!;
      const last = result[result.length - 1]!;
      const unchanged = new OffsetRange(last.seq1Range.endExclusive, cur.seq1Range.start);
      const text = seq1.getText(unchanged).replace(/\s/g, "");
      const join =
        text.length <= 4 &&
        (last.seq1Range.length + last.seq2Range.length > 5 || cur.seq1Range.length + cur.seq2Range.length > 5);
      if (join) {
        shouldRepeat = true;
        result[result.length - 1] = last.join(cur);
      } else {
        result.push(cur);
      }
    }
    diffs = result;
  } while (counter++ < 10 && shouldRepeat);
  return diffs;
}

function removeVeryShortMatchingTextBetweenLongDiffs(seq1: CharSlice, seq2: CharSlice, diffs: SequenceDiff[]): SequenceDiff[] {
  if (diffs.length === 0) return diffs;
  let counter = 0;
  let shouldRepeat: boolean;
  do {
    shouldRepeat = false;
    const result: SequenceDiff[] = [diffs[0]!];
    for (let i = 1; i < diffs.length; i++) {
      const cur = diffs[i]!;
      const last = result[result.length - 1]!;
      const unchanged = new OffsetRange(last.seq1Range.endExclusive, cur.seq1Range.start);
      const unchangedLineCount = seq1.countLinesIn(unchanged);
      let join = false;
      if (!(unchangedLineCount > 5 || unchanged.length > 500)) {
        const text = seq1.getText(unchanged).trim();
        if (!(text.length > 20 || text.split(/\r\n|\r|\n/).length > 1)) {
          const cap = (v: number) => Math.min(v, 2 * 40 + 50);
          const max = 2 * 40 + 50;
          const before =
            Math.pow(Math.pow(cap(seq1.countLinesIn(last.seq1Range) * 40 + last.seq1Range.length), 1.5) + Math.pow(cap(seq2.countLinesIn(last.seq2Range) * 40 + last.seq2Range.length), 1.5), 1.5);
          const after =
            Math.pow(Math.pow(cap(seq1.countLinesIn(cur.seq1Range) * 40 + cur.seq1Range.length), 1.5) + Math.pow(cap(seq2.countLinesIn(cur.seq2Range) * 40 + cur.seq2Range.length), 1.5), 1.5);
          if (before + after > Math.pow(Math.pow(max, 1.5), 1.5) * 1.3) join = true;
        }
      }
      if (join) {
        shouldRepeat = true;
        result[result.length - 1] = last.join(cur);
      } else {
        result.push(cur);
      }
    }
    diffs = result;
  } while (counter++ < 10 && shouldRepeat);

  const newDiffs: SequenceDiff[] = [];
  forEachWithNeighbors(diffs, (prev, cur, next) => {
    let newDiff = cur;
    const shouldMark = (text: string) => text.length > 0 && text.trim().length <= 3 && cur.seq1Range.length + cur.seq2Range.length > 100;
    const fullRange1 = seq1.extendToFullLines(cur.seq1Range);
    const prefix = seq1.getText(new OffsetRange(fullRange1.start, cur.seq1Range.start));
    if (shouldMark(prefix)) newDiff = newDiff.deltaStart(-prefix.length);
    const suffix = seq1.getText(new OffsetRange(cur.seq1Range.endExclusive, fullRange1.endExclusive));
    if (shouldMark(suffix)) newDiff = newDiff.deltaEnd(suffix.length);
    const available = SequenceDiff.fromOffsetPairs(prev ? prev.getEndExclusives() : OffsetPair.zero, next ? next.getStarts() : OffsetPair.max);
    const result = newDiff.intersect(available)!;
    if (newDiffs.length > 0 && result.getStarts().equals(newDiffs[newDiffs.length - 1]!.getEndExclusives())) {
      newDiffs[newDiffs.length - 1] = newDiffs[newDiffs.length - 1]!.join(result);
    } else {
      newDiffs.push(result);
    }
  });
  return newDiffs;
}

// ---------------------------------------------------------------------------
// Orchestration
// ---------------------------------------------------------------------------

export type DiffOp =
  | { kind: "equal"; origIdx: number; modIdx: number }
  | { kind: "delete"; origIdx: number; ranges: [number, number][] }
  | { kind: "insert"; modIdx: number; ranges: [number, number][] };

function computeLineAlignment(originalLines: string[], modifiedLines: string[]): SequenceDiff[] {
  const perfectHashes = new Map<string, number>();
  const hash = (text: string): number => {
    let h = perfectHashes.get(text);
    if (h === undefined) {
      h = perfectHashes.size;
      perfectHashes.set(text, h);
    }
    return h;
  };
  const seq1 = new LineSequence(originalLines.map((l) => hash(l.trim())), originalLines);
  const seq2 = new LineSequence(modifiedLines.map((l) => hash(l.trim())), modifiedLines);

  let lineAlignments: SequenceDiff[];
  if (seq1.length + seq2.length < 1700 && seq1.length * seq2.length <= MAX_DP_CELLS) {
    lineAlignments = dynamicProgrammingDiffing(seq1, seq2, (o1, o2) =>
      originalLines[o1] === modifiedLines[o2] ? (modifiedLines[o2]!.length === 0 ? 0.1 : 1 + Math.log(1 + modifiedLines[o2]!.length)) : 0.99,
    );
  } else {
    lineAlignments = myersDiffAlgorithm(seq1, seq2);
  }
  lineAlignments = optimizeSequenceDiffs(seq1, seq2, lineAlignments);
  lineAlignments = removeVeryShortMatchingLinesBetweenDiffs(seq1, lineAlignments);
  return lineAlignments;
}

// Character refinement for one changed region. Returns the changed column
// ranges per original line (deletions) and per modified line (insertions).
function refineRegion(
  originalLines: string[],
  modifiedLines: string[],
  diff: SequenceDiff,
  considerWhitespaceChanges: boolean,
): { del: Map<number, [number, number][]>; ins: Map<number, [number, number][]> } {
  const slice1 = new CharSlice(originalLines, diff.seq1Range.start, diff.seq1Range.endExclusive, considerWhitespaceChanges);
  const slice2 = new CharSlice(modifiedLines, diff.seq2Range.start, diff.seq2Range.endExclusive, considerWhitespaceChanges);

  let diffs =
    slice1.length + slice2.length < 500 && slice1.length * slice2.length <= MAX_DP_CELLS
      ? dynamicProgrammingDiffing(slice1, slice2)
      : myersDiffAlgorithm(slice1, slice2);

  diffs = optimizeSequenceDiffs(slice1, slice2, diffs);
  diffs = extendDiffsToEntireWordIfAppropriate(slice1, slice2, diffs);
  diffs = removeShortMatches(diffs);
  diffs = removeVeryShortMatchingTextBetweenLongDiffs(slice1, slice2, diffs);

  const del = new Map<number, [number, number][]>();
  const ins = new Map<number, [number, number][]>();
  const add = (map: Map<number, [number, number][]>, lineIdx: number, s: number, e: number) => {
    if (e <= s) return;
    const arr = map.get(lineIdx) ?? [];
    arr.push([s, e]);
    map.set(lineIdx, arr);
  };
  for (const d of diffs) {
    for (const r of slice1.perLineRanges(d.seq1Range)) add(del, r.lineIdx, r.start, r.end);
    for (const r of slice2.perLineRanges(d.seq2Range)) add(ins, r.lineIdx, r.start, r.end);
  }
  return { del, ins };
}

/**
 * Compute a VSCode-quality diff between two arrays of lines and return an
 * ordered op list covering every line. `ignoreTrimWhitespace` (default true,
 * matching VSCode) makes re-indented lines align as unchanged context.
 */
export function computeDiffOps(originalLines: string[], modifiedLines: string[], ignoreTrimWhitespace = true): DiffOp[] {
  const considerWhitespaceChanges = !ignoreTrimWhitespace;
  const lineAlignments = computeLineAlignment(originalLines, modifiedLines);

  const ops: DiffOp[] = [];
  let seq1Pos = 0;
  let seq2Pos = 0;

  for (const diff of lineAlignments) {
    // Equal lines before this change.
    while (seq1Pos < diff.seq1Range.start) {
      ops.push({ kind: "equal", origIdx: seq1Pos, modIdx: seq2Pos });
      seq1Pos++;
      seq2Pos++;
    }

    const dCount = diff.seq1Range.length;
    const aCount = diff.seq2Range.length;
    let refined: { del: Map<number, [number, number][]>; ins: Map<number, [number, number][]> } | null = null;
    if (dCount > 0 && aCount > 0) {
      refined = refineRegion(originalLines, modifiedLines, diff, considerWhitespaceChanges);
    }
    for (let i = diff.seq1Range.start; i < diff.seq1Range.endExclusive; i++) {
      ops.push({ kind: "delete", origIdx: i, ranges: refined?.del.get(i) ?? [] });
    }
    for (let j = diff.seq2Range.start; j < diff.seq2Range.endExclusive; j++) {
      ops.push({ kind: "insert", modIdx: j, ranges: refined?.ins.get(j) ?? [] });
    }
    seq1Pos = diff.seq1Range.endExclusive;
    seq2Pos = diff.seq2Range.endExclusive;
  }

  // Trailing equal lines.
  while (seq1Pos < originalLines.length) {
    ops.push({ kind: "equal", origIdx: seq1Pos, modIdx: seq2Pos });
    seq1Pos++;
    seq2Pos++;
  }
  return ops;
}
