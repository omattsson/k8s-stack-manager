import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { DeployPreviewResponse } from '../../../types';

vi.mock('react-diff-viewer-continued', () => ({
  default: ({ oldValue, newValue, leftTitle, rightTitle }: {
    oldValue: string; newValue: string; leftTitle?: string; rightTitle?: string;
  }) => (
    <div data-testid="diff-viewer">
      <span data-testid="diff-left-title">{leftTitle}</span>
      <span data-testid="diff-right-title">{rightTitle}</span>
      <pre data-testid="diff-old">{oldValue}</pre>
      <pre data-testid="diff-new">{newValue}</pre>
    </div>
  ),
}));

const mockDeployPreview = vi.fn();
vi.mock('../../../api/client', () => ({
  instanceService: {
    deployPreview: (...args: unknown[]) => mockDeployPreview(...args),
  },
}));

vi.mock('../../../context/ThemeContext', () => ({
  useThemeMode: () => ({ mode: 'light', toggleThemeMode: vi.fn() }),
}));

import DeployPreviewDialog from '../index';

const previewWithChanges: DeployPreviewResponse = {
  instance_id: '1',
  instance_name: 'test-stack',
  charts: [
    {
      chart_name: 'backend',
      previous_values: 'replicas: 1\n',
      pending_values: 'replicas: 3\n',
      has_changes: true,
    },
    {
      chart_name: 'frontend',
      previous_values: 'replicas: 2\n',
      pending_values: 'replicas: 2\n',
      has_changes: false,
    },
  ],
};

const previewNoChanges: DeployPreviewResponse = {
  instance_id: '1',
  instance_name: 'test-stack',
  charts: [
    {
      chart_name: 'backend',
      previous_values: 'replicas: 1\n',
      pending_values: 'replicas: 1\n',
      has_changes: false,
    },
  ],
};

describe('DeployPreviewDialog', () => {
  afterEach(() => {
    vi.clearAllMocks();
    vi.restoreAllMocks();
  });

  it('renders loading state when dialog opens', async () => {
    // Never-resolving promise to keep loading state.
    mockDeployPreview.mockReturnValue(new Promise(() => {}));

    render(
      <DeployPreviewDialog
        open
        instanceId={1}
        instanceName="test-stack"
        onConfirm={vi.fn()}
        onClose={vi.fn()}
      />,
    );

    expect(screen.getByRole('progressbar')).toBeInTheDocument();
    expect(screen.getByText('Review Changes — test-stack')).toBeInTheDocument();
  });

  it('shows diff view when data loads with changes', async () => {
    mockDeployPreview.mockResolvedValue(previewWithChanges);

    render(
      <DeployPreviewDialog
        open
        instanceId={1}
        instanceName="test-stack"
        onConfirm={vi.fn()}
        onClose={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(screen.getByTestId('diff-viewer')).toBeInTheDocument();
    });

    // Should show the changed chart name.
    expect(screen.getByText('backend')).toBeInTheDocument();

    // Should show summary chips.
    expect(screen.getByText('1 chart changed')).toBeInTheDocument();
    expect(screen.getByText('1 unchanged')).toBeInTheDocument();

    // Diff viewer should show the values.
    expect(screen.getByTestId('diff-old')).toHaveTextContent('replicas: 1');
    expect(screen.getByTestId('diff-new')).toHaveTextContent('replicas: 3');
  });

  it('shows no-changes message when no charts have changes', async () => {
    mockDeployPreview.mockResolvedValue(previewNoChanges);

    render(
      <DeployPreviewDialog
        open
        instanceId={1}
        instanceName="test-stack"
        onConfirm={vi.fn()}
        onClose={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(
        screen.getByText(/no value changes detected/i),
      ).toBeInTheDocument();
    });

    // Diff viewer should not be present.
    expect(screen.queryByTestId('diff-viewer')).not.toBeInTheDocument();
  });

  it('deploy button calls onConfirm', async () => {
    mockDeployPreview.mockResolvedValue(previewWithChanges);
    const onConfirm = vi.fn();

    render(
      <DeployPreviewDialog
        open
        instanceId={1}
        instanceName="test-stack"
        onConfirm={onConfirm}
        onClose={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(screen.getByTestId('diff-viewer')).toBeInTheDocument();
    });

    const deployButton = screen.getByRole('button', { name: /deploy/i });
    await userEvent.click(deployButton);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it('cancel button calls onClose', async () => {
    mockDeployPreview.mockResolvedValue(previewWithChanges);
    const onClose = vi.fn();

    render(
      <DeployPreviewDialog
        open
        instanceId={1}
        instanceName="test-stack"
        onConfirm={vi.fn()}
        onClose={onClose}
      />,
    );

    await waitFor(() => {
      expect(screen.getByTestId('diff-viewer')).toBeInTheDocument();
    });

    const cancelButton = screen.getByRole('button', { name: /cancel/i });
    await userEvent.click(cancelButton);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('shows error alert when API call fails', async () => {
    mockDeployPreview.mockRejectedValue(new Error('network error'));

    render(
      <DeployPreviewDialog
        open
        instanceId={1}
        instanceName="test-stack"
        onConfirm={vi.fn()}
        onClose={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(screen.getByText('Failed to load deploy preview')).toBeInTheDocument();
    });

    // Deploy button should be disabled on error.
    const deployButton = screen.getByRole('button', { name: /deploy/i });
    expect(deployButton).toBeDisabled();
  });

  it('does not fetch when dialog is closed', () => {
    render(
      <DeployPreviewDialog
        open={false}
        instanceId={1}
        instanceName="test-stack"
        onConfirm={vi.fn()}
        onClose={vi.fn()}
      />,
    );

    expect(mockDeployPreview).not.toHaveBeenCalled();
  });
});
