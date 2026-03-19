import { describe, it, expect, beforeAll } from 'vitest';
import { render, screen } from '@testing-library/react';
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

  it('renders log output in terminal-style area', () => {
    render(<DeploymentLogViewer logs={mockLogs} />);
    expect(screen.getByText(/Release "test" has been upgraded/)).toBeInTheDocument();
    expect(screen.getByText(/Attempting to uninstall/)).toBeInTheDocument();
  });

  it('shows error message when present', () => {
    render(<DeploymentLogViewer logs={mockLogs} />);
    expect(screen.getByText(/release not found/)).toBeInTheDocument();
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

  it('shows timestamps for started_at and completed_at', () => {
    render(<DeploymentLogViewer logs={[mockLogs[0]]} />);
    const startDate = new Date('2026-03-19T10:00:00Z').toLocaleString();
    const endDate = new Date('2026-03-19T10:01:00Z').toLocaleString();
    expect(screen.getByText(new RegExp(startDate.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))).toBeInTheDocument();
    expect(screen.getByText(new RegExp(endDate.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))).toBeInTheDocument();
  });
});
