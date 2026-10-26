// Display labels are intentionally separate from stable backend agent IDs.
// Keep this map shared so every surface presents the same agent name.
export const AGENT_LABELS: Record<string, string> = {
  claude: "Claude Code",
  codex: "Codex",
  zeroclaw: "ZeroClaw",
  crush: "Crush",
  pi: "Pi",
  kimi: "Kimi Code",
};

export function agentLabel(agent: string): string {
  return AGENT_LABELS[agent] || agent;
}
