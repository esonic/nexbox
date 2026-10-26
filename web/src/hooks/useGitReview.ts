import { useCallback, useEffect, useRef, useState } from "react";
import { diffApi, gitApi, type DiffResponse, type GitBranchResponse, type GitCommitInfo, type GitRepositoriesResponse, type GitScope, type GitStatusResponse } from "@/lib/api";

function fulfilled<T>(result: PromiseSettledResult<T>): T {
  if (result.status === "rejected") throw result.reason;
  return result.value;
}

export interface GitReviewSnapshot {
  diff: DiffResponse;
  status: GitStatusResponse;
  branches: GitBranchResponse;
  commits: GitCommitInfo[];
  repositories: GitRepositoriesResponse;
  fileRevision: number;
  refreshRevision: number;
  scopeKey: string;
}

// One scheduler owns all Git panel reads. Publish complete rounds, retain equal
// snapshots, and queue mutations that arrive during an in-flight round.
export function useGitReview(scope: GitScope | null, enabled: boolean) {
  const [snapshot, setSnapshot] = useState<GitReviewSnapshot | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const refreshRef = useRef<() => void>(() => {});
  const refresh = useCallback(() => refreshRef.current(), []);
  const projectId = scope?.projectId;
  const sessionId = scope?.sessionId;
  const repositoryId = scope?.repositoryId;
  const scopeKey = JSON.stringify([projectId, sessionId, repositoryId]);

  useEffect(() => {
    setSnapshot(null);
    setError(null);
    if (!enabled || !projectId || !repositoryId) return;
    const selected: GitScope = { projectId, sessionId, repositoryId };
    let cancelled = false;
    let running = false;
    let pending = false;
    let revision = 0;
    let initialized = false;
    const run = async (explicit = false) => {
      if (cancelled) return;
      if (explicit) { pending = true; revision++; }
      if (running) return;
      running = true;
      pending = false;
      const roundRevision = revision;
      if (!initialized) setLoading(true);
      try {
        const results = await Promise.allSettled([
          diffApi.stats(selected), gitApi.status(selected), gitApi.branches(selected),
          gitApi.log(selected, 20), gitApi.repositories(projectId, sessionId),
        ]);
        if (cancelled || pending) return;
        const diff = fulfilled(results[0]);
        const status = fulfilled(results[1]);
        const branches = fulfilled(results[2]);
        const commits = fulfilled(results[3]);
        const repositories = fulfilled(results[4]);
        setSnapshot((previous) => {
          const headChanged = previous && (previous.commits[0]?.hash !== commits[0]?.hash || previous.branches.current !== branches.current);
          const next = { diff, status, branches, commits, repositories,
            scopeKey: JSON.stringify([projectId, sessionId, repositoryId]),
            refreshRevision: roundRevision,
            fileRevision: (previous?.fileRevision ?? 0) + (headChanged || previous?.refreshRevision !== roundRevision ? 1 : 0) };
          if (previous) {
            for (const field of ["diff", "status", "branches", "commits", "repositories"] as const) {
              if (JSON.stringify(previous[field]) === JSON.stringify(next[field])) {
                // Retain each unchanged consumer's reference as well as the whole snapshot.
                Object.assign(next, { [field]: previous[field] });
              }
            }
          }
          return JSON.stringify(previous) === JSON.stringify(next) ? previous : next;
        });
        setError(null);
        initialized = true;
      } catch (cause) {
        if (!cancelled) setError(cause);
      } finally {
        running = false;
        if (!cancelled) {
          setLoading(false);
          if (pending) void run();
        }
      }
    };
    refreshRef.current = () => { void run(true); };
    const poll = () => { if (!document.hidden) void run(); };
    void run();
    const interval = window.setInterval(poll, 5_000);
    document.addEventListener("visibilitychange", poll);
    return () => {
      cancelled = true;
      refreshRef.current = () => {};
      window.clearInterval(interval);
      document.removeEventListener("visibilitychange", poll);
    };
  }, [enabled, projectId, sessionId, repositoryId]);

  return { snapshot: enabled && snapshot?.scopeKey === scopeKey ? snapshot : null, loading, error, refresh };
}
