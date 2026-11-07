import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { filesApi, type FileEntry } from "@/lib/api";

// Keep unchanged arrays (and the whole map) by identity so a poll does not
// disturb the tree DOM, selection, or scroll position.
function updateDirectory(previous: Record<string, FileEntry[]>, path: string, entries: FileEntry[]) {
  if (JSON.stringify(previous[path]) === JSON.stringify(entries)) return previous;
  return { ...previous, [path]: entries };
}

export function useFileTree(
  projectId: string,
  sessionId: string | undefined,
  expanded: Set<string>,
  containerRef: RefObject<HTMLElement | null>,
) {
  const [childrenMap, setChildrenMap] = useState<Record<string, FileEntry[]>>({});
  const [loadingPaths, setLoadingPaths] = useState<Set<string>>(() => new Set());
  const [rootLoading, setRootLoading] = useState(true);
  const generation = useRef(0);
  const requests = useRef(new Map<string, number>());
  const expandedRef = useRef(expanded);
  useEffect(() => { expandedRef.current = expanded; }, [expanded]);

  const requestDirectory = useCallback(async (path: string) => {
    const gen = generation.current;
    const request = (requests.current.get(path) ?? 0) + 1;
    requests.current.set(path, request);
    const entries = (await filesApi.list(projectId, path, sessionId)) ?? [];
    if (gen !== generation.current || requests.current.get(path) !== request) return;
    setChildrenMap(previous => gen === generation.current && requests.current.get(path) === request
      ? updateDirectory(previous, path, entries) : previous);
    return entries;
  }, [projectId, sessionId]);

  const loadChildren = useCallback(async (path: string) => {
    const gen = generation.current;
    setLoadingPaths(previous => new Set(previous).add(path));
    try {
      await requestDirectory(path);
    } catch (error) {
      console.error(`Failed to load directory "${path}":`, error);
    } finally {
      if (gen === generation.current) {
        setLoadingPaths(previous => {
          const next = new Set(previous);
          next.delete(path);
          return next;
        });
      }
    }
  }, [requestDirectory]);

  const refreshDir = useCallback(async (path: string) => {
    try {
      return await requestDirectory(path);
    } catch (error) {
      console.error(`Failed to refresh directory "${path}":`, error);
    }
  }, [requestDirectory]);

  const refreshInFlight = useRef<{ generation: number; promise: Promise<boolean> } | null>(null);
  const refreshTree = useCallback(() => {
    const gen = generation.current;
    if (refreshInFlight.current?.generation === gen) return refreshInFlight.current.promise;
    const promise = (async () => {
      // Follow the returned tree, rather than every cached expanded path: a
      // collapsed ancestor, deleted directory, or compacted path is not visible.
      const queue = [""];
      const visited = new Set<string>();
      let success = true;
      while (queue.length && gen === generation.current) {
        if (document.hidden || !containerRef.current?.getClientRects().length) break;
        const batch = queue.splice(0, 4).filter(path => !visited.has(path));
        batch.forEach(path => visited.add(path));
        const results = await Promise.all(batch.map(refreshDir));
        if (gen !== generation.current) break;
        for (const entries of results) {
          if (!entries) success = false;
          for (const entry of entries ?? []) {
            if (entry.is_dir && expandedRef.current.has(entry.path)) queue.push(entry.path);
          }
        }
      }
      return success;
    })();
    refreshInFlight.current = { generation: gen, promise };
    void promise.finally(() => {
      if (refreshInFlight.current?.promise === promise) refreshInFlight.current = null;
    });
    return promise;
  }, [refreshDir, containerRef]);

  useEffect(() => {
    const scope = generation;
    const gen = ++scope.current;
    requests.current.clear();
    setChildrenMap({});
    setLoadingPaths(new Set());
    setRootLoading(true);
    void requestDirectory("").catch(error => {
      console.error("Failed to load file tree:", error);
    }).finally(() => {
      if (gen === generation.current) setRootLoading(false);
    });
    return () => { scope.current++; };
  }, [requestDirectory]);

  useEffect(() => {
    if (rootLoading) return;
    const poll = () => { void refreshTree(); };
    const interval = window.setInterval(poll, 5_000);
    document.addEventListener("visibilitychange", poll);
    window.addEventListener("focus", poll);
    return () => {
      window.clearInterval(interval);
      document.removeEventListener("visibilitychange", poll);
      window.removeEventListener("focus", poll);
    };
  }, [rootLoading, refreshTree]);

  return { childrenMap, setChildrenMap, loadingPaths, rootLoading, loadChildren, refreshDir, refreshTree };
}
