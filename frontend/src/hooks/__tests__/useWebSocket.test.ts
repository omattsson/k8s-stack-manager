import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';

// --- Mock reconnecting-websocket before importing the module under test ---

let mockWsInstance: {
  onmessage: ((event: MessageEvent) => void) | null;
  readyState: number;
  send: ReturnType<typeof vi.fn>;
  close: ReturnType<typeof vi.fn>;
};

let MockRWS: ReturnType<typeof vi.fn>;

function createMockRWSClass() {
  MockRWS = vi.fn(function (this: typeof mockWsInstance) {
    this.onmessage = null;
    this.readyState = WebSocket.OPEN;
    this.send = vi.fn();
    this.close = vi.fn();
    mockWsInstance = this;
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

    useWebSocketModule = await import('../useWebSocket');
  });

  afterEach(() => {
    vi.clearAllMocks();
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
    const handler = vi.fn();

    const { result } = renderHook(() => useWebSocketModule.useWebSocket(handler));

    // Set readyState to CLOSED
    mockWsInstance.readyState = WebSocket.CLOSED;

    act(() => {
      result.current.send('ping', {});
    });

    expect(mockWsInstance.send).not.toHaveBeenCalled();
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
