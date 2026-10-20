import { describe, it, expect, vi, afterEach } from 'vitest';
import { act, renderHook, waitFor } from '@testing-library/react';
import { useDeployHistory, useRollbackTargets, DEPLOY_HISTORY_PAGE_SIZE } from '../useDeployHistory';
import type { DeploymentLog, DeployLogPage } from '../../types';

vi.mock('../../api/client', () => ({
  instanceService: {
    getDeployLogPage: vi.fn(),
  },
}));

import { instanceService } from '../../api/client';

const getPage = instanceService.getDeployLogPage as ReturnType<typeof vi.fn>;

const log = (id: string, minute: number, overrides: Partial<DeploymentLog> = {}): DeploymentLog => ({
  id,
  stack_instance_id: 'i1',
  action: 'deploy',
  status: 'success',
  started_at: new Date(Date.UTC(2030, 0, 1, 10, minute)).toISOString(),
  ...overrides,
});

const page = (data: DeploymentLog[], extra: Partial<DeployLogPage> = {}): DeployLogPage => ({
  data,
  total: data.length,
  ...extra,
});

/** A promise that the test resolves. */
function deferred<T>() {
  let resolve: (value: T) => void = () => {};
  let reject: (reason: unknown) => void = () => {};
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

afterEach(() => {
  vi.clearAllMocks();
});

describe('useDeployHistory', () => {
  it('loads the first page with the summary view and the page size', async () => {
    const current = log('d2', 2);
    getPage.mockResolvedValueOnce(page([current, log('d1', 1)], {
      total: 41, next_cursor: 'c1', highlights: { current },
    }));

    const { result } = renderHook(() => useDeployHistory('i1'));
    expect(result.current.loading).toBe(true);
    expect(result.current.hasHistory).toBe(false);

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(getPage).toHaveBeenCalledWith('i1', { limit: DEPLOY_HISTORY_PAGE_SIZE, action: undefined, status: undefined });
    expect(result.current.rows.map((r) => r.id)).toEqual(['d2', 'd1']);
    expect(result.current.total).toBe(41);
    expect(result.current.hasMore).toBe(true);
    expect(result.current.highlights).toEqual({ current });
    expect(result.current.hasHistory).toBe(true);
    expect(result.current.filtersActive).toBe(false);
    expect(result.current.error).toBeNull();
  });

  it('loads nothing without an instance ID', () => {
    const { result } = renderHook(() => useDeployHistory(undefined));
    expect(getPage).not.toHaveBeenCalled();
    expect(result.current.rows).toEqual([]);
  });

  it('reports an empty history', async () => {
    getPage.mockResolvedValueOnce(page([], { highlights: {} }));
    const { result } = renderHook(() => useDeployHistory('i1'));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.hasHistory).toBe(false);
    expect(result.current.total).toBe(0);
  });

  it('sets an error when the first page fails', async () => {
    getPage.mockRejectedValueOnce(new Error('boom'));
    const { result } = renderHook(() => useDeployHistory('i1'));
    await waitFor(() => expect(result.current.error).toBe('Failed to load the deployment history'));
    expect(result.current.loading).toBe(false);
  });

  it('appends the next page through the cursor and keeps the total of page 1', async () => {
    getPage
      .mockResolvedValueOnce(page([log('d3', 3), log('d2', 2)], { total: 3, next_cursor: 'c1' }))
      // The server repeats d2 (a new row moved the window): it shows once.
      .mockResolvedValueOnce(page([log('d2', 2), log('d1', 1)], { total: -1 }));

    const { result } = renderHook(() => useDeployHistory('i1'));
    await waitFor(() => expect(result.current.rows).toHaveLength(2));

    await act(async () => { await result.current.loadMore(); });

    expect(getPage).toHaveBeenLastCalledWith('i1', {
      limit: DEPLOY_HISTORY_PAGE_SIZE, cursor: 'c1', action: undefined, status: undefined,
    });
    expect(result.current.rows.map((r) => r.id)).toEqual(['d3', 'd2', 'd1']);
    expect(result.current.total).toBe(3);
    expect(result.current.hasMore).toBe(false);

    // No cursor left: no request.
    await act(async () => { await result.current.loadMore(); });
    expect(getPage).toHaveBeenCalledTimes(2);
  });

  it('sets an error when a next page fails and keeps the rows', async () => {
    getPage
      .mockResolvedValueOnce(page([log('d2', 2)], { total: 2, next_cursor: 'c1' }))
      .mockRejectedValueOnce(new Error('boom'));
    const { result } = renderHook(() => useDeployHistory('i1'));
    await waitFor(() => expect(result.current.rows).toHaveLength(1));

    await act(async () => { await result.current.loadMore(); });
    expect(result.current.error).toBe('Failed to load more operations');
    expect(result.current.rows).toHaveLength(1);
    expect(result.current.hasMore).toBe(true);
  });

  it('loads page 1 again with the filters, and keeps the history visible when nothing matches', async () => {
    getPage
      .mockResolvedValueOnce(page([log('d1', 1)], { total: 1 }))
      .mockResolvedValueOnce(page([], { total: 0, highlights: { current: log('d1', 1) } }));

    const { result } = renderHook(() => useDeployHistory('i1'));
    await waitFor(() => expect(result.current.rows).toHaveLength(1));

    await act(async () => { result.current.setFilters({ action: 'rollback', status: 'error' }); });
    await waitFor(() => expect(result.current.loading).toBe(false));

    expect(getPage).toHaveBeenLastCalledWith('i1', { limit: DEPLOY_HISTORY_PAGE_SIZE, action: 'rollback', status: 'error' });
    expect(result.current.filters).toEqual({ action: 'rollback', status: 'error' });
    expect(result.current.filtersActive).toBe(true);
    expect(result.current.rows).toEqual([]);
    expect(result.current.total).toBe(0);
    expect(result.current.hasHistory).toBe(true);

    // Clearing a filter drops the key.
    await act(async () => { result.current.setFilters({ action: undefined, status: 'error' }); });
    expect(result.current.filters).toEqual({ status: 'error' });
  });

  it('ignores an older response that returns after a newer one', async () => {
    const slow = deferred<DeployLogPage>();
    getPage
      .mockReturnValueOnce(slow.promise)
      .mockResolvedValueOnce(page([log('new', 5)], { total: 7 }));

    const { result } = renderHook(() => useDeployHistory('i1'));
    await act(async () => { await result.current.refresh(); });
    expect(result.current.rows.map((r) => r.id)).toEqual(['new']);

    await act(async () => { slow.resolve(page([log('old', 1)], { total: 1 })); });
    expect(result.current.rows.map((r) => r.id)).toEqual(['new']);
    expect(result.current.total).toBe(7);
  });

  it('ignores a next page that returns after a refresh started', async () => {
    const more = deferred<DeployLogPage>();
    getPage
      .mockResolvedValueOnce(page([log('d2', 2)], { total: 2, next_cursor: 'c1' }))
      .mockReturnValueOnce(more.promise)
      .mockResolvedValueOnce(page([log('d3', 3), log('d2', 2)], { total: 3, next_cursor: 'c2' }));

    const { result } = renderHook(() => useDeployHistory('i1'));
    await waitFor(() => expect(result.current.rows).toHaveLength(1));

    let loading: Promise<void> = Promise.resolve();
    act(() => { loading = result.current.loadMore(); });
    await act(async () => { await result.current.refresh(); });
    await act(async () => {
      more.resolve(page([log('d1', 1)], { total: -1 }));
      await loading;
    });

    expect(result.current.rows.map((r) => r.id)).toEqual(['d3', 'd2']);
    expect(result.current.hasMore).toBe(true);
    expect(result.current.loadingMore).toBe(false);
  });

  it('drops a next page that started while a refresh ran and returned after it', async () => {
    const refreshed = deferred<DeployLogPage>();
    const more = deferred<DeployLogPage>();
    getPage
      .mockResolvedValueOnce(page([log('d3', 3), log('d2', 2)], { total: 4, next_cursor: 'c1' }))
      .mockReturnValueOnce(refreshed.promise)
      .mockReturnValueOnce(more.promise)
      .mockResolvedValueOnce(page([log('d1', 1)], { total: -1 }));

    const { result } = renderHook(() => useDeployHistory('i1'));
    await waitFor(() => expect(result.current.rows).toHaveLength(2));

    // Order: refresh starts, loadMore starts, refresh returns, loadMore returns.
    let refreshing: Promise<void> = Promise.resolve();
    let loading: Promise<void> = Promise.resolve();
    act(() => { refreshing = result.current.refresh(); });
    act(() => { loading = result.current.loadMore(); });
    expect(getPage).toHaveBeenLastCalledWith('i1', {
      limit: DEPLOY_HISTORY_PAGE_SIZE, cursor: 'c1', action: undefined, status: undefined,
    });
    await act(async () => {
      refreshed.resolve(page([log('d4', 4), log('d3', 3), log('d2', 2)], { total: 5, next_cursor: 'c2' }));
      await refreshing;
    });
    await act(async () => {
      more.resolve(page([log('stale', 0)], { total: -1, next_cursor: 'c-old' }));
      await loading;
    });

    expect(result.current.rows.map((r) => r.id)).toEqual(['d4', 'd3', 'd2']);
    expect(result.current.total).toBe(5);
    expect(result.current.hasMore).toBe(true);
    expect(result.current.loadingMore).toBe(false);

    // The next "Show more" continues from the cursor of the refresh.
    await act(async () => { await result.current.loadMore(); });
    expect(getPage).toHaveBeenLastCalledWith('i1', {
      limit: DEPLOY_HISTORY_PAGE_SIZE, cursor: 'c2', action: undefined, status: undefined,
    });
    expect(result.current.rows.map((r) => r.id)).toEqual(['d4', 'd3', 'd2', 'd1']);
  });

  it('refresh keeps the filters, reloads the highlights and keeps the loaded rows', async () => {
    const twelve = Array.from({ length: 12 }, (_, i) => log(`d${12 - i}`, 12 - i));
    getPage
      .mockResolvedValueOnce(page(twelve.slice(0, 10), { total: 12, next_cursor: 'c1' }))
      .mockResolvedValueOnce(page(twelve.slice(10), { total: -1 }))
      .mockResolvedValueOnce(page(twelve.slice(0, 10), { total: 12, next_cursor: 'c1' }))
      .mockResolvedValueOnce(page(twelve, { total: 12, highlights: { current: twelve[0] } }));

    const { result } = renderHook(() => useDeployHistory('i1'));
    await waitFor(() => expect(result.current.rows).toHaveLength(10));
    await act(async () => { await result.current.loadMore(); });
    expect(result.current.rows).toHaveLength(12);

    await act(async () => { result.current.setFilters({ action: 'deploy' }); });
    await waitFor(() => expect(result.current.rows).toHaveLength(10));

    await act(async () => { await result.current.refresh(); });
    expect(getPage).toHaveBeenLastCalledWith('i1', { limit: 10, action: 'deploy', status: undefined });
    expect(result.current.highlights).toEqual({ current: twelve[0] });
    expect(result.current.filters).toEqual({ action: 'deploy' });
  });

  it('refresh reloads as many rows as "Show more" loaded', async () => {
    const twelve = Array.from({ length: 12 }, (_, i) => log(`d${12 - i}`, 12 - i));
    getPage
      .mockResolvedValueOnce(page(twelve.slice(0, 10), { total: 12, next_cursor: 'c1' }))
      .mockResolvedValueOnce(page(twelve.slice(10), { total: -1 }))
      .mockResolvedValueOnce(page(twelve, { total: 12 }));

    const { result } = renderHook(() => useDeployHistory('i1'));
    await waitFor(() => expect(result.current.rows).toHaveLength(10));
    await act(async () => { await result.current.loadMore(); });

    await act(async () => { await result.current.refresh(); });
    expect(getPage).toHaveBeenLastCalledWith('i1', { limit: 12, action: undefined, status: undefined });
    expect(result.current.rows).toHaveLength(12);
  });

  describe('addRunning', () => {
    const running = log('run', 9, { status: 'running' });

    it('adds the row on top, counts it and pins it as running', async () => {
      getPage.mockResolvedValueOnce(page([log('d1', 1)], { total: 1, highlights: { current: log('d1', 1) } }));
      const { result } = renderHook(() => useDeployHistory('i1'));
      await waitFor(() => expect(result.current.rows).toHaveLength(1));

      act(() => { result.current.addRunning(running); });
      expect(result.current.rows.map((r) => r.id)).toEqual(['run', 'd1']);
      expect(result.current.total).toBe(2);
      expect(result.current.highlights?.running).toEqual(running);

      // A second message for the same log changes nothing.
      act(() => { result.current.addRunning(running); });
      expect(result.current.rows).toHaveLength(2);
      expect(result.current.total).toBe(2);
    });

    it('shows the first operation of an empty history', async () => {
      getPage.mockResolvedValueOnce(page([]));
      const { result } = renderHook(() => useDeployHistory('i1'));
      await waitFor(() => expect(result.current.loading).toBe(false));
      expect(result.current.hasHistory).toBe(false);

      act(() => { result.current.addRunning(running); });
      expect(result.current.hasHistory).toBe(true);
      // An older server sends no highlights; they stay undefined.
      expect(result.current.highlights).toBeUndefined();
    });

    it('does not list a row that the filters exclude, but pins it', async () => {
      getPage
        .mockResolvedValueOnce(page([log('d1', 1)], { total: 1 }))
        .mockResolvedValueOnce(page([], { total: 0, highlights: {} }));
      const { result } = renderHook(() => useDeployHistory('i1'));
      await waitFor(() => expect(result.current.rows).toHaveLength(1));
      await act(async () => { result.current.setFilters({ status: 'error' }); });
      await waitFor(() => expect(result.current.loading).toBe(false));

      act(() => { result.current.addRunning(running); });
      expect(result.current.rows).toEqual([]);
      expect(result.current.total).toBe(0);
      expect(result.current.highlights?.running).toEqual(running);
    });

    it('keeps the row when a response that was requested before it does not have it', async () => {
      const slow = deferred<DeployLogPage>();
      getPage
        .mockResolvedValueOnce(page([log('d1', 1)], { total: 1, highlights: {} }))
        .mockReturnValueOnce(slow.promise)
        .mockResolvedValueOnce(page([{ ...running, status: 'success' }, log('d1', 1)], { total: 2, highlights: {} }));
      const { result } = renderHook(() => useDeployHistory('i1'));
      await waitFor(() => expect(result.current.rows).toHaveLength(1));

      let pending: Promise<void> = Promise.resolve();
      act(() => { pending = result.current.refresh(); });
      act(() => { result.current.addRunning(running); });
      await act(async () => {
        slow.resolve(page([log('d1', 1)], { total: 1, highlights: {} }));
        await pending;
      });
      expect(result.current.rows.map((r) => r.id)).toEqual(['run', 'd1']);
      expect(result.current.total).toBe(2);
      expect(result.current.highlights?.running?.id).toBe('run');

      // A response requested after the row has it: the server row wins.
      await act(async () => { await result.current.refresh(); });
      expect(result.current.rows.map((r) => [r.id, r.status])).toEqual([['run', 'success'], ['d1', 'success']]);
      expect(result.current.highlights?.running).toBeUndefined();
    });
  });

  it('resets and loads again when the instance changes', async () => {
    getPage
      .mockResolvedValueOnce(page([log('a1', 1)], { total: 1 }))
      .mockResolvedValueOnce(page([log('b1', 1, { stack_instance_id: 'i2' })], { total: 1 }));
    const { result, rerender } = renderHook(({ id }) => useDeployHistory(id), { initialProps: { id: 'i1' } });
    await waitFor(() => expect(result.current.rows.map((r) => r.id)).toEqual(['a1']));

    rerender({ id: 'i2' });
    await waitFor(() => expect(result.current.rows.map((r) => r.id)).toEqual(['b1']));
    expect(getPage).toHaveBeenLastCalledWith('i2', { limit: DEPLOY_HISTORY_PAGE_SIZE, action: undefined, status: undefined });
  });
});

describe('useRollbackTargets', () => {
  it('loads the successful deploys and filters the response again', async () => {
    getPage.mockResolvedValueOnce(page([
      log('d1', 1),
      log('fail', 2, { status: 'error' }),
      log('stop', 3, { action: 'stop' }),
      log('d2', 4),
    ]));
    const { result } = renderHook(() => useRollbackTargets('i1'));
    await waitFor(() => expect(result.current.deploys).toHaveLength(2));
    expect(getPage).toHaveBeenCalledWith('i1', { action: 'deploy', status: 'success', limit: 50 });
    // Newest first.
    expect(result.current.deploys.map((d) => d.id)).toEqual(['d2', 'd1']);
  });

  it('keeps the targets when a refresh fails and ignores stale responses', async () => {
    const slow = deferred<DeployLogPage>();
    getPage
      .mockResolvedValueOnce(page([log('d1', 1)]))
      .mockReturnValueOnce(slow.promise)
      .mockResolvedValueOnce(page([log('d2', 2), log('d1', 1)]))
      .mockRejectedValueOnce(new Error('boom'));
    const { result } = renderHook(() => useRollbackTargets('i1'));
    await waitFor(() => expect(result.current.deploys).toHaveLength(1));

    let pending: Promise<void> = Promise.resolve();
    act(() => { pending = result.current.refresh(); });
    await act(async () => { await result.current.refresh(); });
    await act(async () => {
      slow.resolve(page([]));
      await pending;
    });
    expect(result.current.deploys.map((d) => d.id)).toEqual(['d2', 'd1']);

    await act(async () => { await result.current.refresh(); });
    expect(result.current.deploys).toHaveLength(2);
  });
});
