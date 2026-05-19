import { useState, useEffect } from "react";
import { toast } from "sonner";
import { serverApi } from "@/lib/api";
import { ChevronDown, Check } from "lucide-react";

const AGENT_LABELS: Record<string, string> = {
  codex: "Codex",
  claude: "Claude Code",
};

export function GeneralSettings() {
  const [agents, setAgents] = useState<string[]>([]);
  const [defaultAgent, setDefaultAgent] = useState("");
  const [saving, setSaving] = useState(false);

  const [hasGitlabToken, setHasGitlabToken] = useState(false);
  const [maskedGitlabToken, setMaskedGitlabToken] = useState("");
  const [gitlabTokenInput, setGitlabTokenInput] = useState("");
  const [showGitlabTokenInput, setShowGitlabTokenInput] = useState(false);
  const [gitlabTokenSaved, setGitlabTokenSaved] = useState(false);

  const [chromeHeadless, setChromeHeadless] = useState(false);

  useEffect(() => {
    serverApi.availableAgents()
      .then((data) => {
        setAgents(data.agents || []);
        setDefaultAgent(data.default || "");
      })
      .catch((e) => console.error("Failed to load agents:", e));

    serverApi.getSettings()
      .then((data) => {
        setHasGitlabToken(data.has_gitlab_token);
        setMaskedGitlabToken(data.masked_gitlab_token);
        setChromeHeadless(!!data.chrome_headless);
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

  const handleSaveGitlabToken = async () => {
    setSaving(true);
    try {
      await serverApi.updateSettings({ gitlab_token: gitlabTokenInput });
      setHasGitlabToken(gitlabTokenInput !== "");
      if (gitlabTokenInput) {
        const len = gitlabTokenInput.length;
        setMaskedGitlabToken(len > 10 ? gitlabTokenInput.slice(0, 6) + "..." + gitlabTokenInput.slice(-4) : "***");
      } else {
        setMaskedGitlabToken("");
      }
      setShowGitlabTokenInput(false);
      setGitlabTokenInput("");
      setGitlabTokenSaved(true);
      setTimeout(() => setGitlabTokenSaved(false), 2000);
    } catch (e) {
      console.error("Failed to save GitLab token:", e);
      toast.error("Failed to save GitLab token", { description: String(e) });
    } finally {
      setSaving(false);
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

      {/* GitLab Personal Access Token */}
      <div className="rounded-lg border bg-card">
        <div className="px-4 py-3 space-y-3">
          <div className="flex items-center justify-between">
            <div>
              <div className="text-sm font-medium">GitLab Token</div>
              <div className="text-xs text-muted-foreground mt-0.5">
                {hasGitlabToken ? "Personal access token configured" : "No token — GitLab features (MR comments, create MR) won't work"}
              </div>
            </div>
            {gitlabTokenSaved && (
              <span className="flex items-center gap-1 text-xs text-green-500">
                <Check className="h-3 w-3" /> Saved
              </span>
            )}
          </div>

          {hasGitlabToken && !showGitlabTokenInput && (
            <div className="flex items-center gap-2">
              <code className="flex-1 bg-muted px-3 py-1.5 rounded-md text-xs font-mono text-muted-foreground">
                {maskedGitlabToken}
              </code>
              <button
                onClick={() => setShowGitlabTokenInput(true)}
                className="text-xs text-muted-foreground hover:text-foreground transition-colors px-2 py-1"
              >
                Change
              </button>
            </div>
          )}

          {(showGitlabTokenInput || !hasGitlabToken) && (
            <div className="flex items-center gap-2">
              <input
                type="password"
                value={gitlabTokenInput}
                onChange={(e) => setGitlabTokenInput(e.target.value)}
                placeholder="glpat-..."
                className="flex-1 bg-background border rounded-md px-3 py-1.5 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-1"
                onKeyDown={(e) => e.key === "Enter" && handleSaveGitlabToken()}
              />
              <button
                onClick={handleSaveGitlabToken}
                disabled={saving}
                className="px-3 py-1.5 rounded-md bg-primary text-primary-foreground text-xs font-medium hover:bg-primary/90 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
              >
                Save
              </button>
              {showGitlabTokenInput && (
                <button
                  onClick={() => { setShowGitlabTokenInput(false); setGitlabTokenInput(""); }}
                  className="text-xs text-muted-foreground hover:text-foreground transition-colors px-2 py-1"
                >
                  Cancel
                </button>
              )}
            </div>
          )}
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

    </section>
  );
}
