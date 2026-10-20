import { describe, it, expect } from 'vitest';
import {
  actionRunStatus, appendPage, applyFirstPage, applyNextPage, mergeFirstPage, runParameterEntries, statusColor,
} from '../actionRuns';
import type { RunListState } from '../actionRuns';
import type { ActionRun, ActionRunPage, InstanceAction } from '../../types';

const run = (id: string, over: Partial<ActionRun> = {}): ActionRun => ({
  id,
  instance_id: 'i1',
  action: 'a',
  action_label: 'A',
  user_id: 'u1',
  started_at: '2026-10-20T10:00:00Z',
  parameters: {},
  status_code: 200,
  has_job_log: false,
  ...over,
});

describe('statusColor', () => {
  it.each([
    ['succeeded', 'success'],
    ['running', 'info'],
    ['pending', 'info'],
    ['queued', 'info'],
    ['started', 'info'],
    ['failed', 'error'],
    ['cancelled', 'error'],
  ])('%s gives %s', (status, color) => {
    expect(statusColor(status)).toBe(color);
  });
});

describe('actionRunStatus', () => {
  it.each([
    [{ status_code: 0 }, 'Unreachable', 'error'],
    [{ status_code: 199 }, 'Failed (199)', 'error'],
    [{ status_code: 500 }, 'Failed (500)', 'error'],
    [{ status_code: 204 }, 'OK (204)', 'success'],
    [{ status_code: 200, message: 'Subscriber returned a too large body' }, 'Unusable answer (200)', 'error'],
    [{ status_code: 409, message: 'busy' }, 'Failed (409)', 'error'],
    [{ status_code: 202, job_id: 'j' }, 'Job started', 'info'],
    [{ status_code: 202, job_id: 'j', job_status: 'queued' }, 'queued', 'info'],
    [{ status_code: 409, job_id: 'j', job_status: 'succeeded' }, 'Failed (409)', 'error'],
  ])('%o gives %s', (over, label, color) => {
    expect(actionRunStatus(run('r', over))).toEqual({ label, color });
  });
});

describe('runParameterEntries', () => {
  const action: InstanceAction = {
    name: 'a',
    label: 'A',
    parameters: [
      { name: 'x', label: 'X', type: 'string', required: false },
      { name: 'y', label: 'Y', type: 'bool', required: false },
    ],
    has_job_log: false,
    can_invoke: true,
  };

  it('puts the declared names first and the rest in response order', () => {
    const r = run('r', { parameters: { z: 'last', y: false, x: 'first' } });
    expect(runParameterEntries(r, action)).toEqual([['x', 'first'], ['y', 'false'], ['z', 'last']]);
  });

  it('keeps the response order without an action and tolerates a missing object', () => {
    expect(runParameterEntries(run('r', { parameters: { b: true, a: 'v' } }))).toEqual([['b', 'true'], ['a', 'v']]);
    expect(runParameterEntries(run('r', { parameters: undefined as unknown as ActionRun['parameters'] }))).toEqual([]);
  });
});

describe('mergeFirstPage', () => {
  it('puts the page first, replaces rows by ID and keeps the older rows', () => {
    const shown = [run('r2', { message: 'old' }), run('r1'), run('r0')];
    const merged = mergeFirstPage([run('r3'), run('r2', { message: 'new' })], shown);
    expect(merged.map((r) => r.id)).toEqual(['r3', 'r2', 'r1', 'r0']);
    expect(merged[1].message).toBe('new');
  });

  it('keeps a known final job status when the new row has none', () => {
    const shown = [run('r1', { job_id: 'j', job_status: 'succeeded' })];
    expect(mergeFirstPage([run('r1', { job_id: 'j' })], shown)[0].job_status).toBe('succeeded');
  });
});

describe('appendPage', () => {
  it('skips rows that are already shown', () => {
    expect(appendPage([run('r2'), run('r1')], [run('r1'), run('r0')]).map((r) => r.id)).toEqual(['r2', 'r1', 'r0']);
  });
});

describe('applyFirstPage', () => {
  const page = (ids: string[], next_cursor?: string): ActionRunPage => ({
    instance_id: 'i1', can_view_logs: true, data: ids.map((id) => run(id)), next_cursor,
  });
  const state = (ids: string[], pages: number, cursor?: string): RunListState => ({
    rows: ids.map((id) => run(id)), pages, cursor, canViewLogs: false,
  });

  it('takes the first page as is on the first load', () => {
    expect(applyFirstPage(state([], 0), page(['r1'], 'c'))).toEqual({
      rows: [run('r1')], cursor: 'c', pages: 1, canViewLogs: true,
    });
  });

  it('merges and keeps the cursor of the last loaded page', () => {
    const next = applyFirstPage(state(['r2', 'r1', 'r0'], 2, 'c-last'), page(['r3', 'r2'], 'c-new'));
    expect(next.rows.map((r) => r.id)).toEqual(['r3', 'r2', 'r1', 'r0']);
    expect(next).toMatchObject({ cursor: 'c-last', pages: 2 });
  });

  it('takes the new cursor while only the first page is loaded', () => {
    expect(applyFirstPage(state(['r1'], 1), page(['r2', 'r1'], 'c-new'))).toMatchObject({ cursor: 'c-new', pages: 1 });
  });

  it('replaces the list when the page shares no run with it', () => {
    expect(applyFirstPage(state(['r2', 'r1'], 2), page(['n2', 'n1'], 'c-new'))).toEqual({
      rows: [run('n2'), run('n1')], cursor: 'c-new', pages: 1, canViewLogs: true,
    });
  });
});

describe('applyNextPage', () => {
  it('appends the page and takes its cursor', () => {
    const prev: RunListState = { rows: [run('r2')], pages: 1, cursor: 'c2', canViewLogs: false };
    const next = applyNextPage(prev, { instance_id: 'i1', can_view_logs: true, data: [run('r2'), run('r1')] });
    expect(next).toEqual({ rows: [run('r2'), run('r1')], cursor: undefined, pages: 2, canViewLogs: true });
  });
});
