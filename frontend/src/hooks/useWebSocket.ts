import { useEffect, useRef, useCallback } from 'react';
import ReconnectingWebSocket from 'reconnecting-websocket';
import { WS_BASE_URL } from '../api/config';
import { isAuthRejection, refreshAccessToken, TOKEN_STORAGE_KEY } from '../api/refresh';
import { decodeJwtPayload } from '../utils/jwt';

export interface WsMessage {
  type: string;
  payload: Record<string, unknown>;
}

/** Payload of a `deployment.status` message. */
export interface DeploymentStatusPayload {
  instance_id?: string;
  status?: string;
  log_id?: string;
  /** Operation that runs. Older servers omit it. A rollback reports the status "deploying". */
  action?: 'deploy' | 'rollback' | 'stop' | 'clean';
  error_message?: string;
}

type MessageHandler = (msg: WsMessage) => void;

// Module-level singleton connection manager.
// The WebSocket is created on first subscription and closed when all
// subscribers have unsubscribed.
const listeners = new Set<MessageHandler>();
let sharedWs: ReconnectingWebSocket | null = null;

/** Refresh the access token when it expires within this time before a connect. */
const TOKEN_REFRESH_MARGIN_MS = 30_000;

function wsUrl(token: string): string {
  return `${WS_BASE_URL}/ws?token=${encodeURIComponent(token)}`;
}

/** True when the token does not decode or expires within TOKEN_REFRESH_MARGIN_MS. */
function expiresSoon(token: string): boolean {
  const payload = decodeJwtPayload(token);
  if (!payload || typeof payload.exp !== 'number') return true;
  return payload.exp * 1000 - Date.now() <= TOKEN_REFRESH_MARGIN_MS;
}

/** Close code that the server sends when it closes a socket on purpose (revoked session, expired token). */
const CLOSE_POLICY_VIOLATION = 1008;

/** Per-socket reconnect state. */
interface ReconnectState {
  /**
   * Set when the server closed the socket with 1008. The next connect then
   * refreshes the token even if it does not look expired (for example after a
   * logout of this session on another device). A successful refresh or a
   * successful open clears it. A 1006 close (failed upgrade) does not set it,
   * so a refresh failure that kept the flag set retries the refresh.
   */
  forceRefresh: boolean;
}

/** Stop the socket for good: no more reconnects. */
function stopSharedWs(ws: ReconnectingWebSocket): void {
  ws.close();
  if (sharedWs === ws) {
    sharedWs = null;
  }
}

/**
 * Return the URL for the next connect of `ws`. reconnecting-websocket calls it
 * before each connect, so a reconnect uses the current token. The server
 * closes the socket when the access token expires, so a tab that makes no HTTP
 * request (for example one that only shows a deploy log) needs a fresh token
 * here:
 * - The stored token is valid for more than TOKEN_REFRESH_MARGIN_MS and the
 *   server did not close the socket with 1008 (see ReconnectState): use it.
 * - Else refresh it through refreshAccessToken (the shared single-flight,
 *   cross-tab refresh; no second refresh path).
 * - The refresh is rejected (401/403: session idle, max lifetime, revoked) or
 *   no token is stored (logout): stop the socket, so it does not reconnect in
 *   a loop. The next HTTP request then sends the user to the login page.
 * - Other refresh errors (network, timeout, 5xx): use the stored token. The
 *   connect then fails and reconnecting-websocket retries with backoff.
 *
 * The refresh does not count as activity for the idle limit: the backend gives
 * the rotated refresh token the LastActivity of the old token (createRefreshToken
 * in backend/internal/api/handlers/auth.go), and only authenticated requests move
 * it forward. So a tab that only holds a socket still idles out after
 * SESSION_IDLE_TIMEOUT; the refresh then gets 401 and the socket stops.
 *
 * Note: the provider never rejects. A rejected provider promise leaves
 * reconnecting-websocket locked without reconnects and logs an unhandled
 * rejection.
 */
async function nextUrl(
  ws: () => ReconnectingWebSocket | null,
  fallbackToken: string,
  state: ReconnectState,
): Promise<string> {
  const stored = localStorage.getItem(TOKEN_STORAGE_KEY);
  if (!stored) {
    const current = ws();
    if (current) stopSharedWs(current);
    return wsUrl(fallbackToken);
  }
  if (!state.forceRefresh && !expiresSoon(stored)) {
    return wsUrl(stored);
  }
  try {
    const fresh = await refreshAccessToken(stored);
    // A fresh token cannot be the reason for a later failed connect (1006),
    // so do not refresh again until the server sends the next 1008.
    state.forceRefresh = false;
    return wsUrl(fresh);
  } catch (error) {
    if (isAuthRejection(error)) {
      const current = ws();
      if (current) stopSharedWs(current);
    }
    return wsUrl(stored);
  }
}

function getSharedWs(): ReconnectingWebSocket | null {
  if (!sharedWs) {
    const initialToken = localStorage.getItem(TOKEN_STORAGE_KEY);
    if (!initialToken) {
      return null;
    }
    // The provider runs asynchronously after the constructor returns, so
    // `ws` is set when it reads it.
    let ws: ReconnectingWebSocket | null = null;
    const state: ReconnectState = { forceRefresh: false };
    ws = new ReconnectingWebSocket(() => nextUrl(() => ws, initialToken, state));
    ws.addEventListener('open', () => {
      state.forceRefresh = false;
    });
    ws.addEventListener('close', (event) => {
      if (event.code === CLOSE_POLICY_VIOLATION) {
        state.forceRefresh = true;
      }
    });
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
 * If no token exists (e.g. after logout), the existing connection
 * is closed without creating a new one.
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
