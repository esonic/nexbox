import { useState } from "react";
import { Check, Loader2, Circle, ChevronRight, ChevronDown } from "lucide-react";
import { cn } from "@/lib/utils";
import type { WorkflowProgressSnapshot, WorkflowAgentEntry } from "@/types";

// Renders the live tree for a dynamic Workflow run (phases + per-agent rows),
// driven by the server-merged workflow_progress snapshot. Mirrors (a compact
// subset of) Claude Code's terminal `/workflows` view. The snapshot is the full
// merged tree, so this is a pure render — no client-side delta accumulation.

function shortModel(m?: string): string {
  if (!m) return "";
  return m.replace(/^claude-/, "").replace(/-\d{8}$/, "");
}

function fmtTokens(n?: number): string {
  if (!n) return "";
  if (n >= 1000) return (n / 1000).toFixed(1).replace(/\.0$/, "") + "k";
  return String(n);
}

function fmtDuration(ms?: number): string {
  if (!ms) return "";
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  return `${Math.floor(s / 60)}m${s % 60}s`;
}

function AgentStateIcon({ state }: { state?: string }) {
  if (state === "done") return <Check className="h-3 w-3 shrink-0 text-green-600 dark:text-green-400" />;
  if (state === "progress") return <Loader2 className="h-3 w-3 shrink-0 animate-spin text-blue-600 dark:text-blue-400" />;
  return <Circle className="h-3 w-3 shrink-0 text-muted-foreground/40" />;
}

function AgentRow({ agent }: { agent: WorkflowAgentEntry }) {
  const [open, setOpen] = useState(false);
  const hasDetail = !!(agent.promptPreview || agent.lastToolName || agent.resultPreview);

  const meta: string[] = [];
  if (agent.tokens) meta.push(`${fmtTokens(agent.tokens)} tok`);
  if (typeof agent.toolCalls === "number" && agent.toolCalls > 0) {
    meta.push(`${agent.toolCalls} tool${agent.toolCalls > 1 ? "s" : ""}`);
  }
  if (agent.durationMs) meta.push(fmtDuration(agent.durationMs));

  return (
    <div>
      <button
        type="button"
        disabled={!hasDetail}
        onClick={() => setOpen((o) => !o)}
        className={cn(
          "group flex w-full items-center gap-1.5 py-0.5 text-left text-xs",
          hasDetail && "hover:text-foreground",
        )}
      >
        <AgentStateIcon state={agent.state} />
        <span className="truncate font-mono font-medium">{agent.label}</span>
        {agent.model && <span className="shrink-0 opacity-50">{shortModel(agent.model)}</span>}
        {meta.length > 0 && <span className="ml-auto shrink-0 tabular-nums opacity-60">{meta.join(" · ")}</span>}
        {hasDetail &&
          (open ? (
            <ChevronDown className={cn("h-3 w-3 opacity-40", meta.length > 0 ? "ml-1" : "ml-auto")} />
          ) : (
            <ChevronRight className={cn("h-3 w-3 opacity-0 group-hover:opacity-40", meta.length > 0 ? "ml-1" : "ml-auto")} />
          ))}
      </button>

      {open && hasDetail && (
        <div className="mb-1 ml-[18px] space-y-1 border-l border-border/40 pl-2 text-[11px] text-muted-foreground">
          {agent.promptPreview && (
            <div>
              <span className="opacity-50">Prompt</span>
              <div className="max-h-20 overflow-y-auto whitespace-pre-wrap break-words">{agent.promptPreview}</div>
            </div>
          )}
          {agent.lastToolName && (
            <div>
              <span className="opacity-50">Activity</span>
              <div className="font-mono break-words">
                {agent.lastToolName}
                {agent.lastToolSummary ? `(${agent.lastToolSummary})` : ""}
              </div>
            </div>
          )}
          {agent.resultPreview && (
            <div>
              <span className="opacity-50">Outcome</span>
              <div className="max-h-20 overflow-y-auto whitespace-pre-wrap break-words">{agent.resultPreview}</div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export function WorkflowProgressView({ snapshot }: { snapshot: WorkflowProgressSnapshot }) {
  const phases = snapshot.phases || [];
  const agents = snapshot.agents || [];
  // Per-phase collapse state (keyed by phase index). Undefined = use the
  // default, which is "expanded while running, collapsed once complete".
  const [collapsed, setCollapsed] = useState<Record<number, boolean>>({});

  if (phases.length === 0 && agents.length === 0) return null;

  // Once the whole run is done, collapse phases by default into a tidy summary
  // (the user can still expand any). A phase the user explicitly toggled keeps
  // their choice.
  const isComplete =
    snapshot.status === "completed" || (agents.length > 0 && agents.every((a) => a.state === "done"));

  // Per-phase done/total counts from the agents grouped by phaseIndex.
  const counts = new Map<number, { done: number; total: number }>();
  for (const a of agents) {
    if (typeof a.phaseIndex !== "number") continue;
    const c = counts.get(a.phaseIndex) || { done: 0, total: 0 };
    c.total += 1;
    if (a.state === "done") c.done += 1;
    counts.set(a.phaseIndex, c);
  }

  // Current phase = lowest phase index that still has a non-done agent.
  let currentPhase = -1;
  for (const a of agents) {
    if (a.state !== "done" && typeof a.phaseIndex === "number") {
      if (currentPhase < 0 || a.phaseIndex < currentPhase) currentPhase = a.phaseIndex;
    }
  }

  // Group agents under their phase; agents with no known phase render flat.
  const phaseIdx = new Set(phases.map((p) => p.index));
  const byPhase = new Map<number, WorkflowAgentEntry[]>();
  const ungrouped: WorkflowAgentEntry[] = [];
  for (const a of agents) {
    if (typeof a.phaseIndex === "number" && phaseIdx.has(a.phaseIndex)) {
      const arr = byPhase.get(a.phaseIndex) || [];
      arr.push(a);
      byPhase.set(a.phaseIndex, arr);
    } else {
      ungrouped.push(a);
    }
  }

  // No declared phases: fall back to a flat agent list.
  if (phases.length === 0) {
    return (
      <div className="space-y-0 text-xs">
        {agents.map((a) => (
          <AgentRow key={`${a.index}-${a.label}`} agent={a} />
        ))}
      </div>
    );
  }

  return (
    <div className="space-y-0.5 text-xs">
      {phases.map((p) => {
        const c = counts.get(p.index);
        const active = p.index === currentPhase;
        const allDone = !!c && c.total > 0 && c.done === c.total;
        const phaseAgents = byPhase.get(p.index) || [];
        const canToggle = phaseAgents.length > 0;
        const isCollapsed = collapsed[p.index] !== undefined ? collapsed[p.index] : isComplete;
        return (
          <div key={p.index}>
            <button
              type="button"
              disabled={!canToggle}
              onClick={() => setCollapsed((s) => ({ ...s, [p.index]: !(s[p.index] ?? isComplete) }))}
              className={cn(
                "group flex w-full items-center gap-1.5 py-0.5 text-left",
                active ? "font-medium text-blue-600 dark:text-blue-400" : "text-muted-foreground",
                canToggle && "hover:text-foreground",
              )}
            >
              {allDone ? (
                <Check className="h-3 w-3 shrink-0 text-green-600 dark:text-green-400" />
              ) : active ? (
                <Loader2 className="h-3 w-3 shrink-0 animate-spin" />
              ) : (
                <Circle className="h-3 w-3 shrink-0 opacity-40" />
              )}
              <span>{p.title}</span>
              {c && <span className="tabular-nums opacity-60">{c.done}/{c.total}</span>}
              {canToggle &&
                (isCollapsed ? (
                  <ChevronRight className="ml-auto h-3 w-3 shrink-0 opacity-40" />
                ) : (
                  <ChevronDown className="ml-auto h-3 w-3 shrink-0 opacity-40" />
                ))}
            </button>
            {!isCollapsed && phaseAgents.length > 0 && (
              <div className="ml-3 border-l border-border/30 pl-2">
                {phaseAgents.map((a) => (
                  <AgentRow key={`${a.index}-${a.label}`} agent={a} />
                ))}
              </div>
            )}
          </div>
        );
      })}

      {ungrouped.length > 0 && (
        <div className="space-y-0">
          {ungrouped.map((a) => (
            <AgentRow key={`${a.index}-${a.label}`} agent={a} />
          ))}
        </div>
      )}

      {snapshot.status && snapshot.status !== "completed" && (
        <div className="text-[11px] italic text-muted-foreground/70">{snapshot.status}</div>
      )}
    </div>
  );
}
