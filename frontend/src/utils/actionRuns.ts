import type { ActionRun, ActionRunPage, InstanceAction } from '../types';

/** Chip color of a job status. */
export type JobStatusColor = 'info' | 'success' | 'error';

/**
 * Chip color of a job status: `succeeded` is success, an active status
 * (`running`, `pending`, `queued`, `started`) is info, any other status is error.
 * @param status - Job status from the job log or the action run
 * @returns The MUI color of the status chip
 */
export const statusColor = (status: string): JobStatusColor => {
  if (status === 'succeeded') return 'success';
  if (['running', 'pending', 'queued', 'started'].includes(status)) return 'info';
  return 'error';
};

/**
 * Text and color of the status chip of an action run.
 * @param run - The action run
 * @returns "Unreachable", "Failed (<code>)", "Unusable answer (<code>)", "OK (<code>)", "Job started" or the job status, with its color
 */
export const actionRunStatus = (run: ActionRun): { label: string; color: JobStatusColor } => {
  const code = run.status_code;
  if (code === 0) return { label: 'Unreachable', color: 'error' };
  if (code < 200 || code >= 300) return { label: `Failed (${code})`, color: 'error' };
  // The server stores a message with a 2xx code when the body was not JSON or too large.
  if (run.message) return { label: `Unusable answer (${code})`, color: 'error' };
  if (!run.job_id) return { label: `OK (${code})`, color: 'success' };
  if (!run.job_status) return { label: 'Job started', color: 'info' };
  return { label: run.job_status, color: statusColor(run.job_status) };
};

/**
 * Parameters of an action run as `[name, text]` pairs: first in the declared
 * order of the action (when the action is known), then the other names in
 * the response order. A boolean gives `true` or `false`.
 * @param run - The action run
 * @param action - The action of the run, when the server still lists it
 * @returns The parameters to show
 */
export const runParameterEntries = (run: ActionRun, action?: InstanceAction): Array<[string, string]> => {
  const params = run.parameters && typeof run.parameters === 'object' ? run.parameters : {};
  const names = Object.keys(params);
  const declared = (action?.parameters ?? []).map((p) => p.name).filter((n) => names.includes(n));
  const ordered = [...declared, ...names.filter((n) => !declared.includes(n))];
  return ordered.map((name) => {
    const value: unknown = params[name];
    return [name, typeof value === 'string' ? value : JSON.stringify(value)];
  });
};

/**
 * Merge a newly loaded first page into the shown rows. Rows of the page come
 * first and replace the shown rows with the same ID; the other shown rows stay
 * after them. A stored final job status is kept when the new row has none.
 * @param page - Rows of the first page, newest first
 * @param shown - Rows on the screen
 * @returns The merged rows
 */
export const mergeFirstPage = (page: ActionRun[], shown: ActionRun[]): ActionRun[] => {
  const byId = new Map(shown.map((r) => [r.id, r]));
  const fresh = page.map((r) => {
    const old = byId.get(r.id);
    return !r.job_status && old?.job_status ? { ...r, job_status: old.job_status } : r;
  });
  const ids = new Set(page.map((r) => r.id));
  return [...fresh, ...shown.filter((r) => !ids.has(r.id))];
};

/** Rows of the action run list and the paging state. */
export interface RunListState {
  rows: ActionRun[];
  /** Cursor of the page after the last loaded page. */
  cursor?: string;
  /** Number of loaded pages. */
  pages: number;
  canViewLogs: boolean;
}

/**
 * Apply a newly loaded first page. It merges into the shown rows and keeps
 * the cursor of the last loaded page. When the page shares no row with the
 * shown rows (many new runs since the last load), it replaces the list.
 * @param prev - The shown list
 * @param page - The first page
 * @returns The new list
 */
export const applyFirstPage = (prev: RunListState, page: ActionRunPage): RunListState => {
  const shownIds = new Set(prev.rows.map((r) => r.id));
  if (prev.pages === 0 || !page.data.some((r) => shownIds.has(r.id))) {
    return { rows: page.data, cursor: page.next_cursor, pages: 1, canViewLogs: page.can_view_logs };
  }
  return {
    rows: mergeFirstPage(page.data, prev.rows),
    cursor: prev.pages > 1 ? prev.cursor : page.next_cursor,
    pages: prev.pages,
    canViewLogs: page.can_view_logs,
  };
};

/**
 * Apply a further page of "Show more".
 * @param prev - The shown list
 * @param page - The next page
 * @returns The list with the page at the end and the cursor of the page
 */
export const applyNextPage = (prev: RunListState, page: ActionRunPage): RunListState => ({
  rows: appendPage(prev.rows, page.data),
  cursor: page.next_cursor,
  pages: prev.pages + 1,
  canViewLogs: page.can_view_logs,
});

/**
 * Append a further page to the shown rows. Rows that are already shown are skipped.
 * @param shown - Rows on the screen
 * @param page - Rows of the next page
 * @returns The rows with the new page at the end
 */
export const appendPage = (shown: ActionRun[], page: ActionRun[]): ActionRun[] => {
  const ids = new Set(shown.map((r) => r.id));
  return [...shown, ...page.filter((r) => !ids.has(r.id))];
};
