import { create } from "zustand";
import { persist } from "zustand/middleware";

interface FileBrowserState {
  // projectId that the expanded/selected state belongs to
  ownerProjectId: string | null;
  expanded: Set<string>;
  // selectedPath is NOT persisted — stale cross-project selection causes the
  // "files always show the same directory" bug after page refresh / Go restart.
  selectedPath: string | null;
  toggleExpand: (path: string) => void;
  setExpanded: (expanded: Set<string>) => void;
  setSelectedPath: (path: string | null) => void;
  resetForProject: (projectId: string) => void;
}

export const useFileBrowserStore = create<FileBrowserState>()(
  persist(
    (set) => ({
      ownerProjectId: null,
      expanded: new Set<string>(),
      selectedPath: null,
      toggleExpand: (path) =>
        set((state) => {
          const next = new Set(state.expanded);
          next.has(path) ? next.delete(path) : next.add(path);
          return { expanded: next };
        }),
      setExpanded: (expanded) => set({ expanded }),
      setSelectedPath: (selectedPath) => set({ selectedPath }),
      resetForProject: (projectId) =>
        set({ ownerProjectId: projectId, expanded: new Set(), selectedPath: null }),
    }),
    {
      name: "boxnex-file-browser",
      // Only persist ownerProjectId + expanded. Never persist selectedPath.
      partialize: (state) => ({
        ownerProjectId: state.ownerProjectId,
        expanded: state.expanded,
      }),
      storage: {
        getItem: (name) => {
          const str = localStorage.getItem(name);
          if (!str) return null;
          const parsed = JSON.parse(str);
          if (parsed?.state?.expanded) {
            parsed.state.expanded = new Set(parsed.state.expanded);
          }
          return parsed;
        },
        setItem: (name, value) => {
          const toStore = {
            ...value,
            state: {
              ...value.state,
              expanded: value.state.expanded instanceof Set
                ? [...value.state.expanded]
                : value.state.expanded,
            },
          };
          localStorage.setItem(name, JSON.stringify(toStore));
        },
        removeItem: (name) => localStorage.removeItem(name),
      },
    }
  )
);
