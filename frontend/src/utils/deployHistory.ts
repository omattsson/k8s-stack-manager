import type { DeploymentLog, DeployLogFilters, DeployLogHighlights } from '../types';

const byNewest = (a: DeploymentLog, b: DeploymentLog) =>
  new Date(b.started_at).getTime() - new Date(a.started_at).getTime();

/**
 * Returns the successful deploys of an instance, newest first.
 *
 * @param logs - The deployment logs of the instance.
 * @returns The successful deploy logs, newest first.
 */
export function successfulDeploys(logs: DeploymentLog[]): DeploymentLog[] {
  return logs.filter((l) => l.action === 'deploy' && l.status === 'success').sort(byNewest);
}

/**
 * Finds the newest deploy or rollback whose values run now: status success
 * or partial (a partial deploy finished, but not all workloads are ready).
 *
 * @param logs - The deployment logs of the instance.
 * @returns The newest such log, or undefined.
 */
function lastAppliedLog(logs: DeploymentLog[]): DeploymentLog | undefined {
  return logs
    .filter((l) => (l.action === 'deploy' || l.action === 'rollback') && (l.status === 'success' || l.status === 'partial'))
    .sort(byNewest)[0];
}

/**
 * Finds the deploy whose values run now: the newest successful or partial
 * deploy, or the target of a newer rollback. Returns undefined when a newer
 * rollback has no target (the running revision is then not known from the logs).
 *
 * @param logs - The deployment logs of the instance.
 * @returns The ID of the current deploy log, or undefined.
 */
export function currentDeployLogId(logs: DeploymentLog[]): string | undefined {
  const latest = lastAppliedLog(logs);
  if (!latest) return undefined;
  if (latest.action === 'deploy') return latest.id;
  return latest.target_log_id || undefined;
}

/**
 * Picks the default rollback target: the successful deploy before the current
 * one. When the current deploy is not in the list (a partial deploy), it is
 * the newest successful deploy.
 *
 * @param deploys - Successful deploys, newest first.
 * @param currentId - ID of the current deploy log, if known.
 * @returns The default target, or undefined when there is no candidate.
 */
export function defaultRollbackTarget(deploys: DeploymentLog[], currentId: string | undefined): DeploymentLog | undefined {
  if (!currentId) return deploys[1] ?? deploys[0];
  const idx = deploys.findIndex((d) => d.id === currentId);
  if (idx < 0) return deploys[0];
  return deploys[idx + 1] ?? deploys.find((d) => d.id !== currentId);
}

/**
 * Describes a deploy log for the user: its start time and, when known, its branch.
 *
 * @param log - The deploy log.
 * @returns For example "10/9/2026, 14:30:00 (branch main)".
 */
export function describeDeploy(log: DeploymentLog): string {
  const when = new Date(log.started_at).toLocaleString();
  return log.branch ? `${when} (branch ${log.branch})` : when;
}

/** Smallest run of successful deploys that the history folds into one row. */
export const MIN_DEPLOY_RUN = 3;

/** One item of the history list: one log, or a folded run of successful deploys. */
export type DeployHistoryItem =
  | { kind: 'log'; log: DeploymentLog }
  | { kind: 'group'; key: string; logs: DeploymentLog[]; branch?: string };

const foldable = (log: DeploymentLog, currentId: string | undefined) =>
  log.action === 'deploy' && log.status === 'success' && log.id !== currentId;

/**
 * Folds runs of adjacent successful deploys of the same branch into groups.
 * A run needs at least MIN_DEPLOY_RUN logs. The current deploy is never
 * folded: it ends a run.
 *
 * @param rows - The loaded logs, newest first.
 * @param currentId - ID of the current deploy log, if known.
 * @returns The list items in the order of the rows.
 */
export function groupDeployRuns(rows: DeploymentLog[], currentId: string | undefined): DeployHistoryItem[] {
  const items: DeployHistoryItem[] = [];
  let i = 0;
  while (i < rows.length) {
    if (!foldable(rows[i], currentId)) {
      items.push({ kind: 'log', log: rows[i] });
      i += 1;
      continue;
    }
    const branch = rows[i].branch || '';
    let end = i + 1;
    while (end < rows.length && foldable(rows[end], currentId) && (rows[end].branch || '') === branch) end += 1;
    const run = rows.slice(i, end);
    if (run.length >= MIN_DEPLOY_RUN) {
      // The newest log names the group: new deploys become current and do not join it.
      items.push({ kind: 'group', key: run[0].id, logs: run, branch: branch || undefined });
    } else {
      run.forEach((log) => items.push({ kind: 'log', log }));
    }
    i = end;
  }
  return items;
}

/**
 * Describes a folded run of deploys.
 *
 * @param logs - The deploys of the run, newest first.
 * @param branch - The branch of the run, if any.
 * @returns For example "12 deploys of branch main, Oct 12 to Oct 19".
 */
export function describeDeployRun(logs: DeploymentLog[], branch?: string): string {
  const newest = new Date(logs[0].started_at);
  const oldest = new Date(logs[logs.length - 1].started_at);
  const opts: Intl.DateTimeFormatOptions = { month: 'short', day: 'numeric' };
  if (newest.getFullYear() !== oldest.getFullYear()) opts.year = 'numeric';
  const from = oldest.toLocaleDateString(undefined, opts);
  const to = newest.toLocaleDateString(undefined, opts);
  const range = from === to ? from : `${from} to ${to}`;
  const what = branch ? `${logs.length} deploys of branch ${branch}` : `${logs.length} deploys`;
  return `${what}, ${range}`;
}

/** A log that the history shows on top, with its label. */
export interface PinnedLog {
  kind: keyof DeployLogHighlights;
  label: string;
  log: DeploymentLog;
}

const PINNED: { kind: keyof DeployLogHighlights; label: string }[] = [
  { kind: 'running', label: 'Running' },
  { kind: 'current', label: 'Current' },
  { kind: 'latest_failure', label: 'Last failure' },
];

/**
 * Lists the highlights to show on top: running, current, last failure.
 * A log that is in two highlights shows once, with the first label.
 *
 * @param highlights - The highlights of the server, or of fallbackHighlights.
 * @returns The pinned logs in display order.
 */
export function pinnedRows(highlights: DeployLogHighlights | undefined): PinnedLog[] {
  const out: PinnedLog[] = [];
  for (const { kind, label } of PINNED) {
    const log = highlights?.[kind];
    if (log && !out.some((p) => p.log.id === log.id)) out.push({ kind, label, log });
  }
  return out;
}

/**
 * Highlights from the loaded logs, for a server that sends none: the newest
 * running log, the current deploy, and the newest failure when it is newer
 * than the newest successful or partial deploy or rollback.
 *
 * @param rows - The loaded logs.
 * @param currentId - ID of the current deploy log, if known.
 * @returns The highlights found in the rows.
 */
export function fallbackHighlights(rows: DeploymentLog[], currentId: string | undefined): DeployLogHighlights {
  const sorted = [...rows].sort(byNewest);
  const anchor = lastAppliedLog(sorted);
  const failure = sorted.find((l) => l.status === 'error');
  const newer = failure && (!anchor || new Date(failure.started_at).getTime() > new Date(anchor.started_at).getTime());
  return {
    running: sorted.find((l) => l.status === 'running'),
    current: currentId ? rows.find((l) => l.id === currentId) : undefined,
    latest_failure: newer ? failure : undefined,
  };
}

/**
 * Checks a log against the history filters.
 *
 * @param log - The deployment log.
 * @param filters - The action and status filters.
 * @returns True when the log matches every set filter.
 */
export function matchesDeployFilters(log: DeploymentLog, filters: DeployLogFilters): boolean {
  return (!filters.action || log.action === filters.action) && (!filters.status || log.status === filters.status);
}

/**
 * Formats the run time of an operation.
 *
 * @param startedAt - Start time (ISO 8601).
 * @param completedAt - End time (ISO 8601).
 * @returns For example "45s" or "2m 5s".
 */
export function formatDuration(startedAt: string, completedAt: string): string {
  const seconds = Math.max(0, Math.round((new Date(completedAt).getTime() - new Date(startedAt).getTime()) / 1000));
  if (seconds < 60) return `${seconds}s`;
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}
