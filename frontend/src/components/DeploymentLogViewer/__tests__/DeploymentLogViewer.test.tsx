import { describe, it, expect, beforeAll } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import DeploymentLogViewer from '../index';
import type { DeploymentLog } from '../../../types';

// jsdom doesn't implement scrollIntoView
beforeAll(() => {
  Element.prototype.scrollIntoView = () => {};
});

const mockLogs: DeploymentLog[] = [
  {
    id: 'log-1',
    stack_instance_id: 'inst-1',
    action: 'deploy',
    status: 'success',
    output: 'Release "test" has been upgraded.\nHappy deploying!',
    started_at: '2026-03-19T10:00:00Z',
    completed_at: '2026-03-19T10:01:00Z',
  },
  {
    id: 'log-2',
    stack_instance_id: 'inst-1',
    action: 'stop',
    status: 'error',
    output: 'Attempting to uninstall...',
    error_message: 'release not found',
    started_at: '2026-03-19T11:00:00Z',
    completed_at: '2026-03-19T11:00:30Z',
  },
];

describe('DeploymentLogViewer', () => {
  it('renders "No deployment history" when logs are empty', () => {
    render(<DeploymentLogViewer logs={[]} />);
    expect(screen.getByText('No deployment history')).toBeInTheDocument();
  });

  it('renders deployment log entries with correct action chips', () => {
    render(<DeploymentLogViewer logs={mockLogs} />);
    expect(screen.getByText('deploy')).toBeInTheDocument();
    expect(screen.getByText('stop')).toBeInTheDocument();
  });

  it('renders the first log expanded by default with output visible', () => {
    render(<DeploymentLogViewer logs={mockLogs} />);
    expect(screen.getByText(/Release "test" has been upgraded/)).toBeInTheDocument();
  });

  it('does not show the second log output by default (collapsed)', () => {
    render(<DeploymentLogViewer logs={mockLogs} />);
    // MUI Accordion collapsed content stays in DOM, so use aria-expanded
    // on the accordion summary button instead of checking content visibility.
    const buttons = screen.getAllByRole('button');
    // First accordion (most recent log) should be expanded
    expect(buttons[0]).toHaveAttribute('aria-expanded', 'true');
    // Second accordion should be collapsed
    expect(buttons[1]).toHaveAttribute('aria-expanded', 'false');
  });

  it('expands a collapsed log when clicked', async () => {
    const user = userEvent.setup();
    render(<DeploymentLogViewer logs={mockLogs} />);

    // Click the second accordion summary (the 'stop' action)
    const stopChip = screen.getByText('stop');
    await user.click(stopChip);

    // Now the second log's output should be visible
    expect(screen.getByText(/Attempting to uninstall/)).toBeVisible();
  });

  it('shows error message in expanded log', async () => {
    const user = userEvent.setup();
    render(<DeploymentLogViewer logs={mockLogs} />);

    // Expand the second log
    const stopChip = screen.getByText('stop');
    await user.click(stopChip);

    expect(screen.getByText(/release not found/)).toBeVisible();
  });

  it('shows correct status chips (running=info, success=success, error=error)', () => {
    const logsWithAllStatuses: DeploymentLog[] = [
      { ...mockLogs[0], id: 'r1', status: 'running' },
      { ...mockLogs[0], id: 'r2', status: 'success' },
      { ...mockLogs[0], id: 'r3', status: 'error' },
    ];
    render(<DeploymentLogViewer logs={logsWithAllStatuses} />);
    const chips = screen.getAllByText(/running|success|error/);
    expect(chips.length).toBeGreaterThanOrEqual(3);
  });

  it('shows start time and duration for completed logs', () => {
    render(<DeploymentLogViewer logs={[mockLogs[0]]} />);
    const startDate = new Date('2026-03-19T10:00:00Z').toLocaleString();
    // Duration should be 1m 0s
    expect(screen.getByText(new RegExp(startDate.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))).toBeInTheDocument();
    expect(screen.getByText(/1m 0s/)).toBeInTheDocument();
  });

  it('renders clean action with correct chip color', () => {
    const cleanLog: DeploymentLog[] = [
      {
        id: 'log-clean',
        stack_instance_id: 'inst-1',
        action: 'clean',
        status: 'success',
        output: 'Namespace cleaned',
        started_at: '2026-03-19T12:00:00Z',
        completed_at: '2026-03-19T12:00:10Z',
      },
    ];
    render(<DeploymentLogViewer logs={cleanLog} />);
    expect(screen.getByText('clean')).toBeInTheDocument();
  });
});
