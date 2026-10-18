import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';

// --- Mock reconnecting-websocket before importing the module under test ---

let mockWsInstance: {
  onmessage: ((event: MessageEvent) => void) | null;
  readyState: number;
  send: ReturnType<typeof vi.fn>;
  close: ReturnType<typeof vi.fn>;
  addEventListener: ReturnType<typeof vi.fn>;
  emit: (type: string, event: unknown) => void;
};

let MockRWS: ReturnType<typeof vi.fn>;

function createMockRWSClass() {
  MockRWS = vi.fn(function (this: typeof mockWsInstance) {
    this.onmessage = null;
    this.readyState = WebSocket.OPEN;
    this.send = vi.fn();
    this.close = vi.fn();
    const handlers: Record<string, Array<(event: unknown) => void>> = {};
    this.addEventListener = vi.fn((type: string, fn: (event: unknown) => void) => {
      (handlers[type] ??= []).push(fn);
    });
    this.emit = (type: string, event: unknown) => {
      (handlers[type] ?? []).forEach((fn) => fn(event));
    };
    mockWsInstance = this; // eslint-disable-line @typescript-eslint/no-this-alias
  });
  return MockRWS;
}

vi.mock('reconnecting-websocket', () => ({
  default: createMockRWSClass(),
}));

vi.mock('../../api/config', () => ({
  WS_BASE_URL: 'ws://localhost:8081',
}));

// Reset the module-level singleton between tests by re-importing
let useWebSocketModule: typeof import('../useWebSocket');
let refreshAccessToken: ReturnType<typeof vi.fn>;

/** Build an unsigned JWT whose payload has the given exp (seconds from now). */
function jwtExpiringIn(seconds: number, tag = 'a'): string {
  const payload = btoa(JSON.stringify({ exp: Math.floor(Date.now() / 1000) + seconds, tag }))
    .replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '');
  return `header.${payload}.sig`;
}

/** Return the URL provider that the hook passed to reconnecting-websocket. */
function urlProvider(): () => Promise<string> {
  expect(MockRWS).toHaveBeenCalledTimes(1);
  const provider = MockRWS.mock.calls[0][0];
  expect(typeof provider).toBe('function');
  return provider as () => Promise<string>;
}

describe('useWebSocket', () => {
  beforeEach(async () => {
    vi.resetModules();

    // Re-mock after resetModules using a fresh class mock
    vi.doMock('reconnecting-websocket', () => ({
      default: createMockRWSClass(),
    }));

    vi.doMock('../../api/config', () => ({
      WS_BASE_URL: 'ws://localhost:8081',
    }));

    refreshAccessToken = vi.fn();
    vi.doMock('../../api/refresh', () => ({
      TOKEN_STORAGE_KEY: 'token',
      refreshAccessToken,
      isAuthRejection: (error: unknown) =>
        (error as { response?: { status?: number } })?.response?.status === 401,
    }));

    useWebSocketModule = await import('../useWebSocket');

    // Most tests need a token to create a WebSocket connection
    localStorage.setItem('token', 'fake-jwt-token');
  });

  afterEach(() => {
    vi.clearAllMocks();
    localStorage.removeItem('token');
  });

  it('subscribes to messages and calls handler on incoming message', () => {
    const handler = vi.fn();

    renderHook(() => useWebSocketModule.useWebSocket(handler));

    // Simulate an incoming message via the WebSocket onmessage callback
    const message = { type: 'instance_update', payload: { id: '1' } };
    act(() => {
      mockWsInstance.onmessage?.(new MessageEvent('message', {
        data: JSON.stringify(message),
      }));
    });

    expect(handler).toHaveBeenCalledWith(message);
  });

  it('unsubscribes on unmount', () => {
    const handler = vi.fn();

    const { unmount } = renderHook(() => useWebSocketModule.useWebSocket(handler));

    unmount();

    // After unmount, sending a message should not call the handler
    act(() => {
      mockWsInstance.onmessage?.(new MessageEvent('message', {
        data: JSON.stringify({ type: 'test', payload: {} }),
      }));
    });

    expect(handler).not.toHaveBeenCalled();
  });

  it('closes WebSocket when last subscriber unmounts', () => {
    const handler = vi.fn();

    const { unmount } = renderHook(() => useWebSocketModule.useWebSocket(handler));

    unmount();

    expect(mockWsInstance.close).toHaveBeenCalled();
  });

  it('provides a send function that sends JSON via WebSocket', () => {
    const handler = vi.fn();

    const { result } = renderHook(() => useWebSocketModule.useWebSocket(handler));

    act(() => {
      result.current.send('ping', { timestamp: 123 });
    });

    expect(mockWsInstance.send).toHaveBeenCalledWith(
      JSON.stringify({ type: 'ping', payload: { timestamp: 123 } })
    );
  });

  it('does not send when WebSocket is not open', () => {
    const { result } = renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
    const ws = mockWsInstance;
    ws.readyState = WebSocket.CLOSED;

    act(() => {
      result.current.send('ping', {});
    });
    // A later open does not send it either: send does not queue.
    ws.readyState = WebSocket.OPEN;
    act(() => ws.emit('open', {}));

    expect(ws.send).not.toHaveBeenCalled();
  });

  describe('instance subscriptions', () => {
    const subscribeMsg = (id: string) => JSON.stringify({ type: 'subscribe', payload: { instance_id: id } });
    const unsubscribeMsg = (id: string) => JSON.stringify({ type: 'unsubscribe', payload: { instance_id: id } });

    it('sends a subscription made before the socket is open when it opens', () => {
      const { result } = renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
      const ws = mockWsInstance;
      ws.readyState = WebSocket.CONNECTING;

      act(() => {
        result.current.subscribeInstance('inst-1');
      });
      expect(ws.send).not.toHaveBeenCalled();

      ws.readyState = WebSocket.OPEN;
      act(() => ws.emit('open', {}));

      expect(ws.send).toHaveBeenCalledWith(subscribeMsg('inst-1'));
    });

    it('subscribes again after a reconnect', () => {
      const { result } = renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
      const ws = mockWsInstance;

      act(() => {
        result.current.subscribeInstance('inst-1');
      });
      expect(ws.send).toHaveBeenCalledTimes(1);
      expect(ws.send).toHaveBeenLastCalledWith(subscribeMsg('inst-1'));

      // The connection drops and reconnecting-websocket opens a new one.
      ws.readyState = WebSocket.CLOSED;
      act(() => ws.emit('close', { code: 1006 }));
      ws.readyState = WebSocket.OPEN;
      act(() => ws.emit('open', {}));

      expect(ws.send).toHaveBeenCalledTimes(2);
      expect(ws.send).toHaveBeenLastCalledWith(subscribeMsg('inst-1'));
    });

    it('subscribes on the new socket after reconnectWebSocket', () => {
      const { result } = renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
      act(() => {
        result.current.subscribeInstance('inst-1');
      });
      const oldWs = mockWsInstance;

      act(() => useWebSocketModule.reconnectWebSocket());
      const newWs = mockWsInstance;
      expect(newWs).not.toBe(oldWs);

      act(() => newWs.emit('open', {}));
      expect(newWs.send).toHaveBeenCalledWith(subscribeMsg('inst-1'));
      // The closed socket does not send.
      act(() => oldWs.emit('open', {}));
      expect(oldWs.send).toHaveBeenCalledTimes(1);
    });

    it('unsubscribes and stops resubscribing after the cleanup', () => {
      const { result } = renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
      const ws = mockWsInstance;

      let cleanup: () => void = () => {};
      act(() => {
        cleanup = result.current.subscribeInstance('inst-1');
      });
      act(() => cleanup());
      expect(ws.send).toHaveBeenLastCalledWith(unsubscribeMsg('inst-1'));

      ws.send.mockClear();
      act(() => ws.emit('open', {}));
      expect(ws.send).not.toHaveBeenCalled();
    });

    it('keeps the subscription until the last subscriber leaves', () => {
      const { result } = renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
      const ws = mockWsInstance;

      let first: () => void = () => {};
      let second: () => void = () => {};
      act(() => {
        first = result.current.subscribeInstance('inst-1');
        second = result.current.subscribeInstance('inst-1');
      });
      // One subscribe message for two subscribers.
      expect(ws.send).toHaveBeenCalledTimes(1);

      act(() => first());
      act(() => first()); // a second call of the same cleanup does nothing
      expect(ws.send).toHaveBeenCalledTimes(1);

      act(() => second());
      expect(ws.send).toHaveBeenCalledTimes(2);
      expect(ws.send).toHaveBeenLastCalledWith(unsubscribeMsg('inst-1'));
    });

    it('does not send an unsubscribe when the socket is not open', () => {
      const { result } = renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
      const ws = mockWsInstance;

      let cleanup: () => void = () => {};
      act(() => {
        cleanup = result.current.subscribeInstance('inst-1');
      });
      ws.send.mockClear();
      ws.readyState = WebSocket.CLOSED;
      act(() => cleanup());

      expect(ws.send).not.toHaveBeenCalled();
    });
  });

  it('ignores unparseable messages', () => {
    const handler = vi.fn();

    renderHook(() => useWebSocketModule.useWebSocket(handler));

    act(() => {
      mockWsInstance.onmessage?.(new MessageEvent('message', {
        data: 'not-valid-json{{{',
      }));
    });

    expect(handler).not.toHaveBeenCalled();
  });

  it('shares WebSocket connection between multiple subscribers', () => {
    const handler1 = vi.fn();
    const handler2 = vi.fn();

    const { unmount: unmount1 } = renderHook(() => useWebSocketModule.useWebSocket(handler1));
    renderHook(() => useWebSocketModule.useWebSocket(handler2));

    // Both should receive the message
    const message = { type: 'broadcast', payload: { data: 'hello' } };
    act(() => {
      mockWsInstance.onmessage?.(new MessageEvent('message', {
        data: JSON.stringify(message),
      }));
    });

    expect(handler1).toHaveBeenCalledWith(message);
    expect(handler2).toHaveBeenCalledWith(message);

    // Only one WebSocket should be created
    expect(MockRWS).toHaveBeenCalledTimes(1);

    // Unmounting first subscriber should NOT close the connection
    unmount1();
    expect(mockWsInstance.close).not.toHaveBeenCalled();
  });

  it('does not create WebSocket when no token exists', () => {
    localStorage.removeItem('token');
    const handler = vi.fn();

    renderHook(() => useWebSocketModule.useWebSocket(handler));

    expect(MockRWS).not.toHaveBeenCalled();
  });

  it('reads the current token on each connect', async () => {
    const valid = jwtExpiringIn(600);
    localStorage.setItem('token', valid);
    renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
    const provider = urlProvider();

    await expect(provider()).resolves.toBe(`ws://localhost:8081/ws?token=${valid}`);

    // A token refresh by an HTTP request stores a new token. The next reconnect uses it.
    const refreshed = jwtExpiringIn(900, 'b');
    localStorage.setItem('token', refreshed);
    await expect(provider()).resolves.toBe(`ws://localhost:8081/ws?token=${refreshed}`);
    expect(refreshAccessToken).not.toHaveBeenCalled();
  });

  it('refreshes an expired token once before it connects', async () => {
    const expired = jwtExpiringIn(-60);
    const fresh = jwtExpiringIn(900, 'fresh');
    localStorage.setItem('token', expired);
    refreshAccessToken.mockResolvedValue(fresh);
    renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));

    await expect(urlProvider()()).resolves.toBe(`ws://localhost:8081/ws?token=${fresh}`);
    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
    expect(refreshAccessToken).toHaveBeenCalledWith(expired);
  });

  it('refreshes a token that expires within 30 seconds', async () => {
    localStorage.setItem('token', jwtExpiringIn(10));
    const fresh = jwtExpiringIn(900, 'fresh');
    refreshAccessToken.mockResolvedValue(fresh);
    renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));

    await expect(urlProvider()()).resolves.toBe(`ws://localhost:8081/ws?token=${fresh}`);
    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
  });

  it('stops reconnecting when the refresh is rejected', async () => {
    const expired = jwtExpiringIn(-60);
    localStorage.setItem('token', expired);
    refreshAccessToken.mockRejectedValue({ response: { status: 401 } });
    const handler = vi.fn();
    renderHook(() => useWebSocketModule.useWebSocket(handler));
    const ws = mockWsInstance;

    // The provider resolves (a rejection would lock reconnecting-websocket)
    // and closes the socket, which ends the reconnects.
    await expect(urlProvider()()).resolves.toContain('/ws?token=');
    expect(ws.close).toHaveBeenCalledTimes(1);
    expect(refreshAccessToken).toHaveBeenCalledTimes(1);

    // The closed socket is no longer shared: a new subscriber without a token
    // does not open a new one.
    localStorage.removeItem('token');
    renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
    expect(MockRWS).toHaveBeenCalledTimes(1);
  });

  it('keeps reconnecting with backoff when the refresh has a network error', async () => {
    const expired = jwtExpiringIn(-60);
    localStorage.setItem('token', expired);
    refreshAccessToken.mockRejectedValue(new Error('Network Error'));
    renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
    const ws = mockWsInstance;

    await expect(urlProvider()()).resolves.toBe(`ws://localhost:8081/ws?token=${expired}`);
    expect(ws.close).not.toHaveBeenCalled();
  });

  it('stops reconnecting when no token is stored', async () => {
    localStorage.setItem('token', jwtExpiringIn(600));
    renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
    const ws = mockWsInstance;
    localStorage.removeItem('token');

    await expect(urlProvider()()).resolves.toContain('/ws?token=');
    expect(ws.close).toHaveBeenCalledTimes(1);
    expect(refreshAccessToken).not.toHaveBeenCalled();
  });

  it('refreshes a valid token once after a 1008 close', async () => {
    const valid = jwtExpiringIn(600);
    localStorage.setItem('token', valid);
    const fresh = jwtExpiringIn(900, 'fresh');
    refreshAccessToken.mockImplementation(async () => {
      localStorage.setItem('token', fresh);
      return fresh;
    });
    renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
    const provider = urlProvider();
    const ws = mockWsInstance;

    // Server closes the socket: session revoked or token expired.
    ws.emit('close', { code: 1008 });
    await expect(provider()).resolves.toBe(`ws://localhost:8081/ws?token=${fresh}`);
    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
    expect(refreshAccessToken).toHaveBeenCalledWith(valid);

    // The next connect fails (1006): the fresh token is used as is.
    ws.emit('close', { code: 1006 });
    await expect(provider()).resolves.toBe(`ws://localhost:8081/ws?token=${fresh}`);
    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
  });

  it('does not refresh again on repeated 1006 closes after a successful refresh', async () => {
    localStorage.setItem('token', jwtExpiringIn(600));
    const fresh = jwtExpiringIn(900, 'fresh');
    refreshAccessToken.mockImplementation(async () => {
      localStorage.setItem('token', fresh);
      return fresh;
    });
    renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
    const provider = urlProvider();
    const ws = mockWsInstance;

    ws.emit('close', { code: 1008 });
    await provider();
    for (let i = 0; i < 3; i++) {
      ws.emit('close', { code: 1006 });
      await expect(provider()).resolves.toBe(`ws://localhost:8081/ws?token=${fresh}`);
    }
    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
    expect(ws.close).not.toHaveBeenCalled();
  });

  it('retries the refresh after a 1008 close when the refresh had a network error', async () => {
    const valid = jwtExpiringIn(600);
    localStorage.setItem('token', valid);
    const fresh = jwtExpiringIn(900, 'fresh');
    refreshAccessToken.mockRejectedValueOnce(new Error('Network Error')).mockResolvedValueOnce(fresh);
    renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
    const provider = urlProvider();
    const ws = mockWsInstance;

    ws.emit('close', { code: 1008 });
    await expect(provider()).resolves.toBe(`ws://localhost:8081/ws?token=${valid}`);
    ws.emit('close', { code: 1006 });
    await expect(provider()).resolves.toBe(`ws://localhost:8081/ws?token=${fresh}`);
    expect(refreshAccessToken).toHaveBeenCalledTimes(2);
  });

  it('does not refresh a valid token after a 1006 close without a 1008', async () => {
    const valid = jwtExpiringIn(600);
    localStorage.setItem('token', valid);
    renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
    mockWsInstance.emit('close', { code: 1006 });

    await expect(urlProvider()()).resolves.toBe(`ws://localhost:8081/ws?token=${valid}`);
    expect(refreshAccessToken).not.toHaveBeenCalled();
  });

  it('stops reconnecting after a 1008 close when the refresh is rejected', async () => {
    localStorage.setItem('token', jwtExpiringIn(600));
    refreshAccessToken.mockRejectedValue({ response: { status: 401 } });
    renderHook(() => useWebSocketModule.useWebSocket(vi.fn()));
    const ws = mockWsInstance;
    ws.emit('close', { code: 1008 });

    await expect(urlProvider()()).resolves.toContain('/ws?token=');
    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
    expect(ws.close).toHaveBeenCalledTimes(1);
  });

  it('uses the latest handler reference', () => {
    const handler1 = vi.fn();
    const handler2 = vi.fn();

    const { rerender } = renderHook(
      ({ onMessage }) => useWebSocketModule.useWebSocket(onMessage),
      { initialProps: { onMessage: handler1 } }
    );

    // Re-render with a new handler
    rerender({ onMessage: handler2 });

    const message = { type: 'update', payload: {} };
    act(() => {
      mockWsInstance.onmessage?.(new MessageEvent('message', {
        data: JSON.stringify(message),
      }));
    });

    // Should call the latest handler, not the original
    expect(handler2).toHaveBeenCalledWith(message);
    expect(handler1).not.toHaveBeenCalled();
  });
});
