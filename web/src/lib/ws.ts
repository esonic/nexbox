import type { ChatEvent, MessagePart } from "../types";

const WS_BASE = `${window.location.protocol === "https:" ? "wss:" : "ws:"}//${window.location.host}`;

/**
 * TaggedEvent is the wire format from the backend.
 * Extends ChatEvent with session_id for routing.
 */
interface TaggedEvent extends ChatEvent {
  session_id: string;
}

type EventHandler = (event: ChatEvent) => void;
type GlobalEventHandler = (event: Record<string, unknown>) => void;
type ConnectionHandler = (connected: boolean) => void;

/**
 * GlobalWebSocket provides a single multiplexed WebSocket connection for all
 * sessions. Supports per-session subscribe/unsubscribe and automatic
 * reconnection with exponential backoff.
 *
 * Also supports global (non-session) events forwarded from the connect module
 * (e.g. tasks.updated, assets.updated).
 */
class GlobalWebSocket {
  private ws: WebSocket | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectDelay = 1000;
  private static readonly MAX_RECONNECT_DELAY = 30000;
  // Grace period before surfacing a disconnect to the UI. Brief drops on
  // weak networks reconnect within ~1–2s, so debouncing here prevents the
  // chat input from flapping between connected/disconnected states.
  private static readonly DISCONNECT_GRACE_MS = 4000;

  private handlers = new Map<string, Set<EventHandler>>(); // sessionId -> handler set
  private globalHandlers = new Set<GlobalEventHandler>(); // non-session events (connect module)
  private unroutedHandlers = new Set<(sessionId: string) => void>(); // session events nobody is subscribed to

  private connectionHandlers = new Set<ConnectionHandler>();
  private _connected = false;
  // _announcedConnected is the last value handed out to subscribers. We use
  // it to suppress flapping: while waiting out the disconnect grace window
  // we keep reporting "connected" so the UI stays interactive.
  private _announcedConnected = false;
  private pendingDisconnectTimer: ReturnType<typeof setTimeout> | null = null;

  get connected(): boolean {
    // External callers see the announced (debounced) state, matching what
    // their connection handler will eventually fire.
    return this._announcedConnected;
  }

  connect(): void {
    if (
      this.ws?.readyState === WebSocket.OPEN ||
      this.ws?.readyState === WebSocket.CONNECTING
    ) {
      return;
    }

    const wsUrl = `${WS_BASE}/ws`;
    const ws = new WebSocket(wsUrl);
    this.ws = ws;

    ws.onopen = () => {
      if (import.meta.env.DEV) console.log("[ws] Connected");
      this._connected = true;
      this.reconnectDelay = 1000;
      // Cancel any pending disconnect — we recovered inside the grace window.
      if (this.pendingDisconnectTimer) {
        clearTimeout(this.pendingDisconnectTimer);
        this.pendingDisconnectTimer = null;
      }
      this.announceConnection(true);
    };

    ws.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data) as TaggedEvent;
        this.processEvent(data);
      } catch (e) {
        console.error("[ws] Parse error:", e);
      }
    };

    ws.onclose = (event) => {
      if (import.meta.env.DEV) console.log("[ws] Disconnected");
      this._connected = false;
      this.ws = null;
      if (event.code === 1008 && event.reason === "Authentication mode changed") {
        window.location.reload();
        return;
      }
      this.scheduleDisconnectAnnouncement();
      this.scheduleReconnect();
    };

    ws.onerror = (err) => {
      console.error("[ws] Error:", err);
    };
  }

  disconnect(): void {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.pendingDisconnectTimer) {
      clearTimeout(this.pendingDisconnectTimer);
      this.pendingDisconnectTimer = null;
    }
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
    this._connected = false;
    this.announceConnection(false);
  }

  /**
   * Register a local handler for a session's events. Returns an unregister function.
   * No WS subscribe/unsubscribe is sent — the server broadcasts all session events
   * to every connected client; routing is done client-side by session_id.
   */
  subscribe(sessionId: string, handler: EventHandler): () => void {
    if (!this.handlers.has(sessionId)) {
      this.handlers.set(sessionId, new Set());
    }
    this.handlers.get(sessionId)!.add(handler);

    return () => {
      const set = this.handlers.get(sessionId);
      if (set) {
        set.delete(handler);
        if (set.size === 0) {
          this.handlers.delete(sessionId);
        }
      }
    };
  }

  /**
   * Register a callback for session events that had no subscriber. A client
   * only subscribes to the session it is viewing, so any turn running elsewhere
   * lands here instead.
   */
  subscribeUnrouted(handler: (sessionId: string) => void): () => void {
    this.unroutedHandlers.add(handler);
    return () => {
      this.unroutedHandlers.delete(handler);
    };
  }

  /** Send a user message for a session, optionally with structured parts, image filenames, and features. */
  send(sessionId: string, message: string, parts?: MessagePart[], images?: string[], features?: string[]): void {
    this.rawSend({
      type: "user_message",
      session_id: sessionId,
      content: message,
      ...(parts && parts.length > 0 ? { parts } : {}),
      ...(images && images.length > 0 ? { images } : {}),
      ...(features && features.length > 0 ? { features } : {}),
    });
  }

  /**
   * Inject a queue-jumping message into a session's running turn ("interjection").
   * Capable agents fold it into the current turn; unsupported/idle sessions
   * fall back to ordinary queue/send.
   */
  interject(sessionId: string, message: string, parts?: MessagePart[], images?: string[], features?: string[]): void {
    this.rawSend({
      type: "interject",
      session_id: sessionId,
      content: message,
      ...(parts && parts.length > 0 ? { parts } : {}),
      ...(images && images.length > 0 ? { images } : {}),
      ...(features && features.length > 0 ? { features } : {}),
    });
  }

  /** Send stop signal for a session. */
  stop(sessionId: string): void {
    this.rawSend({ type: "stop", session_id: sessionId });
  }

  /**
   * Send a tool response for an interactive tool call (e.g., AskUserQuestion).
   * Pass skip=true to decline the question: the server fails the tool (same as
   * the input timeout) instead of treating the text as an answer.
   */
  sendToolResponse(sessionId: string, toolUseId: string, response: string, skip = false): void {
    this.rawSend({
      type: "tool_response",
      session_id: sessionId,
      tool_use_id: toolUseId,
      tool_response: response,
      skip,
    });
  }

  /**
   * Subscribe to global (non-session) events, e.g. connect module events
   * like tasks.updated, assets.updated. Returns an unsubscribe function.
   */
  subscribeGlobal(handler: GlobalEventHandler): () => void {
    this.globalHandlers.add(handler);
    return () => {
      this.globalHandlers.delete(handler);
    };
  }

  onConnection(handler: ConnectionHandler): () => void {
    this.connectionHandlers.add(handler);
    handler(this._announcedConnected);
    return () => {
      this.connectionHandlers.delete(handler);
    };
  }

  private processEvent(tagged: TaggedEvent): void {
    const sid = tagged.session_id;

    // Non-session events (e.g. connect module: tasks.updated, assets.updated)
    if (!sid) {
      for (const handler of this.globalHandlers) {
        handler(tagged as unknown as Record<string, unknown>);
      }
      return;
    }

    const set = this.handlers.get(sid);
    if (set && set.size > 0) {
      for (const handler of set) {
        handler(tagged as ChatEvent);
      }
      return;
    }

    // The server broadcasts live and never replays. Notify cache owners when a
    // session moved on while nobody was listening to it.
    for (const handler of this.unroutedHandlers) {
      handler(sid);
    }
  }

  private scheduleReconnect(): void {
    if (this.reconnectTimer) return;
    if (import.meta.env.DEV) console.log(`[ws] Reconnecting in ${this.reconnectDelay}ms...`);
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, this.reconnectDelay);
    this.reconnectDelay = Math.min(
      this.reconnectDelay * 2,
      GlobalWebSocket.MAX_RECONNECT_DELAY
    );
  }

  private announceConnection(connected: boolean): void {
    if (this._announcedConnected === connected) return;
    this._announcedConnected = connected;
    for (const handler of this.connectionHandlers) {
      handler(connected);
    }
  }

  // scheduleDisconnectAnnouncement debounces transient drops. We only tell
  // subscribers we're disconnected if the socket fails to come back within
  // DISCONNECT_GRACE_MS — long enough to swallow the typical 1–2s blip but
  // short enough that a genuinely dead connection still updates the UI.
  private scheduleDisconnectAnnouncement(): void {
    if (!this._announcedConnected) return; // already showing disconnected
    if (this.pendingDisconnectTimer) return;
    this.pendingDisconnectTimer = setTimeout(() => {
      this.pendingDisconnectTimer = null;
      if (!this._connected) {
        this.announceConnection(false);
      }
    }, GlobalWebSocket.DISCONNECT_GRACE_MS);
  }

  private rawSend(data: unknown): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(data));
    }
  }
}

/** Singleton global WebSocket instance. */
export const globalWS = new GlobalWebSocket();

// Auto-connect on module load
globalWS.connect();
