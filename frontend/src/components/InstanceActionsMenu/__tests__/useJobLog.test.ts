import { describe, it, expect, vi, afterEach } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { JOB_LOG_MAX_BACKOFF_MS, JOB_LOG_POLL_INTERVAL_MS, useJobLog } from '../useJobLog';
import type { UseJobLogOptions } from '../useJobLog';
import { instanceService } from '../../../api/client';
import type { ActionJobLog } from '../../../types';

vi.mock('../../../api/client', () => ({
  instanceService: {
    getActionJobLog: vi.fn(),
  },
}));

const getActionJobLog = instanceService.getActionJobLog as ReturnType<typeof vi.fn>;

const chunk = (over: Partial<ActionJobLog>): ActionJobLog => ({
  action: 'e2e-job',
  instance_id: 'i1',
  job_id: 'job-1',
  status: 'running',
  log: '',
  offset: 0,
  next_offset: 0,
  done: false,
  truncated: false,
  ...over,
});

const httpError = (status: number, message: string, headers: Record<string, string> = {}) =>
  Object.assign(new Error(message), { response: { status, headers, data: { error: message } } });

const networkError = () => Object.assign(new Error('Network Error'), { code: 'ERR_NETWORK' });

const advance = async (ms: number) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

/** Answer the polls in order (the last answer repeats) and record the clock time of each poll. */
const answerPolls = (...answers: Array<ActionJobLog | Error>) => {
  const times: number[] = [];
  getActionJobLog.mockImplementation(() => {
    times.push(Date.now());
    const answer = answers[Math.min(times.length, answers.length) - 1];
    return answer instanceof Error ? Promise.reject(answer) : Promise.resolve(answer);
  });
  return times;
};

const pollDelays = (times: number[], start: number) =>
  times.map((t, i) => t - (i === 0 ? start : times[i - 1]));

const base: UseJobLogOptions = { instanceId: 'i1', actionName: 'e2e-job', jobId: 'job-1', initialDelayMs: 0 };

describe('useJobLog', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  it('does not poll without a job ID', async () => {
    vi.useFakeTimers();
    const { result } = renderHook(() => useJobLog({ ...base, jobId: undefined }));

    await advance(JOB_LOG_POLL_INTERVAL_MS * 3);
    expect(getActionJobLog).not.toHaveBeenCalled();
    expect(result.current.following).toBe(false);
    expect(result.current.status).toBe('running');
  });

  it('waits initialDelayMs before the first poll', async () => {
    vi.useFakeTimers();
    getActionJobLog.mockReturnValue(new Promise(() => {}));
    renderHook(() => useJobLog({ ...base, initialDelayMs: JOB_LOG_POLL_INTERVAL_MS }));

    await advance(JOB_LOG_POLL_INTERVAL_MS - 1);
    expect(getActionJobLog).not.toHaveBeenCalled();
    await advance(1);
    expect(getActionJobLog).toHaveBeenCalledTimes(1);
  });

  it('backs off on 503, network errors and 429 with Retry-After, then polls at the normal interval', async () => {
    vi.useFakeTimers();
    const onDone = vi.fn();
    const times = answerPolls(
      httpError(503, 'Service unavailable'),
      networkError(),
      httpError(429, 'Rate limit exceeded', { 'retry-after': '2' }),
      chunk({ log: 'step 1\n', next_offset: 7 }),
      chunk({ log: 'step 2\n', offset: 7, next_offset: 14, status: 'succeeded', done: true }),
    );
    const start = Date.now();
    const { result } = renderHook(() => useJobLog({ ...base, onDone }));

    await advance(0);
    expect(result.current.waiting).toBe(true);
    expect(result.current.following).toBe(true);
    await advance(6000);
    await advance(12000);
    expect(result.current.waiting).toBe(true);
    await advance(2000);
    expect(result.current.waiting).toBe(false);
    expect(result.current.log).toBe('step 1\n');
    await advance(JOB_LOG_POLL_INTERVAL_MS);

    expect(pollDelays(times, start)).toEqual([0, 6000, 12000, 2000, JOB_LOG_POLL_INTERVAL_MS]);
    expect(result.current.status).toBe('succeeded');
    expect(result.current.done).toBe(true);
    expect(result.current.following).toBe(false);
    expect(result.current.error).toBeNull();
    expect(onDone).toHaveBeenCalledTimes(1);
    expect(onDone).toHaveBeenCalledWith('succeeded');
  });

  it('caps the doubled delay at 30 s', async () => {
    vi.useFakeTimers();
    const times = answerPolls(networkError());
    const start = Date.now();
    renderHook(() => useJobLog(base));

    const planned = [0, 6000, 12000, 24000, JOB_LOG_MAX_BACKOFF_MS, JOB_LOG_MAX_BACKOFF_MS];
    for (const delay of planned) await advance(delay);
    expect(pollDelays(times, start)).toEqual(planned);
  });

  it('stops on 403 with the server message', async () => {
    vi.useFakeTimers();
    getActionJobLog.mockRejectedValue(httpError(403, 'Forbidden'));
    const { result } = renderHook(() => useJobLog(base));

    await advance(0);
    expect(result.current.error).toBe('Failed to read the job log (HTTP 403: Forbidden)');
    expect(result.current.following).toBe(false);
    await advance(JOB_LOG_MAX_BACKOFF_MS * 2);
    expect(getActionJobLog).toHaveBeenCalledTimes(1);
  });

  it('uses notFoundMessage for 404', async () => {
    vi.useFakeTimers();
    getActionJobLog.mockRejectedValue(httpError(404, 'job log not found'));
    const { result } = renderHook(() => useJobLog({ ...base, notFoundMessage: 'Gone.' }));

    await advance(0);
    expect(result.current.error).toBe('Gone.');
  });

  it('stops on stopFollowing with a notice', async () => {
    vi.useFakeTimers();
    getActionJobLog.mockResolvedValue(chunk({ log: '.', next_offset: 1 }));
    const { result } = renderHook(() => useJobLog(base));

    await advance(0);
    act(() => result.current.stopFollowing());
    expect(result.current.notice).toBe('Stopped following the job log. The job can still run on the server.');
    expect(result.current.following).toBe(false);
    await advance(JOB_LOG_POLL_INTERVAL_MS * 5);
    expect(getActionJobLog).toHaveBeenCalledTimes(1);
  });

  it('calls the newest onDone without a restart of the polling', async () => {
    vi.useFakeTimers();
    const first = vi.fn();
    const second = vi.fn();
    answerPolls(
      chunk({ log: 'a', next_offset: 1 }),
      chunk({ log: 'b', offset: 1, next_offset: 2, status: 'failed', done: true }),
    );
    const { rerender } = renderHook(({ onDone }) => useJobLog({ ...base, onDone }), { initialProps: { onDone: first } });

    await advance(0);
    rerender({ onDone: second });
    await advance(JOB_LOG_POLL_INTERVAL_MS);
    expect(getActionJobLog).toHaveBeenCalledTimes(2);
    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledWith('failed');
  });
});
