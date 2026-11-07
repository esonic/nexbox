import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import type { RepositoryFileRef } from "@/lib/api";
import { gitContextKey } from "@/stores/git";

interface ProjectState {
  expanded: Set<string>;
  selectedPath: string | null;
  selectedRepositoryFile: RepositoryFileRef | null;
}

interface FileBrowserState {
  // The legacy field name is retained for persisted-state compatibility, but
  // each bucket is keyed by project + local/worktree context. Repository IDs
  // are checkout-specific and must never be restored into another context.
  projects: Record<string, ProjectState>;
  navigation: Record<string, { path: string; line?: number; safeLink?: boolean; nonce: number }>;
  navigateToFile: (contextKey: string, path: string, line?: number, safeLink?: boolean) => void;
  clearNavigation: (contextKey: string) => void;
  toggleExpand: (contextKey: string, path: string) => void;
  expandAncestors: (contextKey: string, path: string) => void;
  setSelectedPath: (contextKey: string, path: string | null) => void;
  setSelectedRepositoryFile: (contextKey: string, file: RepositoryFileRef | null) => void;
}

const EMPTY: ProjectState = { expanded: new Set(), selectedPath: null, selectedRepositoryFile: null };

function ensure(projects: Record<string, ProjectState>, contextKey: string): ProjectState {
  return projects[contextKey] ?? EMPTY;
}

type PersistedFileBrowserState = Pick<FileBrowserState, "projects">;

function migratePersistedState(persisted: unknown, version: number): PersistedFileBrowserState {
  const record = persisted && typeof persisted === "object" ? persisted as Record<string, unknown> : {};
  if (version < 2 && typeof record.ownerProjectId === "string") {
    const expanded = record.expanded instanceof Set
      ? new Set([...record.expanded].filter((value): value is string => typeof value === "string"))
      : new Set(Array.isArray(record.expanded) ? record.expanded.filter((value): value is string => typeof value === "string") : []);
    return {
      projects: {
        [gitContextKey(record.ownerProjectId)]: { expanded, selectedPath: null, selectedRepositoryFile: null },
      },
    };
  }
  if (record.projects && typeof record.projects === "object") {
    const projects: Record<string, ProjectState> = {};
    for (const [persistedKey, value] of Object.entries(record.projects)) {
      if (!value || typeof value !== "object") continue;
      const state = value as Record<string, unknown>;
      const expanded = state.expanded instanceof Set
        ? new Set([...state.expanded].filter((item): item is string => typeof item === "string"))
        : new Set(Array.isArray(state.expanded) ? state.expanded.filter((item): item is string => typeof item === "string") : []);
      const contextKey = version < 3 ? gitContextKey(persistedKey) : persistedKey;
      projects[contextKey] = {
        expanded,
        selectedPath: typeof state.selectedPath === "string" ? state.selectedPath : null,
        selectedRepositoryFile: state.selectedRepositoryFile && typeof state.selectedRepositoryFile === "object" && !Array.isArray(state.selectedRepositoryFile)
          ? state.selectedRepositoryFile as RepositoryFileRef
          : null,
      };
    }
    return { projects };
  }
  return { projects: {} };
}

export const useFileBrowserStore = create<FileBrowserState>()(
  persist(
    (set) => ({
      projects: {},
      navigation: {},
      navigateToFile: (contextKey, path, line, safeLink) => set((state) => ({
        navigation: { ...state.navigation, [contextKey]: { path, line, safeLink: safeLink ?? (state.navigation[contextKey]?.path === path ? state.navigation[contextKey]?.safeLink : undefined), nonce: (state.navigation[contextKey]?.nonce ?? 0) + 1 } },
      })),
      clearNavigation: (contextKey) => set((state) => {
        if (!state.navigation[contextKey]) return {};
        const navigation = { ...state.navigation };
        delete navigation[contextKey];
        return { navigation };
      }),
      toggleExpand: (contextKey, path) =>
        set((state) => {
          const prev = ensure(state.projects, contextKey);
          const nextExpanded = new Set(prev.expanded);
          if (nextExpanded.has(path)) nextExpanded.delete(path);
          else nextExpanded.add(path);
          return {
            projects: {
              ...state.projects,
              [contextKey]: { ...prev, expanded: nextExpanded },
            },
          };
        }),
      expandAncestors: (contextKey, path) =>
        set((state) => {
          const prev = ensure(state.projects, contextKey);
          const nextExpanded = new Set(prev.expanded);
          const parts = path.split("/");
          let acc = "";
          for (let i = 0; i < parts.length - 1; i++) {
            acc = acc ? `${acc}/${parts[i]}` : parts[i];
            if (acc) nextExpanded.add(acc);
          }
          if (nextExpanded.size === prev.expanded.size) return {}; // nothing new to expand
          return {
            projects: {
              ...state.projects,
              [contextKey]: { ...prev, expanded: nextExpanded },
            },
          };
        }),
      setSelectedPath: (contextKey, selectedPath) =>        set((state) => {
          const prev = ensure(state.projects, contextKey);
          return {
            projects: {
              ...state.projects,
              [contextKey]: { ...prev, selectedPath, selectedRepositoryFile: null },
            },
          };
        }),
      setSelectedRepositoryFile: (contextKey, file) =>
        set((state) => {
          const prev = ensure(state.projects, contextKey);
          return {
            projects: {
              ...state.projects,
              [contextKey]: { ...prev, selectedPath: file?.path ?? null, selectedRepositoryFile: file },
            },
          };
        }),
    }),
    {
      name: "nexbox-file-browser",
      version: 3,
      // A chat-link navigation carries a transient restricted-read context.
      // Do not restore its selection later without that context.
      partialize: (state) => ({ projects: Object.fromEntries(Object.entries(state.projects).map(([key, project]) => [
        key, state.navigation[key]?.safeLink && state.navigation[key]?.path === project.selectedPath
          ? { ...project, selectedPath: null, selectedRepositoryFile: null } : project,
      ])) }),
      // v1 had one project bucket; v2 keyed buckets by project only. Move both
      // into the local checkout context. Runs after storage revives Set values.
      migrate: migratePersistedState,
      storage: createJSONStorage<PersistedFileBrowserState>(() => localStorage, {
        replacer: (_key, value) => value instanceof Set ? [...value] : value,
        reviver: (key, value) => key === "expanded" && Array.isArray(value)
          ? new Set(value.filter((item): item is string => typeof item === "string"))
          : value,
      }),
    }
  )
);
