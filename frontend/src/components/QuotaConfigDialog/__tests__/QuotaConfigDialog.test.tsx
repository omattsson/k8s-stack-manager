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

    // Error message should appear (in dialog and/or toast notification)
    await waitFor(() => {
      expect(screen.getAllByText('Failed to save quota configuration').length).toBeGreaterThanOrEqual(1);
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

    // Error message should appear (in dialog and/or toast notification)
    await waitFor(() => {
      expect(screen.getAllByText('Failed to remove quota configuration').length).toBeGreaterThanOrEqual(1);
    });

    // Dialog should NOT have been closed on failure
    expect(onClose).not.toHaveBeenCalled();
  });

  describe('validation', () => {
    const fieldByLabel = (label: string) => screen.getByRole('textbox', { name: label });

    const renderEmpty = async () => {
      (clusterService.getQuotas as ReturnType<typeof vi.fn>).mockResolvedValue(null);
      renderDialog();
      await waitFor(() => {
        expect(screen.getByText(/No quotas configured/)).toBeInTheDocument();
      });
    };

    it('shows a format error on blur and disables Save for an invalid quantity', async () => {
      const user = userEvent.setup();
      await renderEmpty();

      await user.type(fieldByLabel('Memory Limit'), 'abc');
      expect(screen.queryByText(/Invalid quantity/)).not.toBeInTheDocument();
      await user.tab();

      expect(screen.getByText(/Invalid quantity\. Use a memory quantity/)).toBeInTheDocument();
      expect(fieldByLabel('Memory Limit')).toHaveAttribute('aria-invalid', 'true');
      expect(screen.getByRole('button', { name: /^save$/i })).toBeDisabled();
      expect(clusterService.updateQuotas).not.toHaveBeenCalled();
    });

    it('rejects a negative quantity', async () => {
      const user = userEvent.setup();
      await renderEmpty();

      await user.type(fieldByLabel('Storage Limit'), '-1Gi');
      await user.tab();

      expect(screen.getByText('Must not be negative')).toBeInTheDocument();
      expect(screen.getByRole('button', { name: /^save$/i })).toBeDisabled();
    });

    it('rejects a CPU request higher than the CPU limit', async () => {
      const user = userEvent.setup();
      await renderEmpty();

      await user.type(fieldByLabel('CPU Request'), '20');
      await user.type(fieldByLabel('CPU Limit'), '16');
      await user.tab();

      expect(screen.getByText('CPU request must not be higher than the CPU limit')).toBeInTheDocument();
      expect(screen.getByRole('button', { name: /^save$/i })).toBeDisabled();
    });

    it('compares memory request and limit across units', async () => {
      const user = userEvent.setup();
      await renderEmpty();

      // 1Gi = 1024Mi, which is higher than 1000Mi.
      await user.type(fieldByLabel('Memory Request'), '1Gi');
      await user.type(fieldByLabel('Memory Limit'), '1000Mi');
      await user.tab();

      expect(screen.getByText('Memory request must not be higher than the memory limit')).toBeInTheDocument();

      await user.clear(fieldByLabel('Memory Limit'));
      await user.type(fieldByLabel('Memory Limit'), '2G');
      await user.tab();

      expect(screen.queryByText('Memory request must not be higher than the memory limit')).not.toBeInTheDocument();
      expect(screen.getByRole('button', { name: /^save$/i })).toBeEnabled();
    });

    it('saves valid quantities in mixed units', async () => {
      const user = userEvent.setup();
      (clusterService.updateQuotas as ReturnType<typeof vi.fn>).mockResolvedValue(mockQuota);
      await renderEmpty();

      await user.type(fieldByLabel('CPU Request'), '500m');
      await user.type(fieldByLabel('CPU Limit'), '1.5');
      await user.type(fieldByLabel('Memory Request'), '1G');
      await user.type(fieldByLabel('Memory Limit'), '1Gi');
      await user.type(fieldByLabel('Storage Limit'), '1e10');
      await user.click(screen.getByRole('button', { name: /^save$/i }));

      await waitFor(() => {
        expect(clusterService.updateQuotas).toHaveBeenCalledWith('c1', expect.objectContaining({
          cpu_request: '500m',
          cpu_limit: '1.5',
          memory_request: '1G',
          memory_limit: '1Gi',
          storage_limit: '1e10',
        }));
      });
    });

    it('trims whitespace before validation and in the payload', async () => {
      const user = userEvent.setup();
      (clusterService.updateQuotas as ReturnType<typeof vi.fn>).mockResolvedValue(mockQuota);
      await renderEmpty();

      await user.type(fieldByLabel('CPU Request'), ' 500m ');
      await user.type(fieldByLabel('Memory Limit'), ' 1Gi');
      await user.tab();

      expect(screen.queryByText(/Invalid quantity/)).not.toBeInTheDocument();
      await user.click(screen.getByRole('button', { name: /^save$/i }));

      await waitFor(() => {
        expect(clusterService.updateQuotas).toHaveBeenCalledWith('c1', expect.objectContaining({
          cpu_request: '500m',
          memory_limit: '1Gi',
        }));
      });
    });

    it('shows errors for invalid stored values right after load', async () => {
      (clusterService.getQuotas as ReturnType<typeof vi.fn>).mockResolvedValue({
        ...mockQuota,
        memory_limit: 'abc',
        cpu_request: '20',
        cpu_limit: '16',
      });
      renderDialog();

      await waitFor(() => {
        expect(screen.getByDisplayValue('abc')).toBeInTheDocument();
      });

      expect(screen.getByText(/Invalid quantity\. Use a memory quantity/)).toBeInTheDocument();
      expect(screen.getByText('CPU request must not be higher than the CPU limit')).toBeInTheDocument();
      expect(screen.getByRole('button', { name: /^save$/i })).toBeDisabled();
      // Valid stored values show their normal help text.
      expect(screen.getByText('Maximum storage per namespace (e.g., 10Gi, 50Gi)')).toBeInTheDocument();
    });

    it('shows the server message when the backend rejects the quotas', async () => {
      const user = userEvent.setup();
      (clusterService.updateQuotas as ReturnType<typeof vi.fn>).mockRejectedValue({
        response: { status: 400, statusText: 'Bad Request', data: { error: 'memory_limit: invalid quantity' } },
      });
      await renderEmpty();

      await user.type(fieldByLabel('Memory Limit'), '1Gi');
      await user.click(screen.getByRole('button', { name: /^save$/i }));

      await waitFor(() => {
        expect(
          screen.getAllByText('Failed to save quota configuration (HTTP 400: memory_limit: invalid quantity)').length,
        ).toBeGreaterThanOrEqual(1);
      });
    });
  });
});
