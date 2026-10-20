import { useCallback, useEffect, useRef, useState } from 'react';
import { instanceService } from '../api/client';
import type { DeploymentLog, DeployLogFilters, DeployLogHighlights } from '../types';
import { matchesDeployFilters, successfulDeploys } from '../utils/deployHistory';

/** Logs on the first page and on each "Show more" page. */
export const DEPLOY_HISTORY_PAGE_SIZE = 10;
/** Most logs that a refresh reloads (it keeps the rows that "Show more" loaded). */
const MAX_REFRESH_LIMIT = 100;
/** Most successful deploys that the rollback dialog lists. */
const ROLLBACK_TARGET_LIMIT = 50;

export interface DeployHistory {
  /** Loaded logs, newest first, without output. */
  rows: DeploymentLog[];
  /** Logs that match the filters (from the first page). */
  total: number;
  /** Highlights of the server. Undefined for a server that sends none. */
  highlights?: DeployLogHighlights;
  /** True while a next page exists. */
  hasMore: boolean;
  filters: DeployLogFilters;
  /** True when a filter is set. */
  filtersActive: boolean;
  /** False while the unfiltered history is empty (or not loaded yet). */
  hasHistory: boolean;
  /** The first page loads. */
  loading: boolean;
  /** A "Show more" page loads. */
  loadingMore: boolean;
  error: string | null;
  /** Set the filters and load the first page again. */
  setFilters: (filters: DeployLogFilters) => void;
  /** Load the next page. */
  loadMore: () => Promise<void>;
  /** Load the first page and the highlights again. Keeps the filters. */
  refresh: () => Promise<void>;
  /** Show a running log that a WebSocket status announced before the list has it. */
  addRunning: (row: DeploymentLog) => void;
}

interface HistoryState {
  rows: DeploymentLog[];
  total: number;
  nextCursor?: string;
  highlights?: DeployLogHighlights;
  hasHistory: boolean;
  loading: boolean;
  loadingMore: boolean;
  error: string | null;
}

const INITIAL: HistoryState = {
  rows: [],
  total: 0,
  hasHistory: false,
  loading: false,
  loadingMore: false,
  error: null,
};

const isActive = (filters: DeployLogFilters) => Boolean(filters.action || filters.status);

/**
 * Pages through the deployment history of an instance (summary rows, newest
 * first). Loads the first page when the instance ID changes. A response of
 * an older request is ignored when a newer first-page request exists.
 *
 * @param instanceId - Instance ID (undefined: nothing loads)
 * @returns The rows, the total, the highlights, the filters and the actions
 */
export function useDeployHistory(instanceId: string | undefined): DeployHistory {
  const [state, setState] = useState<HistoryState>(INITIAL);
  const [filters, setFiltersState] = useState<DeployLogFilters>({});

  const idRef = useRef(instanceId);
  const filtersRef = useRef<DeployLogFilters>({});
  // Each first-page request takes a new number; older responses are dropped.
  const seqRef = useRef(0);
  const cursorRef = useRef<string | undefined>(undefined);
  const loadingMoreRef = useRef(false);
  const rowCountRef = useRef(0);
  // Rows from addRunning. A first-page response that was requested before
  // the row came keeps it, because the server did not know it yet.
  const addedRef = useRef<{ row: DeploymentLog; mark: number }[]>([]);
  const addMarkRef = useRef(0);

  useEffect(() => {
    rowCountRef.current = state.rows.length;
  }, [state.rows]);

  const loadFirst = useCallback(async (active: DeployLogFilters, limit: number) => {
    const id = idRef.current;
    if (!id) return;
    const seq = ++seqRef.current;
    const mark = addMarkRef.current;
    loadingMoreRef.current = false;
    setState((s) => ({ ...s, loading: true, loadingMore: false }));
    try {
      const page = await instanceService.getDeployLogPage(id, {
        limit,
        action: active.action,
        status: active.status,
      });
      if (seq !== seqRef.current) return;
      const kept = addedRef.current
        .filter((a) => a.mark > mark && matchesDeployFilters(a.row, active) && !page.data.some((r) => r.id === a.row.id))
        .map((a) => a.row);
      addedRef.current = addedRef.current.filter((a) => a.mark > mark);
      let highlights = page.highlights;
      if (highlights && kept.length > 0 && !highlights.running) highlights = { ...highlights, running: kept[0] };
      const rows = [...kept, ...page.data];
      cursorRef.current = page.next_cursor;
      setState((s) => ({
        rows,
        total: (page.total >= 0 ? page.total : page.data.length) + kept.length,
        nextCursor: page.next_cursor,
        highlights,
        // A filtered empty list does not hide the history.
        hasHistory: isActive(active)
          ? s.hasHistory || rows.length > 0
          : rows.length > 0 || Boolean(highlights?.running || highlights?.current || highlights?.latest_failure),
        loading: false,
        loadingMore: false,
        error: null,
      }));
    } catch {
      if (seq !== seqRef.current) return;
      setState((s) => ({ ...s, loading: false, error: 'Failed to load the deployment history' }));
    }
  }, []);

  useEffect(() => {
    idRef.current = instanceId;
    // Drop the responses and rows of the previous instance.
    seqRef.current += 1;
    cursorRef.current = undefined;
    loadingMoreRef.current = false;
    addedRef.current = [];
    filtersRef.current = {};
    setFiltersState({});
    setState(INITIAL);
    if (instanceId) loadFirst({}, DEPLOY_HISTORY_PAGE_SIZE);
  }, [instanceId, loadFirst]);

  const setFilters = useCallback((next: DeployLogFilters) => {
    const clean: DeployLogFilters = {
      ...(next.action && { action: next.action }),
      ...(next.status && { status: next.status }),
    };
    filtersRef.current = clean;
    setFiltersState(clean);
    cursorRef.current = undefined;
    setState((s) => ({ ...s, rows: [], total: 0, nextCursor: undefined }));
    loadFirst(clean, DEPLOY_HISTORY_PAGE_SIZE);
  }, [loadFirst]);

  const refresh = useCallback(() => {
    const limit = Math.min(Math.max(DEPLOY_HISTORY_PAGE_SIZE, rowCountRef.current), MAX_REFRESH_LIMIT);
    return loadFirst(filtersRef.current, limit);
  }, [loadFirst]);

  const loadMore = useCallback(async () => {
    const id = idRef.current;
    const cursor = cursorRef.current;
    if (!id || !cursor || loadingMoreRef.current) return;
    loadingMoreRef.current = true;
    const seq = seqRef.current;
    const active = filtersRef.current;
    setState((s) => ({ ...s, loadingMore: true }));
    try {
      const page = await instanceService.getDeployLogPage(id, {
        limit: DEPLOY_HISTORY_PAGE_SIZE,
        cursor,
        action: active.action,
        status: active.status,
      });
      // A first-page request started since: its rows replace this page.
      if (seq !== seqRef.current) return;
      // A first-page request that was in flight returned first and moved the
      // cursor: this page continues the old list.
      if (cursorRef.current !== cursor) {
        setState((s) => ({ ...s, loadingMore: false }));
        return;
      }
      cursorRef.current = page.next_cursor;
      setState((s) => {
        const seen = new Set(s.rows.map((r) => r.id));
        return {
          ...s,
          rows: [...s.rows, ...page.data.filter((r) => !seen.has(r.id))],
          nextCursor: page.next_cursor,
          loadingMore: false,
          error: null,
        };
      });
    } catch {
      if (seq !== seqRef.current) return;
      setState((s) => ({ ...s, loadingMore: false, error: 'Failed to load more operations' }));
    } finally {
      if (seq === seqRef.current) loadingMoreRef.current = false;
    }
  }, []);

  const addRunning = useCallback((row: DeploymentLog) => {
    addMarkRef.current += 1;
    addedRef.current.push({ row, mark: addMarkRef.current });
    setState((s) => {
      const highlights = s.highlights && s.highlights.running?.id !== row.id
        ? { ...s.highlights, running: row }
        : s.highlights;
      if (s.rows.some((r) => r.id === row.id) || !matchesDeployFilters(row, filtersRef.current)) {
        return { ...s, highlights, hasHistory: true };
      }
      return { ...s, rows: [row, ...s.rows], total: s.total + 1, highlights, hasHistory: true };
    });
  }, []);

  return {
    rows: state.rows,
    total: state.total,
    highlights: state.highlights,
    hasMore: Boolean(state.nextCursor),
    filters,
    filtersActive: isActive(filters),
    hasHistory: state.hasHistory,
    loading: state.loading,
    loadingMore: state.loadingMore,
    error: state.error,
    setFilters,
    loadMore,
    refresh,
    addRunning,
  };
}

/**
 * Loads the successful deploys of an instance for the rollback dialog,
 * newest first. Also filters the response, in case the server ignores the
 * action and status filters.
 *
 * @param instanceId - Instance ID (undefined: nothing loads)
 * @returns The successful deploys and a refresh function
 */
export function useRollbackTargets(instanceId: string | undefined): {
  deploys: DeploymentLog[];
  refresh: () => Promise<void>;
} {
  const [deploys, setDeploys] = useState<DeploymentLog[]>([]);
  const idRef = useRef(instanceId);
  const seqRef = useRef(0);

  const refresh = useCallback(async () => {
    const id = idRef.current;
    if (!id) return;
    const seq = ++seqRef.current;
    try {
      const page = await instanceService.getDeployLogPage(id, {
        action: 'deploy',
        status: 'success',
        limit: ROLLBACK_TARGET_LIMIT,
      });
      if (seq === seqRef.current) setDeploys(successfulDeploys(page.data));
    } catch { /* keep the previous targets */ }
  }, []);

  useEffect(() => {
    idRef.current = instanceId;
    seqRef.current += 1;
    setDeploys([]);
    if (instanceId) refresh();
  }, [instanceId, refresh]);

  return { deploys, refresh };
}
