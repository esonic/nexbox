import type { ChatMessage } from "@/types";

export function isVisibleChatMessage(message: ChatMessage): boolean {
  return message.role === "user" ||
    ((message.role === "assistant" || message.role === "system") && (message.parts?.length ?? 0) > 0);
}

export function isSystemNotice(message: ChatMessage): boolean {
  return message.role === "system";
}

export function insertSystemNotice(messages: ChatMessage[], notice: ChatMessage): ChatMessage[] {
  if (messages.some((message) => message.id === notice.id)) return messages;
  const last = messages[messages.length - 1];
  // Streaming updates target the last assistant message. Keep that message at
  // the end so an asynchronous notice cannot split or interrupt the live turn.
  return last?.role === "assistant" && last.isStreaming
    ? [...messages.slice(0, -1), notice, last]
    : [...messages, notice];
}
