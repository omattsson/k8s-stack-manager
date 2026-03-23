import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import QuotaConfigDialog from '../index';
import { NotificationProvider } from '../../../context/NotificationContext';

vi.mock('../../../api/client', () => ({
  clusterService: {
    getQuotas: vi.fn(),
    updateQuotas: vi.fn(),
    deleteQuotas: vi.fn(),
  },
}));

import { clusterService } from '../../../api/client';

const mockQuota = {
  id: 'q1',
  cluster_id: 'c1',
  cpu_request: '500m',
  cpu_limit: '2000m',
  memory_request: '256Mi',
  memory_limit: '1Gi',
  storage_limit: '10Gi',
  pod_limit: 50,
};

const renderDialog = (open = true) =>
  render(
    <NotificationProvider>
      <QuotaConfigDialog
        open={open}
        onClose={vi.fn()}
        clusterId="c1"
        clusterName="production"
      />
    </NotificationProvider>,
  );

describe('QuotaConfigDialog', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows loading spinner while fetching quotas', () => {
    (clusterService.getQuotas as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));

    renderDialog();

    expect(screen.getByRole('progressbar')).toBeInTheDocument();
    expect(screen.getByText('Resource Quotas for production')).toBeInTheDocument();
  });

  it('shows empty state when no quotas configured', async () => {
    (clusterService.getQuotas as ReturnType<typeof vi.fn>).mockResolvedValue(null);

    renderDialog();

    await waitFor(() => {
      expect(screen.getByText(/No quotas configured/)).toBeInTheDocument();
    });
  });

  it('loads and displays existing quota configuration', async () => {
    (clusterService.getQuotas as ReturnType<typeof vi.fn>).mockResolvedValue(mockQuota);

    renderDialog();

    await waitFor(() => {
      expect(screen.getByDisplayValue('500m')).toBeInTheDocument();
    });
    expect(screen.getByDisplayValue('2000m')).toBeInTheDocument();
    expect(screen.getByDisplayValue('256Mi')).toBeInTheDocument();
    expect(screen.getByDisplayValue('1Gi')).toBeInTheDocument();
    expect(screen.getByDisplayValue('10Gi')).toBeInTheDocument();
    expect(screen.getByDisplayValue('50')).toBeInTheDocument();
    // Remove Quotas button visible when quotas exist
    expect(screen.getByRole('button', { name: /remove quotas/i })).toBeInTheDocument();
  });

  it('shows error when fetch fails', async () => {
    (clusterService.getQuotas as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('fail'));

    renderDialog();

    await waitFor(() => {
      expect(screen.getByText('Failed to load quota configuration')).toBeInTheDocument();
    });
  });

  it('saves quota configuration', async () => {
    const user = userEvent.setup();
    (clusterService.getQuotas as ReturnType<typeof vi.fn>).mockResolvedValue(null);
    (clusterService.updateQuotas as ReturnType<typeof vi.fn>).mockResolvedValue(mockQuota);

    const onClose = vi.fn();
    render(
      <NotificationProvider>
        <QuotaConfigDialog
          open={true}
          onClose={onClose}
          clusterId="c1"
          clusterName="production"
        />
      </NotificationProvider>,
    );

    await waitFor(() => {
      expect(screen.getByText(/No quotas configured/)).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() => {
      expect(clusterService.updateQuotas).toHaveBeenCalledWith('c1', expect.objectContaining({
        cluster_id: 'c1',
      }));
    });
    expect(onClose).toHaveBeenCalled();
  });

  it('deletes quota configuration', async () => {
    const user = userEvent.setup();
    (clusterService.getQuotas as ReturnType<typeof vi.fn>).mockResolvedValue(mockQuota);
    (clusterService.deleteQuotas as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);

    const onClose = vi.fn();
    render(
      <NotificationProvider>
        <QuotaConfigDialog
          open={true}
          onClose={onClose}
          clusterId="c1"
          clusterName="production"
        />
      </NotificationProvider>,
    );

    await waitFor(() => {
      expect(screen.getByDisplayValue('500m')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /remove quotas/i }));

    await waitFor(() => {
      expect(clusterService.deleteQuotas).toHaveBeenCalledWith('c1');
    });
    expect(onClose).toHaveBeenCalled();
  });

  it('shows error when save fails', async () => {
    const user = userEvent.setup();
    (clusterService.getQuotas as ReturnType<typeof vi.fn>).mockResolvedValue(null);
    (clusterService.updateQuotas as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Server error'));

    const onClose = vi.fn();
    render(
      <NotificationProvider>
        <QuotaConfigDialog
          open={true}
          onClose={onClose}
          clusterId="c1"
          clusterName="production"
        />
      </NotificationProvider>,
    );

    await waitFor(() => {
      expect(screen.getByText(/No quotas configured/)).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() => {
      expect(clusterService.updateQuotas).toHaveBeenCalledWith('c1', expect.objectContaining({
        cluster_id: 'c1',
      }));
    });

    // Error message should appear in the dialog
    await waitFor(() => {
      expect(screen.getByText('Failed to save quota configuration')).toBeInTheDocument();
    });

    // Dialog should NOT have been closed on failure
    expect(onClose).not.toHaveBeenCalled();
  });

  it('shows error when delete fails', async () => {
    const user = userEvent.setup();
    (clusterService.getQuotas as ReturnType<typeof vi.fn>).mockResolvedValue(mockQuota);
    (clusterService.deleteQuotas as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Server error'));

    const onClose = vi.fn();
    render(
      <NotificationProvider>
        <QuotaConfigDialog
          open={true}
          onClose={onClose}
          clusterId="c1"
          clusterName="production"
        />
      </NotificationProvider>,
    );

    await waitFor(() => {
      expect(screen.getByDisplayValue('500m')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /remove quotas/i }));

    await waitFor(() => {
      expect(clusterService.deleteQuotas).toHaveBeenCalledWith('c1');
    });

    // Error message should appear in the dialog
    await waitFor(() => {
      expect(screen.getByText('Failed to remove quota configuration')).toBeInTheDocument();
    });

    // Dialog should NOT have been closed on failure
    expect(onClose).not.toHaveBeenCalled();
  });
});
