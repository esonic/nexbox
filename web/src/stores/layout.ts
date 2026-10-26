import { create } from "zustand";
import { persist } from "zustand/middleware";

interface TerminalSessionState {
  show: boolean;
  key: number;
}

export type TerminalPosition = "bottom" | "right";

interface LayoutState {
  // Right panel
  rightPanel: string | null;
  setRightPanel: (panel: string | null) => void;
  panelWidth: number;
  setPanelWidth: (width: number) => void;
  fileTreeWidth: number;
  setFileTreeWidth: (width: number) => void;

  // Terminal — height/position are global (UI preference), show/key are per-session
  terminalHeight: number;
  setTerminalHeight: (height: number) => void;
  terminalPosition: TerminalPosition;
  setTerminalPosition: (position: TerminalPosition) => void;
  terminalStates: Record<string, TerminalSessionState>;
  setTerminalShow: (sessionId: string, show: boolean) => void;
  killTerminal: (sessionId: string) => void;

  // Sidebar
  autoSortSessions: boolean;
  setAutoSortSessions: (enabled: boolean) => void;
  projectOrder: string[];
  setProjectOrder: (ids: string[]) => void;
  sidebarOpen: boolean;
  toggleSidebar: () => void;
  sidebarCollapsed: boolean;
  setSidebarCollapsed: (collapsed: boolean) => void;
  sidebarWidth: number;
  setSidebarWidth: (width: number) => void;
}

export const useLayoutStore = create<LayoutState>()(
  persist(
    (set) => ({
      rightPanel: null,
      setRightPanel: (panel) => set({ rightPanel: panel }),
      panelWidth: 480,
      setPanelWidth: (width) => set({ panelWidth: width }),
      fileTreeWidth: 224,
      setFileTreeWidth: (width) => set({ fileTreeWidth: width }),

      terminalHeight: 260,
      setTerminalHeight: (height) => set({ terminalHeight: height }),
      terminalPosition: "bottom",
      setTerminalPosition: (position) => set({ terminalPosition: position }),
      terminalStates: {},
      setTerminalShow: (sessionId, show) =>
        set((s) => ({
          terminalStates: {
            ...s.terminalStates,
            [sessionId]: { key: s.terminalStates[sessionId]?.key ?? 0, show },
          },
        })),
      killTerminal: (sessionId) =>
        set((s) => ({
          terminalStates: {
            ...s.terminalStates,
            [sessionId]: { show: false, key: (s.terminalStates[sessionId]?.key ?? 0) + 1 },
          },
        })),

      autoSortSessions: false,
      setAutoSortSessions: (enabled) => set({ autoSortSessions: enabled }),
      projectOrder: [],
      setProjectOrder: (ids) => set({ projectOrder: ids }),
      sidebarOpen: true,
      toggleSidebar: () => set((s) => ({ sidebarOpen: !s.sidebarOpen })),
      sidebarCollapsed: false,
      setSidebarCollapsed: (collapsed) => set({ sidebarCollapsed: collapsed }),
      sidebarWidth: 260,
      setSidebarWidth: (width) => set({ sidebarWidth: width }),
    }),
    {
      name: "nexbox-layout",
      partialize: (state) => ({
        rightPanel: state.rightPanel,
        panelWidth: state.panelWidth,
        fileTreeWidth: state.fileTreeWidth,
        terminalHeight: state.terminalHeight,
        terminalPosition: state.terminalPosition,
        // Persist show state per session; reset key to 0 so terminal reconnects on reload
        terminalStates: Object.fromEntries(
          Object.entries(state.terminalStates).map(([id, ts]) => [id, { show: ts.show, key: 0 }])
        ),
        autoSortSessions: state.autoSortSessions,
        projectOrder: state.projectOrder,
        sidebarOpen: state.sidebarOpen,
        sidebarCollapsed: state.sidebarCollapsed,
        sidebarWidth: state.sidebarWidth,
      }),
    }
  )
);
