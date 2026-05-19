import { useRef, useEffect, useState, useCallback, memo, useMemo } from "react";
import { Circle, Clock, Wrench, MessageSquare, Loader2, ArrowDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ChatMessage, CopyButton, ForkButton, extractPlainText } from "@/components/chat/ChatMessage";
import { ErrorBoundary } from "@/components/ErrorBoundary";
import { ChatInput } from "@/components/chat/ChatInput";
import { useChatSession } from "@/hooks/useChatSession";
import { diffApi, sessionsApi } from "@/lib/api";
import { useAppStore } from "@/stores/app";
import { formatDuration, formatMessageTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import type { Session, Project, ChatMessage as ChatMessageType } from "@/types";

const MessageFooter = memo(function MessageFooter({ message }: { message: ChatMessageType }) {
  const plainText = useMemo(() => extractPlainText(message.parts), [message.parts]);

  if (message.isStreaming || message.role === "system") return null;

  const isUser = message.role === "user";
  const { metadata, parts } = message;
  const showStats = !isUser && metadata?.durationMs != null && metadata.durationMs > 0;
  const toolCallCount = showStats ? (metadata.toolCalls ?? parts.filter((p) => p.type === "tool_use").length) : 0;
  const totalTokens = showStats ? (metadata.inputTokens || 0) + (metadata.outputTokens || 0) : 0;
  const timestamp = formatMessageTime(message.created_at);

  if (!showStats && !plainText && !timestamp) return null;

  return (
    <div
      className={cn(
        "flex items-center gap-3 px-3 py-1 mt-1 text-[11px] text-muted-foreground/60",
        isUser ? "justify-end" : "ml-12",
      )}
    >
      {showStats && (
        <>
          <span className="inline-flex items-center gap-1">
            <Circle className="h-2 w-2 fill-green-500/50 text-green-500/50" />
            Completed
          </span>
          <span className="inline-flex items-center gap-1">
            <Clock className="h-3 w-3" />
            {formatDuration(metadata.durationMs!)}
          </span>
          {toolCallCount > 0 && (
            <span className="inline-flex items-center gap-1">
              <Wrench className="h-3 w-3" />
              {toolCallCount} tool calls
            </span>
          )}
          {totalTokens > 0 && (
            <span className="inline-flex items-center gap-1">
              <MessageSquare className="h-3 w-3" />
              {totalTokens.toLocaleString()} tokens
            </span>
          )}
          {metadata.totalCostUsd ? (
            <span className="inline-flex items-center gap-1">
              $
              {metadata.totalCostUsd < 0.01
                ? metadata.totalCostUsd.toFixed(4)
                : metadata.totalCostUsd.toFixed(2)}
            </span>
          ) : null}
        </>
      )}
      <div className="ml-auto flex items-center gap-1.5 opacity-0 group-hover/msg:opacity-100 transition-opacity">
        {timestamp && <span className="tabular-nums">{timestamp}</span>}
        {plainText && <CopyButton text={plainText} />}
        <ForkButton message={message} />
      </div>
    </div>
  );
});

interface ChatPanelProps {
  session: Session;
  project: Project | null;
  onMessageSent?: () => void;
}

export function ChatPanel({ session, project, onMessageSent }: ChatPanelProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const [isAtBottom, setIsAtBottom] = useState(true);
  const shouldFollowRef = useRef(true);
  const prevScrollHeightRef = useRef(0);

  const {
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
    effort,
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
    removePickedTask,
    removePickedAsset,
    removePickedWorkflow,
    clearPickedComments,
    loadMoreMessages,
  } = useChatSession({ session });

  const scrollToBottom = useCallback((behavior: ScrollBehavior = "instant") => {
    messagesEndRef.current?.scrollIntoView({ behavior });
  }, []);

  useEffect(() => {
    shouldFollowRef.current = true;
    setIsAtBottom(true);
  }, [session.id]);

  const handleScroll = useCallback(() => {
    const el = containerRef.current;
    if (!el) return;
    const threshold = 80;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < threshold;
    setIsAtBottom(atBottom);
    shouldFollowRef.current = atBottom;

    // Load more messages when scrolling near the top
    if (el.scrollTop < 100 && hasMore && !loadingMore && !loadingHistory) {
      loadMoreMessages();
    }
  }, [hasMore, loadingMore, loadingHistory, loadMoreMessages]);

  // Preserve scroll position when loading more messages
  useEffect(() => {
    const el = containerRef.current;
    if (!el || !loadingMore) return;

    // Store the scroll height before new messages are loaded
    prevScrollHeightRef.current = el.scrollHeight;
  }, [loadingMore]);

  useEffect(() => {
    const el = containerRef.current;
    if (!el || loadingMore) return;

    // After messages are loaded, adjust scroll position
    if (prevScrollHeightRef.current > 0) {
      const newScrollHeight = el.scrollHeight;
      const heightDiff = newScrollHeight - prevScrollHeightRef.current;
      if (heightDiff > 0) {
        el.scrollTop += heightDiff;
      }
      prevScrollHeightRef.current = 0;
    }
  }, [messages.length, loadingMore]);

  // Auto-scroll on new messages or after history loads
  useEffect(() => {
    if (loadingHistory) return;
    const el = containerRef.current;
    // If content doesn't overflow, we're always at bottom
    if (el && el.scrollHeight <= el.clientHeight) {
      setIsAtBottom(true);
      return;
    }
    if (shouldFollowRef.current) {
      requestAnimationFrame(() => scrollToBottom());
    }
  }, [messages, loadingHistory, scrollToBottom]);

  const hasProject = !!project;
  const hasGitRepo = !!project?.git_repo_path;
  const setGitDiffStats = useAppStore((s) => s.setGitDiffStats);
  const gitDiffStats = useAppStore((s) => s.gitDiffStats);
  const setSessions = useAppStore((s) => s.setSessions);
  const sessions = useAppStore((s) => s.sessions);

  const projectId = project?.id;
  const projectGitRepo = project?.git_repo_path;
  const worktreeSessionId = session.worktree_mode === "worktree" ? session.id : undefined;

  const fetchDiffStats = useCallback(async () => {
    if (!projectGitRepo || !projectId) return;
    try {
      const data = await diffApi.stats(projectId, worktreeSessionId);
      setGitDiffStats(data);
    } catch {
      // silently ignore polling errors
    }
  }, [projectId, projectGitRepo, worktreeSessionId, setGitDiffStats]);

  useEffect(() => {
    if (!hasGitRepo) { setGitDiffStats(null); return; }
    fetchDiffStats();
    const interval = setInterval(fetchDiffStats, 10_000);
    return () => clearInterval(interval);
  }, [hasGitRepo, fetchDiffStats, setGitDiffStats]);

  // Refresh diff stats when streaming stops (agent may have changed files)
  const prevStreaming = useRef(isStreaming);
  useEffect(() => {
    if (prevStreaming.current && !isStreaming) {
      if (hasGitRepo) {
        const timer = setTimeout(fetchDiffStats, 2000);
        return () => clearTimeout(timer);
      }
      // Refresh session to pick up worktree_path set during first message
      if (session.worktree_mode === "worktree" && !session.worktree_path) {
        sessionsApi.get(session.id).then((updated) => {
          setSessions(sessions.map((s) => (s.id === updated.id ? updated : s)));
        }).catch(() => {});
      }
    }
    prevStreaming.current = isStreaming;
  }, [isStreaming, hasGitRepo, fetchDiffStats, session, sessions, setSessions]);

  const handleOpenGit = useCallback(() => {
    window.dispatchEvent(new CustomEvent("open-git-panel"));
  }, []);

  const handleSendMessage = useCallback((msg: string, images?: Parameters<typeof sendMessage>[1], features?: Parameters<typeof sendMessage>[2]) => {
    sendMessage(msg, images, features);
    shouldFollowRef.current = true;
    onMessageSent?.();
  }, [sendMessage, onMessageSent]);

  return (
    <div className="flex flex-col flex-1 min-h-0 relative">
      {/* Messages area */}
      <div
        ref={containerRef}
        onScroll={handleScroll}
        className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden"
      >
        <div className="flex flex-col min-w-0 gap-6 flex-1 max-w-4xl mx-auto px-4 pt-4 pb-4">
          {loadingHistory ? (
            <div className="flex flex-col items-center justify-center py-20">
              <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
              <p className="text-sm text-muted-foreground mt-2">Loading chat history...</p>
            </div>
          ) : messages.length === 0 ? (
            <div className="flex flex-col items-center justify-center min-h-[60vh]">
              <div className="flex flex-col gap-2 items-center">
                <h2 className="text-2xl font-semibold tracking-tight">Hello there!</h2>
                <p className="text-muted-foreground text-lg">
                  {hasProject ? "How can I help you today?" : "Select a project to get started."}
                </p>
              </div>
            </div>
          ) : (
            <>
              {loadingMore && (
                <div className="flex justify-center py-2">
                  <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
                </div>
              )}
              <div data-messages="" className="flex flex-col gap-6">
                {messages.map((msg) => (
                  <ErrorBoundary key={msg.id}>
                    <div className="group/msg">
                      <ChatMessage message={msg} />
                      <MessageFooter message={msg} />
                    </div>
                  </ErrorBoundary>
                ))}
              </div>
            </>
          )}
          <div ref={messagesEndRef} />
        </div>

        {/* Scroll to bottom button */}
        {!isAtBottom && messages.length > 0 && (
          <div className="sticky bottom-2 flex justify-center pointer-events-none" style={{ marginTop: -40 }}>
            <Button
              variant="outline"
              size="icon-sm"
              onClick={() => { scrollToBottom("smooth"); shouldFollowRef.current = true; }}
              className="rounded-full shadow-md text-muted-foreground pointer-events-auto"
            >
              <ArrowDown />
            </Button>
          </div>
        )}
      </div>

      {/* Input area */}
      <ChatInput
        sessionId={session.id}
        isConnected={isConnected}
        isStreaming={isStreaming}
        agentName={agentName}
        modelName={modelName}
        effort={effort}
        availableAgents={availableAgents}
        availableModels={availableModels}
        availableModelInfos={availableModelInfos}
        sessionHasMessages={sessionHasMessages}
        pickedTasks={pickedTasks}
        pickedAssets={pickedAssets}
        pickedWorkflows={pickedWorkflows}
        pendingQuestion={pendingQuestion}
        pendingTodo={pendingTodo}
        pendingUserInput={pendingUserInput}
        onSendToolResponse={sendToolResponse}
        gitDiffStats={gitDiffStats}
        onSend={handleSendMessage}
        onStop={handleStop}
        onChangeAgent={handleChangeAgent}
        onChangeModel={handleChangeModel}
        onChangeEffort={handleChangeEffort}
        onRemovePickedTask={removePickedTask}
        onRemovePickedAsset={removePickedAsset}
        onRemovePickedWorkflow={removePickedWorkflow}
        onOpenGit={hasGitRepo ? handleOpenGit : undefined}
        queue={messageQueue}
        onQueueSendNow={sendQueuedNow}
        onQueueRemove={removeQueued}
        onQueueEdit={editQueued}
        richQueue={richQueue}
        onRichQueueSendNow={sendRichQueuedNow}
        onRichQueueRemove={removeRichQueued}
        onRichQueueEdit={editRichQueued}
        pickedComments={pickedComments}
        onClearPickedComments={clearPickedComments}
      />
    </div>
  );
}
