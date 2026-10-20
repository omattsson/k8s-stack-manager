import { describe, it, expect, vi, afterEach } from 'vitest';
import { act, fireEvent, render, screen, within } from '@testing-library/react';
import JobLogDialog, { JOB_LOG_GONE_TEXT } from '../JobLogDialog';
import { JOB_LOG_POLL_INTERVAL_MS } from '../useJobLog';
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

const httpError = (status: number, message: string) =>
  Object.assign(new Error(message), { response: { status, headers: {}, data: { error: message } } });

/** Advance the fake clock and flush the promises of the poll. */
const advance = async (ms: number) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

const renderDialog = (props: { onDone?: (s: string) => void; onClose?: () => void; initialStatus?: string } = {}) =>
  render(
    <JobLogDialog
      instanceId="i1"
      actionName="e2e-job"
      label="E2E job"
      jobId="job-1"
      initialStatus={props.initialStatus}
      onDone={props.onDone}
      onClose={props.onClose ?? vi.fn()}
    />,
  );

describe('JobLogDialog', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  it('polls at once, shows the log and reports the final status', async () => {
    vi.useFakeTimers();
    const onDone = vi.fn();
    getActionJobLog
      .mockResolvedValueOnce(chunk({ log: 'Step 1 of 2 done\n', next_offset: 17 }))
      .mockResolvedValueOnce(chunk({ log: 'Step 2 of 2 done\n', offset: 17, next_offset: 34, status: 'succeeded', done: true }));
    renderDialog({ onDone });

    const dialog = screen.getByRole('dialog', { name: 'Job log: E2E job' });
    expect(within(dialog).getByText('Job job-1')).toBeInTheDocument();
    await advance(0);
    expect(getActionJobLog).toHaveBeenCalledTimes(1);
    expect(getActionJobLog).toHaveBeenCalledWith('i1', 'e2e-job', 'job-1', 0, expect.any(AbortSignal));
    expect(within(dialog).getByTestId('job-log')).toHaveTextContent('Step 1 of 2 done');
    expect(within(dialog).getByRole('button', { name: 'Stop following' })).toBeInTheDocument();
    expect(onDone).not.toHaveBeenCalled();

    await advance(JOB_LOG_POLL_INTERVAL_MS);
    expect(getActionJobLog).toHaveBeenLastCalledWith('i1', 'e2e-job', 'job-1', 17, expect.any(AbortSignal));
    expect(within(dialog).getByTestId('job-status')).toHaveTextContent('succeeded');
    expect(within(dialog).getByTestId('job-log')).toHaveTextContent('Step 1 of 2 done Step 2 of 2 done');
    expect(onDone).toHaveBeenCalledTimes(1);
    expect(onDone).toHaveBeenCalledWith('succeeded');
    expect(within(dialog).queryByRole('button', { name: 'Stop following' })).not.toBeInTheDocument();
  });

  it('shows the known status of the run until the first poll answers', () => {
    getActionJobLog.mockReturnValue(new Promise(() => {}));
    renderDialog({ initialStatus: 'failed' });

    expect(screen.getByTestId('job-status')).toHaveTextContent('failed');
  });

  it('says that the job log is gone on 404 and stops polling', async () => {
    vi.useFakeTimers();
    const onDone = vi.fn();
    getActionJobLog.mockRejectedValue(httpError(404, 'job log not found'));
    renderDialog({ onDone });

    await advance(0);
    expect(screen.getByText(JOB_LOG_GONE_TEXT)).toBeInTheDocument();
    expect(JOB_LOG_GONE_TEXT).toBe('The job log is no longer available.');
    await advance(JOB_LOG_POLL_INTERVAL_MS * 10);
    expect(getActionJobLog).toHaveBeenCalledTimes(1);
    expect(onDone).not.toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: 'Stop following' })).not.toBeInTheDocument();
  });

  it('shows the server message for another error', async () => {
    vi.useFakeTimers();
    getActionJobLog.mockRejectedValue(httpError(403, 'You are not allowed to modify this stack instance'));
    renderDialog();

    await advance(0);
    expect(
      screen.getByText('Failed to read the job log (HTTP 403: You are not allowed to modify this stack instance)'),
    ).toBeInTheDocument();
  });

  it('calls onClose on Close and stops polling on unmount', async () => {
    vi.useFakeTimers();
    const onClose = vi.fn();
    const signals: AbortSignal[] = [];
    getActionJobLog.mockImplementation((_i: string, _n: string, _j: string, _o: number, signal: AbortSignal) => {
      signals.push(signal);
      return new Promise(() => {});
    });
    const { unmount } = renderDialog({ onClose });

    await advance(0);
    expect(signals).toHaveLength(1);
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(onClose).toHaveBeenCalledTimes(1);
    unmount();
    expect(signals[0].aborted).toBe(true);
    await advance(JOB_LOG_POLL_INTERVAL_MS * 10);
    expect(getActionJobLog).toHaveBeenCalledTimes(1);
  });

  it('stops polling on Stop following and keeps the log', async () => {
    vi.useFakeTimers();
    getActionJobLog.mockResolvedValue(chunk({ log: 'working\n', next_offset: 8 }));
    renderDialog();

    await advance(0);
    fireEvent.click(screen.getByRole('button', { name: 'Stop following' }));
    expect(screen.getByText('Stopped following the job log. The job can still run on the server.')).toBeInTheDocument();
    expect(screen.getByTestId('job-log')).toHaveTextContent('working');
    await advance(JOB_LOG_POLL_INTERVAL_MS * 5);
    expect(getActionJobLog).toHaveBeenCalledTimes(1);
  });
});
