import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import UpgradeDialog from '../UpgradeDialog';

vi.mock('../../../api/client', () => ({
  definitionService: {
    checkUpgrade: vi.fn(),
    applyUpgrade: vi.fn(),
  },
}));

vi.mock('react-diff-viewer-continued', () => ({
  default: ({ oldValue, newValue, leftTitle, rightTitle }: {
    oldValue: string; newValue: string; leftTitle: string; rightTitle: string;
  }) => (
    <div data-testid="diff-viewer">
      <span>{leftTitle}</span>
      <span>{rightTitle}</span>
      <span>{oldValue}</span>
      <span>{newValue}</span>
    </div>
  ),
  DiffMethod: { LINES: 'diffLines' },
}));

const mockShowSuccess = vi.fn();
const mockShowError = vi.fn();
vi.mock('../../../context/NotificationContext', () => ({
  useNotification: () => ({
    showSuccess: mockShowSuccess,
    showError: mockShowError,
    showWarning: vi.fn(),
    showInfo: vi.fn(),
  }),
}));

import { definitionService } from '../../../api/client';

describe('UpgradeDialog', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows loading spinner while checking', () => {
    (definitionService.checkUpgrade as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <UpgradeDialog definitionId="d1" open={true} onClose={vi.fn()} />
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('shows error alert when check fails', async () => {
    (definitionService.checkUpgrade as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('fail'));
    render(
      <UpgradeDialog definitionId="d1" open={true} onClose={vi.fn()} />
    );
    await waitFor(() => {
      expect(screen.getByText('Failed to check for upgrades')).toBeInTheDocument();
    });
  });

  it('shows no upgrade available message', async () => {
    (definitionService.checkUpgrade as ReturnType<typeof vi.fn>).mockResolvedValue({
      upgrade_available: false,
    });
    render(
      <UpgradeDialog definitionId="d1" open={true} onClose={vi.fn()} />
    );
    await waitFor(() => {
      expect(screen.getByText(/latest version/i)).toBeInTheDocument();
    });
  });

  it('shows upgrade details when available', async () => {
    (definitionService.checkUpgrade as ReturnType<typeof vi.fn>).mockResolvedValue({
      upgrade_available: true,
      current_version: '1.0',
      latest_version: '2.0',
      changes: {
        charts_added: ['monitoring'],
        charts_removed: [],
        charts_modified: ['frontend'],
        charts_unchanged: ['backend'],
      },
    });
    render(
      <UpgradeDialog definitionId="d1" open={true} onClose={vi.fn()} />
    );
    await waitFor(() => {
      expect(screen.getByText('v1.0')).toBeInTheDocument();
      expect(screen.getByText('v2.0')).toBeInTheDocument();
    });
    expect(screen.getByText('monitoring')).toBeInTheDocument();
    expect(screen.getByText('frontend')).toBeInTheDocument();
    expect(screen.getByText('backend')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /upgrade/i })).toBeInTheDocument();
  });

  it('applies upgrade and calls onUpgraded', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const onUpgraded = vi.fn();

    (definitionService.checkUpgrade as ReturnType<typeof vi.fn>).mockResolvedValue({
      upgrade_available: true,
      current_version: '1.0',
      latest_version: '2.0',
      changes: {
        charts_added: [],
        charts_removed: [],
        charts_modified: ['frontend'],
        charts_unchanged: [],
      },
    });
    (definitionService.applyUpgrade as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: 'd1',
      name: 'My Stack',
    });

    render(
      <UpgradeDialog definitionId="d1" open={true} onClose={onClose} onUpgraded={onUpgraded} />
    );

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /^upgrade$/i })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /^upgrade$/i }));

    await waitFor(() => {
      expect(definitionService.applyUpgrade).toHaveBeenCalledWith('d1');
      expect(onUpgraded).toHaveBeenCalled();
      expect(onClose).toHaveBeenCalled();
    });
  });

  it('explains that charts not in the template are kept', async () => {
    (definitionService.checkUpgrade as ReturnType<typeof vi.fn>).mockResolvedValue({
      upgrade_available: true,
      current_version: '1.0',
      latest_version: '2.0',
      changes: {
        charts_added: [],
        charts_removed: ['old-service'],
        charts_modified: [],
        charts_unchanged: [],
      },
    });
    render(
      <UpgradeDialog definitionId="d1" open={true} onClose={vi.fn()} />
    );
    await waitFor(() => {
      expect(screen.getByText('old-service')).toBeInTheDocument();
      expect(screen.getByText(/stay in your definition/i)).toBeInTheDocument();
      expect(screen.getByText('Not in template')).toBeInTheDocument();
    });
  });

  it('shows error notification when apply upgrade fails', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();

    (definitionService.checkUpgrade as ReturnType<typeof vi.fn>).mockResolvedValue({
      upgrade_available: true,
      current_version: '1.0',
      latest_version: '2.0',
      changes: {
        charts_added: [],
        charts_removed: [],
        charts_modified: ['frontend'],
        charts_unchanged: [],
      },
    });
    (definitionService.applyUpgrade as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Server error'));

    render(
      <UpgradeDialog definitionId="d1" open={true} onClose={onClose} />
    );

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /^upgrade$/i })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /^upgrade$/i }));

    await waitFor(() => {
      expect(definitionService.applyUpgrade).toHaveBeenCalledWith('d1');
      expect(mockShowError).toHaveBeenCalledWith('Failed to apply upgrade');
    });

    // onClose should NOT have been called on failure
    expect(onClose).not.toHaveBeenCalled();
  });

  it('calls onClose when Cancel button is clicked', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();

    (definitionService.checkUpgrade as ReturnType<typeof vi.fn>).mockResolvedValue({
      upgrade_available: true,
      current_version: '1.0',
      latest_version: '2.0',
      changes: {
        charts_added: [],
        charts_removed: [],
        charts_modified: ['frontend'],
        charts_unchanged: [],
      },
    });

    render(
      <UpgradeDialog definitionId="d1" open={true} onClose={onClose} />
    );

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /cancel/i })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /cancel/i }));

    expect(onClose).toHaveBeenCalled();
  });

  const upgradeResult = {
    upgrade_available: true,
    current_version: '1.0',
    latest_version: '2.0',
    changes: {
      charts_added: ['monitoring'],
      charts_removed: [],
      charts_modified: ['frontend'],
      charts_unchanged: ['backend'],
    },
  };

  it('shows the value diff per chart from chart_diffs', async () => {
    (definitionService.checkUpgrade as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...upgradeResult,
      chart_diffs: [
        { chart_name: 'frontend', left_values: 'replicas: 1', right_values: 'replicas: 3', has_differences: true, change_type: 'modified' },
        { chart_name: 'backend', left_values: 'port: 80', right_values: 'port: 80', has_differences: false, change_type: 'unchanged' },
        { chart_name: 'monitoring', left_values: '', right_values: 'enabled: true', has_differences: true, change_type: 'added' },
      ],
    });

    render(<UpgradeDialog definitionId="d1" open={true} onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByText('replicas: 3')).toBeInTheDocument();
    });
    expect(screen.getByText('replicas: 1')).toBeInTheDocument();
    expect(screen.getByText('enabled: true')).toBeInTheDocument();
    expect(screen.getAllByText('Current definition').length).toBe(2);
    // Unchanged charts are not shown as a diff.
    expect(screen.queryByText('port: 80')).not.toBeInTheDocument();
  });

  it('shows removed charts as kept, not as deleted lines', async () => {
    (definitionService.checkUpgrade as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...upgradeResult,
      chart_diffs: [
        { chart_name: 'legacy', left_values: 'old: 1', has_differences: true, change_type: 'removed' },
      ],
    });

    render(<UpgradeDialog definitionId="d1" open={true} onClose={vi.fn()} />);

    expect(await screen.findByText('Kept (not in the new template version).')).toBeInTheDocument();
    expect(screen.queryByText('old: 1')).not.toBeInTheDocument();
  });

  it('shows an empty state when chart_diffs is missing', async () => {
    (definitionService.checkUpgrade as ReturnType<typeof vi.fn>).mockResolvedValue(upgradeResult);

    render(<UpgradeDialog definitionId="d1" open={true} onClose={vi.fn()} />);

    expect(await screen.findByText('No chart value changes.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^upgrade$/i })).toBeInTheDocument();
  });

  it('explains a 409 for a template without a published version on apply', async () => {
    const user = userEvent.setup();
    (definitionService.checkUpgrade as ReturnType<typeof vi.fn>).mockResolvedValue(upgradeResult);
    (definitionService.applyUpgrade as ReturnType<typeof vi.fn>).mockRejectedValue({
      response: { status: 409, data: { error: 'Template has no published version' } },
    });

    render(<UpgradeDialog definitionId="d1" open={true} onClose={vi.fn()} />);
    await user.click(await screen.findByRole('button', { name: /^upgrade$/i }));

    await waitFor(() => {
      expect(mockShowError).toHaveBeenCalledWith(expect.stringMatching(/has no published version/));
    });
  });
});
