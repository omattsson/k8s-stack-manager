import type { DeploymentLog } from '../types';

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
 * Finds the deploy whose values run now: the newest successful deploy, or the
 * target of a newer successful rollback. Returns undefined when a newer rollback
 * has no target (the running revision is then not known from the logs).
 *
 * @param logs - The deployment logs of the instance.
 * @returns The ID of the current deploy log, or undefined.
 */
export function currentDeployLogId(logs: DeploymentLog[]): string | undefined {
  const latest = logs
    .filter((l) => (l.action === 'deploy' || l.action === 'rollback') && l.status === 'success')
    .sort(byNewest)[0];
  if (!latest) return undefined;
  if (latest.action === 'deploy') return latest.id;
  return latest.target_log_id || undefined;
}

/**
 * Picks the default rollback target: the successful deploy before the current one.
 *
 * @param deploys - Successful deploys, newest first.
 * @param currentId - ID of the current deploy log, if known.
 * @returns The default target, or undefined when there is no candidate.
 */
export function defaultRollbackTarget(deploys: DeploymentLog[], currentId: string | undefined): DeploymentLog | undefined {
  const idx = currentId ? deploys.findIndex((d) => d.id === currentId) : -1;
  if (idx >= 0) return deploys[idx + 1] ?? deploys.find((d) => d.id !== currentId);
  return deploys[1] ?? deploys[0];
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
