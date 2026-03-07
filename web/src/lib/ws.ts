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

  private handlers = new Map<string, Set<EventHandler>>(); // sessionId -> handler set
  private globalHandlers = new Set<GlobalEventHandler>(); // non-session events (connect module)

  private connectionHandlers = new Set<ConnectionHandler>();
  private _connected = false;

  get connected(): boolean {
    return this._connected;
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
      this.notifyConnection(true);
    };

    ws.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data) as TaggedEvent;
        this.processEvent(data);
      } catch (e) {
        console.error("[ws] Parse error:", e);
      }
    };

    ws.onclose = () => {
      if (import.meta.env.DEV) console.log("[ws] Disconnected");
      this._connected = false;
      this.ws = null;
      this.notifyConnection(false);
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
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
    this._connected = false;
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

  /** Send stop signal for a session. */
  stop(sessionId: string): void {
    this.rawSend({ type: "stop", session_id: sessionId });
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
    handler(this._connected);
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
    if (set) {
      for (const handler of set) {
        handler(tagged as ChatEvent);
      }
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

  private notifyConnection(connected: boolean): void {
    for (const handler of this.connectionHandlers) {
      handler(connected);
    }
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
