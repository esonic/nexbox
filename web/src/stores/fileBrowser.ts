import { create } from "zustand";
import { persist } from "zustand/middleware";

interface ProjectState {
  expanded: Set<string>;
  selectedPath: string | null;
}

interface FileBrowserState {
  // Per-project bucket so switching projects (and coming back) preserves both
  // expanded folders and the previously-selected file. selectedPath is safe to
  // persist here because it's keyed by projectId — it can never be applied to a
  // different project than the one it was recorded in.
  projects: Record<string, ProjectState>;
  toggleExpand: (projectId: string, path: string) => void;
  setSelectedPath: (projectId: string, path: string | null) => void;
}

const EMPTY: ProjectState = { expanded: new Set(), selectedPath: null };

function ensure(projects: Record<string, ProjectState>, projectId: string): ProjectState {
  return projects[projectId] ?? EMPTY;
}

export const useFileBrowserStore = create<FileBrowserState>()(
  persist(
    (set) => ({
      projects: {},
      toggleExpand: (projectId, path) =>
        set((state) => {
          const prev = ensure(state.projects, projectId);
          const nextExpanded = new Set(prev.expanded);
          nextExpanded.has(path) ? nextExpanded.delete(path) : nextExpanded.add(path);
          return {
            projects: {
              ...state.projects,
              [projectId]: { ...prev, expanded: nextExpanded },
            },
          };
        }),
      setSelectedPath: (projectId, selectedPath) =>
        set((state) => {
          const prev = ensure(state.projects, projectId);
          return {
            projects: {
              ...state.projects,
              [projectId]: { ...prev, selectedPath },
            },
          };
        }),
    }),
    {
      name: "nexbox-file-browser",
      version: 2,
      partialize: (state) => ({ projects: state.projects }),
      // Migrate the pre-v2 single-bucket shape ({ ownerProjectId, expanded })
      // into the per-project map. Runs after the custom storage has already
      // turned the persisted Set array back into a real Set.
      migrate: (persisted: any, version: number) => {
        if (version < 2 && persisted && persisted.ownerProjectId) {
          const projects: Record<string, ProjectState> = {
            [persisted.ownerProjectId]: {
              expanded:
                persisted.expanded instanceof Set
                  ? persisted.expanded
                  : new Set(Array.isArray(persisted.expanded) ? persisted.expanded : []),
              selectedPath: null,
            },
          };
          return { projects };
        }
        return persisted;
      },
      storage: {
        getItem: (name) => {
          const str = localStorage.getItem(name);
          if (!str) return null;
          const parsed = JSON.parse(str);
          // Rehydrate Sets inside each project bucket
          const projects = parsed?.state?.projects;
          if (projects && typeof projects === "object") {
            for (const id of Object.keys(projects)) {
              const proj = projects[id];
              if (proj && Array.isArray(proj.expanded)) {
                proj.expanded = new Set(proj.expanded);
              }
            }
          }
          return parsed;
        },
        setItem: (name, value) => {
          const inProjects = (value.state as any).projects ?? {};
          const outProjects: Record<string, { expanded: string[]; selectedPath: string | null }> = {};
          for (const [id, proj] of Object.entries(inProjects) as Array<[string, ProjectState]>) {
            outProjects[id] = {
              expanded: proj.expanded instanceof Set ? [...proj.expanded] : [],
              selectedPath: proj.selectedPath ?? null,
            };
          }
          const toStore = {
            ...value,
            state: { ...(value.state as any), projects: outProjects },
          };
          localStorage.setItem(name, JSON.stringify(toStore));
        },
        removeItem: (name) => localStorage.removeItem(name),
      },
    }
  )
);
