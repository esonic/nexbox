/**
 * Diff Parser - parses unified diff format into structured file diffs.
 * Based on 1code's diff-parser.ts
 */

export interface ParsedDiffFile {
  key: string;
  oldPath: string;
  newPath: string;
  diffText: string;
  isBinary: boolean;
  additions: number;
  deletions: number;
  isNewFile: boolean;
  isDeletedFile: boolean;
  fileLang: string | null;
  workspacePath?: string;
}

export function getFilePath(file: ParsedDiffFile) {
  return file.isNewFile ? file.newPath : file.isDeletedFile ? file.oldPath : file.newPath || file.oldPath;
}

function decodeGitPathToken(token: string): string {
  if (!token.startsWith('"') || !token.endsWith('"')) return token;
  const bytes: number[] = [];
  const encoder = new TextEncoder();
  const escapes: Record<string, number> = {
    a: 0x07,
    b: 0x08,
    t: 0x09,
    n: 0x0a,
    v: 0x0b,
    f: 0x0c,
    r: 0x0d,
    '"': 0x22,
    "\\": 0x5c,
  };
  for (let index = 1; index < token.length - 1;) {
    const character = token[index]!;
    if (character !== "\\") {
      const codePoint = token.codePointAt(index)!;
      const literal = String.fromCodePoint(codePoint);
      bytes.push(...encoder.encode(literal));
      index += literal.length;
      continue;
    }
    index++;
    const escaped = token[index];
    if (escaped === undefined) break;
    if (escaped >= "0" && escaped <= "7") {
      let octal = escaped;
      index++;
      while (octal.length < 3 && token[index] !== undefined && token[index]! >= "0" && token[index]! <= "7") {
        octal += token[index]!;
        index++;
      }
      bytes.push(parseInt(octal, 8));
      continue;
    }
    bytes.push(escapes[escaped] ?? escaped.charCodeAt(0));
    index++;
  }
  return new TextDecoder().decode(new Uint8Array(bytes));
}

function splitGitHeaderPaths(line: string): [string, string] | null {
  const input = line.slice("diff --git ".length);
  if (input.startsWith("a/") && !input.startsWith('"')) {
    const candidates: Array<{ oldPath: string; newPath: string; split: number }> = [];
    let searchFrom = 2;
    while (searchFrom < input.length) {
      const split = input.indexOf(" b/", searchFrom);
      if (split < 0) break;
      const oldPath = input.slice(2, split);
      const newPath = input.slice(split + 3);
      if (oldPath === newPath) return [oldPath, newPath];
      candidates.push({ oldPath, newPath, split });
      searchFrom = split + 1;
    }
    if (candidates.length > 0) {
      // Rename/copy metadata or ---/+++ lines below will replace this fallback.
      // Picking the separator nearest the midpoint is the least ambiguous
      // choice for the rare header where old and new paths differ.
      const midpoint = input.length / 2;
      candidates.sort((left, right) => Math.abs(left.split - midpoint) - Math.abs(right.split - midpoint));
      return [candidates[0]!.oldPath, candidates[0]!.newPath];
    }
  }
  const tokens: string[] = [];
  let index = 0;
  while (index < input.length && tokens.length < 2) {
    while (input[index] === " ") index++;
    if (index >= input.length) break;
    const start = index;
    if (input[index] === '"') {
      index++;
      let escaped = false;
      while (index < input.length) {
        const character = input[index++];
        if (character === '"' && !escaped) break;
        if (character === "\\" && !escaped) escaped = true;
        else escaped = false;
      }
    } else {
      while (index < input.length && input[index] !== " ") index++;
    }
    tokens.push(input.slice(start, index));
  }
  if (tokens.length !== 2) return null;
  const oldPath = decodeGitPathToken(tokens[0]!);
  const newPath = decodeGitPathToken(tokens[1]!);
  return [oldPath.startsWith("a/") ? oldPath.slice(2) : oldPath, newPath.startsWith("b/") ? newPath.slice(2) : newPath];
}

const LANG_MAP: Record<string, string> = {
  ts: "typescript",
  tsx: "typescript",
  js: "javascript",
  jsx: "javascript",
  css: "css",
  json: "json",
  md: "markdown",
  html: "html",
  xml: "xml",
  yaml: "yaml",
  yml: "yaml",
  py: "python",
  rb: "ruby",
  rs: "rust",
  go: "go",
  java: "java",
  c: "c",
  cpp: "cpp",
  h: "c",
  hpp: "cpp",
  sh: "bash",
  sql: "sql",
  vue: "vue",
};

function getFileLang(filePath: string): string | null {
  if (!filePath || filePath === "/dev/null") return null;
  const ext = filePath.split(".").pop()?.toLowerCase() || "";
  return LANG_MAP[ext] || ext || null;
}

/**
 * Split a unified diff string into per-file ParsedDiffFile objects.
 */
export function splitUnifiedDiffByFile(diffText: string): ParsedDiffFile[] {
  if (!diffText || !diffText.trim()) {
    return [];
  }

  const normalized = diffText.replace(/\r\n/g, "\n");
  const lines = normalized.split("\n");

  const blocks: string[] = [];
  let current: string[] = [];

  const pushCurrent = () => {
    const text = current.join("\n").trim();
    if (
      text &&
      (text.startsWith("diff --git ") ||
        text.startsWith("--- ") ||
        text.startsWith("+++ ") ||
        text.startsWith("Binary files ") ||
        text.includes("\n+++ ") ||
        text.includes("\nBinary files "))
    ) {
      blocks.push(text);
    }
    current = [];
  };

  for (const line of lines) {
    if (line.startsWith("diff --git ") && current.length > 0) {
      pushCurrent();
    }
    current.push(line);
  }
  pushCurrent();

  return blocks.map((blockText, index) => {
    const blockLines = blockText.split("\n");
    let oldPath = "";
    let newPath = "";
    let isBinary = false;
    let additions = 0;
    let deletions = 0;

    for (const line of blockLines) {
      if (line.startsWith("diff --git ")) {
        const paths = splitGitHeaderPaths(line);
        if (paths) {
          if (!oldPath) oldPath = paths[0];
          if (!newPath) newPath = paths[1];
        }
      }

      if (line.startsWith("Binary files ") && line.endsWith(" differ")) {
        isBinary = true;
      }

      if (line.startsWith("rename from ") || line.startsWith("copy from ")) {
        oldPath = decodeGitPathToken(line.slice(line.indexOf(" from ") + 6));
      }

      if (line.startsWith("rename to ") || line.startsWith("copy to ")) {
        newPath = decodeGitPathToken(line.slice(line.indexOf(" to ") + 4));
      }

      if (line.startsWith("--- ")) {
        const raw = decodeGitPathToken(line.slice(4));
        oldPath = raw.startsWith("a/") ? raw.slice(2) : raw;
      }

      if (line.startsWith("+++ ")) {
        const raw = decodeGitPathToken(line.slice(4));
        newPath = raw.startsWith("b/") ? raw.slice(2) : raw;
      }

      if (line.startsWith("+") && !line.startsWith("+++ ")) {
        additions += 1;
      } else if (line.startsWith("-") && !line.startsWith("--- ")) {
        deletions += 1;
      }
    }

    const key = oldPath || newPath ? `${oldPath}->${newPath}` : `file-${index}`;
    const isNewFile = oldPath === "/dev/null";
    const isDeletedFile = newPath === "/dev/null";
    const actualPath = isNewFile ? newPath : isDeletedFile ? oldPath : newPath || oldPath;
    const fileLang = getFileLang(actualPath);

    return {
      key,
      oldPath,
      newPath,
      diffText: blockText,
      isBinary,
      additions,
      deletions,
      isNewFile,
      isDeletedFile,
      fileLang,
    };
  });
}
