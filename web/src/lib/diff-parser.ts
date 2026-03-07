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
        const match = line.match(/^diff --git a\/(.+) b\/(.+)$/);
        if (match) {
          if (!oldPath) oldPath = match[1]!;
          if (!newPath) newPath = match[2]!;
        }
      }

      if (line.startsWith("Binary files ") && line.endsWith(" differ")) {
        isBinary = true;
      }

      if (line.startsWith("--- ")) {
        const raw = line.slice(4).trim();
        oldPath = raw.startsWith("a/") ? raw.slice(2) : raw;
      }

      if (line.startsWith("+++ ")) {
        const raw = line.slice(4).trim();
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
