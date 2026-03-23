import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook } from '@testing-library/react';
import { useUnsavedChanges } from '../useUnsavedChanges';

describe('useUnsavedChanges', () => {
  let addEventSpy: ReturnType<typeof vi.spyOn>;
  let removeEventSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    addEventSpy = vi.spyOn(window, 'addEventListener');
    removeEventSpy = vi.spyOn(window, 'removeEventListener');
  });

  afterEach(() => {
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
