import { describe, it, expect, vi, afterEach } from 'vitest';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import ActionDialog from '../ActionDialog';
import { JOB_LOG_POLL_INTERVAL_MS } from '../useJobLog';
import { useInstanceActions } from '..';
import { instanceService } from '../../../api/client';
import type { InstanceAction } from '../../../types';

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

const echoAction: InstanceAction = {
  name: 'e2e-echo',
  label: 'E2E echo',
  parameters: [
    { name: 'target', label: 'Target', type: 'string', required: false },
    { name: 'token', label: 'Token', type: 'string', required: false, sensitive: true },
  ],
  has_job_log: false,
  can_invoke: true,
};

const jobAction: InstanceAction = {
  name: 'e2e-job',
  label: 'E2E job',
  parameters: [],
  has_job_log: true,
  can_invoke: true,
};

const advance = async (ms: number) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

/** Page part that uses the hook as the instance page does. */
const ActionsHarness = ({ onRunChanged }: { onRunChanged: () => void }) => {
  const { actions, openAction, dialog } = useInstanceActions('i1', { onRunChanged });
  return (
    <>
      {actions.map((a) => (
        <button key={a.name} type="button" onClick={() => openAction(a)}>{`open ${a.label}`}</button>
      ))}
      {dialog}
    </>
  );
};

describe('ActionDialog', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  it('renders a sensitive string parameter as a password field', async () => {
    const user = userEvent.setup();
    invokeAction.mockResolvedValue({ action: 'e2e-echo', instance_id: 'i1', status_code: 200, result: {} });
    render(<ActionDialog instanceId="i1" action={echoAction} onClose={vi.fn()} />);

    const token = screen.getByLabelText('Token');
    expect(token).toHaveAttribute('type', 'password');
    expect(token).toHaveAttribute('autocomplete', 'new-password');
    expect(screen.getByLabelText('Target')).not.toHaveAttribute('type', 'password');

    await user.type(token, 's3cret');
    await user.click(screen.getByRole('button', { name: 'Run' }));
    await waitFor(() => expect(invokeAction).toHaveBeenCalledWith('i1', 'e2e-echo', { token: 's3cret' }));
  });

  it('calls onInvoked after a successful run', async () => {
    const user = userEvent.setup();
    const onInvoked = vi.fn();
    invokeAction.mockResolvedValue({ action: 'e2e-echo', instance_id: 'i1', status_code: 200, result: {} });
    render(<ActionDialog instanceId="i1" action={echoAction} onClose={vi.fn()} onInvoked={onInvoked} />);

    await user.click(screen.getByRole('button', { name: 'Run' }));
    await screen.findByText('E2E echo returned status 200.');
    expect(onInvoked).toHaveBeenCalledTimes(1);
  });

  it('calls onInvoked after a failed invoke call', async () => {
    const user = userEvent.setup();
    const onInvoked = vi.fn();
    invokeAction.mockRejectedValue(
      Object.assign(new Error('bad'), { response: { status: 502, data: { error: 'subscriber unreachable' } } }),
    );
    render(<ActionDialog instanceId="i1" action={echoAction} onClose={vi.fn()} onInvoked={onInvoked} />);

    await user.click(screen.getByRole('button', { name: 'Run' }));
    await screen.findByText('Failed to run E2E echo (HTTP 502: subscriber unreachable)');
    expect(onInvoked).toHaveBeenCalledTimes(1);
  });

  it('calls onJobDone once when the job log reaches its end', async () => {
    vi.useFakeTimers();
    const onJobDone = vi.fn();
    invokeAction.mockResolvedValue({ action: 'e2e-job', instance_id: 'i1', status_code: 202, job_id: 'job-1', result: {} });
    getActionJobLog.mockResolvedValue({
      action: 'e2e-job', instance_id: 'i1', job_id: 'job-1', status: 'succeeded',
      log: 'done\n', offset: 0, next_offset: 5, done: true, truncated: false,
    });
    render(<ActionDialog instanceId="i1" action={jobAction} onClose={vi.fn()} onJobDone={onJobDone} />);

    fireEvent.click(screen.getByRole('button', { name: 'Run' }));
    await advance(0);
    expect(onJobDone).not.toHaveBeenCalled();
    await advance(JOB_LOG_POLL_INTERVAL_MS);
    expect(screen.getByTestId('job-status')).toHaveTextContent('succeeded');
    expect(onJobDone).toHaveBeenCalledTimes(1);
    await advance(JOB_LOG_POLL_INTERVAL_MS * 3);
    expect(onJobDone).toHaveBeenCalledTimes(1);
  });

  it('useInstanceActions passes onRunChanged to the dialog for the run and the job end', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const onRunChanged = vi.fn();
    listActions.mockResolvedValue({ instance_id: 'i1', can_invoke: true, actions: [jobAction] });
    invokeAction.mockResolvedValue({ action: 'e2e-job', instance_id: 'i1', status_code: 202, job_id: 'job-1', result: {} });
    getActionJobLog.mockResolvedValue({
      action: 'e2e-job', instance_id: 'i1', job_id: 'job-1', status: 'failed',
      log: 'boom\n', offset: 0, next_offset: 5, done: true, truncated: false,
    });
    render(<ActionsHarness onRunChanged={onRunChanged} />);

    fireEvent.click(await screen.findByRole('button', { name: 'open E2E job' }));
    const dialog = screen.getByRole('dialog', { name: 'E2E job' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Run' }));
    await advance(0);
    expect(onRunChanged).toHaveBeenCalledTimes(1);
    await advance(JOB_LOG_POLL_INTERVAL_MS);
    expect(within(dialog).getByTestId('job-status')).toHaveTextContent('failed');
    expect(onRunChanged).toHaveBeenCalledTimes(2);
  });
});
