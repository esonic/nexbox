import { create } from "zustand";
import { persist } from "zustand/middleware";

interface GitStore {
  selectedRepositoryByContext: Record<string, string>;
  setSelectedRepository: (contextKey: string, repositoryId: string) => void;
  clearSelectedRepository: (contextKey: string) => void;
}

export function gitContextKey(projectId: string, sessionId?: string) {
  return `${projectId}:${sessionId ?? "local"}`;
}

export const useGitStore = create<GitStore>()(
  persist(
    (set) => ({
      selectedRepositoryByContext: {},
      setSelectedRepository: (contextKey, repositoryId) =>
        set((state) => ({
          selectedRepositoryByContext: {
            ...state.selectedRepositoryByContext,
            [contextKey]: repositoryId,
          },
        })),
      clearSelectedRepository: (contextKey) =>
        set((state) => {
          const next = { ...state.selectedRepositoryByContext };
          delete next[contextKey];
          return { selectedRepositoryByContext: next };
        }),
    }),
    {
      name: "nexbox-git-repository-selection",
      version: 1,
      partialize: (state) => ({ selectedRepositoryByContext: state.selectedRepositoryByContext }),
    },
  ),
);
