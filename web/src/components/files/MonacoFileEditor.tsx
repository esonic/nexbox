import { useEffect, useRef, useState } from "react";
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

export default function MonacoFileEditor({
  filepath,
  value,
  onChange,
  onSave,
}: {
  filepath: string;
  value: string;
  onChange: (next: string) => void;
  onSave?: () => void;
}) {
  const [theme, setTheme] = useState<"vs-dark" | "vs-light">(getDocumentTheme);

  const onSaveRef = useRef(onSave);
  useEffect(() => {
    onSaveRef.current = onSave;
  }, [onSave]);

  useEffect(() => {
    const obs = new MutationObserver(() => setTheme(getDocumentTheme()));
    obs.observe(document.body, { attributes: true, attributeFilter: ["class"] });
    return () => obs.disconnect();
  }, []);

  const handleMount: OnMount = (editor, monaco) => {
    editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => {
      onSaveRef.current?.();
    });
  };

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
      }}
    />
  );
}
