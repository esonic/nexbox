import { useState } from "react";
import { Loader2, X, Eye, EyeOff } from "lucide-react";
import { cn } from "@/lib/utils";

interface EnvVarDef {
  name: string;
  description?: string;
  required?: boolean;
}

export interface ProjectOption {
  id: string;
  name: string;
}

interface InstallDialogProps {
  packageName: string;
  packageTitle: string;
  envVars: EnvVarDef[];
  /** If provided, shows a project scope selector (for skills). */
  projects?: ProjectOption[];
  /** Initial selected project ID; "" = All Projects */
  defaultProjectId?: string;
  onConfirm: (env: Record<string, string>, projectId: string) => Promise<void>;
  onCancel: () => void;
}

export function InstallDialog({
  packageName,
  packageTitle,
  envVars,
  projects,
  defaultProjectId = "",
  onConfirm,
  onCancel,
}: InstallDialogProps) {
  const [values, setValues] = useState<Record<string, string>>(
    Object.fromEntries(envVars.map((v) => [v.name, ""]))
  );
  const [visible, setVisible] = useState<Record<string, boolean>>({});
  const [projectId, setProjectId] = useState(defaultProjectId);
  const [installing, setInstalling] = useState(false);

  const allRequired = envVars
    .filter((v) => v.required)
    .every((v) => values[v.name]?.trim());

  const hasContent = envVars.length > 0 || !!projects;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!allRequired) return;
    setInstalling(true);
    try {
      const env = Object.fromEntries(
        Object.entries(values).filter(([, v]) => v.trim())
      );
      await onConfirm(env, projectId);
    } finally {
      setInstalling(false);
    }
  };

  const subtitle = envVars.length > 0
    ? projects
      ? "Configure environment variables and install scope"
      : "Configure required environment variables"
    : projects
      ? "Choose where to install this skill"
      : null;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="bg-card border rounded-xl shadow-xl w-full max-w-md mx-4">
        <div className="flex items-center justify-between px-5 py-4 border-b">
          <div>
            <h2 className="font-semibold text-sm">Install {packageTitle}</h2>
            {subtitle && (
              <p className="text-xs text-muted-foreground mt-0.5">{subtitle}</p>
            )}
          </div>
          <button
            onClick={onCancel}
            className="p-1 rounded hover:bg-accent transition-colors text-muted-foreground"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        <form onSubmit={handleSubmit} className="p-5 space-y-4">
          {/* Project scope selector (skills only) */}
          {projects && (
            <div className="space-y-1.5">
              <label className="text-sm font-medium">Install scope</label>
              <p className="text-xs text-muted-foreground">
                "All Projects" installs into every project's <code className="font-mono">.claude/skills/</code> folder.
              </p>
              <select
                value={projectId}
                onChange={(e) => setProjectId(e.target.value)}
                className="w-full px-3 py-2 text-sm bg-background border rounded-lg outline-none focus:ring-1 focus:ring-primary"
              >
                <option value="">All Projects</option>
                {projects.map((p) => (
                  <option key={p.id} value={p.id}>{p.name}</option>
                ))}
              </select>
            </div>
          )}

          {/* Env vars / headers */}
          {envVars.map((v) => {
            const isHeader = v.name.startsWith("header:");
            const displayName = isHeader ? v.name.slice("header:".length) : v.name;
            const displayGroup = isHeader ? "HTTP Header" : "Env Var";
            return (
              <div key={v.name} className="space-y-1.5">
                <label className="text-sm font-medium flex items-center gap-2">
                  {displayName}
                  {v.required && <span className="text-destructive">*</span>}
                  <span className="text-[10px] font-normal text-muted-foreground border rounded px-1.5 py-0.5">{displayGroup}</span>
                </label>
                {v.description && (
                  <p className="text-xs text-muted-foreground">{v.description}</p>
                )}
                <div className="relative">
                  <input
                    type={visible[v.name] ? "text" : "password"}
                    value={values[v.name] || ""}
                    onChange={(e) =>
                      setValues((prev) => ({ ...prev, [v.name]: e.target.value }))
                    }
                    placeholder={v.required ? "Required" : "Optional"}
                    className={cn(
                      "w-full pr-9 px-3 py-2 text-sm bg-background border rounded-lg outline-none focus:ring-1 focus:ring-primary font-mono",
                      v.required && !values[v.name]?.trim() && "border-destructive/50"
                    )}
                  />
                  <button
                    type="button"
                    onClick={() =>
                      setVisible((prev) => ({ ...prev, [v.name]: !prev[v.name] }))
                    }
                    className="absolute right-2 top-1/2 -translate-y-1/2 p-0.5 text-muted-foreground hover:text-foreground"
                  >
                    {visible[v.name] ? (
                      <EyeOff className="h-3.5 w-3.5" />
                    ) : (
                      <Eye className="h-3.5 w-3.5" />
                    )}
                  </button>
                </div>
              </div>
            );
          })}

          {envVars.length > 0 && (
            <p className="text-xs text-muted-foreground">
              These values are stored locally and injected at runtime.
            </p>
          )}

          <div className="flex justify-end gap-2 pt-1">
            <button
              type="button"
              onClick={onCancel}
              className="px-3 py-1.5 text-sm border rounded-lg hover:bg-accent transition-colors"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={!allRequired || installing}
              className="flex items-center gap-1.5 px-3 py-1.5 text-sm bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 disabled:opacity-50 transition-colors"
            >
              {installing ? (
                <>
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  Installing...
                </>
              ) : (
                "Install"
              )}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// Parse user-configurable fields from an MCP manifest string.
//
// Convention:
//   env:   empty string value  → required user input, stored as env var
//          non-empty value     → optional (value used as description/default hint)
//   headers: same convention, stored with "header:" prefix so the runtime can
//            inject them as HTTP headers instead of process env vars
//
// The returned `name` field for headers is "header:HeaderName" so callers can
// tell them apart and store them correctly in env_values.
export function parseEnvVarDefs(manifestStr: string): EnvVarDef[] {
  try {
    const parsed = JSON.parse(manifestStr);
    // Standard mcpServers format
    if (parsed?.mcpServers && typeof parsed.mcpServers === "object") {
      const vars: EnvVarDef[] = [];
      for (const server of Object.values(parsed.mcpServers) as any[]) {
        // env vars (stdio transport)
        if (server?.env && typeof server.env === "object") {
          for (const [key, val] of Object.entries(server.env) as [string, string][]) {
            vars.push({
              name: key,
              description: val || undefined,
              required: !val,
            });
          }
        }
        // HTTP headers (http/sse transport)
        if (server?.headers && typeof server.headers === "object") {
          for (const [key, val] of Object.entries(server.headers) as [string, string][]) {
            vars.push({
              name: `header:${key}`,
              description: val || undefined,
              required: !val,
            });
          }
        }
      }
      return vars;
    }
    // Legacy format with config.env_vars
    const envVars = parsed?.config?.env_vars;
    if (Array.isArray(envVars)) {
      return envVars.filter(
        (v: any) => typeof v === "object" && typeof v.name === "string"
      );
    }
  } catch {
    // ignore
  }
  return [];
}
