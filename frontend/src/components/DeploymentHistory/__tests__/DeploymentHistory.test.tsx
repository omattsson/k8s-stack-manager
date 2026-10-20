import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import DeploymentHistory from '../index';
import type { DeployHistory } from '../../../hooks/useDeployHistory';
import type { DeploymentLog } from '../../../types';
import { describeDeployRun } from '../../../utils/deployHistory';

vi.mock('../../../api/client', () => ({
  instanceService: {
    getDeployLogEntry: vi.fn(),
  },
}));

import { instanceService } from '../../../api/client';

const getEntry = instanceService.getDeployLogEntry as ReturnType<typeof vi.fn>;

// A past day, so the rows show a relative time ("9mo ago").
const log = (id: string, hour: number, overrides: Partial<DeploymentLog> = {}): DeploymentLog => ({
  id,
  stack_instance_id: 'inst-1',
  action: 'deploy',
  status: 'success',
  branch: 'main',
  started_at: new Date(Date.UTC(2026, 0, 1, hour, 0)).toISOString(),
  completed_at: new Date(Date.UTC(2026, 0, 1, hour, 1)).toISOString(),
  ...overrides,
});

const makeHistory = (overrides: Partial<DeployHistory> = {}): DeployHistory => ({
  rows: [],
  total: overrides.rows?.length ?? 0,
  highlights: undefined,
  hasMore: false,
  filters: {},
  filtersActive: false,
  hasHistory: (overrides.rows?.length ?? 0) > 0,
  loading: false,
  loadingMore: false,
  error: null,
  setFilters: vi.fn(),
  loadMore: vi.fn().mockResolvedValue(undefined),
  refresh: vi.fn().mockResolvedValue(undefined),
  addRunning: vi.fn(),
  ...overrides,
});

const pinnedList = () => screen.getByRole('list', { name: 'Pinned operations' });
const opsList = () => screen.getByRole('list', { name: 'Operations' });
/** Name of the output region of a log. */
const outputName = (l: DeploymentLog) => `Output of ${l.action} ${l.status}, ${new Date(l.started_at).toLocaleString()}`;
/** The row button of a log in a list; its name starts with "<action> <status>". */
const rowButton = (list: HTMLElement, name: RegExp) => within(list).getByRole('button', { name });

beforeEach(() => {
  // Default: the log request does not answer.
  getEntry.mockReset();
  getEntry.mockReturnValue(new Promise(() => {}));
});

afterEach(() => {
  vi.clearAllMocks();
});

describe('DeploymentHistory', () => {
  describe('header and visibility', () => {
    it('renders nothing while the unfiltered history is empty', () => {
      const { container } = render(<DeploymentHistory instanceId="inst-1" history={makeHistory()} />);
      expect(container).toBeEmptyDOMElement();
    });

    it('shows the total of the API in the header', () => {
      render(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [log('d1', 8)], total: 41 })} />);
      expect(screen.getByRole('heading', { name: 'Deployment History (41)' })).toBeInTheDocument();
    });

    it('shows the matching count with a filter, and a message when nothing matches', () => {
      const { rerender } = render(
        <DeploymentHistory
          instanceId="inst-1"
          history={makeHistory({ rows: [log('d1', 8)], total: 12, filters: { status: 'error' }, filtersActive: true, hasHistory: true })}
        />,
      );
      expect(screen.getByRole('heading', { name: 'Deployment History (12 matching)' })).toBeInTheDocument();

      rerender(
        <DeploymentHistory
          instanceId="inst-1"
          history={makeHistory({ rows: [], total: 0, filters: { status: 'error' }, filtersActive: true, hasHistory: true })}
        />,
      );
      expect(screen.getByText('No operations match the filters.')).toBeInTheDocument();
    });

    it('shows a spinner while the first page loads', () => {
      render(
        <DeploymentHistory
          instanceId="inst-1"
          history={makeHistory({ loading: true, filters: { action: 'stop' }, filtersActive: true, hasHistory: true })}
        />,
      );
      expect(screen.getByRole('progressbar', { name: 'Loading the deployment history' })).toBeInTheDocument();
      expect(screen.getByRole('heading', { name: 'Deployment History' })).toBeInTheDocument();
    });

    it('shows the error of the history', () => {
      render(<DeploymentHistory instanceId="inst-1" history={makeHistory({ error: 'Failed to load the deployment history' })} />);
      expect(screen.getByRole('alert')).toHaveTextContent('Failed to load the deployment history');
    });
  });

  describe('rows', () => {
    it('shows time, action, status, branch, duration and user in one row', async () => {
      const user = userEvent.setup();
      const row = log('d1', 8, { started_by: 'alice' });
      render(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [row] })} />);

      const button = rowButton(opsList(), /^deploy success, branch main, started .*, took 1m 0s, by alice$/);
      expect(button).toHaveAttribute('aria-expanded', 'false');
      expect(within(button).getByText('deploy')).toBeInTheDocument();
      expect(within(button).getByText('success')).toBeInTheDocument();
      expect(within(button).getByText('main')).toBeInTheDocument();
      expect(within(button).getByText('1m 0s')).toBeInTheDocument();
      expect(within(button).getByText('alice')).toBeInTheDocument();

      // The relative time has the absolute time in a tooltip.
      await user.hover(within(button).getByText(/ago$/));
      expect(await screen.findByRole('tooltip')).toHaveTextContent(new Date(row.started_at).toLocaleString());
    });

    it('opens nothing by default when no operation runs', () => {
      render(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [log('d2', 9), log('s1', 8, { action: 'stop' })] })} />);
      for (const button of within(opsList()).getAllByRole('button')) {
        expect(button).toHaveAttribute('aria-expanded', 'false');
      }
      expect(getEntry).not.toHaveBeenCalled();
    });

    it('shows a partial status as a warning chip, like the instance status', () => {
      render(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [log('p1', 8, { status: 'partial' })] })} />);
      expect(screen.getByText('partial').closest('.MuiChip-root')).toHaveClass('MuiChip-colorWarning');
    });

    it('marks the current deploy in the list', () => {
      render(
        <DeploymentHistory
          instanceId="inst-1"
          history={makeHistory({ rows: [log('d2', 9), log('d1', 8)] })}
          currentDeployLogId="d1"
        />,
      );
      expect(rowButton(opsList(), /^deploy success, current,/)).toBeInTheDocument();
    });
  });

  describe('pinned operations', () => {
    const running = log('run', 12, { status: 'running', completed_at: undefined });
    const failure = log('fail', 11, { status: 'error', action: 'rollback' });
    const current = log('cur', 10);

    it('pins the running operation, the current deploy and the last failure', () => {
      render(
        <DeploymentHistory
          instanceId="inst-1"
          history={makeHistory({
            rows: [running, failure, current, log('old', 9)],
            highlights: { running, current, latest_failure: failure },
          })}
          currentDeployLogId="cur"
        />,
      );
      const pinned = pinnedList();
      const names = within(pinned).getAllByRole('button').map((b) => b.getAttribute('aria-label'));
      expect(names).toHaveLength(3);
      expect(names[0]).toMatch(/^deploy running, running,/);
      expect(names[1]).toMatch(/^deploy success, current,/);
      expect(names[2]).toMatch(/^rollback error, last failure,/);
      expect(within(pinned).getByText('Running')).toBeInTheDocument();
      expect(within(pinned).getByText('Current')).toBeInTheDocument();
      expect(within(pinned).getByText('Last failure')).toBeInTheDocument();
      // The list keeps every row in time order.
      expect(within(opsList()).getAllByRole('button', { name: /^(deploy|rollback) / })).toHaveLength(4);
    });

    it('shows a log once when two highlights are the same log', () => {
      render(
        <DeploymentHistory
          instanceId="inst-1"
          history={makeHistory({ rows: [current], highlights: { current, latest_failure: current } })}
          currentDeployLogId="cur"
        />,
      );
      expect(within(pinnedList()).getAllByRole('button')).toHaveLength(1);
    });

    it('pins the running log, the current deploy and a newer failure from the rows for an older server', () => {
      render(
        <DeploymentHistory
          instanceId="inst-1"
          history={makeHistory({ rows: [running, failure, current, log('older-fail', 9, { status: 'error' })] })}
          currentDeployLogId="cur"
        />,
      );
      const labels = within(pinnedList()).getAllByRole('button', { name: /^(deploy|rollback) / })
        .map((b) => b.getAttribute('aria-label'));
      expect(labels).toHaveLength(3);
      expect(labels[0]).toMatch(/^deploy running, running,/);
      expect(labels[1]).toMatch(/^deploy success, current,/);
      expect(labels[2]).toMatch(/^rollback error, last failure,/);
    });

    it('treats a partial current deploy like the server: no older failure is pinned', () => {
      const partial = log('part', 12, { status: 'partial' });
      render(
        <DeploymentHistory
          instanceId="inst-1"
          history={makeHistory({ rows: [partial, log('fail-old', 11, { status: 'error' }), current] })}
          currentDeployLogId="part"
        />,
      );
      const labels = within(pinnedList()).getAllByRole('button').map((b) => b.getAttribute('aria-label'));
      expect(labels).toHaveLength(1);
      expect(labels[0]).toMatch(/^deploy partial, current,/);
    });

    it('shows no pinned area without highlights', () => {
      render(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [current], highlights: {} })} />);
      expect(screen.queryByRole('list', { name: 'Pinned operations' })).not.toBeInTheDocument();
    });
  });

  describe('output panel', () => {
    it('loads the output once when a row opens and caches it', async () => {
      const user = userEvent.setup();
      let answer: (value: DeploymentLog) => void = () => {};
      getEntry.mockReturnValue(new Promise((resolve) => { answer = resolve; }));
      render(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [log('d1', 8)] })} />);

      const button = rowButton(opsList(), /^deploy success/);
      await user.click(button);
      expect(button).toHaveAttribute('aria-expanded', 'true');
      const panel = screen.getByRole('region', { name: outputName(log('d1', 8)) });
      expect(within(panel).getByText('Loading the log...')).toBeInTheDocument();
      answer({ ...log('d1', 8), output: 'Release "test" has been upgraded.\nHappy deploying!' });
      expect(await within(panel).findByText(/Release "test" has been upgraded/)).toBeInTheDocument();
      expect(getEntry).toHaveBeenCalledWith('inst-1', 'd1');

      await user.click(button);
      expect(screen.queryByRole('region', { name: outputName(log('d1', 8)) })).not.toBeInTheDocument();
      await user.click(button);
      expect(screen.getByText(/Happy deploying!/)).toBeInTheDocument();
      expect(getEntry).toHaveBeenCalledTimes(1);
    });

    it('gives each output region its own name', async () => {
      const user = userEvent.setup();
      const a = log('a', 9);
      const b = log('b', 8);
      render(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [a, b] })} />);
      for (const button of within(opsList()).getAllByRole('button')) await user.click(button);
      expect(screen.getByRole('region', { name: outputName(a) })).toBeInTheDocument();
      expect(screen.getByRole('region', { name: outputName(b) })).toBeInTheDocument();
    });

    it('shows the error message of a failed operation', async () => {
      const user = userEvent.setup();
      const failed = log('f1', 8, { action: 'stop', status: 'error', error_message: 'release not found' });
      getEntry.mockResolvedValue({ ...failed, output: 'Attempting to uninstall...' });
      render(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [failed] })} />);

      await user.click(rowButton(opsList(), /^stop error/));
      expect(await screen.findByText(/Attempting to uninstall/)).toBeInTheDocument();
      expect(screen.getByText('Error: release not found')).toBeInTheDocument();
    });

    it('shows "No output recorded" for a log without output', async () => {
      const user = userEvent.setup();
      getEntry.mockResolvedValue({ ...log('d1', 8), output: '' });
      render(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [log('d1', 8)] })} />);
      await user.click(rowButton(opsList(), /^deploy success/));
      expect(await screen.findByText('No output recorded')).toBeInTheDocument();
      expect(screen.queryByText(/^Error:/)).not.toBeInTheDocument();
    });

    it('shows an error and the error message of the row when the log does not load', async () => {
      const user = userEvent.setup();
      getEntry.mockRejectedValue(new Error('boom'));
      const failed = log('f1', 8, { status: 'error', error_message: 'helm failed' });
      render(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [failed] })} />);

      await user.click(rowButton(opsList(), /^deploy error/));
      expect(await screen.findByText('Could not load the log output.')).toBeInTheDocument();
      expect(screen.getByText('Error: helm failed')).toBeInTheDocument();
    });

    it('uses the output of a row from an older server without a request', async () => {
      const user = userEvent.setup();
      render(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [{ ...log('d1', 8), output: 'old output' }] })} />);
      await user.click(rowButton(opsList(), /^deploy success/));
      expect(screen.getByText('old output')).toBeInTheDocument();
      expect(getEntry).not.toHaveBeenCalled();
    });
  });

  describe('running operation and streaming', () => {
    const running = log('run', 12, { status: 'running', completed_at: undefined });
    const history = (rows: DeploymentLog[] = [running]) =>
      makeHistory({ rows, highlights: { running } });

    it('opens the running operation by default and shows the streamed lines with LIVE', async () => {
      getEntry.mockResolvedValue({ ...running, output: '' });
      render(
        <DeploymentHistory
          instanceId="inst-1"
          history={history()}
          streamingLines={{ run: ['Installing chart app-core...', 'Waiting for pods...'] }}
        />,
      );
      const pinnedRow = rowButton(pinnedList(), /^deploy running, running, live,/);
      await waitFor(() => expect(pinnedRow).toHaveAttribute('aria-expanded', 'true'));
      const panel = within(pinnedList()).getByRole('region', { name: outputName(running) });
      expect(within(panel).getByText('Installing chart app-core...')).toBeInTheDocument();
      expect(within(panel).getByText('Waiting for pods...')).toBeInTheDocument();
      expect(within(pinnedList()).getByText('LIVE')).toBeInTheDocument();
      // The list row of the same log stays closed.
      expect(rowButton(opsList(), /^deploy running/)).toHaveAttribute('aria-expanded', 'false');
    });

    it('shows the stored output and then the streamed lines once (page reload)', async () => {
      getEntry.mockResolvedValue({ ...running, output: 'Helm install started...\nline 1\n' });
      render(
        <DeploymentHistory
          instanceId="inst-1"
          history={history()}
          streamingLines={{ run: ['line 1', 'line 2'] }}
        />,
      );
      const panel = await within(pinnedList()).findByRole('region', { name: outputName(running) });
      expect(await within(panel).findByText('Helm install started...')).toBeInTheDocument();
      expect(within(panel).getAllByText('line 1')).toHaveLength(1);
      expect(within(panel).getByText('line 2')).toBeInTheDocument();
    });

    it('shows "Waiting for output..." and no LIVE chip without lines', async () => {
      getEntry.mockResolvedValue({ ...running, output: '' });
      render(<DeploymentHistory instanceId="inst-1" history={history()} streamingLines={{ run: [] }} />);
      expect(await within(pinnedList()).findByText('Waiting for output...')).toBeInTheDocument();
      expect(screen.queryByText('LIVE')).not.toBeInTheDocument();
    });

    it('streams even when the log does not load', async () => {
      getEntry.mockRejectedValue(new Error('404'));
      render(<DeploymentHistory instanceId="inst-1" history={history()} streamingLines={{ run: ['line A'] }} />);
      expect(await within(pinnedList()).findByText('line A')).toBeInTheDocument();
      expect(screen.queryByText('Could not load the log output.')).not.toBeInTheDocument();
    });

    it('does not show LIVE for a finished log with stale streamed lines', () => {
      render(
        <DeploymentHistory
          instanceId="inst-1"
          history={makeHistory({ rows: [log('d1', 8)] })}
          streamingLines={{ d1: ['stale line'] }}
        />,
      );
      expect(screen.queryByText('LIVE')).not.toBeInTheDocument();
    });

    it('does not cache a running log and loads the final output when it ends', async () => {
      getEntry
        .mockResolvedValueOnce({ ...running, output: 'part 1' })
        .mockResolvedValueOnce({ ...running, status: 'success', output: 'part 1\ndone' });
      const { rerender } = render(<DeploymentHistory instanceId="inst-1" history={history()} />);
      expect(await within(pinnedList()).findByText('part 1')).toBeInTheDocument();

      // The refresh after the end: the log is now the current deploy, in the same pinned slot.
      const done = { ...running, status: 'success' as const, completed_at: new Date(Date.UTC(2026, 0, 1, 12, 2)).toISOString() };
      rerender(
        <DeploymentHistory
          instanceId="inst-1"
          history={makeHistory({ rows: [done], highlights: { current: done } })}
          currentDeployLogId="run"
        />,
      );
      expect(await within(pinnedList()).findByText(/done/)).toBeInTheDocument();
      expect(getEntry).toHaveBeenCalledTimes(2);
    });

    it('scrolls the streamed output to the newest line', async () => {
      getEntry.mockResolvedValue({ ...running, output: '' });
      const { rerender } = render(
        <DeploymentHistory instanceId="inst-1" history={history()} streamingLines={{ run: ['a'] }} />,
      );
      const panel = await within(pinnedList()).findByRole('region', { name: outputName(running) });
      const terminal = within(panel).getByText('a').parentElement as HTMLElement;
      Object.defineProperty(terminal, 'scrollHeight', { configurable: true, value: 500 });
      rerender(<DeploymentHistory instanceId="inst-1" history={history()} streamingLines={{ run: ['a', 'b'] }} />);
      await waitFor(() => expect(terminal.scrollTop).toBe(500));
    });
  });

  describe('groups', () => {
    const run = [log('d5', 15), log('d4', 14), log('d3', 13)];

    it('folds a run of successful deploys of one branch and opens it', async () => {
      const user = userEvent.setup();
      render(
        <DeploymentHistory
          instanceId="inst-1"
          history={makeHistory({ rows: [log('cur', 16), ...run, log('s1', 12, { action: 'stop' })] })}
          currentDeployLogId="cur"
        />,
      );
      const text = describeDeployRun(run, 'main');
      expect(text).toMatch(/^3 deploys of branch main, /);
      const group = within(opsList()).getByRole('button', { name: text });
      expect(group).toHaveAttribute('aria-expanded', 'false');
      expect(within(opsList()).queryByRole('button', { name: /^deploy success, branch main/ })).not.toBeInTheDocument();

      await user.click(group);
      expect(group).toHaveAttribute('aria-expanded', 'true');
      const inner = screen.getByRole('list', { name: text });
      expect(within(inner).getAllByRole('button', { name: /^deploy success/ })).toHaveLength(3);
      // The current deploy is not in the group.
      expect(rowButton(opsList(), /^deploy success, current,/)).toBeInTheDocument();
    });
  });

  describe('paging', () => {
    it('shows "Show more" with the loaded count and calls loadMore', async () => {
      const user = userEvent.setup();
      const loadMore = vi.fn().mockResolvedValue(undefined);
      render(
        <DeploymentHistory
          instanceId="inst-1"
          history={makeHistory({ rows: [log('d2', 9), log('s1', 8, { action: 'stop' })], total: 41, hasMore: true, loadMore })}
        />,
      );
      expect(screen.getByText('Showing 2 of 41')).toBeInTheDocument();
      await user.click(screen.getByRole('button', { name: 'Show more' }));
      expect(loadMore).toHaveBeenCalledTimes(1);
    });

    it('disables the button while a page loads and hides it on the last page', () => {
      const { rerender } = render(
        <DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [log('d1', 8)], hasMore: true, loadingMore: true })} />,
      );
      expect(screen.getByRole('button', { name: 'Loading...' })).toBeDisabled();
      // A refresh of page 1 runs: its cursor replaces the current one.
      rerender(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [log('d1', 8)], hasMore: true, loading: true })} />);
      expect(screen.getByRole('button', { name: 'Show more' })).toBeDisabled();
      rerender(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [log('d1', 8)] })} />);
      expect(screen.queryByRole('button', { name: 'Show more' })).not.toBeInTheDocument();
      expect(screen.queryByText(/^Showing /)).not.toBeInTheDocument();
    });
  });

  describe('filters', () => {
    it('sets the action filter and keeps the status filter', async () => {
      const user = userEvent.setup();
      const setFilters = vi.fn();
      render(
        <DeploymentHistory
          instanceId="inst-1"
          history={makeHistory({ rows: [log('d1', 8)], filters: { status: 'error' }, filtersActive: true, setFilters })}
        />,
      );
      await user.click(screen.getByRole('combobox', { name: 'Action' }));
      await user.click(await screen.findByRole('option', { name: 'Rollback' }));
      expect(setFilters).toHaveBeenCalledWith({ status: 'error', action: 'rollback' });
    });

    it('maps "Failed" to the status error and "All" to no filter', async () => {
      const user = userEvent.setup();
      const setFilters = vi.fn();
      const { rerender } = render(
        <DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [log('d1', 8)], setFilters })} />,
      );
      await user.click(screen.getByRole('combobox', { name: 'Status' }));
      await user.click(await screen.findByRole('option', { name: 'Failed' }));
      expect(setFilters).toHaveBeenLastCalledWith({ status: 'error' });

      rerender(
        <DeploymentHistory
          instanceId="inst-1"
          history={makeHistory({ rows: [log('d1', 8)], filters: { status: 'error' }, filtersActive: true, setFilters })}
        />,
      );
      await user.click(screen.getByRole('combobox', { name: 'Status' }));
      await user.click(await screen.findByRole('option', { name: 'All' }));
      expect(setFilters).toHaveBeenLastCalledWith({ status: undefined });
    });

    it('lists every action and status', async () => {
      const user = userEvent.setup();
      render(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [log('d1', 8)] })} />);
      await user.click(screen.getByRole('combobox', { name: 'Action' }));
      expect((await screen.findAllByRole('option')).map((o) => o.textContent)).toEqual(['All', 'Deploy', 'Rollback', 'Stop', 'Clean']);
      await user.keyboard('{Escape}');
      await user.click(screen.getByRole('combobox', { name: 'Status' }));
      await waitFor(() => expect(screen.getAllByRole('option').map((o) => o.textContent))
        .toEqual(['All', 'Running', 'Success', 'Partial', 'Failed']));
    });
  });

  describe('rollback action', () => {
    const current = log('cur', 10);
    const older = log('old', 8, { branch: 'feature-x' });

    it('offers "Roll back to this deploy" for an older successful deploy', async () => {
      const user = userEvent.setup();
      const onRollbackTo = vi.fn();
      getEntry.mockImplementation((_i: string, id: string) => Promise.resolve({ ...(id === 'cur' ? current : older), output: 'ok' }));
      render(
        <DeploymentHistory
          instanceId="inst-1"
          history={makeHistory({ rows: [current, older] })}
          currentDeployLogId="cur"
          onRollbackTo={onRollbackTo}
        />,
      );
      await user.click(rowButton(opsList(), /^deploy success, current,/));
      expect(screen.queryByRole('button', { name: 'Roll back to this deploy' })).not.toBeInTheDocument();

      await user.click(rowButton(opsList(), /^deploy success, branch feature-x/));
      await user.click(await screen.findByRole('button', { name: 'Roll back to this deploy' }));
      expect(onRollbackTo).toHaveBeenCalledWith(older);
    });

    it('offers the action on a successful deploy when a newer partial deploy is current', async () => {
      const user = userEvent.setup();
      const onRollbackTo = vi.fn();
      const d1 = log('d1', 8);
      const d2 = log('d2', 9, { status: 'partial' });
      getEntry.mockImplementation((_i: string, id: string) => Promise.resolve({ ...(id === 'd1' ? d1 : d2), output: 'ok' }));
      render(
        <DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [d2, d1] })} currentDeployLogId="d2" onRollbackTo={onRollbackTo} />,
      );
      expect(rowButton(opsList(), /^deploy partial, current,/)).toBeInTheDocument();
      await user.click(rowButton(opsList(), /^deploy success/));
      await user.click(await screen.findByRole('button', { name: 'Roll back to this deploy' }));
      expect(onRollbackTo).toHaveBeenCalledWith(d1);
    });

    it('explains the action when no deploy is marked current', async () => {
      const user = userEvent.setup();
      getEntry.mockResolvedValue({ ...older, output: 'ok' });
      render(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [older] })} onRollbackTo={vi.fn()} />);
      await user.click(rowButton(opsList(), /^deploy success/));
      await user.hover(screen.getByRole('button', { name: 'Roll back to this deploy' }));
      expect(await screen.findByRole('tooltip')).toHaveTextContent(
        'The running values are unknown after a rollback without a target.',
      );
    });

    it('offers no action for failed deploys, other actions or without onRollbackTo', async () => {
      const user = userEvent.setup();
      getEntry.mockImplementation((_i: string, id: string) => Promise.resolve({ ...log(id, 8), output: 'ok' }));
      const rows = [log('x', 9, { status: 'error' }), log('s', 8, { action: 'stop' })];
      const { rerender } = render(
        <DeploymentHistory instanceId="inst-1" history={makeHistory({ rows })} onRollbackTo={vi.fn()} />,
      );
      await user.click(rowButton(opsList(), /^deploy error/));
      await user.click(rowButton(opsList(), /^stop success/));
      await waitFor(() => expect(screen.getAllByText('ok')).toHaveLength(2));
      expect(screen.queryByRole('button', { name: 'Roll back to this deploy' })).not.toBeInTheDocument();

      rerender(<DeploymentHistory instanceId="inst-1" history={makeHistory({ rows: [older] })} />);
      await user.click(rowButton(opsList(), /^deploy success/));
      await waitFor(() => expect(screen.getAllByText('ok').length).toBeGreaterThan(0));
      expect(screen.queryByRole('button', { name: 'Roll back to this deploy' })).not.toBeInTheDocument();
    });
  });
});
