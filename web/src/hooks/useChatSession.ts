import { useState, useRef, useEffect, useCallback, useMemo } from "react";
import { toast } from "sonner";
import { globalWS } from "@/lib/ws";
import { messagesApi, serverApi, sessionsApi, tasksApi, queueApi } from "@/lib/api";
import type { QueuedMessage } from "@/lib/api";
import { useAppStore } from "@/stores/app";
import {
  parseAskQuestionInput,
} from "@/components/chat/QuestionCard";
import type { AttachedImage } from "@/components/chat/ChatInput";
import { uuid } from "@/lib/utils";
import type { Session, Task, ChatEvent, ChatMessage as ChatMessageType } from "@/types";
import type { PickedAsset, PickedWorkflow, RichQueuedMessage } from "@/stores/app";

export type { QueuedMessage } from "@/lib/api";
export type { RichQueuedMessage } from "@/stores/app";

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

function buildSessionTitle(message: string): string {
  const cleaned = message.replace(/\s+/g, " ").trim();
  if (!cleaned) return "New Session";
  return (cleaned.split(/[.!?。！？\n]/)[0]?.trim() || cleaned).slice(0, 48) || "New Session";
}

/** Auto-rename session with a title derived from the first user message (if still default-named). */
function autoRenameSession(
  sessionId: string,
  sessionName: string,
  firstText: string,
  pickedTasks: Task[],
  pickedAssets: PickedAsset[],
) {
  const name = (sessionName || "").trim();
  const isDefault = !name || name === "New Session" || /^Session\s+\d+$/i.test(name);
  if (!isDefault) return;
  const title = buildSessionTitle(
    firstText || pickedTasks[0]?.title || pickedAssets[0]?.title || "New Session",
  );
  sessionsApi.update(sessionId, { name: title })
    .then(() => {
      const { sessions, setSessions } = useAppStore.getState();
      setSessions(sessions.map((s) => (s.id === sessionId ? { ...s, name: title } : s)));
    })
    .catch((err) => console.error("[autoRename] Failed to rename session:", err));
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
  const [messageQueue, setMessageQueue] = useState<QueuedMessage[]>([]);

  const agentName = session.agent || serverAgent;
  const sessionId = session.id;
  const sessionIdRef = useRef(sessionId);
  sessionIdRef.current = sessionId;

  const {
    chatMessages,
    setChatMessages,
    addChatMessage,
    finishStreaming,
    streamingSessions,
    setStreamingSession,
    pickedTasks: allPickedTasks,
    addPickedTask: storeAddPickedTask,
    removePickedTask: storeRemovePickedTask,
    clearPickedTasks,
    pickedAssets: allPickedAssets,
    addPickedAsset: storeAddPickedAsset,
    removePickedAsset: storeRemovePickedAsset,
    clearPickedAssets,
    pickedWorkflows: allPickedWorkflows,
    addPickedWorkflow: storeAddPickedWorkflow,
    removePickedWorkflow: storeRemovePickedWorkflow,
    clearPickedWorkflows,
    pickedComments: allPickedComments,
    clearPickedComments,
    clientQueue: allClientQueue,
    enqueueRichMessage,
    dequeueRichMessage,
    updateRichQueuedText,
  } = useAppStore();

  const isStreaming = !!streamingSessions[sessionId];
  const messages = chatMessages[sessionId] || [];
  const pickedTasks = allPickedTasks[sessionId] || [];
  const pickedAssets = allPickedAssets[sessionId] || [];
  const pickedWorkflows = allPickedWorkflows[sessionId] || [];
  const pickedComments = allPickedComments[sessionId] || [];
  const richQueue = allClientQueue[sessionId] || [];
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

  // Detect pending ask_question (MCP structured questions)
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
    question: string;
  } | null>(null);

  // Fetch server default agent and available agents
  useEffect(() => {
    serverApi.info().then((info) => setServerAgent(info.agent)).catch(() => {});
    serverApi.availableAgents().then((data) => setAvailableAgents(data.agents || [])).catch(() => {});
  }, []);

  // Load persisted chat history (initial load with pagination)
  useEffect(() => {
    if (!sessionId) return;
    if (chatMessages[sessionId]?.length) return;

    let cancelled = false;
    setLoadingHistory(true);

    messagesApi.list(sessionId, 20).then(({ messages: persisted, active, has_more }) => {
      if (cancelled) return;
      const mapped: ChatMessageType[] = (persisted || [])
        .filter((m) => m.role === "user" || (m.role === "assistant" && m.parts.length > 0))
        .map((m) => ({ ...m, isStreaming: false }));

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
        mapped.push({
          id: `reconnecting-${sessionId}`,
          session_id: sessionId,
          role: "assistant",
          parts: [],
          metadata: {},
          created_at: new Date().toISOString(),
          isStreaming: true,
        });
        setStreamingSession(sessionId, true);
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

    return () => { cancelled = true; };
  }, [sessionId]);

  // WS connection state + reconnect recovery
  useEffect(() => {
    return globalWS.onConnection((connected) => {
      setIsConnected(connected);
      // When WS reconnects, check if our session is still actually active on the server.
      // This handles the case where the "done" event was lost during a disconnect.
      if (connected) {
        refreshQueue();
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
              .filter((m) => m.role === "user" || (m.role === "assistant" && m.parts.length > 0))
              .map((m) => ({ ...m, isStreaming: false }));
            if (refreshed.length > 0) {
              if (active) {
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
              gs.setChatMessages(sessionId, refreshed);
              if (has_more !== undefined) setHasMore(has_more);
            }
          }).catch(() => {});
        }
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
        if (event.tool && event.tool_id) { ensureStreaming(); gs().addToolStart(sid, event.tool_id, event.tool, event.input); }
        break;
      case "tool_input_delta":
        if (event.tool_id && event.input) { gs().updateToolInput(sid, event.tool_id, event.input); }
        break;
      case "tool_result":
        if (event.tool_id) { gs().addToolResult(sid, event.tool_id, event.output || "", event.is_error); }
        break;
      case "thinking_delta":
        if (event.content !== undefined) { ensureStreaming(); gs().appendThinkingDelta(sid, event.content || ""); }
        break;
      case "user_input_request":
        if (event.tool_id && event.tool) {
          setPendingUserInput({
            toolUseId: event.tool_id,
            toolName: event.tool,
            question: event.content || "",
          });
        }
        break;
      case "done":
        setPendingUserInput(null);
        gs().setStreamingSession(sid, false);
        gs().finishStreaming(sid, event.stats || undefined);
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

    globalWS.send(sessionId, finalMessage, parts, imageFilenames.length > 0 ? imageFilenames : undefined, features);

    for (const t of pickedTasks) tasksApi.incrementRefCount(t.id).catch(() => {});

    clearPickedTasks(sessionId);
    clearPickedAssets(sessionId);
    clearPickedWorkflows(sessionId);
    clearPickedComments(sessionId);
    setStreamingSession(sessionId, true);
  }, [sessionId, isConnected, isStreaming, addChatMessage, messages, session.name, session.agent, serverAgent, pickedTasks, pickedAssets, pickedWorkflows, pickedComments, clearPickedTasks, clearPickedAssets, clearPickedWorkflows, clearPickedComments, setStreamingSession, enqueueRichMessage]);

  const handleStop = useCallback(() => {
    globalWS.stop(sessionId);
    setPendingUserInput(null);
    if (isStreaming) {
      setStreamingSession(sessionId, false);
      finishStreaming(sessionId);
    }
    window.dispatchEvent(new Event('workflow-stop'));
  }, [sessionId, isStreaming, setStreamingSession, finishStreaming]);

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
    const item = (allClientQueue[sessionId] || []).find((m) => m.id === msgId);
    if (!item) return;
    dequeueRichMessage(sessionId, msgId);
    sendRichQueuedMessageDirect(sessionId, item);
  }, [sessionId, isStreaming, allClientQueue, dequeueRichMessage]);

  const removeRichQueued = useCallback((msgId: string) => {
    dequeueRichMessage(sessionId, msgId);
  }, [sessionId, dequeueRichMessage]);

  const editRichQueued = useCallback((msgId: string, text: string) => {
    updateRichQueuedText(sessionId, msgId, text);
  }, [sessionId, updateRichQueuedText]);

  const handleChangeAgent = useCallback(async (newAgent: string) => {
    if (sessionHasMessages) return;
    try {
      const updated = await sessionsApi.update(session.id, { agent: newAgent });
      const { sessions, setSessions } = useAppStore.getState();
      setSessions(sessions.map((s) => (s.id === updated.id ? updated : s)));
    } catch (e) {
      console.error("Failed to update session agent:", e);
      toast.error("Failed to change agent", { description: String(e) });
    }
  }, [session.id, sessionHasMessages]);

  const handleChangeModel = useCallback(async (newModel: string) => {
    if (sessionHasMessages) return;
    try {
      const updated = await sessionsApi.update(session.id, { model: newModel });
      const { sessions, setSessions } = useAppStore.getState();
      setSessions(sessions.map((s) => (s.id === updated.id ? updated : s)));
    } catch (e) {
      console.error("Failed to update session model:", e);
      toast.error("Failed to change model", { description: String(e) });
    }
  }, [session.id, sessionHasMessages]);

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
          .filter((m) => m.role === "user" || (m.role === "assistant" && m.parts.length > 0))
          .map((m) => ({ ...m, isStreaming: false }));

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
          parent_id: "",
          title: detail.title,
          description: "",
          status: (detail.status as Task["status"]) || "open",
          priority: "medium",
          ref_count: 0,
          comment_count: 0,
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
    pendingUserInput,
    sendToolResponse,
    agentName,
    modelName: session.model || "",
    availableAgents,
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
