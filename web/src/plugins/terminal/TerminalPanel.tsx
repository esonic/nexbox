import { useEffect, useRef, useCallback } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";

interface TerminalPanelProps {
  sessionId: string;
  visible?: boolean;
  onExited?: () => void;
}

export function TerminalPanel({ sessionId, visible = true, onExited }: TerminalPanelProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const sessionIdRef = useRef(sessionId);
  const connectedRef = useRef(false);
  const unmountingRef = useRef(false);
  const onExitedRef = useRef(onExited);

  sessionIdRef.current = sessionId;
  onExitedRef.current = onExited;

  const connect = useCallback(() => {
    if (!containerRef.current || connectedRef.current) return;
    connectedRef.current = true;

    const isDark = document.body.classList.contains("dark");

    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: "'JetBrains Mono', 'Fira Code', 'SF Mono', Menlo, Monaco, 'Courier New', monospace",
      lineHeight: 1.4,
      scrollback: 5000,
      theme: isDark
        ? { background: "#0d0d0d", foreground: "#e5e5e5", cursor: "#e5e5e5", selectionBackground: "#ffffff30" }
        : { background: "#ffffff", foreground: "#1a1a1a", cursor: "#1a1a1a", selectionBackground: "#00000018" },
    });

    const fitAddon = new FitAddon();
    term.loadAddon(fitAddon);
    term.loadAddon(new WebLinksAddon());

    termRef.current = term;
    fitAddonRef.current = fitAddon;

    term.open(containerRef.current);

    requestAnimationFrame(() => {
      try { fitAddon.fit(); } catch { /* ignore */ }
      term.focus();
    });

    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const wsUrl = `${protocol}//${window.location.host}/ws/terminal/sessions/${sessionIdRef.current}`;
    const ws = new WebSocket(wsUrl);
    wsRef.current = ws;

    ws.binaryType = "arraybuffer";

    ws.onopen = () => {
      const { cols, rows } = term;
      ws.send(JSON.stringify({ type: "resize", cols, rows }));
    };

    ws.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer) {
        term.write(new Uint8Array(ev.data));
      } else {
        term.write(ev.data);
      }
    };

    ws.onclose = () => {
      if (!unmountingRef.current) {
        onExitedRef.current?.();
      }
    };

    ws.onerror = () => {
      term.write("\r\n\x1b[31m[Connection error]\x1b[0m\r\n");
    };

    term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(data);
      }
    });

    term.onResize(({ cols, rows }) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "resize", cols, rows }));
      }
    });
  }, []);

  // Lazy connect: only on first visible
  useEffect(() => {
    if (visible && !connectedRef.current) {
      connect();
    }
  }, [visible, connect]);

  // Re-fit and focus when becoming visible
  useEffect(() => {
    if (!visible || !termRef.current) return;
    requestAnimationFrame(() => {
      try { fitAddonRef.current?.fit(); } catch { /* ignore */ }
      termRef.current?.focus();
    });
  }, [visible]);

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      unmountingRef.current = true;
      if (wsRef.current) {
        wsRef.current.close();
        wsRef.current = null;
      }
      if (termRef.current) {
        termRef.current.dispose();
        termRef.current = null;
      }
      connectedRef.current = false;
    };
  }, []);

  // Observe container resize to re-fit terminal
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => {
      try { fitAddonRef.current?.fit(); } catch { /* ignore */ }
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Watch for theme changes
  useEffect(() => {
    const observer = new MutationObserver(() => {
      const term = termRef.current;
      if (!term) return;
      const isDark = document.body.classList.contains("dark");
      term.options.theme = isDark
        ? {
            background: "#0d0d0d",
            foreground: "#e5e5e5",
            cursor: "#e5e5e5",
            selectionBackground: "#ffffff30",
          }
        : {
            background: "#ffffff",
            foreground: "#1a1a1a",
            cursor: "#1a1a1a",
            selectionBackground: "#00000018",
          };
      term.refresh(0, term.rows - 1);
    });
    observer.observe(document.body, {
      attributes: true,
      attributeFilter: ["class"],
    });
    return () => observer.disconnect();
  }, []);

  return (
    <div
      ref={containerRef}
      className="w-full h-full"
      style={{ padding: "4px 0 0 4px" }}
    />
  );
}
