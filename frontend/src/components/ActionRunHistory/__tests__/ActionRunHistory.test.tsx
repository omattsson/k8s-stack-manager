import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import ActionRunHistory from '../index';
import { instanceService } from '../../../api/client';
import type { ActionJobLog, ActionRun, ActionRunPage, InstanceAction } from '../../../types';

vi.mock('../../../api/client', () => ({
  instanceService: {
    listActionRuns: vi.fn(),
    getActionJobLog: vi.fn(),
  },
}));

type MockFn = ReturnType<typeof vi.fn>;
const listActionRuns = instanceService.listActionRuns as MockFn;
const getActionJobLog = instanceService.getActionJobLog as MockFn;

const minutesAgo = (m: number) => new Date(Date.now() - m * 60 * 1000).toISOString();

const run = (id: string, over: Partial<ActionRun> = {}): ActionRun => ({
  id,
  instance_id: 'i1',
  action: 'e2e-echo',
  action_label: 'E2E echo',
  user_id: 'u-1',
  started_by: 'alice',
  started_at: minutesAgo(5),
  finished_at: minutesAgo(5),
  parameters: {},
  status_code: 200,
  has_job_log: false,
  ...over,
});

const page = (data: ActionRun[], over: Partial<ActionRunPage> = {}): ActionRunPage => ({
  instance_id: 'i1',
  can_view_logs: true,
  data,
  ...over,
});

const httpError = (status: number, message: string) =>
  Object.assign(new Error(message), { response: { status, headers: {}, data: { error: message } } });

const echoAction: InstanceAction = {
  name: 'e2e-echo',
  label: 'E2E echo',
  parameters: [
    { name: 'target', label: 'Target', type: 'string', required: true },
    { name: 'dry_run', label: 'Dry run', type: 'bool', required: false },
    { name: 'mode', label: 'Mode', type: 'enum', required: false, options: ['fast', 'slow'] },
    { name: 'token', label: 'Token', type: 'string', required: false, sensitive: true },
  ],
  has_job_log: false,
  can_invoke: true,
};

const renderHistory = (props: Partial<{ refreshKey: number; hideWhenEmpty: boolean; actions: InstanceAction[] }> = {}) =>
  render(
    <ActionRunHistory
      instanceId="i1"
      refreshKey={props.refreshKey ?? 0}
      hideWhenEmpty={props.hideWhenEmpty ?? false}
      actions={props.actions}
    />,
  );

const rowOf = (id: string) => screen.getByTestId(`action-run-row-${id}`);
const shownRowIds = () =>
  within(screen.getByTestId('action-runs-list'))
    .getAllByRole('listitem')
    .map((el) => el.getAttribute('data-testid'))
    .filter((v): v is string => !!v && v.startsWith('action-run-row-'))
    .map((v) => v.replace('action-run-row-', ''));

describe('ActionRunHistory', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows a spinner while the first page loads', async () => {
    listActionRuns.mockReturnValue(new Promise(() => {}));
    renderHistory();

    expect(screen.getByRole('heading', { name: 'Action runs' })).toBeInTheDocument();
    expect(screen.getByRole('progressbar', { name: 'Loading the action runs' })).toBeInTheDocument();
    await waitFor(() =>
      expect(listActionRuns).toHaveBeenCalledWith('i1', { limit: 10, signal: expect.any(AbortSignal) }),
    );
  });

  it('shows one row per run with the label, the user and the relative time', async () => {
    listActionRuns.mockResolvedValue(page([
      run('r2', { started_at: minutesAgo(5) }),
      run('r1', { action: 'e2e-job', action_label: 'E2E job', started_by: 'bob', started_at: minutesAgo(120) }),
    ]));
    renderHistory();

    const section = await screen.findByTestId('action-runs');
    expect(section).toHaveAttribute('aria-labelledby', 'action-runs-title');
    await screen.findByTestId('action-runs-list');
    expect(shownRowIds()).toEqual(['r2', 'r1']);
    expect(rowOf('r2')).toHaveTextContent('E2E echo');
    expect(rowOf('r2')).toHaveTextContent('alice');
    expect(within(rowOf('r2')).getByText('5m ago')).toBeInTheDocument();
    expect(rowOf('r1')).toHaveTextContent('E2E job');
    expect(rowOf('r1')).toHaveTextContent('bob');
    expect(within(rowOf('r1')).getByText('2h ago')).toBeInTheDocument();
  });

  it('shows the full date in a tooltip of the relative time', async () => {
    const user = userEvent.setup();
    const startedAt = minutesAgo(5);
    listActionRuns.mockResolvedValue(page([run('r1', { started_at: startedAt })]));
    renderHistory();

    await user.hover(await screen.findByText('5m ago'));
    expect(await screen.findByRole('tooltip')).toHaveTextContent(new Date(startedAt).toLocaleString());
  });

  it('shows "Unknown user" when the run has no started_by', async () => {
    listActionRuns.mockResolvedValue(page([run('r1', { started_by: undefined })]));
    renderHistory();

    expect(await screen.findByTestId('action-run-row-r1')).toHaveTextContent('Unknown user');
  });

  it.each([
    ['no answer', { status_code: 0 }, 'Unreachable', 'Error'],
    ['a 409', { status_code: 409 }, 'Failed (409)', 'Error'],
    ['a redirect', { status_code: 302 }, 'Failed (302)', 'Error'],
    ['a 2xx without a job', { status_code: 200 }, 'OK (200)', 'Success'],
    ['a job without a final status', { status_code: 202, job_id: 'job-1' }, 'Job started', 'Info'],
    ['a succeeded job', { status_code: 202, job_id: 'job-1', job_status: 'succeeded' }, 'succeeded', 'Success'],
    ['a running job', { status_code: 202, job_id: 'job-1', job_status: 'running' }, 'running', 'Info'],
    ['a failed job', { status_code: 202, job_id: 'job-1', job_status: 'failed' }, 'failed', 'Error'],
  ])('shows the status chip of %s', async (_name, over, label, color) => {
    listActionRuns.mockResolvedValue(page([run('r1', over as Partial<ActionRun>)]));
    renderHistory();

    const chip = await screen.findByTestId('action-run-status-r1');
    expect(chip).toHaveTextContent(label);
    expect(chip).toHaveClass(`MuiChip-color${color}`);
  });

  it('shows the message of a failed run', async () => {
    listActionRuns.mockResolvedValue(page([run('r1', { status_code: 409, message: 'Refused by the e2e stub' })]));
    renderHistory();

    expect(await screen.findByTestId('action-run-message-r1')).toHaveTextContent('Refused by the e2e stub');
  });

  it('shows the parameters in the declared order, booleans as text and redacted values as sent', async () => {
    listActionRuns.mockResolvedValue(page([
      run('r1', { parameters: { token: '[redacted]', mode: 'fast', dry_run: false, target: 'web' } }),
      run('r2', { parameters: { dry_run: true, target: 'api' } }),
    ]));
    renderHistory({ actions: [echoAction] });

    const params = await screen.findByTestId('action-run-params-r1');
    expect(within(params).getAllByRole('listitem').map((el) => el.textContent)).toEqual([
      'target: web',
      'dry_run: false',
      'mode: fast',
      'token: [redacted]',
    ]);
    expect(within(screen.getByTestId('action-run-params-r2')).getAllByRole('listitem').map((el) => el.textContent))
      .toEqual(['target: api', 'dry_run: true']);
  });

  it('keeps the response key order when the action is not listed', async () => {
    listActionRuns.mockResolvedValue(page([run('r1', { parameters: { b: 'two', a: true } })]));
    renderHistory({ actions: [] });

    const params = await screen.findByTestId('action-run-params-r1');
    expect(within(params).getAllByRole('listitem').map((el) => el.textContent)).toEqual(['b: two', 'a: true']);
  });

  it('has no parameter element for a run without parameters', async () => {
    listActionRuns.mockResolvedValue(page([run('r1')]));
    renderHistory();

    await screen.findByTestId('action-run-row-r1');
    expect(screen.queryByTestId('action-run-params-r1')).not.toBeInTheDocument();
    expect(screen.queryByTestId('action-run-message-r1')).not.toBeInTheDocument();
  });

  it.each([
    [true, true, true],
    [false, true, false],
    [true, false, false],
  ])('shows "Show log" only with can_view_logs=%s and has_job_log=%s', async (canView, hasLog, shown) => {
    listActionRuns.mockResolvedValue(page(
      [run('r1', { status_code: 202, job_id: 'job-1', has_job_log: hasLog })],
      { can_view_logs: canView },
    ));
    renderHistory();

    await screen.findByTestId('action-run-row-r1');
    if (shown) {
      expect(screen.getByTestId('action-run-show-log-r1')).toHaveTextContent('Show log');
    } else {
      expect(screen.queryByTestId('action-run-show-log-r1')).not.toBeInTheDocument();
    }
  });

  it('appends the next page on "Show more" and hides the button on the last page', async () => {
    const user = userEvent.setup();
    const first = Array.from({ length: 10 }, (_, i) => run(`r${20 - i}`));
    listActionRuns
      .mockResolvedValueOnce(page(first, { next_cursor: 'c2' }))
      .mockResolvedValueOnce(page([run('r10')]));
    renderHistory();

    await screen.findByTestId('action-runs-list');
    expect(shownRowIds()).toHaveLength(10);
    await user.click(screen.getByTestId('action-runs-show-more'));

    await waitFor(() => expect(shownRowIds()).toHaveLength(11));
    expect(shownRowIds()[10]).toBe('r10');
    expect(listActionRuns).toHaveBeenLastCalledWith('i1', { limit: 10, cursor: 'c2' });
    expect(screen.queryByTestId('action-runs-show-more')).not.toBeInTheDocument();
  });

  it('shows the empty text when no run is stored', async () => {
    listActionRuns.mockResolvedValue(page([]));
    renderHistory();

    expect(await screen.findByText('No action runs yet.')).toBeInTheDocument();
    expect(screen.queryByTestId('action-runs-list')).not.toBeInTheDocument();
    expect(screen.queryByTestId('action-runs-show-more')).not.toBeInTheDocument();
  });

  it('shows the load error and loads again on Retry', async () => {
    const user = userEvent.setup();
    listActionRuns
      .mockRejectedValueOnce(httpError(500, 'Internal server error'))
      .mockResolvedValueOnce(page([run('r1')]));
    renderHistory();

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Failed to load the action runs (HTTP 500: Internal server error)');
    expect(screen.queryByText('No action runs yet.')).not.toBeInTheDocument();
    await user.click(within(alert).getByRole('button', { name: 'Retry' }));

    expect(await screen.findByTestId('action-run-row-r1')).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(listActionRuns).toHaveBeenCalledTimes(2);
  });

  it('keeps the rows when "Show more" fails and retries the same page', async () => {
    const user = userEvent.setup();
    listActionRuns
      .mockResolvedValueOnce(page([run('r2')], { next_cursor: 'c2' }))
      .mockRejectedValueOnce(httpError(503, 'Service unavailable'))
      .mockResolvedValueOnce(page([run('r1')]));
    renderHistory();

    await user.click(await screen.findByTestId('action-runs-show-more'));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Failed to load more action runs (HTTP 503: Service unavailable)');
    expect(shownRowIds()).toEqual(['r2']);

    await user.click(within(alert).getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(shownRowIds()).toEqual(['r2', 'r1']));
    expect(listActionRuns).toHaveBeenLastCalledWith('i1', { limit: 10, cursor: 'c2' });
  });

  it('loads the first page again on a new refreshKey and merges it into the loaded pages', async () => {
    const user = userEvent.setup();
    listActionRuns
      .mockResolvedValueOnce(page([run('r3'), run('r2', { status_code: 202, job_id: 'job-2' })], { next_cursor: 'c2' }))
      .mockResolvedValueOnce(page([run('r1')], { next_cursor: 'c3' }))
      .mockResolvedValueOnce(page(
        [run('r4'), run('r3'), run('r2', { status_code: 202, job_id: 'job-2', job_status: 'succeeded' })],
        { next_cursor: 'c-new' },
      ))
      .mockResolvedValueOnce(page([run('r0')]));
    const { rerender } = renderHistory();

    await screen.findByTestId('action-runs-list');
    await user.click(screen.getByTestId('action-runs-show-more'));
    await waitFor(() => expect(shownRowIds()).toEqual(['r3', 'r2', 'r1']));
    expect(screen.getByTestId('action-run-status-r2')).toHaveTextContent('Job started');

    rerender(<ActionRunHistory instanceId="i1" refreshKey={1} hideWhenEmpty={false} />);
    await waitFor(() => expect(shownRowIds()).toEqual(['r4', 'r3', 'r2', 'r1']));
    expect(screen.getByTestId('action-run-status-r2')).toHaveTextContent('succeeded');
    expect(listActionRuns).toHaveBeenNthCalledWith(3, 'i1', { limit: 10, signal: expect.any(AbortSignal) });

    // The cursor of the last loaded page stays.
    await user.click(screen.getByTestId('action-runs-show-more'));
    await waitFor(() => expect(shownRowIds()).toEqual(['r4', 'r3', 'r2', 'r1', 'r0']));
    expect(listActionRuns).toHaveBeenLastCalledWith('i1', { limit: 10, cursor: 'c3' });
  });

  it('takes the new cursor of the first page when only the first page was loaded', async () => {
    const user = userEvent.setup();
    listActionRuns
      .mockResolvedValueOnce(page([run('r1')]))
      .mockResolvedValueOnce(page([run('r2'), run('r1')], { next_cursor: 'c2' }))
      .mockResolvedValueOnce(page([]));
    const { rerender } = renderHistory();

    await screen.findByTestId('action-run-row-r1');
    expect(screen.queryByTestId('action-runs-show-more')).not.toBeInTheDocument();
    rerender(<ActionRunHistory instanceId="i1" refreshKey={1} hideWhenEmpty={false} />);

    await user.click(await screen.findByTestId('action-runs-show-more'));
    expect(listActionRuns).toHaveBeenLastCalledWith('i1', { limit: 10, cursor: 'c2' });
  });

  it('hides the section with hideWhenEmpty while it has no rows', async () => {
    listActionRuns.mockResolvedValue(page([]));
    const { container } = renderHistory({ hideWhenEmpty: true });

    await waitFor(() => expect(listActionRuns).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
  });

  it('shows the rows with hideWhenEmpty when runs are stored', async () => {
    listActionRuns.mockResolvedValue(page([run('r1')]));
    renderHistory({ hideWhenEmpty: true });

    expect(await screen.findByTestId('action-run-row-r1')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Action runs' })).toBeInTheDocument();
  });

  it('opens the job log of a past run and patches the row status when the log is done', async () => {
    const user = userEvent.setup();
    listActionRuns.mockResolvedValue(page([
      run('r1', { action: 'e2e-job', action_label: 'E2E job', status_code: 202, job_id: 'job-1', has_job_log: true }),
    ]));
    const done: ActionJobLog = {
      action: 'e2e-job', instance_id: 'i1', job_id: 'job-1', status: 'succeeded',
      log: 'Step 2 of 2 done\n', offset: 0, next_offset: 17, done: true, truncated: false,
    };
    getActionJobLog.mockResolvedValue(done);
    renderHistory();

    expect(await screen.findByTestId('action-run-status-r1')).toHaveTextContent('Job started');
    await user.click(screen.getByTestId('action-run-show-log-r1'));

    const dialog = await screen.findByRole('dialog', { name: 'Job log: E2E job' });
    expect(within(dialog).getByText('Job job-1')).toBeInTheDocument();
    await waitFor(() => expect(within(dialog).getByTestId('job-status')).toHaveTextContent('succeeded'));
    expect(within(dialog).getByTestId('job-log')).toHaveTextContent('Step 2 of 2 done');
    expect(getActionJobLog).toHaveBeenCalledWith('i1', 'e2e-job', 'job-1', 0, expect.any(AbortSignal));

    await user.click(within(dialog).getByRole('button', { name: 'Close' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(screen.getByTestId('action-run-status-r1')).toHaveTextContent('succeeded');
    expect(screen.getByTestId('action-run-status-r1')).toHaveClass('MuiChip-colorSuccess');
    expect(listActionRuns).toHaveBeenCalledTimes(1);
  });
});
