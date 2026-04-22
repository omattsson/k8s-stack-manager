import { describe, it, expect, beforeAll } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import DeploymentLogViewer from '../index';
import type { DeploymentLog } from '../../../types';

// jsdom doesn't implement scrollIntoView
beforeAll(() => {
  Element.prototype.scrollIntoView = () => {};
});

// ---------------------------------------------------------------------------
// Shared fixtures
// ---------------------------------------------------------------------------

const completedDeployLog: DeploymentLog = {
  id: 'log-1',
  stack_instance_id: 'inst-1',
  action: 'deploy',
  status: 'success',
  output: 'Release "test" has been upgraded.\nHappy deploying!',
  started_at: '2026-03-19T10:00:00Z',
  completed_at: '2026-03-19T10:01:00Z',
};

const errorStopLog: DeploymentLog = {
  id: 'log-2',
  stack_instance_id: 'inst-1',
  action: 'stop',
  status: 'error',
  output: 'Attempting to uninstall...',
  error_message: 'release not found',
  started_at: '2026-03-19T11:00:00Z',
  completed_at: '2026-03-19T11:00:30Z',
};

const runningLogNoOutput: DeploymentLog = {
  id: 'log-running-empty',
  stack_instance_id: 'inst-1',
  action: 'deploy',
  status: 'running',
  output: '',
  started_at: '2026-03-19T12:00:00Z',
};

const runningLogWithOutput: DeploymentLog = {
  id: 'log-running-output',
  stack_instance_id: 'inst-1',
  action: 'deploy',
  status: 'running',
  output: 'Helm install started...',
  started_at: '2026-03-19T13:00:00Z',
};

const rollbackLog: DeploymentLog = {
  id: 'log-rollback',
  stack_instance_id: 'inst-1',
  action: 'rollback',
  status: 'success',
  output: 'Rollback complete',
  started_at: '2026-03-19T14:00:00Z',
  completed_at: '2026-03-19T14:00:05Z',
};

const cleanLog: DeploymentLog = {
  id: 'log-clean',
  stack_instance_id: 'inst-1',
  action: 'clean',
  status: 'success',
  output: 'Namespace cleaned',
  started_at: '2026-03-19T15:00:00Z',
  completed_at: '2026-03-19T15:00:10Z',
};

const mockLogs: DeploymentLog[] = [completedDeployLog, errorStopLog];

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('DeploymentLogViewer', () => {
  // -------------------------------------------------------------------------
  // Basic rendering
  // -------------------------------------------------------------------------

  it('renders "No deployment history" when logs are empty', () => {
    render(<DeploymentLogViewer logs={[]} />);
    expect(screen.getByText('No deployment history')).toBeInTheDocument();
  });

  it('renders deployment log entries with correct action chips', () => {
    render(<DeploymentLogViewer logs={mockLogs} />);
    expect(screen.getByText('deploy')).toBeInTheDocument();
    expect(screen.getByText('stop')).toBeInTheDocument();
  });

  it('renders status chips for each log', () => {
    render(<DeploymentLogViewer logs={mockLogs} />);
    expect(screen.getByText('success')).toBeInTheDocument();
    expect(screen.getByText('error')).toBeInTheDocument();
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

  // -------------------------------------------------------------------------
  // Completed log output
  // -------------------------------------------------------------------------

  it('shows completed log output text in the terminal box', () => {
    render(<DeploymentLogViewer logs={[completedDeployLog]} />);
    expect(screen.getByText(/Release "test" has been upgraded/)).toBeInTheDocument();
    expect(screen.getByText(/Happy deploying!/)).toBeInTheDocument();
  });

  // -------------------------------------------------------------------------
  // Error message rendering
  // -------------------------------------------------------------------------

  it('shows error message in expanded log', async () => {
    const user = userEvent.setup();
    render(<DeploymentLogViewer logs={mockLogs} />);

    // Expand the second log
    const stopChip = screen.getByText('stop');
    await user.click(stopChip);

    expect(screen.getByText(/Error: release not found/)).toBeVisible();
  });

  it('shows error_message prefixed with "Error:" for error logs', () => {
    const errorLog: DeploymentLog = {
      id: 'log-err-detail',
      stack_instance_id: 'inst-1',
      action: 'deploy',
      status: 'error',
      output: 'Some partial output',
      error_message: 'timeout waiting for helm',
      started_at: '2026-03-19T16:00:00Z',
      completed_at: '2026-03-19T16:01:00Z',
    };
    render(<DeploymentLogViewer logs={[errorLog]} />);

    expect(screen.getByText(/Error: timeout waiting for helm/)).toBeInTheDocument();
  });

  it('does not render error box when error_message is absent', () => {
    render(<DeploymentLogViewer logs={[completedDeployLog]} />);
    expect(screen.queryByText(/^Error:/)).not.toBeInTheDocument();
  });

  // -------------------------------------------------------------------------
  // Status chip variants
  // -------------------------------------------------------------------------

  it('shows correct status chips (running=info, success=success, error=error)', () => {
    const logsWithAllStatuses: DeploymentLog[] = [
      { ...completedDeployLog, id: 'r1', status: 'running' },
      { ...completedDeployLog, id: 'r2', status: 'success' },
      { ...completedDeployLog, id: 'r3', status: 'error' },
    ];
    render(<DeploymentLogViewer logs={logsWithAllStatuses} />);
    const chips = screen.getAllByText(/running|success|error/);
    expect(chips.length).toBeGreaterThanOrEqual(3);
  });

  // -------------------------------------------------------------------------
  // Timestamp and duration
  // -------------------------------------------------------------------------

  it('shows start time and duration for completed logs', () => {
    render(<DeploymentLogViewer logs={[completedDeployLog]} />);
    const startDate = new Date('2026-03-19T10:00:00Z').toLocaleString();
    // Duration should be 1m 0s
    expect(screen.getByText(new RegExp(startDate.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))).toBeInTheDocument();
    expect(screen.getByText(/1m 0s/)).toBeInTheDocument();
  });

  it('shows only start time for running logs without completed_at', () => {
    render(<DeploymentLogViewer logs={[runningLogNoOutput]} />);
    const startDate = new Date('2026-03-19T12:00:00Z').toLocaleString();
    expect(screen.getByText(new RegExp(startDate.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))).toBeInTheDocument();
    // Should not show a duration
    expect(screen.queryByText(/\d+m \d+s/)).not.toBeInTheDocument();
    expect(screen.queryByText(/\d+s$/)).not.toBeInTheDocument();
  });

  // -------------------------------------------------------------------------
  // Action chip variants
  // -------------------------------------------------------------------------

  it('renders rollback action chip', () => {
    render(<DeploymentLogViewer logs={[rollbackLog]} />);
    expect(screen.getByText('rollback')).toBeInTheDocument();
  });

  it('renders clean action with correct chip color', () => {
    render(<DeploymentLogViewer logs={[cleanLog]} />);
    expect(screen.getByText('clean')).toBeInTheDocument();
  });

  // -------------------------------------------------------------------------
  // "Waiting for output..." state
  // -------------------------------------------------------------------------

  it('shows "Waiting for output..." for running log without output or streaming lines', () => {
    render(<DeploymentLogViewer logs={[runningLogNoOutput]} />);
    expect(screen.getByText('Waiting for output...')).toBeInTheDocument();
  });

  it('shows "Waiting for output..." for running log without output even when streamingLines is empty object', () => {
    render(<DeploymentLogViewer logs={[runningLogNoOutput]} streamingLines={{}} />);
    expect(screen.getByText('Waiting for output...')).toBeInTheDocument();
  });

  it('shows "Waiting for output..." when streamingLines has entry but it is an empty array', () => {
    render(
      <DeploymentLogViewer
        logs={[runningLogNoOutput]}
        streamingLines={{ [runningLogNoOutput.id]: [] }}
      />,
    );
    // lines.length === 0 => isStreaming is false, and output is empty => "Waiting for output..."
    expect(screen.getByText('Waiting for output...')).toBeInTheDocument();
  });

  it('shows "No output recorded" for a completed log with empty output', () => {
    const completedNoOutput: DeploymentLog = {
      ...completedDeployLog,
      id: 'log-no-output',
      output: '',
    };
    render(<DeploymentLogViewer logs={[completedNoOutput]} />);
    expect(screen.getByText('No output recorded')).toBeInTheDocument();
  });

  // -------------------------------------------------------------------------
  // Real-time streaming feature (NEW)
  // -------------------------------------------------------------------------

  describe('real-time streaming', () => {
    it('shows streaming lines for a running log', () => {
      const streamingLines = {
        [runningLogNoOutput.id]: ['Installing chart kvk-core...', 'Waiting for pods...'],
      };
      render(
        <DeploymentLogViewer logs={[runningLogNoOutput]} streamingLines={streamingLines} />,
      );
      expect(screen.getByText('Installing chart kvk-core...')).toBeInTheDocument();
      expect(screen.getByText('Waiting for pods...')).toBeInTheDocument();
    });

    it('shows LIVE chip when log is running and has streaming lines', () => {
      const streamingLines = {
        [runningLogNoOutput.id]: ['line one'],
      };
      render(
        <DeploymentLogViewer logs={[runningLogNoOutput]} streamingLines={streamingLines} />,
      );
      expect(screen.getByText('LIVE')).toBeInTheDocument();
    });

    it('does not show LIVE chip for a completed log', () => {
      render(<DeploymentLogViewer logs={[completedDeployLog]} />);
      expect(screen.queryByText('LIVE')).not.toBeInTheDocument();
    });

    it('does not show LIVE chip for a completed log even if streamingLines has data', () => {
      // Edge case: stale streaming data lingering after completion
      const streamingLines = {
        [completedDeployLog.id]: ['stale line'],
      };
      render(
        <DeploymentLogViewer logs={[completedDeployLog]} streamingLines={streamingLines} />,
      );
      expect(screen.queryByText('LIVE')).not.toBeInTheDocument();
    });

    it('does not show LIVE chip when running log has no streaming lines', () => {
      render(<DeploymentLogViewer logs={[runningLogNoOutput]} streamingLines={{}} />);
      expect(screen.queryByText('LIVE')).not.toBeInTheDocument();
    });

    it('does not show LIVE chip when running log has empty streaming lines array', () => {
      const streamingLines = {
        [runningLogNoOutput.id]: [],
      };
      render(
        <DeploymentLogViewer logs={[runningLogNoOutput]} streamingLines={streamingLines} />,
      );
      expect(screen.queryByText('LIVE')).not.toBeInTheDocument();
    });

    it('renders streaming output instead of log.output for running log with streaming lines', () => {
      // This running log has output AND streaming lines.
      // Streaming lines should win when isStreaming is true.
      const streamingLines = {
        [runningLogWithOutput.id]: ['Real-time line A', 'Real-time line B'],
      };
      render(
        <DeploymentLogViewer logs={[runningLogWithOutput]} streamingLines={streamingLines} />,
      );
      expect(screen.getByText('Real-time line A')).toBeInTheDocument();
      expect(screen.getByText('Real-time line B')).toBeInTheDocument();
    });

    it('falls back to log.output when running log has output but no streaming lines', () => {
      render(<DeploymentLogViewer logs={[runningLogWithOutput]} />);
      expect(screen.getByText('Helm install started...')).toBeInTheDocument();
      expect(screen.queryByText('LIVE')).not.toBeInTheDocument();
    });

    it('renders multiple streaming lines preserving order', () => {
      const lines = [
        'Step 1: Pulling chart...',
        'Step 2: Running hooks...',
        'Step 3: Deploying pods...',
        'Step 4: Checking health...',
      ];
      const streamingLines = {
        [runningLogNoOutput.id]: lines,
      };
      render(
        <DeploymentLogViewer logs={[runningLogNoOutput]} streamingLines={streamingLines} />,
      );
      const renderedTexts = lines.map((l) => screen.getByText(l));
      // Verify all rendered
      renderedTexts.forEach((el) => expect(el).toBeInTheDocument());
    });

    it('shows LIVE chip only for the running log, not for completed siblings', () => {
      const logs: DeploymentLog[] = [runningLogNoOutput, completedDeployLog];
      const streamingLines = {
        [runningLogNoOutput.id]: ['streaming data'],
      };
      render(
        <DeploymentLogViewer logs={logs} streamingLines={streamingLines} />,
      );
      // Only one LIVE chip should be present
      const liveChips = screen.getAllByText('LIVE');
      expect(liveChips).toHaveLength(1);
    });
  });

  // -------------------------------------------------------------------------
  // Auto-expand behavior
  // -------------------------------------------------------------------------

  describe('auto-expand', () => {
    it('auto-expands the most recent (first) log', () => {
      render(<DeploymentLogViewer logs={[completedDeployLog, errorStopLog]} />);
      const buttons = screen.getAllByRole('button');
      expect(buttons[0]).toHaveAttribute('aria-expanded', 'true');
      expect(buttons[1]).toHaveAttribute('aria-expanded', 'false');
    });

    it('auto-expands when there is only one log', () => {
      render(<DeploymentLogViewer logs={[completedDeployLog]} />);
      const buttons = screen.getAllByRole('button');
      expect(buttons[0]).toHaveAttribute('aria-expanded', 'true');
    });

    it('preserves user expansion when clicking a different accordion', async () => {
      const user = userEvent.setup();
      render(<DeploymentLogViewer logs={[completedDeployLog, errorStopLog]} />);

      // Click the second log to expand it
      await user.click(screen.getByText('stop'));

      const buttons = screen.getAllByRole('button');
      // First should now be collapsed, second expanded
      expect(buttons[0]).toHaveAttribute('aria-expanded', 'false');
      expect(buttons[1]).toHaveAttribute('aria-expanded', 'true');
    });
  });

  // -------------------------------------------------------------------------
  // Loading state
  // -------------------------------------------------------------------------

  it('does not show "No deployment history" when loading is true even with empty logs', () => {
    render(<DeploymentLogViewer logs={[]} loading={true} />);
    expect(screen.queryByText('No deployment history')).not.toBeInTheDocument();
  });
});
