import { create } from "zustand";
import { persist } from "zustand/middleware";
import type { DiffResponse, AuthUser } from "@/lib/api";
import type { Project, Session, ChatMessage, MessagePart, Task, AssetType, ExecutionStats } from "../types";

export interface PickedAsset {
  id: string;
  title: string;
  assetType: AssetType;
}

export interface PickedWorkflow {
  id: string;
  name: string;
  projectId: string;
}

export interface InlineComment {
  id: string;
  filePath: string;
  lineNumber: number;
  side: "old" | "new";  // "old" for deletion lines, "new" for addition/context lines
  lineContent: string;
  text: string;
}

export interface RichQueuedMessage {
  id: string;
  text: string;
  images?: { filename: string; url: string }[];
  pickedTasks: Task[];
  pickedAssets: PickedAsset[];
  pickedWorkflows: PickedWorkflow[];
  pickedComments: InlineComment[];
  created_at: string;
}

interface AppState {
  // Current selection
  currentProject: Project | null;
  currentSession: Session | null;
  activeSessionId: string | null;
  sessions: Session[];
  projects: Project[];
  setCurrentProject: (project: Project | null) => void;
  setCurrentSession: (session: Session | null) => void;
  setActiveSessionId: (id: string | null) => void;
  setSessions: (sessions: Session[]) => void;
  setProjects: (projects: Project[]) => void;

  // Chat messages per session (keyed by session ID)
  chatMessages: Record<string, ChatMessage[]>;
  setChatMessages: (sessionId: string, messages: ChatMessage[]) => void;
  addChatMessage: (sessionId: string, message: ChatMessage) => void;

  // Fine-grained streaming updates
  appendTextDelta: (sessionId: string, text: string) => void;
  addToolStart: (sessionId: string, toolCallId: string, toolName: string, input?: string) => void;
  updateToolInput: (sessionId: string, toolCallId: string, inputDelta: string) => void;
  addToolResult: (sessionId: string, toolCallId: string, output: string, isError?: boolean) => void;
  appendThinkingDelta: (sessionId: string, text: string) => void;

  finishStreaming: (sessionId: string, stats?: ExecutionStats) => void;
  clearChatMessages: (sessionId: string) => void;

  // Per-session streaming state (global, survives tab switches)
  streamingSessions: Record<string, boolean>;
  streamingStartedAt: Record<string, number>;
  setStreamingSession: (sessionId: string, active: boolean) => void;
  initStreamingSessions: (activeIds: string[]) => void;

  // Accumulated session duration (ms), keyed by session ID
  sessionTotalDuration: Record<string, number>;
  addSessionDuration: (sessionId: string, ms: number) => void;
  setSessionDuration: (sessionId: string, ms: number) => void;

  // Picked tasks (for sending to chat)
  pickedTasks: Record<string, Task[]>;
  addPickedTask: (sessionId: string, task: Task) => void;
  removePickedTask: (sessionId: string, taskId: string) => void;
  clearPickedTasks: (sessionId: string) => void;

  // Picked assets (for sending to chat)
  pickedAssets: Record<string, PickedAsset[]>;
  addPickedAsset: (sessionId: string, asset: PickedAsset) => void;
  removePickedAsset: (sessionId: string, assetId: string) => void;
  clearPickedAssets: (sessionId: string) => void;

  // Picked workflows (for @ execution)
  pickedWorkflows: Record<string, PickedWorkflow[]>;
  addPickedWorkflow: (sessionId: string, wf: PickedWorkflow) => void;
  removePickedWorkflow: (sessionId: string, wfId: string) => void;
  clearPickedWorkflows: (sessionId: string) => void;

  // Inline code comments (temporary, cleared on send)
  pickedComments: Record<string, InlineComment[]>;
  addPickedComment: (sessionId: string, comment: InlineComment) => void;
  removePickedComment: (sessionId: string, commentId: string) => void;
  clearPickedComments: (sessionId: string) => void;

  // Client-side rich message queue (preserves structured refs for messages queued during streaming)
  clientQueue: Record<string, RichQueuedMessage[]>;
  enqueueRichMessage: (sessionId: string, msg: RichQueuedMessage) => void;
  dequeueRichMessage: (sessionId: string, msgId: string) => void;
  updateRichQueuedText: (sessionId: string, msgId: string, text: string) => void;

  // Server mode (local vs cloud)
  connectStatus: "idle" | "connected";
  setConnectStatus: (status: "idle" | "connected") => void;

  // Git diff stats (shared between ChatPanel badge and Git panel)
  gitDiffStats: DiffResponse | null;
  setGitDiffStats: (stats: DiffResponse | null) => void;

  // Marketplace update badge (packages with newer version available)
  marketplaceUpdateCount: number;
  setMarketplaceUpdateCount: (count: number) => void;

  // Authenticated user (JWT cloud mode)
  currentUser: AuthUser | null;
  setCurrentUser: (user: AuthUser | null) => void;

  // Token balance
  balanceTokens: number;
  balanceCny: number;
  setBalance: (tokens: number, cny: number) => void;
}

/** Replace a session's message array. Reduces boilerplate around chatMessages spread. */
function patchMessages(
  state: AppState,
  sessionId: string,
  updater: (msgs: ChatMessage[]) => ChatMessage[],
): Partial<AppState> {
  return { chatMessages: { ...state.chatMessages, [sessionId]: updater(state.chatMessages[sessionId] || []) } };
}

/** Update the last assistant message's parts array (no-op if last msg isn't assistant). */
function updateLastAssistant(
  state: AppState,
  sessionId: string,
  updater: (parts: MessagePart[]) => MessagePart[]
): Partial<AppState> {
  const msgs = state.chatMessages[sessionId] || [];
  const lastMsg = msgs[msgs.length - 1];
  if (!lastMsg || lastMsg.role !== "assistant") return {};
  return { chatMessages: { ...state.chatMessages, [sessionId]: [...msgs.slice(0, -1), { ...lastMsg, parts: updater([...lastMsg.parts]) }] } };
}

/** Find a tool_use part by callId (scanning backwards) and patch it. */
function patchToolUse(
  parts: MessagePart[],
  toolCallId: string,
  patch: (p: Extract<MessagePart, { type: "tool_use" }>) => MessagePart,
): MessagePart[] {
  for (let i = parts.length - 1; i >= 0; i--) {
    const p = parts[i];
    if (p.type === "tool_use" && p.toolCallId === toolCallId) {
      parts[i] = patch(p);
      break;
    }
  }
  return parts;
}

export const useAppStore = create<AppState>()(
  persist(
    (set) => ({
  currentProject: null,
  currentSession: null,
  activeSessionId: null,
  sessions: [],
  projects: [],
  setCurrentProject: (project) => set({ currentProject: project }),
  setCurrentSession: (session) => set({ currentSession: session }),
  setActiveSessionId: (id) => {
    console.log('[Store] setActiveSessionId:', id);
    set({ activeSessionId: id });
  },
  setSessions: (sessions) => set({ sessions }),
  setProjects: (projects) => set({ projects }),

  chatMessages: {},
  setChatMessages: (sessionId, messages) =>
    set((state) => patchMessages(state, sessionId, () => messages)),
  addChatMessage: (sessionId, message) =>
    set((state) => patchMessages(state, sessionId, (msgs) => [...msgs, message])),

  appendTextDelta: (sessionId, text) =>
    set((state) =>
      updateLastAssistant(state, sessionId, (parts) => {
        const last = parts[parts.length - 1];
        if (last && last.type === "text") {
          parts[parts.length - 1] = { ...last, content: last.content + text };
        } else {
          parts.push({ type: "text", content: text });
        }
        return parts;
      })
    ),

  addToolStart: (sessionId, toolCallId, toolName, input = "") =>
    set((state) =>
      updateLastAssistant(state, sessionId, (parts) => {
        parts.push({
          type: "tool_use",
          toolCallId,
          toolName,
          input,
          state: "running",
        });
        return parts;
      })
    ),

  updateToolInput: (sessionId, toolCallId, inputDelta) =>
    set((state) =>
      updateLastAssistant(state, sessionId, (parts) =>
        patchToolUse(parts, toolCallId, (p) => ({ ...p, input: p.input + inputDelta })),
      ),
    ),

  addToolResult: (sessionId, toolCallId, output, isError) =>
    set((state) =>
      updateLastAssistant(state, sessionId, (parts) => {
        patchToolUse(parts, toolCallId, (p) => ({ ...p, state: isError ? "error" : "complete" }));
        parts.push({ type: "tool_result", toolCallId, output, isError });
        return parts;
      }),
    ),

  appendThinkingDelta: (sessionId, text) =>
    set((state) =>
      updateLastAssistant(state, sessionId, (parts) => {
        const last = parts[parts.length - 1];
        if (last && last.type === "thinking") {
          parts[parts.length - 1] = { ...last, content: last.content + text };
        } else {
          parts.push({ type: "thinking", content: text });
        }
        return parts;
      })
    ),

  finishStreaming: (sessionId, stats?) =>
    set((state) => {
      const msgs = state.chatMessages[sessionId] || [];
      const lastMsg = msgs[msgs.length - 1];
      if (lastMsg && lastMsg.role === "assistant") {
        const finalParts = lastMsg.parts.map((p) => {
          if (p.type === "tool_use" && (p.state === "streaming" || p.state === "running")) {
            return { ...p, state: "complete" as const };
          }
          return p;
        });
        // Merge execution stats into the message metadata
        const updatedMetadata = stats
          ? {
              ...lastMsg.metadata,
              durationMs: stats.duration_ms,
              inputTokens: stats.input_tokens,
              outputTokens: stats.output_tokens,
              totalCostUsd: stats.total_cost_usd,
              toolCalls: stats.tool_calls,
            }
          : lastMsg.metadata;
        return {
          chatMessages: {
            ...state.chatMessages,
            [sessionId]: [
              ...msgs.slice(0, -1),
              { ...lastMsg, parts: finalParts, isStreaming: false, metadata: updatedMetadata },
            ],
          },
        };
      }
      return state;
    }),

  clearChatMessages: (sessionId) =>
    set((state) => patchMessages(state, sessionId, () => [])),

  streamingSessions: {},
  streamingStartedAt: {} as Record<string, number>,
  setStreamingSession: (sessionId, active) =>
    set((state) => ({
      streamingSessions: { ...state.streamingSessions, [sessionId]: active },
      streamingStartedAt: active
        ? { ...state.streamingStartedAt, [sessionId]: Date.now() }
        : state.streamingStartedAt,
    })),
  initStreamingSessions: (activeIds) =>
    set((state) => {
      // Server is the source of truth for active sessions.
      const map: Record<string, boolean> = {};
      for (const id of activeIds) map[id] = true;
      // Only preserve client-side streaming sessions that started very recently
      // (< 5s ago) to handle the race where user just sent a message but the
      // server hasn't registered it yet.
      const now = Date.now();
      for (const [id, active] of Object.entries(state.streamingSessions)) {
        if (active && !map[id]) {
          const startedAt = state.streamingStartedAt[id];
          if (startedAt && now - startedAt < 5000) {
            map[id] = true;
          }
        }
      }
      return { streamingSessions: map };
    }),

  sessionTotalDuration: {},
  addSessionDuration: (sessionId, ms) =>
    set((state) => ({
      sessionTotalDuration: {
        ...state.sessionTotalDuration,
        [sessionId]: (state.sessionTotalDuration[sessionId] || 0) + ms,
      },
    })),
  setSessionDuration: (sessionId, ms) =>
    set((state) => ({
      sessionTotalDuration: {
        ...state.sessionTotalDuration,
        [sessionId]: ms,
      },
    })),

  pickedTasks: {},
  addPickedTask: (sessionId, task) =>
    set((state) => {
      const existing = state.pickedTasks[sessionId] || [];
      if (existing.some((t) => t.id === task.id)) return state; // deduplicate
      return {
        pickedTasks: {
          ...state.pickedTasks,
          [sessionId]: [...existing, task],
        },
      };
    }),
  removePickedTask: (sessionId, taskId) =>
    set((state) => ({
      pickedTasks: {
        ...state.pickedTasks,
        [sessionId]: (state.pickedTasks[sessionId] || []).filter((t) => t.id !== taskId),
      },
    })),
  clearPickedTasks: (sessionId) =>
    set((state) => ({
      pickedTasks: {
        ...state.pickedTasks,
        [sessionId]: [],
      },
    })),

  pickedAssets: {},
  addPickedAsset: (sessionId, asset) =>
    set((state) => {
      const existing = state.pickedAssets[sessionId] || [];
      if (existing.some((a) => a.id === asset.id)) return state;
      return {
        pickedAssets: {
          ...state.pickedAssets,
          [sessionId]: [...existing, asset],
        },
      };
    }),
  removePickedAsset: (sessionId, assetId) =>
    set((state) => ({
      pickedAssets: {
        ...state.pickedAssets,
        [sessionId]: (state.pickedAssets[sessionId] || []).filter((a) => a.id !== assetId),
      },
    })),
  clearPickedAssets: (sessionId) =>
    set((state) => ({
      pickedAssets: {
        ...state.pickedAssets,
        [sessionId]: [],
      },
    })),

  pickedWorkflows: {},
  addPickedWorkflow: (sessionId, wf) =>
    set((state) => {
      const existing = state.pickedWorkflows[sessionId] || [];
      if (existing.some((w) => w.id === wf.id)) return state;
      return {
        pickedWorkflows: {
          ...state.pickedWorkflows,
          [sessionId]: [...existing, wf],
        },
      };
    }),
  removePickedWorkflow: (sessionId, wfId) =>
    set((state) => ({
      pickedWorkflows: {
        ...state.pickedWorkflows,
        [sessionId]: (state.pickedWorkflows[sessionId] || []).filter((w) => w.id !== wfId),
      },
    })),
  clearPickedWorkflows: (sessionId) =>
    set((state) => ({
      pickedWorkflows: {
        ...state.pickedWorkflows,
        [sessionId]: [],
      },
    })),

  pickedComments: {},
  addPickedComment: (sessionId, comment) =>
    set((state) => ({
      pickedComments: {
        ...state.pickedComments,
        [sessionId]: [...(state.pickedComments[sessionId] || []), comment],
      },
    })),
  removePickedComment: (sessionId, commentId) =>
    set((state) => ({
      pickedComments: {
        ...state.pickedComments,
        [sessionId]: (state.pickedComments[sessionId] || []).filter((c) => c.id !== commentId),
      },
    })),
  clearPickedComments: (sessionId) =>
    set((state) => ({
      pickedComments: {
        ...state.pickedComments,
        [sessionId]: [],
      },
    })),

  clientQueue: {},
  enqueueRichMessage: (sessionId, msg) =>
    set((state) => ({
      clientQueue: {
        ...state.clientQueue,
        [sessionId]: [...(state.clientQueue[sessionId] || []), msg],
      },
    })),
  dequeueRichMessage: (sessionId, msgId) =>
    set((state) => ({
      clientQueue: {
        ...state.clientQueue,
        [sessionId]: (state.clientQueue[sessionId] || []).filter((m) => m.id !== msgId),
      },
    })),
  updateRichQueuedText: (sessionId, msgId, text) =>
    set((state) => ({
      clientQueue: {
        ...state.clientQueue,
        [sessionId]: (state.clientQueue[sessionId] || []).map((m) =>
          m.id === msgId ? { ...m, text } : m
        ),
      },
    })),
  connectStatus: "idle",
  setConnectStatus: (status) => set({ connectStatus: status }),

  gitDiffStats: null,
  setGitDiffStats: (stats) => set({ gitDiffStats: stats }),

  marketplaceUpdateCount: 0,
  setMarketplaceUpdateCount: (count) => set({ marketplaceUpdateCount: count }),

  currentUser: null,
  setCurrentUser: (user) => set({ currentUser: user }),

  balanceTokens: 0,
  balanceCny: 0,
  setBalance: (tokens, cny) => set({ balanceTokens: tokens, balanceCny: cny }),
}),
    {
      name: "boxnex-app",
      // Persist activeSessionId to remember which session was last selected
      partialize: (state) => ({
        activeSessionId: state.activeSessionId,
      }),
      onRehydrateStorage: () => (state) => {
        console.log('[Store] Rehydrated from localStorage:', state?.activeSessionId);
      },
    }
  )
);
