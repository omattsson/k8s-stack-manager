import { useEffect, useRef, useCallback } from 'react';
import ReconnectingWebSocket from 'reconnecting-websocket';
import { WS_BASE_URL } from '../api/config';

export interface WsMessage {
  type: string;
  payload: Record<string, unknown>;
}

type MessageHandler = (msg: WsMessage) => void;

// Module-level singleton connection manager.
// The WebSocket is created on first subscription and closed when all
// subscribers have unsubscribed.
const listeners = new Set<MessageHandler>();
let sharedWs: ReconnectingWebSocket | null = null;

function getSharedWs(): ReconnectingWebSocket {
  if (!sharedWs) {
    let url = `${WS_BASE_URL}/ws`;
    const token = localStorage.getItem('token');
    if (token) {
      url += `?token=${encodeURIComponent(token)}`;
    }
    const ws = new ReconnectingWebSocket(url);
    ws.onmessage = (event: MessageEvent) => {
      try {
        const msg = JSON.parse(event.data) as WsMessage;
        listeners.forEach((handler) => handler(msg));
      } catch {
        // ignore unparseable messages
      }
    };
    sharedWs = ws;
  }
  return sharedWs;
}

function subscribe(handler: MessageHandler): () => void {
  listeners.add(handler);
  getSharedWs();
  return () => {
    listeners.delete(handler);
    if (listeners.size === 0 && sharedWs) {
      sharedWs.close();
      sharedWs = null;
    }
  };
}

/**
 * Reconnect the shared WebSocket with a fresh token.
 * Call this after login or token refresh so the connection uses
 * the latest JWT from localStorage.
 */
export function reconnectWebSocket(): void {
  if (sharedWs) {
    sharedWs.close();
    sharedWs = null;
  }
  if (listeners.size > 0) {
    getSharedWs();
  }
}

/**
 * Hook that maintains a shared singleton WebSocket connection and dispatches
 * incoming messages to the provided handler. The connection auto-reconnects
 * on failure via reconnecting-websocket. All hook invocations share the
 * same underlying connection.
 */
export function useWebSocket(onMessage: MessageHandler) {
  const handlerRef = useRef(onMessage);
  handlerRef.current = onMessage;

  useEffect(() => {
    const dispatch: MessageHandler = (msg) => handlerRef.current(msg);
    const unsubscribe = subscribe(dispatch);
    return unsubscribe;
  }, []);

  const send = useCallback((type: string, payload: Record<string, unknown>) => {
    if (sharedWs?.readyState === WebSocket.OPEN) {
      sharedWs.send(JSON.stringify({ type, payload }));
    }
  }, []);

  return { send };
}
