import { describe, it, expect } from 'vitest';
import {
  successfulDeploys,
  currentDeployLogId,
  defaultRollbackTarget,
  describeDeploy,
  groupDeployRuns,
  describeDeployRun,
  pinnedRows,
  fallbackHighlights,
  matchesDeployFilters,
  formatDuration,
} from '../deployHistory';
import type { DeploymentLog } from '../../types';

const log = (id: string, minute: number, overrides: Partial<DeploymentLog> = {}): DeploymentLog => ({
  id,
  stack_instance_id: 'i1',
  action: 'deploy',
  status: 'success',
  output: '',
  started_at: new Date(Date.UTC(2030, 0, 1, 10, minute)).toISOString(),
  ...overrides,
});

describe('deployHistory', () => {
  const logs = [
    log('d1', 1),
    log('stop', 2, { action: 'stop' }),
    log('d2', 3),
    log('failed', 4, { status: 'error' }),
    log('d3', 5),
  ];

  it('lists successful deploys newest first', () => {
    expect(successfulDeploys(logs).map((l) => l.id)).toEqual(['d3', 'd2', 'd1']);
  });

  it('uses the newest successful deploy as current', () => {
    expect(currentDeployLogId(logs)).toBe('d3');
  });

  it('uses the target of a newer rollback as current', () => {
    expect(currentDeployLogId([...logs, log('rb', 6, { action: 'rollback', target_log_id: 'd1' })])).toBe('d1');
  });

  it('returns undefined after a rollback without a target', () => {
    expect(currentDeployLogId([...logs, log('rb', 6, { action: 'rollback' })])).toBeUndefined();
  });

  it('ignores a failed rollback', () => {
    expect(currentDeployLogId([...logs, log('rb', 6, { action: 'rollback', status: 'error', target_log_id: 'd1' })])).toBe('d3');
  });

  it('returns undefined without deploys', () => {
    expect(currentDeployLogId([])).toBeUndefined();
  });

  it('uses a newer partial deploy as current', () => {
    expect(currentDeployLogId([log('d1', 1), log('d2', 2, { status: 'partial' })])).toBe('d2');
  });

  it('uses the target of a newer partial rollback as current', () => {
    expect(currentDeployLogId([...logs, log('rb', 6, { action: 'rollback', status: 'partial', target_log_id: 'd1' })])).toBe('d1');
  });

  it('ignores a newer failed or running deploy', () => {
    expect(currentDeployLogId([log('d1', 1), log('x', 2, { status: 'error' }), log('r', 3, { status: 'running' })])).toBe('d1');
  });

  it('defaults the rollback target to the newest deploy when the current one is partial', () => {
    // d2 is partial: the current deploy, but not a rollback target.
    const history = [log('d1', 1), log('d2', 2, { status: 'partial' })];
    const current = currentDeployLogId(history);
    expect(current).toBe('d2');
    const deploys = successfulDeploys(history);
    expect(deploys.map((d) => d.id)).toEqual(['d1']);
    expect(defaultRollbackTarget(deploys, current)?.id).toBe('d1');
    expect(defaultRollbackTarget(successfulDeploys(logs), 'partial-x')?.id).toBe('d3');
  });

  it('defaults the rollback target to the deploy before the current one', () => {
    const deploys = successfulDeploys(logs);
    expect(defaultRollbackTarget(deploys, 'd3')?.id).toBe('d2');
    expect(defaultRollbackTarget(deploys, 'd2')?.id).toBe('d1');
    expect(defaultRollbackTarget(deploys, 'd1')?.id).toBe('d3');
    expect(defaultRollbackTarget(deploys, undefined)?.id).toBe('d2');
    expect(defaultRollbackTarget([], undefined)).toBeUndefined();
  });

  it('describes a deploy with its branch when known', () => {
    const l = log('d1', 1, { branch: 'feature-x' });
    expect(describeDeploy(l)).toBe(`${new Date(l.started_at).toLocaleString()} (branch feature-x)`);
    expect(describeDeploy(log('d2', 2))).toBe(new Date(log('d2', 2).started_at).toLocaleString());
  });
});

describe('groupDeployRuns', () => {
  // Newest first, as the API returns them.
  const at = (id: string, day: number, overrides: Partial<DeploymentLog> = {}): DeploymentLog => ({
    id,
    stack_instance_id: 'i1',
    action: 'deploy',
    status: 'success',
    branch: 'main',
    started_at: new Date(Date.UTC(2030, 9, day, 12)).toISOString(),
    ...overrides,
  });
  const ids = (items: ReturnType<typeof groupDeployRuns>) =>
    items.map((it) => (it.kind === 'log' ? it.log.id : `[${it.logs.map((l) => l.id).join(',')}]`));

  it('folds a run of three or more successful deploys of one branch', () => {
    const rows = [at('a', 19), at('b', 18), at('c', 17), at('d', 16)];
    const items = groupDeployRuns(rows, undefined);
    expect(ids(items)).toEqual(['[a,b,c,d]']);
    expect(items[0]).toMatchObject({ kind: 'group', key: 'a', branch: 'main' });
  });

  it('does not fold a run of two', () => {
    expect(ids(groupDeployRuns([at('a', 19), at('b', 18), at('s', 17, { action: 'stop' })], undefined)))
      .toEqual(['a', 'b', 's']);
  });

  it('starts a new run when the branch changes', () => {
    const rows = [
      at('a', 19), at('b', 18), at('c', 17),
      at('x', 16, { branch: 'feature' }), at('y', 15, { branch: 'feature' }),
    ];
    expect(ids(groupDeployRuns(rows, undefined))).toEqual(['[a,b,c]', 'x', 'y']);
  });

  it('breaks a run at another action or status', () => {
    const rows = [
      at('a', 19), at('b', 18), at('c', 17),
      at('e', 16, { status: 'error' }),
      at('d', 15), at('f', 14), at('g', 13),
      at('r', 12, { action: 'rollback' }),
      at('h', 11), at('i', 10),
    ];
    expect(ids(groupDeployRuns(rows, undefined))).toEqual(['[a,b,c]', 'e', '[d,f,g]', 'r', 'h', 'i']);
  });

  it('never folds the current deploy', () => {
    const rows = [at('a', 19), at('b', 18), at('cur', 17), at('c', 16), at('d', 15), at('e', 14)];
    expect(ids(groupDeployRuns(rows, 'cur'))).toEqual(['a', 'b', 'cur', '[c,d,e]']);
  });

  it('folds deploys without a branch together', () => {
    const rows = [at('a', 19, { branch: undefined }), at('b', 18, { branch: '' }), at('c', 17, { branch: undefined })];
    const items = groupDeployRuns(rows, undefined);
    expect(ids(items)).toEqual(['[a,b,c]']);
    expect(items[0]).toMatchObject({ kind: 'group', branch: undefined });
  });

  it('returns an empty list for no rows', () => {
    expect(groupDeployRuns([], undefined)).toEqual([]);
  });

  it('describes a run with its branch and date range', () => {
    const logs = [at('a', 19), at('b', 15), at('c', 12)];
    const day = (d: number) => new Date(Date.UTC(2030, 9, d, 12)).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
    expect(describeDeployRun(logs, 'main')).toBe(`3 deploys of branch main, ${day(12)} to ${day(19)}`);
    expect(describeDeployRun(logs)).toBe(`3 deploys, ${day(12)} to ${day(19)}`);
    expect(describeDeployRun([at('a', 19), at('b', 19), at('c', 19)], 'main')).toBe(`3 deploys of branch main, ${day(19)}`);
  });
});

describe('pinnedRows', () => {
  const running = log('run', 9, { status: 'running' });
  const current = log('cur', 5);
  const failure = log('fail', 8, { status: 'error' });

  it('lists running, current and last failure in that order', () => {
    expect(pinnedRows({ latest_failure: failure, current, running }).map((p) => [p.label, p.log.id])).toEqual([
      ['Running', 'run'],
      ['Current', 'cur'],
      ['Last failure', 'fail'],
    ]);
  });

  it('skips missing highlights', () => {
    expect(pinnedRows({ current }).map((p) => p.kind)).toEqual(['current']);
    expect(pinnedRows({})).toEqual([]);
    expect(pinnedRows(undefined)).toEqual([]);
  });

  it('shows a log once when two highlights are the same log', () => {
    expect(pinnedRows({ current, latest_failure: { ...current } }).map((p) => p.label)).toEqual(['Current']);
  });

  it('builds highlights from the rows for an older server', () => {
    const rows = [running, failure, current];
    expect(fallbackHighlights(rows, 'cur')).toEqual({ running, current, latest_failure: failure });
    expect(fallbackHighlights(rows, undefined)).toEqual({ running, current: undefined, latest_failure: failure });
    expect(fallbackHighlights([current], 'gone')).toEqual({ running: undefined, current: undefined, latest_failure: undefined });
  });

  it('pins a failure only when it is newer than the newest successful or partial deploy or rollback', () => {
    const older = log('old-fail', 4, { status: 'error' });
    expect(fallbackHighlights([current, older], 'cur').latest_failure).toBeUndefined();
    // A partial deploy is the anchor too.
    const partial = log('part', 9, { status: 'partial' });
    expect(fallbackHighlights([partial, failure, current], 'part').latest_failure).toBeUndefined();
    // A failure without any applied deploy counts.
    expect(fallbackHighlights([failure], undefined).latest_failure).toEqual(failure);
  });
});

describe('matchesDeployFilters and formatDuration', () => {
  it('matches the set filters only', () => {
    const l = log('d1', 1, { status: 'error' });
    expect(matchesDeployFilters(l, {})).toBe(true);
    expect(matchesDeployFilters(l, { action: 'deploy' })).toBe(true);
    expect(matchesDeployFilters(l, { action: 'stop' })).toBe(false);
    expect(matchesDeployFilters(l, { action: 'deploy', status: 'error' })).toBe(true);
    expect(matchesDeployFilters(l, { status: 'success' })).toBe(false);
  });

  it('formats seconds and minutes', () => {
    expect(formatDuration('2030-01-01T10:00:00Z', '2030-01-01T10:00:45Z')).toBe('45s');
    expect(formatDuration('2030-01-01T10:00:00Z', '2030-01-01T10:02:05Z')).toBe('2m 5s');
    expect(formatDuration('2030-01-01T10:00:05Z', '2030-01-01T10:00:00Z')).toBe('0s');
  });
});
