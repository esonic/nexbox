import { useState, useRef, useEffect, useCallback, useMemo } from "react";
import { toast } from "sonner";
import { globalWS } from "@/lib/ws";
import { messagesApi, serverApi, sessionsApi, tasksApi, queueApi } from "@/lib/api";
import type { QueuedMessage } from "@/lib/api";
import { useAppStore } from "@/stores/app";
import {
  parseAskQuestionInput,
} from "@/components/chat/QuestionCard";
import { TODO_TOOLS } from "@/components/chat/ToolCallCard";
import { foldTasks, activeTaskLabel, allTasksDone, type TaskCall } from "@/lib/taskFold";
import type { AttachedImage } from "@/components/chat/ChatInput";
import { uuid } from "@/lib/utils";
import type { Session, Task, ChatEvent, ChatMessage as ChatMessageType, ModelInfo } from "@/types";
import type { PickedAsset, PickedWorkflow, RichQueuedMessage } from "@/stores/app";

export type { QueuedMessage } from "@/lib/api";
export type { RichQueuedMessage } from "@/stores/app";

const LAST_MODEL_KEY_PREFIX = "nexbox-last-model:";
const LAST_EFFORT_KEY_PREFIX = "nexbox-last-effort:";

// Effort is stored per (agent, model) because supported_efforts varies by model
// (e.g. Claude Opus accepts "xhigh"/"max", Sonnet may not).
function lastEffortKey(agent: string, model: string) {
  return `${LAST_EFFORT_KEY_PREFIX}${agent}:${model}`;
}

// Stable empty-array constants so Zustand selectors don't trigger re-renders
// when a session key is missing (undefined ?? EMPTY gives a stable reference).
const EMPTY_MESSAGES: ChatMessageType[] = [];
const EMPTY_TASKS: Task[] = [];
const EMPTY_ASSETS: PickedAsset[] = [];
const EMPTY_WORKFLOWS: PickedWorkflow[] = [];
const EMPTY_COMMENTS: RichQueuedMessage["pickedComments"] = [];
const EMPTY_RICH_QUEUE: RichQueuedMessage[] = [];

/** Serialize inline comments into a compact text block for the AI. */
function serializeComments(comments: RichQueuedMessage["pickedComments"]): string {
  return comments
    .map((c) => `- ${c.filePath}:${c.lineNumber}: ${c.text}`)
    .join("\n");
}

/** Build and send a rich queued message via WebSocket. Can be called from WS event handlers. */
function sendRichQueuedMessageDirect(sid: string, msg: RichQueuedMessage) {
  const gs = useAppStore.getState();

  const parts: ChatMessageType["parts"] = [];
  if (msg.images) {
    for (const img of msg.images) {
      parts.push({ type: "image", url: img.url, filename: img.filename });
    }
  }

  let finalMessage = msg.text;
  if (msg.pickedComments.length > 0) {
    const commentBlock = serializeComments(msg.pickedComments);
    finalMessage = msg.text
      ? `${msg.text}\n\n[Code Comments]\n${commentBlock}`
      : `[Code Comments]\n${commentBlock}`;
  }

  if (finalMessage) parts.push({ type: "text", content: finalMessage });
  for (const t of msg.pickedTasks) parts.push({ type: "task_ref", taskId: t.id, task: t });
  for (const a of msg.pickedAssets) parts.push({ type: "asset_ref", assetId: a.id, title: a.title, assetType: a.assetType });
  for (const w of msg.pickedWorkflows) parts.push({ type: "workflow_ref", workflowId: w.id, name: w.name });

  const imageFilenames = msg.images?.map((i) => i.filename).filter((f): f is string => !!f) ?? [];

  const now = new Date().toISOString();
  gs.addChatMessage(sid, { id: uuid(), session_id: sid, role: "user", parts, metadata: {}, created_at: now });
  gs.addChatMessage(sid, { id: uuid(), session_id: sid, role: "assistant", parts: [], metadata: {}, created_at: now, isStreaming: true });

  globalWS.send(sid, finalMessage, parts, imageFilenames.length > 0 ? imageFilenames : undefined);
  gs.setStreamingSession(sid, true);
}

// WF[xxxxx] prefix written by the workflow orchestrator. We preserve it
// when checking for "default" names so workflow sessions also receive an
// auto-generated title — the backend re-applies the prefix on save.
const WF_PREFIX_RE = /^WF\[[A-Fa-f0-9]+\]\s*/;

/**
 * Kick off backend auto-title generation for the first user message.
 *
 * Skips when the user has manually named the session (anything that isn't the
 * placeholder name or a WF-prefixed orchestrator name). The backend pins
 * Claude+Haiku and handles the WF prefix server-side; we just push the raw
 * first message and update local store when the response lands.
 */
function autoRenameSession(
  sessionId: string,
  sessionName: string,
  firstText: string,
  pickedTasks: Task[],
  pickedAssets: PickedAsset[],
) {
  const raw = (sessionName || "").trim();
  const hasWFPrefix = WF_PREFIX_RE.test(raw);
  const withoutPrefix = raw.replace(WF_PREFIX_RE, "").trim();
  // WF-prefixed sessions always trigger — backend keeps the prefix and only
  // replaces the workflow/node label after it. Non-WF sessions trigger only
  // when still on the placeholder name; never overwrite a user-edited title.
  const shouldRename = hasWFPrefix || !withoutPrefix || withoutPrefix === "New Session" || /^Session\s+\d+$/i.test(withoutPrefix);
  if (!shouldRename) return;

  const message = (firstText || pickedTasks[0]?.title || pickedAssets[0]?.title || "").trim();
  if (!message) return;

  sessionsApi.autoTitle(sessionId, message)
    .then((updated) => {
      const { sessions, setSessions } = useAppStore.getState();
      setSessions(sessions.map((s) => (s.id === updated.id ? updated : s)));
    })
    .catch((err) => console.error("[autoRename] Failed to generate session title:", err));
}

interface UseChatSessionOptions {
  session: Session;
}

/**
 * useChatSession — Encapsulates all WebSocket, streaming, and message state
 * management for a chat session. Keeps the ChatPanel component focused on rendering.
 */
export function useChatSession({ session }: UseChatSessionOptions) {
  const [isConnected, setIsConnected] = useState(globalWS.connected);
  const [loadingHistory, setLoadingHistory] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [hasMore, setHasMore] = useState(false);
  const [serverAgent, setServerAgent] = useState<string>("");
  const [availableAgents, setAvailableAgents] = useState<string[]>([]);
  const [availableModels, setAvailableModels] = useState<string[]>([]);
  const [availableModelInfos, setAvailableModelInfos] = useState<ModelInfo[]>([]);
  // Which agent the currently held available models belong to. Used to guard
  // against the race where `agentName` updates instantly but the async fetch
  // of the new agent's model list hasn't landed yet — in that window we must
  // not write an old-agent model into the new session.
  const [modelsAgent, setModelsAgent] = useState<string>("");
  const [messageQueue, setMessageQueue] = useState<QueuedMessage[]>([]);

  const agentName = session.agent || serverAgent;
  const modelName = session.model || (availableModels[0] || "");
  const sessionId = session.id;
  const sessionIdRef = useRef(sessionId);
  sessionIdRef.current = sessionId;

  // Use fine-grained selectors to avoid re-renders from unrelated store changes.
  // IMPORTANT: selectors must return the stored reference directly (not `|| []`)
  // because `|| []` creates a new array each render, defeating Zustand's Object.is check.
  const messages = useAppStore((s) => s.chatMessages[sessionId]) ?? EMPTY_MESSAGES;
  const isStreaming = useAppStore((s) => !!s.streamingSessions[sessionId]);
  const pickedTasks = useAppStore((s) => s.pickedTasks[sessionId]) ?? EMPTY_TASKS;
  const pickedAssets = useAppStore((s) => s.pickedAssets[sessionId]) ?? EMPTY_ASSETS;
  const pickedWorkflows = useAppStore((s) => s.pickedWorkflows[sessionId]) ?? EMPTY_WORKFLOWS;
  const pickedComments = useAppStore((s) => s.pickedComments[sessionId]) ?? EMPTY_COMMENTS;
  const richQueue = useAppStore((s) => s.clientQueue[sessionId]) ?? EMPTY_RICH_QUEUE;

  // Action selectors (stable references — these never cause re-renders)
  const setChatMessages = useAppStore((s) => s.setChatMessages);
  const addChatMessage = useAppStore((s) => s.addChatMessage);
  const setStreamingSession = useAppStore((s) => s.setStreamingSession);
  const storeAddPickedTask = useAppStore((s) => s.addPickedTask);
  const storeRemovePickedTask = useAppStore((s) => s.removePickedTask);
  const clearPickedTasks = useAppStore((s) => s.clearPickedTasks);
  const storeAddPickedAsset = useAppStore((s) => s.addPickedAsset);
  const storeRemovePickedAsset = useAppStore((s) => s.removePickedAsset);
  const clearPickedAssets = useAppStore((s) => s.clearPickedAssets);
  const storeAddPickedWorkflow = useAppStore((s) => s.addPickedWorkflow);
  const storeRemovePickedWorkflow = useAppStore((s) => s.removePickedWorkflow);
  const clearPickedWorkflows = useAppStore((s) => s.clearPickedWorkflows);
  const clearPickedComments = useAppStore((s) => s.clearPickedComments);
  const enqueueRichMessage = useAppStore((s) => s.enqueueRichMessage);
  const dequeueRichMessage = useAppStore((s) => s.dequeueRichMessage);
  const updateRichQueuedText = useAppStore((s) => s.updateRichQueuedText);

  const sessionHasMessages = messages.length > 0;

  // Fetch queue from server
  const refreshQueue = useCallback(async (sid?: string) => {
    const id = sid || sessionIdRef.current;
    try {
      const items = await queueApi.list(id);
      setMessageQueue(items);
    } catch { /* ignore */ }
  }, []);

  useEffect(() => {
    refreshQueue(sessionId);
  }, [sessionId, refreshQueue]);

  // Detect pending ask_question
  const pendingQuestion = useMemo(() => {
    if (isStreaming) return null;
    const lastAssistant = [...messages].reverse().find((m) => m.role === "assistant");
    if (!lastAssistant || lastAssistant.isStreaming) return null;
    const askToolPart = lastAssistant.parts.find(
      (p) => p.type === "tool_use" && p.toolName?.includes("ask_question")
    );
    if (!askToolPart || askToolPart.type !== "tool_use") return null;
    const lastMsg = messages[messages.length - 1];
    if (lastMsg?.id !== lastAssistant.id) return null;
    return parseAskQuestionInput(askToolPart.input);
  }, [messages, isStreaming]);

  // Track pending interactive tool calls (AskUserQuestion from Claude CLI).
  // These arrive as user_input_request events DURING streaming.
  const [pendingUserInput, setPendingUserInput] = useState<{
    toolUseId: string;
    toolName: string;
    input: string;
  } | null>(null);

  // Reconstruct the live task list from the current turn's task tool calls —
  // only during streaming, so it clears automatically when the turn ends.
  //
  // Newer Claude Code emits granular TaskCreate/TaskUpdate/TaskList calls
  // (instead of a single TodoWrite snapshot), so the list state is spread across
  // many calls and several assistant messages. We collect every task tool call
  // since the last user message and fold them back into one list. See taskFold.ts.
  const pendingTodo = useMemo(() => {
    if (!isStreaming) return null;
    type ToolUsePart = Extract<ChatMessageType["parts"][number], { type: "tool_use" }>;
    type ToolResultPart = Extract<ChatMessageType["parts"][number], { type: "tool_result" }>;

    // Walk back to the start of the current turn (last user message).
    let start = messages.length;
    for (let i = messages.length - 1; i >= 0; i--) {
      if (messages[i].role === "user") break;
      start = i;
    }

    // Map tool_use_id → output text across the whole turn (results may land in a
    // later assistant message than the originating tool_use).
    const outputs = new Map<string, string>();
    for (let i = start; i < messages.length; i++) {
      for (const p of messages[i].parts) {
        if (p.type === "tool_result") {
          outputs.set((p as ToolResultPart).toolCallId, (p as ToolResultPart).output ?? "");
        }
      }
    }

    // Collect task tool calls in chronological order.
    const calls: TaskCall[] = [];
    for (let i = start; i < messages.length; i++) {
      const msg = messages[i];
      if (msg.role !== "assistant") continue;
      for (const p of msg.parts) {
        if (p.type === "tool_use" && TODO_TOOLS.has((p as ToolUsePart).toolName)) {
          const tu = p as ToolUsePart;
          calls.push({ toolName: tu.toolName, input: tu.input, output: outputs.get(tu.toolCallId) });
        }
      }
    }
    if (calls.length === 0) return null;

    const todos = foldTasks(calls);
    if (todos.length === 0 || allTasksDone(todos)) return null;

    return { todos, isPending: true, title: activeTaskLabel(todos) };
  }, [messages, isStreaming]);

  // Fetch server default agent and available agents
  useEffect(() => {
    serverApi.info().then((info) => setServerAgent(info.agent)).catch(() => {});
    serverApi.availableAgents().then((data) => setAvailableAgents(data.agents || [])).catch(() => {});
  }, []);

  useEffect(() => {
    if (!agentName) {
      setAvailableModels([]);
      setAvailableModelInfos([]);
      setModelsAgent("");
      return;
    }
    // Clear immediately so effects that depend on the list don't act on
    // stale data while the new agent's list is being fetched.
    setAvailableModels([]);
    setAvailableModelInfos([]);
    setModelsAgent("");
    let cancelled = false;
    // Both Claude and Codex emit label/description + supported_efforts directly.
    // No client-side massaging — if the list is empty (CLI not reachable), show empty.
    serverApi.availableModels(agentName).then((data) => {
      if (cancelled) return;
      setAvailableModels(data.models || []);
      setAvailableModelInfos(data.models_info || []);
      setModelsAgent(agentName);
    }).catch(() => {
      if (cancelled) return;
      setAvailableModels([]);
      setAvailableModelInfos([]);
      setModelsAgent(agentName);
    });
    return () => { cancelled = true; };
  }, [agentName]);

  // When a session has no model set, apply the last-used model for this agent.
  // Guarded on `modelsAgent === agentName` so we never write an old-agent
  // default into a freshly switched session.
  useEffect(() => {
    if (!agentName || modelsAgent !== agentName) return;
    if (availableModels.length === 0 || session.model) return;
    let defaultModel = availableModels[0];
    try {
      const stored = localStorage.getItem(LAST_MODEL_KEY_PREFIX + agentName);
      if (stored && availableModels.includes(stored)) defaultModel = stored;
    } catch { /* ignore */ }
    sessionsApi.update(session.id, { model: defaultModel }).then((updated) => {
      const { sessions, setSessions } = useAppStore.getState();
      setSessions(sessions.map((s) => (s.id === updated.id ? updated : s)));
    }).catch(() => {});
  }, [agentName, modelsAgent, availableModels, session.model, session.id]);

  // Apply the model's default effort when the session is missing one, or
  // clear the stored effort if the current model doesn't accept it. Models
  // that don't support effort at all (empty supported_efforts) drop the
  // effort back to the empty string. Guarded on `modelsAgent === agentName`
  // so we don't validate a Claude effort against a Codex model list (or
  // vice-versa) during an agent switch.
  useEffect(() => {
    if (!session.model || availableModelInfos.length === 0) return;
    if (modelsAgent !== agentName) return;
    const info = availableModelInfos.find((m) => m.id === session.model);
    if (!info) return;
    const efforts = info.supported_efforts || [];
    let target: string;
    if (efforts.length === 0) {
      target = "";
    } else if (session.effort && efforts.includes(session.effort)) {
      return; // already valid
    } else {
      let stored = "";
      try { stored = localStorage.getItem(lastEffortKey(agentName, session.model)) || ""; } catch { /* ignore */ }
      target = (stored && efforts.includes(stored)) ? stored : (info.default_effort || efforts[0] || "");
    }
    if (target === (session.effort || "")) return;
    sessionsApi.update(session.id, { effort: target }).then((updated) => {
      const { sessions, setSessions } = useAppStore.getState();
      setSessions(sessions.map((s) => (s.id === updated.id ? updated : s)));
    }).catch(() => {});
  }, [session.id, session.model, session.effort, availableModelInfos, modelsAgent, agentName]);

  // Load persisted chat history (initial load with pagination)
  useEffect(() => {
    if (!sessionId) return;
    if (useAppStore.getState().chatMessages[sessionId]?.length) return;

    let cancelled = false;
    let reconcileTimer: ReturnType<typeof setTimeout> | undefined;
    setLoadingHistory(true);

    messagesApi.list(sessionId, 20).then(({ messages: persisted, active, has_more }) => {
      if (cancelled) return;
      const liveMessages = useAppStore.getState().chatMessages[sessionId] || [];
      if (liveMessages.length > 0) {
        setHasMore(has_more || false);
        return;
      }
      const mapped: ChatMessageType[] = (persisted || [])
        .filter((m) => m.role === "user" || (m.role === "assistant" && (m.parts?.length ?? 0) > 0))
        .map((m) => ({ ...m, parts: m.parts ?? [], isStreaming: false }));

      // Restore accumulated duration from persisted message metadata
      let totalDuration = 0;
      for (const m of persisted || []) {
        if (m.role === "assistant" && m.metadata?.durationMs) {
          totalDuration += m.metadata.durationMs;
        }
      }
      if (totalDuration > 0) {
        useAppStore.getState().setSessionDuration(sessionId, totalDuration);
      }

      if (active) {
        // Mark the last assistant message as still streaming so new events append to it
        // rather than creating a second bubble alongside the DB partial content.
        const lastAsstIdx = mapped.reduce((best, m, i) => m.role === "assistant" ? i : best, -1);
        if (lastAsstIdx >= 0) {
          mapped[lastAsstIdx] = { ...mapped[lastAsstIdx], isStreaming: true };
        } else {
          mapped.push({
            id: `reconnecting-${sessionId}`,
            session_id: sessionId,
            role: "assistant",
            parts: [],
            metadata: {},
            created_at: new Date().toISOString(),
            isStreaming: true,
          });
        }
        setStreamingSession(sessionId, true);

        // Fresh-load recovery for a lost `done`. A brand-new WebSocket may
        // subscribe *after* the server emitted this turn's `done` (the hub
        // broadcasts live and never replays). Unlike a disconnect→reconnect,
        // a fresh page load never runs the recovery in the connection effect
        // below, so the last assistant message would stay stuck
        // isStreaming=true forever — which also hides the fork button even
        // though sdkMessageUuid is already persisted. Once the socket has had
        // time to subscribe, re-check the server's active state; if the turn
        // already finished, finalize from the DB snapshot (which carries the
        // persisted metadata) instead of waiting for another manual refresh.
        reconcileTimer = setTimeout(() => {
          if (cancelled) return;
          if (!useAppStore.getState().streamingSessions[sessionId]) return; // a live `done` resolved it
          messagesApi.list(sessionId, 20).then(({ messages: recovered, active: stillActive, has_more: more }) => {
            if (cancelled || stillActive) return; // genuinely still running: live events will arrive
            const gs = useAppStore.getState();
            if (!gs.streamingSessions[sessionId]) return;
            const refreshed: ChatMessageType[] = (recovered || [])
              .filter((m) => m.role === "user" || (m.role === "assistant" && (m.parts?.length ?? 0) > 0))
              .map((m) => ({ ...m, parts: m.parts ?? [], isStreaming: false }));
            gs.setStreamingSession(sessionId, false);
            if (refreshed.length > 0) {
              gs.setChatMessages(sessionId, refreshed);
              setHasMore(more || false);
            } else {
              gs.finishStreaming(sessionId);
            }
          }).catch(() => {});
        }, 1500);
      }

      if (mapped.length > 0) {
        setChatMessages(sessionId, mapped);
      }
      setHasMore(has_more || false);
    }).catch((err) => {
      console.error("Failed to load chat history:", err);
    }).finally(() => {
      if (!cancelled) setLoadingHistory(false);
    });

    return () => { cancelled = true; if (reconcileTimer) clearTimeout(reconcileTimer); };
  }, [sessionId]);

  // WS connection state + reconnect recovery
  // seenDisconnect tracks whether we actually experienced a disconnection.
  // onConnection() fires immediately on registration even when WS is already connected,
  // and we must not treat that initial fire as a reconnection (it would reset streaming state).
  // nestedToolIdsRef tracks which tool_use_ids belong to a sub-agent so
  // their tool_input_delta / tool_result events can be routed to the
  // nested-tool helpers. Populated at tool_start from parent_tool_id and
  // drained on tool_result.
  const nestedToolIdsRef = useRef<Set<string>>(new Set());
  const seenDisconnectRef = useRef(false);
  useEffect(() => {
    seenDisconnectRef.current = false;
    return globalWS.onConnection((connected) => {
      setIsConnected(connected);
      if (!connected) {
        seenDisconnectRef.current = true;
        return;
      }
      refreshQueue();
      // Only do reconnect recovery when we actually saw a disconnection.
      if (!seenDisconnectRef.current) return;
      seenDisconnectRef.current = false;
      // When WS reconnects, check if our session is still actually active on the server.
      // This handles the case where the "done" event was lost during a disconnect.
      const gs = useAppStore.getState();
      if (gs.streamingSessions[sessionId]) {
        messagesApi.list(sessionId, 20).then(({ messages: persisted, active, has_more }) => {
          if (!active) {
            gs.setStreamingSession(sessionId, false);
            gs.finishStreaming(sessionId);
          }
          // Reload DB messages so any content accumulated during the disconnect
          // (via periodic server-side flush) becomes visible without a full page refresh.
          const refreshed: ChatMessageType[] = (persisted || [])
            .filter((m) => m.role === "user" || (m.role === "assistant" && (m.parts?.length ?? 0) > 0))
            .map((m) => ({ ...m, parts: m.parts ?? [], isStreaming: false }));
          if (refreshed.length > 0) {
            if (active) {
              const lastAsstIdx = refreshed.reduce((best, m, i) => m.role === "assistant" ? i : best, -1);
              if (lastAsstIdx >= 0) {
                refreshed[lastAsstIdx] = { ...refreshed[lastAsstIdx], isStreaming: true };
              } else {
                refreshed.push({
                  id: `reconnecting-${sessionId}`,
                  session_id: sessionId,
                  role: "assistant",
                  parts: [],
                  metadata: {},
                  created_at: new Date().toISOString(),
                  isStreaming: true,
                });
              }
            }
            gs.setChatMessages(sessionId, refreshed);
            if (has_more !== undefined) setHasMore(has_more);
          }
        }).catch(() => {});
      }
    });
  }, [sessionId, refreshQueue]);

  // ── WS event processing ──
  const processEvent = useCallback((sid: string, event: ChatEvent) => {
    const gs = () => useAppStore.getState();

    const ensureStreaming = () => {
      const currentMsgs = gs().chatMessages[sid] || [];
      const last = currentMsgs[currentMsgs.length - 1];
      if (!last || last.role !== "assistant" || !last.isStreaming) {
        gs().addChatMessage(sid, {
          id: uuid(),
          session_id: sid,
          role: "assistant",
          parts: [],
          metadata: {},
          created_at: new Date().toISOString(),
          isStreaming: true,
        });
        gs().setStreamingSession(sid, true);
      }
    };

    switch (event.type) {
      case "user_message":
        if (event.content && !gs().streamingSessions[sid]) {
          gs().addChatMessage(sid, {
            id: uuid(),
            session_id: sid,
            role: "user",
            parts: [{ type: "text", content: event.content }],
            metadata: {},
            created_at: new Date().toISOString(),
            isStreaming: false,
          });
        }
        break;
      case "text_delta":
        if (event.content) { ensureStreaming(); gs().appendTextDelta(sid, event.content); }
        break;
      case "tool_start":
        if (event.tool && event.tool_id) {
          ensureStreaming();
          if (event.parent_tool_id) {
            // Sub-agent call: nest under the parent Task tool identified
            // by parent_tool_id. This works correctly even when several
            // sub-agents run in parallel, because each event carries its
            // own parent reference rather than relying on a shared
            // "currently active agent" pointer.
            nestedToolIdsRef.current.add(event.tool_id);
            gs().addNestedToolStart(sid, event.parent_tool_id, event.tool_id, event.tool, event.input || "");
          } else {
            gs().addToolStart(sid, event.tool_id, event.tool, event.input);
          }
        }
        break;
      case "tool_input_delta":
        if (event.tool_id && event.input) {
          if (nestedToolIdsRef.current.has(event.tool_id)) {
            gs().updateNestedToolInput(sid, event.tool_id, event.input);
          } else {
            gs().updateToolInput(sid, event.tool_id, event.input);
          }
        }
        break;
      case "tool_result":
        if (event.tool_id) {
          const isNested = !!event.parent_tool_id || nestedToolIdsRef.current.has(event.tool_id);
          if (isNested) {
            gs().addNestedToolResult(sid, event.tool_id, event.output || "", event.is_error);
            nestedToolIdsRef.current.delete(event.tool_id);
          } else {
            gs().addToolResult(sid, event.tool_id, event.output || "", event.is_error);
          }
        }
        break;
      case "thinking_delta":
        if (event.content !== undefined) { ensureStreaming(); gs().appendThinkingDelta(sid, event.content || ""); }
        break;
      case "user_input_request":
        if (event.tool_id && event.tool) {
          setPendingUserInput({
            toolUseId: event.tool_id,
            toolName: event.tool,
            input: event.input || "",
          });
        }
        break;
      case "widget":
        if (event.widget_code) {
          ensureStreaming();
          gs().addWidgetPart(sid, event.widget_title || "", event.widget_code, event.loading_messages);
        }
        break;
      case "context_usage":
        if (event.context_usage) {
          gs().setSessionContextUsage(sid, event.context_usage);
        }
        break;
      case "session_info":
        // Currently surfaced only as a debug log; could be used to
        // display the CLI-resolved model slug, cwd, etc. in the UI.
        break;
      case "done":
        setPendingUserInput(null);
        gs().setStreamingSession(sid, false);
        gs().finishStreaming(sid, event.stats || undefined, event.sdk_message_uuid);
        if (event.stats?.duration_ms) {
          gs().addSessionDuration(sid, event.stats.duration_ms);
        }
        window.dispatchEvent(new CustomEvent("session-updated"));
        // Auto-send next rich queued message
        {
          const nextItems = gs().clientQueue[sid] || [];
          if (nextItems.length > 0) {
            const next = nextItems[0];
            gs().dequeueRichMessage(sid, next.id);
            sendRichQueuedMessageDirect(sid, next);
          }
        }
        break;
      case "error":
        setPendingUserInput(null);
        if (event.message) { ensureStreaming(); gs().appendTextDelta(sid, `\n\n**Error:** ${event.message}`); }
        gs().setStreamingSession(sid, false);
        gs().finishStreaming(sid);
        window.dispatchEvent(new CustomEvent("session-updated"));
        break;
      case "subtask_start": {
        ensureStreaming();
        // task_id is the tool_use_id of the Task tool that launched this
        // sub-agent — pass it through so the summary lands on the right
        // Agent card when multiple sub-agents are running in parallel.
        if (event.content) gs().setAgentToolSummary(sid, event.content, event.task_id);
        break;
      }
      case "subtask_progress":
        // Dynamic Workflow tool: the server pushes the full merged snapshot
        // (workflow_progress) keyed by the Workflow tool_use_id (tool_id);
        // attach it to that card. Non-workflow Task sub-agents carry no
        // snapshot — ignore them, since their nested tool calls already stream
        // as tool_start events with parent_tool_id.
        if (event.workflow_progress && event.tool_id) {
          ensureStreaming();
          gs().setWorkflowProgress(sid, event.tool_id, event.workflow_progress);
        }
        break;
      case "subtask_done":
        // Workflow finished: persist the final snapshot on the card. Task
        // sub-agents complete via their own tool_result event (no-op here).
        if (event.workflow_progress && event.tool_id) {
          ensureStreaming();
          gs().setWorkflowProgress(sid, event.tool_id, event.workflow_progress);
        }
        break;
      case "system":
        if (event.content) { ensureStreaming(); gs().appendTextDelta(sid, `\n\n_[System: ${event.content}]_\n\n`); }
        break;
      case "queue_updated":
        refreshQueue(sid);
        break;
    }
  }, [refreshQueue]);

  // WS subscription management
  const activeSubs = useRef<Map<string, () => void>>(new Map());

  useEffect(() => {
    const sid = sessionId;
    if (!activeSubs.current.has(sid)) {
      const unsub = globalWS.subscribe(sid, (event) => processEvent(sid, event));
      activeSubs.current.set(sid, unsub);
    }
    return () => {
      const stillStreaming = useAppStore.getState().streamingSessions[sid];
      if (!stillStreaming) {
        const sub = activeSubs.current.get(sid);
        if (sub) { sub(); activeSubs.current.delete(sid); }
      }
    };
  }, [sessionId, processEvent]);

  useEffect(() => {
    return () => {
      for (const unsub of activeSubs.current.values()) unsub();
      activeSubs.current.clear();
    };
  }, []);

  // ── Actions ──

  const sendMessage = useCallback(async (message: string, images?: AttachedImage[], features?: string[]) => {
    const trimmed = message.trim();
    const hasPickedTasks = pickedTasks.length > 0;
    const hasPickedAssets = pickedAssets.length > 0;
    const hasPickedWorkflows = pickedWorkflows.length > 0;
    const hasPickedComments = pickedComments.length > 0;
    const hasImages = images && images.length > 0;
    if ((!trimmed && !hasPickedTasks && !hasPickedAssets && !hasPickedWorkflows && !hasPickedComments && !hasImages) || !isConnected) return;

    // When streaming, enqueue the message client-side with full structured refs
    if (isStreaming) {
      const imageData = images
        ?.filter((i): i is AttachedImage & { filename: string; url: string } => !!i.filename && !!i.url)
        .map((i) => ({ filename: i.filename, url: i.url }));

      const richMsg: RichQueuedMessage = {
        id: uuid(),
        text: trimmed,
        images: imageData && imageData.length > 0 ? imageData : undefined,
        pickedTasks: [...pickedTasks],
        pickedAssets: [...pickedAssets],
        pickedWorkflows: [...pickedWorkflows],
        pickedComments: [...pickedComments],
        created_at: new Date().toISOString(),
      };
      enqueueRichMessage(sessionId, richMsg);
      // Clear all refs immediately — they now belong to the queued message
      if (hasPickedTasks) clearPickedTasks(sessionId);
      if (hasPickedAssets) clearPickedAssets(sessionId);
      if (hasPickedWorkflows) clearPickedWorkflows(sessionId);
      if (hasPickedComments) clearPickedComments(sessionId);
      return;
    }

    // Auto-name session on first user message
    if (!messages.some((m) => m.role === "user")) {
      autoRenameSession(sessionId, session.name || "", trimmed || "Image analysis", pickedTasks, pickedAssets);
    }

    // Build message parts
    const parts: ChatMessageType["parts"] = [];
    if (hasImages) {
      for (const img of images) {
        if (img.filename && img.url) {
          parts.push({ type: "image", url: img.url, filename: img.filename });
        }
      }
    }
    // Append inline code comments to message text
    let finalMessage = trimmed;
    if (hasPickedComments) {
      const commentBlock = serializeComments(pickedComments);
      finalMessage = trimmed
        ? `${trimmed}\n\n[Code Comments]\n${commentBlock}`
        : `[Code Comments]\n${commentBlock}`;
    }

    if (finalMessage) parts.push({ type: "text", content: finalMessage });
    for (const t of pickedTasks) parts.push({ type: "task_ref", taskId: t.id, task: t });
    for (const a of pickedAssets) parts.push({ type: "asset_ref", assetId: a.id, title: a.title, assetType: a.assetType });
    for (const w of pickedWorkflows) parts.push({ type: "workflow_ref", workflowId: w.id, name: w.name });

    const imageFilenames = images?.map(i => i.filename).filter((f): f is string => !!f) ?? [];

    const now = new Date().toISOString();
    addChatMessage(sessionId, { id: uuid(), session_id: sessionId, role: "user", parts, metadata: {}, created_at: now });
    addChatMessage(sessionId, { id: uuid(), session_id: sessionId, role: "assistant", parts: [], metadata: {}, created_at: now, isStreaming: true });

    // A hidden ("scratch") session gets promoted server-side (is_hidden=0)
    // when the user's first message is persisted, and the server then
    // broadcasts a session change that triggers a loadSessions refetch in
    // AppSidebar — that's what drives the session into the sidebar list.
    // We deliberately don't flip is_hidden locally here: if the WS send
    // ever fails silently, the server never promotes, and a subsequent
    // loadSessions would see a non-hidden local session that's missing
    // from the server response and drop it from the merge — taking the
    // user's active session with it.

    globalWS.send(sessionId, finalMessage, parts, imageFilenames.length > 0 ? imageFilenames : undefined, features);

    clearPickedTasks(sessionId);
    clearPickedAssets(sessionId);
    clearPickedWorkflows(sessionId);
    clearPickedComments(sessionId);
    setStreamingSession(sessionId, true);
  }, [sessionId, isConnected, isStreaming, addChatMessage, messages, session.name, session.agent, serverAgent, pickedTasks, pickedAssets, pickedWorkflows, pickedComments, clearPickedTasks, clearPickedAssets, clearPickedWorkflows, clearPickedComments, setStreamingSession, enqueueRichMessage]);

  const handleStop = useCallback(() => {
    globalWS.stop(sessionId);
    setPendingUserInput(null);
    // Do NOT immediately finishStreaming here — let backend final events settle state.
    window.dispatchEvent(new Event('workflow-stop'));
  }, [sessionId]);

  const sendToolResponse = useCallback((toolUseId: string, response: string) => {
    globalWS.sendToolResponse(sessionId, toolUseId, response);
    setPendingUserInput(null);
  }, [sessionId]);

  const sendQueuedNow = useCallback(async (queuedId: string) => {
    try {
      await queueApi.run(sessionIdRef.current, queuedId);
    } catch (e) {
      console.error("Failed to run queued message:", e);
    }
  }, []);

  const removeQueued = useCallback(async (queuedId: string) => {
    try {
      await queueApi.delete(sessionIdRef.current, queuedId);
    } catch (e) {
      console.error("Failed to remove queued message:", e);
    }
  }, []);

  const editQueued = useCallback(async (queuedId: string, text: string) => {
    try {
      await queueApi.update(sessionIdRef.current, queuedId, text);
    } catch (e) {
      console.error("Failed to edit queued message:", e);
    }
  }, []);

  const sendRichQueuedNow = useCallback((msgId: string) => {
    if (isStreaming) return; // stay in queue while streaming
    const item = (useAppStore.getState().clientQueue[sessionId] || []).find((m: RichQueuedMessage) => m.id === msgId);
    if (!item) return;
    dequeueRichMessage(sessionId, msgId);
    sendRichQueuedMessageDirect(sessionId, item);
  }, [sessionId, isStreaming, dequeueRichMessage]);

  const removeRichQueued = useCallback((msgId: string) => {
    dequeueRichMessage(sessionId, msgId);
  }, [sessionId, dequeueRichMessage]);

  const editRichQueued = useCallback((msgId: string, text: string) => {
    updateRichQueuedText(sessionId, msgId, text);
  }, [sessionId, updateRichQueuedText]);

  const handleChangeAgent = useCallback(async (newAgent: string) => {
    if (sessionHasMessages) return;
    try {
      // Clear both model and effort: each agent has a different effort vocabulary
      // (e.g. Claude accepts "max", Codex doesn't). The auto-apply effects pick
      // fresh defaults once the new agent's model list is loaded.
      const updated = await sessionsApi.update(session.id, { agent: newAgent, model: "", effort: "" });
      const { sessions, setSessions } = useAppStore.getState();
      setSessions(sessions.map((s) => (s.id === updated.id ? updated : s)));
    } catch (e) {
      console.error("Failed to update session agent:", e);
      toast.error("Failed to change agent", { description: String(e) });
    }
  }, [session.id, sessionHasMessages]);

  const handleChangeModel = useCallback(async (newModel: string) => {
    try {
      // When switching models, re-align effort with what the new model accepts.
      const info = availableModelInfos.find((m) => m.id === newModel);
      const update: { model: string; effort?: string } = { model: newModel };
      if (info) {
        const efforts = info.supported_efforts || [];
        if (efforts.length === 0) {
          update.effort = ""; // new model doesn't support effort at all
        } else if (!session.effort || !efforts.includes(session.effort)) {
          let stored = "";
          try { stored = localStorage.getItem(lastEffortKey(agentName, newModel)) || ""; } catch { /* ignore */ }
          update.effort = (stored && efforts.includes(stored)) ? stored : (info.default_effort || efforts[0] || "");
        }
      }
      const updated = await sessionsApi.update(session.id, update);
      try { localStorage.setItem(LAST_MODEL_KEY_PREFIX + agentName, newModel); } catch { /* ignore */ }
      const { sessions, setSessions } = useAppStore.getState();
      setSessions(sessions.map((s) => (s.id === updated.id ? updated : s)));
    } catch (e) {
      console.error("Failed to update session model:", e);
      toast.error("Failed to change model", { description: String(e) });
    }
  }, [session.id, session.effort, agentName, availableModelInfos]);

  const handleChangeEffort = useCallback(async (newEffort: string) => {
    try {
      const updated = await sessionsApi.update(session.id, { effort: newEffort });
      if (session.model && newEffort) {
        try { localStorage.setItem(lastEffortKey(agentName, session.model), newEffort); } catch { /* ignore */ }
      }
      const { sessions, setSessions } = useAppStore.getState();
      setSessions(sessions.map((s) => (s.id === updated.id ? updated : s)));
    } catch (e) {
      console.error("Failed to update session effort:", e);
      toast.error("Failed to change effort", { description: String(e) });
    }
  }, [session.id, session.model, agentName]);

  const addPickedTask = useCallback((task: Task) => {
    storeAddPickedTask(sessionId, task);
  }, [sessionId, storeAddPickedTask]);

  const removePickedTask = useCallback((taskId: string) => {
    storeRemovePickedTask(sessionId, taskId);
  }, [sessionId, storeRemovePickedTask]);

  const addPickedAsset = useCallback((asset: PickedAsset) => {
    storeAddPickedAsset(sessionId, asset);
  }, [sessionId, storeAddPickedAsset]);

  const removePickedAsset = useCallback((assetId: string) => {
    storeRemovePickedAsset(sessionId, assetId);
  }, [sessionId, storeRemovePickedAsset]);

  const addPickedWorkflow = useCallback((wf: PickedWorkflow) => {
    storeAddPickedWorkflow(sessionId, wf);
  }, [sessionId, storeAddPickedWorkflow]);

  const removePickedWorkflow = useCallback((wfId: string) => {
    storeRemovePickedWorkflow(sessionId, wfId);
  }, [sessionId, storeRemovePickedWorkflow]);

  const loadMoreMessages = useCallback(async () => {
    if (loadingMore || !hasMore || messages.length === 0) return;

    const oldestMessage = messages[0];
    if (!oldestMessage) return;

    setLoadingMore(true);
    try {
      const { messages: olderMessages, has_more } = await messagesApi.list(sessionId, 20, oldestMessage.id);
      if (olderMessages && olderMessages.length > 0) {
        const mapped: ChatMessageType[] = olderMessages
          .filter((m) => m.role === "user" || (m.role === "assistant" && (m.parts?.length ?? 0) > 0))
          .map((m) => ({ ...m, parts: m.parts ?? [], isStreaming: false }));

        // Prepend older messages to existing ones
        const currentMessages = useAppStore.getState().chatMessages[sessionId] || [];
        useAppStore.getState().setChatMessages(sessionId, [...mapped, ...currentMessages]);
      }
      setHasMore(has_more || false);
    } catch (err) {
      console.error("Failed to load more messages:", err);
    } finally {
      setLoadingMore(false);
    }
  }, [sessionId, loadingMore, hasMore, messages]);

  // Listen for reference-asset events from AssetPanel cite buttons
  useEffect(() => {
    const handler = (e: Event) => {
      const detail = (e as CustomEvent<PickedAsset>).detail;
      if (detail) storeAddPickedAsset(sessionId, detail);
    };
    window.addEventListener("reference-asset", handler);
    return () => window.removeEventListener("reference-asset", handler);
  }, [sessionId, storeAddPickedAsset]);

  // Listen for reference-task events from IssueRow cite buttons
  useEffect(() => {
    const handler = async (e: Event) => {
      const detail = (e as CustomEvent<{ id: string; title: string; status: string }>).detail;
      if (!detail) return;
      try {
        const fullTask = await tasksApi.get(detail.id);
        storeAddPickedTask(sessionId, fullTask);
      } catch {
        storeAddPickedTask(sessionId, {
          id: detail.id,
          title: detail.title,
          description: "",
          status: (detail.status as Task["status"]) || "open",
          priority: "medium",
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        });
      }
    };
    window.addEventListener("reference-task", handler);
    return () => window.removeEventListener("reference-task", handler);
  }, [sessionId, storeAddPickedTask]);

  return {
    messages,
    isStreaming,
    isConnected,
    loadingHistory,
    loadingMore,
    hasMore,
    pendingQuestion,
    pendingTodo,
    pendingUserInput,
    sendToolResponse,
    agentName,
    modelName,
    effort: session.effort || "",
    availableAgents,
    availableModels,
    availableModelInfos,
    pickedTasks,
    pickedAssets,
    pickedWorkflows,
    pickedComments,
    sessionHasMessages,
    messageQueue,
    richQueue,
    sendMessage,
    handleStop,
    handleChangeAgent,
    handleChangeModel,
    handleChangeEffort,
    sendQueuedNow,
    removeQueued,
    editQueued,
    sendRichQueuedNow,
    removeRichQueued,
    editRichQueued,
    addPickedTask,
    removePickedTask,
    addPickedAsset,
    removePickedAsset,
    addPickedWorkflow,
    removePickedWorkflow,
    clearPickedComments: () => clearPickedComments(sessionId),
    loadMoreMessages,
  };
}
