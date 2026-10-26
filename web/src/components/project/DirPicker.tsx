import { useState, useEffect, useRef } from "react";
import { toast } from "sonner";
import { ChevronRight, FolderOpen, Loader2, X } from "lucide-react";
import { Input } from "@/components/ui/input";
import { utilApi } from "@/lib/api";

interface DirPickerProps {
  value: string;
  onChange: (v: string) => void;
  id?: string;
  placeholder?: string;
}

const joinPath = (base: string, name: string) =>
  base === "/" ? `/${name}` : `${base}/${name}`;

export function DirPicker({
  value,
  onChange,
  id,
  placeholder = "/path/to/directory",
}: DirPickerProps) {
  const [dirs, setDirs] = useState<string[]>([]);
  const [browsePath, setBrowsePath] = useState("");
  const [loading, setLoading] = useState(false);
  const crumbsRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = crumbsRef.current;
    if (el) el.scrollLeft = el.scrollWidth;
  }, [browsePath]);

  // Navigating into a directory also makes it the chosen value.
  const browse = async (path?: string, syncValue = true) => {
    setLoading(true);
    try {
      const resp = await utilApi.browseDir(path);
      const current = resp.current || path || "";
      setDirs(resp.dirs?.map((d: { name: string }) => d.name) || []);
      setBrowsePath(current);
      if (syncValue && current) onChange(current);
    } catch (e) {
      toast.error("Failed to browse directory", { description: String(e) });
    } finally {
      setLoading(false);
    }
  };

  const close = () => {
    setBrowsePath("");
    setDirs([]);
  };

  const segments = browsePath.split("/").filter(Boolean);

  return (
    <div className="space-y-1.5">
      <div className="flex items-center gap-1.5">
        <Input
          id={id}
          value={value}
          onChange={(e) => {
            onChange(e.target.value);
            close();
          }}
          placeholder={placeholder}
          className="h-8 text-sm font-mono flex-1 min-w-0"
        />
        <button
          type="button"
          onClick={() => browse(value.trim() || undefined, false)}
          className="h-8 px-2.5 rounded-md border bg-background text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors shrink-0 flex items-center gap-1"
          title="Browse directories"
        >
          <FolderOpen className="h-3.5 w-3.5" />
        </button>
      </div>

      {browsePath && (
        <div className="border rounded-md bg-background overflow-hidden mt-2">
          <div className="flex items-center gap-1 px-2 py-1.5 border-b bg-muted/30 min-w-0">
            <div
              ref={crumbsRef}
              className="flex items-center flex-1 min-w-0 overflow-x-auto whitespace-nowrap [scrollbar-width:none]"
            >
              <button
                type="button"
                onClick={() => browse("/")}
                className="px-1 py-0.5 rounded text-[11px] font-mono text-muted-foreground hover:text-foreground hover:bg-accent shrink-0"
              >
                /
              </button>
              {segments.map((seg, i) => {
                const target = "/" + segments.slice(0, i + 1).join("/");
                const isLast = i === segments.length - 1;
                return (
                  <span key={target} className="flex items-center shrink-0">
                    {i > 0 && (
                      <ChevronRight className="h-3 w-3 text-muted-foreground/40" />
                    )}
                    <button
                      type="button"
                      onClick={() => browse(target)}
                      className={`px-1 py-0.5 rounded text-[11px] font-mono hover:bg-accent ${
                        isLast
                          ? "text-foreground font-medium"
                          : "text-muted-foreground hover:text-foreground"
                      }`}
                    >
                      {seg}
                    </button>
                  </span>
                );
              })}
            </div>
            <button
              type="button"
              onClick={close}
              className="p-0.5 rounded hover:bg-accent text-muted-foreground/60 hover:text-foreground transition-colors shrink-0"
              title="Close browser"
            >
              <X className="h-3 w-3" />
            </button>
          </div>

          <div className="max-h-44 overflow-y-auto">
            {loading ? (
              <div className="flex items-center justify-center py-3">
                <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />
              </div>
            ) : dirs.length === 0 ? (
              <div className="text-[11px] text-muted-foreground/60 text-center py-3">
                No subdirectories
              </div>
            ) : (
              dirs.map((dir) => {
                const full = joinPath(browsePath, dir);
                return (
                  <button
                    key={dir}
                    type="button"
                    onClick={() => browse(full)}
                    className="flex items-center gap-1.5 w-full px-2.5 py-1.5 text-left hover:bg-accent/50 transition-colors"
                  >
                    <FolderOpen className="h-3 w-3 text-muted-foreground/60 shrink-0" />
                    <span className="text-[11px] truncate">{dir}</span>
                  </button>
                );
              })
            )}
          </div>
        </div>
      )}
    </div>
  );
}
