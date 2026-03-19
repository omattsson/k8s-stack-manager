import { useEffect, useRef, useCallback } from 'react';
import ReconnectingWebSocket from 'reconnecting-websocket';
import { WS_BASE_URL } from '../api/config';

export interface WsMessage {
  type: string;
  payload: Record<string, unknown>;
}

type MessageHandler = (msg: WsMessage) => void;

/**
 * Hook that maintains a shared WebSocket connection and dispatches
 * incoming messages to the provided handler. The connection auto-reconnects
 * on failure via reconnecting-websocket.
 */
export function useWebSocket(onMessage: MessageHandler) {
  const handlerRef = useRef(onMessage);
  handlerRef.current = onMessage;

  const wsRef = useRef<ReconnectingWebSocket | null>(null);

  useEffect(() => {
    const ws = new ReconnectingWebSocket(`${WS_BASE_URL}/ws`);
    wsRef.current = ws;

    ws.onmessage = (event: MessageEvent) => {
      try {
        const msg = JSON.parse(event.data) as WsMessage;
        handlerRef.current(msg);
      } catch {
        // ignore unparseable messages
      }
    };

    return () => {
      ws.close();
      wsRef.current = null;
    };
  }, []);

  const send = useCallback((type: string, payload: Record<string, unknown>) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type, payload }));
    }
  }, []);

  return { send };
}
