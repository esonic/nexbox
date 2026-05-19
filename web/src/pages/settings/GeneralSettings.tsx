import { useState, useEffect } from "react";
import { toast } from "sonner";
import { serverApi, authApi } from "@/lib/api";
import { ChevronDown, LogOut, Loader2 } from "lucide-react";

const AGENT_LABELS: Record<string, string> = {
  codex: "Codex",
  claude: "Claude Code",
};

export function GeneralSettings() {
  const [agents, setAgents] = useState<string[]>([]);
  const [defaultAgent, setDefaultAgent] = useState("");
  const [saving, setSaving] = useState(false);

  const [chromeHeadless, setChromeHeadless] = useState(true);
  const [enableFileEdit, setEnableFileEdit] = useState(false);
  const [signingOut, setSigningOut] = useState(false);

  useEffect(() => {
    serverApi.availableAgents()
      .then((data) => {
        setAgents(data.agents || []);
        setDefaultAgent(data.default || "");
      })
      .catch((e) => console.error("Failed to load agents:", e));

    serverApi.getSettings()
      .then((data) => {
        setChromeHeadless(!!data.chrome_headless);
        setEnableFileEdit(!!data.enable_file_edit);
      })
      .catch((e) => console.error("Failed to load settings:", e));
  }, []);

  const handleChromeHeadlessChange = async (next: boolean) => {
    if (next === chromeHeadless || saving) return;
    const prev = chromeHeadless;
    setChromeHeadless(next);
    setSaving(true);
    try {
      await serverApi.updateSettings({ chrome_headless: next });
    } catch (e) {
      setChromeHeadless(prev);
      console.error("Failed to update chrome headless:", e);
      toast.error("Failed to update setting", { description: String(e) });
    } finally {
      setSaving(false);
    }
  };

  const handleFileEditChange = async (next: boolean) => {
    if (next === enableFileEdit || saving) return;
    const prev = enableFileEdit;
    setEnableFileEdit(next);
    setSaving(true);
    try {
      await serverApi.updateSettings({ enable_file_edit: next });
    } catch (e) {
      setEnableFileEdit(prev);
      console.error("Failed to update file edit:", e);
      toast.error("Failed to update setting", { description: String(e) });
    } finally {
      setSaving(false);
    }
  };

  const handleAgentChange = async (agentId: string) => {
    if (agentId === defaultAgent || saving) return;
    setSaving(true);
    try {
      const res = await serverApi.setDefaultAgent(agentId);
      setDefaultAgent(res.default);
    } catch (e) {
      console.error("Failed to set default agent:", e);
      toast.error("Failed to set default agent", { description: String(e) });
    } finally {
      setSaving(false);
    }
  };

  const handleSignOut = async () => {
    if (signingOut) return;
    setSigningOut(true);
    try {
      await authApi.logout();
    } catch (e) {
      console.error("Failed to sign out:", e);
      // Still bounce to /login on failure — the cookie may already be cleared.
    } finally {
      window.location.assign("/login");
    }
  };

  return (
    <section className="space-y-4">
      {/* Default Agent */}
      <div className="rounded-lg border bg-card">
        <div className="flex items-center justify-between px-4 py-3">
          <div>
            <div className="text-sm font-medium">Default Agent</div>
            <div className="text-xs text-muted-foreground mt-0.5">New sessions will use this agent</div>
          </div>
          <div className="relative">
            <select
              value={defaultAgent}
              onChange={(e) => handleAgentChange(e.target.value)}
              disabled={saving || agents.length === 0}
              className="appearance-none bg-background border rounded-md px-3 py-1.5 pr-8 text-sm font-medium cursor-pointer hover:bg-accent/50 transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-1 disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {agents.map((id) => (
                <option key={id} value={id}>
                  {AGENT_LABELS[id] || id}
                </option>
              ))}
            </select>
            <ChevronDown className="absolute right-2 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground pointer-events-none" />
          </div>
        </div>
      </div>

      {/* Chrome Headless Mode (E2E Browser Testing) */}
      <div className="rounded-lg border bg-card">
        <div className="flex items-center justify-between px-4 py-3">
          <div>
            <div className="text-sm font-medium">Chrome Headless Mode</div>
            <div className="text-xs text-muted-foreground mt-0.5">
              Run E2E browser tests without a visible window.
            </div>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked={chromeHeadless}
            disabled={saving}
            onClick={() => handleChromeHeadlessChange(!chromeHeadless)}
            className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 disabled:opacity-50 disabled:cursor-not-allowed ${chromeHeadless ? "bg-primary" : "bg-input"}`}
          >
            <span
              className={`pointer-events-none inline-block h-5 w-5 transform rounded-full bg-background shadow ring-0 transition-transform ${chromeHeadless ? "translate-x-5" : "translate-x-0"}`}
            />
          </button>
        </div>
      </div>

      {/* Enable file editing from the Files panel */}
      <div className="rounded-lg border bg-card">
        <div className="flex items-center justify-between px-4 py-3">
          <div>
            <div className="text-sm font-medium">Enable File Editing</div>
            <div className="text-xs text-muted-foreground mt-0.5">
              Allow editing, uploading and deleting files from the Files panel.
            </div>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked={enableFileEdit}
            disabled={saving}
            onClick={() => handleFileEditChange(!enableFileEdit)}
            className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 disabled:opacity-50 disabled:cursor-not-allowed ${enableFileEdit ? "bg-primary" : "bg-input"}`}
          >
            <span
              className={`pointer-events-none inline-block h-5 w-5 transform rounded-full bg-background shadow ring-0 transition-transform ${enableFileEdit ? "translate-x-5" : "translate-x-0"}`}
            />
          </button>
        </div>
      </div>

      {/* Sign out — visually distinct, last item */}
      <div className="flex justify-end pt-4">
        <button
          type="button"
          onClick={handleSignOut}
          disabled={signingOut}
          className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium text-destructive hover:bg-destructive/10 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {signingOut ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <LogOut className="h-3.5 w-3.5" />}
          Sign out
        </button>
      </div>
    </section>
  );
}
