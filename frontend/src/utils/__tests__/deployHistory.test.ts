import { describe, it, expect } from 'vitest';
import { successfulDeploys, currentDeployLogId, defaultRollbackTarget, describeDeploy } from '../deployHistory';
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
