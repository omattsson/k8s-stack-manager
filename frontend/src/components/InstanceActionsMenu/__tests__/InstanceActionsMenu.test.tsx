import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import InstanceActionsMenu from '..';
import {
  JOB_LOG_CUT_MARKER,
  JOB_LOG_MAX_BACKOFF_MS,
  JOB_LOG_MAX_CHARS,
  JOB_LOG_MAX_POLL_MS,
  JOB_LOG_POLL_INTERVAL_MS,
  cutLogTail,
  retryAfterMs,
} from '../ActionDialog';
import { instanceService } from '../../../api/client';
import type { InstanceAction, ActionJobLog } from '../../../types';

vi.mock('../../../api/client', () => ({
  instanceService: {
    listActions: vi.fn(),
    invokeAction: vi.fn(),
    getActionJobLog: vi.fn(),
  },
}));

type MockFn = ReturnType<typeof vi.fn>;
const listActions = instanceService.listActions as MockFn;
const invokeAction = instanceService.invokeAction as MockFn;
const getActionJobLog = instanceService.getActionJobLog as MockFn;

const seedAction: InstanceAction = {
  name: 'seed-data',
  label: 'seed-data',
  description: 'Load seed data',
  parameters: [],
  has_job_log: false,
  can_invoke: true,
};

const refreshAction: InstanceAction = {
  name: 'refresh-db',
  label: 'Refresh database',
  description: 'Replace the database',
  confirm: 'This replaces the database of the stack.',
  parameters: [
    { name: 'image', label: 'Image', type: 'string', required: false, default: 'golden' },
    { name: 'market', label: 'Market', type: 'enum', required: true, options: ['a', 'b'] },
    { name: 'dry_run', label: 'Dry run', type: 'bool', required: false },
  ],
  has_job_log: true,
  can_invoke: true,
};

const mockList = (actions: InstanceAction[]) =>
  listActions.mockResolvedValue({ instance_id: 'i1', can_invoke: true, actions });

const chunk = (over: Partial<ActionJobLog>): ActionJobLog => ({
  action: 'refresh-db',
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

/** Use fake timers that userEvent can drive. */
const setupFakeTimers = () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  return userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
};

/** Advance the fake clock and flush the promises of the poll. */
const advance = async (ms: number) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

/** Register an async action that returns job-1. */
const startAsyncJob = () => {
  mockList([{ ...refreshAction, parameters: [], confirm: undefined }]);
  invokeAction.mockResolvedValue({
    action: 'refresh-db', instance_id: 'i1', status_code: 202, job_id: 'job-1', result: { job_id: 'job-1' },
  });
};

/** Render the menu, run the async action and return the dialog. */
const runJob = async (user: ReturnType<typeof userEvent.setup>) => {
  render(<InstanceActionsMenu instanceId="i1" />);
  const dialog = await openAction(user, 'Refresh database');
  await user.click(within(dialog).getByRole('button', { name: 'Run' }));
  await within(dialog).findByTestId('job-log');
  return dialog;
};

/**
 * Run the async action while the fake clock moves only through advance(), so
 * wall time cannot fire a poll early (#633). userEvent, waitFor and findBy*
 * wait for a timer of that clock, so this uses fireEvent.
 */
const runJobOnFakeClock = async () => {
  render(<InstanceActionsMenu instanceId="i1" />);
  await advance(0);
  fireEvent.click(screen.getByRole('button', { name: 'Actions' }));
  fireEvent.click(screen.getByRole('menuitem', { name: /Refresh database/ }));
  const dialog = screen.getByRole('dialog');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Run' }));
  await advance(0);
  expect(within(dialog).getByTestId('job-log')).toBeInTheDocument();
  return dialog;
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

/** Delay before each poll: from start, then from the poll before. */
const pollDelays = (times: number[], start: number) =>
  times.map((t, i) => t - (i === 0 ? start : times[i - 1]));

const openAction = async (user: ReturnType<typeof userEvent.setup>, label: string) => {
  await user.click(await screen.findByRole('button', { name: 'Actions' }));
  await user.click(screen.getByRole('menuitem', { name: new RegExp(label) }));
  return screen.findByRole('dialog');
};

describe('InstanceActionsMenu', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('is hidden when no action is registered', async () => {
    mockList([]);
    const { container } = render(<InstanceActionsMenu instanceId="i1" />);
    await waitFor(() => expect(listActions).toHaveBeenCalledWith('i1'));
    expect(container).toBeEmptyDOMElement();
  });

  it('is hidden when the list request fails', async () => {
    listActions.mockRejectedValue(new Error('boom'));
    const { container } = render(<InstanceActionsMenu instanceId="i1" />);
    await waitFor(() => expect(listActions).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
  });

  it('lists the actions and disables those the user may not run', async () => {
    const user = userEvent.setup();
    mockList([refreshAction, { ...seedAction, can_invoke: false }]);
    render(<InstanceActionsMenu instanceId="i1" />);

    await user.click(await screen.findByRole('button', { name: 'Actions' }));
    const items = screen.getAllByRole('menuitem');
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent('Refresh database');
    expect(items[0]).toHaveTextContent('Replace the database');
    expect(items[0]).not.toHaveAttribute('aria-disabled', 'true');
    expect(items[1]).toHaveTextContent('seed-data');
    expect(items[1]).toHaveAttribute('aria-disabled', 'true');
    expect(items[1]).toHaveTextContent(/Only the owner, an admin or a devops user/);
  });

  it('runs an action without parameters and shows the result', async () => {
    const user = userEvent.setup();
    mockList([seedAction]);
    invokeAction.mockResolvedValue({ action: 'seed-data', instance_id: 'i1', status_code: 200, result: { rows: 12 } });
    render(<InstanceActionsMenu instanceId="i1" />);

    const dialog = await openAction(user, 'seed-data');
    expect(within(dialog).getByText('Load seed data')).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));

    await waitFor(() => expect(invokeAction).toHaveBeenCalledWith('i1', 'seed-data', {}));
    expect(await within(dialog).findByText('seed-data returned status 200.')).toBeInTheDocument();
    const resultBox = within(dialog).getByTestId('action-result');
    expect(within(resultBox).getByRole('rowheader', { name: 'Rows' })).toBeInTheDocument();
    expect(within(resultBox).getByText('12')).toBeInTheDocument();
    await user.click(within(resultBox).getByRole('switch', { name: 'Raw JSON' }));
    expect(within(resultBox).getByTestId('json-raw')).toHaveTextContent('"rows": 12');
    expect(getActionJobLog).not.toHaveBeenCalled();
  });

  it('shows the confirmation text and a form built from the parameters', async () => {
    const user = userEvent.setup();
    mockList([refreshAction]);
    invokeAction.mockResolvedValue({ action: 'refresh-db', instance_id: 'i1', status_code: 200, result: { ok: true } });
    render(<InstanceActionsMenu instanceId="i1" />);

    const dialog = await openAction(user, 'Refresh database');
    expect(within(dialog).getByText('This replaces the database of the stack.')).toBeInTheDocument();
    expect(within(dialog).getByLabelText('Image')).toHaveValue('golden');
    const run = within(dialog).getByRole('button', { name: 'Run' });
    expect(run).toBeDisabled(); // market is required

    await user.click(within(dialog).getByRole('combobox'));
    await user.click(await screen.findByRole('option', { name: 'b' }));
    await user.click(within(dialog).getByRole('switch', { name: 'Dry run' }));
    await user.clear(within(dialog).getByLabelText('Image'));
    await user.type(within(dialog).getByLabelText('Image'), 'nightly');
    expect(run).toBeEnabled();
    await user.click(run);

    await waitFor(() =>
      expect(invokeAction).toHaveBeenCalledWith('i1', 'refresh-db', { image: 'nightly', market: 'b', dry_run: true }),
    );
  });

  it('shows a non-2xx status of the subscriber as an error', async () => {
    const user = userEvent.setup();
    mockList([seedAction]);
    invokeAction.mockResolvedValue({ action: 'seed-data', instance_id: 'i1', status_code: 409, result: { error: 'busy' } });
    render(<InstanceActionsMenu instanceId="i1" />);

    const dialog = await openAction(user, 'seed-data');
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));

    expect(await within(dialog).findByText('seed-data failed: the action returned status 409.')).toBeInTheDocument();
    expect(within(dialog).getByTestId('action-message')).toHaveTextContent('busy');
    expect(within(dialog).getByTestId('action-result')).toHaveTextContent('busy');
  });

  it('prefers the envelope message over the result fields for a non-2xx result', async () => {
    const user = userEvent.setup();
    mockList([seedAction]);
    invokeAction.mockResolvedValue({
      action: 'seed-data', instance_id: 'i1', status_code: 409,
      message: 'A refresh-db job already runs for this stack',
      result: { error: 'conflict' },
    });
    render(<InstanceActionsMenu instanceId="i1" />);

    const dialog = await openAction(user, 'seed-data');
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));

    expect(await within(dialog).findByTestId('action-message')).toHaveTextContent('A refresh-db job already runs for this stack');
    expect(within(dialog).getByTestId('action-message')).not.toHaveTextContent('conflict');
  });

  it('ignores the envelope message for a 2xx result', async () => {
    const user = userEvent.setup();
    mockList([seedAction]);
    invokeAction.mockResolvedValue({ action: 'seed-data', instance_id: 'i1', status_code: 200, message: 'x', result: { ok: true } });
    render(<InstanceActionsMenu instanceId="i1" />);

    const dialog = await openAction(user, 'seed-data');
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));

    expect(await within(dialog).findByText('seed-data returned status 200.')).toBeInTheDocument();
    expect(within(dialog).queryByTestId('action-message')).not.toBeInTheDocument();
  });

  it('shows the message field of a 409 result in the error alert', async () => {
    const user = userEvent.setup();
    mockList([seedAction]);
    invokeAction.mockResolvedValue({
      action: 'seed-data', instance_id: 'i1', status_code: 409,
      result: { message: 'A refresh-db job already runs for this stack', job_id: 'job-9' },
    });
    render(<InstanceActionsMenu instanceId="i1" />);

    const dialog = await openAction(user, 'seed-data');
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));

    expect(await within(dialog).findByTestId('action-status')).toHaveTextContent('A refresh-db job already runs for this stack');
  });

  it('shows the API error when the invoke call fails', async () => {
    const user = userEvent.setup();
    mockList([seedAction]);
    invokeAction.mockRejectedValue(httpError(403, 'You are not allowed to modify this stack instance'));
    render(<InstanceActionsMenu instanceId="i1" />);

    const dialog = await openAction(user, 'seed-data');
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));

    expect(
      await within(dialog).findByText('Failed to run seed-data (HTTP 403: You are not allowed to modify this stack instance)'),
    ).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Run' })).toBeEnabled();
  });

  it('polls the job log every 3 s with the next offset until the job is done', async () => {
    vi.useFakeTimers();
    startAsyncJob();
    const times = answerPolls(
      chunk({ log: 'step 1\n', next_offset: 7, truncated: true }),
      chunk({ log: 'step 2\n', offset: 7, next_offset: 14 }),
      chunk({ log: 'step 3\n', offset: 14, next_offset: 21, status: 'succeeded', done: true }),
    );
    const dialog = await runJobOnFakeClock();
    const start = Date.now();

    // The first poll waits one interval, so the subscriber can create the log.
    expect(getActionJobLog).not.toHaveBeenCalled();
    await advance(JOB_LOG_POLL_INTERVAL_MS);
    expect(getActionJobLog).toHaveBeenCalledTimes(1);
    // A truncated chunk is followed by an immediate poll (the fake clock runs a 0 ms timer 1 ms later).
    await advance(1);
    expect(getActionJobLog).toHaveBeenCalledTimes(2);
    await advance(JOB_LOG_POLL_INTERVAL_MS);
    expect(within(dialog).getByTestId('job-status')).toHaveTextContent('succeeded');
    expect(pollDelays(times, start)).toEqual([JOB_LOG_POLL_INTERVAL_MS, 1, JOB_LOG_POLL_INTERVAL_MS]);

    expect(within(dialog).getByTestId('job-log')).toHaveTextContent('step 1 step 2 step 3');
    expect(getActionJobLog.mock.calls.map((c) => c[3])).toEqual([0, 7, 14]);
    expect(within(dialog).queryByLabelText('Job running')).not.toBeInTheDocument();
    await advance(JOB_LOG_POLL_INTERVAL_MS * 5);
    expect(getActionJobLog).toHaveBeenCalledTimes(3);
  });

  it('does not poll when the action has no job log', async () => {
    const user = userEvent.setup();
    mockList([seedAction]);
    invokeAction.mockResolvedValue({ action: 'seed-data', instance_id: 'i1', status_code: 202, result: { job_id: 'job-1' } });
    render(<InstanceActionsMenu instanceId="i1" />);

    const dialog = await openAction(user, 'seed-data');
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));
    await within(dialog).findByText('seed-data returned status 202.');
    expect(within(dialog).queryByTestId('job-log')).not.toBeInTheDocument();
    expect(getActionJobLog).not.toHaveBeenCalled();
  });

  it('stops polling when the dialog closes', async () => {
    const user = setupFakeTimers();
    startAsyncJob();
    getActionJobLog.mockResolvedValue(chunk({ log: 'working\n', next_offset: 8 }));
    const dialog = await runJob(user);

    await advance(JOB_LOG_POLL_INTERVAL_MS);
    await waitFor(() => expect(within(dialog).getByTestId('job-log')).toHaveTextContent('working'));
    await advance(JOB_LOG_POLL_INTERVAL_MS);
    await waitFor(() => expect(getActionJobLog).toHaveBeenCalledTimes(2));
    expect(getActionJobLog).toHaveBeenLastCalledWith('i1', 'refresh-db', 'job-1', 8, expect.any(AbortSignal));

    await user.click(within(dialog).getByRole('button', { name: 'Close' }));
    await advance(JOB_LOG_POLL_INTERVAL_MS * 5);
    expect(getActionJobLog).toHaveBeenCalledTimes(2);
  });

  it('backs off on 429 with Retry-After and keeps polling', async () => {
    vi.useFakeTimers();
    startAsyncJob();
    const times = answerPolls(
      httpError(429, 'Rate limit exceeded', { 'retry-after': '10' }),
      chunk({ log: 'done\n', next_offset: 5, status: 'succeeded', done: true }),
    );
    const dialog = await runJobOnFakeClock();
    const start = Date.now();

    await advance(JOB_LOG_POLL_INTERVAL_MS);
    expect(within(dialog).getByTestId('job-waiting')).toHaveTextContent('Waiting…');
    await advance(10000);
    expect(within(dialog).getByTestId('job-status')).toHaveTextContent('succeeded');
    expect(pollDelays(times, start)).toEqual([JOB_LOG_POLL_INTERVAL_MS, 10000]);
    expect(within(dialog).queryByTestId('job-waiting')).not.toBeInTheDocument();
    expect(within(dialog).queryByRole('alert', { name: /Failed/ })).not.toBeInTheDocument();
  });

  it('doubles the delay on network errors up to 30 s and keeps polling', async () => {
    vi.useFakeTimers();
    startAsyncJob();
    const times = answerPolls(Object.assign(new Error('Network Error'), { code: 'ERR_NETWORK' }));
    const dialog = await runJobOnFakeClock();
    const start = Date.now();

    // The first poll after 3 s, then the doubled delay after each failure.
    const planned = [JOB_LOG_POLL_INTERVAL_MS, 6000, 12000, 24000, JOB_LOG_MAX_BACKOFF_MS, JOB_LOG_MAX_BACKOFF_MS];
    for (const [i, delay] of planned.entries()) {
      await advance(delay);
      expect(getActionJobLog).toHaveBeenCalledTimes(i + 1);
      expect(within(dialog).getByTestId('job-waiting')).toBeInTheDocument();
    }
    expect(pollDelays(times, start)).toEqual(planned);
    expect(within(dialog).queryByText(/Failed to read the job log/)).not.toBeInTheDocument();
  });

  it('keeps only the last 1 MiB of log text and marks the cut', async () => {
    const user = setupFakeTimers();
    startAsyncJob();
    getActionJobLog
      .mockResolvedValueOnce(chunk({ log: 'HEAD' + 'x'.repeat(JOB_LOG_MAX_CHARS), next_offset: JOB_LOG_MAX_CHARS + 4, truncated: true }))
      .mockResolvedValueOnce(chunk({ log: 'TAIL\n', next_offset: JOB_LOG_MAX_CHARS + 9, status: 'succeeded', done: true }));
    const dialog = await runJob(user);

    await advance(JOB_LOG_POLL_INTERVAL_MS);
    await waitFor(() => expect(within(dialog).getByTestId('job-status')).toHaveTextContent('succeeded'));
    const text = within(dialog).getByTestId('job-log').textContent ?? '';
    expect(text.startsWith(JOB_LOG_CUT_MARKER)).toBe(true);
    expect(text.endsWith('TAIL\n')).toBe(true);
    expect(text).not.toContain('HEAD');
    expect(text.length).toBe(JOB_LOG_CUT_MARKER.length + JOB_LOG_MAX_CHARS);
  });

  it('cuts the kept log after a line end, so no partial first line remains', async () => {
    const user = setupFakeTimers();
    startAsyncJob();
    const line = 'y'.repeat(99) + '\n';
    const lines = Array.from({ length: Math.ceil(JOB_LOG_MAX_CHARS / line.length) + 3 }, (_, i) =>
      `${String(i).padStart(6, '0')}${line.slice(6)}`).join('');
    getActionJobLog.mockResolvedValueOnce(
      chunk({ log: lines, next_offset: lines.length, status: 'succeeded', done: true }),
    );
    const dialog = await runJob(user);

    await advance(JOB_LOG_POLL_INTERVAL_MS);
    await waitFor(() => expect(within(dialog).getByTestId('job-status')).toHaveTextContent('succeeded'));
    const text = within(dialog).getByTestId('job-log').textContent ?? '';
    expect(text.startsWith(JOB_LOG_CUT_MARKER)).toBe(true);
    const kept = text.slice(JOB_LOG_CUT_MARKER.length);
    expect(kept.length).toBeLessThanOrEqual(JOB_LOG_MAX_CHARS);
    expect(kept.length % line.length).toBe(0);
    expect(kept.split('\n').slice(0, -1).every((l) => /^\d{6}y{93}$/.test(l))).toBe(true);
    expect(kept.endsWith(lines.slice(-line.length))).toBe(true);
  });

  it('aborts the request in flight on Stop following', async () => {
    const user = setupFakeTimers();
    startAsyncJob();
    const signals: AbortSignal[] = [];
    getActionJobLog.mockImplementation((_i: string, _n: string, _j: string, _o: number, signal: AbortSignal) => {
      signals.push(signal);
      return new Promise(() => {});
    });
    const dialog = await runJob(user);

    await advance(JOB_LOG_POLL_INTERVAL_MS);
    await waitFor(() => expect(signals).toHaveLength(1));
    expect(signals[0].aborted).toBe(false);
    await user.click(within(dialog).getByRole('button', { name: 'Stop following' }));
    expect(signals[0].aborted).toBe(true);
  });

  it('aborts the request in flight at the 60-minute limit', async () => {
    const user = setupFakeTimers();
    startAsyncJob();
    const signals: AbortSignal[] = [];
    getActionJobLog.mockImplementation((_i: string, _n: string, _j: string, _o: number, signal: AbortSignal) => {
      signals.push(signal);
      return new Promise(() => {});
    });
    const dialog = await runJob(user);

    await advance(JOB_LOG_POLL_INTERVAL_MS);
    await waitFor(() => expect(signals).toHaveLength(1));
    await advance(JOB_LOG_MAX_POLL_MS);
    expect(signals[0].aborted).toBe(true);
    expect(
      within(dialog).getByText('Stopped following the job log after 60 minutes. The job can still run on the server.'),
    ).toBeInTheDocument();
  });

  it('aborts the request in flight when the dialog closes', async () => {
    const user = setupFakeTimers();
    startAsyncJob();
    const signals: AbortSignal[] = [];
    getActionJobLog.mockImplementation((_i: string, _n: string, _j: string, _o: number, signal: AbortSignal) => {
      signals.push(signal);
      return new Promise(() => {});
    });
    const dialog = await runJob(user);

    await advance(JOB_LOG_POLL_INTERVAL_MS);
    await waitFor(() => expect(signals).toHaveLength(1));
    await user.click(within(dialog).getByRole('button', { name: 'Close' }));
    expect(signals[0].aborted).toBe(true);
  });

  it('aborts the request in flight on unmount', async () => {
    const user = setupFakeTimers();
    startAsyncJob();
    const signals: AbortSignal[] = [];
    getActionJobLog.mockImplementation((_i: string, _n: string, _j: string, _o: number, signal: AbortSignal) => {
      signals.push(signal);
      return new Promise(() => {});
    });
    render(<InstanceActionsMenu instanceId="i1" />);
    const dialog = await openAction(user, 'Refresh database');
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));
    await within(dialog).findByTestId('job-log');
    await advance(JOB_LOG_POLL_INTERVAL_MS);
    await waitFor(() => expect(signals).toHaveLength(1));
    cleanup();
    expect(signals[0].aborted).toBe(true);
  });

  it('stops following on Stop following and keeps the log', async () => {
    const user = setupFakeTimers();
    startAsyncJob();
    getActionJobLog.mockResolvedValue(chunk({ log: 'working\n', next_offset: 8 }));
    const dialog = await runJob(user);

    await advance(JOB_LOG_POLL_INTERVAL_MS);
    await waitFor(() => expect(within(dialog).getByTestId('job-log')).toHaveTextContent('working'));
    await user.click(within(dialog).getByRole('button', { name: 'Stop following' }));

    expect(within(dialog).getByText(/Stopped following the job log\. The job can still run on the server\./)).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: 'Stop following' })).not.toBeInTheDocument();
    expect(within(dialog).queryByLabelText('Job running')).not.toBeInTheDocument();
    expect(within(dialog).getByTestId('job-log')).toHaveTextContent('working');
    const calls = getActionJobLog.mock.calls.length;
    await advance(JOB_LOG_POLL_INTERVAL_MS * 5);
    expect(getActionJobLog).toHaveBeenCalledTimes(calls);
  });

  it('stops following after 60 minutes with a message', async () => {
    const user = setupFakeTimers();
    startAsyncJob();
    getActionJobLog.mockResolvedValue(chunk({ log: '.', next_offset: 1 }));
    const dialog = await runJob(user);

    await advance(JOB_LOG_MAX_POLL_MS + JOB_LOG_POLL_INTERVAL_MS);
    expect(
      await within(dialog).findByText('Stopped following the job log after 60 minutes. The job can still run on the server.'),
    ).toBeInTheDocument();
    const calls = getActionJobLog.mock.calls.length;
    expect(calls).toBeGreaterThan(1000);
    expect(calls).toBeLessThanOrEqual(JOB_LOG_MAX_POLL_MS / JOB_LOG_POLL_INTERVAL_MS);
    await advance(JOB_LOG_POLL_INTERVAL_MS * 10);
    expect(getActionJobLog).toHaveBeenCalledTimes(calls);
    expect(within(dialog).queryByRole('button', { name: 'Stop following' })).not.toBeInTheDocument();
  });

  it.each([
    [401, 'Authentication required'],
    [403, 'You are not allowed to modify this stack instance'],
    [404, 'job log not found'],
  ])('stops polling on %i and shows the message', async (status, message) => {
    const user = setupFakeTimers();
    startAsyncJob();
    getActionJobLog.mockRejectedValue(httpError(status, message));
    const dialog = await runJob(user);

    await advance(JOB_LOG_POLL_INTERVAL_MS);
    expect(
      await within(dialog).findByText(`Failed to read the job log (HTTP ${status}: ${message})`),
    ).toBeInTheDocument();
    await advance(JOB_LOG_MAX_BACKOFF_MS * 2);
    expect(getActionJobLog).toHaveBeenCalledTimes(1);
    expect(within(dialog).queryByTestId('job-waiting')).not.toBeInTheDocument();
  });
});

describe('retryAfterMs', () => {
  const now = Date.parse('2026-10-09T12:00:00Z');
  it.each([
    ['seconds', { 'retry-after': '7' }, 7000],
    ['capital header name', { 'Retry-After': '2' }, 2000],
    ['HTTP date', { 'retry-after': 'Fri, 09 Oct 2026 12:00:20 GMT' }, 20000],
    ['date in the past', { 'retry-after': 'Fri, 09 Oct 2026 11:00:00 GMT' }, 0],
    ['capped at 5 minutes', { 'retry-after': '3600' }, 300000],
    ['invalid', { 'retry-after': 'soon' }, undefined],
    ['absent', {}, undefined],
  ])('%s', (_name, headers, want) => {
    expect(retryAfterMs({ response: { status: 429, headers } }, now)).toBe(want);
  });

  it('reads AxiosHeaders through get()', () => {
    const headers = { get: (k: string) => (k === 'retry-after' ? '4' : undefined) };
    expect(retryAfterMs({ response: { status: 429, headers } }, now)).toBe(4000);
  });

  it('returns undefined without a response', () => {
    expect(retryAfterMs(new Error('Network Error'), now)).toBeUndefined();
  });
});

describe('cutLogTail', () => {
  it.each([
    ['short text unchanged', 'abc', 10, 'abc'],
    ['cut after the next line end', 'aaaa\nbbbb\ncc', 6, 'cc'],
    ['cut right after a line end at the cut', 'aaaa\nbbbb\ncc', 8, 'bbbb\ncc'],
    ['line end as last character keeps the tail', 'aaaaaaaa\n', 5, 'aaaa\n'],
    ['no line end keeps the tail', 'abcdefgh', 3, 'fgh'],
    ['drops a lone low surrogate', 'ab\u{1F600}cd', 3, 'cd'],
    ['keeps a whole surrogate pair', 'ab\u{1F600}cd', 4, '\u{1F600}cd'],
  ])('%s', (_name, text, max, want) => {
    expect(cutLogTail(text, max)).toBe(want);
  });
});
