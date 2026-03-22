import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook } from '@testing-library/react';

// Mock react-router-dom's useBlocker
const mockProceed = vi.fn();
const mockReset = vi.fn();
let blockerState = 'idle';

vi.mock('react-router-dom', () => ({
  useBlocker: () => ({
    get state() { return blockerState; },
    proceed: mockProceed,
    reset: mockReset,
  }),
}));

import { useUnsavedChanges } from '../useUnsavedChanges';

describe('useUnsavedChanges', () => {
  let confirmSpy: ReturnType<typeof vi.spyOn>;
  let addEventSpy: ReturnType<typeof vi.spyOn>;
  let removeEventSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    blockerState = 'idle';
    confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(false);
    addEventSpy = vi.spyOn(window, 'addEventListener');
    removeEventSpy = vi.spyOn(window, 'removeEventListener');
    mockProceed.mockClear();
    mockReset.mockClear();
  });

  afterEach(() => {
    confirmSpy.mockRestore();
    addEventSpy.mockRestore();
    removeEventSpy.mockRestore();
  });

  it('registers beforeunload listener when isDirty is true', () => {
    renderHook(() => useUnsavedChanges(true));
    expect(addEventSpy).toHaveBeenCalledWith('beforeunload', expect.any(Function));
  });

  it('does not register beforeunload listener when isDirty is false', () => {
    addEventSpy.mockClear();
    renderHook(() => useUnsavedChanges(false));
    const beforeunloadCalls = addEventSpy.mock.calls.filter(([event]: [string]) => event === 'beforeunload');
    expect(beforeunloadCalls).toHaveLength(0);
  });

  it('removes beforeunload listener on cleanup', () => {
    const { unmount } = renderHook(() => useUnsavedChanges(true));
    unmount();
    expect(removeEventSpy).toHaveBeenCalledWith('beforeunload', expect.any(Function));
  });

  it('removes beforeunload listener when isDirty changes to false', () => {
    const { rerender } = renderHook(
      ({ isDirty }) => useUnsavedChanges(isDirty),
      { initialProps: { isDirty: true } },
    );
    removeEventSpy.mockClear();
    rerender({ isDirty: false });
    expect(removeEventSpy).toHaveBeenCalledWith('beforeunload', expect.any(Function));
  });

  it('shows confirm and calls proceed when user confirms', () => {
    confirmSpy.mockReturnValue(true);
    blockerState = 'blocked';

    renderHook(() => useUnsavedChanges(true));

    expect(confirmSpy).toHaveBeenCalledWith('You have unsaved changes. Leave anyway?');
    expect(mockProceed).toHaveBeenCalled();
    expect(mockReset).not.toHaveBeenCalled();
  });

  it('shows confirm and calls reset when user cancels', () => {
    confirmSpy.mockReturnValue(false);
    blockerState = 'blocked';

    renderHook(() => useUnsavedChanges(true));

    expect(confirmSpy).toHaveBeenCalledWith('You have unsaved changes. Leave anyway?');
    expect(mockReset).toHaveBeenCalled();
    expect(mockProceed).not.toHaveBeenCalled();
  });

  it('does not show confirm when blocker state is idle', () => {
    blockerState = 'idle';
    renderHook(() => useUnsavedChanges(true));
    expect(confirmSpy).not.toHaveBeenCalled();
  });

  it('beforeunload handler calls preventDefault', () => {
    renderHook(() => useUnsavedChanges(true));

    const handler = addEventSpy.mock.calls.find(([event]: [string]) => event === 'beforeunload')?.[1] as EventListener;
    expect(handler).toBeDefined();

    const event = new Event('beforeunload') as BeforeUnloadEvent;
    const preventDefaultSpy = vi.spyOn(event, 'preventDefault');
    handler(event);

    expect(preventDefaultSpy).toHaveBeenCalled();
  });
});
