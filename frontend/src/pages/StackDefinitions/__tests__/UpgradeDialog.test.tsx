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

  it('shows warning for charts being removed', async () => {
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
      expect(screen.getByText(/charts marked for removal/i)).toBeInTheDocument();
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
});
