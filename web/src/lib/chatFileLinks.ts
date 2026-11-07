import { Children, isValidElement, type ReactNode } from "react";

export interface ChatFileLink {
  path: string;
  line?: number;
}

function labelText(children: ReactNode): string {
  return Children.toArray(children).map((child): string => {
    if (typeof child === "string" || typeof child === "number") return String(child);
    if (isValidElement<{ children?: ReactNode }>(child)) return labelText(child.props.children);
    return "";
  }).join("");
}

// Markdown labels may already include a location, including inside inline code
// or emphasis. Preserve those labels without appending another line number.
export function chatFileLineSuffix(children: ReactNode, line?: number): string {
  if (!line || /(?::\d+(?::\d+)?(?:-\d+)?|#L\d+(?:-L?\d+)?)\s*$/i.test(labelText(children))) return "";
  return `:${line}`;
}

// Chat links refer to paths in the environment that owns the session. Web
// URLs and fragment-only links remain ordinary Markdown links.
export function parseChatFileLink(href: string | undefined): ChatFileLink | null {
  if (!href || href.startsWith("#") || href.startsWith("//")) return null;
  let value = href;
  if (/^file:/i.test(value)) {
    try {
      const url = new URL(value);
      if (url.hostname && url.hostname !== "localhost") return null;
      value = url.pathname + url.hash;
    } catch { return null; }
  } else if (/^[a-z][a-z\d+.-]*:/i.test(value) && !/^[a-z]:[\\/]/i.test(value) && !/^[a-z][a-z\d_.-]*:\d+(?::\d+)?$/i.test(value)) {
    return null;
  }
  try { value = decodeURIComponent(value); } catch { return null; }
  const hash = /#L(\d+)(?:-L?\d+)?$/i.exec(value);
  let line: number | undefined;
  if (hash) { line = Number(hash[1]); value = value.slice(0, -hash[0].length); }
  else {
    const suffix = /:(\d+)(?::\d+)?$/.exec(value);
    if (suffix) { line = Number(suffix[1]); value = value.slice(0, -suffix[0].length); }
  }
  if (!value || /[?#]/.test(value) || line === 0 || (line !== undefined && !Number.isSafeInteger(line))) return null;
  // A bare relative name is a file link when it has an extension or is a
  // conventional extensionless filename; paths with slashes need no suffix.
  if (!value.includes("/") && !value.includes("\\") && !/\.[a-z\d_-]+$/i.test(value) && !/^(README|LICENSE|Makefile|Dockerfile)$/i.test(value)) return null;
  return { path: value, line };
}
